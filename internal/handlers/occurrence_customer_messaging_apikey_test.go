package handlers_test

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"golang.org/x/crypto/bcrypt"
)

// An API key acts as the user who created it: it carries no scopes of its own, so what
// the key may do is what that user's role allows. These tests authenticate with a real
// key through the real middleware and then call the endpoint, so they prove the rule is
// the same for integrations as for people in the interface.

func newAPIKey(t *testing.T, w *msgWorld, owner *models.User) string {
	t.Helper()
	raw := make([]byte, 16)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	key := "whm_" + hex.EncodeToString(raw) // whm_ + 32 hex = 36 chars
	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, w.app.DB.Create(&models.APIKey{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: owner.OrganizationID, UserID: owner.ID,
		Name: "integration-" + uuid.New().String()[:6], KeyPrefix: key[4:20], KeyHash: string(hash), IsActive: true,
	}).Error)
	return key
}

// callWithKey authenticates the request with the key and, only if the middleware lets it
// through, runs the handler. It returns the status the integration would see.
func callWithKey(t *testing.T, w *msgWorld, key string, h func(*fastglue.Request) error, body any, params map[string]string) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetHeader(req, "X-API-Key", key)
	testutil.SetHeader(req, "Idempotency-Key", uuid.New().String())
	for k, v := range params {
		testutil.SetPathParam(req, k, v)
	}
	authed := middleware.AuthWithDB(testutil.TestJWTSecret, w.app.DB)(req)
	if authed == nil {
		return testutil.GetResponseStatusCode(req) // refused by the middleware (401)
	}
	require.NoError(t, h(authed))
	return testutil.GetResponseStatusCode(authed)
}

func TestAPIKey_OfAUserWithoutChatWrite_IsRefusedOnEveryCustomerEndpoint(t *testing.T) {
	w := newMsgWorld(t)
	// Owns the conversation (so the endpoints reach the permission check), but the role has no chat:write.
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "key-owner-nochat-"+uuid.New().String()[:5], []string{"contacts:read", "chat:read"})
	owner := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, w.app.DB.Model(w.contact).Update("assigned_user_id", owner.ID).Error)
	key := newAPIKey(t, w, owner)

	id := map[string]string{"id": w.contact.ID.String()}
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.SendMessage,
		map[string]any{"type": "text", "content": map[string]string{"body": "oi"}}, id), "POST /contacts/{id}/messages")
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.SendMediaMessage,
		map[string]any{"contact_id": w.contact.ID.String()}, nil), "POST /messages/media")
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.SendTemplateMessage,
		map[string]any{"contact_id": w.contact.ID.String(), "template_name": "x"}, nil), "POST /messages/template")
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.SendReaction,
		map[string]any{"emoji": "👍"}, map[string]string{"id": w.contact.ID.String(), "message_id": uuid.New().String()}), "reaction")

	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, w.m.sentMessages, "nothing reaches the provider on behalf of the key")
}

func TestAPIKey_OfTheJuridico_CannotSendOrReplyOnCases(t *testing.T) {
	w := newMsgWorld(t)
	key := newAPIKey(t, w, w.jur)
	own := map[string]string{"id": w.occJur.ID.String()}
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.SendOccurrenceProtocol, map[string]any{"message": "texto livre"}, own))
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.SendOccurrenceProtocol, nil, own))
	assert.Equal(t, fasthttp.StatusForbidden, callWithKey(t, w, key, w.app.ReplyToOccurrence, map[string]any{"content": "oi"}, own))
	w.nothingLeft(t, "api key of the juridico")
}

func TestAPIKey_OfAuthorizedUsers_KeepsWorking(t *testing.T) {
	w := newMsgWorld(t)
	key := newAPIKey(t, w, w.agent) // an agent with chat:write who owns the conversation and the case

	assert.Equal(t, fasthttp.StatusOK, callWithKey(t, w, key, w.app.SendMessage,
		map[string]any{"type": "text", "content": map[string]string{"body": "pela integracao"}},
		map[string]string{"id": w.contact.ID.String()}))
	assert.Equal(t, fasthttp.StatusOK, callWithKey(t, w, key, w.app.SendOccurrenceProtocol, map[string]any{"message": "protocolo pela integracao"},
		map[string]string{"id": w.occ.ID.String()}))
	require.Eventually(t, func() bool { return w.mockHas("pela integracao") == 1 && w.mockHas("protocolo pela integracao") == 1 }, 2*time.Second, 20*time.Millisecond)

	// Who sent it is still recorded: the key's user.
	rows := auditRows(t, w.app.DB, w.occ.ID)
	require.NotEmpty(t, rows)
	assert.Equal(t, w.agent.ID, rows[0].UserID)
}

func TestAPIKey_InvalidOrInactive_NeverReachesTheHandler(t *testing.T) {
	w := newMsgWorld(t)
	assert.Equal(t, fasthttp.StatusUnauthorized, callWithKey(t, w, "whm_00000000000000000000000000000000", w.app.SendMessage,
		map[string]any{"type": "text", "content": map[string]string{"body": "x"}}, map[string]string{"id": w.contact.ID.String()}))

	key := newAPIKey(t, w, w.agent)
	require.NoError(t, w.app.DB.Model(&models.APIKey{}).Where("user_id = ?", w.agent.ID).Update("is_active", false).Error)
	assert.Equal(t, fasthttp.StatusUnauthorized, callWithKey(t, w, key, w.app.SendMessage,
		map[string]any{"type": "text", "content": map[string]string{"body": "x"}}, map[string]string{"id": w.contact.ID.String()}))
	w.nothingLeft(t, "invalid keys")
}
