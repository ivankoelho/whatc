package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Um papel que já administra o funil (occurrences.stages:write) ganha as
// onze permissões novas do catálogo, sem perder o que já tinha.
func TestBackfillHelpdeskCatalog_GrantsFromOccurrenceStagesWrite(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "gestor",
		[]string{"chat:read", "chat:write", "occurrences.stages:read", "occurrences.stages:write"})

	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()))

	keys := roleKeys(t, db, role.ID)
	assert.Contains(t, keys, "units:read")
	assert.Contains(t, keys, "units:write")
	assert.Contains(t, keys, "units:delete")
	assert.Contains(t, keys, "departments:read")
	assert.Contains(t, keys, "departments:write")
	assert.Contains(t, keys, "departments:delete")
	assert.Contains(t, keys, "occurrences.categories:read")
	assert.Contains(t, keys, "occurrences.categories:write")
	assert.Contains(t, keys, "occurrences.categories:delete")
	assert.Contains(t, keys, "occurrences.sla_policies:read")
	assert.Contains(t, keys, "occurrences.sla_policies:write")
	// Nada que já tinha foi perdido.
	assert.Contains(t, keys, "chat:read")
	assert.Contains(t, keys, "chat:write")
	assert.Contains(t, keys, "occurrences.stages:write")
}

// Um papel sem occurrences.stages:write — como "agent" — não administra o
// funil hoje, então não deve ganhar administração do catálogo.
func TestBackfillHelpdeskCatalog_RoleWithoutStagesWriteIsUntouched(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "atendente",
		[]string{"chat:read", "chat:write", "occurrences:read", "occurrences:write"})

	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()))

	keys := roleKeys(t, db, role.ID)
	assert.NotContains(t, keys, "units:read")
	assert.NotContains(t, keys, "departments:read")
	assert.NotContains(t, keys, "occurrences.categories:read")
	assert.NotContains(t, keys, "occurrences.sla_policies:read")
	// Nada foi removido do que já tinha.
	assert.Contains(t, keys, "chat:read")
	assert.Contains(t, keys, "occurrences:read")
}

func TestBackfillHelpdeskCatalog_IsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "gestor",
		[]string{"occurrences.stages:write"})

	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()))
	first := len(roleKeys(t, db, role.ID))

	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()))
	assert.Equal(t, first, len(roleKeys(t, db, role.ID)), "rodar duas vezes duplicou vinculos")
}

// A guarda por organização: uma org já migrada é pulada inteira, mesmo que
// um papel dela criado depois tenha a capacidade equivalente.
func TestBackfillHelpdeskCatalog_SkipsOrganisationAlreadyMigrated(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	migrada := testutil.CreateTestOrganization(t, db)
	testutil.CreateTestRoleWithKeys(t, db, migrada.ID, "ja-tem",
		[]string{"units:read"})
	criadoDepois := testutil.CreateTestRoleWithKeys(t, db, migrada.ID, "criado-depois",
		[]string{"occurrences.stages:write"})

	pendente := testutil.CreateTestOrganization(t, db)
	pendenteRole := testutil.CreateTestRoleWithKeys(t, db, pendente.ID, "gestor",
		[]string{"occurrences.stages:write"})

	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()))

	assert.NotContains(t, roleKeys(t, db, criadoDepois.ID), "departments:read",
		"organizacao ja migrada deveria ser pulada inteira")
	assert.Contains(t, roleKeys(t, db, pendenteRole.ID), "departments:read",
		"organizacao pendente deveria ser migrada")
}

func TestBackfillHelpdeskCatalog_NoOpWhenPermissionsNotSeeded(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	// Sem SeedPermissionsAndRoles: as linhas de permissao nao existem.
	org := testutil.CreateTestOrganization(t, db)
	_ = org

	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()),
		"sem as permissoes semeadas o backfill deve nao fazer nada, sem erro")
}
