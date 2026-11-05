package database

import (
	"fmt"
	"regexp"
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

// The predicate of the protection is "state is processing or uncertain", and nothing else. The catalog renders it
// differently between Postgres versions (casts, ANY (ARRAY[...]), parentheses), so it is not compared as text: the
// string literals in it must be EXACTLY {processing, uncertain}, and once the literals, the casts to a character type
// and the tokens of "state = ANY (ARRAY[...])" are taken out, NOTHING may remain: no NOT, no <>, no OR/AND, no other
// column, no other operator, no function.
var (
	predLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)
	predCast    = regexp.MustCompile(`::(?:character varying|character|varchar|bpchar|text|name)(?:\[\])?`)
	predTokens  = strings.NewReplacer("ARRAY", "", "ANY", "", "state", "", "=", "", "(", "", ")", "", "[", "", "]", "", ",", "", " ", "", "\n", "", "\t", "")
)

func openSendIntentPredicateOK(pred string) bool {
	lits := predLiteral.FindAllString(pred, -1)
	set := map[string]bool{}
	for _, l := range lits {
		set[l] = true
	}
	if len(lits) != 2 || len(set) != 2 || !set["'processing'"] || !set["'uncertain'"] {
		return false
	}
	rest := predLiteral.ReplaceAllString(pred, "")
	rest = predCast.ReplaceAllString(rest, "")
	return predTokens.Replace(rest) == ""
}

// VerifyOpenSendIntentGuard says whether the protection that keeps a case at one open customer send is really in
// the database. It looks at the STRUCTURE in the catalog, never at the name alone and never at substrings of a
// rendered definition: the index named uq_send_intent_open on the table occurrence_send_intents of the CURRENT
// schema (a table or index of the same name in another schema counts for nothing), a btree, UNIQUE, VALID, READY and
// live, on exactly the three plain columns (organization_id, occurrence_id, kind) in that order, with no
// expression, no extra or included column, and the partial predicate "state is processing or uncertain" and
// nothing else. nil = the protection is there. Anything else is an operational error that says what to do.
// Read-only.
func VerifyOpenSendIntentGuard(db *gorm.DB) error {
	var row struct {
		Def     string
		Unique  bool
		Valid   bool
		Ready   bool
		Live    bool
		KeyAtts int
		Atts    int
		NoExprs bool
		Pred    string
		Cols    string
		Method  string
	}
	err := db.Raw(`SELECT pg_get_indexdef(i.indexrelid) AS def, i.indisunique AS "unique", i.indisvalid AS valid,
		i.indisready AS ready, i.indislive AS live, i.indnkeyatts::int AS key_atts, i.indnatts::int AS atts,
		(i.indexprs IS NULL) AS no_exprs, COALESCE(pg_get_expr(i.indpred, i.indrelid), '') AS pred,
		COALESCE((SELECT string_agg(a.attname::text, ',' ORDER BY k.ord)
			FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum), '') AS cols,
		am.amname::text AS method
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_am am ON am.oid = c.relam
		WHERE c.relname = ? AND t.relname = 'occurrence_send_intents'
		  AND n.nspname = current_schema() AND c.relnamespace = t.relnamespace`, OpenSendIntentIndexName).Scan(&row).Error
	if err != nil {
		return fmt.Errorf("could not check the open-send index: %w", err)
	}
	fix := " Run the migration (whatomate server -migrate); nothing is sent from cases until the protection is in place. If the migration reports that an existing index is not the protection, follow what it says."
	switch {
	case row.Def == "":
		return fmt.Errorf("the index %s is missing.%s", OpenSendIntentIndexName, fix)
	case !row.Unique || !row.Valid || !row.Ready || !row.Live:
		return fmt.Errorf("the index %s exists but is not a valid unique index (unique=%v valid=%v ready=%v live=%v).%s", OpenSendIntentIndexName, row.Unique, row.Valid, row.Ready, row.Live, fix)
	case row.Method != "btree" || !row.NoExprs || row.KeyAtts != 3 || row.Atts != 3 ||
		row.Cols != "organization_id,occurrence_id,kind" || !openSendIntentPredicateOK(row.Pred):
		return fmt.Errorf("the index %s does not have the expected definition (%s).%s", OpenSendIntentIndexName, row.Def, fix)
	}
	return nil
}

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
	return fmt.Sprintf("%s NOT created: %d case(s) have more than one open customer send. NOTHING was deleted or changed, "+
		"and every other migration step (schema, permissions, roles, backfills) already completed.\n%s"+
		"What to do: start the server WITHOUT -migrate (customer sends from cases stay refused while the protection is "+
		"missing; everything else works), then settle each listed send as a person who holds occurrences:resolve_send "+
		"(POST /api/occurrences/{occurrence}/protocol-sends/{send}/resolve, after checking the conversation; a send still "+
		"'processing' is settled by the reconciliation within minutes), and run the migration again.",
		OpenSendIntentIndexName, len(e.Duplicates), FormatOpenSendIntentDuplicates(e.Duplicates))
}

// OpenSendIntentIndexError: an index named uq_send_intent_open exists but is not the protection (not unique, not
// valid, other columns, a predicate that misses an open state). CREATE UNIQUE INDEX IF NOT EXISTS only looks at the
// name, so it would "succeed" and leave the protection off. The migration never touches an existing index: it stops
// and says exactly what to do.
type OpenSendIntentIndexError struct{ Cause error }

func (e *OpenSendIntentIndexError) Error() string {
	return fmt.Sprintf("%s exists but is not the protection the system needs, and the migration does not touch an existing index: %v\n"+
		"What to do, from psql, as the owner of the table: SET lock_timeout = '5s'; DROP INDEX %s; (an index only, no data is involved) "+
		"and run the migration again, which creates it correctly. DROP INDEX waits for an exclusive lock on occurrence_send_intents, "+
		"and while it waits every other access to that table queues behind it: that is what the lock_timeout is for (if it times out, "+
		"try again when no long transaction is open). DROP INDEX CONCURRENTLY does not lock the table for long but cannot run inside a "+
		"transaction block and, if cancelled, leaves an INVALID index behind that must be dropped as well. "+
		"Customer sends from cases are refused (503) until the migration has run; nothing else is affected.", OpenSendIntentIndexName, e.Cause, OpenSendIntentIndexName)
}
func (e *OpenSendIntentIndexError) Unwrap() error { return e.Cause }

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
	// "IF NOT EXISTS" matches the NAME only: confirm that what is there is really the protection.
	if verr := VerifyOpenSendIntentGuard(db); verr != nil {
		return &OpenSendIntentIndexError{Cause: verr}
	}
	if err := db.Exec(createUncertainRecheckIndexSQL).Error; err != nil {
		return fmt.Errorf("failed to create the uncertain re-check index: %w", err)
	}
	return nil
}
