package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentInitiatedTransfer(t *testing.T) {
	t.Parallel()

	t.Run("agent send opens an attendance assigned to the sender", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)

		var transfer models.AgentTransfer
		require.NoError(t, app.DB.Where("contact_id = ? AND status = ?",
			contact.ID, models.TransferStatusActive).First(&transfer).Error)
		assert.Equal(t, models.TransferSourceAgentInitiated, transfer.Source)
		require.NotNil(t, transfer.AgentID)
		assert.Equal(t, user.ID, *transfer.AgentID)
	})

	t.Run("does not open a second attendance when one is active", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)
		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)

		var count int64
		app.DB.Model(&models.AgentTransfer{}).
			Where("contact_id = ? AND status = ?", contact.ID, models.TransferStatusActive).
			Count(&count)
		assert.Equal(t, int64(1), count)
	})

	t.Run("cancels an active chatbot session", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		session := models.ChatbotSession{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			ContactID:       contact.ID,
			WhatsAppAccount: account.Name,
			PhoneNumber:     contact.PhoneNumber,
			Status:          models.SessionStatusActive,
		}
		require.NoError(t, app.DB.Create(&session).Error)

		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)

		var stored models.ChatbotSession
		require.NoError(t, app.DB.First(&stored, "id = ?", session.ID).Error)
		assert.Equal(t, models.SessionStatusCancelled, stored.Status,
			"human intervention must win over the bot")
	})
}

func TestSendTemplateOpensAttendance(t *testing.T) {
	t.Parallel()

	// A bare newTestApp(t) leaves a.WhatsApp nil, which panics inside the
	// async send goroutine (SendOutgoingMessage always sends templates async)
	// instead of failing gracefully. Use the mock WhatsApp server that the
	// other SendTemplateMessage tests in send_template_test.go use so the send
	// completes (successfully or not doesn't matter here — see below).
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID,
		testutil.WithContactAccount(account.Name))
	tpl := createTestTemplate(t, app, org.ID, account.Name)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":  contact.ID.String(),
		"template_id": tpl.ID.String(),
		// createTestTemplate's body is "Hello {{name}}! Your order {{order_id}}
		// has been confirmed." — both params are required or the handler 400s
		// before ever reaching SendOutgoingMessage.
		"template_params": map[string]string{
			"name":     "Alice",
			"order_id": "ORD-1",
		},
	})
	testutil.SetAuthContext(req, org.ID, user.ID)

	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	// The send itself is async; the attendance record is created
	// synchronously before the goroutine is even spawned, and that's what
	// matters here.
	var count int64
	app.DB.Model(&models.AgentTransfer{}).
		Where("contact_id = ? AND status = ? AND source = ?",
			contact.ID, models.TransferStatusActive, models.TransferSourceAgentInitiated).
		Count(&count)
	assert.Equal(t, int64(1), count,
		"an agent-sent template must open an attendance so the bot does not hijack the reply")
}
