package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M3b: a verification code sent to a number we do not know creates the contact
// (a message needs one) but does not put it in the carteira of the API key owner.
// Ordinary templates keep assigning it, as before.
func TestSendTemplate_NewNumber_AuthenticationLeavesContactUnassigned(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	plain := testutil.CreateTestUser(t, app.DB, org.ID)
	account := createTestAccount(t, app, org.ID)
	tpl := createAuthTemplate(t, app, org.ID, account.Name)
	const phone = "+5511933330001"

	// The same number already exists in ANOTHER organization: it must not be touched or reused.
	foreign := testutil.CreateTestContactWith(t, app.DB, otherOrg.ID, testutil.WithPhoneNumber("5511933330001"))

	req := testutil.NewJSONRequest(t, map[string]any{
		"phone_number": phone, "template_name": tpl.Name,
		"template_params": map[string]string{"1": testOTP},
	})
	testutil.SetAuthContext(req, org.ID, admin.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	var created models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number LIKE ?", org.ID, "%933330001").First(&created).Error)
	assert.Nil(t, created.AssignedUserID, "not assigned to the owner of the API key")
	assert.Equal(t, org.ID, created.OrganizationID, "created in the caller's organization")
	assert.NotEqual(t, foreign.ID, created.ID)
	assert.Nil(t, created.LastMessageAt, "the code does not make it an active conversation")

	var transfers int64
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", created.ID).Count(&transfers).Error)
	assert.Zero(t, transfers)

	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", created.ID).First(&msg).Error)
	assert.NotContains(t, msg.Content, testOTP)

	// Unassigned contacts are an ordinary state (a number that wrote in first):
	// whoever may see all contacts still sees it, a user without that permission does not.
	assert.True(t, app.CanViewConversationByID(admin.ID, org.ID, created.ID))
	assert.False(t, app.CanViewConversationByID(plain.ID, org.ID, created.ID))
	assert.False(t, app.CanViewConversationByID(admin.ID, otherOrg.ID, created.ID), "no cross-organization access")
}

func TestSendTemplate_NewNumber_OrdinaryTemplateStillAssignsTheSender(t *testing.T) {
	t.Parallel()
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()

	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := createTestAccount(t, app, org.ID)
	tpl := createTestTemplate(t, app, org.ID, account.Name)

	req := testutil.NewJSONRequest(t, map[string]any{
		"phone_number": "+5511933330002", "template_name": tpl.Name,
		"template_params": map[string]string{"name": "Ana", "order_id": "1"},
	})
	testutil.SetAuthContext(req, org.ID, admin.ID)
	require.NoError(t, app.SendTemplateMessage(req))
	app.WaitForBackgroundTasks()

	var created models.Contact
	require.NoError(t, app.DB.Where("organization_id = ? AND phone_number LIKE ?", org.ID, "%933330002").First(&created).Error)
	require.NotNil(t, created.AssignedUserID)
	assert.Equal(t, admin.ID, *created.AssignedUserID)
}
