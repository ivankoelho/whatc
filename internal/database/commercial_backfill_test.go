package database_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The classification by name happens once, in the boot that introduces the columns; later boots never redo it, so what an
// administrator set afterwards survives a restart. The test replays two boots exactly as RunMigrationWithProgress does:
// read CommercialColumnsExist BEFORE the migration, migrate, then ApplyCommercialBackfill.
func TestCommercialBackfill_RunsOnceAndPreservesTheAdminsChoice(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	team := &models.Team{OrganizationID: org.ID, Name: "Vendas Loja Teste", IsActive: true}
	require.NoError(t, db.Create(team).Error)
	dept := &models.Department{OrganizationID: org.ID, Name: "Vendas " + uuid.NewString()[:4], Active: true}
	require.NoError(t, db.Create(dept).Error)

	boot := func() {
		existed := database.CommercialColumnsExist(db)
		require.NoError(t, db.AutoMigrate(&models.Team{}, &models.Department{}))
		require.NoError(t, database.ApplyCommercialBackfill(db, existed))
	}
	teamFlag := func() bool {
		var x models.Team
		require.NoError(t, db.First(&x, team.ID).Error)
		return x.Commercial
	}
	deptFlag := func() bool {
		var x models.Department
		require.NoError(t, db.First(&x, dept.ID).Error)
		return x.Commercial
	}

	// the boot that introduces the columns: drop them, so the schema is as before the release
	require.NoError(t, db.Migrator().DropColumn(&models.Team{}, "commercial"))
	require.NoError(t, db.Migrator().DropColumn(&models.Department{}, "commercial"))
	require.False(t, database.CommercialColumnsExist(db))
	boot()
	assert.True(t, teamFlag(), "the introducing boot classifies the team named like a sales team")
	assert.True(t, deptFlag(), "and the department")

	// the administrator turns both off
	require.NoError(t, db.Model(team).Update("commercial", false).Error)
	require.NoError(t, db.Model(dept).Update("commercial", false).Error)

	// restarts, as many as you like: nothing is redone
	for i := 0; i < 3; i++ {
		boot()
		assert.False(t, teamFlag(), "boot %d must not re-flag the team", i+2)
		assert.False(t, deptFlag(), "boot %d must not re-flag the department", i+2)
	}

	// and the helper itself does nothing once the columns existed
	require.NoError(t, database.ApplyCommercialBackfill(db, true))
	assert.False(t, teamFlag())
}
