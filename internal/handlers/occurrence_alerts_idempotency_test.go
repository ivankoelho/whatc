package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type alertWorld struct {
	app           *handlers.App
	org           *models.Organization
	sup, ana, bob *models.User
	contact       *models.Contact
}

func newAlertWorld(t *testing.T) *alertWorld {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	supRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "occ-supervisor",
		[]string{"chat:read", "chat:write", "occurrences:read", "occurrences:write", "occurrences:view_all", "conversations:view_all"})
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	return &alertWorld{app: app, org: org,
		sup:     testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&supRole.ID)),
		ana:     testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID)),
		bob:     testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID)),
		contact: testutil.CreateTestContact(t, app.DB, org.ID)}
}

func (w *alertWorld) update(t *testing.T, occ, assignee uuid.UUID) {
	t.Helper()
	req := authedJSON(t, w.app, w.org.ID, w.sup.ID, "PUT", occ, map[string]any{"title": "t", "assigned_user_id": assignee.String()})
	require.NoError(t, w.app.UpdateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

func (w *alertWorld) reassign(t *testing.T, occ, to uuid.UUID) {
	t.Helper()
	req := authedJSON(t, w.app, w.org.ID, w.sup.ID, "POST", occ, map[string]any{"assigned_user_id": to.String()})
	require.NoError(t, w.app.ReassignOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
}

func (w *alertWorld) alerts(t *testing.T, u uuid.UUID) []handlers.OccurrenceAlertItem {
	return listAlerts(t, w.app, w.org.ID, u).Data.Alerts
}

// Repeating the same event must not stack alerts or resurrect a seen one.
func TestOccurrenceAlerts_RepeatedAssignmentIsIdempotent(t *testing.T) {
	w := newAlertWorld(t)
	id := createOcc(t, w.app, w.org.ID, w.sup.ID, w.contact.ID, map[string]any{"title": "rep", "assigned_user_id": w.ana.ID.String()})
	require.Len(t, w.alerts(t, w.ana.ID), 1)

	// PUT with the assignee she already has: nothing changed, nothing new.
	w.update(t, id, w.ana.ID)
	w.update(t, id, w.ana.ID)
	assert.Len(t, w.alerts(t, w.ana.ID), 1, "same assignee again is not a new notification")

	// Hand it to Bob, then to Bob again through the redistribute endpoint.
	w.update(t, id, w.bob.ID)
	w.reassign(t, id, w.bob.ID)
	w.reassign(t, id, w.bob.ID)
	assert.Len(t, w.alerts(t, w.bob.ID), 1, "repeated hand-over to the same person stays one alert")
	assert.Empty(t, w.alerts(t, w.ana.ID), "the previous owner stops receiving it")

	// Seen stays seen: a repeated hand-over to the current owner does not re-notify.
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, w.org.ID, w.bob.ID)
	require.NoError(t, w.app.MarkOccurrenceAlertsSeen(req))
	w.reassign(t, id, w.bob.ID)
	got := listAlerts(t, w.app, w.org.ID, w.bob.ID)
	assert.Len(t, got.Data.Alerts, 1)
	assert.Equal(t, 0, got.Data.Unseen, "no new news, no new badge")

	// Handing it back to Ana is real news and notifies her once.
	w.reassign(t, id, w.ana.ID)
	assert.Len(t, w.alerts(t, w.ana.ID), 1)
	assert.Empty(t, w.alerts(t, w.bob.ID))
}

// A concluded protocol is no longer pending. Reopening it brings back what was
// never dismissed (a pending alert is the person's open to-do, not a log).
func TestOccurrenceAlerts_ConcludedProtocolLeavesThePendingList(t *testing.T) {
	w := newAlertWorld(t)
	id := createOcc(t, w.app, w.org.ID, w.sup.ID, w.contact.ID, map[string]any{"title": "fecha", "assigned_user_id": w.ana.ID.String()})
	require.Len(t, w.alerts(t, w.ana.ID), 1)

	var closing, initial models.OccurrenceStage
	require.NoError(t, w.app.DB.Where("organization_id = ? AND is_closing = ?", w.org.ID, true).First(&closing).Error)
	require.NoError(t, w.app.DB.Where("organization_id = ? AND is_closing = ?", w.org.ID, false).Order("position").First(&initial).Error)

	stage := func(to models.OccurrenceStage) {
		req := authedJSON(t, w.app, w.org.ID, w.sup.ID, "PUT", id, map[string]any{"stage_id": to.ID.String()})
		require.NoError(t, w.app.ChangeOccurrenceStage(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	}
	stage(closing)
	assert.Empty(t, w.alerts(t, w.ana.ID), "concluded protocols leave the pending list")

	stage(initial)
	assert.Len(t, w.alerts(t, w.ana.ID), 1, "reopened: the never-dismissed alert is pending again")
}

// occurrence_alert must reach a client only after occurrence_changed, so a client
// that refetches on the alert finds the case already in its new state.
func TestOccurrenceAlerts_ArriveAfterOccurrenceChanged(t *testing.T) {
	w := newAlertWorld(t)
	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	hub.SetOccurrenceAuthorizer(func(_, _, _ uuid.UUID) bool { return true })
	hub.SetConversationAuthorizer(func(_, _, _ uuid.UUID) bool { return true })
	client := websocket.NewClient(hub, nil, w.bob.ID, w.org.ID)
	hub.Register(client)
	for d := time.Now().Add(2 * time.Second); time.Now().Before(d) && hub.GetClientCount() != 1; {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 1, hub.GetClientCount())
	w.app.WSHub = hub

	// next returns the next relevant message type, skipping the counters hint.
	next := func() string {
		for {
			select {
			case data := <-client.SendChan():
				var m websocket.WSMessage
				require.NoError(t, json.Unmarshal(data, &m))
				if m.Type != websocket.TypeContactCountsChanged {
					return m.Type
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out")
				return ""
			}
		}
	}
	drain := func() {
		for {
			select {
			case <-client.SendChan():
			case <-time.After(150 * time.Millisecond):
				return
			}
		}
	}

	createOcc(t, w.app, w.org.ID, w.sup.ID, w.contact.ID, map[string]any{"title": "ordem", "assigned_user_id": w.bob.ID.String()})
	assert.Equal(t, websocket.TypeOccurrenceChanged, next(), "create: changed first")
	assert.Equal(t, websocket.TypeOccurrenceAlert, next(), "create: alert second")

	id := createOcc(t, w.app, w.org.ID, w.sup.ID, w.contact.ID, map[string]any{"title": "ordem2", "assigned_user_id": w.ana.ID.String()})
	drain()
	w.update(t, id, w.bob.ID)
	assert.Equal(t, websocket.TypeOccurrenceChanged, next(), "update: changed first")
	assert.Equal(t, websocket.TypeOccurrenceAlert, next(), "update: alert second")

	w.reassign(t, id, w.ana.ID)
	drain()
	w.reassign(t, id, w.bob.ID)
	assert.Equal(t, websocket.TypeOccurrenceChanged, next(), "reassign: changed first")
	assert.Equal(t, websocket.TypeOccurrenceAlert, next(), "reassign: alert second")
}
