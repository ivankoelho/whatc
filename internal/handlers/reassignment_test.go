package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// reassignEnv is a team with one agent who is going away and some colleagues, on
// an organization that turned the away tolerance on (10 minutes). Presence is a
// switchable fake standing in for the WebSocket hub.
type reassignEnv struct {
	app     *handlers.App
	org     *models.Organization
	account *models.WhatsAppAccount
	team    *models.Team
	away    *models.User
	others  []*models.User
	online  map[uuid.UUID]bool
	reaper  *handlers.PresenceReaper
}

func newReassignEnv(t *testing.T, strategy models.AssignmentStrategy, colleagues int) *reassignEnv {
	t.Helper()
	e := &reassignEnv{online: map[uuid.UUID]bool{}}
	e.app = newTestApp(t, withPresence(&e.online))
	e.org = testutil.CreateTestOrganization(t, e.app.DB)
	e.account = testutil.CreateTestWhatsAppAccount(t, e.app.DB, e.org.ID)
	e.away = createTestAgent(t, e.app, e.org.ID)
	e.online[e.away.ID] = true
	members := []uuid.UUID{e.away.ID}
	for range colleagues {
		u := createTestAgent(t, e.app, e.org.ID)
		e.online[u.ID] = true
		e.others = append(e.others, u)
		members = append(members, u.ID)
	}
	e.team = createTestTeam(t, e.app, e.org.ID, members...)
	require.NoError(t, e.app.DB.Model(&models.Team{}).Where("id = ?", e.team.ID).
		Update("assignment_strategy", strategy).Error)
	e.app.Assigner.InvalidateTeamCache(e.team.ID)

	upsertChatbotSettings(t, e.app, e.org.ID, true)
	setAwayTolerance(t, e.app, e.org.ID, true, 10)
	e.reaper = handlers.NewPresenceReaper(e.app, handlers.DefaultPresenceGrace, time.Hour)
	return e
}

// attendance creates an active attendance of the away agent in the team. With
// customerWaits the customer's message is the latest; otherwise the agent replied last.
func (e *reassignEnv) attendance(t *testing.T, owner *uuid.UUID, customerWaits bool) *models.AgentTransfer {
	t.Helper()
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	tr := createTestTransfer(t, e.app, e.org.ID, contact.ID, e.account.Name, models.TransferStatusActive, owner)
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).Update("team_id", e.team.ID).Error)
	now := time.Now()
	if customerWaits {
		e.message(t, contact.ID, models.DirectionIncoming, now.Add(-2*time.Minute), nil)
	} else {
		e.message(t, contact.ID, models.DirectionIncoming, now.Add(-30*time.Minute), nil)
		e.message(t, contact.ID, models.DirectionOutgoing, now.Add(-20*time.Minute), &e.away.ID)
	}
	return tr
}

func (e *reassignEnv) message(t *testing.T, contactID uuid.UUID, dir models.Direction, at time.Time, sentBy *uuid.UUID) {
	t.Helper()
	m := createTestMessage(t, e.app, e.org.ID, contactID, dir, at)
	if sentBy != nil {
		require.NoError(t, e.app.DB.Model(&models.Message{}).Where("id = ?", m.ID).Update("sent_by_user_id", *sentBy).Error)
	}
}

// goAwayFor marks the away agent away through the real endpoint, `ago` ago.
func (e *reassignEnv) goAwayFor(t *testing.T, ago time.Duration) {
	t.Helper()
	setAway(t, e.app, e.org.ID, e.away.ID, true)
	res := e.app.DB.Model(&models.UserAvailabilityLog{}).
		Where("user_id = ? AND is_available = false AND ended_at IS NULL", e.away.ID).
		Update("started_at", time.Now().Add(-ago))
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)
}

func (e *reassignEnv) sweep() int { return e.reaper.SweepAwayOrganization(e.org.ID, time.Now()) }

func (e *reassignEnv) read(t *testing.T, id uuid.UUID) models.AgentTransfer {
	t.Helper()
	var tr models.AgentTransfer
	require.NoError(t, e.app.DB.First(&tr, "id = ?", id).Error)
	return tr
}

func (e *reassignEnv) history(t *testing.T, transferID uuid.UUID) []models.AgentTransferAssignment {
	t.Helper()
	var rows []models.AgentTransferAssignment
	require.NoError(t, e.app.DB.Where("organization_id = ? AND transfer_id = ?", e.org.ID, transferID).
		Order("occurred_at ASC, id ASC").Find(&rows).Error)
	return rows
}

func (e *reassignEnv) isOneOf(id *uuid.UUID, users ...*models.User) bool {
	if id == nil {
		return false
	}
	for _, u := range users {
		if u.ID == *id {
			return true
		}
	}
	return false
}

// Only the attendances in which the customer is waiting are redistributed; the one
// in which the agent waits for the customer stays with them.
func TestReassign_OnlyAttendancesWithTheCustomerWaitingMove(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 2)
	waiting := e.attendance(t, &e.away.ID, true)
	quiet := e.attendance(t, &e.away.ID, false)
	e.goAwayFor(t, 11*time.Minute)

	assert.Equal(t, 1, e.sweep())

	moved := e.read(t, waiting.ID)
	assert.True(t, e.isOneOf(moved.AgentID, e.others...), "the waiting customer went to a colleague")
	kept := e.read(t, quiet.ID)
	require.NotNil(t, kept.AgentID)
	assert.Equal(t, e.away.ID, *kept.AgentID, "the agent waiting for the customer keeps it")
}

// The team's own strategy chooses, through the existing distribution.
func TestReassign_UsesTheTeamStrategy(t *testing.T) {
	t.Run("round robin: the one assigned longest ago", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 2)
		older, newer := e.others[0], e.others[1]
		require.NoError(t, e.app.DB.Model(&models.TeamMember{}).Where("team_id = ? AND user_id = ?", e.team.ID, older.ID).
			Update("last_assigned_at", time.Now().Add(-2*time.Hour)).Error)
		require.NoError(t, e.app.DB.Model(&models.TeamMember{}).Where("team_id = ? AND user_id = ?", e.team.ID, newer.ID).
			Update("last_assigned_at", time.Now().Add(-5*time.Minute)).Error)
		tr := e.attendance(t, &e.away.ID, true)
		e.goAwayFor(t, 11*time.Minute)

		require.Equal(t, 1, e.sweep())

		got := e.read(t, tr.ID)
		require.NotNil(t, got.AgentID)
		assert.Equal(t, older.ID, *got.AgentID)
	})

	t.Run("load balanced: the one with fewer attendances", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyLoadBalanced, 2)
		busy, free := e.others[0], e.others[1]
		for range 3 {
			c := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
			createTestTransfer(t, e.app, e.org.ID, c.ID, e.account.Name, models.TransferStatusActive, &busy.ID)
		}
		tr := e.attendance(t, &e.away.ID, true)
		e.goAwayFor(t, 11*time.Minute)

		require.Equal(t, 1, e.sweep())

		got := e.read(t, tr.ID)
		require.NotNil(t, got.AgentID)
		assert.Equal(t, free.ID, *got.AgentID)
	})

	t.Run("manual queue: nobody is chosen, it waits in the team queue", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyManual, 2)
		tr := e.attendance(t, &e.away.ID, true)
		e.goAwayFor(t, 11*time.Minute)

		require.Equal(t, 1, e.sweep())

		got := e.read(t, tr.ID)
		assert.Nil(t, got.AgentID)
		require.NotNil(t, got.TeamID)
		assert.Equal(t, e.team.ID, *got.TeamID)
	})
}

// Away, offline and the agent being replaced are never chosen; the team and the
// organization are preserved.
func TestReassign_NeverChoosesSomeoneWhoCannotReceive(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 3)
	offline, alsoAway, ready := e.others[0], e.others[1], e.others[2]
	e.online[offline.ID] = false
	require.NoError(t, e.app.DB.Model(&models.User{}).Where("id = ?", alsoAway.ID).Update("is_available", false).Error)

	// An agent of ANOTHER organization, eligible, in a team of their own.
	otherOrg := testutil.CreateTestOrganization(t, e.app.DB)
	stranger := createTestAgent(t, e.app, otherOrg.ID)
	e.online[stranger.ID] = true
	createTestTeam(t, e.app, otherOrg.ID, stranger.ID)

	var tr *models.AgentTransfer
	for range 4 {
		tr = e.attendance(t, &e.away.ID, true)
	}
	e.goAwayFor(t, 11*time.Minute)

	assert.Equal(t, 4, e.sweep())

	var rows []models.AgentTransfer
	require.NoError(t, e.app.DB.Where("organization_id = ? AND status = ?", e.org.ID, models.TransferStatusActive).Find(&rows).Error)
	require.Len(t, rows, 4)
	for _, r := range rows {
		require.NotNil(t, r.AgentID)
		assert.Equal(t, ready.ID, *r.AgentID, "only the eligible colleague receives")
		require.NotNil(t, r.TeamID)
		assert.Equal(t, e.team.ID, *r.TeamID, "the team is preserved")
		assert.Equal(t, e.org.ID, r.OrganizationID)
	}
	_ = tr
}

// With nobody eligible the attendance waits in the SAME team's queue, keeps its
// place in line, and the first colleague who picks takes it before newer ones.
func TestReassign_NoEligibleAgentKeepsItInTheTeamQueueInOrder(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
	colleague := e.others[0]
	e.online[colleague.ID] = false

	tr := e.attendance(t, &e.away.ID, true)
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).
		Update("transferred_at", time.Now().Add(-3*time.Hour)).Error)
	newer := createTestTransfer(t, e.app, e.org.ID, testutil.CreateTestContact(t, e.app.DB, e.org.ID).ID,
		e.account.Name, models.TransferStatusActive, nil)
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", newer.ID).Update("team_id", e.team.ID).Error)
	e.goAwayFor(t, 11*time.Minute)

	require.Equal(t, 1, e.sweep())

	got := e.read(t, tr.ID)
	assert.Nil(t, got.AgentID)
	require.NotNil(t, got.TeamID)
	assert.Equal(t, e.team.ID, *got.TeamID)
	rows := e.history(t, tr.ID)
	require.NotEmpty(t, rows)
	assert.Equal(t, "returned_away_timeout", rows[len(rows)-1].Reason)

	// The colleague comes back and picks from the queue: the older attendance first.
	e.online[colleague.ID] = true
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, e.org.ID, colleague.ID)
	require.NoError(t, e.app.PickNextTransfer(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.NotNil(t, e.read(t, tr.ID).AgentID, "the reassigned attendance was picked first")
	assert.Nil(t, e.read(t, newer.ID).AgentID)
}

// The customer writing wakes the attendance up, but only once the agent has been
// away past the tolerance, and only when the customer really is the one waiting.
func TestReassign_CustomerReplyReevaluatesImmediately(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 2)
	tr := e.attendance(t, &e.away.ID, false) // the agent replied last
	e.goAwayFor(t, 5*time.Minute)            // inside the tolerance

	// The customer writes while the agent has been away only 5 of 10 minutes.
	e.message(t, tr.ContactID, models.DirectionIncoming, time.Now(), nil)
	e.app.ReevaluateOnCustomerReply(e.org.ID, tr.ContactID)
	assert.Equal(t, e.away.ID, *e.read(t, tr.ID).AgentID, "inside the tolerance nothing moves")

	// Past the tolerance the same reply hands it over at once, without waiting for a sweep.
	require.NoError(t, e.app.DB.Model(&models.UserAvailabilityLog{}).
		Where("user_id = ? AND is_available = false AND ended_at IS NULL", e.away.ID).
		Update("started_at", time.Now().Add(-11*time.Minute)).Error)
	e.app.ReevaluateOnCustomerReply(e.org.ID, tr.ContactID)

	got := e.read(t, tr.ID)
	assert.True(t, e.isOneOf(got.AgentID, e.others...))
	rows := e.history(t, tr.ID)
	require.NotEmpty(t, rows)
	assert.Equal(t, "reassigned_customer_waiting", rows[len(rows)-1].Reason)
}

func TestReassign_CustomerReplyDoesNothingWhenItDoesNotApply(t *testing.T) {
	t.Run("the agent replied last", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		tr := e.attendance(t, &e.away.ID, false)
		e.goAwayFor(t, 30*time.Minute)
		e.app.ReevaluateOnCustomerReply(e.org.ID, tr.ContactID) // no new message: the agent is the one owing nothing
		assert.Equal(t, e.away.ID, *e.read(t, tr.ID).AgentID)
	})
	t.Run("the agent is not away", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		tr := e.attendance(t, &e.away.ID, true)
		e.app.ReevaluateOnCustomerReply(e.org.ID, tr.ContactID)
		assert.Equal(t, e.away.ID, *e.read(t, tr.ID).AgentID)
	})
	t.Run("the organization did not opt in", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		setAwayTolerance(t, e.app, e.org.ID, false, 0)
		// Off, going away returns the attendances at once, so the attendance is given to
		// the agent after they are already away (as if assigned while away).
		e.goAwayFor(t, 30*time.Minute)
		tr := e.attendance(t, &e.away.ID, true)
		e.app.ReevaluateOnCustomerReply(e.org.ID, tr.ContactID)
		assert.Equal(t, e.away.ID, *e.read(t, tr.ID).AgentID)
	})
}

// Several sweeps at once hand the attendance over exactly once, and the history
// shows a single move.
func TestReassign_ConcurrentSweepsAssignOnlyOnce(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 3)
	tr := e.attendance(t, &e.away.ID, true)
	e.goAwayFor(t, 11*time.Minute)

	var total atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); total.Add(int64(e.sweep())) }()
	}
	wg.Wait()

	assert.EqualValues(t, 1, total.Load(), "one winner")
	got := e.read(t, tr.ID)
	assert.True(t, e.isOneOf(got.AgentID, e.others...))
	moves := 0
	for _, r := range e.history(t, tr.ID) {
		if r.Reason == "reassigned_away_timeout" {
			moves++
		}
	}
	assert.Equal(t, 1, moves, "a single reassignment in the history")
}

// A supervisor who hands the attendance to someone else before the sweep wins:
// the sweep finds nothing of the away agent's to move, and the history records the supervisor.
func TestReassign_SupervisorManualReassignmentWinsOverTheSweep(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 2)
	tr := e.attendance(t, &e.away.ID, true)
	e.goAwayFor(t, 11*time.Minute)

	adminRole := testutil.CreateAdminRole(t, e.app.DB, e.org.ID)
	supervisor := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&adminRole.ID),
		testutil.WithEmail(testutil.UniqueEmail("supervisor")))
	chosen := e.others[1]
	req := testutil.NewJSONRequest(t, map[string]any{"agent_id": chosen.ID.String()})
	testutil.SetAuthContext(req, e.org.ID, supervisor.ID)
	testutil.SetPathParam(req, "id", tr.ID.String())
	require.NoError(t, e.app.AssignAgentTransfer(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	assert.Equal(t, 0, e.sweep(), "nothing left of the away agent's to move")
	got := e.read(t, tr.ID)
	require.NotNil(t, got.AgentID)
	assert.Equal(t, chosen.ID, *got.AgentID)

	rows := e.history(t, tr.ID)
	require.NotEmpty(t, rows)
	last := rows[len(rows)-1]
	assert.Equal(t, "assigned", last.Reason)
	require.NotNil(t, last.ActorUserID)
	assert.Equal(t, supervisor.ID, *last.ActorUserID, "the history records who did it")
}

// The history tells the story of an attendance: who took it, who it was taken
// from and why, and how it ended, with intervals derivable from the events.
func TestOwnershipHistory_TellsTheWholeStory(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
	colleague := e.others[0]

	// The away agent picks it from the queue.
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	tr := createTestTransfer(t, e.app, e.org.ID, contact.ID, e.account.Name, models.TransferStatusActive, nil)
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).Update("team_id", e.team.ID).Error)
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, e.org.ID, e.away.ID)
	require.NoError(t, e.app.PickNextTransfer(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	// The customer writes, the agent goes away and stays away, the sweep moves it.
	e.message(t, contact.ID, models.DirectionIncoming, time.Now(), nil)
	e.goAwayFor(t, 11*time.Minute)
	require.Equal(t, 1, e.sweep())

	// The colleague closes it.
	closeReq := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(closeReq, e.org.ID, colleague.ID)
	testutil.SetPathParam(closeReq, "id", tr.ID.String())
	require.NoError(t, e.app.ResumeFromTransfer(closeReq))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(closeReq))

	rows := e.history(t, tr.ID)
	require.Len(t, rows, 3)

	claimed, moved, closed := rows[0], rows[1], rows[2]
	assert.Equal(t, "claimed", claimed.Reason)
	assert.Nil(t, claimed.FromAgentID)
	require.NotNil(t, claimed.ToAgentID)
	assert.Equal(t, e.away.ID, *claimed.ToAgentID)

	assert.Equal(t, "reassigned_away_timeout", moved.Reason)
	require.NotNil(t, moved.FromAgentID)
	assert.Equal(t, e.away.ID, *moved.FromAgentID)
	require.NotNil(t, moved.ToAgentID)
	assert.Equal(t, colleague.ID, *moved.ToAgentID)
	assert.Nil(t, moved.ActorUserID, "the system moved it")
	require.NotNil(t, moved.TeamID)
	assert.Equal(t, e.team.ID, *moved.TeamID)

	assert.Equal(t, "closed", closed.Reason)
	require.NotNil(t, closed.FromAgentID)
	assert.Equal(t, colleague.ID, *closed.FromAgentID)
	assert.Nil(t, closed.ToAgentID)

	// Intervals come out of the events: each holder holds from their event until the next.
	var held []struct {
		AgentID uuid.UUID
		Seconds float64
	}
	require.NoError(t, e.app.DB.Raw(`
SELECT agent_id, seconds FROM (
    SELECT to_agent_id AS agent_id, occurred_at,
           EXTRACT(EPOCH FROM (LEAD(occurred_at) OVER (PARTITION BY transfer_id ORDER BY occurred_at, id) - occurred_at)) AS seconds
    FROM agent_transfer_assignments WHERE organization_id = ? AND transfer_id = ?
) x WHERE agent_id IS NOT NULL ORDER BY occurred_at`, e.org.ID, tr.ID).Scan(&held).Error)
	require.Len(t, held, 2)
	assert.Equal(t, e.away.ID, held[0].AgentID)
	assert.Equal(t, colleague.ID, held[1].AgentID)
	assert.Greater(t, held[0].Seconds, 0.0, "the first holder has a measurable interval")
	assert.Greater(t, held[1].Seconds, 0.0, "the closing event ends the second one")
}

// After a reassignment each agent's numbers reflect what really happened: the
// one who lost it, the one who got it, and the one who resolved it.
func TestReassign_MetricsAfterReassignment(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
	colleague := e.others[0]
	tr := e.attendance(t, &e.away.ID, true)
	e.goAwayFor(t, 11*time.Minute)
	require.Equal(t, 1, e.sweep())

	// The colleague resolves it.
	resumed := time.Now()
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).
		Updates(map[string]any{"status": models.TransferStatusResumed, "resumed_at": resumed, "resumed_by": colleague.ID}).Error)

	role := testutil.CreateTestRoleExact(t, e.app.DB, e.org.ID, "Analytics Viewer", false, false, getAnalyticsPermissions(t, e.app))
	viewer := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithEmail(testutil.UniqueEmail("viewer")),
		testutil.WithRoleID(&role.ID))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, viewer.ID)
	testutil.SetQueryParam(req, "from", time.Now().AddDate(0, 0, -2).Format("2006-01-02"))
	testutil.SetQueryParam(req, "to", time.Now().AddDate(0, 0, 1).Format("2006-01-02"))
	require.NoError(t, e.app.GetAgentAnalytics(req))
	var resp struct {
		Data handlers.AgentAnalyticsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	byAgent := map[string]handlers.AgentPerformanceStats{}
	for _, s := range resp.Data.AgentStats {
		byAgent[s.AgentID] = s
	}

	lost, got := byAgent[e.away.ID.String()], byAgent[colleague.ID.String()]
	assert.EqualValues(t, 1, lost.ReassignedAway, "the agent who was away lost one")
	assert.EqualValues(t, 0, lost.ReassignedIn)
	assert.EqualValues(t, 0, lost.TransfersHandled, "and does not get credit for the resolution")
	assert.EqualValues(t, 1, got.ReassignedIn, "the colleague received one")
	assert.EqualValues(t, 1, got.TransfersHandled, "and resolved it")
}

// The relationship (carteira) stays with the agent who held it, whichever way the
// attendance goes: to a colleague, or back to the team queue.
func TestReassign_KeepsTheCarteira(t *testing.T) {
	for name, colleagues := range map[string]int{"to a colleague": 1, "back to the queue": 0} {
		t.Run(name, func(t *testing.T) {
			e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, colleagues)
			tr := e.attendance(t, &e.away.ID, true)
			require.NoError(t, e.app.DB.Model(&models.Contact{}).Where("id = ?", tr.ContactID).
				Update("assigned_user_id", e.away.ID).Error)
			e.goAwayFor(t, 11*time.Minute)

			require.Equal(t, 1, e.sweep())

			got := readContactAssignedUser(t, e.app, tr.ContactID)
			require.NotNil(t, got)
			assert.Equal(t, e.away.ID, *got, "the carteira is not touched by a reassignment")
		})
	}
}

// An attendance selected for an away agent but already handed to a colleague (by
// another sweep, a moment earlier) must not be taken from that colleague. This is
// the exact failure two simultaneous sweeps used to produce.
func TestReassign_DoesNotTakeAnAttendanceFromSomeoneWhoIsNotTheAwayAgent(t *testing.T) {
	e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 2)
	colleague := e.others[0]
	tr := e.attendance(t, &colleague.ID, true) // already with an available colleague

	loaded := e.read(t, tr.ID)
	moved := e.app.ReassignAwayTransferForTest(&loaded, e.away.ID, "reassigned_away_timeout")

	assert.False(t, moved, "its owner is not the away agent it was selected for")
	got := e.read(t, tr.ID)
	require.NotNil(t, got.AgentID)
	assert.Equal(t, colleague.ID, *got.AgentID, "the colleague keeps it")
	assert.Empty(t, e.history(t, tr.ID), "and nothing was recorded")

	// When the owner IS the away agent, it moves.
	mine := e.attendance(t, &e.away.ID, true)
	loadedMine := e.read(t, mine.ID)
	assert.True(t, e.app.ReassignAwayTransferForTest(&loadedMine, e.away.ID, "reassigned_away_timeout"))
	assert.NotEqual(t, e.away.ID, *e.read(t, mine.ID).AgentID)
}

// webhookSink is an HTTP endpoint that records what an integration would receive.
type webhookSink struct {
	mu       sync.Mutex
	received []handlers.OutboundWebhookPayload
	server   *httptest.Server
}

func newWebhookSink(t *testing.T) *webhookSink {
	t.Helper()
	s := &webhookSink{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p handlers.OutboundWebhookPayload
		if json.Unmarshal(body, &p) == nil {
			s.mu.Lock()
			s.received = append(s.received, p)
			s.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *webhookSink) events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.received))
	for _, p := range s.received {
		out = append(out, p.Event)
	}
	return out
}

// transferData decodes the data of the n-th delivery.
func (s *webhookSink) transferData(t *testing.T, n int) handlers.TransferEventData {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Greater(t, len(s.received), n)
	raw, err := json.Marshal(s.received[n].Data)
	require.NoError(t, err)
	var d handlers.TransferEventData
	require.NoError(t, json.Unmarshal(raw, &d))
	return d
}

// subscribe makes the organization's integration listen to transfer.assigned.
func (e *reassignEnv) subscribe(t *testing.T) *webhookSink {
	t.Helper()
	sink := newWebhookSink(t)
	createTestWebhook(t, e.app, e.org.ID, "integration", sink.server.URL,
		[]string{string(models.WebhookEventTransferAssigned)})
	e.app.InvalidateWebhooksCache(e.org.ID)
	return sink
}

// Handing an attendance to a new agent tells integrations, exactly as a manual
// assignment does; sending it back to the queue sends nothing (as before).
func TestReassign_SendsTheTransferAssignedWebhook(t *testing.T) {
	t.Run("by the sweep, to the colleague who received it", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		colleague := e.others[0]
		sink := e.subscribe(t)
		tr := e.attendance(t, &e.away.ID, true)
		e.goAwayFor(t, 11*time.Minute)

		require.Equal(t, 1, e.sweep())
		e.app.WaitForBackgroundTasks()

		require.Equal(t, []string{"transfer.assigned"}, sink.events())
		d := sink.transferData(t, 0)
		assert.Equal(t, tr.ID.String(), d.TransferID)
		assert.Equal(t, tr.ContactID.String(), d.ContactID)
		require.NotNil(t, d.AgentID)
		assert.Equal(t, colleague.ID.String(), *d.AgentID, "the event names the NEW agent")
		require.NotNil(t, d.AgentName)
		assert.Equal(t, colleague.FullName, *d.AgentName)
		assert.Equal(t, e.account.Name, d.WhatsAppAccount)
	})

	t.Run("when the customer writes to an agent who is away past the tolerance", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		colleague := e.others[0]
		sink := e.subscribe(t)
		tr := e.attendance(t, &e.away.ID, false)
		e.goAwayFor(t, 11*time.Minute)
		e.message(t, tr.ContactID, models.DirectionIncoming, time.Now(), nil)

		e.app.ReevaluateOnCustomerReply(e.org.ID, tr.ContactID)
		e.app.WaitForBackgroundTasks()

		require.Equal(t, []string{"transfer.assigned"}, sink.events())
		d := sink.transferData(t, 0)
		require.NotNil(t, d.AgentID)
		assert.Equal(t, colleague.ID.String(), *d.AgentID)
	})

	t.Run("not when it only goes back to the queue", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		e.online[e.others[0].ID] = false // nobody eligible
		sink := e.subscribe(t)
		tr := e.attendance(t, &e.away.ID, true)
		e.goAwayFor(t, 11*time.Minute)

		require.Equal(t, 1, e.sweep())
		e.app.WaitForBackgroundTasks()

		assert.Nil(t, e.read(t, tr.ID).AgentID, "it did go back to the queue")
		assert.Empty(t, sink.events(), "no agent was assigned, so no transfer.assigned")
	})

	t.Run("not for an organization whose integration does not listen to it", func(t *testing.T) {
		e := newReassignEnv(t, models.AssignmentStrategyRoundRobin, 1)
		sink := newWebhookSink(t)
		createTestWebhook(t, e.app, e.org.ID, "other-event", sink.server.URL, []string{"message.incoming"})
		e.app.InvalidateWebhooksCache(e.org.ID)
		e.attendance(t, &e.away.ID, true)
		e.goAwayFor(t, 11*time.Minute)

		require.Equal(t, 1, e.sweep())
		e.app.WaitForBackgroundTasks()

		assert.Empty(t, sink.events())
	})
}
