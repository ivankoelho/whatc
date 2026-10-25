package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"golang.org/x/crypto/bcrypt"
)

// audit run-1: lifecycle:deactivated-user-api-keys-and-sessions-not-revoked
func TestUserLifecycle_DeactivatedUserAPIKeyRejected(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())

	key := "whm_0123456789abcdef0123456789abcdef"
	hash, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, app.DB.Create(&models.APIKey{
		OrganizationID: org.ID, UserID: user.ID, Name: "k",
		KeyPrefix: key[4:20], KeyHash: string(hash), IsActive: true,
	}).Error)
	require.NoError(t, app.DB.Model(&models.User{}).Where("id = ?", user.ID).Update("is_active", false).Error)

	req := testutil.NewGETRequest(t)
	req.RequestCtx.Request.Header.Set("X-API-Key", key)
	got := middleware.AuthWithDB("test-secret", app.DB)(req)
	require.Nil(t, got, "a deactivated user's API key must not authenticate")
	require.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
}

// audit run-1: rbac:users:update-user-credential-takeover-no-hierarchy
func TestUserLifecycle_AdminCannotResetSuperAdminCredentials(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "org-admin",
		[]string{"users:read", "users:write"})
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	super := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())

	req := testutil.NewJSONRequest(t, map[string]any{"password": "taken-over-123"})
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetPathParam(req, "id", super.ID.String())
	require.NoError(t, app.UpdateUser(req))
	require.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

// audit run-1: lifecycle:super-admin-revocation-stale-permission-cache
func TestUserLifecycle_DemotionInvalidatesPermissionCache(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	actor := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	target := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithSuperAdmin())
	require.True(t, app.HasPermission(target.ID, models.ResourceUsers, models.ActionWrite, org.ID)) // warms cache

	req := testutil.NewJSONRequest(t, map[string]any{"is_super_admin": false})
	testutil.SetAuthContext(req, org.ID, actor.ID)
	testutil.SetPathParam(req, "id", target.ID.String())
	require.NoError(t, app.UpdateUser(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	require.False(t, app.HasPermission(target.ID, models.ResourceUsers, models.ActionWrite, org.ID),
		"demoted super admin must lose permissions immediately")
}
