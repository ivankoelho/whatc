package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// The route-level RBAC hook in main.go is a no-op, so every handler that
// changes org configuration must check its own permission. A member whose
// role only allows chatting must be refused by all of them (audit run-1:
// rbac:roles, rbac:sso-settings, rbac:route-hook-noop).
func TestRBACGuards_ChatOnlyMemberIsForbidden(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "chat-only",
		[]string{"chat:read", "chat:write"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	handlers := map[string]func(*fastglue.Request) error{
		"CreateRole":            app.CreateRole,
		"UpdateRole":            app.UpdateRole,
		"DeleteRole":            app.DeleteRole,
		"GetSSOSettings":        app.GetSSOSettings,
		"UpdateSSOProvider":     app.UpdateSSOProvider,
		"DeleteSSOProvider":     app.DeleteSSOProvider,
		"ListWebhooks":          app.ListWebhooks,
		"CreateWebhook":         app.CreateWebhook,
		"UpdateWebhook":         app.UpdateWebhook,
		"DeleteWebhook":         app.DeleteWebhook,
		"TestWebhook":           app.TestWebhook,
		"CreateCustomAction":    app.CreateCustomAction,
		"UpdateCustomAction":    app.UpdateCustomAction,
		"DeleteCustomAction":    app.DeleteCustomAction,
		"CreateCampaign":        app.CreateCampaign,
		"UpdateCampaign":        app.UpdateCampaign,
		"DeleteCampaign":        app.DeleteCampaign,
		"StartCampaign":         app.StartCampaign,
		"PauseCampaign":         app.PauseCampaign,
		"CancelCampaign":        app.CancelCampaign,
		"RetryFailed":           app.RetryFailed,
		"ImportRecipients":      app.ImportRecipients,
		"CreateTemplate":        app.CreateTemplate,
		"UpdateTemplate":        app.UpdateTemplate,
		"DeleteTemplate":        app.DeleteTemplate,
		"SubmitTemplate":        app.SubmitTemplate,
		"SyncTemplates":         app.SyncTemplates,
		"CreateFlow":            app.CreateFlow,
		"UpdateFlow":            app.UpdateFlow,
		"DeleteFlow":            app.DeleteFlow,
		"PublishFlow":           app.PublishFlow,
		"CreateKeywordRule":     app.CreateKeywordRule,
		"UpdateKeywordRule":     app.UpdateKeywordRule,
		"DeleteKeywordRule":     app.DeleteKeywordRule,
		"CreateAIContext":       app.CreateAIContext,
		"UpdateAIContext":       app.UpdateAIContext,
		"DeleteAIContext":       app.DeleteAIContext,
		"CreateCatalog":         app.CreateCatalog,
		"DeleteCatalog":         app.DeleteCatalog,
		"CreateCatalogProduct":  app.CreateCatalogProduct,
		"CreateCannedResponse":  app.CreateCannedResponse,
		"UpdateCannedResponse":  app.UpdateCannedResponse,
		"DeleteCannedResponse":  app.DeleteCannedResponse,
		"UpdateBusinessProfile": app.UpdateBusinessProfile,
		"UpdateProfilePicture":  app.UpdateProfilePicture,
		"SubscribeApp":          app.SubscribeApp,
		"TestAccountConnection": app.TestAccountConnection,
	}

	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			req := testutil.NewJSONRequest(t, map[string]any{})
			testutil.SetAuthContext(req, org.ID, user.ID)
			testutil.SetPathParam(req, "id", "00000000-0000-0000-0000-000000000001")
			testutil.SetPathParam(req, "provider", "custom")
			_ = h(req)
			if got := testutil.GetResponseStatusCode(req); got != fasthttp.StatusForbidden {
				t.Fatalf("%s: status %d, want 403", name, got)
			}
		})
	}
}
