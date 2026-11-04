package database

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OpenSendIntentIndexName is the partial unique index that guarantees a case has at most ONE open
// customer send (processing, or uncertain until somebody settles it). It is what makes "nobody else can
// send to this customer from this case while a send may be in flight or delivered" true in the database,
// whatever the number of users, devices, tabs or servers.
//
// It is created here and not by a gorm tag so that, as with the active-transfer index, existing data that
// would break it is reported and never fixed automatically.
const OpenSendIntentIndexName = "uq_send_intent_open"

const createOpenSendIntentIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS ` + OpenSendIntentIndexName +
	` ON occurrence_send_intents (organization_id, occurrence_id, kind) WHERE state IN ('processing', 'uncertain')`

// createUncertainRecheckIndexSQL serves the background re-check of recent unsettled uncertain sends.
const createUncertainRecheckIndexSQL = `CREATE INDEX IF NOT EXISTS idx_send_intent_uncertain_recheck ON occurrence_send_intents (finished_at) WHERE state = 'uncertain' AND reconciled_at IS NULL`

// OpenSendIntentDuplicate is one case that already has more than one open send: data that blocks the index.
type OpenSendIntentDuplicate struct {
	OrganizationID uuid.UUID
	OccurrenceID   uuid.UUID
	Kind           string
	OpenCount      int
	SendIDs        []uuid.UUID
	OldestAt       time.Time
	NewestAt       time.Time
}

// OpenSendIntentDuplicatesSQL is the read-only diagnostic. An operator can run it by hand.
const OpenSendIntentDuplicatesSQL = `
SELECT organization_id, occurrence_id, kind,
       COUNT(*)                                  AS open_count,
       STRING_AGG(id::text, ',' ORDER BY claimed_at) AS send_ids,
       MIN(claimed_at)                           AS oldest_at,
       MAX(claimed_at)                           AS newest_at
FROM occurrence_send_intents
WHERE state IN ('processing', 'uncertain')
GROUP BY organization_id, occurrence_id, kind
HAVING COUNT(*) > 1
ORDER BY open_count DESC, oldest_at`

// FindOpenSendIntentDuplicates runs the diagnostic. It never modifies data.
func FindOpenSendIntentDuplicates(db *gorm.DB) ([]OpenSendIntentDuplicate, error) {
	rows, err := db.Raw(OpenSendIntentDuplicatesSQL).Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to query duplicate open send intents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []OpenSendIntentDuplicate
	for rows.Next() {
		var (
			d   OpenSendIntentDuplicate
			ids string
		)
		if err := rows.Scan(&d.OrganizationID, &d.OccurrenceID, &d.Kind, &d.OpenCount, &ids, &d.OldestAt, &d.NewestAt); err != nil {
			return nil, fmt.Errorf("failed to scan duplicate open send intents: %w", err)
		}
		for _, s := range strings.Split(ids, ",") {
			if id, err := uuid.Parse(s); err == nil {
				d.SendIDs = append(d.SendIDs, id)
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FormatOpenSendIntentDuplicates renders the diagnostic for the migration console.
func FormatOpenSendIntentDuplicates(dups []OpenSendIntentDuplicate) string {
	var b strings.Builder
	for _, d := range dups {
		ids := make([]string, len(d.SendIDs))
		for i, id := range d.SendIDs {
			ids[i] = id.String()
		}
		fmt.Fprintf(&b, "  org=%s occurrence=%s kind=%s open=%d sends=[%s] oldest=%s newest=%s\n",
			d.OrganizationID, d.OccurrenceID, d.Kind, d.OpenCount, strings.Join(ids, ","),
			d.OldestAt.Format(time.RFC3339), d.NewestAt.Format(time.RFC3339))
	}
	return b.String()
}

// OpenSendIntentDuplicatesError is returned by the migration when the index cannot be created. Nothing was
// changed: the operator settles each listed send (the resolve endpoint, or by hand after checking the
// conversation) and runs the migration again.
type OpenSendIntentDuplicatesError struct{ Duplicates []OpenSendIntentDuplicate }

func (e *OpenSendIntentDuplicatesError) Error() string {
	return fmt.Sprintf("%s NOT created: %d case(s) have more than one open customer send (nothing was changed). "+
		"Settle each one after checking the conversation, then run the migration again:\n%s",
		OpenSendIntentIndexName, len(e.Duplicates), FormatOpenSendIntentDuplicates(e.Duplicates))
}

// EnsureOpenSendIntentUniqueness creates the index when the data allows it. With duplicates it creates
// nothing, changes nothing and returns *OpenSendIntentDuplicatesError, so the migration stops loudly
// instead of leaving the protection off. Idempotent.
func EnsureOpenSendIntentUniqueness(db *gorm.DB) error {
	if !db.Migrator().HasTable("occurrence_send_intents") {
		return nil
	}
	dups, err := FindOpenSendIntentDuplicates(db)
	if err != nil {
		return err
	}
	if len(dups) > 0 {
		return &OpenSendIntentDuplicatesError{Duplicates: dups}
	}
	if err := db.Exec(createOpenSendIntentIndexSQL).Error; err != nil {
		return fmt.Errorf("failed to create %s: %w", OpenSendIntentIndexName, err)
	}
	if err := db.Exec(createUncertainRecheckIndexSQL).Error; err != nil {
		return fmt.Errorf("failed to create the uncertain re-check index: %w", err)
	}
	return nil
}
