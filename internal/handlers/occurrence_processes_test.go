package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOccurrenceProcesses_SeedsFiveRealProcessesOnFirstRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var processes []models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).
		Order("position ASC").Find(&processes).Error)
	require.Len(t, processes, 5)

	names := make([]string, len(processes))
	for i, p := range processes {
		names[i] = p.Name
	}
	assert.Equal(t, []string{
		"Devolução após 24h",
		"Material já separado ou em romaneio",
		"Divergência no ato do recebimento",
		"Avaria — comunicação e abertura",
		"Desistência sem avaria",
	}, names)

	// The avaria process must carry its real restrictions/guidance text, not a
	// placeholder — proof the transcription from the validated HTML landed.
	var avaria models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").
		First(&avaria).Error)
	assert.Contains(t, avaria.Restrictions, "Vamos trocar seu produto.")
	assert.NotNil(t, avaria.ResponseMinutes)
	require.NotNil(t, avaria.WhatHappenedID)
	require.NotNil(t, avaria.CategoryID)
	assert.True(t, avaria.IsActive)

	var whatHappened models.OccurrenceWhatHappened
	require.NoError(t, app.DB.First(&whatHappened, "id = ?", *avaria.WhatHappenedID).Error)
	assert.Equal(t, "Produto com Avaria", whatHappened.Name, "must reuse the existing seeded reason, not create a duplicate")
}

func TestOccurrenceProcesses_SeedIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var count int64
	app.DB.Model(&models.OccurrenceProcess{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.EqualValues(t, 5, count)
}

func TestFindOrCreateWhatHappened_ReusesExistingByName(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID)) // seeds the six defaults, including "Produto com Avaria"

	found, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	var count int64
	app.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ? AND name = ?", org.ID, "Produto com Avaria").Count(&count)
	assert.EqualValues(t, 1, count, "must not create a duplicate row for a name that already exists")
	assert.Equal(t, "Produto com Avaria", found.Name)
}

// GORM replaces a zero-value field carrying a `default:` tag with that default,
// so an explicit IsActive:false must survive Create.
func TestOccurrenceProcess_InactiveIsStoredAsInactive(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Rascunho", IsActive: false}
	require.NoError(t, app.DB.Create(&p).Error)

	var reloaded models.OccurrenceProcess
	require.NoError(t, app.DB.First(&reloaded, "id = ?", p.ID).Error)
	assert.False(t, reloaded.IsActive)

	m := models.OccurrenceProcessMessage{
		OrganizationID: org.ID, ProcessID: p.ID,
		Stage: models.OccurrenceProcessMessageClosing, Content: "x", IsActive: false,
	}
	require.NoError(t, app.DB.Create(&m).Error)
	var reloadedMsg models.OccurrenceProcessMessage
	require.NoError(t, app.DB.First(&reloadedMsg, "id = ?", m.ID).Error)
	assert.False(t, reloadedMsg.IsActive)
}

// DB half of the "one active process per reason" invariant: the partial unique
// index must be created by AutoMigrate and really enforced.
func TestOccurrenceProcess_PartialUniqueIndexOneActivePerReason(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	wh := models.OccurrenceWhatHappened{OrganizationID: org.ID, Name: "Motivo", IsActive: true}
	require.NoError(t, app.DB.Create(&wh).Error)
	reason := wh.ID

	first := models.OccurrenceProcess{OrganizationID: org.ID, Name: "A", WhatHappenedID: &reason, IsActive: true}
	require.NoError(t, app.DB.Create(&first).Error)

	dupActive := models.OccurrenceProcess{OrganizationID: org.ID, Name: "B", WhatHappenedID: &reason, IsActive: true}
	assert.Error(t, app.DB.Create(&dupActive).Error, "second ACTIVE process for the same reason must be rejected")

	inactive := models.OccurrenceProcess{OrganizationID: org.ID, Name: "C", WhatHappenedID: &reason, IsActive: false}
	assert.NoError(t, app.DB.Create(&inactive).Error, "an INACTIVE process for the same reason is allowed")
}
