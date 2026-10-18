package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Criterion 9: team_chat:join_any lives only on the system admin role.
func TestBackfillTeamChatJoinAny_AdminOnly(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "custom",
		[]string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"})

	require.NoError(t, database.BackfillTeamChatJoinAnyPermission(db, testLog()))

	assert.Contains(t, roleKeys(t, db, admin.ID), "team_chat:join_any")
	for name, id := range map[string]any{"manager": manager.ID, "agent": agent.ID, "custom": custom.ID} {
		assert.NotContains(t, roleKeys(t, db, id), "team_chat:join_any", name)
	}
	// The existing four are untouched by this backfill.
	assert.ElementsMatch(t,
		[]string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"},
		filterTeamChat(roleKeys(t, db, custom.ID)))
}

func TestBackfillTeamChatJoinAny_IdempotentPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)

	require.NoError(t, database.BackfillTeamChatJoinAnyPermission(db, testLog()))
	first := roleKeys(t, db, admin.ID)
	require.Contains(t, first, "team_chat:join_any")

	// Running again changes nothing (no duplicate links, no other role widened).
	require.NoError(t, database.BackfillTeamChatJoinAnyPermission(db, testLog()))
	assert.ElementsMatch(t, first, roleKeys(t, db, admin.ID))
}

// The guard is per organization: once ANY role of the organization holds the
// permission, the backfill leaves that organization alone. Here a custom role was
// given it by hand and the admin role does not have it: it must not be added back.
func TestBackfillTeamChatJoinAny_SkipsAnOrganizationWhereSomeRoleHoldsIt(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "auditor", []string{"team_chat:read", "team_chat:join_any"})

	require.NoError(t, database.BackfillTeamChatJoinAnyPermission(db, testLog()))

	assert.NotContains(t, roleKeys(t, db, admin.ID), "team_chat:join_any")
	assert.Contains(t, roleKeys(t, db, custom.ID), "team_chat:join_any", "the hand-made grant is untouched")
}

func TestBackfillTeamChatJoinAny_NotSeededDoesNothing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)

	require.NoError(t, database.BackfillTeamChatJoinAnyPermission(db, testLog()))
	assert.Empty(t, roleKeys(t, db, admin.ID))
}

func TestSystemRolePermissions_JoinAnyIsAdminOnly(t *testing.T) {
	roles := models.SystemRolePermissions()
	assert.Contains(t, roles["admin"], "team_chat:join_any")
	assert.NotContains(t, roles["manager"], "team_chat:join_any")
	assert.NotContains(t, roles["agent"], "team_chat:join_any")
	// manage and delete stay where they were.
	assert.Contains(t, roles["manager"], "team_chat:manage")
}
