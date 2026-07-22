package handlers

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// NotifyTyping tells agents viewing this contact that the caller is typing.
//
// Nothing is persisted: the event is ephemeral and the frontend expires it
// after a few seconds. The contact lookup is not decoration — without it any
// authenticated user could fake typing on any contact in the instance.
func (a *App) NotifyTyping(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	// Same visibility rules as GetContact: users without contacts:read only
	// reach contacts assigned to them.
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).
		First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	if !a.canInteractWithConversation(userID, orgID, &contact) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden,
			"You do not have access to this conversation", nil, "")
	}

	if a.WSHub != nil {
		a.WSHub.BroadcastToContactViewers(orgID, contact.ID, websocket.WSMessage{
			Type: websocket.TypeAgentTyping,
			Payload: websocket.AgentTypingPayload{
				ContactID: contact.ID,
				UserID:    userID,
				UserName:  audit.GetUserName(a.DB, userID),
				At:        time.Now(),
			},
		})
	}

	r.RequestCtx.SetStatusCode(fasthttp.StatusNoContent)
	return nil
}
