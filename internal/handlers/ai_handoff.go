package handlers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Why an AI agent hands a conversation to people (the reason passed to handoffFromAI). The first
// two come from aiReply.Handoff; the other two are decided by the processor.
const (
	aiHandoffCap     = "cap"        // the organization's monthly token cap was reached
	aiHandoffFailure = "ai_failure" // the AI provider failed twice in a row on this conversation
)

// What the customer is told when the agent hands over. Fixed texts, sent only when the attendance
// really was created. The long one is only for a handoff the agent chose (the customer is
// qualified and the seller gets what was learned); after a cap or a failure nothing was
// qualified, so the short one promises nothing it did not do.
const (
	aiHandoffWaitMessage  = "Vou chamar um atendente para continuar com você. Já passei a ele o que conversamos."
	aiHandoffShortMessage = "Vou chamar um atendente para continuar com você."
)

func handoffMessageFor(reason string) string {
	if reason == aiHandoffMarker {
		return aiHandoffWaitMessage
	}
	return aiHandoffShortMessage
}

// Keys of ChatbotSession.SessionData used by the agent.
const (
	aiSessionHandedOff = "ai_handed_off" // the out-of-hours message was already given on this conversation
	aiSessionQualified = "ai_qualified"  // the qualification already ran on this conversation
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

// setSessionValue sets one SessionData key in memory and saves just that key in the database, so
// it also works when nobody saves the session afterwards (the direct-reply path) and never
// overwrites keys another goroutine wrote meanwhile.
func (a *App) setSessionValue(s *models.ChatbotSession, key string, val any) {
	if s.SessionData == nil {
		s.SessionData = models.JSONB{}
	}
	s.SessionData[key] = val
	raw, err := json.Marshal(val)
	if err == nil {
		err = a.DB.Exec(
			`UPDATE chatbot_sessions SET session_data = jsonb_set(COALESCE(session_data, '{}'::jsonb), ?, ?::jsonb, true) WHERE id = ?`,
			"{"+key+"}", string(raw), s.ID).Error
	}
	if err != nil {
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

// assignAIOpportunity gives the opportunity that THIS attendance opened to the agent who took it
// over, whatever the "assign to the same agent" setting says. Only that opportunity, and only
// while it still has no owner: the seller sees in the sales funnel what is assigned to them, and
// an opportunity somebody else already owns is never moved.
func (a *App) assignAIOpportunity(db *gorm.DB, t *models.AgentTransfer, agentID uuid.UUID) {
	if t.Source != models.TransferSourceAIAgent {
		return
	}
	if err := db.Model(&models.SalesOpportunity{}).
		Where("organization_id = ? AND source_transfer_id = ? AND status = ? AND assigned_user_id IS NULL",
			t.OrganizationID, t.ID, models.SalesOpportunityStatusAberta).
		Update("assigned_user_id", agentID).Error; err != nil {
		a.Log.Error("failed to assign the AI opportunity to the agent", "transfer_id", t.ID, "agent_id", agentID, "error", err)
	}
}

// linkOpportunityToTransfer ties the opportunity THIS handoff created to the attendance that was
// just created (traceability) and, when the attendance already has an agent, gives it to them. An
// opportunity that already existed before the handoff is left alone.
func (a *App) linkOpportunityToTransfer(oppID uuid.UUID, transferID uuid.UUID) {
	a.DB.Model(&models.SalesOpportunity{}).Where("id = ? AND source_transfer_id IS NULL", oppID).
		Update("source_transfer_id", transferID)
	var t models.AgentTransfer
	if err := a.DB.First(&t, "id = ?", transferID).Error; err == nil && t.AgentID != nil {
		a.assignAIOpportunity(a.DB, &t, *t.AgentID)
	}
}

// openOpportunityID returns the id of the contact's open opportunity, if any.
func (a *App) openOpportunityID(contact *models.Contact) (uuid.UUID, bool) {
	var opp models.SalesOpportunity
	err := a.DB.Select("id").Where("organization_id = ? AND contact_id = ? AND status = ?",
		contact.OrganizationID, contact.ID, models.SalesOpportunityStatusAberta).First(&opp).Error
	return opp.ID, err == nil
}

// handoffFromAI is the one place where an AI agent hands a conversation to people. In order:
// qualify (one extra AI call, only when the agent decided the customer is ready), register the
// opportunity in the CRM, create the attendance in the team of the number's unit, and only then
// tell the customer. It returns true when the conversation is now (or already was) in human hands.
//
// Safeguards: a human who already owns the conversation ends it quietly; an attendance that is
// already active sends nothing (so two messages arriving together cannot produce two waiting
// messages or two transfers: the unique index decides); a failed extraction or a failed
// opportunity never stops the transfer, and a failed transfer to the unit's team falls back to the
// general queue, marked, so the customer is always attended.
func (a *App) handoffFromAI(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession, settings *models.ChatbotSettings, reason string) bool {
	ctx := context.Background()
	orgID := account.OrganizationID
	if a.humanTookOver(orgID, contact.ID) {
		return true
	}
	told := sessionFlag(session, aiSessionHandedOff) // the out-of-hours message was already given

	var q *Qualification
	if !sessionFlag(session, aiSessionQualified) && (reason == aiHandoffMarker || reason == aiHandoffUnsafe) {
		q = a.extractQualification(ctx, settings, account, contact, session)
		if !q.ExtractionFailed {
			a.setSessionValue(session, aiSessionQualified, true) // do not pay for it again on this conversation
		}
	}

	// An opportunity only when the customer showed purchase intent (proved by their own words), or
	// when the extraction failed after the agent itself said the customer was ready: then a bare
	// opportunity is better than none, and the human sees the conversation anyway.
	var createdOpp uuid.UUID
	if q != nil {
		wanted := q.Intencao != ""
		if q.ExtractionFailed {
			wanted = reason == aiHandoffMarker
		}
		if wanted {
			_, existed := a.openOpportunityID(contact)
			opp, err := a.createOrRetriggerSalesOpportunity(contact, nil, models.SalesOpportunityEventSourceSystem,
				formatInterest(q), nil, nil, applyQualification(q),
				// the contact's own account owner may belong to ANOTHER unit: an opportunity the
				// agent opens belongs to whoever takes the attendance, not to a stranger
				func(o *models.SalesOpportunity) { o.AssignedUserID = nil })
			switch {
			case err != nil:
				a.Log.Error("AI handoff: could not register the opportunity", "error", err, "contact_id", contact.ID)
			case !existed:
				createdOpp = opp.ID
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
		if err != nil {
			a.Log.Error("AI handoff: transfer to the unit's team failed, trying the general queue", "error", err, "contact_id", contact.ID)
		}
	} else {
		// The number lost its unit team after the agent was enabled.
		a.Log.Warn("AI handoff: the number has no valid unit team, using the general queue", "account", account.Name, "issue", issue)
		err = errNoUnitTeam
	}
	if err != nil {
		// The customer still has to be attended: general queue, marked so it can be found and fixed.
		res, err = a.transferToQueue(ctx, account, contact, models.TransferSourceAIAgent, notes, models.RoutingReasonAINoUnitTeam)
	}
	if err != nil {
		a.Log.Error("AI handoff: transfer failed", "error", err, "contact_id", contact.ID)
		return false
	}

	switch res.Outcome {
	case TransferCreated:
		if createdOpp != uuid.Nil {
			a.linkOpportunityToTransfer(createdOpp, res.TransferID)
		}
		a.clearAIFailures(session)
		if told { // the out-of-hours message is moot now; a later handoff may say it again
			a.setSessionValue(session, aiSessionHandedOff, false)
		}
		if err := a.sendAndSaveTextMessage(botCtx("ai_handoff"), account, contact, handoffMessageFor(reason)); err != nil {
			a.Log.Error("AI handoff: could not send the waiting message", "error", err, "contact_id", contact.ID)
		}
		return true
	case TransferAlreadyActive:
		return true
	case TransferOutsideHours:
		if !told {
			a.setSessionValue(session, aiSessionHandedOff, true)
			if msg := strings.TrimSpace(settings.BusinessHours.OutOfHoursMessage); msg != "" {
				_ = a.sendAndSaveTextMessage(botCtx("ai_handoff_out_of_hours"), account, contact, msg)
			}
		}
		return false
	}
	return false
}

// errNoUnitTeam marks "go straight to the general queue" inside handoffFromAI.
var errNoUnitTeam = &noUnitTeamError{}

type noUnitTeamError struct{}

func (*noUnitTeamError) Error() string { return "the number has no valid unit team" }
