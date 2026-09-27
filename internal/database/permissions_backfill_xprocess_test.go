package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillXProcessIntegrationPermission_GrantsToRolesWithAccountsWrite(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	adminRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "admin-like", []string{"accounts:write"})
	agentRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "agent-like", []string{"chat:write"})

	require.NoError(t, database.BackfillXProcessIntegrationPermission(db, testLog()))

	adminKeys := roleKeys(t, db, adminRole.ID)
	assert.Contains(t, adminKeys, "xprocess_integration:read")
	assert.Contains(t, adminKeys, "xprocess_integration:write")

	agentKeys := roleKeys(t, db, agentRole.ID)
	assert.NotContains(t, agentKeys, "xprocess_integration:read",
		"a role without accounts:write must not gain the X2 integration permission")
}

func TestBackfillXProcessIntegrationPermission_IdempotentPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "admin-like", []string{"accounts:write"})

	require.NoError(t, database.BackfillXProcessIntegrationPermission(db, testLog()))
	require.Contains(t, roleKeys(t, db, role.ID), "xprocess_integration:write")

	// Simulate the admin manually revoking it after the first backfill.
	require.NoError(t, db.Exec(`
		DELETE FROM role_permissions
		WHERE custom_role_id = ?
		  AND permission_id = (SELECT id FROM permissions WHERE resource = ? AND action = ?)`,
		role.ID, models.ResourceXProcessIntegration, models.ActionWrite).Error)

	require.NoError(t, database.BackfillXProcessIntegrationPermission(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, role.ID), "xprocess_integration:write",
		"second run must not re-grant to an org already migrated")
}
