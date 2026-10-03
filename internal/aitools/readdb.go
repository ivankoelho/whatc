package aitools

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/ai"
	"gorm.io/gorm"
)

// Deps are the dependencies a tool receives from its Factory. A read tool gets only a ReadDB.
type Deps struct {
	Read ReadDB
}

// DefaultReadTimeout bounds one read-only view when the caller gives none.
const DefaultReadTimeout = 3 * time.Second

// ReadDB is how a read tool reaches the database. It hands out no database handle: the tool only
// gets a *gorm.DB inside View's callback, and that handle belongs to a PostgreSQL transaction
// opened READ ONLY, with a local statement_timeout, which View always rolls back.
//
// The security boundary is PostgreSQL, not Go encapsulation: any INSERT, UPDATE, DELETE, DDL or
// nextval inside that transaction fails with SQLSTATE 25006, even if a tool tries. Only SELECT
// (and SHOW) can run.
type ReadDB struct{ db *gorm.DB }

// NewReadDB wraps the application's database for read-only views.
func NewReadDB(db *gorm.DB) ReadDB { return ReadDB{db: db} }

// View runs fn in a READ ONLY transaction. The handle passed to fn is valid only inside fn. A
// query cancelled by the timeout comes back as context.DeadlineExceeded, which the governed tool
// records as a timeout.
func (r ReadDB) View(ctx context.Context, timeout time.Duration, fn func(tx *gorm.DB) error) (err error) {
	if r.db == nil {
		return errors.New("aitools: no database for the read view")
	}
	if timeout <= 0 {
		timeout = DefaultReadTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tx := r.db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true})
	if tx.Error != nil {
		return asTimeout(ctx, tx.Error)
	}
	defer tx.Rollback() // never Commit: a view has nothing to persist

	if e := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())).Error; e != nil {
		return asTimeout(ctx, e)
	}
	return asTimeout(ctx, fn(tx))
}

// asTimeout turns "the timeout cancelled the query" into context.DeadlineExceeded.
func asTimeout(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "57014" { // query_canceled (statement_timeout)
		return fmt.Errorf("%w: %v", context.DeadlineExceeded, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", context.DeadlineExceeded, err)
	}
	return err
}

// ErrInvalidArgs is returned by DecodeArgs for arguments a tool does not accept.
var ErrInvalidArgs = errors.New("invalid arguments")

// DecodeArgs reads a tool call's arguments strictly into dst: it must be one JSON object, with no
// field dst does not declare (so a model that sends contact_id or organization_id is refused, not
// silently ignored), and nothing after it. Empty arguments mean {}. Types and ranges are then
// checked by the tool itself.
func DecodeArgs(call ai.ToolCall, dst any) error {
	raw := bytes.TrimSpace(call.Arguments)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w", ErrInvalidArgs)
	}
	if _, err := dec.Token(); err != io.EOF { // anything after the object
		return fmt.Errorf("%w", ErrInvalidArgs)
	}
	return nil
}

// InvalidArgsResult is what the model hears for arguments a tool refused: short and generic.
func InvalidArgsResult() ai.ToolResult {
	return ai.ToolResult{Content: "error: invalid arguments", IsError: true}
}
