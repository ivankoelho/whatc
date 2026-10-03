package aitools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReadDB_ReadsButNeverWrites(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Create(&models.AIToolSetting{OrganizationID: org.ID, ToolName: "seed", Enabled: true}).Error)
	rd := aitools.NewReadDB(db)

	// reading works
	var n int64
	require.NoError(t, rd.View(t.Context(), 0, func(tx *gorm.DB) error {
		return tx.Model(&models.AIToolSetting{}).Where("organization_id = ?", org.ID).Count(&n).Error
	}))
	assert.EqualValues(t, 1, n)

	// every kind of write is refused by PostgreSQL itself (SQLSTATE 25006 for DML)
	writes := map[string]string{
		"insert": "INSERT INTO ai_tool_settings (organization_id, tool_name) VALUES (gen_random_uuid(), 'x')",
		"update": "UPDATE ai_tool_settings SET enabled = false",
		"delete": "DELETE FROM ai_tool_settings",
		"ddl":    "CREATE TABLE zz_should_not_exist (a int)",
		"temp":   "CREATE TEMP TABLE zz_tmp (a int)",
	}
	for name, stmt := range writes {
		err := rd.View(t.Context(), 0, func(tx *gorm.DB) error { return tx.Exec(stmt).Error })
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "read-only", name)
	}
	err := rd.View(t.Context(), 0, func(tx *gorm.DB) error { return tx.Exec("SELECT nextval('pg_catalog.pg_class_oid_index')").Error })
	assert.Error(t, err, "sequences cannot advance in a read-only transaction")

	// nothing changed
	require.NoError(t, db.Model(&models.AIToolSetting{}).Where("organization_id = ?", org.ID).Count(&n).Error)
	assert.EqualValues(t, 1, n)
	var exists bool
	require.NoError(t, db.Raw("SELECT to_regclass('zz_should_not_exist') IS NOT NULL").Scan(&exists).Error)
	assert.False(t, exists)
}

func TestReadDB_TheHandleDiesWithTheView(t *testing.T) {
	db := testutil.SetupTestDB(t)
	rd := aitools.NewReadDB(db)
	var leaked *gorm.DB
	require.NoError(t, rd.View(t.Context(), 0, func(tx *gorm.DB) error { leaked = tx; return nil }))
	var one int
	assert.Error(t, leaked.Raw("SELECT 1").Scan(&one).Error, "after View the transaction is closed (always rolled back)")
}

func TestReadDB_ReturnsTheCallbackErrorAndSurvivesAPanic(t *testing.T) {
	db := testutil.SetupTestDB(t)
	rd := aitools.NewReadDB(db)
	boom := errors.New("boom")
	assert.ErrorIs(t, rd.View(t.Context(), 0, func(*gorm.DB) error { return boom }), boom)

	assert.Panics(t, func() {
		_ = rd.View(t.Context(), 0, func(*gorm.DB) error { panic("kaboom") })
	})
	// the connection went back to the pool clean: a normal query still works and is not read-only
	var ro string
	require.NoError(t, db.Raw("SHOW transaction_read_only").Scan(&ro).Error)
	assert.Equal(t, "off", ro)
}

func TestReadDB_TimeoutCancelsTheQueryAndDoesNotLeakIntoThePool(t *testing.T) {
	db := testutil.SetupTestDB(t)
	rd := aitools.NewReadDB(db)

	start := time.Now()
	err := rd.View(t.Context(), 300*time.Millisecond, func(tx *gorm.DB) error {
		return tx.Exec("SELECT pg_sleep(5)").Error
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the governed tool records this as a timeout")
	assert.Less(t, time.Since(start), 3*time.Second, "the slow query was interrupted")

	// SET LOCAL: the timeout does not stay on the connection
	var st string
	require.NoError(t, db.Raw("SHOW statement_timeout").Scan(&st).Error)
	assert.Equal(t, "0", st)
}

func TestReadDB_WithoutADatabaseFails(t *testing.T) {
	var rd aitools.ReadDB
	assert.Error(t, rd.View(t.Context(), 0, func(*gorm.DB) error { return nil }))
}

func TestDeps_ReachTheFactoryOnlyAfterAuthorizationAndRequested(t *testing.T) {
	db := testutil.SetupTestDB(t)
	deps := aitools.Deps{Read: aitools.NewReadDB(db)}
	var got aitools.Deps
	s := spec("get_order", aitools.RiskRead)
	s.Factory = func(_ aitools.Scope, d aitools.Deps) ai.Tool {
		got = d
		return toolFn(func(context.Context, ai.ToolCall) (ai.ToolResult, error) { return ai.ToolResult{Content: "ok"}, nil })
	}
	au := &memAuditor{}
	contact := uuid.New()
	r := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: catalogOf(t, s), GlobalEnabled: true, Enabled: map[string]bool{"get_order": true}, Auditor: au,
		Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact}, Deps: deps,
	}, nil)
	tool, _ := r.Resolve("get_order")
	assert.Equal(t, aitools.Deps{}, got, "Resolve did not build the tool")
	_, err := tool.Execute(context.Background(), call("c", "get_order", `{}`))
	require.NoError(t, err)
	assert.Equal(t, deps, got)
}

func TestGovernedTool_ASlowReadIsRecordedAsATimeout(t *testing.T) {
	db := testutil.SetupTestDB(t)
	s := spec("get_order", aitools.RiskRead)
	s.Factory = func(_ aitools.Scope, d aitools.Deps) ai.Tool {
		return toolFn(func(ctx context.Context, _ ai.ToolCall) (ai.ToolResult, error) {
			return ai.ToolResult{}, d.Read.View(ctx, 200*time.Millisecond, func(tx *gorm.DB) error {
				return tx.Exec("SELECT pg_sleep(5)").Error
			})
		})
	}
	au := &memAuditor{}
	contact := uuid.New()
	r := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: catalogOf(t, s), GlobalEnabled: true, Enabled: map[string]bool{"get_order": true}, Auditor: au,
		Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact},
		Deps: aitools.Deps{Read: aitools.NewReadDB(db)},
	}, nil)
	tool, _ := r.Resolve("get_order")
	res, err := tool.Execute(context.Background(), call("c", "get_order", `{}`))
	require.NoError(t, err)
	assert.Equal(t, "error: tool failed", res.Content)
	require.Len(t, au.events, 2)
	assert.Equal(t, models.AIToolCallFailed, au.events[1].outcome.Status)
	assert.Equal(t, "timeout", au.events[1].outcome.ErrorKind)
}

// --- DecodeArgs ---

type occArgs struct {
	Protocol string `json:"protocol"`
	Limit    int    `json:"limit"`
}

func decode(t *testing.T, raw string) (occArgs, error) {
	t.Helper()
	var a occArgs
	err := aitools.DecodeArgs(ai.ToolCall{Arguments: json.RawMessage(raw)}, &a)
	return a, err
}

func TestDecodeArgs_IsStrict(t *testing.T) {
	a, err := decode(t, `{"protocol":"OC-1","limit":3}`)
	require.NoError(t, err)
	assert.Equal(t, occArgs{"OC-1", 3}, a)

	a, err = decode(t, ``)
	require.NoError(t, err, "empty means no arguments")
	assert.Equal(t, occArgs{}, a)
	_, err = decode(t, `{}`)
	assert.NoError(t, err)

	for name, raw := range map[string]string{
		"unknown field":    `{"protocol":"x","contact_id":"` + uuid.NewString() + `"}`,
		"organization id":  `{"organization_id":"` + uuid.NewString() + `"}`,
		"wrong type":       `{"limit":"three"}`,
		"trailing data":    `{"limit":1} {"limit":2}`,
		"not an object":    `[1]`,
		"garbage":          `{oops`,
		"trailing garbage": `{"limit":1}xyz`,
	} {
		_, err := decode(t, raw)
		assert.ErrorIs(t, err, aitools.ErrInvalidArgs, name)
	}
	assert.Equal(t, "error: invalid arguments", aitools.InvalidArgsResult().Content)
	assert.True(t, aitools.InvalidArgsResult().IsError)
}
