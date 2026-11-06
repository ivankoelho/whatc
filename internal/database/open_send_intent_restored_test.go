package database_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
)

// pg_dump/pg_restore leaves the SAME index with its predicate rendered differently (checked on PostgreSQL 16.15 and
// 17.11): ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text] instead of
// (ARRAY['processing'::character varying, 'uncertain'::character varying])::text[]. The statement below is exactly what
// pg_dump emits for it; running it in a scratch database produces that restored rendering.
const restoredIndexDDL = `CREATE UNIQUE INDEX uq_send_intent_open ON public.occurrence_send_intents USING btree (organization_id, occurrence_id, kind) WHERE ((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`

// Index created by the normal migration: accepted (and its rendering is the bare one, so the two shapes are really different).
func TestVerifyOpenSendIntentGuard_TheIndexTheMigrationCreates_IsAccepted_InItsBareRendering(t *testing.T) {
	db := testutil.ScratchDatabase(t)
	require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
	require.NoError(t, database.EnsureOpenSendIntentUniqueness(db))
	var pred string
	require.NoError(t, db.Raw(`SELECT pg_get_expr(i.indpred, i.indrelid) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = ?`, database.OpenSendIntentIndexName).Scan(&pred).Error)
	assert.NotContains(t, pred, "ARRAY[(", "the migration's own index is rendered with bare elements")
	require.NoError(t, database.VerifyOpenSendIntentGuard(db))
}

// Index as pg_restore leaves it: accepted by the guard and by the migration, which leaves it exactly as it is.
func TestVerifyOpenSendIntentGuard_AnIndexRestoredByPgDump_IsAccepted(t *testing.T) {
	for label, ddl := range map[string]string{
		"as pg_dump emits it":  restoredIndexDDL,
		"the other order":      `CREATE UNIQUE INDEX uq_send_intent_open ON public.occurrence_send_intents USING btree (organization_id, occurrence_id, kind) WHERE ((state)::text = ANY (ARRAY[('uncertain'::character varying)::text, ('processing'::character varying)::text]))`,
		"written by hand (IN)": `CREATE UNIQUE INDEX uq_send_intent_open ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`,
	} {
		t.Run(label, func(t *testing.T) {
			db := testutil.ScratchDatabase(t)
			require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
			require.NoError(t, db.Exec(ddl).Error)
			if label != "written by hand (IN)" {
				var pred string
				require.NoError(t, db.Raw(`SELECT pg_get_expr(i.indpred, i.indrelid) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = ?`, database.OpenSendIntentIndexName).Scan(&pred).Error)
				assert.Contains(t, pred, "ARRAY[(", "this test really reproduces the restored rendering: %s", pred)
			}
			before := indexDef(t, db)
			require.NoError(t, database.VerifyOpenSendIntentGuard(db), "a restored copy of the protection must not disable sends")
			require.NoError(t, database.EnsureOpenSendIntentUniqueness(db), "and the migration must recognise it")
			assert.Equal(t, before, indexDef(t, db), "the existing index was left exactly as it was")
		})
	}
}

// Every way of changing the meaning of the restored shape is refused, at the database level (the real parser, the real
// catalog), by the guard AND by the migration, which leaves the index alone and says how to fix it.
func TestVerifyOpenSendIntentGuard_ChangedIndexesInTheRestoredShape_AreRefused(t *testing.T) {
	const cols = `(organization_id, occurrence_id, kind)`
	idx := func(unique, onCols, where string) string {
		return fmt.Sprintf(`CREATE %sINDEX uq_send_intent_open ON public.occurrence_send_intents USING btree %s%s`, unique, onCols, where)
	}
	restored := func(a, b string) string {
		return fmt.Sprintf(` WHERE ((state)::text = ANY (ARRAY[('%s'::character varying)::text, ('%s'::character varying)::text]))`, a, b)
	}
	cases := map[string]string{
		"a replaced element":              idx("UNIQUE ", cols, restored("processing", "sent")),
		"the same element twice":          idx("UNIQUE ", cols, restored("processing", "processing")),
		"a third element":                 idx("UNIQUE ", cols, ` WHERE ((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text, ('sent'::character varying)::text]))`),
		"a one-element array":             idx("UNIQUE ", cols, ` WHERE ((state)::text = ANY (ARRAY[('processing'::character varying)::text]))`),
		"ALL instead of ANY (NOT IN)":     idx("UNIQUE ", cols, ` WHERE ((state)::text <> ALL (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`),
		"the column inside the array":     idx("UNIQUE ", cols, ` WHERE (('processing'::character varying)::text = ANY (ARRAY[(state)::text, ('uncertain'::character varying)::text]))`),
		"a constant against a constant":   idx("UNIQUE ", cols, ` WHERE (('processing'::character varying)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`),
		"NOT around it":                   idx("UNIQUE ", cols, ` WHERE (NOT ((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])))`),
		"OR with another condition":       idx("UNIQUE ", cols, ` WHERE (((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])) OR ((kind)::text = 'x'::text))`),
		"AND with a time condition":       idx("UNIQUE ", cols, ` WHERE (((state)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text])) AND (claimed_at < '2000-01-01 00:00:00+00'::timestamp with time zone))`),
		"the other column":                idx("UNIQUE ", cols, ` WHERE ((kind)::text = ANY (ARRAY[('processing'::character varying)::text, ('uncertain'::character varying)::text]))`),
		"not unique":                      idx("", cols, restored("processing", "uncertain")),
		"not partial":                     idx("UNIQUE ", cols, ""),
		"other columns":                   idx("UNIQUE ", `(occurrence_id, kind)`, restored("processing", "uncertain")),
		"columns in another order":        idx("UNIQUE ", `(occurrence_id, organization_id, kind)`, restored("processing", "uncertain")),
		"an extra key column":             idx("UNIQUE ", `(organization_id, occurrence_id, kind, user_id)`, restored("processing", "uncertain")),
		"an expression instead of column": idx("UNIQUE ", `(organization_id, occurrence_id, lower((kind)::text))`, restored("processing", "uncertain")),
	}
	for label, ddl := range cases {
		t.Run(label, func(t *testing.T) {
			db := testutil.ScratchDatabase(t)
			require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
			require.NoError(t, db.Exec(ddl).Error)
			before := indexDef(t, db)

			require.Error(t, database.VerifyOpenSendIntentGuard(db), "the guard must refuse it")
			err := database.EnsureOpenSendIntentUniqueness(db)
			require.Error(t, err, "the migration must not report success while the protection is off")
			var ie *database.OpenSendIntentIndexError
			require.True(t, errors.As(err, &ie))
			assert.Equal(t, before, indexDef(t, db), "the existing index was left exactly as it was")
		})
	}
}

// Invalid and absent are refused whatever the rendering is.
func TestVerifyOpenSendIntentGuard_ARestoredIndex_InvalidOrAbsent_IsRefused(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		db := testutil.ScratchDatabase(t)
		require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
		require.NoError(t, db.Exec(restoredIndexDDL).Error)
		require.NoError(t, db.Exec(fmt.Sprintf(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = '%s'::regclass`, database.OpenSendIntentIndexName)).Error)
		err := database.VerifyOpenSendIntentGuard(db)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "valid=false")
	})
	t.Run("absent", func(t *testing.T) {
		db := testutil.ScratchDatabase(t)
		require.NoError(t, db.AutoMigrate(&models.OccurrenceSendIntent{}))
		require.NoError(t, db.Exec(restoredIndexDDL).Error)
		require.NoError(t, db.Exec("DROP INDEX "+database.OpenSendIntentIndexName).Error)
		err := database.VerifyOpenSendIntentGuard(db)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is missing")
	})
}
