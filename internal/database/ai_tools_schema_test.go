package database_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAIToolTables_AreInTheMigrationList(t *testing.T) {
	names := map[string]bool{}
	for _, m := range database.GetMigrationModels() {
		names[m.Name] = true
	}
	assert.True(t, names["AIToolSetting"])
	assert.True(t, names["AIToolCall"])
}

func TestAIToolSetting_IsOffByDefaultAndUniquePerOrganizationAndTool(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	other := testutil.CreateTestOrganization(t, db)

	// The column default is false, even when the row is written without saying so.
	require.NoError(t, db.Exec(`INSERT INTO ai_tool_settings (organization_id, tool_name) VALUES (?, 'get_order')`, org.ID).Error)
	var s models.AIToolSetting
	require.NoError(t, db.Where("organization_id = ? AND tool_name = ?", org.ID, "get_order").First(&s).Error)
	assert.False(t, s.Enabled)

	// One row per (organization, tool); another organization can have its own.
	assert.Error(t, db.Exec(`INSERT INTO ai_tool_settings (organization_id, tool_name) VALUES (?, 'get_order')`, org.ID).Error)
	assert.NoError(t, db.Exec(`INSERT INTO ai_tool_settings (organization_id, tool_name, enabled) VALUES (?, 'get_order', true)`, other.ID).Error)
}

func TestAIToolCall_RoundTrip(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)

	contact, session := uuid.New(), uuid.New()
	now := time.Now()
	in := models.AIToolCall{
		OrganizationID: org.ID, RunID: uuid.New(), Step: 1, CallID: "call_1", ToolName: "get_order", Risk: models.AIToolRiskRead,
		ActorKind: models.AIActorKindAI, ActorRef: "chatbot_reply", SubjectContactID: &contact, SessionID: &session,
		Status: models.AIToolCallRequested, ArgsKeys: models.JSONBArray{"id"}, ArgsBytes: 12, ArgsHMAC: "ab",
		RequestedAt: now,
	}
	require.NoError(t, db.Create(&in).Error)

	var out models.AIToolCall
	require.NoError(t, db.First(&out, "id = ?", in.ID).Error)
	assert.Equal(t, models.AIActorKindAI, out.ActorKind)
	assert.Equal(t, models.AIToolCallRequested, out.Status)
	assert.Equal(t, "ab", out.ArgsHMAC)
	assert.Equal(t, models.JSONBArray{"id"}, out.ArgsKeys)
	assert.Nil(t, out.FinishedAt)
	assert.Equal(t, contact, *out.SubjectContactID)

	// the audited columns are exactly these (no place for values, results or Opaque)
	cols, err := db.Migrator().ColumnTypes(&models.AIToolCall{})
	require.NoError(t, err)
	var names []string
	for _, c := range cols {
		names = append(names, c.Name())
	}
	assert.ElementsMatch(t, []string{
		"id", "organization_id", "run_id", "step", "call_id", "tool_name", "risk", "actor_kind", "actor_ref",
		"subject_contact_id", "session_id", "whats_app_account", "status", "denial_reason", "args_keys", "args_bytes",
		"args_hmac_sha256", "result_bytes", "result_truncated", "result_is_error", "error_kind",
		"requested_at", "finished_at", "duration_ms",
	}, names)
}

func TestAIToolConfirmation_IsInTheMigrationListAndEnforcesItsKeys(t *testing.T) {
	names := map[string]bool{}
	for _, m := range database.GetMigrationModels() {
		names[m.Name] = true
	}
	assert.True(t, names["AIToolConfirmation"])

	db := testutil.SetupTestDB(t)
	cleanAll(t, db)
	org := testutil.CreateTestOrganization(t, db)
	now := time.Now()
	mk := func(hash string) models.AIToolConfirmation {
		return models.AIToolConfirmation{
			OrganizationID: org.ID, ContactID: uuid.New(), SessionID: uuid.New(), RunID: uuid.New(), ToolName: "request_agent_transfer",
			Risk: "write", TokenHash: hash, Args: models.JSONB{"reason": "x"}, ActionDigest: "d", Status: models.AIConfirmationPending,
			ProposedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		}
	}
	a := mk("hash-1")
	require.NoError(t, db.Create(&a).Error)
	var got models.AIToolConfirmation
	require.NoError(t, db.First(&got, "id = ?", a.ID).Error)
	assert.Equal(t, models.AIConfirmationPending, got.Status)
	assert.Equal(t, 0, got.ReconcileAttempts)
	assert.Equal(t, models.JSONB{"reason": "x"}, got.Args)
	assert.Nil(t, got.ConfirmedAt)

	dup := mk("hash-1")
	assert.Error(t, db.Create(&dup).Error, "a token hash is unique")

	// the model must not serialize its secrets
	b, err := json.Marshal(got)
	require.NoError(t, err)
	for _, secret := range []string{"hash-1", "action_digest", "args", "token", "reconcile", "confirm_wamid"} {
		assert.NotContains(t, string(b), secret)
	}
	assert.Len(t, models.AIConfirmationPending, len("pending"))
	assert.LessOrEqual(t, len(models.AIConfirmationNotExecuted), 16, "fits the status column")
}
