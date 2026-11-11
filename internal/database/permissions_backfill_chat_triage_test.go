package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chat:triage goes to admin, manager and the SAC preset only; nothing else is widened.
func TestBackfillChatTriage_GrantsOnlyAdminManagerAndSAC(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)
	sac := testutil.CreateTestRoleExact(t, db, org.ID, "SAC", false, false, nil)
	vendas := testutil.CreateTestRoleExact(t, db, org.ID, "Vendas", false, false, nil)
	supervisor := testutil.CreateTestRoleExact(t, db, org.ID, "Supervisor de Loja", false, false, nil)
	juridico := testutil.CreateTestRoleExact(t, db, org.ID, "Jurídico", false, false, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "custom", []string{"chat:read", "chat:write"})

	require.NoError(t, database.BackfillChatTriagePermission(db, testLog()))

	for name, id := range map[string]any{"admin": admin.ID, "manager": manager.ID, "SAC": sac.ID} {
		assert.Contains(t, roleKeys(t, db, id), "chat:triage", name)
	}
	for name, id := range map[string]any{"agent": agent.ID, "Vendas": vendas.ID, "Supervisor de Loja": supervisor.ID, "Jurídico": juridico.ID, "custom": custom.ID} {
		assert.NotContains(t, roleKeys(t, db, id), "chat:triage", name)
	}
	// Only the one permission is added.
	assert.Equal(t, []string{"chat:triage"}, roleKeys(t, db, sac.ID))
}

func TestBackfillChatTriage_IsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	sac := testutil.CreateTestRoleExact(t, db, org.ID, "SAC", false, false, nil)

	require.NoError(t, database.BackfillChatTriagePermission(db, testLog()))
	first := roleKeys(t, db, sac.ID)
	require.NoError(t, database.BackfillChatTriagePermission(db, testLog()))
	assert.ElementsMatch(t, first, roleKeys(t, db, sac.ID), "a second run changes nothing")
}

// Once admin holds it the organization is considered served: an administrator who took it off the SAC role on
// purpose is not overridden by the next -migrate.
func TestBackfillChatTriage_DoesNotUndoARemovalFromSAC(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	sac := testutil.CreateTestRoleExact(t, db, org.ID, "SAC", false, false, nil)

	require.NoError(t, database.BackfillChatTriagePermission(db, testLog())) // grants admin + SAC
	require.NoError(t, db.Exec(`DELETE FROM role_permissions WHERE custom_role_id = ? AND permission_id =
		(SELECT id FROM permissions WHERE resource = 'chat' AND action = 'triage')`, sac.ID).Error)

	require.NoError(t, database.BackfillChatTriagePermission(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, sac.ID), "chat:triage")
}

// Without the permission seeded the backfill must say so, not quietly leave the SAC unable to triage.
func TestBackfillChatTriage_PermissionNotSeeded_IsAnErrorNotASilentSkip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, db.Exec(`DELETE FROM role_permissions`).Error)
	require.NoError(t, db.Exec(`DELETE FROM permissions WHERE resource = 'chat' AND action = 'triage'`).Error)

	err := database.BackfillChatTriagePermission(db, testLog())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not seeded")
}
