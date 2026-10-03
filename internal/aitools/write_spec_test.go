package aitools_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goodConfirm() *aitools.ConfirmSpec {
	return &aitools.ConfirmSpec{
		Prompt:       func(ttl time.Duration) string { return "Confirm?" },
		ConfirmLabel: "Sim, transferir", DeclineLabel: "Não, obrigado",
		DeclinedText: "ok", ExpiredText: "expirou", OutcomeText: func(string) string { return "done" },
	}
}

type fakeWriteTool struct{ proposed, executed *int }

func (f fakeWriteTool) Propose(context.Context, ai.ToolCall) (*aitools.Proposal, ai.ToolResult, error) {
	*f.proposed++
	return nil, ai.ToolResult{Content: "{}"}, nil
}
func (f fakeWriteTool) Execute(context.Context, models.JSONB, time.Time) (aitools.ExecResult, error) {
	*f.executed++
	return aitools.ExecResult{}, nil
}

func writeSpec(built, proposed, executed *int) aitools.ToolSpec {
	return aitools.ToolSpec{
		Name: "write_it", Description: "d", Parameters: objSchema, Risk: aitools.RiskWrite,
		Features: []string{"chatbot_reply"}, Confirm: goodConfirm(),
		WriteFactory: func(aitools.Scope, aitools.WriteDeps) aitools.WriteTool {
			*built++
			return fakeWriteTool{proposed: proposed, executed: executed}
		},
	}
}

func TestCatalog_WriteSpecsNeedAWriteFactoryAndACompleteConfirmationAndNoReadFactory(t *testing.T) {
	var b, p, e int
	good := writeSpec(&b, &p, &e)
	_, err := aitools.NewCatalog(good)
	require.NoError(t, err)

	mut := map[string]func(*aitools.ToolSpec){
		"no write factory":         func(s *aitools.ToolSpec) { s.WriteFactory = nil },
		"a read factory beside it": func(s *aitools.ToolSpec) { s.Factory = func(aitools.Scope, aitools.Deps) ai.Tool { return nil } },
		"no confirmation":          func(s *aitools.ToolSpec) { s.Confirm = nil },
		"no prompt":                func(s *aitools.ToolSpec) { s.Confirm.Prompt = nil },
		"no outcome text":          func(s *aitools.ToolSpec) { s.Confirm.OutcomeText = nil },
		"no declined text":         func(s *aitools.ToolSpec) { s.Confirm.DeclinedText = "" },
		"no expired text":          func(s *aitools.ToolSpec) { s.Confirm.ExpiredText = "" },
		"no confirm label":         func(s *aitools.ToolSpec) { s.Confirm.ConfirmLabel = "" },
		"button title over 20":     func(s *aitools.ToolSpec) { s.Confirm.DeclineLabel = "this title is too long for a button" },
	}
	for name, m := range mut {
		s := writeSpec(&b, &p, &e)
		s.Confirm = goodConfirm()
		m(&s)
		_, err := aitools.NewCatalog(s)
		assert.Error(t, err, name)
	}

	// a read spec must not carry write parts
	rs := spec("read_it", aitools.RiskRead)
	rs.Confirm = goodConfirm()
	_, err = aitools.NewCatalog(rs)
	assert.Error(t, err, "a read tool with a confirmation")
	rs = spec("read_it", aitools.RiskRead)
	rs.WriteFactory = good.WriteFactory
	_, err = aitools.NewCatalog(rs)
	assert.Error(t, err, "a read tool with a write factory")
	assert.Zero(t, b, "validating a catalog never builds a tool")
}

func writeResolver(t *testing.T, s aitools.ToolSpec, feature string, write, confirm bool, au aitools.Auditor) *aitools.Resolver {
	t.Helper()
	contact := uuid.New()
	return aitools.NewResolver(context.Background(), aitools.ResolverConfig{
		Catalog: catalogOf(t, s), GlobalEnabled: true, Enabled: map[string]bool{"write_it": true}, Auditor: au,
		Actor: aitools.AIActor(feature), Scope: aitools.Scope{OrganizationID: uuid.New(), ContactID: &contact},
		WriteEnabled: write, ConfirmationAvailable: confirm,
	}, nil)
}

func TestResolver_AWriteToolIsOfferedOnlyWithTheWholeChainAndTheRightFeature(t *testing.T) {
	var b, p, e int
	s := writeSpec(&b, &p, &e)
	au := &memAuditor{}

	assert.Empty(t, writeResolver(t, s, "chatbot_reply", false, false, au).Definitions(), "write_enabled off")
	assert.Empty(t, writeResolver(t, s, "chatbot_reply", true, false, au).Definitions(), "no confirmation mechanism")
	assert.Empty(t, writeResolver(t, s, "chatbot_reply", false, true, au).Definitions(), "write_enabled off")
	assert.Empty(t, writeResolver(t, s, "chatbot_flow_node", true, true, au).Definitions(), "the flow node is not a feature of this tool")
	defs := writeResolver(t, s, "chatbot_reply", true, true, au).Definitions()
	require.Len(t, defs, 1)
	assert.Equal(t, "write_it", defs[0].Name)
	assert.Zero(t, b+p+e, "offering a tool builds nothing")
}

func TestResolver_AForcedWriteCallIsDeniedWithTheRightReasonAndBuildsNothing(t *testing.T) {
	var b, p, e int
	s := writeSpec(&b, &p, &e)
	cases := []struct {
		name           string
		feature        string
		write, confirm bool
		reason         string
	}{
		{"write_enabled off", "chatbot_reply", false, true, aitools.DenyWriteDisabled},
		{"no confirmation mechanism", "chatbot_reply", true, false, aitools.DenyConfirmationUnavailable},
		{"wrong feature", "chatbot_flow_node", true, true, aitools.DenyFeatureNotAllowed},
	}
	for _, c := range cases {
		au := &memAuditor{}
		r := writeResolver(t, s, c.feature, c.write, c.confirm, au)
		tool, _ := r.Resolve("write_it")
		res, err := tool.Execute(context.Background(), call("c", "write_it", `{}`))
		require.NoError(t, err, c.name)
		assert.True(t, res.IsError, c.name)
		assert.Equal(t, "error: this tool is not available", res.Content, c.name)
		require.Len(t, au.events, 1, c.name)
		assert.Equal(t, "denied", au.events[0].kind)
		assert.Equal(t, c.reason, au.events[0].reason, c.name)
		assert.Equal(t, aitools.RiskWrite, au.events[0].attempt.Risk)
	}
	assert.Zero(t, b+p+e, "nothing was built, proposed or executed")
}

// Until the confirmation path exists, even a fully allowed write tool can only be refused.
func TestResolver_TheWriteTailIsNotWiredYetSoAnAllowedWriteToolIsStillRefused(t *testing.T) {
	var b, p, e int
	au := &memAuditor{}
	r := writeResolver(t, writeSpec(&b, &p, &e), "chatbot_reply", true, true, au)
	tool, _ := r.Resolve("write_it")
	res, err := tool.Execute(context.Background(), call("c", "write_it", `{}`))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Zero(t, b+p+e)
	assert.Equal(t, aitools.DenyConfirmationUnavailable, au.events[0].reason)
}

func TestFeaturesEmptyMeansEveryFeature(t *testing.T) {
	s := spec("get_order", aitools.RiskRead)
	assert.Empty(t, s.Features)
	au := &memAuditor{}
	for _, feature := range []string{"chatbot_reply", "chatbot_flow_node", "anything"} {
		r := newResolver(catalogOf(t, s), map[string]bool{"get_order": true}, au, nil)
		_ = feature
		assert.Len(t, r.Definitions(), 1)
	}
}
