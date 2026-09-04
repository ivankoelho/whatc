package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// chatbotExportVersion is the schema version of the portable export envelope.
// Bump it if the portable shape changes in a backward-incompatible way.
const chatbotExportVersion = 1

// portableKeywordRule is the installation-independent shape of a keyword rule.
// It deliberately omits IDs, organization, whatsapp_account, audit user refs
// and timestamps so the payload is portable between installations.
type portableKeywordRule struct {
	Name            string              `json:"name"`
	Keywords        []string            `json:"keywords"`
	MatchType       models.MatchType    `json:"match_type"`
	CaseSensitive   bool                `json:"case_sensitive"`
	ResponseType    models.ResponseType `json:"response_type"`
	ResponseContent map[string]any      `json:"response_content"`
	Conditions      string              `json:"conditions,omitempty"`
	Priority        int                 `json:"priority"`
	Enabled         bool                `json:"enabled"`
}

// portableFlow is the installation-independent shape of a chatbot flow,
// including the full v2 JSONB graph and the contact-info panel config.
type portableFlow struct {
	Name               string              `json:"name"`
	Description        string              `json:"description"`
	TriggerKeywords    []string            `json:"trigger_keywords"`
	TriggerButtonID    string              `json:"trigger_button_id,omitempty"`
	InitialMessage     string              `json:"initial_message"`
	InitialMessageType models.FlowStepType `json:"initial_message_type"`
	CompletionMessage  string              `json:"completion_message"`
	OnCompleteAction   string              `json:"on_complete_action"`
	CompletionConfig   map[string]any      `json:"completion_config"`
	TimeoutMessage     string              `json:"timeout_message,omitempty"`
	CancelKeywords     []string            `json:"cancel_keywords,omitempty"`
	PanelConfig        map[string]any      `json:"panel_config"`
	Graph              map[string]any      `json:"graph"`
	Enabled            bool                `json:"enabled"`
}

// chatbotExportEnvelope is the portable JSON document produced by export and
// consumed by import. Associated keyword rules are those whose keywords
// intersect the flow's trigger keywords (within the same organization).
type chatbotExportEnvelope struct {
	Version      int                   `json:"version"`
	ExportedAt   string                `json:"exported_at"`
	Flow         portableFlow          `json:"flow"`
	KeywordRules []portableKeywordRule `json:"keyword_rules"`
}

// keywordRulesForFlow returns the org's keyword rules whose keywords overlap
// (case-insensitively) with the flow's trigger keywords. The engine has no
// hard FK from keyword rules to flows, so trigger-keyword overlap is the
// practical association.
func keywordRulesForFlow(rules []models.KeywordRule, triggers models.StringArray) []models.KeywordRule {
	if len(triggers) == 0 {
		return nil
	}
	triggerSet := make(map[string]struct{}, len(triggers))
	for _, kw := range triggers {
		triggerSet[strings.ToLower(strings.TrimSpace(kw))] = struct{}{}
	}

	var matched []models.KeywordRule
	for _, rule := range rules {
		for _, kw := range rule.Keywords {
			if _, ok := triggerSet[strings.ToLower(strings.TrimSpace(kw))]; ok {
				matched = append(matched, rule)
				break
			}
		}
	}
	return matched
}

// ChatbotExport serializes a chatbot flow (graph + fields) and its associated
// keyword rules into a portable JSON envelope.
//
// POST /api/chatbot/export  body: {"flow_id": "<uuid>"}
func (a *App) ChatbotExport(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	if !a.HasPermission(userID, models.ResourceFlowsChatbot, models.ActionRead, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Permission denied", nil, "")
	}

	var req struct {
		FlowID string `json:"flow_id"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}

	flowID, err := uuid.Parse(strings.TrimSpace(req.FlowID))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Valid flow_id is required", nil, "")
	}

	var flow models.ChatbotFlow
	if err := a.DB.Where("id = ? AND organization_id = ?", flowID, orgID).First(&flow).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Flow not found", nil, "")
	}

	// Load org keyword rules and keep those associated with this flow.
	var rules []models.KeywordRule
	if err := a.DB.Where("organization_id = ?", orgID).Order("priority DESC, created_at DESC").
		Find(&rules).Error; err != nil {
		a.Log.Error("Failed to load keyword rules for export", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to export flow", nil, "")
	}
	associated := keywordRulesForFlow(rules, flow.TriggerKeywords)

	envelope := chatbotExportEnvelope{
		Version:    chatbotExportVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Flow: portableFlow{
			Name:               flow.Name,
			Description:        flow.Description,
			TriggerKeywords:    []string(flow.TriggerKeywords),
			TriggerButtonID:    flow.TriggerButtonID,
			InitialMessage:     flow.InitialMessage,
			InitialMessageType: flow.InitialMessageType,
			CompletionMessage:  flow.CompletionMessage,
			OnCompleteAction:   flow.OnCompleteAction,
			CompletionConfig:   map[string]any(flow.CompletionConfig),
			TimeoutMessage:     flow.TimeoutMessage,
			CancelKeywords:     []string(flow.CancelKeywords),
			PanelConfig:        map[string]any(flow.PanelConfig),
			Graph:              map[string]any(flow.Graph),
			Enabled:            flow.IsEnabled,
		},
		KeywordRules: make([]portableKeywordRule, 0, len(associated)),
	}

	for _, rule := range associated {
		envelope.KeywordRules = append(envelope.KeywordRules, portableKeywordRule{
			Name:            rule.Name,
			Keywords:        []string(rule.Keywords),
			MatchType:       rule.MatchType,
			CaseSensitive:   rule.CaseSensitive,
			ResponseType:    rule.ResponseType,
			ResponseContent: map[string]any(rule.ResponseContent),
			Conditions:      rule.Conditions,
			Priority:        rule.Priority,
			Enabled:         rule.IsEnabled,
		})
	}

	return r.SendEnvelope(envelope)
}

// ChatbotImport deserializes a portable export envelope into a NEW flow (fresh
// UUID) plus its keyword rules, scoped to the caller's organization and the
// selected WhatsApp account. Nothing existing is overwritten.
//
// POST /api/chatbot/import
// body: {"whatsapp_account": "<name>", "version": 1, "flow": {...}, "keyword_rules": [...]}
func (a *App) ChatbotImport(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	if !a.HasPermission(userID, models.ResourceFlowsChatbot, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Permission denied", nil, "")
	}

	var req struct {
		WhatsAppAccount string                `json:"whatsapp_account"`
		Version         int                   `json:"version"`
		Flow            portableFlow          `json:"flow"`
		KeywordRules    []portableKeywordRule `json:"keyword_rules"`
	}
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid import file", nil, "")
	}

	if req.Version != chatbotExportVersion {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			fmt.Sprintf("Unsupported export version %d (expected %d)", req.Version, chatbotExportVersion), nil, "")
	}
	if strings.TrimSpace(req.Flow.Name) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Import file is missing a flow name", nil, "")
	}

	account := strings.TrimSpace(req.WhatsAppAccount)
	if account == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A target WhatsApp account is required", nil, "")
	}
	// The target account must belong to the caller's organization.
	var accountCount int64
	if err := a.DB.Model(&models.WhatsAppAccount{}).
		Where("organization_id = ? AND name = ?", orgID, account).
		Count(&accountCount).Error; err != nil || accountCount == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Selected WhatsApp account was not found", nil, "")
	}

	// Resolve a non-colliding flow name within the organization.
	flowName := a.uniqueFlowName(orgID, strings.TrimSpace(req.Flow.Name))

	flow := models.ChatbotFlow{
		BaseModel:          models.BaseModel{ID: uuid.New()},
		OrganizationID:     orgID,
		WhatsAppAccount:    account,
		Name:               flowName,
		Description:        req.Flow.Description,
		TriggerKeywords:    models.StringArray(req.Flow.TriggerKeywords),
		TriggerButtonID:    req.Flow.TriggerButtonID,
		InitialMessage:     req.Flow.InitialMessage,
		InitialMessageType: req.Flow.InitialMessageType,
		CompletionMessage:  req.Flow.CompletionMessage,
		OnCompleteAction:   req.Flow.OnCompleteAction,
		CompletionConfig:   models.JSONB(req.Flow.CompletionConfig),
		TimeoutMessage:     req.Flow.TimeoutMessage,
		CancelKeywords:     models.StringArray(req.Flow.CancelKeywords),
		PanelConfig:        models.JSONB(req.Flow.PanelConfig),
		Graph:              models.JSONB(req.Flow.Graph),
		IsEnabled:          req.Flow.Enabled,
		CreatedByID:        &userID,
		UpdatedByID:        &userID,
	}

	rules := make([]models.KeywordRule, 0, len(req.KeywordRules))
	for _, pr := range req.KeywordRules {
		if len(pr.Keywords) == 0 {
			continue // skip malformed rules rather than fail the whole import
		}
		matchType := pr.MatchType
		if matchType == "" {
			matchType = models.MatchTypeContains
		}
		responseType := pr.ResponseType
		if responseType == "" {
			responseType = models.ResponseTypeText
		}
		name := strings.TrimSpace(pr.Name)
		if name == "" {
			name = pr.Keywords[0]
		}
		rules = append(rules, models.KeywordRule{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  orgID,
			WhatsAppAccount: account,
			Name:            name,
			Keywords:        models.StringArray(pr.Keywords),
			MatchType:       matchType,
			CaseSensitive:   pr.CaseSensitive,
			ResponseType:    responseType,
			ResponseContent: models.JSONB(pr.ResponseContent),
			Conditions:      pr.Conditions,
			Priority:        pr.Priority,
			IsEnabled:       pr.Enabled,
			CreatedByID:     &userID,
			UpdatedByID:     &userID,
		})
	}

	// Persist flow + rules atomically.
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&flow).Error; err != nil {
			return err
		}
		if len(rules) > 0 {
			if err := tx.Create(&rules).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		a.Log.Error("Failed to import chatbot flow", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to import flow", nil, "")
	}

	a.InvalidateChatbotFlowsCache(orgID)
	if len(rules) > 0 {
		a.InvalidateKeywordRulesCache(orgID)
	}

	a.logAudit(orgID, userID, "chatbot_flow", flow.ID, models.AuditActionCreated, nil, &flow)

	return r.SendEnvelope(map[string]any{
		"imported_flow_id":   flow.ID.String(),
		"imported_flow_name": flow.Name,
		"imported_keywords":  len(rules),
	})
}

// uniqueFlowName returns base, or "base (imported)" / "base (imported N)" when
// a flow with that name already exists in the organization, so imports never
// collide with or overwrite existing flows.
func (a *App) uniqueFlowName(orgID uuid.UUID, base string) string {
	exists := func(name string) bool {
		var count int64
		a.DB.Model(&models.ChatbotFlow{}).
			Where("organization_id = ? AND name = ?", orgID, name).
			Count(&count)
		return count > 0
	}

	if !exists(base) {
		return base
	}
	candidate := base + " (imported)"
	if !exists(candidate) {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = fmt.Sprintf("%s (imported %d)", base, i)
		if !exists(candidate) {
			return candidate
		}
	}
}
