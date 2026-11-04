package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// serviceWindowOpen reports whether the customer messaged within the last 24
// hours. Outside that window WhatsApp only accepts templates.
//
// The same expression exists inline in contacts.go, where it feeds
// service_window_open on the contact payload. It is duplicated rather than
// extracted because extracting it would edit a production file for no
// behavioural gain — see the spec's "no production change" constraint.
func serviceWindowOpen(contact *models.Contact) bool {
	return contact.LastInboundAt != nil && time.Since(*contact.LastInboundAt) < 24*time.Hour
}

// maxWhatsAppTextLength is WhatsApp's limit for a text message body.
const maxWhatsAppTextLength = 4096

// SendOccurrenceProtocolRequest is the optional body for sending the protocol
// message. An absent, empty or blank Message keeps the legacy hardcoded text.
type SendOccurrenceProtocolRequest struct {
	Message string `json:"message"`
}

// SendOccurrenceProtocol sends the protocol number to the customer.
//
// This is the only endpoint in the system that enforces the 24-hour window.
// SendMessage does not — it computes the window, reports it, and sends anyway.
// The inconsistency is deliberate and recorded in the spec: enforcing it
// globally would change production behaviour.
func (a *App) SendOccurrenceProtocol(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}

	// Sending the protocol is writing to the customer: chat:write plus the scope of
	// the case's channel, like the reply. This comes before anything that could
	// reveal the window or the account, and applies to the optional free text too.
	occ, account, ok := a.authorizeCustomerMessageFromCase(r, orgID, userID)
	if !ok {
		return nil
	}

	// One deliberate send, one key: the interface makes it, the database enforces it.
	key, ok := requireIdempotencyKey(r)
	if !ok {
		return nil
	}

	// The body is optional: no body keeps the legacy text. A present body must
	// be valid JSON.
	var customMessage string
	if len(r.RequestCtx.PostBody()) > 0 {
		var req SendOccurrenceProtocolRequest
		if err := r.Decode(&req, "json"); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
		}
		customMessage = strings.TrimSpace(req.Message)
		if utf8.RuneCountInString(customMessage) > maxWhatsAppTextLength {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
				"Message exceeds the 4096-character WhatsApp limit", nil, "")
		}
	}

	contact := occ.Contact
	body := customMessage
	if body == "" {
		body = fmt.Sprintf("Seu protocolo de atendimento é %s. Guarde este número para consultas futuras.",
			occ.ProtocolNumber)
	}

	// The hash covers everything that decides what is sent and to whom, so the same key can
	// only ever mean this exact request.
	identity := protocolRequestIdentity{
		Kind: models.SendIntentKindProtocol, OccurrenceID: occ.ID.String(), ProtocolNumber: occ.ProtocolNumber,
		ContactID: contact.ID.String(), Phone: contact.PhoneNumber, BSUID: contact.BSUID, Content: body,
	}
	if account != nil {
		identity.AccountID, identity.AccountName = account.ID.String(), account.Name
	}

	// Claim the key. Exactly one request per key gets past this point; every other one only
	// reads the row and answers from it: nothing below runs for them.
	intent, claimed, err := a.claimSendIntent(orgID, userID, occ, key, hashProtocolRequest(identity))
	if err != nil {
		a.Log.Error("Failed to claim the send intent", "error", err, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record the send; nothing was sent", nil, errTypeSendRecordFailed)
	}
	if !claimed {
		return a.answerExistingIntent(r, intent, occ, userID, hashProtocolRequest(identity))
	}
	sendID, messageID := intent.ID, intent.MessageID

	// From here this request owns the send, and every way out closes the claim.
	answer := func(result string, status int, message, errType string) error {
		st, env := intentAnswer(intent, occ.ProtocolNumber, result, status, message, errType)
		a.finishSendIntent(intent, result, st, env)
		return sendStoredAnswer(r, st, env, false)
	}

	found := account != nil
	// Direct Send lets Meta carry free text outside the window.
	if !serviceWindowOpen(contact) && !(found && account.DirectSendEnabled) {
		return answer(protocolResultFailed, fasthttp.StatusUnprocessableEntity,
			"The 24-hour service window is closed; only templates can be sent", "")
	}
	if !found {
		return answer(protocolResultFailed, fasthttp.StatusBadRequest, "WhatsApp account not found for this contact", "")
	}

	// Nothing reaches a customer untraced: the record of the attempt is written
	// first, and if it cannot be written the message is not sent.
	if err := a.recordProtocolSend(orgID, userID, occ, sendID, messageID, account.Name, body, protocolResultAttempted); err != nil {
		a.Log.Error("Failed to record the protocol send", "error", err, "occurrence", occ.ID, "user_id", userID)
		return answer(protocolResultFailed, fasthttp.StatusInternalServerError, "Failed to record the send; nothing was sent", errTypeSendRecordFailed)
	}
	recordResult := func(result string, cause error) {
		var extra []map[string]any
		if cause != nil {
			extra = append(extra, map[string]any{"field": "error", "old_value": nil, "new_value": whatsapp.PublicErrorText(cause)})
		}
		if err := a.recordProtocolSend(orgID, userID, occ, sendID, messageID, account.Name, body, result, extra...); err != nil {
			// The message is already out (or already failed). The answer stays what it is:
			// failing the request now would only invite a second send of a message that
			// went out. The "attempted" row stays, and its message_id says what happened.
			a.Log.Error("Failed to record the protocol send result", "error", err, "occurrence", occ.ID, "send_id", sendID, "result", result)
		}
	}

	// A system message on purpose (no SentByUserID): the protocol number and the
	// registration text are the company speaking, so they are never signed and
	// never open an attendance or change the contact status. Who sent it is in the
	// record above, not in the text the customer reads.
	var sendErr error
	opts := DefaultSendOptions()
	opts.Async = false // the answer and the record must tell what really happened
	opts.OnResult = func(_ string, e error) { sendErr = e }
	_, err = a.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		MessageID: messageID,
		Account:   account,
		Contact:   contact,
		Type:      models.MessageTypeText,
		Content:   body,
		Origin:    usage.Origin{ActorType: usage.ActorAgent, ActorUserID: &userID, OccurrenceID: &occ.ID, Detail: "occurrence_protocol"},
	}, opts)
	if err != nil {
		// Before the provider was reached (ownership, the message row): nothing was sent.
		a.Log.Error("Failed to send protocol", "error", err, "occurrence", occ.ID)
		recordResult(protocolResultFailed, err)
		return answer(protocolResultFailed, fasthttp.StatusInternalServerError, "Failed to send protocol", "")
	}
	if sendErr != nil {
		a.Log.Error("The provider did not accept the protocol", "error", sendErr, "occurrence", occ.ID, "send_id", sendID)
		if !whatsapp.NotSent(sendErr) {
			// A timeout, a dropped connection or a 5xx proves nothing: Meta may have
			// accepted it. It is recorded as uncertain and answered with 202, not an error:
			// an error status invites the caller to try again, and that is exactly what
			// would duplicate a message that may already have gone out. 202 is not a
			// delivery confirmation: the body says the result is pending confirmation.
			// The key stays taken: a repeat gets this same answer and sends nothing.
			recordResult(protocolResultUncertain, sendErr)
			return answer(protocolResultUncertain, fasthttp.StatusAccepted, "", "")
		}
		recordResult(protocolResultFailed, sendErr)
		return answer(protocolResultFailed, fasthttp.StatusBadGateway, "The provider did not accept the message", "")
	}
	recordResult(protocolResultSent, nil)

	a.DB.Create(&models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventProtocolSent,
		Content:        occ.ProtocolNumber,
		CreatedByID:    &userID,
	})

	return answer(protocolResultSent, fasthttp.StatusOK, "", "")
}
