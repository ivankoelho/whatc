package handlers

import (
	"context"

	"github.com/shridarpatil/whatomate/internal/aitools"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm/clause"
)

// aiToolCatalog is the catalog in use: the injected one in tests, otherwise the production one,
// which is empty in Fase 9B.
func (a *App) aiToolCatalog() *aitools.Catalog {
	if a.AIToolCatalog != nil {
		return a.AIToolCatalog
	}
	return aitools.DefaultCatalog()
}

// aiToolsGloballyEnabled is the server-wide ai_tools.enabled switch (off unless set to true).
func (a *App) aiToolsGloballyEnabled() bool {
	return a.Config != nil && a.Config.AITools.Enabled
}

// AIToolView is one catalog tool as an administrator sees it, with the organization's own state.
type AIToolView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
	// Enabled is the organization's opt-in. Available says whether the AI could actually use the
	// tool right now (global switch on, enabled and a class of risk the policy allows).
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
}

// ListAITools GET /api/ai-tools: the catalog with the state of the caller's organization only.
func (a *App) ListAITools(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAITools, models.ActionRead)
	if err != nil {
		return nil
	}
	// context.Background(), not r.RequestCtx: fasthttp's RequestCtx only satisfies context.Context
	// nominally (Done() closes on server shutdown, never on a client disconnect, there is no
	// deadline, and Done() dereferences a server that does not exist outside a running one). It
	// would add no cancellation here, and it is not what the AI handlers use (see ai_models.go).
	enabled, err := aitools.SettingsStore{DB: a.DB}.EnabledTools(context.Background(), orgID)
	if err != nil {
		a.Log.Error("Failed to read the AI tool settings", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load AI tools", nil, "")
	}
	global := a.aiToolsGloballyEnabled()
	cat := a.aiToolCatalog()
	tools := make([]AIToolView, 0, len(cat.Names()))
	for _, name := range cat.Names() {
		spec, _ := cat.Get(name)
		v := aitools.Authorize(aitools.PolicyInput{GlobalEnabled: global, Known: true, OrgEnabled: enabled[name], Risk: spec.Risk})
		tools = append(tools, AIToolView{
			Name: name, Description: spec.Description, Risk: string(spec.Risk), Enabled: enabled[name], Available: v.Allowed,
		})
	}
	return r.SendEnvelope(map[string]any{"global_enabled": global, "tools": tools})
}

// SetAIToolEnabledRequest is the body of PUT /api/ai-tools/{name}.
type SetAIToolEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

// SetAIToolEnabled PUT /api/ai-tools/{name} {"enabled": bool}: opt the caller's organization in or
// out of one catalog tool. Only a tool that exists in the catalog can be switched, and the change
// is audited against the human who made it.
func (a *App) SetAIToolEnabled(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceAITools, models.ActionWrite)
	if err != nil {
		return nil
	}
	name, _ := r.RequestCtx.UserValue("name").(string)
	if _, ok := a.aiToolCatalog().Get(name); !ok {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "AI tool not found", nil, "")
	}
	var req SetAIToolEnabledRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Enabled == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "enabled is required", nil, "")
	}

	var before models.AIToolSetting
	existed := a.DB.Where("organization_id = ? AND tool_name = ?", orgID, name).First(&before).Error == nil

	row := models.AIToolSetting{OrganizationID: orgID, ToolName: name, Enabled: *req.Enabled, UpdatedByID: &userID}
	if err := a.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "organization_id"}, {Name: "tool_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "updated_by_id", "updated_at"}),
	}).Create(&row).Error; err != nil {
		a.Log.Error("Failed to save the AI tool setting", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save AI tool", nil, "")
	}
	if err := a.DB.Where("organization_id = ? AND tool_name = ?", orgID, name).First(&row).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to save AI tool", nil, "")
	}

	view := func(s models.AIToolSetting) map[string]any {
		return map[string]any{"tool_name": s.ToolName, "enabled": s.Enabled}
	}
	after := view(row)
	if existed {
		a.logAudit(orgID, userID, "ai_tool", row.ID, models.AuditActionUpdated, view(before), after)
	} else {
		a.logAudit(orgID, userID, "ai_tool", row.ID, models.AuditActionCreated, nil, after)
	}
	return r.SendEnvelope(map[string]any{"name": name, "enabled": row.Enabled})
}
