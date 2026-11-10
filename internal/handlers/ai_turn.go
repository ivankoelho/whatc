package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
)

// aiTurn carries what only the agent's wait adds to an AI turn. The zero value is the turn as it
// always was: answer the message just received, with the history as it stands, no typing indicator.
type aiTurn struct {
	// typingTo is the customer's message the "typing…" indicator is shown for ("" = no indicator).
	typingTo string
	// historyBefore, when set, keeps the session history to what came before the grouped messages
	// (they are all in the prompt as the user message; repeating them would only cost tokens).
	historyBefore time.Time
	// typing receives the indicator's counters for the audit trail (may be nil).
	typing *aiTypingStats
}

// answerWithAI is the AI step of an incoming text message: the agent (or the free-text AI) answers,
// hands over, or the configured fallback is sent. It is the code that used to sit in
// processIncomingMessageFull, moved here unchanged so the agent's wait can run it later with the
// grouped messages; apart from the typing indicator and the history cut, behavior is identical.
// It returns how the turn ended, for the wait's audit trail.
func (a *App) answerWithAI(account *models.WhatsAppAccount, contact *models.Contact, session *models.ChatbotSession,
	settings *models.ChatbotSettings, messageText string, isNewSession bool, turn aiTurn) string {

	outcome := aiOutcomeNoAnswer
	agentTurn := false // an AI agent is answering this message: everything it says goes through the takeover guard
	if settings.AI.Enabled && settings.AI.Provider != "" && settings.AI.APIKey != "" {
		a.Log.Info("Attempting AI response", "provider", settings.AI.Provider, "model", settings.AI.Model)

		// The indicator starts only now that the agent is going to answer, and ends as soon as the
		// model is done (the answer follows at once). It never delays or blocks the answer.
		var stopTyping func() aiTypingStats
		if turn.typingTo != "" && a.agentForSession(settings, session) != nil {
			stopTyping = a.startAITyping(account, contact.ID, turn.typingTo)
		}
		reply, err := a.generateAIReplyWith(settings, session, messageText, aiFeatureChatbotReply, aiReplyOpts{historyBefore: turn.historyBefore})
		if stopTyping != nil {
			stats := stopTyping()
			if turn.typing != nil {
				*turn.typing = stats
			}
		}
		agentTurn = reply.ByAgent
		aiResponse := reply.Text
		if err != nil {
			a.Log.Error("AI response failed", "error", err, "provider", settings.AI.Provider, "model", settings.AI.Model)
			if reply.ByAgent {
				// The agent's own failures: the monthly cap hands over at once; any other failure
				// hands over on the second one in a row. No waiting message for a single failure.
				if errors.Is(err, ErrAICapReached) {
					a.handoffFromAI(account, contact, session, settings, aiHandoffCap)
					return aiOutcomeHandoff
				}
				if a.noteAIFailure(session) >= aiFailuresBeforeHandoff {
					if a.handoffFromAI(account, contact, session, settings, aiHandoffFailure) {
						return aiOutcomeHandoff
					}
				}
			}
			// Fall through to default response
		} else if aiResponse != "" || reply.Handoff != "" {
			if reply.ByAgent {
				a.clearAIFailures(session)
			}
			if aiResponse == "" { // the agent only asks for a human: there is no text to send first
				a.handoffFromAI(account, contact, session, settings, reply.Handoff)
				return aiOutcomeHandoff
			}
			a.Log.Info("AI response generated successfully", "response_length", len(aiResponse))
			sent, sendErr := a.sendAIReply(aiCtx("chatbot_reply"), reply.ByAgent, account.OrganizationID, contact.ID,
				func(ctx context.Context) error {
					if len(reply.Buttons) > 0 { // the server's confirmation message, with its buttons
						return a.sendAndSaveInteractiveButtons(ctx, account, contact, aiResponse, reply.Buttons)
					}
					return a.sendAndSaveAgentText(ctx, account, contact, aiResponse, reply.Sender)
				})
			if sendErr != nil {
				a.Log.Error("Failed to send AI response", "error", sendErr, "contact", contact.PhoneNumber)
			} else if !sent {
				// not sent on purpose (a human took over while the reply was being written, or the
				// guard could not be had in time): say nothing, and do not fall through to the
				// default response either. The reason is in the AIUsageLog row of the discard.
				a.Log.Info("AI reply not sent", "contact", contact.PhoneNumber)
				return aiOutcomeNotSent
			}
			a.logSessionMessage(session.ID, models.DirectionOutgoing, aiResponse, "ai_response")
			if reply.Handoff != "" { // the agent said its last words; now hand over
				a.handoffFromAI(account, contact, session, settings, reply.Handoff)
				return aiOutcomeHandoff
			}
			return aiOutcomeReplied
		} else {
			a.Log.Warn("AI returned empty response")
		}
	} else {
		a.Log.Info("AI not configured", "ai_enabled", settings.AI.Enabled, "has_provider", settings.AI.Provider != "", "has_api_key", settings.AI.APIKey != "")
		outcome = aiOutcomeAIOff
	}

	// If no AI response or AI not enabled, send fallback message (for existing sessions)
	// Greeting is already sent for new sessions above
	if settings.FallbackMessage != "" && !isNewSession {
		a.Log.Info("Sending fallback message", "response", settings.FallbackMessage)
		fallbackButtons := make([]map[string]any, 0)
		for _, btn := range settings.FallbackButtons {
			if btnMap, ok := btn.(map[string]any); ok {
				fallbackButtons = append(fallbackButtons, btnMap)
			}
		}
		// After an agent turn (the model may have taken up to aiCallDeadline) the fallback is part
		// of that turn: it goes through the same takeover guard. Otherwise it is sent as always.
		sent, err := a.sendAIReply(botCtx("fallback"), agentTurn, account.OrganizationID, contact.ID,
			func(ctx context.Context) error {
				if len(fallbackButtons) > 0 {
					return a.sendAndSaveInteractiveButtons(ctx, account, contact, settings.FallbackMessage, fallbackButtons)
				}
				return a.sendAndSaveTextMessage(ctx, account, contact, settings.FallbackMessage)
			})
		if err != nil {
			a.Log.Error("Failed to send fallback message", "error", err, "contact", contact.PhoneNumber)
		} else if !sent {
			a.Log.Info("Fallback message not sent", "contact", contact.PhoneNumber)
			return aiOutcomeNotSent
		}
		a.logSessionMessage(session.ID, models.DirectionOutgoing, settings.FallbackMessage, "fallback_response")
		return aiOutcomeFallback
	} else if !isNewSession {
		a.Log.Info("No fallback message configured for existing session")
	}
	return outcome
}
