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
	messages := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, map[string]string{"role": string(m.Role), "content": m.Content})
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

	raw, derr := doJSON(ctx, p.client, "Anthropic", p.apiKey, http.MethodPost, p.baseURL+"/messages", p.headers(), payload)
	if derr != nil {
		return nil, derr
	}
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, badResponse("Anthropic")
	}
	for _, c := range out.Content {
		if c.Type == "text" {
			return &Response{
				Text:  strings.TrimSpace(c.Text),
				Model: out.Model,
				Usage: Usage{
					InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
					TotalTokens: out.Usage.InputTokens + out.Usage.OutputTokens,
				},
			}, nil
		}
	}
	return nil, &Error{Provider: "Anthropic", Kind: KindEmptyResponse, Message: "no text response from Anthropic"}
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
