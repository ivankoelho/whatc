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
	messages := make([]map[string]string, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, map[string]string{"role": string(m.Role), "content": m.Content})
	}

	payload := map[string]any{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": req.MaxTokens,
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}

	raw, derr := doJSON(ctx, p.client, p.label, p.apiKey, http.MethodPost, p.baseURL+"/chat/completions", p.headers(), payload)
	if derr != nil {
		return nil, derr
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
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
	return &Response{
		Text:  strings.TrimSpace(out.Choices[0].Message.Content),
		Model: out.Model,
		Usage: Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens, TotalTokens: out.Usage.TotalTokens},
	}, nil
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
