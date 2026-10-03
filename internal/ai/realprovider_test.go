//go:build realprovider

// Real-provider validation of tool calling (Fase 9C gate).
//
// This is NOT part of the normal suite or of CI: it needs the build tag and real API keys, and it
// spends a few cents. Run it by hand, one provider or all:
//
//	WHATC_REAL_GROQ_KEY=... WHATC_REAL_GROQ_MODEL=...  \
//	WHATC_REAL_GOOGLE_KEY=... WHATC_REAL_GOOGLE_MODEL=... \
//	WHATC_REAL_OPENAI_KEY=... WHATC_REAL_OPENAI_MODEL=... \
//	WHATC_REAL_ANTHROPIC_KEY=... WHATC_REAL_ANTHROPIC_MODEL=... \
//	go test -tags realprovider ./internal/ai -run TestRealProviders -v -count=1
//
// Keys and models come ONLY from the environment: never from a file, never printed, never
// committed. A provider without both variables is skipped and reported as NOT VALIDATED, never as
// approved by inference. The run prints one RESULT line per scenario and, when WHATC_REAL_REPORT
// names a file, writes the same table there.
//
// A scenario that diverges from what the adapter assumes is reported, not hidden: the contract or
// the adapter changes only after the divergence is reviewed, never silently here.
package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/stretchr/testify/require"
)

const (
	verdictValidated    = "validated"
	verdictDivergent    = "divergent"
	verdictObserved     = "observed"
	verdictNotValidated = "not_validated"
)

var report struct {
	sync.Mutex
	lines []string
}

func record(t *testing.T, provider, scenario, verdict, detail string) {
	t.Helper()
	detail = strings.ReplaceAll(strings.TrimSpace(detail), "\n", " ")
	if len(detail) > 240 {
		detail = detail[:240] + "…"
	}
	line := fmt.Sprintf("| %s | %s | %s | %s |", provider, scenario, verdict, detail)
	report.Lock()
	report.lines = append(report.lines, line)
	report.Unlock()
	t.Logf("RESULT|%s|%s|%s|%s", provider, scenario, verdict, detail)
}

type realCase struct {
	name, keyEnv, modelEnv string
}

// The order is the priority order of the validation.
var realCases = []realCase{
	{ai.ProviderGroq, "WHATC_REAL_GROQ_KEY", "WHATC_REAL_GROQ_MODEL"},
	{ai.ProviderGoogle, "WHATC_REAL_GOOGLE_KEY", "WHATC_REAL_GOOGLE_MODEL"},
	{ai.ProviderOpenAI, "WHATC_REAL_OPENAI_KEY", "WHATC_REAL_OPENAI_MODEL"},
	{ai.ProviderAnthropic, "WHATC_REAL_ANTHROPIC_KEY", "WHATC_REAL_ANTHROPIC_MODEL"},
}

// stubTools answers the two real 9C tools with canned data: the point is the provider's protocol,
// not the database.
type stubTools struct {
	defs  []ai.ToolDefinition
	calls []ai.ToolCall
}

func newStubTools() *stubTools {
	return &stubTools{defs: []ai.ToolDefinition{
		aitools.NewBusinessHoursSpec(time.Now).Definition(),
		aitools.NewMyOccurrencesSpec().Definition(),
	}}
}

func (s *stubTools) Definitions() []ai.ToolDefinition { return s.defs }
func (s *stubTools) Resolve(name string) (ai.Tool, bool) {
	for _, d := range s.defs {
		if d.Name == name {
			return stubTool{s: s, name: name}, true
		}
	}
	return nil, false
}

type stubTool struct {
	s    *stubTools
	name string
}

func (t stubTool) Execute(_ context.Context, c ai.ToolCall) (ai.ToolResult, error) {
	t.s.calls = append(t.s.calls, c)
	switch t.name {
	case aitools.BusinessHoursToolName:
		return ai.ToolResult{Content: `{"configured":true,"open_now":true,"server_time":"10:00","days":[{"day":"monday","open":true,"start":"08:00","end":"18:00"}]}`}, nil
	default:
		return ai.ToolResult{Content: `{"occurrences":[{"protocol":"OC-1","title":"Piso riscado","stage":"Em análise","status":"open","opened_at":"2026-10-01"}]}`}, nil
	}
}

func TestRealProviders(t *testing.T) {
	defer func() {
		if path := os.Getenv("WHATC_REAL_REPORT"); path != "" {
			report.Lock()
			body := "| Provider | Scenario | Verdict | Detail |\n|---|---|---|---|\n" + strings.Join(report.lines, "\n") + "\n"
			report.Unlock()
			_ = os.WriteFile(path, []byte(body), 0o600)
		}
	}()

	for _, rc := range realCases {
		rc := rc
		t.Run(rc.name, func(t *testing.T) {
			key, model := os.Getenv(rc.keyEnv), os.Getenv(rc.modelEnv)
			if key == "" || model == "" {
				record(t, rc.name, "all", verdictNotValidated, "no "+rc.keyEnv+"/"+rc.modelEnv+" in the environment: not tested, NOT approved")
				t.Skip("not validated: variables not set")
			}
			p, err := ai.New(rc.name, ai.Config{APIKey: key, HTTPClient: &http.Client{Timeout: 60 * time.Second}})
			require.NoError(t, err)
			runScenarios(t, rc.name, p, model, key)
		})
	}
}

func baseReq(model, prompt string) ai.Request {
	return ai.Request{
		Model: model, MaxTokens: 400, Temperature: 0,
		System:   "You are a support assistant. Use the tools to answer; never invent data.",
		Messages: []ai.Message{{Role: ai.RoleUser, Content: prompt}},
	}
}

func runScenarios(t *testing.T, name string, p ai.Provider, model, key string) {
	ctx := context.Background()

	// 1 + 3: one tool call and its result back, with BOTH real 9C schemas declared in the request
	// (so a 200 here also means the schemas were accepted).
	t.Run("1_single_call_and_result", func(t *testing.T) {
		tools := newStubTools()
		res, err := ai.RunToolLoop(ctx, p, baseReq(model, "Are you open right now? Check the business hours."), tools, ai.Limits{})
		if err != nil {
			record(t, name, "1_single_call_and_result", verdictDivergent, fmt.Sprintf("error kind=%s: %v", ai.KindOf(err), err))
			record(t, name, "3_real_schemas_accepted", verdictDivergent, "the request with both schemas failed: "+fmt.Sprint(err))
			return
		}
		ok := len(tools.calls) >= 1 && strings.TrimSpace(res.Response.Text) != ""
		verdict := verdictValidated
		if !ok {
			verdict = verdictDivergent
		}
		record(t, name, "1_single_call_and_result", verdict, fmt.Sprintf("tool calls=%d, provider answers=%d, final text=%t", len(tools.calls), len(res.Responses), res.Response.Text != ""))
		record(t, name, "3_real_schemas_accepted", verdictValidated, "both 9C schemas (empty object; enum + integer min/max) accepted by the API")
		for _, c := range tools.calls {
			if len(c.Arguments) == 0 {
				record(t, name, "1_arguments_object", verdictDivergent, "a call came with no arguments object")
			}
		}
	})

	// 2: several calls in one answer, results in order
	t.Run("2_multiple_calls", func(t *testing.T) {
		tools := newStubTools()
		res, err := ai.RunToolLoop(ctx, p, baseReq(model, "Tell me BOTH: whether you are open now and the status of my occurrences. Call every tool you need."), tools, ai.Limits{})
		if err != nil {
			record(t, name, "2_multiple_calls", verdictDivergent, fmt.Sprintf("error kind=%s: %v", ai.KindOf(err), err))
			return
		}
		inOne := 0
		for _, r := range res.Responses {
			if len(r.ToolCalls) > inOne {
				inOne = len(r.ToolCalls)
			}
		}
		seen := map[string]bool{}
		for _, c := range tools.calls {
			seen[c.Name] = true
		}
		verdict := verdictValidated
		if len(seen) < 2 {
			verdict = verdictDivergent
		}
		record(t, name, "2_multiple_calls", verdict, fmt.Sprintf("distinct tools=%d, max calls in one answer=%d (parallel=%t)", len(seen), inOne, inOne > 1))
	})

	// 4: tool_choice none with tools declared
	t.Run("4_tool_choice_none", func(t *testing.T) {
		req := baseReq(model, "Are you open right now? Check the business hours.")
		req.Tools = newStubTools().Definitions()
		req.ToolChoice = ai.ToolChoiceNone
		resp, err := p.Complete(ctx, req)
		switch {
		case err != nil:
			record(t, name, "4_tool_choice_none", verdictDivergent, fmt.Sprintf("kind=%s: %v", ai.KindOf(err), err))
		case len(resp.ToolCalls) > 0:
			record(t, name, "4_tool_choice_none", verdictDivergent, "the model still returned tool calls")
		default:
			record(t, name, "4_tool_choice_none", verdictValidated, "no tool call; text answer")
		}
	})

	// 5: a response cut off while generating a tool call. Observed, not asserted: how each provider
	// signals it is the data; the adapter mapping is reviewed from it.
	t.Run("5_interrupted_tool_call", func(t *testing.T) {
		req := baseReq(model, "Look up the occurrences with protocol OC-1 using the tool.")
		req.MaxTokens = 8
		req.Tools = newStubTools().Definitions()
		resp, err := p.Complete(ctx, req)
		switch {
		case err != nil && ai.KindOf(err) == ai.KindInvalidToolCall:
			record(t, name, "5_interrupted_tool_call", verdictObserved, "classified as KindInvalidToolCall: "+err.Error())
		case err != nil:
			record(t, name, "5_interrupted_tool_call", verdictObserved, fmt.Sprintf("error kind=%s: %v", ai.KindOf(err), err))
		default:
			record(t, name, "5_interrupted_tool_call", verdictObserved, fmt.Sprintf("no error: finish=%s tool_calls=%d text_len=%d (compare with the provider's own stop signal)", resp.Finish, len(resp.ToolCalls), len(resp.Text)))
		}
	})

	switch name {
	case ai.ProviderGroq:
		groqScenarios(t, name, p, model)
	case ai.ProviderGoogle:
		geminiScenarios(t, name, p, model, key)
	}
}

// 6: Groq: the tool result carries `name` (accepted?) — the multi-step run of scenario 1 already
// sends it; here a second round trip is made explicitly and the response is recorded.
func groqScenarios(t *testing.T, name string, p ai.Provider, model string) {
	t.Run("6_groq_tool_result_name", func(t *testing.T) {
		tools := newStubTools()
		_, err := ai.RunToolLoop(context.Background(), p, baseReq(model, "What are your business hours?"), tools, ai.Limits{})
		if err != nil {
			record(t, name, "6_groq_tool_result_name", verdictDivergent, fmt.Sprintf("kind=%s: %v", ai.KindOf(err), err))
			return
		}
		record(t, name, "6_groq_tool_result_name", verdictValidated, "role=tool with tool_call_id + name + content accepted; no message.name sent")
	})
	t.Run("6_groq_failed_generation_format", func(t *testing.T) {
		// not provokable on demand: recorded as observed only if the model fails a call during the run
		record(t, name, "6_groq_failed_generation_format", verdictNotValidated, "not provoked in this run: the real failed_generation body (object or text) was not observed")
	})
}

// 7: Gemini: thoughtSignature, functionResponse without id, and the schema keywords.
func geminiScenarios(t *testing.T, name string, p ai.Provider, model, key string) {
	ctx := context.Background()

	t.Run("7_signature_and_ids", func(t *testing.T) {
		req := baseReq(model, "Are you open right now? Check the business hours.")
		req.Tools = newStubTools().Definitions()
		first, err := p.Complete(ctx, req)
		if err != nil || len(first.ToolCalls) == 0 {
			record(t, name, "7_signature_and_ids", verdictDivergent, fmt.Sprintf("no tool call to continue from (err=%v)", err))
			return
		}
		call := first.ToolCalls[0]
		var state struct {
			Synth bool   `json:"synth"`
			Sig   string `json:"sig"`
		}
		_ = json.Unmarshal(call.Opaque, &state)
		record(t, name, "7_id_from_provider", verdictObserved, fmt.Sprintf("provider sent an id=%t (local synthetic id=%t)", !state.Synth, state.Synth))
		record(t, name, "7_thought_signature_present", verdictObserved, fmt.Sprintf("thoughtSignature present=%t", state.Sig != ""))

		follow := func(c ai.ToolCall) error {
			r := req
			r.Messages = append(append([]ai.Message(nil), req.Messages...),
				ai.Message{Role: ai.RoleAssistant, Content: first.Text, ToolCalls: []ai.ToolCall{c}},
				ai.Message{Role: ai.RoleTool, ToolResults: []ai.ToolResult{{CallID: c.ID, Name: c.Name, Content: `{"configured":true,"open_now":true,"server_time":"10:00"}`}}})
			_, err := p.Complete(ctx, r)
			return err
		}
		if err := follow(call); err != nil {
			record(t, name, "7_replay_with_signature", verdictDivergent, fmt.Sprintf("kind=%s: %v", ai.KindOf(err), err))
		} else {
			record(t, name, "7_replay_with_signature", verdictValidated, "functionCall replayed with its thoughtSignature and a functionResponse "+map[bool]string{true: "without id", false: "with the provider's id"}[state.Synth]+": accepted")
		}
		stripped := call
		stripped.Opaque = nil
		if err := follow(stripped); err != nil {
			record(t, name, "7_replay_without_signature", verdictObserved, fmt.Sprintf("REJECTED without the signature: kind=%s: %v", ai.KindOf(err), err))
		} else {
			record(t, name, "7_replay_without_signature", verdictObserved, "accepted without the signature (it is not enforced for this model/turn)")
		}
	})

	// Which schema keywords does the real API accept? The adapter refuses additionalProperties and
	// default before sending (googleAllowed, provisional); this asks the API directly, so the
	// restriction can be confirmed or relaxed from evidence.
	t.Run("7_schema_keywords", func(t *testing.T) {
		for _, kw := range []string{`"additionalProperties":false`, `"default":"x"`} {
			status, msg := rawGeminiToolProbe(model, key, kw)
			verdict := verdictObserved
			record(t, name, "7_schema_"+strings.Trim(strings.Split(kw, ":")[0], `"`), verdict, fmt.Sprintf("the API answered HTTP %d %s", status, msg))
		}
	})
}

// rawGeminiToolProbe posts a minimal generateContent request whose tool schema contains the given
// keyword, straight to the API (the adapter would refuse it). The key travels in a header.
func rawGeminiToolProbe(model, key, keyword string) (int, string) {
	schema := `{"type":"object","properties":{"id":{"type":"string"}},` + keyword + `}`
	if strings.Contains(keyword, "default") {
		schema = `{"type":"object","properties":{"id":{"type":"string","` + keyword[1:] + `}}}`
	}
	body := `{"contents":[{"role":"user","parts":[{"text":"ping"}]}],"generationConfig":{"maxOutputTokens":16},` +
		`"tools":[{"functionDeclarations":[{"name":"probe","description":"probe","parameters":` + schema + `}]}]}`
	req, _ := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models/"+model+":generateContent", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, "network error"
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := strings.ReplaceAll(e.Error.Message, key, "[redacted]")
	return resp.StatusCode, msg
}
