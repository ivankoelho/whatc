package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seqTransport answers the n-th request with the n-th canned reply (the last one repeats).
type seqTransport struct {
	mu      sync.Mutex
	replies []seqReply
	bodies  []map[string]any
}

type seqReply struct {
	status int
	body   string
}

func (s *seqTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	_ = json.Unmarshal(b, &parsed)
	s.mu.Lock()
	i := len(s.bodies)
	s.bodies = append(s.bodies, parsed)
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	rep := s.replies[i]
	s.mu.Unlock()
	return &http.Response{StatusCode: rep.status, Body: io.NopCloser(strings.NewReader(rep.body)), Header: http.Header{}, Request: r}, nil
}

const usage = `"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`

func textReply(text string) seqReply {
	return seqReply{200, `{"choices":[{"message":{"content":"` + text + `"},"finish_reason":"stop"}],` + usage + `}`}
}

func callsReply(calls ...[2]string) seqReply { // each: id, tool name
	var parts []string
	for _, c := range calls {
		parts = append(parts, `{"id":"`+c[0]+`","type":"function","function":{"name":"`+c[1]+`","arguments":"{\"id\":\"7\",\"organization_id\":\"`+uuid.NewString()+`\"}"}}`)
	}
	return seqReply{200, `{"choices":[{"message":{"tool_calls":[` + strings.Join(parts, ",") + `]},"finish_reason":"tool_calls"}],` + usage + `}`}
}

type toolEnv struct {
	app       *handlers.App
	tr        *seqTransport
	org       *models.Organization
	settings  *models.ChatbotSettings
	session   *models.ChatbotSession
	contact   *models.Contact
	execs     *[]aitools.Scope
	factories *int
}

func newToolEnv(t *testing.T, replies ...seqReply) toolEnv {
	t.Helper()
	tr := &seqTransport{replies: replies}
	app := newTestApp(t, withHTTPClient(&http.Client{Transport: tr, Timeout: 5 * time.Second}))
	org := testutil.CreateTestOrganization(t, app.DB)
	settings := encryptedSettings(t, app, org.ID, models.AIProviderOpenAI, "model-1", "sk-o")
	session, contact := newSession(t, app, org.ID)

	var execs []aitools.Scope
	var factories int
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)
	mk := func(name string, risk aitools.Risk) aitools.ToolSpec {
		return aitools.ToolSpec{Name: name, Description: "d", Parameters: schema, Risk: risk, Factory: func(sc aitools.Scope, _ aitools.Deps) ai.Tool {
			factories++
			return toolFn(func(_ context.Context, c ai.ToolCall) (ai.ToolResult, error) {
				execs = append(execs, sc)
				return ai.ToolResult{Content: "order 7 shipped"}, nil
			})
		}}
	}
	cat, err := aitools.NewCatalog(mk("get_order", aitools.RiskRead), mk("write_it", aitools.RiskWrite))
	require.NoError(t, err)
	app.AIToolCatalog = cat
	return toolEnv{app: app, tr: tr, org: org, settings: settings, session: session, contact: contact, execs: &execs, factories: &factories}
}

type toolFn func(context.Context, ai.ToolCall) (ai.ToolResult, error)

func (f toolFn) Execute(ctx context.Context, c ai.ToolCall) (ai.ToolResult, error) { return f(ctx, c) }

func (e toolEnv) enable(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		require.NoError(t, e.app.DB.Create(&models.AIToolSetting{OrganizationID: e.org.ID, ToolName: n, Enabled: true}).Error)
	}
}

func (e toolEnv) callRows(t *testing.T) []models.AIToolCall {
	t.Helper()
	var rows []models.AIToolCall
	require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Order("requested_at ASC").Find(&rows).Error)
	return rows
}

func (e toolEnv) usage(t *testing.T) []models.AIUsageLog {
	t.Helper()
	var rows []models.AIUsageLog
	require.NoError(t, e.app.DB.Where("organization_id = ?", e.org.ID).Order("created_at ASC").Find(&rows).Error)
	return rows
}

func hasTools(body map[string]any) bool { _, ok := body["tools"]; return ok }

// --- dormant: the chatbot behaves exactly as before ---

func TestAITools_Dormant_GlobalSwitchOffSendsNoToolsAndTouchesNothing(t *testing.T) {
	e := newToolEnv(t, textReply("plain answer"))
	e.enable(t, "get_order") // an organization opted in, but the server switch is off (default)
	require.False(t, e.app.Config.AITools.Enabled)

	out, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.NoError(t, err)
	assert.Equal(t, "plain answer", out)
	require.Len(t, e.tr.bodies, 1)
	assert.False(t, hasTools(e.tr.bodies[0]), "no tools offered")
	assert.Len(t, e.usage(t), 1)
	assert.Empty(t, e.callRows(t))
	assert.Zero(t, *e.factories)
}

func TestAITools_Dormant_EmptyProductionCatalogSendsNoTools(t *testing.T) {
	e := newToolEnv(t, textReply("plain answer"))
	e.app.AIToolCatalog = nil // the production catalog, empty in 9B
	e.app.Config.AITools.Enabled = true
	e.enable(t, "get_order")

	out, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.NoError(t, err)
	assert.Equal(t, "plain answer", out)
	require.Len(t, e.tr.bodies, 1)
	assert.False(t, hasTools(e.tr.bodies[0]))
	assert.Len(t, e.usage(t), 1)
	assert.Empty(t, e.callRows(t))
}

func TestAITools_Dormant_NoToolEnabledByTheOrganizationSendsNoTools(t *testing.T) {
	e := newToolEnv(t, textReply("plain answer"))
	e.app.Config.AITools.Enabled = true // global on, catalog has tools, but this organization enabled none

	out, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.NoError(t, err)
	assert.Equal(t, "plain answer", out)
	assert.False(t, hasTools(e.tr.bodies[0]))
	assert.Empty(t, e.callRows(t))
}

func TestAITools_Dormant_OnlyAWriteToolEnabledSendsNoTools(t *testing.T) {
	e := newToolEnv(t, textReply("plain answer"))
	e.app.Config.AITools.Enabled = true
	e.enable(t, "write_it") // a write tool is never offered in 9B

	_, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.NoError(t, err)
	assert.False(t, hasTools(e.tr.bodies[0]))
	assert.Zero(t, *e.factories)
}

// --- active, through the single door ---

func TestAITools_Active_ReadToolRunsGovernedAndEveryStepIsRecorded(t *testing.T) {
	e := newToolEnv(t, callsReply([2]string{"call_1", "get_order"}), textReply("It shipped."))
	e.app.Config.AITools.Enabled = true
	e.enable(t, "get_order", "write_it")

	out, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "where is order 7?")
	require.NoError(t, err)
	assert.Equal(t, "It shipped.", out)

	// the provider was offered only the read tool, and got the result back
	require.Len(t, e.tr.bodies, 2)
	tools := e.tr.bodies[0]["tools"].([]any)
	require.Len(t, tools, 1, "the enabled write tool is not offered")
	assert.Equal(t, "get_order", tools[0].(map[string]any)["function"].(map[string]any)["name"])
	last := e.tr.bodies[1]["messages"].([]any)
	toolMsg := last[len(last)-1].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call_1", toolMsg["tool_call_id"])
	assert.Equal(t, "order 7 shipped", toolMsg["content"])

	// the tool got the scope of the session, not the organization the model put in the arguments
	require.Len(t, *e.execs, 1)
	sc := (*e.execs)[0]
	assert.Equal(t, e.org.ID, sc.OrganizationID)
	require.NotNil(t, sc.ContactID)
	assert.Equal(t, e.contact.ID, *sc.ContactID)
	assert.Equal(t, e.session.ID, *sc.SessionID)
	assert.Equal(t, "acc-x", sc.WhatsAppAccount)

	// audit: requested -> executed, the AI as the actor and the customer as the subject
	rows := e.callRows(t)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.Equal(t, models.AIToolCallExecuted, r.Status)
	assert.Equal(t, "ai", r.ActorKind)
	assert.Equal(t, "chatbot_reply", r.ActorRef)
	assert.Equal(t, "get_order", r.ToolName)
	assert.Equal(t, e.contact.ID, *r.SubjectContactID)
	assert.NotNil(t, r.FinishedAt)
	assert.Equal(t, models.JSONBArray{"id", "organization_id"}, r.ArgsKeys)
	assert.NotEmpty(t, r.ArgsHMAC)

	// usage: one row per provider call, with tokens of its own
	u := e.usage(t)
	require.Len(t, u, 2)
	for _, row := range u {
		assert.True(t, row.Success)
		assert.Equal(t, "chatbot_reply", row.Feature)
		assert.Equal(t, 15, row.TotalTokens)
		assert.Equal(t, e.session.ID, *row.SessionID)
	}
}

func TestAITools_Active_FlowNodeFeatureIsTheActorRef(t *testing.T) {
	e := newToolEnv(t, callsReply([2]string{"call_1", "get_order"}), textReply("ok"))
	e.app.Config.AITools.Enabled = true
	e.enable(t, "get_order")

	_, err := e.app.GenerateAIResponseNodeForTest(e.settings, e.session, "hi")
	require.NoError(t, err)
	rows := e.callRows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "chatbot_flow_node", rows[0].ActorRef)
}

func TestAITools_Active_AForcedCallToAnUnofferedOrWriteToolIsDeniedNotRun(t *testing.T) {
	e := newToolEnv(t, callsReply([2]string{"c1", "write_it"}, [2]string{"c2", "made_up"}), textReply("sorry"))
	e.app.Config.AITools.Enabled = true
	e.enable(t, "get_order", "write_it")

	out, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "do it")
	require.NoError(t, err)
	assert.Equal(t, "sorry", out)
	assert.Zero(t, *e.factories, "no tool was even built")

	rows := e.callRows(t)
	require.Len(t, rows, 2)
	got := map[string]string{}
	for _, r := range rows {
		assert.Equal(t, models.AIToolCallDenied, r.Status)
		got[r.ToolName] = r.DenialReason
	}
	assert.Equal(t, map[string]string{"write_it": aitools.DenyConfirmationUnavailable, "made_up": aitools.DenyUnknownTool}, got)

	// the model heard the same generic answer for both
	msgs := e.tr.bodies[1]["messages"].([]any)
	var contents []string
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "tool" {
			contents = append(contents, mm["content"].(string))
		}
	}
	require.Len(t, contents, 2)
	assert.Equal(t, contents[0], contents[1])
}

func TestAITools_Active_RoundOverTheLimitRunsNothingAndFallsBackLikeAnyAIError(t *testing.T) {
	five := callsReply([2]string{"a", "get_order"}, [2]string{"b", "get_order"}, [2]string{"c", "get_order"}, [2]string{"d", "get_order"}, [2]string{"e", "get_order"})
	e := newToolEnv(t, five)
	e.app.Config.AITools.Enabled = true
	e.enable(t, "get_order")

	_, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.ErrorIs(t, err, ai.ErrToolLoopLimit)
	assert.Zero(t, *e.factories, "nothing of the round ran")

	rows := e.callRows(t)
	require.Len(t, rows, 5)
	for _, r := range rows {
		assert.Equal(t, models.AIToolCallDenied, r.Status)
		assert.Equal(t, aitools.DenyLoopLimit, r.DenialReason)
	}
	assert.Len(t, e.usage(t), 1, "the one provider call that happened is recorded; the limit itself is not a call")
}

func TestAITools_Active_MalformedToolCallFromTheProviderEndsTheLoopWithoutRetry(t *testing.T) {
	bad := seqReply{200, `{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"get_order","arguments":"{oops"}}]},"finish_reason":"tool_calls"}]}`}
	e := newToolEnv(t, bad, textReply("never"))
	e.app.Config.AITools.Enabled = true
	e.enable(t, "get_order")

	_, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.Error(t, err)
	assert.Equal(t, ai.KindInvalidToolCall, ai.KindOf(err))
	assert.Len(t, e.tr.bodies, 1, "no retry")
	assert.Zero(t, *e.factories)
	u := e.usage(t)
	require.Len(t, u, 1)
	assert.False(t, u[0].Success)
	assert.Equal(t, "invalid_tool_call", u[0].ErrorKind)
}

func TestAITools_Active_OrganizationsDoNotShareOptIn(t *testing.T) {
	e := newToolEnv(t, textReply("plain answer"))
	e.app.Config.AITools.Enabled = true
	// another organization enabled the tool; this one did not
	other := testutil.CreateTestOrganization(t, e.app.DB)
	require.NoError(t, e.app.DB.Create(&models.AIToolSetting{OrganizationID: other.ID, ToolName: "get_order", Enabled: true}).Error)

	_, err := e.app.GenerateAIResponseForTest(e.settings, e.session, "hi")
	require.NoError(t, err)
	assert.False(t, hasTools(e.tr.bodies[0]), "another organization's opt-in does not count")
	assert.Empty(t, e.callRows(t))
}
