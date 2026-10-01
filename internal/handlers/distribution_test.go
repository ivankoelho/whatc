package handlers_test

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
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

func TestApp_CreateAgentTransfer_ExplicitAgentOfflineIsRejected(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID) // is_available=true in the DB, but not connected

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":       contact.ID.String(),
		"whatsapp_account": account.Name,
		"agent_id":         agent.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)

	require.NoError(t, app.CreateAgentTransfer(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	var result map[string]any
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &result))
	assert.Equal(t, "Agent is currently offline", result["message"])

	var n int64
	app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Count(&n)
	assert.Zero(t, n, "no transfer may be created for an offline agent")
}

func TestApp_CreateAgentTransfer_ExplicitAgentConnectedIsAccepted(t *testing.T) {
	online := map[uuid.UUID]bool{}
	app := newTestApp(t, withPresence(&online))
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	online[agent.ID] = true

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":       contact.ID.String(),
		"whatsapp_account": account.Name,
		"agent_id":         agent.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)

	require.NoError(t, app.CreateAgentTransfer(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
}

// --- Atomic claim / uniqueness / ownership gate ---

func twoAgents(t *testing.T, app *handlers.App, orgID uuid.UUID) (*models.User, *models.User) {
	t.Helper()
	return createTestAgent(t, app, orgID), createTestAgent(t, app, orgID)
}

func TestClaimTransfer_ConcurrentClaimersExactlyOneWins(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	transfer := createTestTransfer(t, app, org.ID, contact.ID, account.Name, models.TransferStatusActive, nil)

	const claimers = 12
	var wins atomic.Int32
	var wg sync.WaitGroup
	agents := make([]*models.User, claimers)
	for i := range agents {
		agents[i] = createTestAgent(t, app, org.ID)
	}
	start := make(chan struct{})
	for _, ag := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tr := *transfer // each caller holds its own stale copy, as in production
			ok, err := app.ClaimTransferForTest(&tr, ag.ID)
			assert.NoError(t, err)
			if ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, wins.Load(), "exactly one claimer may win")
	var stored models.AgentTransfer
	require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
	require.NotNil(t, stored.AgentID)
	assert.NotNil(t, stored.SLA.PickedUpAt)
}

func TestClaimTransfer_ClosedOrAssignedTransferIsNotClaimable(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	a1, a2 := twoAgents(t, app, org.ID)

	assigned := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &a1.ID)
	ok, err := app.ClaimTransferForTest(assigned, a2.ID)
	require.NoError(t, err)
	assert.False(t, ok)

	closed := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusResumed, nil)
	ok, err = app.ClaimTransferForTest(closed, a2.ID)
	require.NoError(t, err)
	assert.False(t, ok, "a resolved transfer must not be re-claimed")
}

func TestOpenAgentInitiatedTransfer_ConcurrentOpensCreateOneAttendance(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	const n = 10
	var wg sync.WaitGroup
	var conflicts, created atomic.Int32
	start := make(chan struct{})
	for range n {
		ag := createTestAgent(t, app, org.ID)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := app.EnsureAgentOwnsConversationForTest(account, contact, ag.ID)
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, handlers.ErrConversationOwned):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	var active int64
	app.DB.Model(&models.AgentTransfer{}).
		Where("contact_id = ? AND status = ?", contact.ID, models.TransferStatusActive).Count(&active)
	assert.EqualValues(t, 1, active, "never two active attendances for one contact")
	assert.EqualValues(t, 1, created.Load(), "only the winner may proceed to send")
	assert.EqualValues(t, n-1, conflicts.Load(), "every loser is refused, not given a 500")
}

func TestEnsureAgentOwnsConversation_Rules(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	owner, other := twoAgents(t, app, org.ID)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	supervisor := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	t.Run("unassigned queue transfer is claimed by the sender", func(t *testing.T) {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		tr := createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, nil)
		require.NoError(t, app.EnsureAgentOwnsConversationForTest(account, c, owner.ID))
		var got models.AgentTransfer
		require.NoError(t, app.DB.First(&got, "id = ?", tr.ID).Error)
		require.NotNil(t, got.AgentID)
		assert.Equal(t, owner.ID, *got.AgentID)
	})

	t.Run("owner keeps sending", func(t *testing.T) {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, &owner.ID)
		assert.NoError(t, app.EnsureAgentOwnsConversationForTest(account, c, owner.ID))
	})

	t.Run("another agent is refused and nothing changes", func(t *testing.T) {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		tr := createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, &owner.ID)
		err := app.EnsureAgentOwnsConversationForTest(account, c, other.ID)
		assert.ErrorIs(t, err, handlers.ErrConversationOwned)
		var got models.AgentTransfer
		require.NoError(t, app.DB.First(&got, "id = ?", tr.ID).Error)
		assert.Equal(t, owner.ID, *got.AgentID)
	})

	t.Run("supervisor with transfers:write may write without taking over", func(t *testing.T) {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		tr := createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, &owner.ID)
		assert.NoError(t, app.EnsureAgentOwnsConversationForTest(account, c, supervisor.ID))
		var got models.AgentTransfer
		require.NoError(t, app.DB.First(&got, "id = ?", tr.ID).Error)
		assert.Equal(t, owner.ID, *got.AgentID, "ownership is unchanged")
	})

	t.Run("no attendance yet: the sender opens one", func(t *testing.T) {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.EnsureAgentOwnsConversationForTest(account, c, other.ID))
		trs := activeTransfers(t, app, c.ID)
		require.Len(t, trs, 1)
		assert.Equal(t, other.ID, *trs[0].AgentID)
		assert.Equal(t, models.TransferSourceAgentInitiated, trs[0].Source)
	})
}

func TestApp_CreateAgentTransfer_DuplicateActiveIsConflictNot500(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	createTestTransfer(t, app, org.ID, contact.ID, account.Name, models.TransferStatusActive, nil)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "whatsapp_account": account.Name})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateAgentTransfer(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}

func TestApp_AssignAgentTransfer_SelfAssignCannotStealFromAnotherAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	owner, thief := twoAgents(t, app, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	tr := createTestTransfer(t, app, org.ID, contact.ID, account.Name, models.TransferStatusActive, &owner.ID)

	req := testutil.NewJSONRequest(t, map[string]any{}) // agent_id absent: "assign to me"
	testutil.SetAuthContext(req, org.ID, thief.ID)
	testutil.SetPathParam(req, "id", tr.ID.String())
	require.NoError(t, app.AssignAgentTransfer(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))

	var got models.AgentTransfer
	require.NoError(t, app.DB.First(&got, "id = ?", tr.ID).Error)
	assert.Equal(t, owner.ID, *got.AgentID, "the owner is untouched")
}

func TestApp_AssignAgentTransfer_ConcurrentSelfAssignOneWinner(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	tr := createTestTransfer(t, app, org.ID, contact.ID, account.Name, models.TransferStatusActive, nil)

	const n = 8
	var wg sync.WaitGroup
	var ok, conflict atomic.Int32
	start := make(chan struct{})
	for range n {
		ag := createTestAgent(t, app, org.ID)
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, map[string]any{})
			testutil.SetAuthContext(req, org.ID, ag.ID)
			testutil.SetPathParam(req, "id", tr.ID.String())
			<-start
			_ = app.AssignAgentTransfer(req)
			switch testutil.GetResponseStatusCode(req) {
			case fasthttp.StatusOK:
				ok.Add(1)
			case fasthttp.StatusConflict:
				conflict.Add(1)
			default:
				t.Errorf("unexpected status %d", testutil.GetResponseStatusCode(req))
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, ok.Load())
	assert.EqualValues(t, n-1, conflict.Load())
}

// --- Audit trail and realtime ---

// hubWithClients starts a real hub and registers n clients in orgID.
func hubWithClients(t *testing.T, orgID uuid.UUID, n int) (*websocket.Hub, []*websocket.Client) {
	t.Helper()
	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	clients := make([]*websocket.Client, n)
	for i := range clients {
		clients[i] = websocket.NewClient(hub, nil, uuid.New(), orgID)
		hub.Register(clients[i])
	}
	require.Eventually(t, func() bool { return hub.GetClientCount() == n }, 2*time.Second, 5*time.Millisecond)
	return hub, clients
}

// distributionAudit returns the audit changes for a transfer as event -> entry.
func distributionAudit(t *testing.T, app *handlers.App, transferID uuid.UUID, wantEvent string) map[string]any {
	t.Helper()
	var found map[string]any
	require.Eventually(t, func() bool {
		var logs []models.AuditLog
		app.DB.Where("resource_type = ? AND resource_id = ?", models.ResourceTransfers, transferID).Find(&logs)
		for _, l := range logs {
			fields := map[string]any{}
			for _, c := range l.Changes {
				m := c.(map[string]any)
				fields[m["field"].(string)] = m["new_value"]
			}
			fields["_user_name"] = l.UserName
			fields["_user_id"] = l.UserID.String()
			fields["_old_agent"] = nil
			for _, c := range l.Changes {
				if m := c.(map[string]any); m["field"] == "agent_id" {
					fields["_old_agent"] = m["old_value"]
				}
			}
			if fields["event"] == wantEvent {
				found = fields
				return true
			}
		}
		return false
	}, 3*time.Second, 25*time.Millisecond, "audit event %q not recorded", wantEvent)
	return found
}

func TestDistributionAudit_ClaimOnSendIsRecordedWithActor(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	c := testutil.CreateTestContact(t, app.DB, org.ID)
	tr := createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, nil)

	require.NoError(t, app.EnsureAgentOwnsConversationForTest(account, c, agent.ID))

	got := distributionAudit(t, app, tr.ID, "claimed")
	assert.Equal(t, agent.ID.String(), got["agent_id"])
	assert.Equal(t, agent.ID.String(), got["_user_id"])
}

func TestDistributionAudit_RefusedSendIsRecordedAsConflict(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	owner, intruder := twoAgents(t, app, org.ID)
	c := testutil.CreateTestContact(t, app.DB, org.ID)
	tr := createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, &owner.ID)

	require.ErrorIs(t, app.EnsureAgentOwnsConversationForTest(account, c, intruder.ID), handlers.ErrConversationOwned)

	got := distributionAudit(t, app, tr.ID, "conflict")
	assert.Equal(t, intruder.ID.String(), got["_user_id"], "the actor is the refused agent")
	assert.Equal(t, owner.ID.String(), got["_old_agent"], "the current owner is recorded")
}

func TestDistributionAudit_ReaperReleaseIsASystemEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	tr := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)

	presence := newFakePresence()
	presence.set(agent.ID, false)
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)
	t0 := time.Now()
	reaper.Sweep(t0)
	require.Equal(t, 1, reaper.Sweep(t0.Add(61*time.Second)))

	got := distributionAudit(t, app, tr.ID, "returned_offline")
	assert.Equal(t, "System", got["_user_name"])
	assert.Equal(t, agent.ID.String(), got["_old_agent"])
	assert.Nil(t, got["agent_id"])
}

func TestDistributionRealtime_ClaimAndReleaseReachEveryClient(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	hub, clients := hubWithClients(t, org.ID, 3)
	app.WSHub = hub
	agent := createTestAgent(t, app, org.ID)
	c := testutil.CreateTestContact(t, app.DB, org.ID)
	createTestTransfer(t, app, org.ID, c.ID, account.Name, models.TransferStatusActive, nil)

	require.NoError(t, app.EnsureAgentOwnsConversationForTest(account, c, agent.ID))
	for _, cl := range clients {
		msg := readBroadcast(t, cl, websocket.TypeAgentTransferAssign)
		assert.Equal(t, agent.ID.String(), msg.Payload.(map[string]any)["agent_id"])
	}

	// Released by the reaper: every client sees the attendance back in the queue.
	presence := newFakePresence()
	presence.set(agent.ID, false)
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)
	t0 := time.Now()
	reaper.Sweep(t0)
	require.Equal(t, 1, reaper.Sweep(t0.Add(61*time.Second)))
	for _, cl := range clients {
		msg := readBroadcast(t, cl, websocket.TypeAgentTransferAssign)
		assert.Nil(t, msg.Payload.(map[string]any)["agent_id"])
	}
}

func TestDistributionRealtime_PresenceAndAvailabilityEvents(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	hub, clients := hubWithClients(t, org.ID, 2)
	app.WSHub = hub
	agent := createTestAgent(t, app, org.ID)

	app.BroadcastAgentPresence(org.ID, agent.ID, false)
	for _, cl := range clients {
		p := readBroadcast(t, cl, websocket.TypeAgentPresence).Payload.(map[string]any)
		assert.Equal(t, agent.ID.String(), p["user_id"])
		assert.Equal(t, false, p["online"])
	}

	req := testutil.NewJSONRequest(t, map[string]any{"is_available": false})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.UpdateAvailability(req))
	for _, cl := range clients {
		p := readBroadcast(t, cl, websocket.TypeAgentAvailability).Payload.(map[string]any)
		assert.Equal(t, agent.ID.String(), p["user_id"])
		assert.Equal(t, false, p["is_available"])
	}

	// Setting the same value again is not a change: no event, no audit entry.
	req = testutil.NewJSONRequest(t, map[string]any{"is_available": false})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.UpdateAvailability(req))
	assertNoBroadcast(t, clients[0])
}

func TestDistributionAudit_AvailabilityChangeIsRecorded(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := createTestAgent(t, app, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"is_available": false})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.UpdateAvailability(req))

	require.Eventually(t, func() bool {
		var n int64
		app.DB.Model(&models.AuditLog{}).Where("resource_type = ? AND resource_id = ? AND user_id = ?",
			models.ResourceUsers, agent.ID, agent.ID).Count(&n)
		return n == 1
	}, 3*time.Second, 25*time.Millisecond)
}
