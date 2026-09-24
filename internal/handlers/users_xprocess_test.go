package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUpdateUser_SetsXProcessSellerCode(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	adminUser := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	target := testutil.CreateTestUser(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"xprocess_seller_code": "V042"})
	testutil.SetAuthContext(req, org.ID, adminUser.ID)
	req.RequestCtx.SetUserValue("id", target.ID.String())
	require.NoError(t, app.UpdateUser(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.User
	require.NoError(t, app.DB.First(&got, "id = ?", target.ID).Error)
	require.NotNil(t, got.XProcessSellerCode)
	assert.Equal(t, "V042", *got.XProcessSellerCode)
}
