package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOTP = "482913"

func createAuthTemplate(t *testing.T, app *handlers.App, orgID uuid.UUID, accountName string) *models.Template {
	t.Helper()
	tpl := &models.Template{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  orgID,
		WhatsAppAccount: accountName,
		Name:            "codigo_acesso_" + uuid.New().String()[:8],
		DisplayName:     "codigo_acesso",
		MetaTemplateID:  "meta-" + uuid.New().String()[:8],
		Category:        "AUTHENTICATION",
		Language:        "pt_BR",
		Status:          string(models.TemplateStatusApproved),
		BodyContent:     "Seu código de verificação é *{{1}}*. Para sua segurança, não o compartilhe.",
		Buttons:         models.JSONBArray{map[string]any{"type": "OTP", "text": "Copiar código", "otp_type": "COPY_CODE"}},
	}
	require.NoError(t, app.DB.Create(tpl).Error)
	return tpl
}

func TestApp_SendTemplateMessage_AuthenticationCodeIsMaskedAndStaysOutOfInbox(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	tpl := createAuthTemplate(t, app, org.ID, account.Name)

	var before models.Contact
	require.NoError(t, app.DB.First(&before, contact.ID).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":      contact.ID.String(),
		"template_name":   tpl.Name,
		"template_params": map[string]string{"1": testOTP},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	// The real code reaches Meta and only there. Full structure, so a missing or
	// reshaped OTP button (Meta error 131008) is caught.
	require.Len(t, mockServer.sentMessages, 1)
	assert.Equal(t, map[string]any{
		"messaging_product": "whatsapp",
		"type":              "template",
		"to":                contact.PhoneNumber,
		"template": map[string]any{
			"name":     tpl.Name,
			"language": map[string]any{"code": "pt_BR"},
			"components": []any{
				map[string]any{"type": "body", "parameters": []any{map[string]any{"type": "text", "text": testOTP}}},
				map[string]any{"type": "button", "sub_type": "url", "index": "0", "parameters": []any{map[string]any{"type": "text", "text": testOTP}}},
			},
		},
	}, mockServer.sentMessages[0])
	sent, _ := json.Marshal(mockServer.sentMessages[0])
	assert.Equal(t, 2, strings.Count(string(sent), testOTP), "code only in body and button")

	// API response carries no code.
	assert.NotContains(t, string(testutil.GetResponseBody(req)), testOTP)

	// Persisted message is masked everywhere.
	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&msg).Error)
	assert.Equal(t, "Seu código de verificação é *••••••*. Para sua segurança, não o compartilhe.", msg.Content)
	assert.Equal(t, tpl.Name, msg.TemplateName)
	assert.Equal(t, true, msg.Metadata[handlers.MetadataAuthCodeMasked])
	assert.Equal(t, handlers.InteractiveTypeAuthCode, msg.InteractiveData["type"])
	assert.Nil(t, msg.InteractiveData["buttons"], "no copy-code button stored")
	assert.Equal(t, models.MessageStatusSent, msg.Status)
	for _, col := range []string{"content", "template_params", "metadata", "interactive_data", "error_message"} {
		var raw string
		require.NoError(t, app.DB.Raw("SELECT COALESCE(CAST("+col+" AS text), '') FROM messages WHERE id = ?", msg.ID).Scan(&raw).Error)
		assert.NotContains(t, raw, testOTP, col)
	}

	// Inbox is untouched.
	var after models.Contact
	require.NoError(t, app.DB.First(&after, contact.ID).Error)
	assert.Equal(t, before.LastMessageAt, after.LastMessageAt)
	assert.Equal(t, before.LastMessagePreview, after.LastMessagePreview)
	assert.Equal(t, before.ContactStatus, after.ContactStatus)
	assert.Equal(t, before.AssignedUserID, after.AssignedUserID)

	var transfers int64
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Count(&transfers).Error)
	assert.Zero(t, transfers, "no attendance opened or claimed")
}

func TestApp_SendTemplateMessage_NormalTemplateStillUpdatesInbox(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	tpl := createTestTemplate(t, app, org.ID, account.Name)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":      contact.ID.String(),
		"template_name":   tpl.Name,
		"template_params": map[string]string{"name": "Alice", "order_id": "ORD-42"},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	var after models.Contact
	require.NoError(t, app.DB.First(&after, contact.ID).Error)
	assert.NotNil(t, after.LastMessageAt)
	assert.Equal(t, "[Template: Order Confirmation]", after.LastMessagePreview)

	var transfers int64
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Count(&transfers).Error)
	assert.NotZero(t, transfers, "a normal agent send still opens the attendance")
}

func TestApp_SendTemplateMessage_AuthenticationMetaFailureDoesNotExposeCode(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()
	mockServer.returnError = true
	mockServer.errorMessage = "Invalid parameter " + testOTP

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	tpl := createAuthTemplate(t, app, org.ID, account.Name)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "template_name": tpl.Name,
		"template_params": map[string]string{"1": testOTP},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	assert.NotContains(t, string(testutil.GetResponseBody(req)), testOTP)
	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&msg).Error)
	assert.Equal(t, models.MessageStatusFailed, msg.Status)
	assert.Contains(t, msg.ErrorMessage, "••••••", "error kept for support, code redacted")
	for _, col := range []string{"content", "template_params", "metadata", "interactive_data", "error_message"} {
		var raw string
		require.NoError(t, app.DB.Raw("SELECT COALESCE(CAST("+col+" AS text), '') FROM messages WHERE id = ?", msg.ID).Scan(&raw).Error)
		assert.NotContains(t, raw, testOTP, col)
	}
}

func TestApp_SendTemplateMessage_NormalTemplateHasNoButtonComponent(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	tpl := createTestTemplate(t, app, org.ID, account.Name)
	// An OTP-looking button on a non-authentication template must stay inert.
	require.NoError(t, app.DB.Model(tpl).Update("buttons", models.JSONBArray{map[string]any{"type": "OTP", "otp_type": "COPY_CODE"}}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "template_name": tpl.Name,
		"template_params": map[string]string{"name": "Alice", "order_id": "1"},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	require.Len(t, mockServer.sentMessages, 1)
	comps := mockServer.sentMessages[0]["template"].(map[string]any)["components"].([]any)
	require.Len(t, comps, 1)
	assert.Equal(t, "body", comps[0].(map[string]any)["type"])
}

func TestApp_SendTemplateMessage_AuthenticationExplicitButtonParamsAreRespected(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))
	tpl := createAuthTemplate(t, app, org.ID, account.Name)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "template_name": tpl.Name,
		"template_params": map[string]string{"1": testOTP},
		"button_params":   map[string]string{"0": "999000"}, // caller-supplied wins, as before
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	require.Len(t, mockServer.sentMessages, 1)
	comps := mockServer.sentMessages[0]["template"].(map[string]any)["components"].([]any)
	btn := comps[1].(map[string]any)["parameters"].([]any)[0].(map[string]any)
	assert.Equal(t, "999000", btn["text"])
}
