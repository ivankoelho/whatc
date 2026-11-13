package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// Writing to a customer is the chat's permission and the channel's scope, on every
// endpoint that can do it. From a case that takes chat:write AND being someone who
// may use the case's channel (canUseCaseChannel: assignee, the case's or the
// account's team, or conversations:view_all). occurrences:write only lets a person
// work the case; supervising a case never opens the customer's conversation.

const (
	errTypeChatWriteRequired    = "chat_write_required"
	errTypeCaseChannelForbidden = "case_channel_forbidden"
	errTypeSendRecordFailed     = "send_record_failed"

	// protocolSendAuditType is the audit trail of what was sent to a customer from a
	// case: author, protocol, the text actually sent, channel and result.
	protocolSendAuditType = "occurrence_message"
)

// requireChatWrite writes the 403 and returns false when the user may not write to
// customers at all. It says nothing about the case, the window or the account.
func (a *App) requireChatWrite(r *fastglue.Request, orgID, userID uuid.UUID) bool {
	if a.HasPermission(userID, models.ResourceChat, models.ActionWrite, orgID) {
		return true
	}
	_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot send messages to the customer", nil, errTypeChatWriteRequired)
	return false
}

// customerAccountOf is the WhatsApp account the customer's conversation lives on,
// or nil when it is not configured.
func (a *App) customerAccountOf(orgID uuid.UUID, occ *models.Occurrence) *models.WhatsAppAccount {
	if occ.Contact == nil {
		return nil
	}
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, occ.Contact.WhatsAppAccount).First(&account).Error; err != nil {
		return nil
	}
	return &account
}

// canMessageCustomerFromCase is the one rule behind send-protocol, reply and the
// "can" flags the interface uses to show or hide the buttons: chat:write, working
// the case, and a valid tie to the customer's conversation. That tie is either the
// case channel's own rule (assignee, the case's or the account's team,
// conversations:view_all) or the right to interact with the conversation itself
// (its owner, its queue's team, conversations:view_all): whoever may write on that
// customer's chat may send the protocol or reply about it. Seeing every case
// (occurrences:view_all) and holding chat:write is not a tie.
func (a *App) canMessageCustomerFromCase(orgID, userID uuid.UUID, occ *models.Occurrence, acc occurrenceAccess) bool {
	if occ.IsInternal() || !a.HasPermission(userID, models.ResourceChat, models.ActionWrite, orgID) || !workTheCase(acc) {
		return false
	}
	if a.canUseCaseChannel(orgID, userID, occ, acc, a.customerAccountOf(orgID, occ), true) {
		return true
	}
	// The case is the person's own: nobody else was given it and it has no team, and
	// they opened it (the same "own case" the case capabilities use).
	if occ.AssignedUserID == nil && occ.TeamID == nil && occ.OpenedByUserID == userID {
		return true
	}
	return occ.Contact != nil && a.canInteractWithConversation(userID, orgID, occ.Contact)
}

// authorizeCustomerMessageFromCase is the gate of the case endpoints that send to
// the customer. Order matters: chat:write first (it reveals nothing), then the case
// and the channel scope. Only after it passes may the caller look at the window or
// the account, so a refusal never tells the refused anything about them.
func (a *App) authorizeCustomerMessageFromCase(r *fastglue.Request, orgID, userID uuid.UUID) (*models.Occurrence, *models.WhatsAppAccount, bool) {
	if !a.requireChatWrite(r, orgID, userID) {
		return nil, nil, false
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, true)
	if err != nil {
		return nil, nil, false
	}
	if !a.canMessageCustomerFromCase(orgID, userID, occ, acc) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "You can follow this case but not write to the customer", nil, errTypeCaseChannelForbidden)
		return nil, nil, false
	}
	return occ, a.customerAccountOf(orgID, occ), true
}

// Protocol send results in the audit trail.
//
//	attempted  written BEFORE the send, so nothing leaves untraced
//	sent       the provider accepted it (written after)
//	failed     the provider (or we) PROVED it was not accepted: safe to try again
//	uncertain  no answer to rely on (timeout, dropped connection, 5xx): the message
//	           may have been delivered, so it is never repeated automatically
//
// Both rows of one send share send_id and message_id. message_id is assigned before
// the send and is the id of the message row, so an "attempted" row with no result
// row can always be matched to what really happened (see the reconciliation query
// in the R2 report): a message row with a status, or none at all (never created).
const (
	protocolResultAttempted = "attempted"
	protocolResultSent      = "sent"
	protocolResultFailed    = "failed"
	protocolResultUncertain = "uncertain"
)

// recordProtocolSend writes one append-only audit entry about a protocol send.
func (a *App) recordProtocolSend(orgID, userID uuid.UUID, occ *models.Occurrence, sendID, messageID uuid.UUID, channel, content, result string, extra ...map[string]any) error {
	field := func(name string, value any) map[string]any {
		return map[string]any{"field": name, "old_value": nil, "new_value": value}
	}
	changes := []map[string]any{
		field("kind", "protocol"),
		field("send_id", sendID.String()),
		field("protocol_number", occ.ProtocolNumber),
		field("content", content),
		field("channel", channel),
		field("message_id", messageID.String()),
		field("result", result),
	}
	changes = append(changes, extra...)
	action := models.AuditActionUpdated
	if result == protocolResultAttempted {
		action = models.AuditActionCreated
	}
	return audit.LogAuditTx(a.DB, orgID, userID, audit.GetUserName(a.DB, userID), protocolSendAuditType, occ.ID, action, changes...)
}
