package aitools_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var objSchema = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)

func spec(name string, risk aitools.Risk) aitools.ToolSpec {
	return aitools.ToolSpec{
		Name: name, Description: "d", Parameters: objSchema, Risk: risk,
		Factory: func(aitools.Scope, aitools.Deps) ai.Tool { return nil },
	}
}

func TestDefaultCatalogHoldsExactlyTheTwoReadTools(t *testing.T) {
	c := aitools.DefaultCatalog()
	require.NotNil(t, c)
	assert.Equal(t, []string{"get_business_hours", "get_my_occurrences"}, c.Names())
	for _, n := range c.Names() {
		s, _ := c.Get(n)
		assert.Equal(t, aitools.RiskRead, s.Risk, n+": no write tool in the catalog")
	}
	_, ok := c.Get("anything")
	assert.False(t, ok)
}

func TestCatalog_ValidatesAndSorts(t *testing.T) {
	c, err := aitools.NewCatalog(spec("zeta", aitools.RiskRead), spec("alpha", aitools.RiskRead))
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zeta"}, c.Names())
	s, ok := c.Get("alpha")
	require.True(t, ok)
	assert.Equal(t, "alpha", s.Definition().Name)

	bad := map[string]aitools.ToolSpec{
		"bad name":     spec("has space", aitools.RiskRead),
		"bad schema":   {Name: "n", Parameters: json.RawMessage(`{"type":"string"}`), Risk: aitools.RiskRead, Factory: spec("n", aitools.RiskRead).Factory},
		"no risk":      spec("n", ""),
		"unknown risk": spec("n", "admin"),
		"no factory":   {Name: "n", Parameters: objSchema, Risk: aitools.RiskRead},
	}
	for name, s := range bad {
		_, err := aitools.NewCatalog(s)
		assert.Error(t, err, name)
	}
	_, err = aitools.NewCatalog(spec("dup", aitools.RiskRead), spec("dup", aitools.RiskRead))
	assert.Error(t, err, "duplicate")
}

func TestAuthorize_DenyByDefaultInAFixedOrder(t *testing.T) {
	read := aitools.PolicyInput{GlobalEnabled: true, Known: true, OrgEnabled: true, FeatureAllowed: true, Risk: aitools.RiskRead}
	write := aitools.PolicyInput{GlobalEnabled: true, Known: true, OrgEnabled: true, FeatureAllowed: true, Risk: aitools.RiskWrite}
	with := func(in aitools.PolicyInput, f func(*aitools.PolicyInput)) aitools.PolicyInput { f(&in); return in }

	cases := []struct {
		name string
		in   aitools.PolicyInput
		want string // "" = allowed
		conf bool   // allowed, but only to propose
	}{
		{"all off", aitools.PolicyInput{}, aitools.DenyGlobalOff, false},
		{"global off beats the rest", with(read, func(p *aitools.PolicyInput) { p.GlobalEnabled = false }), aitools.DenyGlobalOff, false},
		{"unknown tool", with(read, func(p *aitools.PolicyInput) { p.Known = false }), aitools.DenyUnknownTool, false},
		{"not enabled for the organization", with(read, func(p *aitools.PolicyInput) { p.OrgEnabled = false }), aitools.DenyNotEnabled, false},
		{"feature not allowed", with(read, func(p *aitools.PolicyInput) { p.FeatureAllowed = false }), aitools.DenyFeatureNotAllowed, false},
		{"read, enabled, global on", read, "", false},
		{"a read tool ignores the write switches", with(read, func(p *aitools.PolicyInput) { p.WriteEnabled, p.ConfirmationAvailable = false, false }), "", false},

		{"write while write_enabled is off", write, aitools.DenyWriteDisabled, false},
		{"write_enabled but no confirmation mechanism", with(write, func(p *aitools.PolicyInput) { p.WriteEnabled = true }), aitools.DenyConfirmationUnavailable, false},
		{"confirmation wired but write_enabled off", with(write, func(p *aitools.PolicyInput) { p.ConfirmationAvailable = true }), aitools.DenyWriteDisabled, false},
		{"write with the whole chain: allowed only to propose", with(write, func(p *aitools.PolicyInput) { p.WriteEnabled, p.ConfirmationAvailable = true, true }), "", true},
		{"write chain but the tool is not enabled", with(write, func(p *aitools.PolicyInput) {
			p.WriteEnabled, p.ConfirmationAvailable, p.OrgEnabled = true, true, false
		}), aitools.DenyNotEnabled, false},
		{"write chain but the global switch is off", with(write, func(p *aitools.PolicyInput) {
			p.WriteEnabled, p.ConfirmationAvailable, p.GlobalEnabled = true, true, false
		}), aitools.DenyGlobalOff, false},
		{"write chain but the feature is not allowed", with(write, func(p *aitools.PolicyInput) {
			p.WriteEnabled, p.ConfirmationAvailable, p.FeatureAllowed = true, true, false
		}), aitools.DenyFeatureNotAllowed, false},
		{"no risk class is never allowed", with(read, func(p *aitools.PolicyInput) { p.Risk = ""; p.WriteEnabled, p.ConfirmationAvailable = true, true }), aitools.DenyConfirmationUnavailable, false},
	}
	for _, c := range cases {
		v := aitools.Authorize(c.in)
		assert.Equal(t, c.want == "", v.Allowed, c.name)
		assert.Equal(t, c.want, v.Reason, c.name)
		assert.Equal(t, c.conf, v.NeedsConfirmation, c.name)
	}
	// a caller that forgets to say the feature gets a denial, not a permission
	assert.Equal(t, aitools.DenyFeatureNotAllowed, aitools.Authorize(aitools.PolicyInput{GlobalEnabled: true, Known: true, OrgEnabled: true, Risk: aitools.RiskRead}).Reason)
}

func TestAIActor(t *testing.T) {
	a := aitools.AIActor("chatbot_reply")
	assert.Equal(t, aitools.ActorAI, a.Kind)
	assert.Equal(t, "ai", string(a.Kind))
	assert.Equal(t, "chatbot_reply", a.Ref)
}

// --- arguments: canonical form, keys, HMAC ---

func TestCanonicalJSON(t *testing.T) {
	a, err := aitools.CanonicalJSON([]byte(`{ "b": 1, "a": {"z": [3, 1], "y": "x"} }`))
	require.NoError(t, err)
	b, err := aitools.CanonicalJSON([]byte(`{"a":{"y":"x","z":[3,1]},"b":1}`))
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b), "key order and spacing do not matter")
	assert.Equal(t, `{"a":{"y":"x","z":[3,1]},"b":1}`, string(a))

	n1, _ := aitools.CanonicalJSON([]byte(`{"n":1.0}`))
	n2, _ := aitools.CanonicalJSON([]byte(`{"n":1}`))
	assert.NotEqual(t, string(n1), string(n2), "numbers are kept as written")
	big, _ := aitools.CanonicalJSON([]byte(`{"n":12345678901234567890}`))
	assert.Equal(t, `{"n":12345678901234567890}`, string(big), "no float rounding")

	h, _ := aitools.CanonicalJSON([]byte(`{"q":"a<b>&é"}`))
	assert.Equal(t, `{"q":"a<b>&é"}`, string(h), "no HTML escaping, UTF-8 kept")

	_, err = aitools.CanonicalJSON([]byte(`{oops`))
	assert.Error(t, err)
}

func TestArgsHMAC(t *testing.T) {
	cpf := `{"cpf":"000.000.000-00"}` // a sentinel, not a real document
	h1 := aitools.ArgsHMAC("secret", []byte(cpf))
	require.Len(t, h1, 64)

	assert.Equal(t, h1, aitools.ArgsHMAC("secret", []byte(`{ "cpf" : "000.000.000-00" }`)), "same information, same HMAC")
	assert.NotEqual(t, h1, aitools.ArgsHMAC("secret", []byte(`{"cpf":"000.000.000-01"}`)), "another value, another HMAC")
	assert.NotEqual(t, h1, aitools.ArgsHMAC("another", []byte(cpf)), "another secret, another HMAC")
	assert.Equal(t, "", aitools.ArgsHMAC("", []byte(cpf)), "no secret: nothing, never an unkeyed hash")

	// it is NOT a plain SHA-256 of the arguments (which could be brute-forced offline)
	canon, _ := aitools.CanonicalJSON([]byte(cpf))
	for _, in := range [][]byte{canon, []byte(cpf)} {
		sum := sha256.Sum256(in)
		assert.NotEqual(t, hex.EncodeToString(sum[:]), h1)
	}
	// an unreadable document is still keyed
	bad := aitools.ArgsHMAC("secret", []byte(`{oops`))
	assert.Len(t, bad, 64)
	plain := sha256.Sum256([]byte(`{oops`))
	assert.NotEqual(t, hex.EncodeToString(plain[:]), bad)
}

func TestArgsKeys_NamesOnlyAndBounded(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, aitools.ArgsKeys([]byte(`{"b":"secret-value","a":1}`)))
	assert.Nil(t, aitools.ArgsKeys([]byte(`[1]`)))
	assert.Nil(t, aitools.ArgsKeys([]byte(`{oops`)))

	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, `"k%02d":1,`, i)
	}
	sb.WriteString(`"` + strings.Repeat("x", 100) + `":1}`)
	keys := aitools.ArgsKeys([]byte(sb.String()))
	assert.Len(t, keys, 20)
	for _, k := range keys {
		assert.LessOrEqual(t, len(k), 64)
	}
}

// --- DBAuditor and SettingsStore, against the real schema ---

func attempt(org uuid.UUID, args string) aitools.Attempt {
	contact, session := uuid.New(), uuid.New()
	return aitools.Attempt{
		RunID: uuid.New(), Step: 1,
		Call: ai.ToolCall{ID: "call_1", Name: "get_order", Arguments: json.RawMessage(args), Opaque: json.RawMessage(`{"sig":"SECRET-OPAQUE"}`)},
		Risk: aitools.RiskRead, Actor: aitools.AIActor("chatbot_reply"),
		Scope: aitools.Scope{OrganizationID: org, ContactID: &contact, SessionID: &session, WhatsAppAccount: "acc"},
	}
}

func TestDBAuditor_Lifecycle(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	au := aitools.DBAuditor{DB: db, Secret: "server-secret"}
	at := attempt(org.ID, `{"id":"7","cpf":"000.000.000-00"}`)

	id, err := au.Requested(t.Context(), at)
	require.NoError(t, err)
	var row models.AIToolCall
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, models.AIToolCallRequested, row.Status)
	assert.Equal(t, org.ID, row.OrganizationID)
	assert.Equal(t, "ai", row.ActorKind)
	assert.Equal(t, "chatbot_reply", row.ActorRef)
	assert.Equal(t, at.Scope.ContactID, row.SubjectContactID, "the customer is the subject, not the actor")
	assert.Equal(t, "get_order", row.ToolName)
	assert.Equal(t, "read", row.Risk)
	assert.Equal(t, at.RunID, row.RunID)
	assert.Equal(t, 1, row.Step)
	assert.Equal(t, models.JSONBArray{"cpf", "id"}, row.ArgsKeys)
	assert.Equal(t, len(at.Call.Arguments), row.ArgsBytes)
	assert.Equal(t, aitools.ArgsHMAC("server-secret", at.Call.Arguments), row.ArgsHMAC)
	assert.Nil(t, row.FinishedAt)

	require.NoError(t, au.Finish(t.Context(), id, aitools.Outcome{Status: models.AIToolCallExecuted, ResultBytes: 42, Truncated: true, ResultError: true}))
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	assert.Equal(t, models.AIToolCallExecuted, row.Status)
	assert.Equal(t, 42, row.ResultSize)
	assert.True(t, row.Truncated)
	assert.True(t, row.ResultErr)
	assert.NotNil(t, row.FinishedAt)

	// a denied attempt is a single closed row with the reason
	require.NoError(t, au.Denied(t.Context(), at, aitools.DenyNotEnabled))
	var denied models.AIToolCall
	require.NoError(t, db.First(&denied, "organization_id = ? AND status = ?", org.ID, models.AIToolCallDenied).Error)
	assert.Equal(t, aitools.DenyNotEnabled, denied.DenialReason)
	assert.NotNil(t, denied.FinishedAt)
}

// No value of the arguments, no Opaque and no result text may reach any column of the audit.
func TestDBAuditor_NeverStoresValuesOrOpaque(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	au := aitools.DBAuditor{DB: db, Secret: "server-secret"}
	at := attempt(org.ID, `{"cpf":"SENTINEL-CPF-123","phone":"SENTINEL-PHONE-456"}`)

	id, err := au.Requested(t.Context(), at)
	require.NoError(t, err)
	require.NoError(t, au.Finish(t.Context(), id, aitools.Outcome{Status: models.AIToolCallFailed, ErrorKind: "tool_error"}))
	require.NoError(t, au.Denied(t.Context(), at, aitools.DenyLoopLimit))

	var rows []map[string]any
	require.NoError(t, db.Raw(`SELECT * FROM ai_tool_calls WHERE organization_id = ?`, org.ID).Scan(&rows).Error)
	require.Len(t, rows, 2)
	dump, err := json.Marshal(rows)
	require.NoError(t, err)
	for _, secret := range []string{"SENTINEL-CPF-123", "SENTINEL-PHONE-456", "SECRET-OPAQUE", "server-secret"} {
		assert.NotContains(t, string(dump), secret)
	}
}

func TestDBAuditor_BoundsWhatTheModelControls(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	at := attempt(org.ID, `{}`)
	at.Call.Name = strings.Repeat("n", 500)
	at.Call.ID = strings.Repeat("i", 500)
	at.Risk = ""
	require.NoError(t, aitools.DBAuditor{DB: db}.Denied(t.Context(), at, aitools.DenyUnknownTool))
	var row models.AIToolCall
	require.NoError(t, db.First(&row, "organization_id = ?", org.ID).Error)
	assert.Len(t, row.ToolName, 64)
	assert.Len(t, row.CallID, 128)
	assert.Empty(t, row.ArgsHMAC, "no secret configured: no HMAC stored")
}

func TestSettingsStore_EnabledToolsAreOptInAndPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a, b := testutil.CreateTestOrganization(t, db), testutil.CreateTestOrganization(t, db)
	st := aitools.SettingsStore{DB: db}

	got, err := st.EnabledTools(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Empty(t, got, "no row means disabled")

	require.NoError(t, db.Create(&models.AIToolSetting{OrganizationID: a.ID, ToolName: "get_order", Enabled: true}).Error)
	require.NoError(t, db.Create(&models.AIToolSetting{OrganizationID: a.ID, ToolName: "off_tool", Enabled: false}).Error)

	got, err = st.EnabledTools(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"get_order": true}, got)

	got, err = st.EnabledTools(t.Context(), b.ID)
	require.NoError(t, err)
	assert.Empty(t, got, "another organization's opt-in does not count")
}

func TestDBAuditor_ArgsTooLargeRecordsOnlyTheSize(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	au := aitools.DBAuditor{DB: db, Secret: "server-secret"}
	huge := `{"cpf":"SENTINEL-CPF","pad":"` + strings.Repeat("x", 5000) + `"}`
	at := attempt(org.ID, huge)

	require.NoError(t, au.Denied(t.Context(), at, aitools.DenyArgsTooLarge))
	var row models.AIToolCall
	require.NoError(t, db.First(&row, "organization_id = ?", org.ID).Error)
	assert.Equal(t, models.AIToolCallDenied, row.Status)
	assert.Equal(t, "args_too_large", row.DenialReason)
	assert.Equal(t, len(huge), row.ArgsBytes)
	assert.Empty(t, row.ArgsHMAC, "nothing is computed over an oversized document")
	assert.Empty(t, row.ArgsKeys)

	// any other denial of the same call still records keys and HMAC
	require.NoError(t, au.Denied(t.Context(), at, aitools.DenyNotEnabled))
	var other models.AIToolCall
	require.NoError(t, db.First(&other, "organization_id = ? AND denial_reason = ?", org.ID, "not_enabled").Error)
	assert.NotEmpty(t, other.ArgsHMAC)
	assert.Equal(t, models.JSONBArray{"cpf", "pad"}, other.ArgsKeys)
}
