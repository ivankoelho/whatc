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

func TestWhatHappened_CategoryFollowsTheReason(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	category := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Avaria", IsActive: true}
	require.NoError(t, app.DB.Create(&category).Error)

	create := testutil.NewJSONRequest(t, map[string]any{"name": "Produto chegou quebrado", "category_id": category.ID.String()})
	testutil.SetAuthContext(create, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceWhatHappened(create))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(create))

	var reason models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Produto chegou quebrado").First(&reason).Error)
	require.NotNil(t, reason.CategoryID)
	assert.Equal(t, category.ID, *reason.CategoryID)

	// Agent only says what happened; the protocol gets the reason's category.
	occReq := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"what_happened_id": reason.ID.String()})
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(occReq))
	var occ models.Occurrence
	require.NoError(t, app.DB.Where("organization_id = ? AND contact_id = ?", org.ID, contact.ID).First(&occ).Error)
	require.NotNil(t, occ.CategoryID)
	assert.Equal(t, category.ID, *occ.CategoryID)

	// A category from another organization is rejected.
	bad := testutil.NewJSONRequest(t, map[string]any{"name": "Outro", "category_id": uuid.NewString()})
	testutil.SetAuthContext(bad, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceWhatHappened(bad))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(bad))
}
