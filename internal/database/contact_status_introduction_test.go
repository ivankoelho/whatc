package database_test

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These tests run the REAL migration (RunMigrationWithProgress) on scratch databases. They pin the behaviour of the
// one-time, atomic introduction of contacts.contact_status:
//   - an installation without the column gets it, and the statuses of its conversations are derived, together;
//   - an installation with the column is never touched again: the queue of "new" conversations survives every boot;
//   - a failure at any point leaves no column behind and no half-done change, and the next boot completes.

type legacyIDs struct {
	recent, unread, oldHistory, silent, nullInbound uuid.UUID
	orgID                                           uuid.UUID
}

// legacyInstallation builds a database as it was before contact_status existed: fully migrated, with conversations in
// several situations, and then contacts.contact_status dropped (with the index and the CHECK that depend on it).
func legacyInstallation(t *testing.T) (*gorm.DB, legacyIDs) {
	t.Helper()
	db := scratchDatabase(t)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))

	org := &models.Organization{BaseModel: models.BaseModel{ID: uuid.New()}, Name: "Legacy Org", Slug: "legacy-" + uuid.NewString()[:8]}
	require.NoError(t, db.Create(org).Error)
	mk := func(name string, fields map[string]any) uuid.UUID {
		c := &models.Contact{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, PhoneNumber: "5511" + uuid.NewString()[:8], ProfileName: name}
		require.NoError(t, db.Create(c).Error)
		if len(fields) > 0 {
			require.NoError(t, db.Model(c).Updates(fields).Error)
		}
		return c.ID
	}
	msg := func(contact uuid.UUID, dir models.Direction) {
		require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WhatsAppAccount: "acct",
			ContactID: contact, Direction: dir, MessageType: models.MessageTypeText, Content: "hi"}).Error)
	}
	now := time.Now()
	ids := legacyIDs{orgID: org.ID}
	ids.recent = mk("recent", map[string]any{"last_inbound_at": now.Add(-1 * time.Hour)})
	ids.unread = mk("unread", map[string]any{"is_read": false, "last_inbound_at": now.Add(-90 * time.Hour)})
	ids.oldHistory = mk("old history", map[string]any{"last_inbound_at": now.Add(-72 * time.Hour), "is_read": true})
	msg(ids.oldHistory, models.DirectionIncoming)
	ids.silent = mk("silent", nil) // no inbound, no messages
	// last_inbound_at is NULL, but a message arrived 'now': only BackfillLastInboundAt (inside the same transaction,
	// BEFORE the derivation) can make this one "recent".
	ids.nullInbound = mk("null inbound", nil)
	msg(ids.nullInbound, models.DirectionIncoming)

	require.NoError(t, db.Exec("ALTER TABLE contacts DROP COLUMN contact_status").Error)
	require.False(t, db.Migrator().HasColumn(&models.Contact{}, "contact_status"))
	return db, ids
}

func statusOf(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, db.Raw("SELECT contact_status FROM contacts WHERE id = ?", id).Scan(&s).Error)
	return s
}

type colDef struct {
	DataType, Nullable, Default string
	MaxLen                      int
}

func statusColumnDef(t *testing.T, db *gorm.DB) colDef {
	t.Helper()
	var d colDef
	require.NoError(t, db.Raw(`SELECT data_type AS data_type, is_nullable AS nullable, COALESCE(column_default,'') AS "default",
		COALESCE(character_maximum_length,0) AS max_len FROM information_schema.columns
		WHERE table_name = 'contacts' AND column_name = 'contact_status'`).Row().Scan(&d.DataType, &d.Nullable, &d.Default, &d.MaxLen))
	return d
}

// 1. Legacy installation: the boot adds the column and derives every status; the column has exactly the model's definition.
func TestContactStatusIntroduction_LegacyInstallation_AddsTheColumnAndDerivesTheStatuses(t *testing.T) {
	db, ids := legacyInstallation(t)

	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))

	assert.Equal(t, "in_progress", statusOf(t, db, ids.recent), "recent inbound")
	assert.Equal(t, "in_progress", statusOf(t, db, ids.unread), "unread")
	assert.Equal(t, "resolved", statusOf(t, db, ids.oldHistory), "old history, read")
	assert.Equal(t, "new", statusOf(t, db, ids.silent), "no messages at all stays in the queue")
	assert.Equal(t, "in_progress", statusOf(t, db, ids.nullInbound), "last_inbound_at was backfilled from the message BEFORE the derivation, in the same transaction")

	// exactly the definition the model/AutoMigrate gives (compared with a database built from zero)
	fresh := scratchDatabase(t)
	require.NoError(t, database.RunMigrationWithProgress(fresh, adminCfg))
	assert.Equal(t, statusColumnDef(t, fresh), statusColumnDef(t, db), "same type, NOT NULL and default as a column created by AutoMigrate")
	assert.Equal(t, colDef{DataType: "character varying", Nullable: "NO", Default: "'new'::character varying", MaxLen: 20}, statusColumnDef(t, db))

	// the normal flow ran after it: CHECK constraint and index are back
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_constraint WHERE conname = 'chk_contacts_contact_status'").Scan(&n).Error)
	assert.Equal(t, int64(1), n)
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname = 'idx_contacts_org_status_lastmsg'").Scan(&n).Error)
	assert.Equal(t, int64(1), n)
}

// 2. THE DEFECT: a database that already has the column. The queue of "new" conversations (recent inbound, unread) must
// survive every boot. Before the fix each boot moved them to in_progress / resolved.
func TestContactStatusIntroduction_ExistingColumn_TheWaitingQueueSurvivesEveryBoot(t *testing.T) {
	db, ids := legacyInstallation(t)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg)) // introduces the column (legacy)

	// Now these conversations are WAITING for attendance: new, with a fresh inbound message, one unread.
	require.NoError(t, db.Exec("UPDATE contacts SET contact_status = 'new', last_inbound_at = now(), is_read = false WHERE id IN (?, ?)", ids.recent, ids.unread).Error)
	require.NoError(t, db.Exec("UPDATE contacts SET contact_status = 'new' WHERE id = ?", ids.oldHistory).Error) // has history: the OLD code would resolve it
	var queue int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM contacts WHERE contact_status = 'new' AND organization_id = ?", ids.orgID).Scan(&queue).Error)

	for boot := 1; boot <= 3; boot++ {
		require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
		var after int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM contacts WHERE contact_status = 'new' AND organization_id = ?", ids.orgID).Scan(&after).Error)
		assert.Equal(t, queue, after, "boot %d must not move any waiting conversation", boot)
		for _, id := range []uuid.UUID{ids.recent, ids.unread, ids.oldHistory} {
			assert.Equal(t, "new", statusOf(t, db, id), "boot %d", boot)
		}
	}
}

// 3. Idempotence: statuses decided by people after the introduction are never rewritten by a later boot.
func TestContactStatusIntroduction_TwoConsecutiveBoots_AreIdempotent(t *testing.T) {
	db, ids := legacyInstallation(t)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	first := map[uuid.UUID]string{}
	for _, id := range []uuid.UUID{ids.recent, ids.unread, ids.oldHistory, ids.silent, ids.nullInbound} {
		first[id] = statusOf(t, db, id)
	}
	// an agent reopens the finished one; another is concluded by hand
	require.NoError(t, db.Exec("UPDATE contacts SET contact_status = 'in_progress' WHERE id = ?", ids.oldHistory).Error)
	require.NoError(t, db.Exec("UPDATE contacts SET contact_status = 'resolved' WHERE id = ?", ids.silent).Error)

	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	assert.Equal(t, "in_progress", statusOf(t, db, ids.oldHistory))
	assert.Equal(t, "resolved", statusOf(t, db, ids.silent))
	for _, id := range []uuid.UUID{ids.recent, ids.unread, ids.nullInbound} {
		assert.Equal(t, first[id], statusOf(t, db, id))
	}
}

// 4. New database: no contacts table, so nothing to derive; the normal flow creates everything.
func TestContactStatusIntroduction_NewDatabase_NoBackfillAndNormalCreation(t *testing.T) {
	db := scratchDatabase(t)
	introduced, err := database.IntroduceContactStatus(db)
	require.NoError(t, err)
	assert.False(t, introduced, "no contacts table: nothing to introduce")
	assert.False(t, db.Migrator().HasTable(&models.Contact{}), "it must not create the table")

	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	assert.True(t, db.Migrator().HasColumn(&models.Contact{}, "contact_status"))
	var n int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM contacts").Scan(&n).Error)
	assert.Zero(t, n)
}

// 5 and 6. ATOMICITY. A failure after the column was added: the column must not stay, nothing else may stay either,
// and the next boot must complete correctly. Two injection points (database triggers, so the failure is a real
// statement error inside the transaction):
//   - v1: fails while last_inbound_at is being backfilled (right after ADD COLUMN);
//   - v2: fails while the statuses are being derived (after ADD COLUMN AND the last_inbound_at backfill both
//     succeeded: proves those completed steps are rolled back too).
func TestContactStatusIntroduction_AFailureRollsEverythingBack_AndTheNextBootCompletes(t *testing.T) {
	cases := []struct{ name, when string }{
		{"v1 fails backfilling last_inbound_at", `OLD.last_inbound_at IS NULL AND NEW.last_inbound_at IS NOT NULL`},
		{"v2 fails deriving the statuses", `to_jsonb(NEW)->>'contact_status' = 'in_progress'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, ids := legacyInstallation(t)
			require.NoError(t, db.Exec(`CREATE FUNCTION test_inject_failure() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$ LANGUAGE plpgsql`).Error)
			require.NoError(t, db.Exec(`CREATE TRIGGER test_inject BEFORE UPDATE ON contacts FOR EACH ROW WHEN (`+tc.when+`) EXECUTE FUNCTION test_inject_failure()`).Error)

			introduced, err := database.IntroduceContactStatus(db)
			require.Error(t, err, "the error is returned, not swallowed")
			assert.Contains(t, err.Error(), "injected failure")
			assert.False(t, introduced)

			assert.False(t, db.Migrator().HasColumn(&models.Contact{}, "contact_status"), "the column must NOT be left behind")
			var li sql.NullTime
			require.NoError(t, db.Raw("SELECT last_inbound_at FROM contacts WHERE id = ?", ids.nullInbound).Row().Scan(&li))
			assert.False(t, li.Valid, "the completed last_inbound_at backfill was rolled back too")

			// the whole boot fails the same way and still leaves no column
			require.Error(t, database.RunMigrationWithProgress(db, adminCfg))
			assert.False(t, db.Migrator().HasColumn(&models.Contact{}, "contact_status"))

			// next boot, failure gone: completes and derives correctly
			require.NoError(t, db.Exec("DROP TRIGGER test_inject ON contacts").Error)
			require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
			assert.Equal(t, "in_progress", statusOf(t, db, ids.recent))
			assert.Equal(t, "in_progress", statusOf(t, db, ids.unread))
			assert.Equal(t, "resolved", statusOf(t, db, ids.oldHistory))
			assert.Equal(t, "new", statusOf(t, db, ids.silent))
			assert.Equal(t, "in_progress", statusOf(t, db, ids.nullInbound))
		})
	}
}

// 7. An old installation that also lacks the columns the derivation reads: they are added in the same transaction
// (only if missing) and nothing fails.
func TestContactStatusIntroduction_AnOldInstallationWithoutTheInputColumns(t *testing.T) {
	db, ids := legacyInstallation(t)
	require.NoError(t, db.Exec("ALTER TABLE contacts DROP COLUMN last_inbound_at").Error)
	require.NoError(t, db.Exec("ALTER TABLE contacts DROP COLUMN is_read").Error)

	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	assert.True(t, db.Migrator().HasColumn(&models.Contact{}, "last_inbound_at"))
	assert.True(t, db.Migrator().HasColumn(&models.Contact{}, "is_read"))
	assert.Equal(t, "in_progress", statusOf(t, db, ids.nullInbound), "recent message -> recent inbound -> in progress")
	assert.Equal(t, "new", statusOf(t, db, ids.silent))
}

// 8. The other backfills keep working in the same boot: BackfillLastInboundAt (still per boot, only fills NULLs) and
// the commercial classification (once, when ITS columns are introduced).
func TestContactStatusIntroduction_OtherBackfillsKeepWorking(t *testing.T) {
	db, ids := legacyInstallation(t)
	require.NoError(t, db.Exec("ALTER TABLE teams DROP COLUMN commercial").Error)
	require.NoError(t, db.Exec("ALTER TABLE departments DROP COLUMN commercial").Error)
	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: ids.orgID, Name: "Vendas Loja Teste", IsActive: true}
	require.NoError(t, db.Omit("Commercial").Create(team).Error)

	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	var commercial bool
	require.NoError(t, db.Raw("SELECT commercial FROM teams WHERE id = ?", team.ID).Scan(&commercial).Error)
	assert.True(t, commercial, "the commercial backfill ran in the boot that introduced its columns")
	assert.Equal(t, "in_progress", statusOf(t, db, ids.nullInbound))

	// BackfillLastInboundAt is still part of every boot and only fills NULL: a later message fills a NULL, never overwrites
	var li time.Time
	require.NoError(t, db.Raw("SELECT last_inbound_at FROM contacts WHERE id = ?", ids.nullInbound).Scan(&li).Error)
	assert.False(t, li.IsZero(), "filled from the message in the introduction")
	require.NoError(t, db.Exec("UPDATE contacts SET last_inbound_at = NULL WHERE id = ?", ids.nullInbound).Error)
	require.NoError(t, database.RunMigrationWithProgress(db, adminCfg))
	require.NoError(t, db.Raw("SELECT last_inbound_at FROM contacts WHERE id = ?", ids.nullInbound).Scan(&li).Error)
	assert.False(t, li.IsZero(), "BackfillLastInboundAt still runs every boot")
	assert.Equal(t, "in_progress", statusOf(t, db, ids.nullInbound), "and a boot never changes the status")
}

// independentConnections opens n separate connection pools to the same database, so each concurrent executor
// behaves like its own -migrate process: its own connection, its own transaction, nothing shared in memory.
func independentConnections(t *testing.T, db *gorm.DB, n int) []*gorm.DB {
	t.Helper()
	dsn := db.Dialector.(*postgres.Dialector).DSN
	conns := make([]*gorm.DB, n)
	for i := range conns {
		c, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		require.NoError(t, err)
		conns[i] = c
		t.Cleanup(func() {
			if sqlDB, e := c.DB(); e == nil {
				_ = sqlDB.Close()
			}
		})
	}
	return conns
}

// 9. CONCURRENT EXECUTORS (real PostgreSQL, independent connections). Several -migrate processes reaching the
// introduction at the same time on the same legacy database: nobody gets an error, EXACTLY ONE introduces the column
// and runs the derivation (the others return without running any backfill), the final statuses are right, and no
// conversation is lost or altered. Before the advisory lock, the losers failed with `column "contact_status" ... already
// exists` (14 of 60 executions in the original probe).
//
// "Runs no backfill" is proven by a statement-level trigger on contacts that records every UPDATE statement: one
// complete introduction issues exactly 3 (last_inbound_at, active conversations, finished conversations), so any
// executor that repeated a backfill would show up as extra rows.
func TestContactStatusIntroduction_ConcurrentExecutors_ExactlyOneIntroduces(t *testing.T) {
	const executors, trials = 5, 10
	db, ids := legacyInstallation(t)
	conns := independentConnections(t, db, executors)

	require.NoError(t, db.Exec(`CREATE TABLE zz_update_log (id bigserial PRIMARY KEY)`).Error)
	require.NoError(t, db.Exec(`CREATE FUNCTION zz_log_update() RETURNS trigger AS $$ BEGIN INSERT INTO zz_update_log DEFAULT VALUES; RETURN NULL; END $$ LANGUAGE plpgsql`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER zz_log AFTER UPDATE ON contacts FOR EACH STATEMENT EXECUTE FUNCTION zz_log_update()`).Error)

	var contactsBefore, messagesBefore int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM contacts").Scan(&contactsBefore).Error)
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM messages").Scan(&messagesBefore).Error)

	for trial := 1; trial <= trials; trial++ {
		require.NoError(t, db.Exec("ALTER TABLE contacts DROP COLUMN IF EXISTS contact_status").Error) // back to legacy
		require.NoError(t, db.Exec("TRUNCATE zz_update_log").Error)

		type result struct {
			introduced bool
			err        error
		}
		results := make([]result, executors)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < executors; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results[i].introduced, results[i].err = database.IntroduceContactStatus(conns[i])
			}(i)
		}
		close(start)
		wg.Wait()

		introduced := 0
		for i, r := range results {
			require.NoError(t, r.err, "trial %d, executor %d: a concurrent executor must never fail", trial, i)
			if r.introduced {
				introduced++
			}
		}
		assert.Equal(t, 1, introduced, "trial %d: exactly one executor introduces the column", trial)

		var statements int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM zz_update_log").Scan(&statements).Error)
		assert.Equal(t, int64(3), statements, "trial %d: one derivation only (3 UPDATE statements); the others ran no backfill", trial)

		assert.True(t, db.Migrator().HasColumn(&models.Contact{}, "contact_status"))
		assert.Equal(t, "in_progress", statusOf(t, db, ids.recent), "trial %d", trial)
		assert.Equal(t, "in_progress", statusOf(t, db, ids.unread), "trial %d", trial)
		assert.Equal(t, "resolved", statusOf(t, db, ids.oldHistory), "trial %d", trial)
		assert.Equal(t, "new", statusOf(t, db, ids.silent), "trial %d", trial)
		assert.Equal(t, "in_progress", statusOf(t, db, ids.nullInbound), "trial %d", trial)

		var contactsAfter, messagesAfter int64
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM contacts").Scan(&contactsAfter).Error)
		require.NoError(t, db.Raw("SELECT COUNT(*) FROM messages").Scan(&messagesAfter).Error)
		assert.Equal(t, contactsBefore, contactsAfter, "trial %d: no conversation lost", trial)
		assert.Equal(t, messagesBefore, messagesAfter, "trial %d: no message lost", trial)
	}

	// afterwards: idempotent. A person's decision survives another round of executors.
	require.NoError(t, db.Exec("UPDATE contacts SET contact_status = 'new' WHERE id = ?", ids.recent).Error) // a waiting conversation
	require.NoError(t, db.Exec("TRUNCATE zz_update_log").Error)
	for _, c := range conns {
		introduced, err := database.IntroduceContactStatus(c)
		require.NoError(t, err)
		assert.False(t, introduced)
	}
	assert.Equal(t, "new", statusOf(t, db, ids.recent), "the waiting queue is untouched by later executors")
	var statements int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM zz_update_log").Scan(&statements).Error)
	assert.Zero(t, statements, "no backfill ran after the introduction")
}
