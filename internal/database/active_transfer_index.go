package database

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ActiveTransferIndexName is the partial unique index that guarantees a contact
// has at most one active attendance (agent_transfers row with status='active').
const ActiveTransferIndexName = "idx_agent_transfers_one_active_per_contact"

const createActiveTransferIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS ` + ActiveTransferIndexName +
	` ON agent_transfers (organization_id, contact_id) WHERE status = 'active' AND deleted_at IS NULL`

// ActiveTransferDuplicate describes one contact that already has more than one
// active transfer, i.e. data that blocks creating the unique index.
type ActiveTransferDuplicate struct {
	OrganizationID uuid.UUID
	ContactID      uuid.UUID
	ActiveCount    int
	TransferIDs    []uuid.UUID
	OldestAt       time.Time
	NewestAt       time.Time
}

// ActiveTransferDuplicatesSQL is the read-only diagnostic query. It is also
// documented in docs/superpowers/specs/2026-10-01-fase-6-distribuicao-design.md
// so an operator can run it by hand.
const ActiveTransferDuplicatesSQL = `
SELECT organization_id,
       contact_id,
       COUNT(*)                          AS active_count,
       STRING_AGG(id::text, ',' ORDER BY transferred_at) AS transfer_ids,
       MIN(transferred_at)               AS oldest_at,
       MAX(transferred_at)               AS newest_at
FROM agent_transfers
WHERE status = 'active' AND deleted_at IS NULL
GROUP BY organization_id, contact_id
HAVING COUNT(*) > 1
ORDER BY active_count DESC, oldest_at`

// FindActiveTransferDuplicates runs the read-only diagnostic. It never modifies
// data.
func FindActiveTransferDuplicates(db *gorm.DB) ([]ActiveTransferDuplicate, error) {
	rows, err := db.Raw(ActiveTransferDuplicatesSQL).Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to query duplicate active transfers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ActiveTransferDuplicate
	for rows.Next() {
		var (
			d   ActiveTransferDuplicate
			ids string
		)
		if err := rows.Scan(&d.OrganizationID, &d.ContactID, &d.ActiveCount, &ids, &d.OldestAt, &d.NewestAt); err != nil {
			return nil, fmt.Errorf("failed to scan duplicate active transfers: %w", err)
		}
		for _, s := range strings.Split(ids, ",") {
			if id, err := uuid.Parse(s); err == nil {
				d.TransferIDs = append(d.TransferIDs, id)
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FormatActiveTransferDuplicates renders the diagnostic for logs / the
// migration console.
func FormatActiveTransferDuplicates(dups []ActiveTransferDuplicate) string {
	var b strings.Builder
	for _, d := range dups {
		ids := make([]string, len(d.TransferIDs))
		for i, id := range d.TransferIDs {
			ids[i] = id.String()
		}
		fmt.Fprintf(&b, "  org=%s contact=%s active=%d transfers=[%s] oldest=%s newest=%s\n",
			d.OrganizationID, d.ContactID, d.ActiveCount, strings.Join(ids, ","),
			d.OldestAt.Format(time.RFC3339), d.NewestAt.Format(time.RFC3339))
	}
	return b.String()
}

// EnsureActiveTransferUniqueness creates the one-active-transfer-per-contact
// index when it is safe to do so.
//
// It is deliberately conservative: if any contact already has more than one
// active transfer it does NOT touch, delete or merge anything and does NOT
// create the index; it returns the offending rows so the caller can report
// them. The operator resolves them, and the next run creates the index.
// Returns created=true when the index exists after the call.
func EnsureActiveTransferUniqueness(db *gorm.DB) (created bool, dups []ActiveTransferDuplicate, err error) {
	dups, err = FindActiveTransferDuplicates(db)
	if err != nil {
		return false, nil, err
	}
	if len(dups) > 0 {
		return false, dups, nil
	}
	if err := db.Exec(createActiveTransferIndexSQL).Error; err != nil {
		return false, nil, fmt.Errorf("failed to create %s: %w", ActiveTransferIndexName, err)
	}
	return true, nil, nil
}
