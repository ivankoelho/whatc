package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Investigation of the report "an agent who logs in receives past contacts as if they were never attended".
// Findings, each pinned below:
//   - logging in (or reconnecting) writes nothing: no code path assigns on presence, and the rows are unchanged;
//   - distribution only ever picks an agent who is active, available AND connected; with nobody eligible the
//     attendance stays in the team queue, unassigned;
//   - the cause of the symptom was the "new" counter/tab: "new" only ends when an agent replies, so every customer
//     the bot served (or who gave up) weeks ago stayed "new" and was counted for the whole team.

type presenceWorld struct {
	app     *handlers.App
	org     *models.Organization
	team    *models.Team
	account *models.WhatsAppAccount
	agents  map[string]*models.User
	online  map[uuid.UUID]bool
}

func newPresenceWorld(t *testing.T) *presenceWorld {
	t.Helper()
	w := &presenceWorld{agents: map[string]*models.User{}, online: map[uuid.UUID]bool{}}
	w.app = newTestApp(t, withPresence(&w.online))
	w.org = testutil.CreateTestOrganization(t, w.app.DB)
	w.account = testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	var ids []uuid.UUID
	for _, n := range []string{"A", "B", "C", "D"} {
		u := createTestAgent(t, w.app, w.org.ID)
		w.agents[n] = u
		ids = append(ids, u.ID)
	}
	w.team = createTestTeam(t, w.app, w.org.ID, ids...)
	require.NoError(t, w.app.DB.Model(w.account).Update("default_team_id", w.team.ID).Error)
	return w
}

func (w *presenceWorld) setAvailable(t *testing.T, u *models.User, available bool) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(&models.User{}).Where("id = ?", u.ID).Update("is_available", available).Error)
}

func (w *presenceWorld) contact(t *testing.T, status models.ContactStatus, incoming int, ago time.Duration) *models.Contact {
	t.Helper()
	c := testutil.CreateTestContactWith(t, w.app.DB, w.org.ID, testutil.WithContactAccount(w.account.Name))
	last := time.Now().Add(-ago)
	require.NoError(t, w.app.DB.Model(c).Updates(map[string]any{"contact_status": status, "last_message_at": last, "last_inbound_at": last}).Error)
	for i := 0; i < incoming; i++ {
		require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, ContactID: c.ID,
			Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Status: models.MessageStatusDelivered}).Error)
	}
	return c
}

func (w *presenceWorld) transfer(t *testing.T, c *models.Contact, status models.TransferStatus, agent *models.User, team *models.Team, ago time.Duration) {
	t.Helper()
	tr := &models.AgentTransfer{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, ContactID: c.ID, WhatsAppAccount: w.account.Name,
		PhoneNumber: "+550000", Status: status, Source: models.TransferSourceManual, TransferredAt: time.Now().Add(-ago)}
	if agent != nil {
		tr.AgentID = &agent.ID
	}
	if team != nil {
		tr.TeamID = &team.ID
	}
	require.NoError(t, w.app.DB.Create(tr).Error)
}

func (w *presenceWorld) listed(t *testing.T, u *models.User, query map[string]string) map[uuid.UUID]models.ContactStatus {
	t.Helper()
	if query == nil {
		query = map[string]string{}
	}
	query["limit"] = "100"
	code, raw := chatDo(t, w.app.ListContacts, w.org.ID, u.ID, nil, nil, query)
	require.Equal(t, fasthttp.StatusOK, code)
	d := decodeChat[struct {
		Contacts []struct {
			ID            uuid.UUID            `json:"id"`
			ContactStatus models.ContactStatus `json:"contact_status"`
		} `json:"contacts"`
	}](t, raw)
	out := map[uuid.UUID]models.ContactStatus{}
	for _, c := range d.Contacts {
		out[c.ID] = c.ContactStatus
	}
	return out
}

func (w *presenceWorld) newBadge(t *testing.T, u *models.User) int {
	t.Helper()
	code, raw := chatDo(t, w.app.GetContactStatusCounts, w.org.ID, u.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	return decodeChat[struct {
		New int `json:"new"`
	}](t, raw).New
}

// An agent who just logged in: the customer waiting in the queue is counted and listed as new; the history is
// listed with its true status but is not counted as waiting; looking changes nothing.
func TestPresence_LoginDoesNotPresentHistoryAsNewWork(t *testing.T) {
	w := newPresenceWorld(t)
	a, d := w.agents["A"], w.agents["D"]

	cMine := w.contact(t, models.ContactStatusInProgress, 2, time.Minute)
	w.transfer(t, cMine, models.TransferStatusActive, a, w.team, time.Minute)
	cQueue := w.contact(t, models.ContactStatusNew, 1, 5*time.Minute)
	w.transfer(t, cQueue, models.TransferStatusActive, nil, w.team, 5*time.Minute)
	cClosed := w.contact(t, models.ContactStatusResolved, 3, 40*24*time.Hour)
	w.transfer(t, cClosed, models.TransferStatusResumed, a, w.team, 40*24*time.Hour)
	cBot := w.contact(t, models.ContactStatusNew, 4, 20*24*time.Hour) // the bot answered, no person ever replied
	cFresh := w.contact(t, models.ContactStatusNew, 1, time.Hour)     // wrote an hour ago, nobody has taken it yet

	var before int64
	w.app.DB.Model(&models.AgentTransfer{}).Where("organization_id = ? AND updated_at IS NOT NULL", w.org.ID).Count(&before)
	w.online[d.ID] = true // D logs in

	all := w.listed(t, d, nil)
	assert.Contains(t, all, cQueue.ID)
	assert.NotContains(t, all, cMine.ID, "A's attendance is not D's")
	assert.Equal(t, models.ContactStatusResolved, all[cClosed.ID], "a finished attendance keeps its real status")

	news := w.listed(t, d, map[string]string{"status": "new"})
	assert.Contains(t, news, cQueue.ID, "waiting in the team queue")
	assert.Contains(t, news, cFresh.ID, "wrote within the service window")
	assert.NotContains(t, news, cBot.ID, "the bot-only customer of 20 days ago is not waiting for a person")
	assert.Equal(t, 2, w.newBadge(t, d), "the badge counts only what is waiting")

	var n int64
	w.app.DB.Model(&models.AgentTransfer{}).Where("organization_id = ? AND agent_id = ? AND status = ?", w.org.ID, d.ID, models.TransferStatusActive).Count(&n)
	assert.Zero(t, n, "logging in assigned nothing to D")
	var after int64
	w.app.DB.Model(&models.AgentTransfer{}).Where("organization_id = ? AND updated_at IS NOT NULL", w.org.ID).Count(&after)
	assert.Equal(t, before, after)
}

// Two or more agents in different states: only the one who is active, available and connected receives; with
// nobody eligible the attendance stays in the team queue, and reconnecting later does not assign it by itself.
func TestPresence_OnlyTheEligibleAgentReceives_AndAbsenceKeepsItInTheQueue(t *testing.T) {
	for _, strategy := range []models.AssignmentStrategy{models.AssignmentStrategyRoundRobin, models.AssignmentStrategyLoadBalanced} {
		t.Run(string(strategy), func(t *testing.T) {
			w := newPresenceWorld(t)
			require.NoError(t, w.app.DB.Model(&models.Team{}).Where("id = ?", w.team.ID).Update("assignment_strategy", strategy).Error)
			w.app.Assigner.InvalidateTeamCache(w.team.ID)
			a, b, c, d := w.agents["A"], w.agents["B"], w.agents["C"], w.agents["D"]

			w.online[a.ID], w.online[b.ID], w.online[c.ID] = true, true, true // D is not connected
			w.setAvailable(t, b, false)                                       // B is away
			require.NoError(t, w.app.DB.Model(&models.User{}).Where("id = ?", c.ID).Update("is_active", false).Error)

			for range 5 {
				got := w.app.Assigner.AssignToTeam(w.team.ID, w.org.ID, nil, assignment.ChatLoadCounter)
				require.NotNil(t, got)
				assert.Equal(t, a.ID, *got, "A is the only active, available and connected agent")
			}

			// everybody gone: nobody receives, the attendance would wait unassigned
			w.online[a.ID] = false
			assert.Nil(t, w.app.Assigner.AssignToTeam(w.team.ID, w.org.ID, nil, assignment.ChatLoadCounter))

			// a queued attendance exists; D and A reconnect: nothing is assigned by presence
			cq := w.contact(t, models.ContactStatusNew, 1, time.Hour)
			w.transfer(t, cq, models.TransferStatusActive, nil, w.team, time.Hour)
			w.online[d.ID], w.online[a.ID] = true, true
			var assigned int64
			w.app.DB.Model(&models.AgentTransfer{}).Where("organization_id = ? AND status = ? AND agent_id IS NOT NULL", w.org.ID, models.TransferStatusActive).Count(&assigned)
			assert.Zero(t, assigned, "reconnecting does not assign the queue")
		})
	}
}
