package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"gorm.io/gorm"
)

// The notifier turns the communication intents written with a forwarded case
// into WhatsApp template messages. It reuses SendOutgoingMessage (templates,
// accounts, message rows, usage ledger, webhooks) and adds only what a
// background send needs: an atomic claim, a record of every attempt, and an
// honest answer to "did the provider accept it?".
//
// Delivery guarantees, stated plainly: this is at-most-once toward the customer
// for ambiguous outcomes and at-least-once for definite refusals. A request the
// provider may have accepted (timeout, dropped connection, 5xx, or a worker
// that died mid-send) is parked as "uncertain" and is only repeated by a person
// who accepts the risk of a duplicate. Exactly-once cannot be promised: the
// provider offers no idempotency key for message sends.

const (
	// notificationClaimTimeout is how long a "processing" claim may last before
	// the send is considered lost. The HTTP call itself is capped well below it.
	notificationClaimTimeout = 5 * time.Minute
	notificationSendTimeout  = 30 * time.Second
	notificationTickEvery    = 5 * time.Second
	notificationBatch        = 20
	// customerInitiatedWindow: how recent the customer's own message must be for an
	// automatic operational follow-up to be sent.
	customerInitiatedWindow = 90 * 24 * time.Hour
)

var notifierID = uuid.NewString()[:8]

// StartOccurrenceNotifier runs the worker until ctx ends. Safe to run in several
// processes: every transition is a conditional UPDATE.
func (a *App) StartOccurrenceNotifier(ctx context.Context) {
	t := time.NewTicker(notificationTickEvery)
	defer t.Stop()
	for {
		a.RecoverStuckNotifications(time.Now())
		a.ProcessDueNotifications(ctx, notificationBatch)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RecoverStuckNotifications parks sends whose worker vanished. The request may
// have reached the provider, so they become "uncertain", never "pending".
func (a *App) RecoverStuckNotifications(now time.Time) int64 {
	var stuck []models.OccurrenceNotification
	a.DB.Where("status = ? AND locked_at < ?", models.NotificationProcessing, now.Add(-notificationClaimTimeout)).Find(&stuck)
	var n int64
	for _, s := range stuck {
		res := a.DB.Model(&models.OccurrenceNotification{}).
			Where("id = ? AND status = ? AND locked_at < ?", s.ID, models.NotificationProcessing, now.Add(-notificationClaimTimeout)).
			Updates(map[string]any{"status": models.NotificationUncertain, "reason": "worker_lost_during_send",
				"last_error": "the worker stopped while the request was in flight", "locked_at": nil, "locked_by": ""})
		if res.RowsAffected == 1 {
			n++
			a.DB.Model(&models.OccurrenceNotificationAttempt{}).
				Where("notification_id = ? AND finished_at IS NULL", s.ID).
				Updates(map[string]any{"finished_at": now, "outcome": "uncertain", "error": "worker lost"})
			a.notificationEvent(&s, "uncertain", "worker_lost_during_send")
		}
	}
	return n
}

// ProcessDueNotifications claims and sends up to limit due intents and returns
// how many it handled. Two workers racing for one row: the conditional UPDATE
// lets exactly one proceed.
func (a *App) ProcessDueNotifications(ctx context.Context, limit int) int {
	// The master switch: while it is off, intents wait as "pending" and nothing is
	// claimed, so enabling it later sends what is still wanted, never something
	// that was cancelled meanwhile.
	if !a.Config.WhatsApp.AutomationSendingEnabled {
		return 0
	}
	var due []models.OccurrenceNotification
	a.DB.Where("status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", models.NotificationPending, time.Now()).
		Order("next_attempt_at").Limit(limit).Find(&due)
	handled := 0
	for i := range due {
		if ctx.Err() != nil {
			break
		}
		if a.claimNotification(&due[i]) {
			a.processNotification(ctx, &due[i])
			handled++
		}
	}
	return handled
}

func (a *App) claimNotification(n *models.OccurrenceNotification) bool {
	now := time.Now()
	res := a.DB.Model(&models.OccurrenceNotification{}).
		Where("id = ? AND status = ?", n.ID, models.NotificationPending).
		Updates(map[string]any{"status": models.NotificationProcessing, "locked_at": now, "locked_by": notifierID,
			"attempts": gorm.Expr("attempts + 1"), "last_error": ""})
	if res.RowsAffected != 1 {
		return false
	}
	n.Status, n.LockedAt, n.LockedBy, n.Attempts = models.NotificationProcessing, &now, notifierID, n.Attempts+1
	return true
}

// notificationOutcome is what one attempt concluded.
type notificationOutcome struct {
	status string // accepted | failed | uncertain | skipped | cancelled
	reason string
	err    string
	retry  bool // a definite refusal that is worth trying again
	wamid  string
	msgID  *uuid.UUID
	cat    string
}

// classifySendError separates "the provider refused it" (safe to retry or give
// up) from "we do not know" (never repeated automatically).
func classifySendError(err error) (status string, retry bool, reason string) {
	// The provider package decides what an error PROVES about delivery (Meta error code together with a 4xx
	// status); anything it cannot prove stays "unknown" and is never repeated automatically.
	var api *whatsapp.APIError
	isAPI := errors.As(err, &api)
	switch whatsapp.Classify(err) {
	case whatsapp.OutcomeTransient:
		return models.NotificationFailed, true, "provider_rate_limited"
	case whatsapp.OutcomeRefused:
		if isAPI {
			return models.NotificationFailed, false, "provider_rejected"
		}
		return models.NotificationFailed, false, "not_sent"
	default:
		if isAPI && api.Status >= 500 {
			return models.NotificationUncertain, false, "provider_server_error"
		}
		return models.NotificationUncertain, false, "provider_result_unknown"
	}
}

// publicErrorText is what is stored and shown about a failed send (see whatsapp.PublicErrorText).
func publicErrorText(err error) string { return whatsapp.PublicErrorText(err) }

func (a *App) processNotification(ctx context.Context, n *models.OccurrenceNotification) {
	started := time.Now()
	// Numbered across the whole history: a manual retry resets the policy counter
	// (n.Attempts) but never renumbers what already happened.
	var done int64
	a.DB.Model(&models.OccurrenceNotificationAttempt{}).Where("notification_id = ?", n.ID).Count(&done)
	attempt := models.OccurrenceNotificationAttempt{OrganizationID: n.OrganizationID, NotificationID: n.ID, Number: int(done) + 1,
		WhatsAppAccount: n.WhatsAppAccount, TemplateName: n.TemplateName, StartedAt: started, TriggeredByID: n.RetryRequestedByID}
	a.DB.Create(&attempt)
	out := a.sendNotification(ctx, n, &attempt)
	a.finishNotification(n, &attempt, out)
}

// sendNotification re-validates everything at send time and performs the call.
func (a *App) sendNotification(ctx context.Context, n *models.OccurrenceNotification, attempt *models.OccurrenceNotificationAttempt) notificationOutcome {
	skip := func(reason string) notificationOutcome {
		return notificationOutcome{status: models.NotificationSkipped, reason: reason}
	}

	var cfg models.OccurrenceChannelConfig
	if n.ConfigID == nil || a.DB.Where("id = ? AND organization_id = ?", *n.ConfigID, n.OrganizationID).First(&cfg).Error != nil || !cfg.IsActive {
		return notificationOutcome{status: models.NotificationCancelled, reason: "automation_disabled_or_removed"}
	}
	var occ models.Occurrence
	if err := a.DB.Where("id = ? AND organization_id = ?", n.OccurrenceID, n.OrganizationID).First(&occ).Error; err != nil {
		return notificationOutcome{status: models.NotificationCancelled, reason: "occurrence_not_found"}
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", occ.ContactID, n.OrganizationID).First(&contact).Error; err != nil || strings.TrimSpace(contact.PhoneNumber) == "" {
		return skip("contact_unavailable")
	}
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", n.OrganizationID, n.WhatsAppAccount).First(&account).Error; err != nil {
		return notificationOutcome{status: models.NotificationFailed, reason: "account_not_found_in_organization", err: "the configured account does not exist in this organization"}
	}

	// Eligibility: automatic messages are limited to the customer who wrote to us
	// (an operational follow-up to their own request). A template classified as
	// UTILITY is not, by itself, permission to contact someone. Without an inbound
	// message in the window there is no recorded basis, so nothing is sent.
	var inbound models.Message
	if err := a.DB.Where("organization_id = ? AND contact_id = ? AND direction = ? AND created_at >= ?",
		n.OrganizationID, contact.ID, models.DirectionIncoming, time.Now().Add(-customerInitiatedWindow)).
		Order("created_at DESC").First(&inbound).Error; err != nil {
		return skip("no_customer_initiated_contact")
	}
	a.DB.Model(&models.OccurrenceNotification{}).Where("id = ?", n.ID).
		Updates(map[string]any{"consent_basis": "customer_initiated", "consent_evidence": inbound.ID.String()})

	customer := strings.TrimSpace(contact.ProfileName)
	if customer == "" {
		return skip("missing_customer_name")
	}
	order := strings.TrimSpace(occ.OrderRef)
	tplName := n.TemplateName
	values := []string{customer, order, occ.ProtocolNumber}
	if order == "" {
		if n.FallbackTemplateName == "" {
			return skip("missing_order_number")
		}
		tplName, values = n.FallbackTemplateName, []string{customer, occ.ProtocolNumber}
	}
	attempt.TemplateName = tplName

	var tpl models.Template
	if err := a.DB.Where("organization_id = ? AND whats_app_account = ? AND name = ? AND language = ?",
		n.OrganizationID, n.WhatsAppAccount, tplName, n.TemplateLanguage).First(&tpl).Error; err != nil {
		return notificationOutcome{status: models.NotificationFailed, reason: "template_not_found", err: "template " + tplName + " not found on the account"}
	}
	if !strings.EqualFold(tpl.Status, "APPROVED") {
		return notificationOutcome{status: models.NotificationFailed, reason: "template_not_approved", err: "template status is " + tpl.Status, cat: tpl.Category}
	}
	if !strings.EqualFold(tpl.Category, "UTILITY") {
		return notificationOutcome{status: models.NotificationSkipped, reason: "template_category_not_allowed", cat: tpl.Category}
	}
	names := templateutil.ExtParamNames(tpl.BodyContent)
	if len(names) != len(values) {
		return notificationOutcome{status: models.NotificationFailed, reason: "template_parameter_mismatch",
			err: fmt.Sprintf("template has %d parameters, %d values available", len(names), len(values)), cat: tpl.Category}
	}
	params := make(map[string]string, len(names))
	for i, name := range names {
		if strings.TrimSpace(values[i]) == "" {
			return skip("empty_template_parameter")
		}
		params[name] = values[i]
	}

	var wamid string
	var sendErr error
	called := false
	opts := MessageSendOptions{BroadcastWebSocket: true, Async: false,
		OnResult: func(w string, e error) { wamid, sendErr, called = w, e, true }}
	sendCtx, cancel := context.WithTimeout(ctx, notificationSendTimeout)
	defer cancel()
	msg, err := a.SendOutgoingMessage(sendCtx, OutgoingMessageRequest{
		Account: &account, Contact: &contact, Type: models.MessageTypeTemplate, Template: &tpl, BodyParams: params,
		ExtraMetadata: models.JSONB{"occurrence_notification_id": n.ID.String(), "occurrence_id": occ.ID.String()},
	}, opts)
	out := notificationOutcome{cat: tpl.Category}
	if msg != nil {
		out.msgID = &msg.ID
	}
	switch {
	case !called:
		// The sender failed before reaching the provider (e.g. the message row
		// could not be written): nothing was sent, safe to retry.
		out.status, out.retry, out.reason, out.err = models.NotificationFailed, true, "not_sent", errString(err)
	case sendErr != nil:
		out.status, out.retry, out.reason = classifySendError(sendErr)
		out.err = publicErrorText(sendErr)
	default:
		out.status, out.wamid = models.NotificationAccepted, wamid
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// finishNotification records the attempt and moves the intent on. Provider
// facts (message id, acceptance time) are always saved; the STATUS only moves
// from "processing", so a webhook that already advanced it is never undone.
func (a *App) finishNotification(n *models.OccurrenceNotification, attempt *models.OccurrenceNotificationAttempt, out notificationOutcome) {
	now := time.Now()
	attemptOutcome := out.status
	if out.status == models.NotificationCancelled {
		attemptOutcome = "skipped"
	}
	a.DB.Model(&models.OccurrenceNotificationAttempt{}).Where("id = ?", attempt.ID).
		Updates(map[string]any{"finished_at": now, "outcome": attemptOutcome, "provider_message_id": out.wamid,
			"error": clip(out.err, 500), "template_name": attempt.TemplateName})

	facts := map[string]any{"locked_at": nil, "locked_by": "", "retry_requested_by_id": nil}
	if out.wamid != "" {
		facts["provider_message_id"], facts["accepted_at"] = out.wamid, now
	}
	if out.msgID != nil {
		facts["message_id"] = *out.msgID
	}
	if out.cat != "" {
		facts["template_category"] = out.cat
	}
	if out.err != "" {
		facts["last_error"] = clip(out.err, 500)
	}
	a.DB.Model(&models.OccurrenceNotification{}).Where("id = ?", n.ID).Updates(facts)

	next := map[string]any{"reason": out.reason}
	status := out.status
	switch out.status {
	case models.NotificationFailed:
		switch {
		case out.retry && n.Attempts < n.MaxAttempts:
			status = models.NotificationPending
			next["next_attempt_at"] = now.Add(time.Duration(n.RetryBackoffSecs) * time.Second * time.Duration(1<<uint(n.Attempts-1)))
		case n.OnFailure == "cancel":
			status, next["reason"] = models.NotificationCancelled, out.reason+"_cancelled_by_policy"
		}
	case models.NotificationAccepted:
		next["reason"] = ""
	}
	next["status"] = status
	res := a.DB.Model(&models.OccurrenceNotification{}).
		Where("id = ? AND status = ?", n.ID, models.NotificationProcessing).Updates(next)
	if res.RowsAffected == 1 && status != models.NotificationPending {
		a.notificationEvent(n, status, out.reason)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// notificationEvent writes the communication milestone on the case timeline.
func (a *App) notificationEvent(n *models.OccurrenceNotification, status, reason string) {
	ev := models.OccurrenceEvent{
		OrganizationID: n.OrganizationID, OccurrenceID: n.OccurrenceID, Type: models.OccurrenceEventCustomerNotice,
		Content: status,
		Metadata: models.JSONB{"notification_id": n.ID.String(), "status": status, "reason": reason,
			"account": n.WhatsAppAccount, "template": n.TemplateName},
	}
	if err := a.DB.Create(&ev).Error; err != nil {
		return
	}
	// Live on the timeline for whoever may see the case.
	a.broadcastOccurrence(n.OrganizationID, n.OccurrenceID, websocket.WSMessage{
		Type: websocket.TypeOccurrenceEventCreated,
		Payload: OccurrenceEventResponse{ID: ev.ID, OccurrenceID: ev.OccurrenceID, Type: string(ev.Type),
			Content: ev.Content, Metadata: ev.Metadata, CreatedAt: ev.CreatedAt},
	})
}

// ---------------------------------------------------------------------------
// Provider evidence (status webhooks)
// ---------------------------------------------------------------------------

var notificationRank = map[string]int{
	models.NotificationPending: 0, models.NotificationProcessing: 0, models.NotificationAccepted: 1,
	models.NotificationDelivered: 2, models.NotificationRead: 3,
}

// syncNotificationFromStatus applies a provider status to the notification a
// message belongs to. Called from the status webhook handler for every status
// (duplicates and out-of-order arrivals included): the update is conditional on
// the current state being strictly behind, so a repeat or a late "sent" after
// "read" changes nothing. Only the provider's own evidence moves these states.
func (a *App) syncNotificationFromStatus(msg *models.Message, wamid, statusValue string, errs []WebhookStatusError) {
	idRaw, _ := msg.Metadata["occurrence_notification_id"].(string)
	id, err := uuid.Parse(idRaw)
	if err != nil {
		return
	}
	var target string
	switch statusValue {
	case "sent":
		target = models.NotificationAccepted
	case "delivered":
		target = models.NotificationDelivered
	case "read":
		target = models.NotificationRead
	case "failed":
		target = models.NotificationFailed
	default:
		return
	}
	now := time.Now()
	updates := map[string]any{"status": target}
	if wamid != "" {
		updates["provider_message_id"] = wamid
	}
	var behind []string
	switch target {
	case models.NotificationFailed:
		detail := "provider reported the message as failed"
		if len(errs) > 0 && (errs[0].Message != "" || errs[0].Title != "") {
			detail = errs[0].Message
			if detail == "" {
				detail = errs[0].Title
			}
		}
		updates["last_error"], updates["reason"] = clip(detail, 500), "provider_reported_failure"
		behind = []string{models.NotificationProcessing, models.NotificationUncertain, models.NotificationAccepted, models.NotificationDelivered}
	default:
		rank := notificationRank[target]
		for st, r := range notificationRank {
			if r < rank {
				behind = append(behind, st)
			}
		}
		// A webhook is evidence the provider accepted it: it also resolves an
		// "uncertain" intent, which is how an ambiguous send reconciles itself.
		behind = append(behind, models.NotificationUncertain)
		updates["reason"] = ""
		switch target {
		case models.NotificationAccepted:
			updates["accepted_at"] = now
		case models.NotificationDelivered:
			updates["delivered_at"] = now
		case models.NotificationRead:
			updates["read_at"] = now
		}
	}
	res := a.DB.Model(&models.OccurrenceNotification{}).
		Where("id = ? AND organization_id = ? AND status IN ?", id, msg.OrganizationID, behind).Updates(updates)
	if res.RowsAffected != 1 {
		return
	}
	var n models.OccurrenceNotification
	if a.DB.First(&n, "id = ?", id).Error == nil {
		a.notificationEvent(&n, target, "provider_status")
		if wsMsg := a.WSHub; wsMsg != nil {
			a.broadcastOccurrenceNotification(&n)
		}
	}
}

// broadcastOccurrenceNotification tells whoever may see the case that its
// communication changed (the detail view refetches).
func (a *App) broadcastOccurrenceNotification(n *models.OccurrenceNotification) {
	a.broadcastOccurrence(n.OrganizationID, n.OccurrenceID, websocket.WSMessage{
		Type: "occurrence_notification_changed", Payload: map[string]any{"occurrence_id": n.OccurrenceID, "status": n.Status},
	})
}
