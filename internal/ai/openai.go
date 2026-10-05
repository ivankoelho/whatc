package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// openAICompat speaks the OpenAI Chat Completions dialect. It serves OpenAI and
// Groq (OpenAI-compatible), which differ only in name and base URL.
type openAICompat struct {
	name    string // provider id: "openai", "groq"
	label   string // used in error messages: "OpenAI", "Groq"
	baseURL string
	apiKey  string
	client  *http.Client
}

func newOpenAICompat(name, label, defaultBase string, cfg Config) *openAICompat {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBase
	}
	return &openAICompat{name: name, label: label, baseURL: base, apiKey: cfg.APIKey, client: cfg.HTTPClient}
}

func (p *openAICompat) Name() string { return p.name }

func (p *openAICompat) Capabilities() Capabilities {
	return Capabilities{ListModels: true, ToolCalling: true, StructuredOutputs: true, Streaming: true}
}

func (p *openAICompat) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

func (p *openAICompat) Complete(ctx context.Context, req Request) (*Response, error) {
	if derr := checkToolRequest(p.label, req); derr != nil {
		return nil, derr
	}
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, p.wireMessages(m)...)
	}

	payload := map[string]any{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": req.MaxTokens,
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}})
		}
		payload["tools"] = tools
		if req.ToolChoice == ToolChoiceNone {
			payload["tool_choice"] = "none"
		} // auto is the provider default
	}

	raw, derr := doJSON(ctx, p.client, p.label, p.apiKey, http.MethodPost, p.baseURL+"/chat/completions", p.headers(), payload)
	if derr != nil {
		// A 400 whose body says the model failed to produce a valid tool call is the model's
		// error, not a bad request of ours.
		if len(req.Tools) > 0 && derr.Status == http.StatusBadRequest && derr.Code == "tool_use_failed" {
			derr.Kind = KindInvalidToolCall
		}
		return nil, derr
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"` // a JSON document inside a string
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse(p.label)
	}
	if len(out.Choices) == 0 {
		return nil, emptyResponse(p.label)
	}
	ch := out.Choices[0]
	resp := &Response{
		Text:   strings.TrimSpace(ch.Message.Content),
		Model:  out.Model,
		Usage:  Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens, TotalTokens: out.Usage.TotalTokens},
		Finish: finishFromOpenAI(ch.FinishReason),
	}
	for _, c := range ch.Message.ToolCalls {
		if len(req.Tools) == 0 {
			break // no tools were offered: as before tool calling existed, only text counts
		}
		args, ok := argsObject(c.Function.Arguments)
		if c.ID == "" || c.Function.Name == "" || !ok {
			return nil, invalidToolCall(p.label, "the model returned a tool call that is not valid JSON")
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: args})
	}
	if len(resp.ToolCalls) > 0 {
		if resp.Finish == FinishLength {
			return nil, invalidToolCall(p.label, "tool call cut off by the token limit")
		}
		resp.Finish = FinishToolCalls
	}
	return resp, nil
}

func finishFromOpenAI(r string) FinishReason {
	switch r {
	case "stop":
		return FinishStop
	case "tool_calls", "function_call":
		return FinishToolCalls
	case "length":
		return FinishLength
	}
	return FinishOther
}

// wireMessages turns one neutral message into the Chat Completions wire form. Only the result
// of a tool carries a "name", and only on Groq, which documents it there; a message "name" is
// never sent (Groq rejects messages[].name). OpenAI's tool message is role/tool_call_id/content.
func (p *openAICompat) wireMessages(m Message) []map[string]any {
	switch {
	case m.Role == RoleTool:
		out := make([]map[string]any, 0, len(m.ToolResults))
		for _, r := range m.ToolResults {
			msg := map[string]any{"role": "tool", "tool_call_id": r.CallID, "content": r.Content}
			if p.name == ProviderGroq {
				msg["name"] = r.Name
			}
			out = append(out, msg)
		}
		return out
	case len(m.ToolCalls) > 0:
		calls := make([]map[string]any, 0, len(m.ToolCalls))
		for _, c := range m.ToolCalls {
			calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{
				"name": c.Name, "arguments": string(c.Arguments),
			}})
		}
		msg := map[string]any{"role": "assistant", "tool_calls": calls}
		if m.Content != "" {
			msg["content"] = m.Content
		}
		return []map[string]any{msg}
	}
	return []map[string]any{{"role": string(m.Role), "content": m.Content}}
}

// nonChatModel marks listing entries that cannot serve chat completions
// (speech, embeddings, image, moderation). Excluding them keeps the model
// picker usable; it is a filter on the provider's own list, not a list of ours.
func nonChatModel(id string) bool {
	id = strings.ToLower(id)
	for _, s := range []string{"whisper", "tts", "embed", "dall-e", "moderation", "transcribe"} {
		if strings.Contains(id, s) {
			return true
		}
	}
	return false
}

func (p *openAICompat) ListModels(ctx context.Context) ([]ModelInfo, error) {
	raw, derr := doJSON(ctx, p.client, p.label, p.apiKey, http.MethodGet, p.baseURL+"/models", p.headers(), nil)
	if derr != nil {
		return nil, derr
	}
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse(p.label)
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID == "" || nonChatModel(m.ID) {
			continue
		}
		models = append(models, ModelInfo{ID: m.ID, OwnedBy: m.OwnedBy})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
