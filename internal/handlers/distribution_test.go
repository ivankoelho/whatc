package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
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
