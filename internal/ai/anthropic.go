package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

const anthropicVersion = "2023-06-01"

type anthropicProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newAnthropic(cfg Config) *anthropicProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	return &anthropicProvider{baseURL: base, apiKey: cfg.APIKey, client: cfg.HTTPClient}
}

func (p *anthropicProvider) Name() string { return ProviderAnthropic }

func (p *anthropicProvider) Capabilities() Capabilities {
	return Capabilities{ListModels: true, ToolCalling: true, StructuredOutputs: true, Streaming: true}
}

func (p *anthropicProvider) headers() map[string]string {
	return map[string]string{"x-api-key": p.apiKey, "anthropic-version": anthropicVersion}
}

func (p *anthropicProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	if derr := checkToolRequest("Anthropic", req); derr != nil {
		return nil, derr
	}
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, anthropicMessage(m))
	}
	payload := map[string]any{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": req.MaxTokens,
	}
	if req.System != "" {
		payload["system"] = req.System
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Parameters})
		}
		payload["tools"] = tools
		if req.ToolChoice == ToolChoiceNone {
			payload["tool_choice"] = map[string]any{"type": "none"}
		} // auto is the provider default
	}

	raw, derr := doJSON(ctx, p.client, "Anthropic", p.apiKey, http.MethodPost, p.baseURL+"/messages", p.headers(), payload)
	if derr != nil {
		return nil, derr
	}
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse("Anthropic")
	}
	resp := &Response{
		Model: out.Model,
		Usage: Usage{
			InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
			TotalTokens: out.Usage.InputTokens + out.Usage.OutputTokens,
		},
		Finish: finishFromAnthropic(out.StopReason),
	}
	hasText := false
	for _, c := range out.Content {
		switch c.Type {
		case "text":
			if !hasText { // the first text block, as before tool calling existed
				resp.Text, hasText = strings.TrimSpace(c.Text), true
			}
		case "tool_use":
			if len(req.Tools) == 0 {
				continue // no tools were offered: as before tool calling existed, only text counts
			}
			args, ok := argsObject(strings.TrimSpace(string(c.Input)))
			if string(c.Input) == "null" {
				args, ok = json.RawMessage(`{}`), true
			}
			if c.ID == "" || c.Name == "" || !ok {
				return nil, invalidToolCall("Anthropic", "the model returned a tool_use that is not valid")
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Arguments: args})
		}
	}
	if len(resp.ToolCalls) > 0 {
		if resp.Finish == FinishLength {
			return nil, invalidToolCall("Anthropic", "tool call cut off by the token limit")
		}
		resp.Finish = FinishToolCalls
		return resp, nil
	}
	if !hasText {
		return nil, &Error{Provider: "Anthropic", Kind: KindEmptyResponse, Message: "no text response from Anthropic"}
	}
	return resp, nil
}

func finishFromAnthropic(r string) FinishReason {
	switch r {
	case "end_turn", "stop_sequence":
		return FinishStop
	case "tool_use":
		return FinishToolCalls
	case "max_tokens":
		return FinishLength
	}
	return FinishOther
}

// anthropicMessage is the wire form of one neutral message. All the results of a round go in
// ONE user message of tool_result blocks, right after the assistant message with the tool_use.
func anthropicMessage(m Message) map[string]any {
	switch {
	case m.Role == RoleTool:
		blocks := make([]map[string]any, 0, len(m.ToolResults))
		for _, r := range m.ToolResults {
			b := map[string]any{"type": "tool_result", "tool_use_id": r.CallID, "content": r.Content}
			if r.IsError {
				b["is_error"] = true
			}
			blocks = append(blocks, b)
		}
		return map[string]any{"role": "user", "content": blocks}
	case len(m.ToolCalls) > 0:
		blocks := make([]map[string]any, 0, len(m.ToolCalls)+1)
		if m.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
		}
		for _, c := range m.ToolCalls {
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Arguments})
		}
		return map[string]any{"role": "assistant", "content": blocks}
	}
	return map[string]any{"role": string(m.Role), "content": m.Content}
}

func (p *anthropicProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	raw, derr := doJSON(ctx, p.client, "Anthropic", p.apiKey, http.MethodGet, p.baseURL+"/models?limit=1000", p.headers(), nil)
	if derr != nil {
		return nil, derr
	}
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse("Anthropic")
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			models = append(models, ModelInfo{ID: m.ID, DisplayName: m.DisplayName})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
