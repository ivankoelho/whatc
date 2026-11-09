package handlers_test

import (
	"encoding/json"
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

// A webhook that changes message state must prove it came from Meta. These tests
// pin that nothing is accepted otherwise.

func statusBody(phoneID, wamid, status string) []byte {
	b, _ := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []map[string]any{{
			"id": "WABA-X",
			"changes": []map[string]any{{
				"field": "messages",
				"value": map[string]any{
					"messaging_product": "whatsapp",
					"metadata":          map[string]any{"phone_number_id": phoneID},
					"statuses": []map[string]any{{
						"id": wamid, "status": status, "timestamp": "1716152000", "recipient_id": "5511999990000",
					}},
				},
			}},
		}},
	})
	return b
}

// postWebhook sends body with the given X-Hub-Signature-256 value ("" = header absent).
func postWebhook(t *testing.T, app *handlers.App, body []byte, signature string) int {
	t.Helper()
	req := testutil.NewRequest(t)
	req.RequestCtx.Request.Header.SetMethod("POST")
	req.RequestCtx.Request.Header.SetContentType("application/json")
	if signature != "" {
		req.RequestCtx.Request.Header.Set("X-Hub-Signature-256", signature)
	}
	req.RequestCtx.Request.SetBody(body)
	require.NoError(t, app.WebhookHandler(req))
	return testutil.GetResponseStatusCode(req)
}

type hookEnv struct {
	app     *handlers.App
	org     *models.Organization
	account *models.WhatsAppAccount
	secret  string
}

// newHookEnv: an account with its own App Secret and a message awaiting status.
func newHookEnv(t *testing.T, ownSecret string, globalSecret string) (*hookEnv, *models.Message) {
	t.Helper()
	app := newAppForWebhook(t, "")
	app.Config.WhatsApp.AppSecret = globalSecret
	org := testutil.CreateTestOrganization(t, app.DB)
	acc := &models.WhatsAppAccount{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "hook-" + uuid.NewString()[:8], PhoneID: "phone-" + uuid.NewString()[:8], BusinessID: "waba-" + uuid.NewString()[:8],
		AccessToken: "tok", AppSecret: ownSecret, WebhookVerifyToken: "vt-" + uuid.NewString()[:6], Status: "active",
	}
	require.NoError(t, app.DB.Create(acc).Error)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	msg := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WhatsAppAccount: acc.Name,
		ContactID: contact.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "x",
		Status: models.MessageStatusSent, WhatsAppMessageID: "wamid." + uuid.NewString()[:10]}
	require.NoError(t, app.DB.Create(msg).Error)
	return &hookEnv{app: app, org: org, account: acc, secret: ownSecret}, msg
}

func messageStatus(t *testing.T, app *handlers.App, id uuid.UUID) models.MessageStatus {
	var m models.Message
	require.NoError(t, app.DB.First(&m, "id = ?", id).Error)
	return m.Status
}

func TestWebhookSecurity_SignedBodyUpdatesTheMessage(t *testing.T) {
	env, msg := newHookEnv(t, "own-secret-1", "")
	body := statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "delivered")
	assert.Equal(t, fasthttp.StatusOK, postWebhook(t, env.app, body, signWebhook(body, "own-secret-1")))
	assert.Eventually(t, func() bool { return messageStatus(t, env.app, msg.ID) == models.MessageStatusDelivered },
		3*time.Second, 50*time.Millisecond)
}

func TestWebhookSecurity_UnauthenticatedBodiesChangeNothing(t *testing.T) {
	env, msg := newHookEnv(t, "own-secret-2", "")
	body := statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "read")
	good := signWebhook(body, "own-secret-2")

	cases := map[string]struct {
		body []byte
		sig  string
	}{
		"header absent":              {body, ""},
		"wrong secret":               {body, signWebhook(body, "someone-elses-secret")},
		"no sha256= prefix":          {body, good[len("sha256="):]},
		"malformed hex":              {body, "sha256=zzzz"},
		"truncated signature":        {body, good[:len(good)-4]},
		"empty digest":               {body, "sha256="},
		"body changed after signing": {statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "failed"), good},
		"valid for another body":     {body, signWebhook([]byte(`{"a":1}`), "own-secret-2")},
	}
	for name, c := range cases {
		assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, c.body, c.sig), name)
	}
	// None of them may have touched the message, even after the async work would have run.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, models.MessageStatusSent, messageStatus(t, env.app, msg.ID))
}

func TestWebhookSecurity_NoSecretMeansRejectedNeverAccepted(t *testing.T) {
	env, msg := newHookEnv(t, "", "") // neither account nor instance secret
	body := statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "read")
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, body, signWebhook(body, "")), "an empty key is not a key")
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, body, signWebhook(body, "guess")))
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, body, ""))
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, models.MessageStatusSent, messageStatus(t, env.app, msg.ID))
}

func TestWebhookSecurity_InstanceSecretCoversAccountsWithoutTheirOwn(t *testing.T) {
	env, msg := newHookEnv(t, "", "instance-app-secret")
	body := statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "delivered")
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, body, signWebhook(body, "other")))
	assert.Equal(t, fasthttp.StatusOK, postWebhook(t, env.app, body, signWebhook(body, "instance-app-secret")))
}

// The owner of one account knows ITS secret. That must not let them forge events
// for another organization's account by listing both in one body.
func TestWebhookSecurity_CannotForgeAnotherAccountWithYourOwnSecret(t *testing.T) {
	mine, _ := newHookEnv(t, "attacker-secret", "")
	victim, vmsg := newHookEnv(t, "victim-secret", "")

	body, _ := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []map[string]any{{"id": "W", "changes": []map[string]any{
			{"field": "messages", "value": map[string]any{"metadata": map[string]any{"phone_number_id": mine.account.PhoneID}, "statuses": []any{}}},
			{"field": "messages", "value": map[string]any{"metadata": map[string]any{"phone_number_id": victim.account.PhoneID},
				"statuses": []map[string]any{{"id": vmsg.WhatsAppMessageID, "status": "read", "timestamp": "1", "recipient_id": "1"}}}},
		}}},
	})
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, mine.app, body, signWebhook(body, "attacker-secret")))
	// And a payload for the victim's account signed with the attacker's secret.
	vbody := statusBody(victim.account.PhoneID, vmsg.WhatsAppMessageID, "read")
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, victim.app, vbody, signWebhook(vbody, "attacker-secret")))
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, models.MessageStatusSent, messageStatus(t, victim.app, vmsg.ID))
}

func TestWebhookSecurity_UnattributablePayloadsAreRejected(t *testing.T) {
	env, _ := newHookEnv(t, "s", "")
	// No change at all: nothing to process, so nothing to protect.
	empty := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	assert.Equal(t, fasthttp.StatusOK, postWebhook(t, env.app, empty, ""))
	// A change that names no account cannot be vouched for.
	anon := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"","changes":[{"field":"messages","value":{"statuses":[{"id":"wamid.q","status":"read"}]}}]}]}`)
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, anon, signWebhook(anon, "s")))
	unknown := statusBody("phone-does-not-exist", "wamid.x", "read")
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, unknown, signWebhook(unknown, "s")))
	assert.Equal(t, fasthttp.StatusBadRequest, postWebhook(t, env.app, []byte("not json"), signWebhook([]byte("not json"), "s")))
}

// Template status updates carry only the WABA id: they are authenticated too.
func TestWebhookSecurity_TemplateStatusUpdatesNeedTheSignature(t *testing.T) {
	env, _ := newHookEnv(t, "tpl-secret", "")
	tpl := models.Template{OrganizationID: env.org.ID, WhatsAppAccount: env.account.Name, Name: "t_" + uuid.NewString()[:6],
		Language: "pt_BR", Category: "UTILITY", Status: "PENDING", BodyContent: "oi {{1}}"}
	require.NoError(t, env.app.DB.Create(&tpl).Error)
	body, _ := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []map[string]any{{"id": env.account.BusinessID, "changes": []map[string]any{{
			"field": "message_template_status_update",
			"value": map[string]any{"event": "APPROVED", "message_template_name": tpl.Name, "message_template_language": "pt_BR"},
		}}}},
	})
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, body, ""))
	assert.Equal(t, fasthttp.StatusForbidden, postWebhook(t, env.app, body, signWebhook(body, "nope")))
	assert.Equal(t, fasthttp.StatusOK, postWebhook(t, env.app, body, signWebhook(body, "tpl-secret")))
	assert.Eventually(t, func() bool {
		var got models.Template
		env.app.DB.First(&got, "id = ?", tpl.ID)
		return got.Status == "APPROVED"
	}, 3*time.Second, 50*time.Millisecond)
}

// A repeated delivery of the same signed webhook is harmless.
func TestWebhookSecurity_DuplicateSignedWebhookIsIdempotent(t *testing.T) {
	env, msg := newHookEnv(t, "dup-secret", "")
	body := statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "delivered")
	sig := signWebhook(body, "dup-secret")
	for i := 0; i < 3; i++ {
		assert.Equal(t, fasthttp.StatusOK, postWebhook(t, env.app, body, sig))
	}
	assert.Eventually(t, func() bool { return messageStatus(t, env.app, msg.ID) == models.MessageStatusDelivered }, 3*time.Second, 50*time.Millisecond)
	// A late, also signed, "sent" never moves it backwards.
	late := statusBody(env.account.PhoneID, msg.WhatsAppMessageID, "sent")
	assert.Equal(t, fasthttp.StatusOK, postWebhook(t, env.app, late, signWebhook(late, "dup-secret")))
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, models.MessageStatusDelivered, messageStatus(t, env.app, msg.ID))
}

func TestWebhookSecurity_GETVerification(t *testing.T) {
	env, _ := newHookEnv(t, "s", "")
	env.app.Config.WhatsApp.WebhookVerifyToken = "global-verify"
	verify := func(mode, token string) (int, string) {
		req := testutil.NewGETRequest(t)
		testutil.SetQueryParam(req, "hub.mode", mode)
		testutil.SetQueryParam(req, "hub.verify_token", token)
		testutil.SetQueryParam(req, "hub.challenge", "echo-me")
		require.NoError(t, env.app.WebhookVerify(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}
	code, body := verify("subscribe", "global-verify")
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, "echo-me", body)
	code, body = verify("subscribe", env.account.WebhookVerifyToken)
	assert.Equal(t, fasthttp.StatusOK, code, "an account's own token")
	assert.Equal(t, "echo-me", body)

	for name, c := range map[string][2]string{
		"wrong token": {"subscribe", "nope"}, "empty token": {"subscribe", ""},
		"wrong mode": {"unsubscribe", "global-verify"}, "token prefix": {"subscribe", "global-verif"},
	} {
		code, body = verify(c[0], c[1])
		assert.Equal(t, fasthttp.StatusForbidden, code, name)
		assert.NotContains(t, body, "echo-me", name)
	}
}
