package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestCreateOccurrence_InheritsUnitDepartmentFromSourceTransfer(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	unit := models.Unit{OrganizationID: org.ID, Name: "Loja Alagoinhas"}
	require.NoError(t, app.DB.Create(&unit).Error)
	department := models.Department{OrganizationID: org.ID, Name: "Logística"}
	require.NoError(t, app.DB.Create(&department).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Alagoinhas Logística", UnitID: &unit.ID, DepartmentID: &department.ID}
	require.NoError(t, app.DB.Create(&team).Error)
	transfer := models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "test",
		PhoneNumber: contact.PhoneNumber, TeamID: &team.ID,
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Sem entrega",
		"source_transfer_id": transfer.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.UnitID)
	require.NotNil(t, occ.DepartmentID)
	assert.Equal(t, unit.ID, *occ.UnitID)
	assert.Equal(t, department.ID, *occ.DepartmentID)
	assert.Equal(t, "whatsapp", occ.Source)
}

func TestCreateOccurrence_ManualUnitOverridesInheritedOne(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	inherited := models.Unit{OrganizationID: org.ID, Name: "Loja Feira"}
	require.NoError(t, app.DB.Create(&inherited).Error)
	override := models.Unit{OrganizationID: org.ID, Name: "Matriz"}
	require.NoError(t, app.DB.Create(&override).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Feira Logística", UnitID: &inherited.ID}
	require.NoError(t, app.DB.Create(&team).Error)
	transfer := models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "test",
		PhoneNumber: contact.PhoneNumber, TeamID: &team.ID,
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Reclassificado",
		"source_transfer_id": transfer.ID.String(), "unit_id": override.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.UnitID)
	assert.Equal(t, override.ID, *occ.UnitID)
}

func TestCreateOccurrence_MalformedSourceTransferIDFallsBackToManual(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Transfer ID malformado",
		"source_transfer_id": "not-a-uuid",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	// Without the fix, Source is left at Go's zero value "" here, and GORM's
	// default:'whatsapp' column silently applies on INSERT instead — wrongly
	// marking as "whatsapp" a case with no transfer actually recorded.
	assert.Equal(t, "manual", occ.Source)
	assert.Nil(t, occ.SourceTransferID)
}

func TestCreateOccurrence_ManualHasSourceManual(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Aberto sem conversa",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	assert.Equal(t, "manual", occ.Source)
	assert.Nil(t, occ.UnitID)
}

// Finding 4 of the final branch review: unit_id/department_id/category_id
// were accepted with a bare uuid.Parse and no check that the referenced row
// belongs to the caller's organization.
func TestCreateOccurrence_RejectsUnitFromAnotherOrg(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	foreignUnit := models.Unit{OrganizationID: otherOrg.ID, Name: "Unidade de outra org"}
	require.NoError(t, app.DB.Create(&foreignUnit).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Invasao",
		"unit_id": foreignUnit.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))

	var count int64
	app.DB.Model(&models.Occurrence{}).Where("contact_id = ?", contact.ID).Count(&count)
	assert.EqualValues(t, 0, count, "an occurrence referencing another org's unit must not be created")
}

func TestCreateOccurrence_RejectsDepartmentFromAnotherOrg(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	foreignDept := models.Department{OrganizationID: otherOrg.ID, Name: "Depto de outra org"}
	require.NoError(t, app.DB.Create(&foreignDept).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Invasao",
		"department_id": foreignDept.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestCreateOccurrence_RejectsCategoryFromAnotherOrg(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	foreignCategory := models.OccurrenceCategory{OrganizationID: otherOrg.ID, Name: "Categoria de outra org"}
	require.NoError(t, app.DB.Create(&foreignCategory).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Invasao",
		"category_id": foreignCategory.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

// UpdateOccurrence must apply the same org-ownership check as CreateOccurrence.
func TestUpdateOccurrence_RejectsUnitFromAnotherOrg(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Caso",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	foreignUnit := models.Unit{OrganizationID: otherOrg.ID, Name: "Unidade de outra org"}
	require.NoError(t, app.DB.Create(&foreignUnit).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"title": occ.Title, "unit_id": foreignUnit.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.UpdateOccurrence(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))

	require.NoError(t, app.DB.First(&occ, "id = ?", occ.ID).Error)
	assert.Nil(t, occ.UnitID, "the foreign unit must not have been applied")
}
