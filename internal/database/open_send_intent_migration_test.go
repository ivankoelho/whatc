package database_test

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// scratchDatabase makes a brand new empty database on the test server and drops it afterwards, so the REAL
// migration function can run from zero without touching any existing database.
func scratchDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	name := "whatomate_mig_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	admin := *u
	admin.Path = "/postgres"
	adm, err := gorm.Open(postgres.Open(admin.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, adm.Exec("CREATE DATABASE "+name).Error)
	scratch := *u
	scratch.Path = "/" + name
	db, err := gorm.Open(postgres.Open(scratch.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
		_ = adm.Exec("DROP DATABASE IF EXISTS " + name).Error
		if sqlDB, e := adm.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

var adminCfg = &config.DefaultAdminConfig{Email: "admin@migration.test", Password: "migration-test-pw-1", FullName: "Migration Test"}

func indexExists(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname = ?", database.OpenSendIntentIndexName).Scan(&n).Error)
	return n == 1
}

func permissionExists(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&models.Permission{}).Where("resource = ? AND action = ?", models.ResourceOccurrences, models.ActionResolveSend).Count(&n).Error)
	return n == 1
}

// M2, normal path: the real migration from zero, then the last step creates the index; running it again changes nothing.
func TestMigration_FromZero_CreatesThePermissionAndTheIndex_AndIsIdempotent(t *testing.T) {
	db := scratchDatabase(t)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	assert.True(t, permissionExists(t, db), "the permission is seeded by the migration")
	assert.False(t, indexExists(t, db), "the index is the LAST step of the boot, not part of the schema step")

	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	assert.True(t, indexExists(t, db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))

	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))
}

// M2, duplicates: everything else completes first (permission seeded, roles, backfill), the index step stops with
// an error that says what to do, nothing is deleted or changed, and the recovery path it describes exists.
func TestMigration_WithDuplicates_EverythingElseIsDone_TheIndexStepStopsAndSaysWhatToDo(t *testing.T) {
	db := scratchDatabase(t)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	// Data from a database that ran the table before the rule existed: two open sends on one case.
	require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	org := testutil.CreateTestOrganization(t, db)
	occ := uuid.New()
	mk := func(state string) uuid.UUID {
		id := uuid.New()
		require.NoError(t, db.Create(&models.OccurrenceSendIntent{ID: id, OrganizationID: org.ID, IdempotencyKey: uuid.NewString(),
			OccurrenceID: occ, UserID: uuid.New(), Kind: models.SendIntentKindProtocol, RequestHash: "h", State: state,
			MessageID: uuid.New(), ClaimedAt: time.Now()}).Error)
		return id
	}
	a, b := mk(models.SendIntentUncertain), mk(models.SendIntentUncertain)

	// A boot with -migrate, in the order main.go runs it: the migration, the backfill, the index LAST.
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg), "the schema step no longer stops on the duplicates")
	require.NoError(t, database.BackfillResolveUncertainSendPermission(db, testLog()))
	err := database.EnsureOpenSendIntentUniqueness(db)
	require.Error(t, err)

	// Everything else is in place, in particular what the recovery needs.
	assert.True(t, permissionExists(t, db))
	// The message names the cases and the procedure, and promises nothing that needs hand-written SQL.
	var dup *database.OpenSendIntentDuplicatesError
	require.True(t, errors.As(err, &dup))
	msg := err.Error()
	for _, want := range []string{occ.String(), a.String(), b.String(), "NOTHING was deleted or changed", "WITHOUT -migrate",
		"occurrences:resolve_send", "/resolve", "run the migration again"} {
		assert.Contains(t, msg, want)
	}
	assert.NotContains(t, strings.ToLower(msg), "sql", "the procedure must not depend on hand-written SQL")

	// Nothing was deleted or changed, and the protection is reported as missing (so sends are refused).
	var rows int64
	require.NoError(t, db.Model(&models.OccurrenceSendIntent{}).Where("organization_id = ?", org.ID).Count(&rows).Error)
	assert.EqualValues(t, 2, rows)
	assert.False(t, indexExists(t, db))
	require.Error(t, database.VerifyOpenSendIntentGuard(db))

	// The recovery the message describes: a person settles one send, the migration is run again, the index appears.
	require.NoError(t, db.Model(&models.OccurrenceSendIntent{}).Where("id = ?", a).Updates(map[string]any{"state": models.SendIntentSent, "reconciled_at": time.Now()}).Error)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))
}

// M3: the check of the protection, with the index present, absent and invalid or wrong.
func TestVerifyOpenSendIntentGuard_Present_Absent_AndWrong(t *testing.T) {
	db := scratchDatabase(t)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))

	// absent
	err := database.VerifyOpenSendIntentGuard(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
	assert.Contains(t, err.Error(), "-migrate")

	// present
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))

	// wrong: same name, NOT unique
	require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	require.NoError(t, db.Exec(`CREATE INDEX `+database.OpenSendIntentIndexName+` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`).Error)
	err = database.VerifyOpenSendIntentGuard(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid unique index")

	// wrong: unique, but the predicate only covers one of the open states
	require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX `+database.OpenSendIntentIndexName+` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state = 'processing'`).Error)
	err = database.VerifyOpenSendIntentGuard(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected definition")

	// wrong: unique, partial, but on other columns
	require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX `+database.OpenSendIntentIndexName+` ON occurrence_send_intents (occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`).Error)
	require.Error(t, database.VerifyOpenSendIntentGuard(db))

	// wrong: not partial at all (it would forbid a second send ever)
	require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX `+database.OpenSendIntentIndexName+` ON occurrence_send_intents (organization_id, occurrence_id, kind)`).Error)
	require.Error(t, database.VerifyOpenSendIntentGuard(db))

	// invalid: a failed concurrent build leaves an index that is not valid
	require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX `+database.OpenSendIntentIndexName+` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = '%s'::regclass`, database.OpenSendIntentIndexName)).Error)
	err = database.VerifyOpenSendIntentGuard(db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "valid=false")
}

// M-A of the last review: CREATE UNIQUE INDEX IF NOT EXISTS only looks at the NAME, so an invalid or wrong index with
// that name used to make the migration "succeed" while the protection stayed off. Now the migration verifies what is
// there, fails, touches nothing, and says exactly what to do; after that the same migration creates it correctly.
func TestEnsureOpenSendIntentUniqueness_AnExistingWrongIndexFailsTheMigration_AndSaysHowToFixIt(t *testing.T) {
	db := testutil.ScratchDatabase(t)
	require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
	name := database.OpenSendIntentIndexName
	for label, ddl := range map[string]string{
		"not unique":       `CREATE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
		"covers one state": `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state = 'processing'`,
		"other columns":    `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
		"not partial":      `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind)`,
		// The shapes the previous substring check let through (found by the independent review): unique, valid, partial,
		// on the right columns, and still NOT the protection.
		"NOT IN of both states":             `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state NOT IN ('processing', 'uncertain')`,
		"OR on another column":              `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state = 'processing' OR kind = 'uncertain'`,
		"IN plus a time condition":          `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain') AND claimed_at < '2000-01-01'`,
		"both states written as OR":         `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state = 'processing' OR state = 'uncertain'`,
		"a third state":                     `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain', 'sent')`,
		"columns in another order":          `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (occurrence_id, organization_id, kind) WHERE state IN ('processing', 'uncertain')`,
		"an extra key column":               `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind, user_id) WHERE state IN ('processing', 'uncertain')`,
		"an included column":                `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) INCLUDE (user_id) WHERE state IN ('processing', 'uncertain')`,
		"an expression instead of a column": `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, lower(kind)) WHERE state IN ('processing', 'uncertain')`,
		// The six predicates the last independent review created: unique, valid, partial, right columns, and no protection.
		"two constants compared":        `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE 'processing' = 'uncertain'`,
		"two booleans compared":         `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE (state = 'processing') = (state = 'uncertain')`,
		"two arrays compared":           `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE ARRAY['processing', 'uncertain']::text[] = ARRAY[state, state]::text[]`,
		"the column inside the array":   `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE 'processing' = ANY (ARRAY[state, 'uncertain'])`,
		"booleans, constants first":     `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE ('processing' = state) = ('uncertain' = state)`,
		"the other column in the array": `CREATE UNIQUE INDEX ` + name + ` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE 'uncertain' = ANY (ARRAY['processing', state])`,
		"a hash index on a subset":      `CREATE INDEX ` + name + ` ON occurrence_send_intents USING hash (occurrence_id) WHERE state IN ('processing', 'uncertain')`,
	} {
		t.Run(label, func(t *testing.T) {
			require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+name).Error)
			require.NoError(t, db.Exec(ddl).Error)
			before := indexDef(t, db)

			err := database.EnsureOpenSendIntentUniqueness(db)
			require.Error(t, err, "the migration must not report success while the protection is off")
			var ie *database.OpenSendIntentIndexError
			require.True(t, errors.As(err, &ie))
			assert.Contains(t, err.Error(), "DROP INDEX "+name)
			assert.Contains(t, err.Error(), "lock_timeout", "the procedure carries the lock cautions")
			assert.Contains(t, err.Error(), "does not touch an existing index")
			assert.Equal(t, before, indexDef(t, db), "the existing index was left exactly as it was")

			// The documented fix works: drop it, run the migration again, and the protection is there and valid.
			require.NoError(t, db.Exec("DROP INDEX "+name).Error)
			require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
			require.NoError(t, database.VerifyOpenSendIntentGuard(db))
		})
	}

	t.Run("invalid", func(t *testing.T) {
		require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+name).Error)
		require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
		require.NoError(t, db.Exec(fmt.Sprintf(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = '%s'::regclass`, name)).Error)
		err := database.EnsureOpenSendIntentUniqueness(db)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "valid=false")
		assert.Contains(t, err.Error(), "DROP INDEX "+name)
	})

	t.Run("a correct index is left alone and the call is idempotent", func(t *testing.T) {
		require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+name).Error)
		require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
		def := indexDef(t, db)
		require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
		assert.Equal(t, def, indexDef(t, db))
	})
}

func indexDef(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var def string
	require.NoError(t, db.Raw("SELECT pg_get_indexdef(c.oid) FROM pg_class c WHERE c.relname = ?", database.OpenSendIntentIndexName).Scan(&def).Error)
	return def
}

// L-1 of the last review: the check looks at the table of the CURRENT schema only. A table of the same name with a
// perfect index in another schema must not satisfy it, and the migration must create the real one and leave the
// other alone.
func TestVerifyOpenSendIntentGuard_AnIndexInAnotherSchemaCountsForNothing(t *testing.T) {
	db := testutil.ScratchDatabase(t)
	require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
	require.NoError(t, db.Exec("CREATE SCHEMA other_sch").Error)
	require.NoError(t, db.Exec("CREATE TABLE other_sch.occurrence_send_intents (organization_id uuid, occurrence_id uuid, kind text, state text)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX "+database.OpenSendIntentIndexName+" ON other_sch.occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')").Error)

	var cur string
	require.NoError(t, db.Raw("SELECT current_schema()").Scan(&cur).Error)
	require.Equal(t, "public", cur)

	err := database.VerifyOpenSendIntentGuard(db)
	require.Error(t, err, "the perfect index of the other schema must not satisfy the guard")
	assert.Contains(t, err.Error(), "is missing")

	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))
	var inPublic, inOther int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = ?", database.OpenSendIntentIndexName).Scan(&inPublic).Error)
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE schemaname = 'other_sch' AND indexname = ?", database.OpenSendIntentIndexName).Scan(&inOther).Error)
	assert.EqualValues(t, 1, inPublic, "the real one was created where the table is")
	assert.EqualValues(t, 1, inOther, "and the other schema was left alone")
}

// The check of the structure, with the index the migration itself creates: it passes, and so does the second pass.
func TestVerifyOpenSendIntentGuard_TheIndexTheMigrationCreatesPasses(t *testing.T) {
	db := testutil.ScratchDatabase(t)
	require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))
}

// L-1 of the last review: when the reserved name is held by something that is NOT an index, the message must say what
// it found and must not send the operator to DROP INDEX (which fails with "is not an index"). It fails closed, and the
// migration never touches the object.
func TestEnsureOpenSendIntentUniqueness_ANameHeldByANonIndexObject_SaysSoAndTouchesNothing(t *testing.T) {
	name := database.OpenSendIntentIndexName
	for label, c := range map[string]struct{ ddl, kind, drop string }{
		"a sequence": {"CREATE SEQUENCE " + name, "sequence", "DROP SEQUENCE " + name},
		"a table":    {"CREATE TABLE " + name + " (id int)", "table", "DROP TABLE " + name},
		"a view":     {"CREATE VIEW " + name + " AS SELECT 1 AS x", "view", "DROP VIEW " + name},
	} {
		t.Run(label, func(t *testing.T) {
			db := testutil.ScratchDatabase(t)
			require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
			require.NoError(t, db.Exec(c.ddl).Error)

			err := database.EnsureOpenSendIntentUniqueness(db)
			require.Error(t, err, "the migration must not report success while the protection is off")
			var taken *database.OpenSendIntentNameTakenError
			require.True(t, errors.As(err, &taken))
			assert.False(t, taken.IsIdx)
			assert.Contains(t, err.Error(), c.kind)
			assert.Contains(t, err.Error(), "NOT an index")
			assert.NotContains(t, err.Error(), "DROP INDEX "+name, "DROP INDEX does not apply to this object")
			// The guard (what every claim runs) refuses, and says the same.
			gerr := database.VerifyOpenSendIntentGuard(db)
			require.Error(t, gerr)
			assert.Contains(t, gerr.Error(), c.kind)
			// Nothing was touched: the object is still there and no index was created under another form.
			var n int64
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_class WHERE relname = ?", name).Scan(&n).Error)
			assert.EqualValues(t, 1, n)
			// Dropping it with ITS OWN statement (what the message tells the operator) lets the migration finish.
			require.NoError(t, db.Exec(c.drop).Error)
			require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
			require.NoError(t, database.VerifyOpenSendIntentGuard(db))
		})
	}

	t.Run("an index of ANOTHER table", func(t *testing.T) {
		db := testutil.ScratchDatabase(t)
		require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
		require.NoError(t, db.Exec("CREATE TABLE zz_other (id int)").Error)
		require.NoError(t, db.Exec("CREATE INDEX "+name+" ON zz_other (id)").Error)
		err := database.EnsureOpenSendIntentUniqueness(db)
		require.Error(t, err)
		var taken *database.OpenSendIntentNameTakenError
		require.True(t, errors.As(err, &taken))
		assert.True(t, taken.IsIdx, "here DROP INDEX is the right statement")
		assert.Contains(t, err.Error(), "zz_other")
		assert.Contains(t, err.Error(), "DROP INDEX "+name)
	})
}
