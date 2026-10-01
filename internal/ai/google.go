package ai

import (
	"context"
	"encoding/json"
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
	contents := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]string{{"text": m.Content}},
		})
	}
	generation := map[string]any{"maxOutputTokens": req.MaxTokens}
	if req.Temperature > 0 {
		generation["temperature"] = req.Temperature
	}
	payload := map[string]any{"contents": contents, "generationConfig": generation}
	if req.System != "" {
		payload["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": req.System}}}
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
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
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
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return nil, emptyResponse("Google AI")
	}
	return &Response{
		Text:  strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text),
		Model: out.ModelVersion,
		Usage: Usage{
			InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount,
			TotalTokens: out.UsageMetadata.TotalTokenCount,
		},
	}, nil
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
