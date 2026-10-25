package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// The WhatsApp side of a case, on the case's own channel (the logistics account).
//
// Being on a case never opens the customer's whole WhatsApp history. This file
// exposes ONE thing: the messages the case's channel account exchanged with the
// case's customer, and only to those who work the case (team, assignee, unit
// supervisors, view_all). Participants and the SAC who forwarded it see the
// operational context (state of the automatic message, in the communication
// card) and nothing of the conversation. Conversations on any other account,
// such as the SAC's own chat, are never reachable from here.
//
// Writing to the customer is a different endpoint from writing to colleagues
// (the internal chat): the destinations are separate by construction, and each
// has its own authorization.

const serviceWindow = 24 * time.Hour

// caseChannelAccount is the WhatsApp account that talks to the customer about the
// case: the one its communication used, else the one the automation names today.
// nil when none is configured; nothing falls back to another account.
func (a *App) caseChannelAccount(orgID uuid.UUID, occ *models.Occurrence) *models.WhatsAppAccount {
	name := ""
	var n models.OccurrenceNotification
	if a.DB.Where("organization_id = ? AND occurrence_id = ? AND whats_app_account <> ''", orgID, occ.ID).Order("created_at DESC").First(&n).Error == nil {
		name = n.WhatsAppAccount
	} else if cfg := resolveAutomation(a.DB, orgID, occ.UnitID, occ.TeamID, automationEventForwarded); cfg != nil {
		name = cfg.WhatsAppAccount
	}
	if name == "" {
		return nil
	}
	var acc models.WhatsAppAccount
	if a.DB.Where("organization_id = ? AND name = ?", orgID, name).First(&acc).Error != nil {
		return nil
	}
	return &acc
}

type caseWhatsAppMessage struct {
	ID           uuid.UUID `json:"id"`
	Direction    string    `json:"direction"`
	Type         string    `json:"type"`
	Content      string    `json:"content"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	TemplateName string    `json:"template_name,omitempty"`
	SentBy       string    `json:"sent_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type caseWhatsAppTemplate struct {
	Name     string   `json:"name"`
	Language string   `json:"language"`
	Body     string   `json:"body"`
	Params   []string `json:"params"`
}

// workTheCase: may read and write the channel (not merely follow the case).
func workTheCase(acc occurrenceAccess) bool { return acc.interact || acc.supervisor }

// GetOccurrenceWhatsApp returns the case's channel conversation to those who
// work it; everyone else gets only the fact that it is restricted.
func (a *App) GetOccurrenceWhatsApp(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	account := a.caseChannelAccount(orgID, occ)
	out := map[string]any{"configured": account != nil, "restricted": !workTheCase(acc)}
	if account != nil {
		out["account"] = account.Name
	}
	if account == nil || !workTheCase(acc) {
		return r.SendEnvelope(out)
	}

	var rows []models.Message
	a.DB.Where("organization_id = ? AND contact_id = ? AND whats_app_account = ?", orgID, occ.ContactID, account.Name).
		Order("created_at DESC").Limit(50).Find(&rows)
	msgs := make([]caseWhatsAppMessage, len(rows))
	for i := range rows {
		m := rows[len(rows)-1-i] // oldest first
		msgs[i] = caseWhatsAppMessage{ID: m.ID, Direction: string(m.Direction), Type: string(m.MessageType), Content: m.Content,
			Status: string(m.Status), Error: m.ErrorMessage, TemplateName: m.TemplateName, CreatedAt: m.CreatedAt}
		if m.SentByUserID != nil {
			msgs[i].SentBy = a.senderNameForBroadcast(&m)
		}
	}
	out["messages"] = msgs
	out["window_open"] = a.serviceWindowOpen(orgID, occ.ContactID, account.Name)
	canWrite := a.HasPermission(userID, models.ResourceOccurrences, models.ActionWrite, orgID)
	out["can_reply"] = canWrite
	out["templates"] = a.caseTemplates(orgID, account.Name)
	return r.SendEnvelope(out)
}

// serviceWindowOpen: the customer wrote on THIS account within 24 h, which is
// what allows free text; outside it only approved templates may be sent.
func (a *App) serviceWindowOpen(orgID, contactID uuid.UUID, account string) bool {
	var n int64
	a.DB.Model(&models.Message{}).Where("organization_id = ? AND contact_id = ? AND whats_app_account = ? AND direction = ? AND created_at >= ?",
		orgID, contactID, account, models.DirectionIncoming, time.Now().Add(-serviceWindow)).Count(&n)
	return n > 0
}

// caseTemplates lists what an agent may send by hand: approved UTILITY templates
// of the case's account (the same rule as the automation).
func (a *App) caseTemplates(orgID uuid.UUID, account string) []caseWhatsAppTemplate {
	var ts []models.Template
	a.DB.Where("organization_id = ? AND whats_app_account = ? AND status = ? AND category = ?", orgID, account, "APPROVED", "UTILITY").
		Order("name").Limit(50).Find(&ts)
	out := make([]caseWhatsAppTemplate, 0, len(ts))
	for _, t := range ts {
		out = append(out, caseWhatsAppTemplate{Name: t.Name, Language: t.Language, Body: t.BodyContent, Params: templateutil.ExtParamNames(t.BodyContent)})
	}
	return out
}

type caseSendRequest struct {
	Content  string   `json:"content"`
	Template string   `json:"template"`
	Language string   `json:"language"`
	Params   []string `json:"params"`
}

// SendOccurrenceWhatsApp sends to the CUSTOMER, from the case's channel account:
// free text inside the 24 h window, or an approved UTILITY template. It never
// accepts an internal destination, never touches the internal chat and never
// changes who owns the conversation.
func (a *App) SendOccurrenceWhatsApp(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	if !workTheCase(acc) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You can follow this case but not write to the customer", nil, "")
	}
	account := a.caseChannelAccount(orgID, occ)
	if account == nil {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This case has no WhatsApp account configured", nil, "")
	}
	var req caseSendRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", occ.ContactID, orgID).First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	msgReq := OutgoingMessageRequest{Account: account, Contact: &contact}
	switch {
	case strings.TrimSpace(req.Template) != "":
		var tpl models.Template
		if err := a.DB.Where("organization_id = ? AND whats_app_account = ? AND name = ? AND language = ?",
			orgID, account.Name, req.Template, req.Language).First(&tpl).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Template not found on this account", nil, "")
		}
		if msg := a.checkAutomationTemplate(orgID, account.Name, tpl.Name, tpl.Language); msg != "" {
			return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, msg, nil, "")
		}
		var inbound int64
		a.DB.Model(&models.Message{}).Where("organization_id = ? AND contact_id = ? AND direction = ? AND created_at >= ?",
			orgID, contact.ID, models.DirectionIncoming, time.Now().Add(-customerInitiatedWindow)).Count(&inbound)
		if inbound == 0 {
			return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "The customer has not written to us recently: there is no basis to contact them", nil, "")
		}
		names := templateutil.ExtParamNames(tpl.BodyContent)
		if len(req.Params) != len(names) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The template needs exactly its parameters", nil, "")
		}
		params := make(map[string]string, len(names))
		for i, n := range names {
			if strings.TrimSpace(req.Params[i]) == "" {
				return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Template parameters cannot be empty", nil, "")
			}
			params[n] = req.Params[i]
		}
		msgReq.Type, msgReq.Template, msgReq.BodyParams = models.MessageTypeTemplate, &tpl, params
	case strings.TrimSpace(req.Content) != "":
		if !a.serviceWindowOpen(orgID, contact.ID, account.Name) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "The 24-hour service window is closed on this account; send an approved template", nil, "")
		}
		msgReq.Type, msgReq.Content = models.MessageTypeText, strings.TrimSpace(req.Content)
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "content or template is required", nil, "")
	}

	var sendErr error
	opts := DefaultSendOptions()
	opts.Async = false
	opts.SentByUserID = &userID
	opts.AllowOtherOwner = true // never takes the conversation from whoever holds it
	opts.OnResult = func(_ string, e error) { sendErr = e }
	msg, err := a.SendOutgoingMessage(context.Background(), msgReq, opts)
	if err != nil {
		if errors.Is(err, ErrConversationOwned) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "This conversation is assigned to another agent", nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to send", nil, "")
	}
	a.DB.Create(&models.OccurrenceEvent{OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventReply,
		Content: msg.Content, CreatedByID: &userID, Metadata: models.JSONB{"account": account.Name, "channel": "case_whatsapp"}})
	if sendErr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "The provider did not accept the message: "+sendErr.Error(), nil, "")
	}
	return r.SendEnvelope(map[string]any{"sent": true, "message_id": msg.ID})
}
