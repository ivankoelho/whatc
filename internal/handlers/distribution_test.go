package handlers_test

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
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
