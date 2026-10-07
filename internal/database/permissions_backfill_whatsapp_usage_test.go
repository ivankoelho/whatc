package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemRolePermissions_WhatsAppUsageSplit(t *testing.T) {
	roles := models.SystemRolePermissions()
	assert.Contains(t, roles["admin"], "whatsapp_usage:read")
	assert.Contains(t, roles["admin"], "whatsapp_usage:write")
	assert.Contains(t, roles["manager"], "whatsapp_usage:read")
	assert.NotContains(t, roles["manager"], "whatsapp_usage:write", "the manager reads, only the admin edits prices")
	assert.NotContains(t, roles["agent"], "whatsapp_usage:read")
}

func TestBackfillWhatsAppUsagePermissions_AdminReadsAndWritesManagerReads(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	agent := testutil.CreateTestRoleExact(t, db, org.ID, "agent", true, true, nil)
	custom := testutil.CreateTestRoleWithKeys(t, db, org.ID, "custom", []string{"analytics:read"})

	require.NoError(t, database.BackfillWhatsAppUsagePermissions(db, testLog()))

	assert.ElementsMatch(t, []string{"whatsapp_usage:read", "whatsapp_usage:write"}, usageKeys(roleKeys(t, db, admin.ID)))
	assert.Equal(t, []string{"whatsapp_usage:read"}, usageKeys(roleKeys(t, db, manager.ID)))
	assert.Empty(t, usageKeys(roleKeys(t, db, agent.ID)))
	assert.Empty(t, usageKeys(roleKeys(t, db, custom.ID)), "custom roles are never touched")
	assert.Contains(t, roleKeys(t, db, custom.ID), "analytics:read", "and nothing is revoked")
}

func TestBackfillWhatsAppUsagePermissions_IsIdempotentAndRespectsARevocation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)

	require.NoError(t, database.BackfillWhatsAppUsagePermissions(db, testLog()))
	require.NoError(t, database.BackfillWhatsAppUsagePermissions(db, testLog()))
	assert.Len(t, usageKeys(roleKeys(t, db, admin.ID)), 2, "running twice grants nothing more")

	require.NoError(t, db.Exec(`
		DELETE FROM role_permissions
		WHERE custom_role_id = ?
		  AND permission_id = (SELECT id FROM permissions WHERE resource = ? AND action = ?)`,
		admin.ID, models.ResourceWhatsAppUsage, models.ActionWrite).Error)
	require.NoError(t, database.BackfillWhatsAppUsagePermissions(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, admin.ID), "whatsapp_usage:write", "an organization already migrated is not re-granted")
}

func usageKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if len(k) > 15 && k[:15] == "whatsapp_usage:" {
			out = append(out, k)
		}
	}
	return out
}
