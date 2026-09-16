package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillWhatHappened_GrantsFromCategoriesPermissions(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID,
		"gestor-categorias", []string{"occurrences.categories:read", "occurrences.categories:write"})

	require.NoError(t, database.BackfillWhatHappenedPermission(db, testLog()))

	keys := roleKeys(t, db, role.ID)
	assert.Contains(t, keys, "occurrences.what_happened:read")
	assert.Contains(t, keys, "occurrences.what_happened:write")
	assert.NotContains(t, keys, "occurrences.what_happened:delete",
		"a acao so eh concedida quando a fonte tem a mesma acao")
}

func TestBackfillWhatHappened_SkipsRoleWithoutCategoriesPermission(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "so-relatorios", []string{"analytics:read"})

	require.NoError(t, database.BackfillWhatHappenedPermission(db, testLog()))

	keys := roleKeys(t, db, role.ID)
	assert.NotContains(t, keys, "occurrences.what_happened:read")
	assert.Equal(t, []string{"analytics:read"}, keys)
}

func TestBackfillWhatHappened_IsIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID,
		"gestor-categorias", []string{"occurrences.categories:read", "occurrences.categories:write", "occurrences.categories:delete"})

	require.NoError(t, database.BackfillWhatHappenedPermission(db, testLog()))
	first := len(roleKeys(t, db, role.ID))
	require.NoError(t, database.BackfillWhatHappenedPermission(db, testLog()))
	assert.Equal(t, first, len(roleKeys(t, db, role.ID)), "rodar duas vezes duplicou vinculos")
}

// Reproduz exatamente o gap documentado no comentario da funcao: um papel
// que ja tinha occurrences.categories:read antes desta mudanca teria sido
// marcado "ja migrado" pela guarda do backfill do catalogo antigo — este
// teste garante que o novo backfill, por ser proprio, concede mesmo assim.
func TestBackfillWhatHappened_GrantsEvenWhenHelpdeskCatalogAlreadyMigrated(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	require.NoError(t, database.SeedPermissionsAndRoles(db))

	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID,
		"gestor-categorias", []string{"occurrences.categories:read"})

	// A organizacao ja passou pelo backfill antigo antes de what_happened existir.
	require.NoError(t, database.BackfillHelpdeskCatalogPermissions(db, testLog()))

	require.NoError(t, database.BackfillWhatHappenedPermission(db, testLog()))

	assert.Contains(t, roleKeys(t, db, role.ID), "occurrences.what_happened:read")
}
