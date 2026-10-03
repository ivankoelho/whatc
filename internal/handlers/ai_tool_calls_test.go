package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type callsEnv struct {
	app            *handlers.App
	orgA, orgB     *models.Organization
	adminA, adminB *models.User
	reader, writer *models.User
	bad            *models.User
}

func newCallsEnv(t *testing.T) callsEnv {
	t.Helper()
	app := newTestApp(t)
	e := callsEnv{app: app, orgA: testutil.CreateTestOrganization(t, app.DB), orgB: testutil.CreateTestOrganization(t, app.DB)}
	roleA, roleB := testutil.CreateAdminRole(t, app.DB, e.orgA.ID), testutil.CreateAdminRole(t, app.DB, e.orgB.ID)
	e.adminA = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&roleA.ID))
	e.adminB = testutil.CreateTestUser(t, app.DB, e.orgB.ID, testutil.WithRoleID(&roleB.ID))
	r := testutil.CreateTestRoleWithKeys(t, app.DB, e.orgA.ID, "callsr", []string{"ai_tools:read"})
	w := testutil.CreateTestRoleWithKeys(t, app.DB, e.orgA.ID, "callsw", []string{"ai_tools:write"})
	e.reader = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&r.ID))
	e.writer = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&w.ID))
	agent := testutil.CreateAgentRole(t, app.DB, e.orgA.ID)
	e.bad = testutil.CreateTestUser(t, app.DB, e.orgA.ID, testutil.WithRoleID(&agent.ID))
	return e
}

func (e callsEnv) list(t *testing.T, org, user uuid.UUID, query map[string]string) (int, []byte) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org, user)
	for k, v := range query {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, e.app.ListAIToolCalls(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

type callsPage struct {
	Data struct {
		Calls []map[string]any `json:"calls"`
		Total int              `json:"total"`
		Page  int              `json:"page"`
		Limit int              `json:"limit"`
	} `json:"data"`
}

func decodeCalls(t *testing.T, body []byte) callsPage {
	t.Helper()
	var p callsPage
	require.NoError(t, json.Unmarshal(body, &p))
	return p
}

func (e callsEnv) seed(t *testing.T, org uuid.UUID, mut func(*models.AIToolCall)) models.AIToolCall {
	t.Helper()
	contact, session := uuid.New(), uuid.New()
	c := models.AIToolCall{
		OrganizationID: org, RunID: uuid.New(), Step: 1, CallID: "SENTINEL-PROVIDER-CALL-ID", ToolName: "get_order", Risk: "read",
		ActorKind: "ai", ActorRef: "chatbot_reply", SubjectContactID: &contact, SessionID: &session, WhatsAppAccount: "acc",
		Status: models.AIToolCallExecuted, ArgsKeys: models.JSONBArray{"id"}, ArgsBytes: 10, ArgsHMAC: "SENTINEL-HMAC",
		ResultSize: 20, RequestedAt: time.Now(),
	}
	if mut != nil {
		mut(&c)
	}
	require.NoError(t, e.app.DB.Create(&c).Error)
	return c
}

func TestAIToolCallsAPI_ShowsOnlyMetadataAndNeverTheSecrets(t *testing.T) {
	e := newCallsEnv(t)
	e.seed(t, e.orgA.ID, nil)

	status, body := e.list(t, e.orgA.ID, e.adminA.ID, nil)
	require.Equal(t, fasthttp.StatusOK, status)
	for _, secret := range []string{"SENTINEL-HMAC", "SENTINEL-PROVIDER-CALL-ID", "args_hmac", "call_id", "opaque", "Opaque", "content"} {
		assert.NotContains(t, string(body), secret)
	}
	p := decodeCalls(t, body)
	require.Len(t, p.Data.Calls, 1)
	var got []string
	for k := range p.Data.Calls[0] {
		got = append(got, k)
	}
	assert.ElementsMatch(t, []string{
		"id", "run_id", "step", "tool_name", "risk", "actor_kind", "actor_ref", "subject_contact_id", "session_id",
		"whatsapp_account", "status", "args_keys", "args_bytes", "result_bytes", "result_truncated", "result_is_error",
		"requested_at", "duration_ms",
	}, got, "exactly the documented metadata (empty optional fields are omitted)")
	assert.Equal(t, "ai", p.Data.Calls[0]["actor_kind"])
	assert.Equal(t, []any{"id"}, p.Data.Calls[0]["args_keys"])
}

func TestAIToolCallsAPI_PermissionsAndOrganizationIsolation(t *testing.T) {
	e := newCallsEnv(t)
	mine := e.seed(t, e.orgA.ID, nil)
	theirs := e.seed(t, e.orgB.ID, nil)

	for name, u := range map[string]uuid.UUID{"no permission": e.bad.ID, "write only": e.writer.ID} {
		status, _ := e.list(t, e.orgA.ID, u, nil)
		assert.Equal(t, fasthttp.StatusForbidden, status, name)
	}
	status, body := e.list(t, e.orgA.ID, e.reader.ID, nil)
	assert.Equal(t, fasthttp.StatusOK, status, "ai_tools:read is enough")
	assert.Contains(t, string(body), mine.ID.String())

	// each organization sees only its own rows, whatever it filters by
	_, body = e.list(t, e.orgA.ID, e.adminA.ID, nil)
	assert.NotContains(t, string(body), theirs.ID.String())
	_, body = e.list(t, e.orgB.ID, e.adminB.ID, nil)
	assert.NotContains(t, string(body), mine.ID.String())
	_, body = e.list(t, e.orgB.ID, e.adminB.ID, map[string]string{"run_id": mine.RunID.String(), "contact_id": mine.SubjectContactID.String()})
	assert.Equal(t, 0, decodeCalls(t, body).Data.Total, "A's run and contact ids find nothing in B")

	// a user of A authenticated in B's organization has no role there
	status, _ = e.list(t, e.orgB.ID, e.adminA.ID, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status)
}

func TestAIToolCallsAPI_FiltersAndPagination(t *testing.T) {
	e := newCallsEnv(t)
	now := time.Now()
	contact := uuid.New()
	run := uuid.New()
	e.seed(t, e.orgA.ID, func(c *models.AIToolCall) {
		c.ToolName = "get_my_occurrences"
		c.SubjectContactID = &contact
		c.RunID = run
		c.RequestedAt = now.Add(-72 * time.Hour)
	})
	e.seed(t, e.orgA.ID, func(c *models.AIToolCall) {
		c.Status = models.AIToolCallDenied
		c.DenialReason = "not_enabled"
		c.RequestedAt = now.Add(-48 * time.Hour)
	})
	e.seed(t, e.orgA.ID, func(c *models.AIToolCall) {
		c.Status = models.AIToolCallFailed
		c.ErrorKind = "timeout"
		c.RequestedAt = now.Add(-24 * time.Hour)
	})
	e.seed(t, e.orgA.ID, func(c *models.AIToolCall) { c.RequestedAt = now })

	total := func(q map[string]string) int {
		status, body := e.list(t, e.orgA.ID, e.adminA.ID, q)
		require.Equal(t, fasthttp.StatusOK, status, "%v", q)
		return decodeCalls(t, body).Data.Total
	}
	assert.Equal(t, 4, total(nil))
	assert.Equal(t, 1, total(map[string]string{"tool": "get_my_occurrences"}))
	assert.Equal(t, 1, total(map[string]string{"status": "denied"}))
	assert.Equal(t, 1, total(map[string]string{"denial_reason": "not_enabled"}))
	assert.Equal(t, 1, total(map[string]string{"contact_id": contact.String()}))
	assert.Equal(t, 1, total(map[string]string{"run_id": run.String()}))
	assert.Equal(t, 2, total(map[string]string{"from": now.Add(-30 * time.Hour).Format(time.RFC3339)}))
	assert.Equal(t, 3, total(map[string]string{"to": now.Add(-12 * time.Hour).Format(time.RFC3339)}))
	assert.Equal(t, 4, total(map[string]string{"from": now.Add(-100 * time.Hour).UTC().Format(time.DateOnly), "to": now.UTC().Format(time.DateOnly)}), "a plain 'to' date covers the whole day")

	// newest first, paginated, with a ceiling on the page size
	_, body := e.list(t, e.orgA.ID, e.adminA.ID, map[string]string{"limit": "2", "page": "2"})
	p := decodeCalls(t, body)
	assert.Equal(t, 4, p.Data.Total)
	assert.Len(t, p.Data.Calls, 2)
	assert.Equal(t, 2, p.Data.Page)
	_, body = e.list(t, e.orgA.ID, e.adminA.ID, map[string]string{"limit": "100000"})
	assert.LessOrEqual(t, decodeCalls(t, body).Data.Limit, 100)
	_, body = e.list(t, e.orgA.ID, e.adminA.ID, map[string]string{"limit": "1"})
	first := decodeCalls(t, body).Data.Calls[0]
	assert.Equal(t, "executed", first["status"], "the most recent call comes first")

	// a malformed filter is an error, never a silently unfiltered list
	for _, q := range []map[string]string{{"status": "bogus"}, {"contact_id": "nope"}, {"run_id": "nope"}, {"from": "yesterday"}, {"to": "x"}} {
		status, _ := e.list(t, e.orgA.ID, e.adminA.ID, q)
		assert.Equal(t, fasthttp.StatusBadRequest, status, "%v", q)
	}
}

// What the governance really writes (arguments with sensitive values, a model-chosen organization
// id) never comes back through the API.
func TestAIToolCallsAPI_WhatTheGovernanceWritesCarriesNoValues(t *testing.T) {
	te := newToolEnv(t, callsReply([2]string{"call_1", "get_order"}), textReply("ok"))
	te.app.Config.AITools.Enabled = true
	te.enable(t, "get_order")
	_, err := te.app.GenerateAIResponseForTest(te.settings, te.session, "hi")
	require.NoError(t, err)
	rows := te.callRows(t)
	require.Len(t, rows, 1)

	role := testutil.CreateAdminRole(t, te.app.DB, te.org.ID)
	admin := testutil.CreateTestUser(t, te.app.DB, te.org.ID, testutil.WithRoleID(&role.ID))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, te.org.ID, admin.ID)
	require.NoError(t, te.app.ListAIToolCalls(req))
	body := string(testutil.GetResponseBody(req))

	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, body, rows[0].ID.String())
	assert.Contains(t, body, `"args_keys":["id","organization_id"]`, "the names of the arguments are visible, never their values")
	assert.NotContains(t, body, "order 7 shipped", "no result content")
	assert.False(t, strings.Contains(body, rows[0].ArgsHMAC) && rows[0].ArgsHMAC != "", "no HMAC")
}
