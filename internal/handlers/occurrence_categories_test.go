package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOccurrenceCategories_CreateTopLevelAndSubcategory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	top := testutil.NewJSONRequest(t, map[string]any{"name": "Impressora", "is_active": true})
	testutil.SetAuthContext(top, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(top))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(top))

	var parent models.OccurrenceCategory
	require.NoError(t, app.DB.Where("name = ?", "Impressora").First(&parent).Error)

	sub := testutil.NewJSONRequest(t, map[string]any{
		"name": "Impressora não imprime", "parent_id": parent.ID.String(), "is_active": true,
	})
	testutil.SetAuthContext(sub, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(sub))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(sub))
}

func TestOccurrenceCategories_RejectsSubcategoryOfSubcategory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	top := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Impressora"}
	require.NoError(t, app.DB.Create(&top).Error)
	sub := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Não imprime", ParentID: &top.ID}
	require.NoError(t, app.DB.Create(&sub).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name": "Tela azul", "parent_id": sub.ID.String(), "is_active": true,
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestOccurrenceCategories_RejectsReparentingCategoryThatHasChildren(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	a := models.OccurrenceCategory{OrganizationID: org.ID, Name: "A"}
	require.NoError(t, app.DB.Create(&a).Error)
	b := models.OccurrenceCategory{OrganizationID: org.ID, Name: "B", ParentID: &a.ID}
	require.NoError(t, app.DB.Create(&b).Error)
	x := models.OccurrenceCategory{OrganizationID: org.ID, Name: "X"}
	require.NoError(t, app.DB.Create(&x).Error)

	// Reparenting A (which already has child B) under top-level X must be
	// rejected — otherwise it produces X → A → B, two levels of subcategory
	// nesting the one-level rule exists to prevent.
	req := testutil.NewJSONRequest(t, map[string]any{
		"name": "A", "parent_id": x.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", a.ID.String())
	require.NoError(t, app.UpdateOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var reloaded models.OccurrenceCategory
	require.NoError(t, app.DB.First(&reloaded, "id = ?", a.ID).Error)
	assert.Nil(t, reloaded.ParentID)
}

func TestOccurrenceCategories_CreateOmittedIsActiveDefaultsTrue(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Sem is_active"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var category models.OccurrenceCategory
	require.NoError(t, app.DB.Where("name = ?", "Sem is_active").First(&category).Error)
	assert.True(t, category.IsActive)
}

func TestOccurrenceCategories_UpdateOmittedIsActiveLeavesItUnchanged(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	category := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Impressora", IsActive: true}
	require.NoError(t, app.DB.Create(&category).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Impressora renomeada"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", category.ID.String())
	require.NoError(t, app.UpdateOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var reloaded models.OccurrenceCategory
	require.NoError(t, app.DB.First(&reloaded, "id = ?", category.ID).Error)
	assert.True(t, reloaded.IsActive)
	assert.Equal(t, "Impressora renomeada", reloaded.Name)
}

func TestOccurrenceCategories_DeleteRejectedWhenOccurrenceUsesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	category := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Impressora"}
	require.NoError(t, app.DB.Create(&category).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Sem tinta",
		StageID: stage.ID, OpenedByUserID: user.ID, CategoryID: &category.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", category.ID.String())
	require.NoError(t, app.DeleteOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}
