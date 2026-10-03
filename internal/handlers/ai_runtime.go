package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/knowledge"
	"github.com/shridarpatil/whatomate/internal/models"
)

// AI features (the value of AIUsageLog.Feature).
const (
	aiFeatureChatbotReply = "chatbot_reply"     // keyword/AI fallback reply
	aiFeatureChatbotNode  = "chatbot_flow_node" // ai_response node in a flow graph
	aiFeatureModelList    = "model_list"        // admin loading a provider's models
)

// aiCallMeta identifies the context of one AI call for the usage log. No
// content: ids only.
type aiCallMeta struct {
	Feature   string
	OrgID     uuid.UUID
	Account   string
	UserID    *uuid.UUID
	ContactID *uuid.UUID
	SessionID *uuid.UUID
	// KnowledgeSources are the Knowledge chunks put in the prompt, in prompt order (ids,
	// title, origin and score; never text). Empty when Knowledge was not used.
	KnowledgeSources []knowledge.Source
	// Tools is set when this call may use tools (Fase 9B): the server-built scope and the actor.
	// nil means no tools, exactly as before. Even when set, tools run only if ai_tools.enabled is
	// on, the catalog has tools and the organization enabled one (see completeAI).
	Tools *aiToolsRun
}

// aiToolsRun is the identity and scope a governed tool run is made with. The server builds it
// from the chatbot session; nothing in it comes from what the model said.
type aiToolsRun struct {
	Scope aitools.Scope
	Actor aitools.Actor
}

// newAIProvider builds the adapter for provider using a plaintext key. Callers
// get the key from resolveAIAPIKey or from an admin's request, never from storage.
func (a *App) newAIProvider(provider, apiKey string) (ai.Provider, error) {
	return ai.New(provider, ai.Config{APIKey: apiKey, HTTPClient: a.HTTPClient})
}

// completeAI is the one door every AI completion goes through: it resolves the
// credential, calls the provider through the neutral interface and writes the
// usage log. Features never call a vendor directly.
func (a *App) completeAI(ctx context.Context, settings *models.ChatbotSettings, meta aiCallMeta, req ai.Request) (*ai.Response, error) {
	provider := string(settings.AI.Provider)

	apiKey, err := a.resolveAIAPIKey(settings)
	if err != nil {
		a.recordAIUsage(meta, provider, req.Model, nil, 0, err)
		return nil, err
	}
	p, err := a.newAIProvider(provider, apiKey)
	if err != nil {
		a.recordAIUsage(meta, provider, req.Model, nil, 0, err)
		return nil, err
	}

	if resolver := a.governedTools(ctx, meta, p); resolver != nil {
		return a.completeWithTools(ctx, p, provider, meta, req, resolver)
	}

	start := time.Now()
	resp, err := p.Complete(ctx, req)
	a.recordAIUsage(meta, provider, req.Model, resp, time.Since(start), err)
	return resp, err
}

// governedTools returns the governed resolver for this call, or nil when the call must go the
// plain way, which is the case unless ALL of these hold: the caller asked for tools, the global
// ai_tools.enabled switch is on, the catalog has at least one tool, the provider can call tools
// and the organization enabled at least one tool that the policy lets the AI use. With the
// production catalog empty (Fase 9B) this is always nil, and nothing extra is even queried.
func (a *App) governedTools(ctx context.Context, meta aiCallMeta, p ai.Provider) *aitools.Resolver {
	if meta.Tools == nil || !a.aiToolsGloballyEnabled() || len(a.aiToolCatalog().Names()) == 0 || !p.Capabilities().ToolCalling {
		return nil
	}
	// the provider has to be on the validated list (ai_tools.providers); an unvalidated one never
	// gets tools, however everything else is set
	if !a.Config.AITools.ProviderValidated(p.Name()) {
		return nil
	}
	secret := ""
	if a.Config != nil {
		secret = a.Config.App.EncryptionKey
	}
	r := aitools.NewResolver(ctx, aitools.ResolverConfig{
		Catalog: a.aiToolCatalog(), GlobalEnabled: true, Auditor: aitools.DBAuditor{DB: a.DB, Secret: secret},
		Actor: meta.Tools.Actor, Scope: meta.Tools.Scope, Log: a.Log, Deps: aitools.Deps{Read: aitools.NewReadDB(a.DB)},
		WriteEnabled: a.Config.AITools.WriteEnabled, ConfirmationAvailable: a.aiToolConfirmationsReady(),
	}, aitools.SettingsStore{DB: a.DB})
	if len(r.Definitions()) == 0 {
		return nil
	}
	return r
}

// completeWithTools runs the bounded tool loop through the governed resolver and writes one
// AIUsageLog row per provider call, with the real latency of each. A failed loop surfaces as an
// ordinary AI error, so the caller falls back exactly as it does today.
func (a *App) completeWithTools(ctx context.Context, p ai.Provider, provider string, meta aiCallMeta, req ai.Request, r *aitools.Resolver) (*ai.Response, error) {
	res, err := ai.RunToolLoop(ctx, p, req, r, ai.Limits{})
	for i, resp := range res.Responses {
		a.recordAIUsage(meta, provider, req.Model, resp, res.Took[i], nil)
	}
	if err != nil {
		if errors.Is(err, ai.ErrToolLoopLimit) {
			// the round the loop refused ran nothing; leave the attempts on record
			if res.Response != nil {
				r.RecordAborted(ctx, res.Response.ToolCalls)
			}
		} else {
			a.recordAIUsage(meta, provider, req.Model, nil, res.FailedTook, err)
		}
		return nil, err
	}
	return res.Response, nil
}

// recordAIUsage persists one AIUsageLog row. A failure to log is logged and
// swallowed: observability must never break a customer's reply.
func (a *App) recordAIUsage(meta aiCallMeta, provider, model string, resp *ai.Response, took time.Duration, callErr error) {
	entry := models.AIUsageLog{
		OrganizationID:  meta.OrgID,
		Provider:        provider,
		Model:           model,
		Feature:         meta.Feature,
		UserID:          meta.UserID,
		ContactID:       meta.ContactID,
		SessionID:       meta.SessionID,
		WhatsAppAccount: meta.Account,
		Success:         callErr == nil,
		LatencyMs:       int(took.Milliseconds()),
	}
	if len(meta.KnowledgeSources) > 0 { // recorded even when the provider call failed: they were used
		entry.KnowledgeSources = make(models.JSONBArray, len(meta.KnowledgeSources))
		for i, s := range meta.KnowledgeSources {
			entry.KnowledgeSources[i] = map[string]any{
				"document_id": s.DocumentID.String(), "title": s.Title, "origin": s.Origin, "score": s.Score,
			}
		}
	}
	if resp != nil {
		if resp.Model != "" {
			entry.Model = resp.Model
		}
		entry.InputTokens, entry.OutputTokens, entry.TotalTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.TotalTokens
	}
	if callErr != nil {
		var aerr *ai.Error
		switch {
		case errors.As(callErr, &aerr):
			entry.ErrorKind, entry.HTTPStatus, entry.ErrorMessage = string(aerr.Kind), aerr.Status, truncate(aerr.Message, 500)
		case errors.Is(callErr, ErrAIKeyUnavailable):
			entry.ErrorKind, entry.ErrorMessage = "credentials", "AI API key is not available"
		default:
			entry.ErrorKind, entry.ErrorMessage = "error", truncate(callErr.Error(), 500)
		}
	}
	if err := a.DB.Create(&entry).Error; err != nil {
		a.Log.Error("Failed to record AI usage", "error", err, "provider", provider, "feature", meta.Feature)
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func isSupportedAIProvider(name string) bool {
	for _, p := range ai.Supported() {
		if p == name {
			return true
		}
	}
	return false
}
