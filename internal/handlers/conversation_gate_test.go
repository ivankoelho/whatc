package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// A member who cannot see a conversation must not be able to reach it
// through transfers or custom actions (audit run-1:
// whatc:transfers:create-and-resume-without-permission-or-conversation-gate,
// access:custom-action-execute:no-conversation-visibility-check).
func TestConversationGate_InvisibleContactIsForbidden(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agente-restrito",
		[]string{"chat:read", "chat:write", "transfers:read", "transfers:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	t.Run("create transfer to self", func(t *testing.T) {
		req := testutil.NewJSONRequest(t, map[string]any{
			"contact_id": contact.ID.String(),
			"agent_id":   agent.ID.String(),
		})
		testutil.SetAuthContext(req, org.ID, agent.ID)
		require.NoError(t, app.CreateAgentTransfer(req))
		require.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})

	t.Run("resume someone else's transfer", func(t *testing.T) {
		tr := createTestTransfer(t, app, org.ID, contact.ID, "acc", models.TransferStatusActive, &other.ID)
		req := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(req, org.ID, agent.ID)
		testutil.SetPathParam(req, "id", tr.ID.String())
		require.NoError(t, app.ResumeFromTransfer(req))
		require.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})

	t.Run("execute custom action", func(t *testing.T) {
		action := createTestCustomAction(t, app, org.ID, "url", models.ActionTypeURL,
			map[string]any{"url": "https://example.com/{{contact.phone}}"}, true, 0)
		req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String()})
		testutil.SetAuthContext(req, org.ID, agent.ID)
		testutil.SetPathParam(req, "id", action.ID.String())
		require.NoError(t, app.ExecuteCustomAction(req))
		require.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})
}
