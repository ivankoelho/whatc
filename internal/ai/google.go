package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type googleProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newGoogle(cfg Config) *googleProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &googleProvider{baseURL: base, apiKey: cfg.APIKey, client: cfg.HTTPClient}
}

func (p *googleProvider) Name() string { return ProviderGoogle }

func (p *googleProvider) Capabilities() Capabilities {
	return Capabilities{ListModels: true, ToolCalling: true, StructuredOutputs: true, Streaming: true}
}

// The key travels in a header, never in the URL, so it cannot land in access
// logs of proxies or in error strings that embed the URL.
func (p *googleProvider) headers() map[string]string {
	return map[string]string{"x-goog-api-key": p.apiKey}
}

func (p *googleProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	if derr := checkToolRequest("Google AI", req); derr != nil {
		return nil, derr
	}
	contents := make([]map[string]any, 0, len(req.Messages))
	var lastCalls []ToolCall // the calls of the assistant message just before, for ordering results
	for _, m := range req.Messages {
		contents = append(contents, googleContent(m, lastCalls))
		lastCalls = m.ToolCalls
	}
	generation := map[string]any{"maxOutputTokens": req.MaxTokens}
	if req.Temperature > 0 {
		generation["temperature"] = req.Temperature
	}
	payload := map[string]any{"contents": contents, "generationConfig": generation}
	if req.System != "" {
		payload["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": req.System}}}
	}
	if len(req.Tools) > 0 {
		decls := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			if err := googleSchemaOK(t.Parameters); err != nil {
				return nil, &Error{Provider: "Google AI", Kind: KindInvalidRequest, Message: "tool " + t.Name + ": " + err.Error()}
			}
			decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters})
		}
		payload["tools"] = []map[string]any{{"functionDeclarations": decls}}
		if req.ToolChoice == ToolChoiceNone {
			payload["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "NONE"}}
		} // AUTO is the provider default
	}

	endpoint := p.baseURL + "/models/" + url.PathEscape(req.Model) + ":generateContent"
	raw, derr := doJSON(ctx, p.client, "Google AI", p.apiKey, http.MethodPost, endpoint, p.headers(), payload)
	if derr != nil {
		return nil, derr
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						ID   string          `json:"id"`
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
					ThoughtSignature string `json:"thoughtSignature"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse("Google AI")
	}
	usage := Usage{
		InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount,
		TotalTokens: out.UsageMetadata.TotalTokenCount,
	}
	if len(req.Tools) == 0 { // as before tool calling existed
		if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
			return nil, emptyResponse("Google AI")
		}
		return &Response{Text: strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text), Model: out.ModelVersion, Usage: usage}, nil
	}

	if len(out.Candidates) == 0 {
		return nil, emptyResponse("Google AI")
	}
	cand := out.Candidates[0]
	if cand.FinishReason == "MALFORMED_FUNCTION_CALL" || cand.FinishReason == "UNEXPECTED_TOOL_CALL" {
		return nil, invalidToolCall("Google AI", "the model produced a tool call that cannot be used ("+cand.FinishReason+")")
	}
	resp := &Response{Model: out.ModelVersion, Usage: usage, Finish: finishFromGoogle(cand.FinishReason)}
	hasText := false
	for _, part := range cand.Content.Parts {
		switch {
		case part.FunctionCall != nil:
			fc := part.FunctionCall
			args, ok := argsObject(strings.TrimSpace(string(fc.Args)))
			if string(fc.Args) == "null" {
				args, ok = json.RawMessage(`{}`), true
			}
			if fc.Name == "" || !ok {
				return nil, invalidToolCall("Google AI", "the model returned a functionCall that is not valid")
			}
			call := ToolCall{ID: fc.ID, Name: fc.Name, Arguments: args}
			state := googleOpaque{Sig: part.ThoughtSignature}
			if call.ID == "" { // the provider sent no id: a deterministic local one, never sent back
				call.ID, state.Synth = fmt.Sprintf("call_%d", len(resp.ToolCalls)), true
			}
			if state.Synth || state.Sig != "" {
				call.Opaque, _ = json.Marshal(state)
			}
			resp.ToolCalls = append(resp.ToolCalls, call)
		case part.Text != "" && !part.Thought && !hasText:
			resp.Text, hasText = strings.TrimSpace(part.Text), true
		}
	}
	if len(resp.ToolCalls) > 0 {
		if resp.Finish == FinishLength {
			return nil, invalidToolCall("Google AI", "tool call cut off by the token limit")
		}
		resp.Finish = FinishToolCalls
		return resp, nil
	}
	if !hasText {
		return nil, emptyResponse("Google AI")
	}
	return resp, nil
}

func finishFromGoogle(r string) FinishReason {
	switch r {
	case "STOP":
		return FinishStop
	case "MAX_TOKENS":
		return FinishLength
	}
	return FinishOther
}

// googleOpaque is what the Google adapter keeps in ToolCall.Opaque (transport state, never read
// elsewhere): the thoughtSignature that must go back on the same functionCall part, and whether
// the call's id was made up locally because Google sent none.
// PROVISIONAL: that a signature belongs to, and must return on, the functionCall part that carried
// it is the conservative reading of the API, not a confirmed fact. It is covered only by structural
// tests (a signature is never moved to another call; one on a text part is dropped). A real
// Gemini 3 test is needed before relying on it.
type googleOpaque struct {
	Synth bool   `json:"synth,omitempty"`
	Sig   string `json:"sig,omitempty"`
}

func googleState(c ToolCall) googleOpaque {
	var s googleOpaque
	if len(c.Opaque) > 0 {
		_ = json.Unmarshal(c.Opaque, &s)
	}
	return s
}

// googleContent is the wire form of one neutral message. lastCalls are the tool calls of the
// assistant message right before it: results go back in that order, because without provider
// ids Google pairs a functionResponse with its call by name and position.
func googleContent(m Message, lastCalls []ToolCall) map[string]any {
	switch {
	case m.Role == RoleTool:
		byID := make(map[string]ToolResult, len(m.ToolResults))
		for _, r := range m.ToolResults {
			byID[r.CallID] = r
		}
		parts := make([]map[string]any, 0, len(lastCalls))
		for _, c := range lastCalls {
			r := byID[c.ID]
			key := "result"
			if r.IsError {
				key = "error"
			}
			fr := map[string]any{"name": c.Name, "response": map[string]any{key: r.Content}}
			if !googleState(c).Synth {
				fr["id"] = c.ID
			}
			parts = append(parts, map[string]any{"functionResponse": fr})
		}
		return map[string]any{"role": "user", "parts": parts}
	case len(m.ToolCalls) > 0:
		parts := make([]map[string]any, 0, len(m.ToolCalls)+1)
		if m.Content != "" {
			parts = append(parts, map[string]any{"text": m.Content})
		}
		for _, c := range m.ToolCalls {
			state := googleState(c)
			fc := map[string]any{"name": c.Name, "args": c.Arguments}
			if !state.Synth {
				fc["id"] = c.ID
			}
			part := map[string]any{"functionCall": fc}
			if state.Sig != "" {
				part["thoughtSignature"] = state.Sig
			}
			parts = append(parts, part)
		}
		return map[string]any{"role": "model", "parts": parts}
	}
	role := "user"
	if m.Role == RoleAssistant {
		role = "model"
	}
	return map[string]any{"role": role, "parts": []map[string]string{{"text": m.Content}}}
}

// googleAllowed is a restriction of the GEMINI ADAPTER ONLY, not of the neutral ToolDefinition
// (whose Parameters is any JSON Schema object; each adapter validates what its provider takes).
// PROVISIONAL: it is a conservative subset of JSON Schema keywords for
// functionDeclarations[].parameters (the OpenAPI-style Schema), chosen from the field list of the
// official reference. It has NOT been confirmed against the real Gemini API and is covered only by
// structural tests. Anything outside it (additionalProperties, $ref, oneOf, default...) is rejected
// with a clear error instead of failing inside the provider. Revisit with a real Gemini test.
var googleAllowed = map[string]bool{
	"type": true, "format": true, "description": true, "nullable": true, "enum": true, "properties": true,
	"required": true, "items": true, "minItems": true, "maxItems": true, "minimum": true, "maximum": true,
	"title": true, "anyOf": true, "propertyOrdering": true,
}

// googleSchemaOK applies googleAllowed (Gemini adapter only, provisional) to a tool's schema.
func googleSchemaOK(raw json.RawMessage) error {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return err
	}
	return googleWalk(node)
}

func googleWalk(node any) error {
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys) // the same schema always reports the same keyword
		for _, k := range keys {
			child := v[k]
			if !googleAllowed[k] {
				return fmt.Errorf("schema keyword %q is not supported by Google AI", k)
			}
			switch k {
			case "properties": // property names are free; check each property's schema
				props, _ := child.(map[string]any)
				for _, p := range props {
					if err := googleWalk(p); err != nil {
						return err
					}
				}
			case "items":
				if err := googleWalk(child); err != nil {
					return err
				}
			case "anyOf":
				list, _ := child.([]any)
				for _, p := range list {
					if err := googleWalk(p); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (p *googleProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	raw, derr := doJSON(ctx, p.client, "Google AI", p.apiKey, http.MethodGet, p.baseURL+"/models?pageSize=1000", p.headers(), nil)
	if derr != nil {
		return nil, derr
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse("Google AI")
	}
	models := make([]ModelInfo, 0, len(out.Models))
	for _, m := range out.Models {
		canGenerate := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				canGenerate = true
			}
		}
		if !canGenerate {
			continue
		}
		models = append(models, ModelInfo{ID: strings.TrimPrefix(m.Name, "models/"), DisplayName: m.DisplayName})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
