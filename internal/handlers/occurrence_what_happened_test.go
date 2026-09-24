package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
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

func TestWhatHappened_CategoryRuleOnCreateUpdateAndSeed(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	// A brand-new org's seeded processes also give their reasons a category.
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))
	var seeded []models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND id IN (?)", org.ID,
		app.DB.Model(&models.OccurrenceProcess{}).Select("what_happened_id").Where("organization_id = ?", org.ID)).
		Find(&seeded).Error)
	require.NotEmpty(t, seeded)
	for _, r := range seeded {
		assert.NotNil(t, r.CategoryID, "seeded reason %q has no category", r.Name)
	}

	avaria := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Avaria X", IsActive: true}
	troca := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Troca X", IsActive: true}
	require.NoError(t, app.DB.Create(&avaria).Error)
	require.NoError(t, app.DB.Create(&troca).Error)
	quebrado := models.OccurrenceWhatHappened{OrganizationID: org.ID, Name: "Quebrado X", IsActive: true, CategoryID: &avaria.ID}
	semCategoria := models.OccurrenceWhatHappened{OrganizationID: org.ID, Name: "Sem categoria X", IsActive: true}
	require.NoError(t, app.DB.Create(&quebrado).Error)
	require.NoError(t, app.DB.Create(&semCategoria).Error)

	load := func(contactID uuid.UUID) models.Occurrence {
		var occ models.Occurrence
		require.NoError(t, app.DB.Where("contact_id = ?", contactID).First(&occ).Error)
		return occ
	}

	// Agent's explicit category wins over the reason's.
	c1 := testutil.CreateTestContact(t, app.DB, org.ID)
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(createOccurrenceWith(t, app, org.ID, user.ID, c1.ID,
		map[string]any{"what_happened_id": quebrado.ID.String(), "category_id": troca.ID.String()})))
	assert.Equal(t, troca.ID, *load(c1.ID).CategoryID)

	// Reason without a category falls back to its process's category.
	proc := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Proc X", CategoryID: &troca.ID, WhatHappenedID: &semCategoria.ID, IsActive: true}
	require.NoError(t, app.DB.Create(&proc).Error)
	c2 := testutil.CreateTestContact(t, app.DB, org.ID)
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(createOccurrenceWith(t, app, org.ID, user.ID, c2.ID,
		map[string]any{"what_happened_id": semCategoria.ID.String(), "process_id": proc.ID.String()})))
	assert.Equal(t, troca.ID, *load(c2.ID).CategoryID)

	// Update: new reason, no category sent -> category follows the reason.
	occ := load(c2.ID)
	up := testutil.NewJSONRequest(t, map[string]any{"title": occ.Title, "what_happened_id": quebrado.ID.String()})
	testutil.SetAuthContext(up, org.ID, user.ID)
	testutil.SetPathParam(up, "id", occ.ID.String())
	require.NoError(t, app.UpdateOccurrence(up))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(up))
	assert.Equal(t, avaria.ID, *load(c2.ID).CategoryID)

	// Update with an explicit category keeps the agent's choice.
	up2 := testutil.NewJSONRequest(t, map[string]any{"title": occ.Title, "what_happened_id": quebrado.ID.String(), "category_id": troca.ID.String()})
	testutil.SetAuthContext(up2, org.ID, user.ID)
	testutil.SetPathParam(up2, "id", occ.ID.String())
	require.NoError(t, app.UpdateOccurrence(up2))
	assert.Equal(t, troca.ID, *load(c2.ID).CategoryID)

	// Startup backfill must not bring back a category the admin cleared.
	require.NoError(t, app.DB.Model(&models.OccurrenceWhatHappened{}).Where("id = ?", seeded[0].ID).Update("category_id", nil).Error)
	require.NoError(t, database.BackfillWhatHappenedCategory(app.DB))
	var cleared models.OccurrenceWhatHappened
	require.NoError(t, app.DB.First(&cleared, "id = ?", seeded[0].ID).Error)
	assert.Nil(t, cleared.CategoryID)
}
