package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// picked_up_at is the first pickup from the queue: a transfer returned to the
// queue and picked again keeps the original instant, and a breach is not
// re-evaluated against the new pickup.
func TestApp_PickNextTransfer_KeepsFirstPickedUpAtAfterReturnToQueue(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	first := createTestAgent(t, app, org.ID)
	second := createTestAgent(t, app, org.ID)
	transfer := createTestTransfer(t, app, org.ID, contact.ID, account.Name, models.TransferStatusActive, nil)

	pick := func(agentID uuid.UUID) {
		t.Helper()
		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, agentID)
		require.NoError(t, app.PickNextTransfer(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	}
	read := func() models.AgentTransfer {
		t.Helper()
		var tr models.AgentTransfer
		require.NoError(t, app.DB.First(&tr, "id = ?", transfer.ID).Error)
		return tr
	}

	pick(first.ID)
	afterFirst := read()
	require.NotNil(t, afterFirst.SLA.PickedUpAt)
	require.NotNil(t, afterFirst.AgentID)
	assert.Equal(t, first.ID, *afterFirst.AgentID)

	// The deadline passes while the transfer sits in the queue again.
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("id = ?", transfer.ID).
		Update("sla_response_deadline", time.Now().Add(-time.Hour)).Error)
	require.Equal(t, 1, app.ReturnAgentTransfersToQueue(first.ID, org.ID))

	time.Sleep(20 * time.Millisecond) // the clock must be able to differ
	pick(second.ID)

	afterSecond := read()
	require.NotNil(t, afterSecond.AgentID)
	assert.Equal(t, second.ID, *afterSecond.AgentID)
	require.NotNil(t, afterSecond.SLA.PickedUpAt)
	assert.True(t, afterFirst.SLA.PickedUpAt.Equal(*afterSecond.SLA.PickedUpAt),
		"picked_up_at must keep the first pickup, got %s then %s", afterFirst.SLA.PickedUpAt, afterSecond.SLA.PickedUpAt)
	assert.False(t, afterSecond.SLA.Breached, "a later pickup must not re-evaluate the SLA breach")
}
