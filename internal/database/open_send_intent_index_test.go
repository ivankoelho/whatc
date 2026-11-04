package database_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The index that keeps a case at one open customer send is created by the migration, and when existing data
// would break it the migration STOPS: it reports the cases, changes nothing and creates nothing.
func TestEnsureOpenSendIntentUniqueness_ReportsDuplicatesStopsAndChangesNothing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+database.OpenSendIntentIndexName).Error)
	org := testutil.CreateTestOrganization(t, db)
	occID := uuid.New()
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM occurrence_send_intents WHERE organization_id = ?", org.ID).Error
		_ = database.EnsureOpenSendIntentUniqueness(db) // leave the shared test database with the index in place
	})

	mk := func(state string, age time.Duration) uuid.UUID {
		id := uuid.New()
		claimed := time.Now().Add(-age)
		require.NoError(t, db.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: org.ID, IdempotencyKey: uuid.NewString(),
			OccurrenceID: occID, UserID: uuid.New(), Kind: models.SendIntentKindProtocol, RequestHash: "h", State: state,
			MessageID: uuid.New(), ClaimedAt: claimed}).Error)
		return id
	}
	older, newer := mk(models.SendIntentUncertain, 2*time.Hour), mk(models.SendIntentProcessing, time.Hour)

	err := database.EnsureOpenSendIntentUniqueness(db)
	require.Error(t, err, "the migration must not carry on with the protection off")
	var dupErr *database.OpenSendIntentDuplicatesError
	require.True(t, errors.As(err, &dupErr))
	require.Len(t, dupErr.Duplicates, 1)
	d := dupErr.Duplicates[0]
	assert.Equal(t, org.ID, d.OrganizationID)
	assert.Equal(t, occID, d.OccurrenceID)
	assert.Equal(t, 2, d.OpenCount)
	assert.Equal(t, []uuid.UUID{older, newer}, d.SendIDs, "listed oldest first")
	assert.Contains(t, err.Error(), occID.String())
	assert.Contains(t, err.Error(), "nothing was changed")

	var rows int64
	require.NoError(t, db.Model(&models.OccurrenceSendIntent{}).Where("organization_id = ?", org.ID).Count(&rows).Error)
	assert.EqualValues(t, 2, rows, "the diagnostic deleted and changed nothing")
	var idx int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname = ?", database.OpenSendIntentIndexName).Scan(&idx).Error)
	assert.Zero(t, idx, "and created no index")

	// Once a person has settled one of them the same call goes through, and it is idempotent.
	require.NoError(t, db.Model(&models.OccurrenceSendIntent{}).Where("id = ?", older).Update("state", models.SendIntentSent).Error)
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname = ?", database.OpenSendIntentIndexName).Scan(&idx).Error)
	assert.EqualValues(t, 1, idx)
}

// occurrences:resolve_send reaches the roles meant to settle an unknown outcome and nobody else, once per
// organization, never overruling an administrator's later choice.
func TestBackfillResolveSend_GrantsAdminManagerAndSupervisorsOnly(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)
	supStore := testutil.CreateTestRoleExact(t, db, org.ID, "Supervisor de Loja", false, false, nil)
	supSAC := testutil.CreateTestRoleExact(t, db, org.ID, "Supervisor de SAC", false, false, nil)
	sac := testutil.CreateTestRoleExact(t, db, org.ID, "SAC", false, false, nil)
	sales := testutil.CreateTestRoleExact(t, db, org.ID, "Vendas", false, false, nil)
	logistics := testutil.CreateTestRoleExact(t, db, org.ID, "Logística", false, false, nil)

	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))

	const key = "occurrences:resolve_send"
	for name, id := range map[string]any{"admin": admin.ID, "manager": manager.ID, "Supervisor de Loja": supStore.ID, "Supervisor de SAC": supSAC.ID} {
		assert.Contains(t, roleKeys(t, db, id), key, name)
	}
	for name, id := range map[string]any{"agent": agent.ID, "SAC": sac.ID, "Vendas": sales.ID, "Logística": logistics.ID} {
		assert.NotContains(t, roleKeys(t, db, id), key, name)
	}

	// Idempotent: nothing more, nothing less.
	before := roleKeys(t, db, admin.ID)
	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))
	assert.ElementsMatch(t, before, roleKeys(t, db, admin.ID))
}

func TestBackfillResolveSend_SkipsAnOrganizationWhereARoleAlreadyHoldsIt(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "auditoria", []string{"occurrences:read", "occurrences:resolve_send"})

	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))

	assert.NotContains(t, roleKeys(t, db, admin.ID), "occurrences:resolve_send", "an administrator's choice is not overruled")
	assert.Contains(t, roleKeys(t, db, custom.ID), "occurrences:resolve_send")
}

// The name fits the column of the permissions table (no schema change) and the catalog knows it.
func TestResolveSendPermission_FitsTheExistingColumn(t *testing.T) {
	assert.LessOrEqual(t, len(models.ActionResolveSend), 20, "permissions.action is varchar(20)")
	var found bool
	for _, p := range models.DefaultPermissions() {
		if p.Resource == models.ResourceOccurrences && p.Action == models.ActionResolveSend {
			found = true
		}
	}
	assert.True(t, found)
	assert.Contains(t, models.SystemRolePermissions()["admin"], "occurrences:resolve_send")
	assert.Contains(t, models.SystemRolePermissions()["manager"], "occurrences:resolve_send")
	assert.NotContains(t, models.SystemRolePermissions()["agent"], "occurrences:resolve_send")
}
