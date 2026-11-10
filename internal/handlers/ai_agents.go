package handlers

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Size limits for an agent's free-text fields (the prompt budget is not unlimited).
const (
	aiAgentNameMax     = 255
	aiAgentTextMax     = 4000
	aiAgentAccountsMax = 50
)

// aiAgentRequest is the body of create and update. Pointers: an update only touches what it sends.
type aiAgentRequest struct {
	Name             *string   `json:"name"`
	Objective        *string   `json:"objective"`
	Instructions     *string   `json:"instructions"`
	CompanyInfo      *string   `json:"company_info"`
	Tone             *string   `json:"tone"`
	HandoffRules     *string   `json:"handoff_rules"`
	IsEnabled        *bool     `json:"is_enabled"`
	WhatsAppAccounts *[]string `json:"whatsapp_accounts"`
}

// validate checks lengths and, for the number list, that every name is a WhatsApp account of
// the organization (an agent must never be pointed at another organization's number).
func (a *App) validateAIAgentRequest(orgID uuid.UUID, req *aiAgentRequest) string {
	if req.Name != nil && (strings.TrimSpace(*req.Name) == "" || len(*req.Name) > aiAgentNameMax) {
		return "Name is required (max 255 characters)"
	}
	for _, f := range []*string{req.Objective, req.Instructions, req.CompanyInfo, req.Tone, req.HandoffRules} {
		if f != nil && len(*f) > aiAgentTextMax {
			return "Text fields accept at most 4000 characters"
		}
	}
	if req.WhatsAppAccounts != nil {
		if len(*req.WhatsAppAccounts) > aiAgentAccountsMax {
			return "Too many WhatsApp accounts"
		}
		for _, name := range *req.WhatsAppAccounts {
			var n int64
			a.DB.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND name = ?", orgID, name).Count(&n)
			if n == 0 {
				return "Unknown WhatsApp account: " + name
			}
		}
	}
	return ""
}

func applyAIAgentRequest(agent *models.AIAgent, req *aiAgentRequest) {
	if req.Name != nil {
		agent.Name = strings.TrimSpace(*req.Name)
	}
	if req.Objective != nil {
		agent.Objective = *req.Objective
	}
	if req.Instructions != nil {
		agent.Instructions = *req.Instructions
	}
	if req.CompanyInfo != nil {
		agent.CompanyInfo = *req.CompanyInfo
	}
	if req.Tone != nil {
		agent.Tone = *req.Tone
	}
	if req.HandoffRules != nil {
		agent.HandoffRules = *req.HandoffRules
	}
	if req.IsEnabled != nil {
		agent.IsEnabled = *req.IsEnabled
	}
	if req.WhatsAppAccounts != nil {
		agent.WhatsAppAccounts = models.StringArray(*req.WhatsAppAccounts)
	}
}

// ListAIAgents lists the organization's AI agents.
func (a *App) ListAIAgents(r *fastglue.Request) error {
	if !a.can(r, models.ResourceChatbotAI, models.ActionRead) {
		return nil
	}
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	pg := parsePagination(r)
	query := a.DB.Model(&models.AIAgent{}).Where("organization_id = ?", orgID)
	var total int64
	query.Count(&total)
	var agents []models.AIAgent
	if err := pg.Apply(query.Order("created_at DESC")).Find(&agents).Error; err != nil {
		a.Log.Error("Failed to fetch AI agents", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to fetch AI agents", nil, "")
	}
	return r.SendEnvelope(listEnvelope("agents", agents, total, pg))
}

// CreateAIAgent creates an AI agent. It starts disabled unless the body says otherwise.
func (a *App) CreateAIAgent(r *fastglue.Request) error {
	if !a.can(r, models.ResourceChatbotAI, models.ActionWrite) {
		return nil
	}
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	var req aiAgentRequest
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Name is required", nil, "")
	}
	if msg := a.validateAIAgentRequest(orgID, &req); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	agent := models.AIAgent{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		CreatedByID:    &userID,
		UpdatedByID:    &userID,
	}
	applyAIAgentRequest(&agent, &req)
	if err := a.DB.Create(&agent).Error; err != nil {
		a.Log.Error("Failed to create AI agent", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create AI agent", nil, "")
	}
	a.logAudit(orgID, userID, "ai_agent", agent.ID, models.AuditActionCreated, nil, &agent)
	return r.SendEnvelope(map[string]any{"id": agent.ID.String(), "message": "AI agent created successfully"})
}

// GetAIAgent returns one agent of the organization.
func (a *App) GetAIAgent(r *fastglue.Request) error {
	if !a.can(r, models.ResourceChatbotAI, models.ActionRead) {
		return nil
	}
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := parsePathUUID(r, "id", "agent")
	if err != nil {
		return nil
	}
	agent, err := findByIDAndOrg[models.AIAgent](a.DB, r, id, orgID, "AI agent")
	if err != nil {
		return nil
	}
	return r.SendEnvelope(agent)
}

// UpdateAIAgent updates an agent; only the fields present in the body change.
func (a *App) UpdateAIAgent(r *fastglue.Request) error {
	if !a.can(r, models.ResourceChatbotAI, models.ActionWrite) {
		return nil
	}
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := parsePathUUID(r, "id", "agent")
	if err != nil {
		return nil
	}
	agent, err := findByIDAndOrg[models.AIAgent](a.DB, r, id, orgID, "AI agent")
	if err != nil {
		return nil
	}
	old := *agent
	var req aiAgentRequest
	if err := json.Unmarshal(r.RequestCtx.PostBody(), &req); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}
	if msg := a.validateAIAgentRequest(orgID, &req); msg != "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, msg, nil, "")
	}
	applyAIAgentRequest(agent, &req)
	agent.UpdatedByID = &userID
	if err := a.DB.Save(agent).Error; err != nil {
		a.Log.Error("Failed to update AI agent", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update AI agent", nil, "")
	}
	a.logAudit(orgID, userID, "ai_agent", agent.ID, models.AuditActionUpdated, &old, agent)
	return r.SendEnvelope(map[string]any{"message": "AI agent updated successfully"})
}

// DeleteAIAgent deletes an agent. If it was the organization's default, the default is cleared
// (the chatbot then behaves as before: no agent).
func (a *App) DeleteAIAgent(r *fastglue.Request) error {
	if !a.can(r, models.ResourceChatbotAI, models.ActionDelete) {
		return nil
	}
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := parsePathUUID(r, "id", "agent")
	if err != nil {
		return nil
	}
	agent, err := findByIDAndOrg[models.AIAgent](a.DB, r, id, orgID, "AI agent")
	if err != nil {
		return nil
	}
	if err := a.DB.Delete(agent).Error; err != nil {
		a.Log.Error("Failed to delete AI agent", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete AI agent", nil, "")
	}
	// resolveAIAgent already ignores a deleted agent; clearing the pointer keeps the settings honest.
	a.DB.Model(&models.ChatbotSettings{}).
		Where("organization_id = ? AND ai_default_agent_id = ?", orgID, id).
		Update("ai_default_agent_id", nil)
	a.InvalidateChatbotSettingsCache(orgID)
	a.logAudit(orgID, userID, "ai_agent", agent.ID, models.AuditActionDeleted, agent, nil)
	return r.SendEnvelope(map[string]any{"message": "AI agent deleted successfully"})
}

// AIAgentAccountIssue says why an allowed number cannot host the agent yet.
type AIAgentAccountIssue struct {
	Account string `json:"account"`
	Problem string `json:"problem"`
}

// Problems reported by aiAgentAccountIssues.
const (
	aiIssueNoTeam   = "no_default_team"
	aiIssueTeamGone = "team_inactive_or_foreign"
	aiIssueNoUnit   = "team_without_unit"
)

// aiAgentAccountUnitTeamIssue returns "" when the account's default team is active, belongs to
// the account's organization and is tied to a unit (§5.2: the agent only works where the unit
// responsible for the opportunity is known); otherwise one of the aiIssue* codes.
func (a *App) aiAgentAccountUnitTeamIssue(account *models.WhatsAppAccount) string {
	if account.DefaultTeamID == nil {
		return aiIssueNoTeam
	}
	var team models.Team
	if err := a.DB.Where("id = ? AND organization_id = ? AND is_active = ?",
		*account.DefaultTeamID, account.OrganizationID, true).First(&team).Error; err != nil {
		return aiIssueTeamGone
	}
	if team.UnitID == nil {
		return aiIssueNoUnit
	}
	return ""
}

// aiAgentAccountIssues lists, for each number the agent is allowed on, the reason it cannot work
// there yet. A number that does not exist any more is reported too.
func (a *App) aiAgentAccountIssues(agent *models.AIAgent) []AIAgentAccountIssue {
	issues := []AIAgentAccountIssue{}
	for _, name := range agent.WhatsAppAccounts {
		var account models.WhatsAppAccount
		if err := a.DB.Where("organization_id = ? AND name = ?", agent.OrganizationID, name).First(&account).Error; err != nil {
			issues = append(issues, AIAgentAccountIssue{Account: name, Problem: "account_not_found"})
			continue
		}
		if p := a.aiAgentAccountUnitTeamIssue(&account); p != "" {
			issues = append(issues, AIAgentAccountIssue{Account: name, Problem: p})
		}
	}
	return issues
}

// GetAIAgentReadiness tells the UI which allowed numbers cannot host the agent yet.
func (a *App) GetAIAgentReadiness(r *fastglue.Request) error {
	if !a.can(r, models.ResourceChatbotAI, models.ActionRead) {
		return nil
	}
	orgID, err := a.getOrgID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	id, err := parsePathUUID(r, "id", "agent")
	if err != nil {
		return nil
	}
	agent, err := findByIDAndOrg[models.AIAgent](a.DB, r, id, orgID, "AI agent")
	if err != nil {
		return nil
	}
	return r.SendEnvelope(map[string]any{"issues": a.aiAgentAccountIssues(agent)})
}

// resolveAIAgent returns the agent that may answer on account, or nil. It is FAIL-CLOSED: any
// doubt means "no agent", and the chatbot then behaves exactly as it did before agents existed.
// All of these must hold: the organization has a default agent; it exists, is enabled and
// belongs to the organization; the number is in the agent's own WhatsAppAccounts (the only
// thing that limits a pilot to one number, since ChatbotSettings is one row per organization);
// and the number's default team is active and tied to a unit (§5.2).
func (a *App) resolveAIAgent(settings *models.ChatbotSettings, account *models.WhatsAppAccount) *models.AIAgent {
	if settings == nil || account == nil || settings.AI.DefaultAgentID == nil {
		return nil
	}
	if settings.OrganizationID != account.OrganizationID {
		return nil
	}
	var agent models.AIAgent
	if err := a.DB.Where("id = ? AND organization_id = ? AND is_enabled = ?",
		*settings.AI.DefaultAgentID, account.OrganizationID, true).First(&agent).Error; err != nil {
		return nil
	}
	allowed := false
	for _, name := range agent.WhatsAppAccounts {
		if name == account.Name {
			allowed = true
			break
		}
	}
	if !allowed || a.aiAgentAccountUnitTeamIssue(account) != "" {
		return nil
	}
	return &agent
}
