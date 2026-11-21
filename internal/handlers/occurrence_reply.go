package handlers

import (
	"context"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// OccurrenceReplyRequest is the body for sending a public reply.
type OccurrenceReplyRequest struct {
	Content string `json:"content"`
}

// ReplyToOccurrence sends a message to the contact and records it as a public
// reply — distinct from CreateOccurrenceEvent's internal note. This is the
// only thing that sets FirstResponseAt: an internal note must never stop the
// first-response SLA clock, and an explicit action here avoids the ambiguity
// of guessing which of a contact's several open occurrences an ordinary chat
// message was meant to answer.
func (a *App) ReplyToOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	// Writing to the customer's WhatsApp is the chat's permission, and only for someone
	// who may use the case's channel: working a case does not include it, and chat:write
	// alone does not open every conversation of the organization. Checked before
	// anything that could reveal the window or the account.
	occ, account, ok := a.authorizeCustomerMessageFromCase(r, orgID, userID)
	if !ok {
		return nil
	}

	var req OccurrenceReplyRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Content == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "content is required", nil, "")
	}

	contact := occ.Contact
	found := account != nil
	// Direct Send lets Meta carry free text outside the window.
	if !serviceWindowOpen(contact) && (!found || !account.DirectSendEnabled) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity,
			"The 24-hour service window is closed; only templates can be sent", nil, "")
	}
	if !found {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"WhatsApp account not found for this contact", nil, "")
	}

	// A reply typed by the agent is human conversation, wherever it is typed:
	// same as the chat composer it is attributed to the agent (signed when the
	// org opted in) and opens or keeps the attendance, so the customer's answer
	// reaches a person instead of the chatbot.
	opts := DefaultSendOptions()
	opts.SentByUserID = &userID
	opts.AllowOtherOwner = true // a protocol reply never takes over, nor is blocked by, the chat owner
	if _, err := a.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: account,
		Contact: contact,
		Type:    models.MessageTypeText,
		Content: req.Content,
		Origin: usage.Origin{ActorType: usage.ActorAgent, ActorUserID: &userID, OccurrenceID: &occ.ID, Detail: "occurrence_reply"},
	}, opts); err != nil {
		a.Log.Error("Failed to send occurrence reply", "error", err, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to send reply", nil, "")
	}

	event := models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventReply,
		Content:        req.Content,
		CreatedByID:    &userID,
	}
	if err := a.DB.Create(&event).Error; err != nil {
		a.Log.Error("Failed to record occurrence reply event", "error", err, "occurrence", occ.ID)
	}

	// Only the first reply sets these — a second reply on an already-answered
	// case must not push the SLA clock forward again. FirstResponseAt lives on
	// the embedded SLATracking struct (SLA.FirstResponseAt), not as a bare
	// Occurrence field — see the comment on Occurrence.SLA.
	if occ.SLA.FirstResponseAt == nil {
		if err := a.DB.Model(occ).Updates(map[string]any{
			"first_response_at":    event.CreatedAt,
			"first_response_by_id": userID,
		}).Error; err != nil {
			a.Log.Error("Failed to stamp first response", "error", err, "occurrence", occ.ID)
		}
	}

	return r.SendEnvelope(map[string]any{"sent": true})
}
