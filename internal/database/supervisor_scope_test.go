package database_test

import (
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func formerSupervisorSAC() []string {
	var keys []string
	for _, k := range models.SystemRolePermissions()["agent"] {
		if !strings.HasPrefix(k, "sales_opportunities:") {
			keys = append(keys, k)
		}
	}
	return append(keys,
		"contacts:read", "contacts:write", "contacts:export", "tags:read", "templates:read",
		"conversations:view_all", "chat.assign:write",
		"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all",
		"team_chat:manage", "analytics:read", "analytics.agents:read",
		"call_logs:read", "teams:read", "users:read")
}

func TestBackfillOccurrenceViewAll_KeepsCaseSupervisionAndRespectsRevocation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestRoleWithKeys(t, db, org.ID, "admin", []string{"conversations:view_all"})
	sup := testutil.CreateTestRoleWithKeys(t, db, org.ID, "sup", []string{"conversations:view_all"})
	plain := testutil.CreateTestRoleWithKeys(t, db, org.ID, "plain", []string{"chat:write"})

	require.NoError(t, database.BackfillOccurrenceViewAll(db, testLog()))
	assert.Contains(t, roleKeys(t, db, sup.ID), "occurrences:view_all")
	assert.Contains(t, roleKeys(t, db, admin.ID), "occurrences:view_all")
	assert.NotContains(t, roleKeys(t, db, plain.ID), "occurrences:view_all")

	// An admin revokes it afterwards: a later start must not overrule that.
	require.NoError(t, db.Exec(`DELETE FROM role_permissions WHERE custom_role_id = ? AND permission_id =
		(SELECT id FROM permissions WHERE resource='occurrences' AND action='view_all')`, sup.ID).Error)
	other := testutil.CreateTestRoleWithKeys(t, db, org.ID, "other", []string{"conversations:view_all"})
	require.NoError(t, database.BackfillOccurrenceViewAll(db, testLog()))
	assert.NotContains(t, roleKeys(t, db, sup.ID), "occurrences:view_all")
	assert.NotContains(t, roleKeys(t, db, other.ID), "occurrences:view_all", "the organisation was already migrated")
}

func TestReconcileSupervisorSAC_OnlyTouchesTheUnmodifiedPreset(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))
	orgA := testutil.CreateTestOrganization(t, db)
	orgB := testutil.CreateTestOrganization(t, db)
	untouched := testutil.CreateTestRoleWithKeys(t, db, orgA.ID, "sac", append(formerSupervisorSAC(), "occurrences:view_all"))
	custom := testutil.CreateTestRoleWithKeys(t, db, orgB.ID, "sac", append(formerSupervisorSAC(), "contacts:delete"))
	require.NoError(t, db.Model(untouched).Update("name", "Supervisor de SAC").Error)
	require.NoError(t, db.Model(custom).Update("name", "Supervisor de SAC").Error)

	require.NoError(t, database.ReconcileSupervisorSACPreset(db, testLog()))

	k := roleKeys(t, db, untouched.ID)
	assert.NotContains(t, k, "conversations:view_all")
	assert.Contains(t, k, "conversations:view_team")
	assert.Contains(t, k, "occurrences:view_all", "case supervision is kept")
	assert.Contains(t, roleKeys(t, db, custom.ID), "conversations:view_all", "a customised role is never narrowed")

	// Idempotent.
	require.NoError(t, database.ReconcileSupervisorSACPreset(db, testLog()))
	assert.Contains(t, roleKeys(t, db, untouched.ID), "conversations:view_team")
}
