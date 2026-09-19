package handlers

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ResolveOccurrenceProcess returns the active OccurrenceProcess configured for
// a WhatHappened reason, or a null process when none is configured — that is
// the generic fallback path, not an error. Relies on the
// one-active-process-per-reason invariant enforced in
// CreateOccurrenceProcess/UpdateOccurrenceProcess.
func (a *App) ResolveOccurrenceProcess(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	raw := r.RequestCtx.QueryArgs().Peek("what_happened_id")
	if len(raw) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "what_happened_id is required", nil, "")
	}
	id, err := uuid.Parse(string(raw))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid what_happened_id", nil, "")
	}

	var process models.OccurrenceProcess
	err = a.DB.Where("organization_id = ? AND what_happened_id = ? AND is_active = true", orgID, id).
		Preload("Category").Preload("WhatHappened").Preload("Department").
		First(&process).Error
	if err != nil {
		return r.SendEnvelope(map[string]any{"process": nil})
	}
	return r.SendEnvelope(map[string]any{"process": process})
}

// ListOccurrenceProcessMessages returns every stage's template for a process.
func (a *App) ListOccurrenceProcessMessages(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionRead)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	if _, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, processID, orgID, "Process"); err != nil {
		return nil
	}

	var messages []models.OccurrenceProcessMessage
	if err := a.DB.Where("organization_id = ? AND process_id = ?", orgID, processID).Find(&messages).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load messages", nil, "")
	}
	return r.SendEnvelope(map[string]any{"messages": messages})
}

// UpsertOccurrenceProcessMessageRequest is the body for PUT .../messages/{stage}.
type UpsertOccurrenceProcessMessageRequest struct {
	Content  string `json:"content"`
	IsActive *bool  `json:"is_active"`
}

var validProcessMessageStages = map[string]models.OccurrenceProcessMessageStage{
	"registration": models.OccurrenceProcessMessageRegistration,
	"documents":    models.OccurrenceProcessMessageDocuments,
	"follow_up":    models.OccurrenceProcessMessageFollowUp,
	"forwarding":   models.OccurrenceProcessMessageForwarding,
	"closing":      models.OccurrenceProcessMessageClosing,
}

// UpsertOccurrenceProcessMessage creates or replaces one stage's template —
// same upsert-by-key shape as UpsertOccurrenceSLAPolicy. IsActive has no DB
// default, so it is always set explicitly (a map on update so false is written).
func (a *App) UpsertOccurrenceProcessMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionWrite)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	if _, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, processID, orgID, "Process"); err != nil {
		return nil
	}

	stageRaw, _ := r.RequestCtx.UserValue("stage").(string)
	stage, ok := validProcessMessageStages[stageRaw]
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}

	var req UpsertOccurrenceProcessMessageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Content == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "content is required", nil, "")
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	userName := audit.GetUserName(a.DB, userID)

	var existing models.OccurrenceProcessMessage
	err = a.DB.Where("organization_id = ? AND process_id = ? AND stage = ?", orgID, processID, stage).First(&existing).Error
	if err != nil {
		message := models.OccurrenceProcessMessage{
			OrganizationID: orgID, ProcessID: processID, Stage: stage, Content: req.Content, IsActive: isActive,
		}
		if err := a.DB.Create(&message).Error; err != nil {
			a.Log.Error("Failed to create process message", "error", err, "process", processID)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create message", nil, "")
		}
		audit.LogAudit(a.DB, orgID, userID, userName,
			models.ResourceOccurrenceProcesses, processID, models.AuditActionCreated, nil, message)
		return r.SendEnvelope(message)
	}

	before := existing
	if err := a.DB.Model(&existing).Updates(map[string]any{"content": req.Content, "is_active": isActive}).Error; err != nil {
		a.Log.Error("Failed to update process message", "error", err, "process", processID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update message", nil, "")
	}
	var updated models.OccurrenceProcessMessage
	if err := a.DB.First(&updated, "id = ?", existing.ID).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update message", nil, "")
	}
	audit.LogAudit(a.DB, orgID, userID, userName,
		models.ResourceOccurrenceProcesses, processID, models.AuditActionUpdated, before, updated)
	return r.SendEnvelope(updated)
}

// resolveProcessMessageVariables substitutes the bracket variables the
// validated process map uses in its message text. Nothing else in the codebase
// resolves `[Bracket]` placeholders (internal/templateutil handles WABA's
// `{{1}}` syntax for approved templates, a different feature).
func resolveProcessMessageVariables(content string, occ *models.Occurrence, agentName string) string {
	name := ""
	if occ.Contact != nil {
		name = occ.Contact.ProfileName
	}
	unit := ""
	if occ.Unit != nil {
		unit = occ.Unit.Name
	}
	deadline := ""
	if occ.SLA.ResponseDeadline != nil {
		remaining := time.Until(*occ.SLA.ResponseDeadline)
		if days := int(remaining.Hours() / 24); days > 0 {
			deadline = strconv.Itoa(days) + " dias"
		} else {
			deadline = strconv.Itoa(max(int(remaining.Hours()), 1)) + " horas"
		}
	}

	return strings.NewReplacer(
		"[Nome]", name,
		"[Atendente]", agentName,
		"[Protocolo]", occ.ProtocolNumber,
		"[NF]", occ.InvoiceNumber,
		"[Prazo]", deadline,
		"[Loja]", unit,
	).Replace(content)
}

// fallbackProcessMessage is the explicit, stage-aware fallback. "registration"
// returns the exact text SendOccurrenceProtocol sends today, so a process-less
// occurrence's suggested message is byte-identical to current behaviour. Every
// other stage returns ("", false): no customer-facing message is invented for a
// stage nobody wrote a template for — the caller shows "no message registered"
// and lets the agent write one manually.
func fallbackProcessMessage(stage string, occ *models.Occurrence) (content string, hasFallback bool) {
	if stage == "registration" {
		return "Seu protocolo de atendimento é " + occ.ProtocolNumber + ". Guarde este número para consultas futuras.", true
	}
	return "", false
}

// PreviewOccurrenceProcessMessage renders one stage's message for one
// occurrence, substituting variables, and falls back explicitly when the
// process has no template for that stage.
//
// Deliberately does NOT call loadAuthorizedOccurrence: that helper reads the
// occurrence id from the {id} path param, but on this route {id} is the
// PROCESS id — the occurrence id is the occurrence_id query param. This loads
// the occurrence explicitly (org-scoped), re-applies the canViewConversation
// check, and requires the occurrence to be linked to the previewed process.
func (a *App) PreviewOccurrenceProcessMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	stageRaw, _ := r.RequestCtx.UserValue("stage").(string)
	if _, ok := validProcessMessageStages[stageRaw]; !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}

	occurrenceID, err := uuid.Parse(string(r.RequestCtx.QueryArgs().Peek("occurrence_id")))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid occurrence_id", nil, "")
	}

	var occ models.Occurrence
	if err := a.DB.Where("id = ? AND organization_id = ?", occurrenceID, orgID).
		Preload("Contact").Preload("Unit").First(&occ).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Occurrence not found", nil, "")
	}
	if !a.canViewConversation(userID, orgID, occ.Contact) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have access to this occurrence", nil, "")
	}
	if occ.ProcessID == nil || *occ.ProcessID != processID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "This occurrence is not linked to the given process", nil, "")
	}

	var template models.OccurrenceProcessMessage
	found := a.DB.Where("organization_id = ? AND process_id = ? AND stage = ? AND is_active = true",
		orgID, processID, stageRaw).First(&template).Error == nil

	var content string
	hasTemplate := found
	if found {
		content = resolveProcessMessageVariables(template.Content, &occ, audit.GetUserName(a.DB, userID))
	} else {
		content, hasTemplate = fallbackProcessMessage(stageRaw, &occ)
	}

	return r.SendEnvelope(map[string]any{"content": content, "has_template": hasTemplate})
}

// LogOccurrenceProcessMessageUseRequest is the body for logging that a
// suggested message was used.
type LogOccurrenceProcessMessageUseRequest struct {
	Stage       string `json:"stage"`
	ProcessName string `json:"process_name"`
}

// LogOccurrenceProcessMessageUse records, on the occurrence's existing
// timeline, that a suggested message was used. Route {id} is the occurrence id,
// so loadAuthorizedOccurrence is correct here.
func (a *App) LogOccurrenceProcessMessageUse(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, err := a.loadAuthorizedOccurrence(r, orgID, userID, false)
	if err != nil {
		return nil
	}

	var req LogOccurrenceProcessMessageUseRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if _, ok := validProcessMessageStages[req.Stage]; !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}

	content := "Mensagem de " + req.Stage + " utilizada."
	if req.ProcessName != "" {
		content = "Mensagem de " + req.Stage + " utilizada — processo: " + req.ProcessName + "."
	}

	if err := a.DB.Create(&models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventProcessMessageUsed,
		Content:        content,
		CreatedByID:    &userID,
	}).Error; err != nil {
		a.Log.Error("Failed to log process message use", "error", err, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to log message use", nil, "")
	}

	return r.SendEnvelope(map[string]any{"logged": true})
}
