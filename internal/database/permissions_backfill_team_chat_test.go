package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)


func TestBackfillTeamChatPermissions_SystemRolesOnly(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "custom", []string{"chat:read", "chat:write"})

	require.NoError(t, database.BackfillTeamChatPermissions(db, testLog()))

	assert.ElementsMatch(t,
		[]string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"},
		filterTeamChat(roleKeys(t, db, admin.ID)))
	assert.ElementsMatch(t,
		[]string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"},
		filterTeamChat(roleKeys(t, db, manager.ID)))
	assert.ElementsMatch(t,
		[]string{"team_chat:read", "team_chat:write"},
		filterTeamChat(roleKeys(t, db, agent.ID)))
	assert.Empty(t, filterTeamChat(roleKeys(t, db, custom.ID)),
		"custom roles must never be widened by the backfill")
}

func TestBackfillTeamChatPermissions_IdempotentPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)

	require.NoError(t, database.BackfillTeamChatPermissions(db, testLog()))
	require.Contains(t, roleKeys(t, db, agent.ID), "team_chat:write")

	require.NoError(t, db.Exec(`
		DELETE FROM role_permissions
		WHERE custom_role_id = ?
		  AND permission_id = (SELECT id FROM permissions WHERE resource = ? AND action = ?)`,
		agent.ID, models.ResourceTeamChat, models.ActionWrite).Error)

	require.NoError(t, database.BackfillTeamChatPermissions(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, agent.ID), "team_chat:write",
		"second run must not re-grant to an organization already migrated")
}

func TestBackfillTeamChatPermissions_NotSeededDoesNothing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	org := testutil.CreateTestOrganization(t, db)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)

	require.NoError(t, database.BackfillTeamChatPermissions(db, testLog()))
	assert.Empty(t, roleKeys(t, db, agent.ID))
}

func TestSystemRolePermissions_TeamChat(t *testing.T) {
	roles := models.SystemRolePermissions()
	assert.Subset(t, roles["admin"], []string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"})
	assert.Subset(t, roles["manager"], []string{"team_chat:read", "team_chat:write", "team_chat:manage", "team_chat:delete"})
	assert.Subset(t, roles["agent"], []string{"team_chat:read", "team_chat:write"})
	assert.NotContains(t, roles["agent"], "team_chat:manage")
	assert.NotContains(t, roles["agent"], "team_chat:delete")
}

func filterTeamChat(keys []string) []string {
	var out []string
	for _, k := range keys {
		if len(k) > 10 && k[:10] == "team_chat:" {
			out = append(out, k)
		}
	}
	return out
}
