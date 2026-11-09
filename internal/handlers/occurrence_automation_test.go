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

// autoWorld is an iso world plus an account with approved templates, and a user
// who administers WhatsApp (accounts:read/write).
type autoWorld struct {
	*isoWorld
	account *models.WhatsAppAccount
	admin   *models.User
}

func (w *isoWorld) template(t *testing.T, account, name, lang, category, status, body string) models.Template {
	t.Helper()
	tpl := models.Template{OrganizationID: w.org.ID, WhatsAppAccount: account, Name: name, Language: lang,
		Category: category, Status: status, BodyContent: body, MetaTemplateID: uuid.NewString()}
	require.NoError(t, w.app.DB.Create(&tpl).Error)
	return tpl
}

func newAutoWorld(t *testing.T) *autoWorld {
	t.Helper()
	w := newIsoWorld(t)
	acc := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "waadmin"+uuid.NewString()[:5], []string{"accounts:read", "accounts:write"})
	admin := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))
	w.template(t, acc.Name, "recebimento_logistica", "pt_BR", "UTILITY", "APPROVED", "Olá, {{1}}! Pedido {{2}}. Protocolo: {{3}}")
	w.template(t, acc.Name, "recebimento_sem_pedido", "pt_BR", "UTILITY", "APPROVED", "Olá, {{1}}! Protocolo: {{2}}")
	return &autoWorld{isoWorld: w, account: acc, admin: admin}
}

func (w *autoWorld) save(t *testing.T, user uuid.UUID, body map[string]any) (int, string) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, w.org.ID, user)
	require.NoError(t, w.app.SetOccurrenceChannelConfig(req))
	return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
}

func (w *autoWorld) cfg(extra map[string]any) map[string]any {
	b := map[string]any{"whatsapp_account": w.account.Name, "template_name": "recebimento_logistica", "template_language": "pt_BR",
		"fallback_template_name": "recebimento_sem_pedido"}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestAutomation_OnlyWhatsAppAdminsConfigureIt(t *testing.T) {
	w := newAutoWorld(t)
	code, _ := w.save(t, w.logPorto.ID, w.cfg(nil))
	assert.Equal(t, fasthttp.StatusForbidden, code, "an agent")
	code, _ = w.save(t, w.supSAC.ID, w.cfg(nil))
	assert.Equal(t, fasthttp.StatusForbidden, code, "a supervisor without accounts:write")
	code, _ = w.save(t, w.admin.ID, w.cfg(nil))
	assert.Equal(t, fasthttp.StatusOK, code)
}

func TestAutomation_ValidatesAccountTemplateAndScope(t *testing.T) {
	w := newAutoWorld(t)
	other := newAutoWorld(t) // another organization with its own account and units

	code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"whatsapp_account": other.account.Name}))
	assert.Equal(t, fasthttp.StatusBadRequest, code, "another organization's account is rejected, not silently replaced")
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"unit_id": other.unitPorto.ID.String()}))
	assert.Equal(t, fasthttp.StatusNotFound, code, "a unit of another organization")
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"unit_id": w.unitOutra.ID.String(), "team_id": w.teamPorto.ID.String()}))
	assert.Equal(t, fasthttp.StatusBadRequest, code, "team outside the unit")

	w.template(t, w.account.Name, "promo", "pt_BR", "MARKETING", "APPROVED", "Oferta {{1}}")
	w.template(t, w.account.Name, "pendente", "pt_BR", "UTILITY", "PENDING", "Oi {{1}}")
	code, body := w.save(t, w.admin.ID, w.cfg(map[string]any{"template_name": "promo"}))
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	assert.Contains(t, body, "UTILITY", "marketing is not assumed to be utility")
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"template_name": "pendente"}))
	assert.Equal(t, fasthttp.StatusBadRequest, code, "not approved yet")
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"template_name": "nao_existe"}))
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"max_attempts": 99}))
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"on_failure": "explodir"}))
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	// Saving it inactive does not demand a template: it can be prepared first.
	code, _ = w.save(t, w.admin.ID, map[string]any{"whatsapp_account": w.account.Name, "is_active": false})
	assert.Equal(t, fasthttp.StatusOK, code)
	// ...and saving again updates the same row, it does not duplicate it.
	code, _ = w.save(t, w.admin.ID, w.cfg(map[string]any{"is_active": true}))
	assert.Equal(t, fasthttp.StatusOK, code)
	var n int64
	w.app.DB.Model(&models.OccurrenceChannelConfig{}).Where("organization_id = ? AND unit_id IS NULL AND team_id IS NULL", w.org.ID).Count(&n)
	assert.EqualValues(t, 1, n)
}

func (w *autoWorld) notificationFor(t *testing.T, occID uuid.UUID) models.OccurrenceNotification {
	t.Helper()
	var n models.OccurrenceNotification
	require.NoError(t, w.app.DB.Where("occurrence_id = ?", occID).First(&n).Error)
	return n
}

func TestAutomation_MostSpecificScopeGovernsTheIntent(t *testing.T) {
	w := newAutoWorld(t)
	portoAcc := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	w.template(t, portoAcc.Name, "recebimento_logistica", "pt_BR", "UTILITY", "APPROVED", "Olá, {{1}}! Pedido {{2}}. Protocolo: {{3}}")

	// 1. nothing configured: no intent to send, and no other account is used.
	n := w.notificationFor(t, w.forward(t, nil))
	assert.Equal(t, models.NotificationSkipped, n.Status)
	assert.Equal(t, "no_automation_configured", n.Reason)
	assert.Empty(t, n.WhatsAppAccount)

	// 2. the organization default applies to every store.
	code, _ := w.save(t, w.admin.ID, w.cfg(nil))
	require.Equal(t, fasthttp.StatusOK, code)
	n = w.notificationFor(t, w.forward(t, nil))
	assert.Equal(t, models.NotificationPending, n.Status)
	assert.True(t, n.Eligible)
	assert.Equal(t, w.account.Name, n.WhatsAppAccount)
	assert.Equal(t, "recebimento_logistica", n.TemplateName)
	assert.NotNil(t, n.NextAttemptAt)

	// 3. the unit's own configuration prevails over the organization's.
	code, _ = w.save(t, w.admin.ID, map[string]any{"unit_id": w.unitPorto.ID.String(), "whatsapp_account": portoAcc.Name,
		"template_name": "recebimento_logistica", "template_language": "pt_BR"})
	require.Equal(t, fasthttp.StatusOK, code)
	n = w.notificationFor(t, w.forward(t, nil))
	assert.Equal(t, portoAcc.Name, n.WhatsAppAccount)

	// 4. a team-level one prevails over the unit's.
	teamAcc := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	w.template(t, teamAcc.Name, "recebimento_logistica", "pt_BR", "UTILITY", "APPROVED", "Olá, {{1}}! Pedido {{2}}. Protocolo: {{3}}")
	code, _ = w.save(t, w.admin.ID, map[string]any{"team_id": w.teamPorto.ID.String(), "whatsapp_account": teamAcc.Name,
		"template_name": "recebimento_logistica", "template_language": "pt_BR"})
	require.Equal(t, fasthttp.StatusOK, code)
	n = w.notificationFor(t, w.forward(t, nil))
	assert.Equal(t, teamAcc.Name, n.WhatsAppAccount)

	// 5. switching the unit's off wins over the organization's too: no fallback to it.
	code, _ = w.save(t, w.admin.ID, map[string]any{"team_id": w.teamPorto.ID.String(), "whatsapp_account": teamAcc.Name, "is_active": false})
	require.Equal(t, fasthttp.StatusOK, code)
	n = w.notificationFor(t, w.forward(t, nil))
	assert.Equal(t, models.NotificationPrepared, n.Status, "inactive: intent recorded, nothing will send")
	assert.Equal(t, "automation_inactive", n.Reason)
	assert.False(t, n.Eligible)
}

func TestAutomation_IntentIsIdempotentAndSnapshotsTheConfig(t *testing.T) {
	w := newAutoWorld(t)
	require.Equal(t, fasthttp.StatusOK, func() int { c, _ := w.save(t, w.admin.ID, w.cfg(nil)); return c }())
	id := w.forward(t, nil)

	// The same business event arriving again must not create a second intent.
	var occ models.Occurrence
	require.NoError(t, w.app.DB.First(&occ, id).Error)
	var contact models.Contact
	require.NoError(t, w.app.DB.First(&contact, occ.ContactID).Error)
	require.NoError(t, w.app.NewForwardNotificationForTest(&occ, &contact))
	require.NoError(t, w.app.NewForwardNotificationForTest(&occ, &contact))
	var cnt int64
	w.app.DB.Model(&models.OccurrenceNotification{}).Where("occurrence_id = ?", id).Count(&cnt)
	assert.EqualValues(t, 1, cnt)

	// Changing the configuration afterwards does not rewrite the intent.
	other := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	w.template(t, other.Name, "recebimento_logistica", "pt_BR", "UTILITY", "APPROVED", "Olá {{1}} {{2}} {{3}}")
	code, _ := w.save(t, w.admin.ID, w.cfg(map[string]any{"whatsapp_account": other.Name, "fallback_template_name": ""}))
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, w.account.Name, w.notificationFor(t, id).WhatsAppAccount)
}
