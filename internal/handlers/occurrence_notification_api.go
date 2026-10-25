package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type notificationView struct {
	models.OccurrenceNotification
	Attempts_ []models.OccurrenceNotificationAttempt `json:"attempt_history"`
	CanRetry  bool                                   `json:"can_retry"`
	CanCancel bool                                   `json:"can_cancel"`
}

// ListOccurrenceNotifications shows the customer communication of a case: the
// account and template used, state, provider message id, failure reason and the
// history of attempts. Whoever may see the case sees it; the buttons it offers
// are the ones the caller may actually use.
func (a *App) ListOccurrenceNotifications(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	var ns []models.OccurrenceNotification
	a.DB.Where("occurrence_id = ? AND organization_id = ?", occ.ID, orgID).Order("created_at").Find(&ns)
	out := make([]notificationView, len(ns))
	mayAct := acc.supervisor || (occ.AssignedUserID != nil && *occ.AssignedUserID == userID)
	for i, n := range ns {
		out[i] = notificationView{OccurrenceNotification: n}
		a.DB.Where("notification_id = ?", n.ID).Order("number, started_at").Find(&out[i].Attempts_)
		out[i].CanRetry = mayAct && retryable(n.Status)
		out[i].CanCancel = mayAct && cancellable(n.Status)
	}
	return r.SendEnvelope(map[string]any{"notifications": out})
}

func retryable(status string) bool {
	switch status {
	case models.NotificationFailed, models.NotificationCancelled, models.NotificationPrepared,
		models.NotificationSkipped, models.NotificationUncertain:
		return true
	}
	return false
}

func cancellable(status string) bool {
	switch status {
	case models.NotificationPending, models.NotificationPrepared, models.NotificationFailed, models.NotificationUncertain:
		return true
	}
	return false
}

type resolveNotificationRequest struct {
	Action string `json:"action"` // retry | cancel
	// ConfirmDuplicateRisk must be true to retry an "uncertain" send: the
	// provider may already have accepted it, so a second message can reach the
	// customer. Never implied.
	ConfirmDuplicateRisk bool `json:"confirm_duplicate_risk"`
}

// ResolveOccurrenceNotification is the controlled intervention for a
// communication that did not complete.
//
//   - retry: a failed/cancelled/prepared/skipped intent goes back to pending
//     (re-snapshotting the configuration when it was not sendable, so a newly
//     activated automation takes effect). An "uncertain" one needs a
//     supervisor and an explicit acknowledgement that a duplicate is possible.
//   - cancel: closes the intent; nothing more is sent.
//
// Allowed to the case's assignee and to supervisors; every use is recorded.
func (a *App) ResolveOccurrenceNotification(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	nid, err := parsePathUUID(r, "nid", "notification")
	if err != nil {
		return nil
	}
	var n models.OccurrenceNotification
	if err := a.DB.Where("id = ? AND occurrence_id = ? AND organization_id = ?", nid, occ.ID, orgID).First(&n).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Notification not found", nil, "")
	}
	isAssignee := occ.AssignedUserID != nil && *occ.AssignedUserID == userID
	if !acc.supervisor && !isAssignee {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only the assignee or a supervisor can act on a customer communication", nil, "")
	}
	var req resolveNotificationRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	switch req.Action {
	case "cancel":
		if !cancellable(n.Status) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "This communication can no longer be cancelled", nil, "")
		}
		res := a.DB.Model(&models.OccurrenceNotification{}).Where("id = ? AND status = ?", n.ID, n.Status).
			Updates(map[string]any{"status": models.NotificationCancelled, "reason": "cancelled_by_user"})
		if res.RowsAffected != 1 {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "The communication changed in the meantime; reload", nil, "")
		}
		a.notificationEvent(&n, models.NotificationCancelled, "cancelled_by_user")
	case "retry":
		if !retryable(n.Status) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "This communication cannot be retried in its current state", nil, "")
		}
		if n.Status == models.NotificationUncertain {
			if !acc.supervisor {
				return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Only a supervisor can retry a send whose result is unknown", nil, "")
			}
			if !req.ConfirmDuplicateRisk {
				return r.SendErrorEnvelope(fasthttp.StatusConflict,
					"The provider may already have accepted this message. Retrying can send a duplicate: confirm with confirm_duplicate_risk", nil, "")
			}
		}
		updates := map[string]any{"status": models.NotificationPending, "reason": "", "last_error": "", "attempts": 0,
			"next_attempt_at": time.Now(), "retry_requested_by_id": userID, "eligible": true}
		if n.Status == models.NotificationPrepared || n.Status == models.NotificationSkipped {
			// It was not sendable: take the configuration as it is now, and only
			// proceed when that one is. Never fall back to anything else.
			var contact models.Contact
			a.DB.Where("id = ? AND organization_id = ?", occ.ContactID, orgID).First(&contact)
			cfg := resolveAutomation(a.DB, orgID, occ.UnitID, occ.TeamID, automationEventForwarded)
			if cfg == nil || !cfg.IsActive || cfg.TemplateName == "" {
				return r.SendErrorEnvelope(fasthttp.StatusConflict, "There is no active automation with a template for this case", nil, "")
			}
			var accN int64
			a.DB.Model(&models.WhatsAppAccount{}).Where("organization_id = ? AND name = ?", orgID, cfg.WhatsAppAccount).Count(&accN)
			if accN == 0 {
				return r.SendErrorEnvelope(fasthttp.StatusConflict, "The configured account does not exist in this organization", nil, "")
			}
			updates["config_id"], updates["whats_app_account"], updates["template_name"] = cfg.ID, cfg.WhatsAppAccount, cfg.TemplateName
			updates["template_language"], updates["fallback_template_name"] = cfg.TemplateLanguage, cfg.FallbackTemplateName
			updates["max_attempts"], updates["retry_backoff_secs"], updates["on_failure"] = cfg.MaxAttempts, cfg.RetryBackoffSecs, cfg.OnFailure
		}
		res := a.DB.Model(&models.OccurrenceNotification{}).Where("id = ? AND status = ?", n.ID, n.Status).Updates(updates)
		if res.RowsAffected != 1 {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "The communication changed in the meantime; reload", nil, "")
		}
		a.notificationEvent(&n, "retry_requested", "requested_by_user")
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "action must be retry or cancel", nil, "")
	}
	a.logAudit(orgID, userID, "occurrence_notification", n.ID, models.AuditActionUpdated,
		map[string]any{"status": n.Status}, map[string]any{"action": req.Action, "duplicate_risk_confirmed": req.ConfirmDuplicateRisk})
	a.broadcastOccurrenceNotification(&n)
	return r.SendEnvelope(map[string]any{"ok": true, "notification_id": uuid.UUID(n.ID)})
}
