package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ListAIModels asks an AI provider for the models it offers, using the
// provider's own listing endpoint (Whatc keeps no model lists).
//
//	POST /api/chatbot/ai/models   {"provider": "groq", "api_key": "optional"}
//
// With api_key the typed (not yet saved) key is used for this one call and never
// stored. Without it the organization's saved key is used, but only if it
// belongs to the same provider: a key is never sent to a provider it was not
// issued for. The key is never part of any response.
func (a *App) ListAIModels(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSettingsChatbot, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if !isSupportedAIProvider(provider) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unsupported AI provider", nil, "")
	}

	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		settings, err := a.getChatbotSettingsCached(orgID, "")
		if err != nil || string(settings.AI.Provider) != provider || settings.AI.APIKey == "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Enter the API key for this provider to list its models", nil, "")
		}
		if apiKey, err = a.resolveAIAPIKey(settings); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The saved API key cannot be used; enter it again", nil, "")
		}
	}

	p, err := a.newAIProvider(provider, apiKey)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unsupported AI provider", nil, "")
	}

	meta := aiCallMeta{Feature: aiFeatureModelList, OrgID: orgID, UserID: &userID}
	start := time.Now()
	// Not the fasthttp RequestCtx: it is recycled after the handler returns and is
	// not a real cancellable context. A bounded background context is.
	ctx, cancel := context.WithTimeout(context.Background(), ai.DefaultTimeout)
	defer cancel()
	list, err := p.ListModels(ctx)
	a.recordAIUsage(meta, provider, "", nil, time.Since(start), err)
	if err != nil {
		var aerr *ai.Error
		if errors.As(err, &aerr) && aerr.Kind == ai.KindAuth {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The provider rejected the API key", nil, "")
		}
		a.Log.Error("Failed to list AI models", "provider", provider, "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not load models from the provider: "+err.Error(), nil, "")
	}

	return r.SendEnvelope(map[string]any{"provider": provider, "models": list})
}
