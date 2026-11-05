package database

import (
	"errors"
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

// The predicate of the protection is "state is processing or uncertain", and nothing else. Postgres stores
// `state IN ('processing','uncertain')` as `state = ANY (ARRAY['processing','uncertain'])`, and renders it with casts
// and parentheses that differ between versions. So the casts to a character type are taken out, the whitespace is
// dropped, and what is left must be EXACTLY ONE shape, anchored at both ends:
//
//	(...(state)...) = ANY (...(ARRAY[ 'processing' , 'uncertain' ])...)      (the two literals distinct, either order)
//
// Nothing is stripped out of the middle: a literal, an operator, a column or a function anywhere else (state compared
// to a literal, an array of columns, ALL instead of ANY, NOT, <>, OR/AND, a time condition, a comparison of two
// constants, a comparison of two booleans) makes the shape not match. Parentheses must also balance.
var (
	predCast      = regexp.MustCompile(`::(?:character varying|character|varchar|bpchar|text|name)(?:\[\])?`)
	predCanonical = regexp.MustCompile(`^\(*state\)*=ANY\(*ARRAY\[('(?:processing|uncertain)'),('(?:processing|uncertain)')\]\)*$`)
)

func openSendIntentPredicateOK(pred string) bool {
	rest := predCast.ReplaceAllString(pred, "")
	rest = strings.Join(strings.Fields(rest), "") // drop every whitespace (none is inside a literal here)
	m := predCanonical.FindStringSubmatch(rest)
	if m == nil || m[1] == m[2] {
		return false
	}
	return strings.Count(rest, "(") == strings.Count(rest, ")")
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
		if taken := objectWithTheIndexName(db); taken != nil {
			return taken
		}
		return fmt.Errorf("the index %s is missing.%s", OpenSendIntentIndexName, fix)
	case !row.Unique || !row.Valid || !row.Ready || !row.Live:
		return fmt.Errorf("the index %s exists but is not a valid unique index (unique=%v valid=%v ready=%v live=%v).%s", OpenSendIntentIndexName, row.Unique, row.Valid, row.Ready, row.Live, fix)
	case row.Method != "btree" || !row.NoExprs || row.KeyAtts != 3 || row.Atts != 3 ||
		row.Cols != "organization_id,occurrence_id,kind" || !openSendIntentPredicateOK(row.Pred):
		return fmt.Errorf("the index %s does not have the expected definition (%s).%s", OpenSendIntentIndexName, row.Def, fix)
	}
	return nil
}

// constraintBehindIndexName returns the UNIQUE, PRIMARY KEY or EXCLUDE constraint that owns the index of the reserved
// name in the current schema (the table it is on and its name), or empty strings. Foreign keys are not considered: they
// only DEPEND on a unique index, they do not own it.
func constraintBehindIndexName(db *gorm.DB) (table, name string) {
	var r struct{ Tbl, Name string }
	_ = db.Raw(`SELECT t.relname::text AS tbl, con.conname::text AS name
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_constraint con ON con.conindid = c.oid AND con.contype IN ('p', 'u', 'x')
		JOIN pg_class t ON t.oid = con.conrelid
		WHERE c.relname = ? AND n.nspname = current_schema() LIMIT 1`, OpenSendIntentIndexName).Scan(&r).Error
	return r.Tbl, r.Name
}

var plainIdentRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// quoteIdentForAdvice renders a catalog name so that the statement printed for the operator works when pasted into psql:
// a name made only of lower-case letters, digits and underscores is left as it is, anything else (capitals, hyphens,
// spaces, quotes) is double-quoted with embedded quotes doubled.
func quoteIdentForAdvice(s string) string {
	if plainIdentRe.MatchString(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// OpenSendIntentNameTakenError: the name reserved for the index is held by an object of the current schema that is not
// the index of occurrence_send_intents (a sequence, a table, a view, ... or an index of ANOTHER table). The migration
// never touches it, and "DROP INDEX" is the right advice only when the object really is an index.
type OpenSendIntentNameTakenError struct {
	Kind  string // human name of the object: "sequence", "table", "index of the table x", ...
	IsIdx bool
	// When that index belongs to a constraint, DROP INDEX is refused (2BP01): the constraint has to be dropped.
	ConstraintTable, ConstraintName string
}

func (e *OpenSendIntentNameTakenError) Error() string {
	if e.IsIdx && e.ConstraintName != "" {
		return fmt.Sprintf("the name %s is held by an %s, and that index belongs to the constraint %s of the table %s, so the protection cannot be created under it. "+
			"The migration does not touch it. DROP INDEX does not apply: Postgres refuses it (2BP01) because the constraint needs the index. "+
			"Check what the constraint is for: dropping a PRIMARY KEY or UNIQUE constraint of another table removes that table's integrity guarantee, and the statement "+
			"fails (it has no CASCADE) while a foreign key depends on it. If it is not needed, from psql as the owner of the table: SET lock_timeout = '5s'; ALTER TABLE %s DROP CONSTRAINT %s; "+
			"and run the migration again. Customer sends from cases are refused (503) until then.",
			OpenSendIntentIndexName, e.Kind, e.ConstraintName, e.ConstraintTable,
			quoteIdentForAdvice(e.ConstraintTable), quoteIdentForAdvice(e.ConstraintName))
	}
	if e.IsIdx {
		return fmt.Sprintf("the name %s is held by an %s, so the protection cannot be created under it. The migration does not touch it. "+
			"Check what it is for; if it is not needed, from psql as its owner: SET lock_timeout = '5s'; DROP INDEX %s; and run the migration again. "+
			"Customer sends from cases are refused (503) until then.", OpenSendIntentIndexName, e.Kind, OpenSendIntentIndexName)
	}
	return fmt.Sprintf("the name %s is held by a %s in the current schema, which is NOT an index, so the protection cannot be created under it, "+
		"and DROP INDEX does not apply to it. The migration does not touch it. Find out what that %s is and who uses it, then rename or drop it "+
		"with the statement for its own kind (do not guess), and run the migration again. Customer sends from cases are refused (503) until then.",
		OpenSendIntentIndexName, e.Kind, e.Kind)
}

// objectWithTheIndexName reports the object of the current schema that holds the reserved name, or nil.
func objectWithTheIndexName(db *gorm.DB) *OpenSendIntentNameTakenError {
	var o struct {
		Relkind string
		Tbl     string
	}
	err := db.Raw(`SELECT c.relkind::text AS relkind, COALESCE(t.relname::text, '') AS tbl
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_index i ON i.indexrelid = c.oid LEFT JOIN pg_class t ON t.oid = i.indrelid
		WHERE c.relname = ? AND n.nspname = current_schema()`, OpenSendIntentIndexName).Scan(&o).Error
	if err != nil || o.Relkind == "" {
		return nil
	}
	kinds := map[string]string{"r": "table", "p": "partitioned table", "S": "sequence", "v": "view", "m": "materialized view",
		"c": "composite type", "f": "foreign table", "t": "TOAST table"}
	if o.Relkind == "i" || o.Relkind == "I" {
		ct, cn := constraintBehindIndexName(db)
		return &OpenSendIntentNameTakenError{Kind: "index of the table " + o.Tbl, IsIdx: true, ConstraintTable: ct, ConstraintName: cn}
	}
	k, ok := kinds[o.Relkind]
	if !ok {
		k = "relation of kind " + o.Relkind
	}
	return &OpenSendIntentNameTakenError{Kind: k}
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
type OpenSendIntentIndexError struct {
	Cause error
	// When the index is the one behind a UNIQUE, PRIMARY KEY or EXCLUDE constraint, Postgres refuses DROP INDEX (2BP01):
	// the constraint has to be dropped instead.
	ConstraintTable, ConstraintName string
}

func (e *OpenSendIntentIndexError) Error() string {
	drop := fmt.Sprintf("DROP INDEX %s;", OpenSendIntentIndexName)
	what := "(an index only, no data is involved)"
	concurrently := "DROP INDEX CONCURRENTLY does not lock the table for long but cannot run inside a " +
		"transaction block and, if cancelled, leaves an INVALID index behind that must be dropped as well. "
	if e.ConstraintName != "" {
		drop = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", quoteIdentForAdvice(e.ConstraintTable), quoteIdentForAdvice(e.ConstraintName))
		what = fmt.Sprintf("(this index belongs to the constraint %s, so DROP INDEX is refused by Postgres with 2BP01 and the constraint has to be dropped instead; "+
			"no row is deleted by the statement, but dropping a PRIMARY KEY or UNIQUE constraint removes the integrity guarantee it gave, "+
			"and the statement fails, without CASCADE, while a foreign key depends on it)", e.ConstraintName)
		concurrently = "" // DROP INDEX, with or without CONCURRENTLY, does not apply to an index owned by a constraint
	}
	return fmt.Sprintf("%s exists but is not the protection the system needs, and the migration does not touch an existing index: %v\n"+
		"What to do, from psql, as the owner of the table: SET lock_timeout = '5s'; %s %s "+
		"and run the migration again, which creates it correctly. The statement waits for an exclusive lock on the table, "+
		"and while it waits every other access to that table queues behind it: that is what the lock_timeout is for (if it times out, "+
		"try again when no long transaction is open). %s"+
		"Customer sends from cases are refused (503) until the migration has run; nothing else is affected.", OpenSendIntentIndexName, e.Cause, drop, what, concurrently)
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
		var taken *OpenSendIntentNameTakenError
		if errors.As(verr, &taken) {
			return verr
		}
		ct, cn := constraintBehindIndexName(db)
		return &OpenSendIntentIndexError{Cause: verr, ConstraintTable: ct, ConstraintName: cn}
	}
	if err := db.Exec(createUncertainRecheckIndexSQL).Error; err != nil {
		return fmt.Errorf("failed to create the uncertain re-check index: %w", err)
	}
	return nil
}
