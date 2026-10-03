package aitools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- doubles ---

type event struct {
	kind    string // requested | denied | finish
	attempt aitools.Attempt
	reason  string
	outcome aitools.Outcome
}

type memAuditor struct {
	mu          sync.Mutex
	events      []event
	failRequest bool
	failFinish  int // how many Finish calls fail
	finishCalls int
	nextID      uuid.UUID
}

func (m *memAuditor) Requested(_ context.Context, a aitools.Attempt) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failRequest {
		return uuid.Nil, errors.New("audit db down")
	}
	m.events = append(m.events, event{kind: "requested", attempt: a})
	m.nextID = uuid.New()
	return m.nextID, nil
}

func (m *memAuditor) Denied(_ context.Context, a aitools.Attempt, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failRequest {
		return errors.New("audit db down")
	}
	m.events = append(m.events, event{kind: "denied", attempt: a, reason: reason})
	return nil
}

func (m *memAuditor) Finish(_ context.Context, _ uuid.UUID, o aitools.Outcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishCalls++
	if m.failFinish > 0 {
		m.failFinish--
		return errors.New("audit db down")
	}
	m.events = append(m.events, event{kind: "finish", outcome: o})
	return nil
}

func (m *memAuditor) kinds() []string {
	var k []string
	for _, e := range m.events {
		k = append(k, e.kind)
	}
	return k
}

type memLog struct{ lines []string }

func (l *memLog) Error(msg string, kv ...any) {
	s := msg
	for _, v := range kv {
		s += " " + toString(v)
	}
	l.lines = append(l.lines, s)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "?"
}

type toolFn func(context.Context, ai.ToolCall) (ai.ToolResult, error)

func (f toolFn) Execute(ctx context.Context, c ai.ToolCall) (ai.ToolResult, error) { return f(ctx, c) }

// probe is a spec whose Factory and tool report how often they ran and what they were given.
type probe struct {
	factoryCalls int
	execCalls    int
	gotScope     aitools.Scope
	gotCall      ai.ToolCall
	run          toolFn
}

func (p *probe) spec(name string, risk aitools.Risk) aitools.ToolSpec {
	s := spec(name, risk)
	s.Factory = func(sc aitools.Scope) ai.Tool {
		p.factoryCalls++
		p.gotScope = sc
		return toolFn(func(ctx context.Context, c ai.ToolCall) (ai.ToolResult, error) {
			p.execCalls++
			p.gotCall = c
			if p.run != nil {
				return p.run(ctx, c)
			}
			return ai.ToolResult{Content: "ok"}, nil
		})
	}
	return s
}

func catalogOf(t *testing.T, specs ...aitools.ToolSpec) *aitools.Catalog {
	t.Helper()
	c, err := aitools.NewCatalog(specs...)
	require.NoError(t, err)
	return c
}

func newResolver(cat *aitools.Catalog, enabled map[string]bool, au aitools.Auditor, log aitools.Logger) *aitools.Resolver {
	contact, session := uuid.New(), uuid.New()
	return aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: cat, GlobalEnabled: true, Enabled: enabled, Auditor: au, Actor: aitools.AIActor("chatbot_reply"),
		Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact, SessionID: &session, WhatsAppAccount: "acc"},
		Log:   log,
	}, nil)
}

func call(id, name, args string) ai.ToolCall {
	return ai.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

// --- Definitions and Resolve ---

func TestDefinitions_OnlyWhatIsEffectivelyAvailable(t *testing.T) {
	var p probe
	cat := catalogOf(t, p.spec("read_on", aitools.RiskRead), p.spec("read_off", aitools.RiskRead), p.spec("write_on", aitools.RiskWrite))
	au := &memAuditor{}

	r := newResolver(cat, map[string]bool{"read_on": true, "write_on": true}, au, nil)
	defs := r.Definitions()
	require.Len(t, defs, 1, "disabled and write tools are not offered")
	assert.Equal(t, "read_on", defs[0].Name)

	// the global switch off offers nothing at all
	contact := uuid.New()
	off := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: cat, GlobalEnabled: false, Enabled: map[string]bool{"read_on": true}, Auditor: au,
		Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact},
	}, nil)
	assert.Empty(t, off.Definitions())

	// the empty production catalog offers nothing even with everything on
	empty := newResolver(aitools.DefaultCatalog(), map[string]bool{"anything": true}, au, nil)
	assert.Empty(t, empty.Definitions())
	assert.Zero(t, p.factoryCalls, "no factory ran")
}

func TestResolve_NeverInstantiatesTheRealTool(t *testing.T) {
	var p probe
	cat := catalogOf(t, p.spec("read_on", aitools.RiskRead), p.spec("read_off", aitools.RiskRead), p.spec("write_on", aitools.RiskWrite))
	r := newResolver(cat, map[string]bool{"read_on": true, "write_on": true}, &memAuditor{}, nil)

	for _, name := range []string{"read_on", "read_off", "write_on", "never_heard_of_it", "", strings.Repeat("x", 1000)} {
		tool, ok := r.Resolve(name)
		assert.True(t, ok, "any name resolves to a governed or a denied tool: %q", name)
		assert.NotNil(t, tool)
	}
	assert.Zero(t, p.factoryCalls, "Resolve never builds the real tool")
	assert.Zero(t, p.execCalls)
}

// --- Execute: allowed ---

func TestExecute_AllowedRunsAfterRequestedAndRecordsTheOutcome(t *testing.T) {
	var p probe
	p.run = func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
		return ai.ToolResult{Content: "shipped"}, nil
	}
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)

	tool, _ := r.Resolve("get_order")
	c := call("c1", "get_order", `{"id":"7"}`)
	c.Opaque = json.RawMessage(`{"sig":"SECRET"}`)
	res, err := tool.Execute(context.Background(), c)
	require.NoError(t, err)

	assert.Equal(t, "shipped", res.Content)
	assert.Equal(t, []string{"requested", "finish"}, au.kinds())
	req := au.events[0].attempt
	assert.Equal(t, aitools.ActorAI, req.Actor.Kind)
	assert.Equal(t, "get_order", req.Call.Name)
	assert.Equal(t, aitools.RiskRead, req.Risk)
	assert.Equal(t, 1, req.Step)
	assert.Nil(t, req.Call.Opaque, "Opaque never reaches the audit")
	assert.Equal(t, r.RunID(), req.RunID)
	out := au.events[1].outcome
	assert.Equal(t, models.AIToolCallExecuted, out.Status)
	assert.Equal(t, len("shipped"), out.ResultBytes)
	assert.False(t, out.Truncated)
	assert.False(t, out.ResultError)
	assert.Equal(t, 1, p.factoryCalls)
	assert.Equal(t, 1, p.execCalls)
}

func TestExecute_StepOrdinalCountsCallsInTheRun(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)
	for i := 0; i < 3; i++ {
		tool, _ := r.Resolve("get_order")
		_, _ = tool.Execute(context.Background(), call("c", "get_order", `{}`))
	}
	var steps []int
	for _, e := range au.events {
		if e.kind == "requested" {
			steps = append(steps, e.attempt.Step)
		}
	}
	assert.Equal(t, []int{1, 2, 3}, steps)
}

func TestExecute_TheToolOnlyGetsTheServerScopeNeverOneFromTheArguments(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)
	otherOrg := uuid.New()

	tool, _ := r.Resolve("get_order")
	_, err := tool.Execute(context.Background(), call("c1", "get_order",
		`{"organization_id":"`+otherOrg.String()+`","contact_id":"`+uuid.NewString()+`"}`))
	require.NoError(t, err)

	assert.NotEqual(t, otherOrg, p.gotScope.OrganizationID, "an organization sent by the model changes nothing")
	assert.Equal(t, au.events[0].attempt.Scope.OrganizationID, p.gotScope.OrganizationID)
}

func TestExecute_ResultFlagsAreRecorded(t *testing.T) {
	var p probe
	p.run = func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
		return ai.ToolResult{Content: strings.Repeat("a", 100), IsError: true}, nil
	}
	au := &memAuditor{}
	contact := uuid.New()
	r := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: catalogOf(t, p.spec("get_order", aitools.RiskRead)), GlobalEnabled: true, Enabled: map[string]bool{"get_order": true},
		Auditor: au, Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact}, MaxResultBytes: 50,
	}, nil)
	tool, _ := r.Resolve("get_order")
	res, _ := tool.Execute(context.Background(), call("c", "get_order", `{}`))
	assert.True(t, res.IsError)
	out := au.events[1].outcome
	assert.Equal(t, models.AIToolCallExecuted, out.Status, "an error result reported to the model is still an execution")
	assert.True(t, out.ResultError)
	assert.True(t, out.Truncated)
	assert.Equal(t, 100, out.ResultBytes)
}

// --- Execute: failures of the tool ---

func TestExecute_ToolErrorPanicAndTimeoutAreFailedAndGeneric(t *testing.T) {
	cases := map[string]struct {
		run  toolFn
		kind string
	}{
		"error": {func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
			return ai.ToolResult{}, errors.New("db password=hunter2")
		}, "tool_error"},
		"panic": {func(context.Context, ai.ToolCall) (ai.ToolResult, error) { panic("kaboom") }, "panic"},
		"timeout": {func(context.Context, ai.ToolCall) (ai.ToolResult, error) {
			return ai.ToolResult{}, context.DeadlineExceeded
		}, "timeout"},
	}
	for name, c := range cases {
		var p probe
		p.run = c.run
		au := &memAuditor{}
		r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)
		tool, _ := r.Resolve("get_order")
		res, err := tool.Execute(context.Background(), call("c", "get_order", `{}`))
		require.NoError(t, err, name)
		assert.True(t, res.IsError, name)
		assert.Equal(t, "error: tool failed", res.Content, "%s: the internal error text never leaves", name)
		assert.Equal(t, []string{"requested", "finish"}, au.kinds(), name)
		assert.Equal(t, models.AIToolCallFailed, au.events[1].outcome.Status, name)
		assert.Equal(t, c.kind, au.events[1].outcome.ErrorKind, name)
	}
}

func TestExecute_APanickingFactoryIsAFailedCall(t *testing.T) {
	s := spec("get_order", aitools.RiskRead)
	s.Factory = func(aitools.Scope) ai.Tool { panic("bad init") }
	au := &memAuditor{}
	r := newResolver(catalogOf(t, s), map[string]bool{"get_order": true}, au, nil)
	tool, _ := r.Resolve("get_order")
	res, err := tool.Execute(context.Background(), call("c", "get_order", `{}`))
	require.NoError(t, err)
	assert.Equal(t, "error: tool failed", res.Content)
	assert.Equal(t, "panic", au.events[1].outcome.ErrorKind)

	s.Factory = func(aitools.Scope) ai.Tool { return nil }
	r = newResolver(catalogOf(t, s), map[string]bool{"get_order": true}, &memAuditor{}, nil)
	tool, _ = r.Resolve("get_order")
	res, _ = tool.Execute(context.Background(), call("c", "get_order", `{}`))
	assert.Equal(t, "error: tool failed", res.Content, "a nil tool is a failure, not a crash")
}

// --- Execute: denied ---

func TestExecute_DeniedNeverBuildsOrRunsTheToolAndTheModelHearsTheSameThing(t *testing.T) {
	var p probe
	cat := catalogOf(t, p.spec("read_off", aitools.RiskRead), p.spec("write_on", aitools.RiskWrite))
	au := &memAuditor{}
	r := newResolver(cat, map[string]bool{"write_on": true}, au, nil)

	contact := uuid.New()
	globalOff := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: cat, GlobalEnabled: false, Enabled: map[string]bool{"read_off": true}, Auditor: au,
		Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact},
	}, nil)

	type attemptCase struct {
		res    *aitools.Resolver
		name   string
		reason string
	}
	var contents []string
	for _, c := range []attemptCase{
		{r, "never_heard_of_it", aitools.DenyUnknownTool},
		{r, "read_off", aitools.DenyNotEnabled},
		{r, "write_on", aitools.DenyConfirmationUnavailable},
		{globalOff, "read_off", aitools.DenyGlobalOff},
	} {
		tool, _ := c.res.Resolve(c.name)
		res, err := tool.Execute(context.Background(), call("c", c.name, `{"id":"1"}`))
		require.NoError(t, err)
		assert.True(t, res.IsError)
		contents = append(contents, res.Content)
		last := au.events[len(au.events)-1]
		assert.Equal(t, "denied", last.kind)
		assert.Equal(t, c.reason, last.reason, c.name)
	}
	for _, c := range contents {
		assert.Equal(t, contents[0], c, "the model hears the same thing whatever the reason")
		for _, reason := range []string{"unknown", "enabled", "global", "confirmation", "write", "policy"} {
			assert.NotContains(t, strings.ToLower(c), reason)
		}
	}
	assert.Zero(t, p.factoryCalls, "a denied call never builds the tool")
	assert.Zero(t, p.execCalls, "a denied call never runs it")
	for _, e := range au.events {
		assert.Equal(t, "denied", e.kind, "no 'requested' row for a denied call")
	}
}

func TestExecute_AWriteToolNeverRuns(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("write_it", aitools.RiskWrite)), map[string]bool{"write_it": true}, au, nil)
	tool, _ := r.Resolve("write_it")
	_, err := tool.Execute(context.Background(), call("c", "write_it", `{}`))
	require.NoError(t, err)
	assert.Zero(t, p.factoryCalls+p.execCalls)
	assert.Equal(t, aitools.DenyConfirmationUnavailable, au.events[0].reason)
	assert.Equal(t, aitools.RiskWrite, au.events[0].attempt.Risk)
}

// --- audit failures ---

func TestExecute_FailingToRecordTheRequestMeansTheToolDoesNotRun(t *testing.T) {
	var p probe
	au := &memAuditor{failRequest: true}
	log := &memLog{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, log)
	tool, _ := r.Resolve("get_order")
	res, err := tool.Execute(context.Background(), call("c", "get_order", `{"cpf":"SENTINEL"}`))
	require.NoError(t, err)

	assert.True(t, res.IsError)
	assert.Equal(t, "error: this tool is not available", res.Content)
	assert.Zero(t, p.factoryCalls, "fail-closed: not even built")
	assert.Zero(t, p.execCalls)
	require.NotEmpty(t, log.lines)
	for _, l := range log.lines {
		assert.NotContains(t, l, "SENTINEL", "the log never carries arguments")
	}
}

func TestExecute_FailingToRecordTheOutcomeNeitherUndoesNorRepeatsTheRun(t *testing.T) {
	var p probe
	p.run = func(context.Context, ai.ToolCall) (ai.ToolResult, error) { return ai.ToolResult{Content: "done"}, nil }
	au := &memAuditor{failFinish: 99}
	log := &memLog{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, log)
	tool, _ := r.Resolve("get_order")
	res, err := tool.Execute(context.Background(), call("c", "get_order", `{"cpf":"SENTINEL","result":"x"}`))
	require.NoError(t, err)

	assert.Equal(t, "done", res.Content, "the result still goes to the model")
	assert.False(t, res.IsError)
	assert.Equal(t, 1, p.execCalls, "the tool ran once and is never run again")
	assert.Equal(t, 2, au.finishCalls, "one more try of WRITING the outcome, nothing else")
	assert.Equal(t, []string{"requested"}, au.kinds(), "the row stays 'requested'")
	require.Len(t, log.lines, 1)
	assert.Contains(t, log.lines[0], "outcome could not be recorded")
	assert.NotContains(t, log.lines[0], "SENTINEL")
	assert.NotContains(t, log.lines[0], "done")

	// one failure, then success: recorded, still one execution
	var q probe
	au2 := &memAuditor{failFinish: 1}
	r2 := newResolver(catalogOf(t, q.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au2, &memLog{})
	tool2, _ := r2.Resolve("get_order")
	_, _ = tool2.Execute(context.Background(), call("c", "get_order", `{}`))
	assert.Equal(t, 1, q.execCalls)
	assert.Equal(t, []string{"requested", "finish"}, au2.kinds())
}

// --- the loop, end to end ---

type fakeProvider struct {
	answers []func() *ai.Response
	calls   int
}

func (f *fakeProvider) Name() string                                       { return "fake" }
func (f *fakeProvider) Capabilities() ai.Capabilities                      { return ai.Capabilities{ToolCalling: true} }
func (f *fakeProvider) ListModels(context.Context) ([]ai.ModelInfo, error) { return nil, nil }
func (f *fakeProvider) Complete(context.Context, ai.Request) (*ai.Response, error) {
	f.calls++
	return f.answers[f.calls-1](), nil
}

func asks(calls ...ai.ToolCall) func() *ai.Response {
	return func() *ai.Response { return &ai.Response{ToolCalls: calls, Finish: ai.FinishToolCalls} }
}
func says(text string) func() *ai.Response {
	return func() *ai.Response { return &ai.Response{Text: text, Finish: ai.FinishStop} }
}

var userMsg = ai.Request{Model: "m", MaxTokens: 5, Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}}

func TestRunToolLoop_WithTheGovernedResolver(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)
	prov := &fakeProvider{answers: []func() *ai.Response{
		asks(call("c1", "get_order", `{"id":"1"}`), call("c2", "write_everything", `{}`)),
		says("here you go"),
	}}
	res, err := ai.RunToolLoop(context.Background(), prov, userMsg, r, ai.Limits{})
	require.NoError(t, err)
	assert.Equal(t, "here you go", res.Response.Text)
	assert.Equal(t, []string{"requested", "finish", "denied"}, au.kinds())
	assert.Equal(t, aitools.DenyUnknownTool, au.events[2].reason, "a forced call to a tool never offered is denied and recorded")
	assert.Equal(t, 1, p.execCalls)
}

func TestRunToolLoop_AnAbortedRoundRunsNothingAndIsRecordedAsLoopLimit(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)
	round := []ai.ToolCall{call("a", "get_order", `{}`), call("b", "get_order", `{}`), call("c", "other", `{}`)}
	prov := &fakeProvider{answers: []func() *ai.Response{asks(round...)}}

	res, err := ai.RunToolLoop(context.Background(), prov, userMsg, r, ai.Limits{MaxCallsStep: 2})
	require.ErrorIs(t, err, ai.ErrToolLoopLimit)
	assert.Zero(t, p.execCalls+p.factoryCalls, "nothing of the round ran")

	// the caller records the refused round from the last provider answer
	r.RecordAborted(context.Background(), res.Response.ToolCalls)
	require.Len(t, au.events, 3)
	for i, e := range au.events {
		assert.Equal(t, "denied", e.kind)
		assert.Equal(t, aitools.DenyLoopLimit, e.reason)
		assert.Equal(t, round[i].ID, e.attempt.Call.ID)
		assert.Nil(t, e.attempt.Call.Opaque)
	}
	assert.Equal(t, aitools.RiskRead, au.events[0].attempt.Risk)
	assert.Equal(t, aitools.Risk(""), au.events[2].attempt.Risk, "an unknown tool has no risk class")
}

// --- isolation between organizations, against the real schema ---

func TestGovernedResolver_OrganizationsAreIsolated(t *testing.T) {
	db := testutil.SetupTestDB(t)
	orgA, orgB := testutil.CreateTestOrganization(t, db), testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Create(&models.AIToolSetting{OrganizationID: orgA.ID, ToolName: "get_order", Enabled: true}).Error)

	var pa, pb probe
	build := func(org uuid.UUID, p *probe) *aitools.Resolver {
		contact := uuid.New()
		return aitools.NewResolver(context.Background(), aitools.ResolverConfig{
			Catalog: catalogOf(t, p.spec("get_order", aitools.RiskRead)), GlobalEnabled: true,
			Auditor: aitools.DBAuditor{DB: db, Secret: "s"}, Actor: aitools.AIActor("chatbot_reply"),
			Scope: aitools.Scope{OrganizationID: org, ContactID: &contact},
		}, aitools.SettingsStore{DB: db})
	}
	ra, rb := build(orgA.ID, &pa), build(orgB.ID, &pb)

	assert.Len(t, ra.Definitions(), 1, "A enabled it")
	assert.Empty(t, rb.Definitions(), "B did not")

	ta, _ := ra.Resolve("get_order")
	_, err := ta.Execute(context.Background(), call("c1", "get_order", `{}`))
	require.NoError(t, err)
	tb, _ := rb.Resolve("get_order") // B forces the call by name
	resB, err := tb.Execute(context.Background(), call("c2", "get_order", `{}`))
	require.NoError(t, err)

	assert.Equal(t, 1, pa.execCalls)
	assert.Zero(t, pb.factoryCalls+pb.execCalls, "A's opt-in does not run the tool for B")
	assert.True(t, resB.IsError)

	var rowsA, rowsB []models.AIToolCall
	require.NoError(t, db.Where("organization_id = ?", orgA.ID).Order("requested_at").Find(&rowsA).Error)
	require.NoError(t, db.Where("organization_id = ?", orgB.ID).Find(&rowsB).Error)
	require.Len(t, rowsA, 1)
	assert.Equal(t, models.AIToolCallExecuted, rowsA[0].Status)
	require.Len(t, rowsB, 1)
	assert.Equal(t, models.AIToolCallDenied, rowsB[0].Status)
	assert.Equal(t, aitools.DenyNotEnabled, rowsB[0].DenialReason)
	assert.Equal(t, "ai", rowsB[0].ActorKind)
}

func TestNewResolver_FailsClosedWhenTheSettingsCannotBeRead(t *testing.T) {
	var p probe
	log := &memLog{}
	contact := uuid.New()
	r := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: catalogOf(t, p.spec("get_order", aitools.RiskRead)), GlobalEnabled: true, Auditor: &memAuditor{},
		Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact}, Log: log,
	}, failingSource{})
	assert.Empty(t, r.Definitions(), "an unreadable opt-in means nothing is available")
	require.Len(t, log.lines, 1)

	// with the global switch off the settings are not even read
	log2 := &memLog{}
	r2 := aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: catalogOf(t, p.spec("get_order", aitools.RiskRead)), GlobalEnabled: false, Auditor: &memAuditor{},
		Actor: aitools.AIActor("x"), Scope: aitools.Scope{OrganizationID: uuid.New()}, Log: log2,
	}, failingSource{})
	assert.Empty(t, r2.Definitions())
	assert.Empty(t, log2.lines)
}

type failingSource struct{}

func (failingSource) EnabledTools(context.Context, uuid.UUID) (map[string]bool, error) {
	return nil, errors.New("db down")
}

// --- oversized arguments: recorded, never executed ---

func bigArgs(n int) string { return `{"id":"` + strings.Repeat("x", n) + `"}` }

func TestRunToolLoop_OversizedArgumentsAreRecordedAsArgsTooLargeAndNeverBuildTheTool(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("get_order", aitools.RiskRead)), map[string]bool{"get_order": true}, au, nil)
	prov := &fakeProvider{answers: []func() *ai.Response{asks(call("c1", "get_order", bigArgs(200))), says("done")}}

	res, err := ai.RunToolLoop(context.Background(), prov, userMsg, r, ai.Limits{MaxArgsBytes: 100})
	require.NoError(t, err)
	assert.Equal(t, "done", res.Response.Text)
	assert.Zero(t, p.factoryCalls+p.execCalls, "the tool was neither built nor run")
	require.Equal(t, []string{"denied"}, au.kinds())
	assert.Equal(t, aitools.DenyArgsTooLarge, au.events[0].reason)
	assert.Equal(t, aitools.RiskRead, au.events[0].attempt.Risk)
	assert.Greater(t, len(au.events[0].attempt.Call.Arguments), 100)
	// the model hears the same generic answer as for any other refusal
	assert.Equal(t, "error: this tool is not available", res.Messages[1].ToolResults[0].Content)
}

func TestRunToolLoop_OversizedArgumentsToAnUnavailableToolKeepThePolicyReason(t *testing.T) {
	var p probe
	au := &memAuditor{}
	r := newResolver(catalogOf(t, p.spec("read_off", aitools.RiskRead)), map[string]bool{}, au, nil)
	prov := &fakeProvider{answers: []func() *ai.Response{
		asks(call("c1", "read_off", bigArgs(200)), call("c2", "made_up", bigArgs(200))), says("done"),
	}}
	_, err := ai.RunToolLoop(context.Background(), prov, userMsg, r, ai.Limits{MaxArgsBytes: 100})
	require.NoError(t, err)
	require.Len(t, au.events, 2)
	assert.Equal(t, aitools.DenyNotEnabled, au.events[0].reason)
	assert.Equal(t, aitools.DenyUnknownTool, au.events[1].reason)
	assert.Zero(t, p.factoryCalls)
}

// Every ToolCall that reaches the loop and asks to run leaves exactly one row.
func TestRunToolLoop_EveryCallThatReachesTheLoopLeavesExactlyOneRow(t *testing.T) {
	var p probe
	au := &memAuditor{}
	cat := catalogOf(t, p.spec("read_on", aitools.RiskRead), p.spec("read_off", aitools.RiskRead), p.spec("write_on", aitools.RiskWrite))
	r := newResolver(cat, map[string]bool{"read_on": true, "write_on": true}, au, nil)
	round := []ai.ToolCall{
		call("a", "read_on", `{}`),         // allowed: requested + finish (one row)
		call("b", "read_off", `{}`),        // not enabled
		call("c", "write_on", `{}`),        // write
		call("d", "made_up", `{}`),         // unknown
		call("e", "read_on", bigArgs(200)), // too large
	}
	prov := &fakeProvider{answers: []func() *ai.Response{asks(round...), says("done")}}
	_, err := ai.RunToolLoop(context.Background(), prov, userMsg, r, ai.Limits{MaxArgsBytes: 100, MaxCallsStep: 5})
	require.NoError(t, err)

	rows := map[string][]string{} // call id -> "kind:reason"
	for _, e := range au.events {
		if e.kind == "finish" {
			continue
		}
		rows[e.attempt.Call.ID] = append(rows[e.attempt.Call.ID], e.kind+":"+e.reason)
	}
	assert.Equal(t, map[string][]string{
		"a": {"requested:"}, "b": {"denied:" + aitools.DenyNotEnabled}, "c": {"denied:" + aitools.DenyConfirmationUnavailable},
		"d": {"denied:" + aitools.DenyUnknownTool}, "e": {"denied:" + aitools.DenyArgsTooLarge},
	}, rows, "one row per call")

	// and a round the loop refuses as a whole is covered by RecordAborted
	au2 := &memAuditor{}
	r2 := newResolver(cat, map[string]bool{"read_on": true}, au2, nil)
	prov2 := &fakeProvider{answers: []func() *ai.Response{asks(round...)}}
	res2, err := ai.RunToolLoop(context.Background(), prov2, userMsg, r2, ai.Limits{MaxCallsStep: 2})
	require.ErrorIs(t, err, ai.ErrToolLoopLimit)
	r2.RecordAborted(context.Background(), res2.Response.ToolCalls)
	assert.Len(t, au2.events, len(round))
}
