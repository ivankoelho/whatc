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
	assert.Contains(t, err.Error(), "NOTHING was deleted or changed")

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

// The guard is the admin and manager system roles: when either already holds it the organization is left alone (a
// new organization, or one already served).
func TestBackfillResolveSend_SkipsAnOrganizationWhoseAdminOrManagerAlreadyHoldsIt(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	var perm models.Permission
	require.NoError(t, db.Where("resource = ? AND action = ?", models.ResourceOccurrences, models.ActionResolveSend).First(&perm).Error)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, []models.Permission{perm})
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	supSAC := testutil.CreateTestRoleExact(t, db, org.ID, "Supervisor de SAC", false, false, nil)

	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))

	assert.Contains(t, roleKeys(t, db, admin.ID), "occurrences:resolve_send")
	assert.NotContains(t, roleKeys(t, db, manager.ID), "occurrences:resolve_send", "served already: left alone")
	assert.NotContains(t, roleKeys(t, db, supSAC.ID), "occurrences:resolve_send")
}

// A role somebody made by hand and gave the permission does not stop the intended roles from receiving it: only the
// system roles count as "already served". (The previous guard skipped the whole organization.)
func TestBackfillResolveSend_AHandMadeRoleDoesNotBlockTheIntendedRoles(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "auditoria", []string{"occurrences:read", "occurrences:resolve_send"})

	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))

	assert.Contains(t, roleKeys(t, db, admin.ID), "occurrences:resolve_send")
	assert.Contains(t, roleKeys(t, db, custom.ID), "occurrences:resolve_send", "the hand-made grant is untouched")
}

// The ordering the review found: the seed recreates a missing preset WITH the permission before the backfill runs.
// Admin and manager must still be granted.
func TestBackfillResolveSend_APresetRecreatedByTheSeedDoesNotBlockAdminAndManager(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	// The seed of the presets (it runs before the backfill in the migration) creates the missing ones with the permission.
	require.NoError(t, database.SeedTeamProfilesForOrg(db, org.ID))
	var sup models.CustomRole
	require.NoError(t, db.Where("organization_id = ? AND name = ?", org.ID, "Supervisor de SAC").First(&sup).Error)
	require.Contains(t, roleKeys(t, db, sup.ID), "occurrences:resolve_send", "precondition: the recreated preset carries it")

	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))

	assert.Contains(t, roleKeys(t, db, admin.ID), "occurrences:resolve_send")
	assert.Contains(t, roleKeys(t, db, manager.ID), "occurrences:resolve_send")
	// and the profiles that are not meant to have it still do not
	for _, name := range []string{"SAC", "Vendas", "Logística", "Jurídico"} {
		var r models.CustomRole
		if err := db.Where("organization_id = ? AND name = ?", org.ID, name).First(&r).Error; err == nil {
			assert.NotContains(t, roleKeys(t, db, r.ID), "occurrences:resolve_send", name)
		}
	}
}

// What an administrator's removal does, exactly as documented: removing it from a supervisor preset (or a custom
// role) is never undone; removing it from BOTH admin and manager is undone at the next -migrate (the data cannot
// tell "never granted" from "removed on purpose").
func TestBackfillResolveSend_WhatAnAdministratorRemovalDoes_AsDocumented(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	supStore := testutil.CreateTestRoleExact(t, db, org.ID, "Supervisor de Loja", false, false, nil)
	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))
	require.Contains(t, roleKeys(t, db, supStore.ID), "occurrences:resolve_send")

	// removed from a supervisor preset only: not undone
	require.NoError(t, db.Exec("DELETE FROM role_permissions WHERE custom_role_id = ? AND permission_id = (SELECT id FROM permissions WHERE resource = 'occurrences' AND action = 'resolve_send')", supStore.ID).Error)
	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, supStore.ID), "occurrences:resolve_send", "a supervisor removal is respected")

	// removed from BOTH admin and manager: granted again (documented limit)
	for _, id := range []any{admin.ID, manager.ID} {
		require.NoError(t, db.Exec("DELETE FROM role_permissions WHERE custom_role_id = ? AND permission_id = (SELECT id FROM permissions WHERE resource = 'occurrences' AND action = 'resolve_send')", id).Error)
	}
	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))
	assert.Contains(t, roleKeys(t, db, admin.ID), "occurrences:resolve_send")
	assert.Contains(t, roleKeys(t, db, manager.ID), "occurrences:resolve_send")
}

// Two boots at once cannot fail on a duplicate link.
func TestBackfillResolveSend_ConcurrentRuns_DoNotFail(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)

	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func() { errs <- database.BackfillResolveUncertainSendPermission(db, testLog()) }()
	}
	for i := 0; i < 6; i++ {
		assert.NoError(t, <-errs)
	}
	n := 0
	for _, k := range roleKeys(t, db, admin.ID) {
		if k == "occurrences:resolve_send" {
			n++
		}
	}
	assert.Equal(t, 1, n, "granted once")
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
