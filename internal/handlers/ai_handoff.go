package handlers

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Why an AI agent hands a conversation to people (the reason passed to handoffFromAI). The first
// two come from aiReply.Handoff; the other two are decided by the processor.
const (
	aiHandoffCap     = "cap"        // the organization's monthly token cap was reached
	aiHandoffFailure = "ai_failure" // the AI provider failed twice in a row on this conversation
)

// What the customer is told when the agent hands over. Fixed text: it is the only thing the
// handoff says, it is sent only when the attendance really was created, and the human takes it
// from there.
const aiHandoffWaitMessage = "Vou chamar um atendente para continuar com você. Já passei a ele o que conversamos."

// Keys of ChatbotSession.SessionData used by the agent.
const (
	aiSessionHandedOff = "ai_handed_off" // the handoff already ran once outside business hours
	aiSessionFailures  = "ai_failures"   // consecutive AI failures on this conversation
)

// aiFailuresBeforeHandoff is how many AI failures in a row on one conversation hand it to people.
const aiFailuresBeforeHandoff = 2

func sessionInt(s *models.ChatbotSession, key string) int {
	switch v := s.SessionData[key].(type) {
	case int:
		return v
	case float64: // after a round trip through JSON
		return int(v)
	}
	return 0
}

func sessionFlag(s *models.ChatbotSession, key string) bool {
	b, _ := s.SessionData[key].(bool)
	return b
}

// setSessionValue sets one SessionData key in memory and saves just that column, so it also works
// when nobody saves the session afterwards (the direct-reply path).
func (a *App) setSessionValue(s *models.ChatbotSession, key string, val any) {
	if s.SessionData == nil {
		s.SessionData = models.JSONB{}
	}
	s.SessionData[key] = val
	if err := a.DB.Model(&models.ChatbotSession{}).Where("id = ?", s.ID).Update("session_data", s.SessionData).Error; err != nil {
		a.Log.Error("Failed to save AI state in the session", "error", err, "session", s.ID, "key", key)
	}
}

// noteAIFailure counts one more AI failure on the conversation and returns the new count.
func (a *App) noteAIFailure(s *models.ChatbotSession) int {
	n := sessionInt(s, aiSessionFailures) + 1
	a.setSessionValue(s, aiSessionFailures, n)
	return n
}

// clearAIFailures resets the failure count after a good answer (or after the handoff).
func (a *App) clearAIFailures(s *models.ChatbotSession) {
	if sessionInt(s, aiSessionFailures) != 0 {
		a.setSessionValue(s, aiSessionFailures, 0)
	}
}

func aiHandoffReasonLabel(reason string) string {
	switch reason {
	case aiHandoffMarker:
		return "qualificação concluída ou cliente pediu um atendente"
	case aiHandoffUnsafe:
		return "resposta do agente bloqueada pela proteção comercial (preço, estoque ou prazo)"
	case aiHandoffCap:
		return "limite mensal de uso de IA atingido"
	case aiHandoffFailure:
		return "falhas seguidas do provedor de IA"
	}
	return reason
}

// transferNotes is stored on the attendance, so the human who takes over sees why and what the
// agent learned.
func transferNotes(reason string, q *Qualification) string {
	n := "Transferido pelo agente de IA. Motivo: " + aiHandoffReasonLabel(reason) + "."
	if q != nil {
		n += "\n\n" + formatInterest(q)
	}
	return n
}

// linkOpportunityToTransfer ties the contact's open opportunity to the attendance that was just
// created (traceability) and, when the attendance already has an agent, makes that agent its
// owner so the seller sees it in the sales funnel.
func (a *App) linkOpportunityToTransfer(contact *models.Contact, transferID uuid.UUID) {
	var opp models.SalesOpportunity
	if err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
		contact.OrganizationID, contact.ID, models.SalesOpportunityStatusAberta).First(&opp).Error; err != nil {
		return // no opportunity: nothing to link
	}
	if opp.SourceTransferID == nil {
		a.DB.Model(&models.SalesOpportunity{}).Where("id = ? AND source_transfer_id IS NULL", opp.ID).
			Update("source_transfer_id", transferID)
	}
	var t models.AgentTransfer
	if err := a.DB.First(&t, "id = ?", transferID).Error; err == nil && t.AgentID != nil {
		a.assignOpenSalesOpportunityToAgent(a.DB, contact, *t.AgentID)
	}
}

// handoffFromAI is the one place where an AI agent hands a conversation to people. In order:
// qualify (one extra AI call, only when the agent decided the customer is ready), register the
// opportunity in the CRM, create the attendance in the team of the number's unit, and only then
// tell the customer. It returns true when the conversation is now (or already was) in human hands.
//
// Safeguards: a human who already owns the conversation ends it quietly; an attendance that is
// already active sends nothing (so two messages arriving together cannot produce two waiting
// messages or two transfers: the unique index decides); a failed extraction or a failed
// opportunity never stops the transfer.
func (a *App) handoffFromAI(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession, settings *models.ChatbotSettings, reason string) bool {
	ctx := context.Background()
	orgID := account.OrganizationID
	if a.humanTookOver(orgID, contact.ID) {
		return true
	}
	again := sessionFlag(session, aiSessionHandedOff) // this conversation already went through here out of hours

	var q *Qualification
	if !again && (reason == aiHandoffMarker || reason == aiHandoffUnsafe) {
		q = a.extractQualification(ctx, settings, account, contact, session)
	}

	// An opportunity only when the customer showed purchase intent (proved by their own words), or
	// when the extraction failed after the agent itself said the customer was ready: then a bare
	// opportunity is better than none, and the human sees the conversation anyway.
	if q != nil {
		wanted := q.Intencao != ""
		if q.ExtractionFailed {
			wanted = reason == aiHandoffMarker
		}
		if wanted {
			if _, err := a.createOrRetriggerSalesOpportunity(contact, nil, models.SalesOpportunityEventSourceSystem,
				formatInterest(q), nil, nil, applyQualification(q)); err != nil {
				a.Log.Error("AI handoff: could not register the opportunity", "error", err, "contact_id", contact.ID)
			}
		}
	}

	if a.humanTookOver(orgID, contact.ID) { // someone took over during the extraction
		return true
	}

	notes := transferNotes(reason, q)
	var res TransferResult
	var err error
	if issue := a.aiAgentAccountUnitTeamIssue(account); issue == "" {
		res, err = a.transferToTeam(ctx, account, contact, *account.DefaultTeamID, notes, models.TransferSourceAIAgent, models.RoutingReasonAIUnitTeam)
	} else {
		// The number lost its unit team after the agent was enabled. The customer still has to be
		// attended: general queue, marked so it can be found and fixed.
		a.Log.Warn("AI handoff: the number has no valid unit team, using the general queue",
			"account", account.Name, "issue", issue)
		res, err = a.transferToQueue(ctx, account, contact, models.TransferSourceAIAgent, notes, models.RoutingReasonAINoUnitTeam)
	}
	if err != nil {
		a.Log.Error("AI handoff: transfer failed", "error", err, "contact_id", contact.ID)
		return false
	}

	switch res.Outcome {
	case TransferCreated:
		a.linkOpportunityToTransfer(contact, res.TransferID)
		a.clearAIFailures(session)
		if err := a.sendAndSaveTextMessage(botCtx("ai_handoff"), account, contact, aiHandoffWaitMessage); err != nil {
			a.Log.Error("AI handoff: could not send the waiting message", "error", err, "contact_id", contact.ID)
		}
		return true
	case TransferAlreadyActive:
		return true
	case TransferOutsideHours:
		if !again {
			a.setSessionValue(session, aiSessionHandedOff, true)
			if msg := strings.TrimSpace(settings.BusinessHours.OutOfHoursMessage); msg != "" {
				_ = a.sendAndSaveTextMessage(botCtx("ai_handoff_out_of_hours"), account, contact, msg)
			}
		}
		return false
	}
	return false
}
