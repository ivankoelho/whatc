package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Opening a request for a team is not working the conversation: the SAC can
// forward even while another agent holds the chat, and gains nothing from it.
func TestCreateAuth_ForwardingNeedsNoAccessToTheConversation(t *testing.T) {
	w := newIsoWorld(t)
	enableStrictVisibility(t, w.app, w.org.ID)
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	require.NoError(t, w.app.DB.Model(contact).Update("assigned_user_id", w.logOutra.ID).Error)

	create := func(userID uuid.UUID, body map[string]any) int {
		body["contact_id"] = contact.ID.String()
		body["title"] = "Pedido 45872"
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.org.ID, userID)
		require.NoError(t, w.app.CreateOccurrence(req))
		return testutil.GetResponseStatusCode(req)
	}

	// Without a destination team it is still the old rule: you need the conversation.
	assert.Equal(t, fasthttp.StatusForbidden, create(w.sac.ID, map[string]any{}))
	// Forwarded to a team: allowed.
	assert.Equal(t, fasthttp.StatusOK, create(w.sac.ID, map[string]any{"team_id": w.teamPorto.ID.String()}))

	// ...but it opens nothing else.
	var after models.Contact
	require.NoError(t, w.app.DB.First(&after, contact.ID).Error)
	require.NotNil(t, after.AssignedUserID)
	assert.Equal(t, w.logOutra.ID, *after.AssignedUserID, "the conversation keeps its owner")
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, w.app.GetMessages(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req), "no access to the chat history")

	// Cannot cite a conversation it cannot see as the origin.
	tr := models.AgentTransfer{OrganizationID: w.org.ID, ContactID: contact.ID, WhatsAppAccount: "x", PhoneNumber: "5511911111111", TeamID: &w.teamOutra.ID}
	require.NoError(t, w.app.DB.Create(&tr).Error)
	assert.Equal(t, fasthttp.StatusForbidden, create(w.sac.ID, map[string]any{"team_id": w.teamPorto.ID.String(), "source_transfer_id": tr.ID.String()}))

	// Seeing contacts is the floor.
	blind := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "semcontatos"+uuid.NewString()[:4], []string{"occurrences:read", "occurrences:write"})
	u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&blind.ID))
	assert.Equal(t, fasthttp.StatusForbidden, create(u.ID, map[string]any{"team_id": w.teamPorto.ID.String()}))
	// Needs the occurrences:write permission too.
	reader := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "leitor"+uuid.NewString()[:4], []string{"occurrences:read", "contacts:read"})
	r := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&reader.ID))
	assert.Equal(t, fasthttp.StatusForbidden, create(r.ID, map[string]any{"team_id": w.teamPorto.ID.String()}))
}

func TestCreateAuth_TargetMustBeValid(t *testing.T) {
	w := newIsoWorld(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	create := func(body map[string]any) int {
		body["contact_id"] = contact.ID.String()
		body["title"] = "x"
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
		require.NoError(t, w.app.CreateOccurrence(req))
		return testutil.GetResponseStatusCode(req)
	}

	other := newIsoWorld(t) // another organization with its own teams
	assert.Equal(t, fasthttp.StatusNotFound, create(map[string]any{"team_id": other.teamPorto.ID.String()}), "team of another org")
	assert.Equal(t, fasthttp.StatusBadRequest, create(map[string]any{"team_id": w.teamPorto.ID.String(), "unit_id": w.unitOutra.ID.String()}), "unit that is not the team's")
	require.NoError(t, w.app.DB.Model(w.teamOutra).Update("is_active", false).Error)
	assert.Equal(t, fasthttp.StatusBadRequest, create(map[string]any{"team_id": w.teamOutra.ID.String()}), "inactive team")

	// A contact of another organization is not found, whatever the team.
	foreign := testutil.CreateTestContact(t, other.app.DB, other.org.ID)
	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": foreign.ID.String(), "title": "x", "team_id": w.teamPorto.ID.String()})
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	require.NoError(t, w.app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}
