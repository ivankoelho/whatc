package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillSalesOpportunityPermissions_GrantsToChatWriteAndViewAll(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	agentRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "agent-like", []string{"chat:write"})
	managerRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "manager-like", []string{"chat:write", "conversations:view_all"})

	require.NoError(t, database.BackfillSalesOpportunityPermissions(db, testLog()))

	agentKeys := roleKeys(t, db, agentRole.ID)
	assert.Contains(t, agentKeys, "sales_opportunities:read")
	assert.Contains(t, agentKeys, "sales_opportunities:write")
	assert.NotContains(t, agentKeys, "sales_opportunities:view_all")

	managerKeys := roleKeys(t, db, managerRole.ID)
	assert.Contains(t, managerKeys, "sales_opportunities:view_all")
}

func TestBackfillSalesOpportunityPermissions_IdempotentPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "custom", []string{"chat:write"})

	require.NoError(t, database.BackfillSalesOpportunityPermissions(db, testLog()))
	require.Contains(t, roleKeys(t, db, role.ID), "sales_opportunities:write")

	// Simulate the admin manually revoking write after the first backfill.
	require.NoError(t, db.Exec(`
		DELETE FROM role_permissions
		WHERE custom_role_id = ?
		  AND permission_id = (SELECT id FROM permissions WHERE resource = ? AND action = ?)`,
		role.ID, models.ResourceSalesOpportunities, models.ActionWrite).Error)

	require.NoError(t, database.BackfillSalesOpportunityPermissions(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, role.ID), "sales_opportunities:write",
		"second run must not re-grant to an org already migrated")
}
