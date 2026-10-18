package handlers_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func presenceRows(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) []models.AgentPresenceLog {
	t.Helper()
	var rows []models.AgentPresenceLog
	require.NoError(t, app.DB.Where("organization_id = ? AND user_id = ?", orgID, userID).
		Order("started_at ASC").Find(&rows).Error)
	return rows
}

func openRows(rows []models.AgentPresenceLog) (n int) {
	for _, r := range rows {
		if r.EndedAt == nil {
			n++
		}
	}
	return n
}

func setAway(t *testing.T, app *handlers.App, orgID, userID uuid.UUID, away bool) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"is_available": !away})
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.UpdateAvailability(req))
}

// Every state change is recorded with the instant it really happened, the
// intervals are contiguous and exactly one stays open.
func TestPresenceLog_RecordsEachTransitionWithItsRealInstant(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, org.ID)

	online[agent.ID] = true
	t0 := time.Now()
	app.RecordPresence(org.ID, agent.ID) // online
	time.Sleep(15 * time.Millisecond)
	setAway(t, app, org.ID, agent.ID, true) // away
	time.Sleep(15 * time.Millisecond)
	setAway(t, app, org.ID, agent.ID, false) // online again
	time.Sleep(15 * time.Millisecond)
	online[agent.ID] = false
	app.RecordPresence(org.ID, agent.ID) // offline
	t1 := time.Now()

	rows := presenceRows(t, app, org.ID, agent.ID)
	require.Len(t, rows, 4)
	want := []models.AgentState{models.AgentStateOnline, models.AgentStateAway, models.AgentStateOnline, models.AgentStateOffline}
	for i, r := range rows {
		assert.Equal(t, want[i], r.State, "interval %d", i)
		assert.False(t, r.StartedAt.Before(t0.Add(-time.Millisecond)) || r.StartedAt.After(t1.Add(time.Millisecond)),
			"interval %d starts when the change really happened", i)
		if i < len(rows)-1 {
			require.NotNil(t, r.EndedAt, "interval %d is closed", i)
			assert.True(t, r.EndedAt.Equal(rows[i+1].StartedAt), "interval %d ends exactly where the next begins", i)
			assert.Equal(t, models.PresenceClosedTransition, r.ClosedReason)
		}
	}
	assert.Equal(t, 1, openRows(rows), "exactly one open interval: the current state")
	assert.Nil(t, rows[3].EndedAt)

	// "Offline for N" is a difference of timestamps from the backend.
	info := app.CurrentPresence(org.ID, agent.ID)
	assert.Equal(t, models.AgentStateOffline, info.State)
	require.NotNil(t, info.Since)
	assert.True(t, info.Since.Equal(rows[3].StartedAt))
}

func TestPresenceLog_RepeatingTheSameStateWritesNothing(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, org.ID)
	online[agent.ID] = true

	for range 5 {
		app.RecordPresence(org.ID, agent.ID)
	}
	assert.Len(t, presenceRows(t, app, org.ID, agent.ID), 1)
}

// Offline beats away: a disconnected agent is offline whatever the toggle says,
// and back online they are still away until they toggle.
func TestPresenceLog_OfflineWinsOverAway(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, org.ID)

	online[agent.ID] = true
	setAway(t, app, org.ID, agent.ID, true)
	online[agent.ID] = false
	app.RecordPresence(org.ID, agent.ID)
	assert.Equal(t, models.AgentStateOffline, app.CurrentPresence(org.ID, agent.ID).State)

	online[agent.ID] = true
	app.RecordPresence(org.ID, agent.ID)
	assert.Equal(t, models.AgentStateAway, app.CurrentPresence(org.ID, agent.ID).State)
}

// A restart drops every connection at once. It closes the open intervals as
// "restart", does not invent an offline for everybody, and the first reconnect
// opens a fresh interval. An agent who never comes back keeps an honest
// "offline since" (the restart).
func TestPresenceLog_RestartClosesIntervalsWithoutInventingOffline(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	back, gone := createTestAgent(t, app, org.ID), createTestAgent(t, app, org.ID)
	online[back.ID], online[gone.ID] = true, true
	app.RecordPresence(org.ID, back.ID)
	app.RecordPresence(org.ID, gone.ID)
	time.Sleep(10 * time.Millisecond)

	boot := time.Now()
	app.ResetPresenceLogOnBoot()
	for _, id := range []uuid.UUID{back.ID, gone.ID} {
		rows := presenceRows(t, app, org.ID, id)
		require.Len(t, rows, 1)
		require.NotNil(t, rows[0].EndedAt, "closed by the restart")
		assert.Equal(t, models.PresenceClosedRestart, rows[0].ClosedReason)
		assert.False(t, rows[0].EndedAt.Before(boot.Add(-time.Millisecond)))
		assert.Equal(t, 0, openRows(rows), "no offline interval was opened for anyone")
	}

	// Nobody is connected after the restart; the agent who never returns is
	// offline since the restart, and that is what the log can truthfully say.
	online[back.ID], online[gone.ID] = false, false
	info := app.CurrentPresence(org.ID, gone.ID)
	assert.Equal(t, models.AgentStateOffline, info.State)
	require.NotNil(t, info.Since)
	assert.True(t, info.Since.After(boot.Add(-time.Millisecond)))

	// The other reconnects: a new interval, after the restart.
	online[back.ID] = true
	app.RecordPresence(org.ID, back.ID)
	rows := presenceRows(t, app, org.ID, back.ID)
	require.Len(t, rows, 2)
	assert.Equal(t, models.AgentStateOnline, rows[1].State)
	assert.Nil(t, rows[1].EndedAt)
	assert.False(t, rows[1].StartedAt.Before(*rows[0].EndedAt))
}

// The start of a state is only reported when the log can back it: an agent who
// was already online before the log existed has no honest "since".
func TestPresenceLog_SinceIsNilWhenTheLogHasNothingToSay(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, org.ID)
	online[agent.ID] = true

	info := app.CurrentPresence(org.ID, agent.ID)
	assert.Equal(t, models.AgentStateOnline, info.State)
	assert.Nil(t, info.Since)
}

// Callers race (the hub reports each transition from its own goroutine). Many
// concurrent records of the same state leave one open interval and no error,
// and a state change under concurrency still leaves exactly one open.
func TestPresenceLog_ConcurrentRecordsKeepOneOpenInterval(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, org.ID)
	online[agent.ID] = true

	run := func() {
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); app.RecordPresence(org.ID, agent.ID) }()
		}
		wg.Wait()
	}
	run()
	rows := presenceRows(t, app, org.ID, agent.ID)
	assert.Len(t, rows, 1)
	assert.Equal(t, 1, openRows(rows))

	require.NoError(t, app.DB.Model(&models.User{}).Where("id = ?", agent.ID).Update("is_available", false).Error)
	run()
	rows = presenceRows(t, app, org.ID, agent.ID)
	assert.Len(t, rows, 2)
	assert.Equal(t, 1, openRows(rows))
	assert.Equal(t, models.AgentStateAway, rows[1].State)
}

// Presence is per organization: one organization's log never leaks into another's.
func TestPresenceLog_IsolatedByOrganization(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, orgA.ID)
	online[agent.ID] = true
	app.RecordPresence(orgA.ID, agent.ID)

	assert.Len(t, presenceRows(t, app, orgA.ID, agent.ID), 1)
	assert.Empty(t, presenceRows(t, app, orgB.ID, agent.ID))
	assert.Nil(t, app.CurrentPresence(orgB.ID, agent.ID).Since, "organization B cannot see organization A's interval")
}

// The state in the user list is computed by the backend, with the instant it began.
func TestListUsers_ReportsPresenceStateAndSince(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID),
		testutil.WithEmail(testutil.UniqueEmail("pl-admin")))
	connected, away, offline := createTestAgent(t, app, org.ID), createTestAgent(t, app, org.ID), createTestAgent(t, app, org.ID)

	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	app.WSHub = hub
	for _, u := range []*models.User{admin, connected, away} {
		hub.Register(websocket.NewClient(hub, nil, u.ID, org.ID))
	}
	require.Eventually(t, func() bool { return hub.IsUserOnline(org.ID, away.ID) && hub.IsUserOnline(org.ID, connected.ID) },
		2*time.Second, 5*time.Millisecond)

	setAway(t, app, org.ID, away.ID, true)
	app.RecordPresence(org.ID, connected.ID)
	app.RecordPresence(org.ID, offline.ID)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	require.NoError(t, app.ListUsers(req))
	var resp struct {
		Data struct {
			Users []handlers.UserResponse `json:"users"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))

	byID := map[uuid.UUID]handlers.UserResponse{}
	for _, u := range resp.Data.Users {
		byID[u.ID] = u
	}
	state := func(id uuid.UUID) string {
		require.NotNil(t, byID[id].PresenceState)
		return *byID[id].PresenceState
	}
	assert.Equal(t, "online", state(connected.ID))
	assert.Equal(t, "away", state(away.ID))
	assert.Equal(t, "offline", state(offline.ID))

	for _, id := range []uuid.UUID{connected.ID, away.ID} {
		require.NotNil(t, byID[id].StatusSince, "a state the log recorded has a start")
		rows := presenceRows(t, app, org.ID, id)
		assert.WithinDuration(t, rows[len(rows)-1].StartedAt, *byID[id].StatusSince, time.Millisecond)
	}
}

// The realtime events carry the backend's state and its start, so a client
// never has to time a state itself.
func TestPresenceEvents_CarryStateAndSince(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	hub, clients := hubWithClients(t, org.ID, 1)
	app.WSHub = hub
	agent := createTestAgent(t, app, org.ID)

	setAway(t, app, org.ID, agent.ID, true)
	p := readBroadcast(t, clients[0], websocket.TypeAgentAvailability).Payload.(map[string]any)
	assert.Equal(t, false, p["is_available"], "the original fields keep their meaning")
	assert.Equal(t, "offline", p["state"], "no live connection: offline wins over away")
	since, ok := p["since"].(string)
	require.True(t, ok, "since is a server timestamp")
	parsed, err := time.Parse(time.RFC3339Nano, since)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), parsed, 5*time.Second)

	app.RecordPresence(org.ID, agent.ID)
	app.BroadcastAgentPresence(org.ID, agent.ID, false)
	p = readBroadcast(t, clients[0], websocket.TypeAgentPresence).Payload.(map[string]any)
	assert.Equal(t, false, p["online"])
	assert.Equal(t, "offline", p["state"])
}

// The offline release stays what it was (60 s of disconnection) but is now a
// policy of its own, separate from how away is handled.
func TestOfflineReleasePolicy_ReleasesAtTheGrace(t *testing.T) {
	p := handlers.OfflineReleasePolicy{Grace: handlers.DefaultPresenceGrace}
	assert.False(t, p.ShouldRelease(59*time.Second))
	assert.True(t, p.ShouldRelease(60*time.Second))
	assert.True(t, p.ShouldRelease(5*time.Minute))
}
