package database_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The preset roles of an organization as they existed BEFORE profile keys: name and seed description, no key.
func legacyPreset(t *testing.T, db *gorm.DB, orgID uuid.UUID, key string) *models.CustomRole {
	t.Helper()
	for _, tp := range models.TeamProfiles() {
		if tp.Key == key {
			role := &models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, Name: tp.Name, Description: tp.Description}
			require.NoError(t, db.Create(role).Error)
			return role
		}
	}
	t.Fatalf("no preset with key %q", key)
	return nil
}

func keyOf(t *testing.T, db *gorm.DB, roleID uuid.UUID) string {
	t.Helper()
	var r models.CustomRole
	require.NoError(t, db.First(&r, "id = ?", roleID).Error)
	if r.ProfileKey == nil {
		return ""
	}
	return *r.ProfileKey
}

func seededDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))
	return db
}

func triageHolders(t *testing.T, db *gorm.DB, orgID uuid.UUID) []string {
	t.Helper()
	var names []string
	require.NoError(t, db.Raw(`SELECT r.name FROM role_permissions rp
		JOIN permissions p ON p.id = rp.permission_id AND p.resource = 'chat' AND p.action = 'triage'
		JOIN custom_roles r ON r.id = rp.custom_role_id
		WHERE r.organization_id = ? ORDER BY r.name`, orgID).Scan(&names).Error)
	return names
}

// 1. Only the SAC preset gets chat:triage; the other presets get their key and nothing else.
func TestBackfillChatTriage_OnlyTheSACPreset(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	sac := legacyPreset(t, db, org.ID, "sac")
	vendas := legacyPreset(t, db, org.ID, "vendas")
	sup := legacyPreset(t, db, org.ID, "supervisor_loja")
	admin := testutil.CreateTestRoleExact(t, db, org.ID, "admin", true, false, nil)
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))

	assert.Equal(t, "sac", keyOf(t, db, sac.ID))
	assert.Equal(t, "vendas", keyOf(t, db, vendas.ID))
	assert.Equal(t, "supervisor_loja", keyOf(t, db, sup.ID))
	assert.Equal(t, []string{"SAC"}, triageHolders(t, db, org.ID))
	assert.Empty(t, keyOf(t, db, admin.ID), "system roles are never keyed")
	assert.NotContains(t, roleKeys(t, db, admin.ID), "chat:triage")
	assert.NotContains(t, roleKeys(t, db, manager.ID), "chat:triage")
}

// 2. A custom role that someone named "SAC" is not the preset: different description, so no key and no grant.
func TestBackfillChatTriage_ACustomRoleNamedSACIsNotThePreset(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	custom := &models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "SAC", Description: "feito à mão"}
	require.NoError(t, db.Create(custom).Error)
	legacyPreset(t, db, org.ID, "vendas") // something recognisable, so the organization is processed

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))

	assert.Empty(t, keyOf(t, db, custom.ID))
	assert.Empty(t, triageHolders(t, db, org.ID))
}

// 2b. Nor is a system role, even with the preset's name and description.
func TestBackfillChatTriage_ASystemRoleIsNeverAPreset(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	var tp models.TeamProfile
	for _, p := range models.TeamProfiles() {
		if p.Key == "sac" {
			tp = p
		}
	}
	sys := &models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: tp.Name, Description: tp.Description, IsSystem: true}
	require.NoError(t, db.Create(sys).Error)

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	assert.Empty(t, keyOf(t, db, sys.ID))
	assert.Empty(t, triageHolders(t, db, org.ID))
}

// 3. A SAC preset that an administrator renamed or re-described BEFORE keys existed cannot be recognised by
// name: it is left alone, without error (chat:triage can be given by hand).
func TestBackfillChatTriage_ALegacyPresetEditedBeforeKeysIsLeftAlone(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	renamed := legacyPreset(t, db, org.ID, "sac")
	require.NoError(t, db.Model(renamed).Update("name", "Atendimento").Error)
	edited := legacyPreset(t, db, org.ID, "vendas")
	require.NoError(t, db.Model(edited).Update("description", "outra coisa").Error)

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	assert.Empty(t, keyOf(t, db, renamed.ID))
	assert.Empty(t, keyOf(t, db, edited.ID))
	assert.Empty(t, triageHolders(t, db, org.ID))
}

// 4. A preset seeded WITH its key is identified by it from then on: renaming it does not make the seed create a
// second SAC, the key stays unique, chat:triage stays on it, and the backfill leaves the organization alone.
func TestSeed_RenamedPresetKeepsItsKeyAndIsNotDuplicated(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, database.SeedTeamProfilesForOrg(db, org.ID))

	var sac models.CustomRole
	require.NoError(t, db.Where("organization_id = ? AND profile_key = ?", org.ID, "sac").First(&sac).Error)
	assert.Contains(t, roleKeys(t, db, sac.ID), "chat:triage", "a new organization's SAC preset is seeded with it")
	require.NoError(t, db.Model(&sac).Update("name", "Atendimento ao Cliente").Error)

	require.NoError(t, database.SeedTeamProfilesForOrg(db, org.ID)) // a later boot
	var n int64
	require.NoError(t, db.Model(&models.CustomRole{}).Where("organization_id = ? AND profile_key = ?", org.ID, "sac").Count(&n).Error)
	assert.Equal(t, int64(1), n, "the renamed preset is still the SAC preset")
	require.NoError(t, db.Model(&models.CustomRole{}).Where("organization_id = ? AND name = ?", org.ID, "SAC").Count(&n).Error)
	assert.Zero(t, n, "no second role named SAC was created")

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	assert.Equal(t, []string{"Atendimento ao Cliente"}, triageHolders(t, db, org.ID))
}

// 5. The organization's manager already holds chat:triage but the SAC does not: the SAC is still given it,
// because the backfill never looks at what admin or manager hold.
func TestBackfillChatTriage_DoesNotDependOnManagerHoldingIt(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	sac := legacyPreset(t, db, org.ID, "sac")
	manager := testutil.CreateTestRoleExact(t, db, org.ID, "manager", true, false, nil)
	require.NoError(t, db.Exec(`INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT ?, id FROM permissions WHERE resource = 'chat' AND action = 'triage'`, manager.ID).Error)

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	assert.ElementsMatch(t, []string{"SAC", manager.Name}, triageHolders(t, db, org.ID))
	assert.Equal(t, "sac", keyOf(t, db, sac.ID))
}

// 6. An administrator removes chat:triage from the SAC role; running the migration again does not restore it.
func TestBackfillChatTriage_ManualRemovalSurvivesLaterRuns(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	sac := legacyPreset(t, db, org.ID, "sac")

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	require.Equal(t, []string{"SAC"}, triageHolders(t, db, org.ID))

	require.NoError(t, db.Exec(`DELETE FROM role_permissions WHERE custom_role_id = ? AND permission_id =
		(SELECT id FROM permissions WHERE resource = 'chat' AND action = 'triage')`, sac.ID).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	}
	assert.Empty(t, triageHolders(t, db, org.ID))
	assert.Equal(t, "sac", keyOf(t, db, sac.ID), "and the key stays")
}

// 7. Idempotent, and per organization: an organization already keyed is not touched while another is migrated.
func TestBackfillChatTriage_IdempotentAndPerOrganization(t *testing.T) {
	db := seededDB(t)
	legacyOrg := testutil.CreateTestOrganization(t, db)
	legacy := legacyPreset(t, db, legacyOrg.ID, "sac")
	newOrg := testutil.CreateTestOrganization(t, db)
	require.NoError(t, database.SeedTeamProfilesForOrg(db, newOrg.ID))
	require.NoError(t, db.Exec(`DELETE FROM role_permissions WHERE custom_role_id IN
		(SELECT id FROM custom_roles WHERE organization_id = ? AND profile_key = 'sac')`, newOrg.ID).Error) // removed on purpose

	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	assert.Equal(t, []string{"SAC"}, triageHolders(t, db, legacyOrg.ID))
	assert.Empty(t, triageHolders(t, db, newOrg.ID), "an organization that already has keys is not touched")

	before := triageHolders(t, db, legacyOrg.ID)
	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))
	assert.Equal(t, before, triageHolders(t, db, legacyOrg.ID))
	assert.Equal(t, "sac", keyOf(t, db, legacy.ID))
}

// The key is unique per organization among roles that are not deleted; a deleted role does not block it and
// another organization may use the same key.
func TestProfileKey_UniquePerOrganizationAmongActiveRoles(t *testing.T) {
	db := seededDB(t)
	orgA := testutil.CreateTestOrganization(t, db)
	orgB := testutil.CreateTestOrganization(t, db)
	key := "sac"
	mk := func(org uuid.UUID, name string) error {
		return db.Create(&models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, Name: name, ProfileKey: &key}).Error
	}
	first := &models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgA.ID, Name: "A1", ProfileKey: &key}
	require.NoError(t, db.Create(first).Error)

	assert.Error(t, mk(orgA.ID, "A2"), "a second role with the same key in the same organization")
	assert.NoError(t, mk(orgB.ID, "B1"), "the same key in another organization")
	require.NoError(t, db.Delete(first).Error)
	assert.NoError(t, mk(orgA.ID, "A3"), "a deleted role does not block the key")
}

// An existing installation: the column does not exist yet, so the boot that introduces it migrates the schema
// and then backfills. The data the installation already had is preserved.
func TestBackfillChatTriage_AnExistingInstallationBeforeTheColumn(t *testing.T) {
	db := seededDB(t)
	org := testutil.CreateTestOrganization(t, db)
	sac := legacyPreset(t, db, org.ID, "sac")
	juridico := legacyPreset(t, db, org.ID, "juridico")
	custom := &models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "SAC Extra", Description: "da loja"}
	require.NoError(t, db.Create(custom).Error)
	permsBefore := len(roleKeys(t, db, juridico.ID))

	// the schema as it was before this release
	require.NoError(t, db.Migrator().DropIndex(&models.CustomRole{}, "idx_custom_roles_org_profile_key"))
	require.NoError(t, db.Migrator().DropColumn(&models.CustomRole{}, "profile_key"))
	require.False(t, db.Migrator().HasColumn(&models.CustomRole{}, "profile_key"))

	require.NoError(t, db.AutoMigrate(&models.CustomRole{})) // what -migrate does first
	require.True(t, db.Migrator().HasColumn(&models.CustomRole{}, "profile_key"))
	require.True(t, db.Migrator().HasIndex(&models.CustomRole{}, "idx_custom_roles_org_profile_key"))
	require.NoError(t, database.BackfillTeamProfileKeysAndChatTriage(db, testLog()))

	assert.Equal(t, "sac", keyOf(t, db, sac.ID))
	assert.Equal(t, "juridico", keyOf(t, db, juridico.ID))
	assert.Empty(t, keyOf(t, db, custom.ID))
	assert.Equal(t, []string{"SAC"}, triageHolders(t, db, org.ID))
	assert.Len(t, roleKeys(t, db, juridico.ID), permsBefore, "other presets keep their permissions exactly")
	var name string
	require.NoError(t, db.Raw(`SELECT name FROM custom_roles WHERE id = ?`, sac.ID).Scan(&name).Error)
	assert.Equal(t, "SAC", name, "names are never rewritten")
}

// Without the permission seeded the backfill must say so, not quietly leave the SAC unable to triage.
func TestBackfillChatTriage_PermissionNotSeeded_IsAnErrorNotASilentSkip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, db.Exec(`DELETE FROM role_permissions`).Error)
	require.NoError(t, db.Exec(`DELETE FROM permissions WHERE resource = 'chat' AND action = 'triage'`).Error)

	err := database.BackfillTeamProfileKeysAndChatTriage(db, testLog())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not seeded")
}
