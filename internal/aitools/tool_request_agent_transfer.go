package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// RequestAgentTransferToolName is the first write tool: it asks for the customer's conversation to be
// moved to the human support queue.
const RequestAgentTransferToolName = "request_agent_transfer"

const maxTransferReasonRunes = 200

// FeatureChatbotReply is the only feature that gets the write tool (the flow's ai_response node does not).
const FeatureChatbotReply = "chatbot_reply"

// NotAvailableResult is what the model hears when a proposal cannot be made for a reason of the
// customer's situation (not of governance). Governance denials stay generic.
func NotAvailableResult(reason string) ai.ToolResult {
	b, _ := json.Marshal(map[string]any{"status": "not_available", "reason": reason, "transfer_performed": false})
	return ai.ToolResult{Content: string(b)}
}

// AwaitingConfirmationResult is what the model hears when a proposal was created: the transfer was NOT done.
func AwaitingConfirmationResult() ai.ToolResult {
	return ai.ToolResult{Content: `{"status":"awaiting_customer_confirmation","transfer_performed":false}`}
}

// Reasons the model may hear in a not_available result.
const (
	NotAvailableAlreadyWithAgent = "already_with_agent"
	NotAvailableOutsideHours     = "outside_business_hours"
	NotAvailableTooManyRequests  = "too_many_requests"
	NotAvailableRecentlyDeclined = "recently_declined"
)

// NewRequestAgentTransferSpec builds the request_agent_transfer tool.
//
// The tool only PROPOSES: calling it never transfers anything. The customer confirms with a button
// the server sends; only then does the server carry the transfer out, always to the queue (the model
// chooses no destination). The only argument is an optional short reason, which is untrusted text
// from the model: cleaned, cut to 200 characters and kept only as a labelled line in the transfer's note.
func NewRequestAgentTransferSpec() ToolSpec {
	return ToolSpec{
		Name: RequestAgentTransferToolName,
		Description: "Asks to transfer the customer to the human support queue. It does NOT transfer: it only creates a request " +
			"that the customer has to confirm with a button the system sends. Never tell the customer that the transfer was " +
			"done or is done; after calling it, wait. Use it when the customer asks to talk to a person, or the matter clearly " +
			"needs a human. The optional reason is a short note in the customer's words. Returns the status of the request, " +
			"or why a request cannot be made right now.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"reason":{"type":"string","description":"Short reason, in the customer's words. Optional."}}}`),
		Risk:     RiskWrite,
		Features: []string{FeatureChatbotReply},
		Confirm: &ConfirmSpec{
			Prompt: func(ttl time.Duration) string {
				minutes := int(ttl.Minutes())
				if minutes < 1 {
					minutes = 1
				}
				return fmt.Sprintf("Posso transferir seu atendimento para um atendente humano? Toque em “Sim, transferir” para confirmar. "+
					"Esta solicitação vale por %d minutos.", minutes)
			},
			ConfirmLabel: "Sim, transferir",
			DeclineLabel: "Não, obrigado",
			DeclinedText: "Tudo bem, seguimos por aqui.",
			ExpiredText:  "Essa solicitação expirou. Se ainda quiser falar com um atendente, é só pedir.",
			OutcomeText: func(outcome string) string {
				switch outcome {
				case models.AIConfirmationOutcomeCreated:
					return "Pronto! Você entrou na fila de atendimento. Um atendente vai responder em breve."
				case models.AIConfirmationOutcomeAlreadyActive:
					return "Você já está sendo atendido por nossa equipe."
				}
				return ""
			},
		},
		WriteFactory: func(sc Scope, d WriteDeps) WriteTool {
			return requestAgentTransferTool{scope: sc, transfers: d.Transfers}
		},
	}
}

type requestAgentTransferTool struct {
	scope     Scope
	transfers TransferService
}

type transferArgs struct {
	Reason *string `json:"reason"`
}

// Propose validates the arguments and the customer's situation. It changes nothing.
func (t requestAgentTransferTool) Propose(ctx context.Context, call ai.ToolCall) (*Proposal, ai.ToolResult, error) {
	var args transferArgs
	if err := DecodeArgs(call, &args); err != nil { // team_id, contact_id, agent_id... are refused
		return nil, InvalidArgsResult(), nil
	}
	if t.scope.ContactID == nil || t.scope.OrganizationID == uuid.Nil || t.transfers == nil {
		return nil, ai.ToolResult{}, fmt.Errorf("aitools: %s needs an organization, a contact and the transfer service", RequestAgentTransferToolName)
	}
	if t.transfers.HasActive(ctx, t.scope.OrganizationID, *t.scope.ContactID) {
		return nil, NotAvailableResult(NotAvailableAlreadyWithAgent), nil
	}
	if !t.transfers.WithinBusinessHours(ctx, t.scope.OrganizationID, t.scope.WhatsAppAccount) {
		return nil, NotAvailableResult(NotAvailableOutsideHours), nil
	}
	p := &Proposal{Args: models.JSONB{}}
	if args.Reason != nil {
		if reason := cleanText(*args.Reason, maxTransferReasonRunes); reason != "" {
			p.Args["reason"] = reason
		}
	}
	return p, AwaitingConfirmationResult(), nil
}

// Execute carries out the confirmed proposal: the transfer to the queue, idempotent. The reason is
// untrusted and only becomes a labelled line of the note.
func (t requestAgentTransferTool) Execute(ctx context.Context, args models.JSONB, confirmedAt time.Time) (ExecResult, error) {
	if t.transfers == nil {
		return ExecResult{}, fmt.Errorf("aitools: %s needs the transfer service", RequestAgentTransferToolName)
	}
	note := "Solicitado pelo assistente de IA e confirmado pelo cliente em " + confirmedAt.UTC().Format("2006-01-02 15:04") + " UTC."
	if reason, _ := args["reason"].(string); reason != "" {
		note += "\nMotivo informado pela IA (não verificado): " + cleanText(reason, maxTransferReasonRunes)
	}
	return t.transfers.TransferToQueue(ctx, t.scope, note)
}
