package database_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// cleanAll truncates every table so each test starts with a blank slate.
func cleanAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	testutil.TruncateTables(db)
}

// --- SeedPermissionsAndRoles ---

func TestSeedPermissionsAndRoles_CreatesAllDefaultPermissions(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	err := database.SeedPermissionsAndRoles(db)
	require.NoError(t, err)

	var count int64
	db.Model(&models.Permission{}).Count(&count)

	expected := len(models.DefaultPermissions())
	assert.Equal(t, int64(expected), count, "all default permissions should be created")
}

func TestSeedPermissionsAndRoles_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	// Seed twice
	require.NoError(t, database.SeedPermissionsAndRoles(db))
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	var count int64
	db.Model(&models.Permission{}).Count(&count)

	expected := len(models.DefaultPermissions())
	assert.Equal(t, int64(expected), count, "idempotent: count should remain the same after two seeds")
}

func TestSeedPermissionsAndRoles_PermissionsHaveResourceAndAction(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	var perms []models.Permission
	db.Find(&perms)

	for _, p := range perms {
		assert.NotEmpty(t, p.Resource, "permission resource must not be empty")
		assert.NotEmpty(t, p.Action, "permission action must not be empty")
		assert.NotEqual(t, uuid.Nil, p.ID, "permission ID must be set")
	}
}

// --- SeedSystemRolesForOrg ---

func TestSeedSystemRolesForOrg_CreatesThreeSystemRoles(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	// Need permissions first
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Test Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)

	err := database.SeedSystemRolesForOrg(db, org.ID)
	require.NoError(t, err)

	var roles []models.CustomRole
	db.Where("organization_id = ? AND is_system = ?", org.ID, true).Find(&roles)
	assert.Len(t, roles, 3, "should create admin, manager, agent roles")

	names := make(map[string]bool)
	for _, r := range roles {
		names[r.Name] = true
	}
	assert.True(t, names["admin"], "admin role should exist")
	assert.True(t, names["manager"], "manager role should exist")
	assert.True(t, names["agent"], "agent role should exist")
}

func TestSeedSystemRolesForOrg_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Idempotent Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)

	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))
	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))

	var count int64
	db.Model(&models.CustomRole{}).Where("organization_id = ? AND is_system = ?", org.ID, true).Count(&count)
	assert.Equal(t, int64(3), count, "idempotent: still exactly 3 system roles")
}

func TestSeedSystemRolesForOrg_AgentIsDefault(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Default Role Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))

	var agentRole models.CustomRole
	err := db.Where("organization_id = ? AND name = ? AND is_system = ?", org.ID, "agent", true).First(&agentRole).Error
	require.NoError(t, err)
	assert.True(t, agentRole.IsDefault, "agent role should be the default role")
}

func TestSeedSystemRolesForOrg_AdminRoleHasAllPermissions(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Admin Perms Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&org).Error)
	require.NoError(t, database.SeedSystemRolesForOrg(db, org.ID))

	var adminRole models.CustomRole
	err := db.Where("organization_id = ? AND name = ? AND is_system = ?", org.ID, "admin", true).First(&adminRole).Error
	require.NoError(t, err)

	// Load permissions through the association
	var perms []models.Permission
	err = db.Model(&adminRole).Association("Permissions").Find(&perms)
	require.NoError(t, err)

	totalPerms := len(models.DefaultPermissions())
	assert.Equal(t, totalPerms, len(perms), "admin role should have all permissions")
}

// --- CreateDefaultAdmin ---

func TestCreateDefaultAdmin_CreatesOrgAndUser(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	cfg := &config.DefaultAdminConfig{
		Email:    "test-admin@example.com",
		Password: "testpassword123",
		FullName: "Test Admin",
	}

	err := database.CreateDefaultAdmin(db, cfg)
	require.NoError(t, err)

	// Verify user was created
	var user models.User
	err = db.Where("email = ?", cfg.Email).First(&user).Error
	require.NoError(t, err)
	assert.Equal(t, cfg.FullName, user.FullName)
	assert.True(t, user.IsActive)
	assert.True(t, user.IsSuperAdmin)
	assert.NotEmpty(t, user.PasswordHash)

	// Verify an organization was created
	var org models.Organization
	err = db.First(&org).Error
	require.NoError(t, err)
	assert.Equal(t, "Default Organization", org.Name)

	// Verify the user belongs to the organization
	assert.Equal(t, org.ID, user.OrganizationID)
}

func TestCreateDefaultAdmin_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	cfg := &config.DefaultAdminConfig{
		Email:    "idempotent-admin@example.com",
		Password: "pass123",
		FullName: "Idempotent Admin",
	}

	require.NoError(t, database.CreateDefaultAdmin(db, cfg))
	require.NoError(t, database.CreateDefaultAdmin(db, cfg))

	var count int64
	db.Model(&models.User{}).Where("email = ?", cfg.Email).Count(&count)
	assert.Equal(t, int64(1), count, "should not create duplicate admin")
}

func TestCreateDefaultAdmin_UsesExistingOrg(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)

	// Pre-create an organization
	existingOrg := models.Organization{
		BaseModel: models.BaseModel{ID: uuid.New()},
		Name:      "Pre-existing Org",
		Settings:  models.JSONB{},
	}
	require.NoError(t, db.Create(&existingOrg).Error)

	cfg := &config.DefaultAdminConfig{
		Email:    "admin-existing-org@example.com",
		Password: "password",
		FullName: "Admin",
	}

	err := database.CreateDefaultAdmin(db, cfg)
	require.NoError(t, err)

	var user models.User
	err = db.Where("email = ?", cfg.Email).First(&user).Error
	require.NoError(t, err)
	assert.Equal(t, existingOrg.ID, user.OrganizationID, "admin should belong to existing org")

	// Should not have created a new org
	var orgCount int64
	db.Model(&models.Organization{}).Count(&orgCount)
	assert.Equal(t, int64(1), orgCount, "should reuse existing organization")
}

// --- BackfillContactStatus ---

func TestBackfillContactStatus(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)

	// No messages at all -> stays 'new'
	silent := testutil.CreateTestContact(t, db, org.ID)

	// Recent inbound -> 'in_progress'
	recent := testutil.CreateTestContact(t, db, org.ID)
	now := time.Now()
	require.NoError(t, db.Model(recent).Update("last_inbound_at", now).Error)

	// Unread -> 'in_progress'
	unread := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(unread).Update("is_read", false).Error)

	// Old history, read -> 'resolved'
	old := testutil.CreateTestContact(t, db, org.ID)
	longAgo := now.Add(-72 * time.Hour)
	require.NoError(t, db.Model(old).Updates(map[string]any{
		"last_inbound_at": longAgo,
		"is_read":         true,
	}).Error)
	require.NoError(t, db.Create(&models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  org.ID,
		WhatsAppAccount: "test-account",
		ContactID:       old.ID,
		Direction:       models.DirectionIncoming,
		MessageType:     models.MessageTypeText,
		Content:         "older conversation",
	}).Error)

	require.NoError(t, database.BackfillContactStatus(db))

	statusOf := func(id uuid.UUID) string {
		var s string
		require.NoError(t, db.Model(&models.Contact{}).Where("id = ?", id).Pluck("contact_status", &s).Error)
		return s
	}

	assert.Equal(t, "new", statusOf(silent.ID), "a contact with no messages stays in the queue")
	assert.Equal(t, "in_progress", statusOf(recent.ID))
	assert.Equal(t, "in_progress", statusOf(unread.ID))
	assert.Equal(t, "resolved", statusOf(old.ID))
}

func TestBackfillContactStatus_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	c := testutil.CreateTestContact(t, db, org.ID)

	require.NoError(t, database.BackfillContactStatus(db))
	// An agent resolves it manually
	require.NoError(t, db.Model(c).Update("contact_status", "resolved").Error)
	// A second run must not overwrite real state
	require.NoError(t, database.BackfillContactStatus(db))

	var s string
	require.NoError(t, db.Model(&models.Contact{}).Where("id = ?", c.ID).Pluck("contact_status", &s).Error)
	assert.Equal(t, "resolved", s)
}
