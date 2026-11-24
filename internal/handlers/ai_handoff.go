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
	// aiHandoffCapMissing: the agent has neither a monthly limit nor the explicit acceptance of none (fail closed).
	aiHandoffCapMissing = "cap_not_configured"
)

// What the customer is told when the agent hands over. Fixed texts, sent only when the attendance
// really was created. The long one is only for a handoff the agent chose (the customer is
// qualified and the seller gets what was learned); after a cap or a failure nothing was
// qualified, so the short one promises nothing it did not do.
const (
	aiHandoffWaitMessage  = "Vou chamar um atendente para continuar com você. Já passei a ele o que conversamos."
	aiHandoffShortMessage = "Vou chamar um atendente para continuar com você."
)

// aiHandoffErrorMessage is sent only when the attendance could not be created at all.
const aiHandoffErrorMessage = "Tive um problema para te encaminhar agora. Pode me mandar uma nova mensagem em instantes?"

// handoffMessageFor picks the waiting message. "I already passed him what we talked about" is only said when
// something was qualified (or had been, earlier in this conversation): when the extraction failed the
// attendant gets the conversation but no summary, and the customer is not told otherwise.
func handoffMessageFor(reason string, q *Qualification) string {
	if reason == aiHandoffMarker && (q == nil || !q.ExtractionFailed) {
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
	case aiHandoffCapMissing:
		return "limite mensal de uso de IA não configurado (e sem aceite explícito de uso sem limite)"
	case aiHandoffFailure:
		return "falhas seguidas do provedor de IA"
	case aiHandoffSchedulingFailed:
		return "não foi possível agendar a reunião; o cliente aguarda um atendente"
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

// fillBareOpportunity completes an open opportunity that an earlier handoff created with no data (its
// extraction failed). A retrigger never rewrites an opportunity, because a person may have edited it; this is
// the one exception, and it applies only while the text is still exactly the bare one the failed extraction
// wrote, so nothing a person typed is ever overwritten.
func (a *App) fillBareOpportunity(id uuid.UUID, q *Qualification) {
	var opp models.SalesOpportunity
	if err := a.DB.First(&opp, "id = ?", id).Error; err != nil || opp.Interest != formatInterest(nil) {
		return
	}
	opp.Interest = formatInterest(q)
	applyQualification(q)(&opp)
	if err := a.DB.Model(&opp).Select("interest", "estimated_quantity", "unit_of_measure").Updates(&opp).Error; err != nil {
		a.Log.Error("AI handoff: could not complete the opportunity with the new qualification", "error", err, "opportunity_id", id)
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
// opportunity never stops the transfer, and a failed transfer to the unit's team falls back to the
// general queue, marked, so the customer is always attended.
func (a *App) handoffFromAI(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession, settings *models.ChatbotSettings, reason string) bool {
	switch a.handoffFromAIOutcome(account, contact, session, settings, reason) {
	case aiHandoffTransferred, aiHandoffAlreadyActive, aiHandoffHumanActive:
		return true
	}
	return false
}

// How a handoff ended (what an AIHandoffIntent records as its Outcome).
const (
	aiHandoffTransferred    = "transferred"     // the attendance was created now
	aiHandoffAlreadyActive  = "already_active"  // an attendance already existed: nothing sent, nothing duplicated
	aiHandoffHumanActive    = "human_active"    // a person owned the conversation: ended quietly
	aiHandoffOutsideHours   = "outside_hours"   // out of hours: the customer got the out-of-hours message
	aiHandoffFailedNotified = "failed_notified" // no queue took it; the customer was told to write again
)

// handoffFromAIOutcome is handoffFromAI reporting how it ended.
func (a *App) handoffFromAIOutcome(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession, settings *models.ChatbotSettings, reason string) string {
	ctx := context.Background()
	orgID := account.OrganizationID
	if a.humanTookOver(orgID, contact.ID) {
		return aiHandoffHumanActive
	}
	// what the agent says during the handoff is signed like everything else it says
	sender := newAISender(a.agentForSession(settings, session))
	told := sessionFlag(session, aiSessionHandedOff) // the out-of-hours message was already given

	var q *Qualification
	// A customer with a booked meeting was qualified before the booking: the new session (a session ends with an
	// attendance) only holds what was said since, so a second extraction would find nothing and write noise.
	alreadyQualified := sessionFlag(session, aiSessionQualified) || a.upcomingAppointment(orgID, contact.ID) != nil
	if !alreadyQualified && (reason == aiHandoffMarker || reason == aiHandoffUnsafe) {
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
		if q.CustomMode { // no purchase intent here: an opportunity when the customer told us anything we asked
			wanted = len(q.Custom) > 0
		}
		if q.ExtractionFailed {
			wanted = reason == aiHandoffMarker
		}
		if wanted {
			existingID, existed := a.openOpportunityID(contact)
			opp, err := a.createOrRetriggerSalesOpportunity(contact, nil, models.SalesOpportunityEventSourceSystem,
				formatInterest(q), nil, nil, applyQualification(q),
				// the contact's own account owner may belong to ANOTHER unit: an opportunity the
				// agent opens belongs to whoever takes the attendance, not to a stranger
				func(o *models.SalesOpportunity) { o.AssignedUserID = nil; o.CreatedByAI = true })
			switch {
			case err != nil:
				a.Log.Error("AI handoff: could not register the opportunity", "error", err, "contact_id", contact.ID)
			case !existed:
				createdOpp = opp.ID
			case !q.ExtractionFailed:
				a.fillBareOpportunity(existingID, q)
			}
		}
	}

	aiHandoffFaultPoint("after_qualification") // test seam: a crash between the CRM work and the transfer
	if a.humanTookOver(orgID, contact.ID) {    // someone took over during the extraction
		return aiHandoffHumanActive
	}

	// With a calendar enabled, the agent offers meeting times instead of calling a person right away: the
	// customer picks one and the booking hands over (handleAppointmentTap). Any failure falls through to
	// the normal transfer below.
	if reason == aiHandoffMarker && a.agentForSession(settings, session) != nil {
		var oppID *uuid.UUID
		if createdOpp != uuid.Nil {
			oppID = &createdOpp
		} else if id, ok := a.openOpportunityID(contact); ok {
			oppID = &id
		}
		if a.offerAppointment(account, contact, session, oppID, 0) {
			return aiHandoffOffered
		}
	}

	notes := transferNotes(reason, q)
	if ap := a.upcomingAppointment(orgID, contact.ID); ap != nil {
		notes += "\n" + ap.note()
	}
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
		// Neither the unit's team nor the general queue could take the attendance. The customer must
		// not be left waiting for something that will not happen: tell them, and invite them to write
		// again (the agent is still active, so the next message retries the handoff; the
		// qualification is not paid for twice).
		a.Log.Error("AI handoff: transfer failed, including the general queue", "error", err, "contact_id", contact.ID)
		if !a.humanTookOver(orgID, contact.ID) {
			if serr := a.sendAndSaveAgentText(aiCtx("ai_handoff_failed"), account, contact, aiHandoffErrorMessage, sender); serr != nil {
				a.Log.Error("AI handoff: could not tell the customer about the failure", "error", serr, "contact_id", contact.ID)
			}
		}
		return aiHandoffFailedNotified
	}
	aiHandoffFaultPoint("after_transfer") // test seam: a crash after the attendance exists, before the waiting message

	switch res.Outcome {
	case TransferCreated:
		if createdOpp != uuid.Nil {
			a.linkOpportunityToTransfer(createdOpp, res.TransferID)
		}
		a.clearAIFailures(session)
		if told { // the out-of-hours message is moot now; a later handoff may say it again
			a.setSessionValue(session, aiSessionHandedOff, false)
		}
		if msg := handoffMessageFor(reason, q); msg != "" {
			if err := a.sendAndSaveAgentText(aiCtx("ai_handoff"), account, contact, msg, sender); err != nil {
				a.Log.Error("AI handoff: could not send the waiting message", "error", err, "contact_id", contact.ID)
			}
		}
		return aiHandoffTransferred
	case TransferAlreadyActive:
		return aiHandoffAlreadyActive
	case TransferOutsideHours:
		if !told {
			a.setSessionValue(session, aiSessionHandedOff, true)
			if msg := strings.TrimSpace(settings.BusinessHours.OutOfHoursMessage); msg != "" {
				_ = a.sendAndSaveTextMessage(botCtx("ai_handoff_out_of_hours"), account, contact, msg)
			}
		}
		return aiHandoffOutsideHours
	}
	return aiHandoffFailedNotified
}

// errNoUnitTeam marks "go straight to the general queue" inside handoffFromAI.
var errNoUnitTeam = &noUnitTeamError{}

type noUnitTeamError struct{}

func (*noUnitTeamError) Error() string { return "the number has no valid unit team" }
