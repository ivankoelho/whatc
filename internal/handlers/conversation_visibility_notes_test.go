package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestSendTemplate_403WhenNotAssigned covers GAP 1 from the final Cycle 2
// review: SendTemplateMessage never called canInteractWithConversation, so in
// strict mode any org user could send a template into another agent's
// conversation. The visibility check runs immediately after the contact is
// loaded and before any WhatsApp account/client interaction, so no mock
// WhatsApp server is needed here — the request never gets that far.
func TestSendTemplate_403WhenNotAssigned(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID) // agent role: no view_all
	agentA := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	agentB := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	tpl := createTestTemplate(t, app, org.ID, account.Name)

	// Contact is being actively served by agent A.
	activeTransfer(t, app, org.ID, contact.ID, &agentA.ID, nil)
	enableStrictVisibility(t, app, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":    contact.ID.String(),
		"template_name": tpl.Name,
		"template_params": map[string]string{
			"name":     "Alice",
			"order_id": "ORD-1",
		},
	})
	testutil.SetAuthContext(req, org.ID, agentB.ID)

	require.NoError(t, app.SendTemplateMessage(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req),
		"agent B is not assigned to this conversation and must not be able to send a template into it")
}

// TestListConversationNotes_403WhenNotVisible covers GAP 2: conversation
// notes only gated on chat:read/chat:write, bypassing the visibility rule
// entirely. In strict mode, an agent not assigned to the conversation must be
// blocked, while the assigned agent still gets through.
func TestListConversationNotes_403WhenNotVisible(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID) // agent role: no view_all
	agentA := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	agentB := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	activeTransfer(t, app, org.ID, contact.ID, &agentA.ID, nil)
	enableStrictVisibility(t, app, org.ID)

	// Agent B (not assigned) must not see the notes.
	reqB := testutil.NewGETRequest(t)
	testutil.SetAuthContext(reqB, org.ID, agentB.ID)
	testutil.SetPathParam(reqB, "id", contact.ID.String())
	require.NoError(t, app.ListConversationNotes(reqB))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(reqB),
		"agent B is not assigned to this conversation and must not see its notes")

	// Agent A (assigned) can still list them.
	reqA := testutil.NewGETRequest(t)
	testutil.SetAuthContext(reqA, org.ID, agentA.ID)
	testutil.SetPathParam(reqA, "id", contact.ID.String())
	require.NoError(t, app.ListConversationNotes(reqA))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(reqA),
		"the assigned agent must still be able to list notes")
}
