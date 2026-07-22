package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// GetContactStatusCounts returns conversation counts by status, scoped to what
// the requesting user can actually see. Only 'new' is reported — it is the only
// count the sidebar surfaces.
func (a *App) GetContactStatusCounts(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	query := a.ScopeToOrg(a.DB, userID, orgID)
	query = a.scopeVisibleConversations(query, userID, orgID)

	var newCount int64
	if err := query.Model(&models.Contact{}).
		Where("contact_status = ?", models.ContactStatusNew).
		Count(&newCount).Error; err != nil {
		a.Log.Error("Failed to count contacts by status", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to count contacts", nil, "")
	}

	return r.SendEnvelope(map[string]any{"new": newCount})
}

// UpdateContactStatusRequest is the body of PUT /contacts/{id}/status.
type UpdateContactStatusRequest struct {
	ContactStatus models.ContactStatus `json:"contact_status"`
}

// UpdateContactStatus manually sets a conversation's service status.
func (a *App) UpdateContactStatus(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	if !a.HasPermission(userID, models.ResourceContacts, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have permission to change contact status", nil, "")
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var req UpdateContactStatusRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	if !req.ContactStatus.IsValid() {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"contact_status must be one of: new, in_progress, resolved", nil, "")
	}

	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}
	if !a.canInteractWithConversation(userID, orgID, contact) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden,
			"You do not have access to this conversation", nil, "")
	}

	// Resolving is a close: it must close the attendance and free the contact,
	// not merely flip a label. Freeing the contact alone would leave the
	// AgentTransfer `active` and still carrying an AgentID — hasActiveAgentTransfer
	// would keep the chatbot out of a conversation that nobody owns.
	if req.ContactStatus == models.ContactStatusResolved {
		if err := a.closeActiveAttendanceForContact(contact, &userID, "manual resolve"); err != nil {
			a.Log.Error("Failed to close attendance on resolve", "error", err,
				"org_id", orgID, "contact_id", contact.ID)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact status", nil, "")
		}
	} else if _, err := a.transitionContactStatus(contact, req.ContactStatus, nil, &userID); err != nil {
		a.Log.Error("Failed to update contact status", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact status", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"id":             contact.ID,
		"contact_status": contact.ContactStatus,
	})
}

// transitionContactStatus moves a contact to a new status, but only if its
// current status is in `from` (an empty `from` allows any origin).
//
// The UPDATE is conditional on the current value, which is what makes
// concurrent triggers safe: an inbound message and an agent reply landing at
// the same instant produce exactly one transition and one broadcast, not two.
//
// actorID is nil for automatic transitions. Only manual transitions produce an
// audit entry — models.AuditLog.UserID is NOT NULL, so there is no valid row to
// write without an actor.
//
// Returns true only when a row actually changed.
func (a *App) transitionContactStatus(
	contact *models.Contact,
	to models.ContactStatus,
	from []models.ContactStatus,
	actorID *uuid.UUID,
) (bool, error) {
	changed, oldStatus, err := a.transitionContactStatusDB(a.DB, contact, to, from)
	if err != nil || !changed {
		return changed, err
	}

	a.notifyContactStatusChange(contact, oldStatus, to, actorID)
	return true, nil
}

// transitionContactStatusDB performs the conditional status UPDATE against db
// — which may be a.DB or an open transaction — with no side effects (no audit
// entry, no broadcast). Callers that run this inside a transaction must call
// notifyContactStatusChange themselves once the transaction has committed:
// firing the audit/broadcast before commit would announce a change that
// might still roll back.
//
// Returns whether a row actually changed and the status the contact held
// before the update (needed by notifyContactStatusChange).
func (a *App) transitionContactStatusDB(
	db *gorm.DB,
	contact *models.Contact,
	to models.ContactStatus,
	from []models.ContactStatus,
) (bool, models.ContactStatus, error) {
	oldStatus := contact.ContactStatus
	if oldStatus == to {
		return false, oldStatus, nil
	}

	q := db.Model(&models.Contact{}).Where("id = ?", contact.ID)
	if len(from) > 0 {
		q = q.Where("contact_status IN ?", from)
	}

	res := q.Update("contact_status", to)
	if res.Error != nil {
		return false, oldStatus, res.Error
	}
	if res.RowsAffected == 0 {
		return false, oldStatus, nil
	}

	contact.ContactStatus = to
	return true, oldStatus, nil
}

// notifyContactStatusChange writes the audit entry (when actorID is set) and
// broadcasts the status change over the websocket. Must only be called after
// the write that produced the change has durably committed.
func (a *App) notifyContactStatusChange(
	contact *models.Contact,
	oldStatus, newStatus models.ContactStatus,
	actorID *uuid.UUID,
) {
	if actorID != nil {
		userName := audit.GetUserName(a.DB, *actorID)
		audit.LogAudit(a.DB, contact.OrganizationID, *actorID, userName,
			"contact", contact.ID, models.AuditActionUpdated,
			map[string]any{"contact_status": string(oldStatus)},
			map[string]any{"contact_status": string(newStatus)},
		)
	}

	if a.WSHub != nil {
		a.WSHub.BroadcastToOrg(contact.OrganizationID, websocket.WSMessage{
			Type: websocket.TypeContactStatusChanged,
			Payload: websocket.ContactStatusChangedPayload{
				ContactID:       contact.ID,
				OldStatus:       string(oldStatus),
				NewStatus:       string(newStatus),
				ChangedByUserID: actorID,
				ChangedAt:       time.Now(),
			},
		})
	}
}

// releaseContact frees a contact at the end of an attendance: it clears the
// relationship manager and marks the conversation resolved, so the next
// inbound message starts a fresh cycle through the flow instead of returning
// to whoever happened to serve it last.
//
// actorID is the user who closed the attendance, or nil for automatic closes
// (SLA, inactivity) — AuditLog.UserID is NOT NULL, so only actor-driven
// closes produce an audit entry.
//
// Idempotent: releasing an already-free contact is a no-op.
//
// Both writes — clearing assigned_user_id and resolving the status — happen
// inside a single transaction. Without that, a failure on the second write
// would leave the contact unassigned but not resolved: an orphaned
// conversation nobody owns and no automatic routing picks up. The audit entry
// and websocket broadcast for the status change fire only after the
// transaction commits, so a rollback never produces a notification for a
// change that did not persist.
func (a *App) releaseContact(contact *models.Contact, actorID *uuid.UUID, reason string) error {
	previousStatus := contact.ContactStatus

	var commit func()
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		fn, err := a.releaseContactTx(tx, contact, actorID, reason)
		commit = fn
		return err
	})
	if err != nil {
		// transitionContactStatusDB updates the in-memory struct as it writes;
		// a rollback must not leave the caller holding a status that never
		// persisted.
		contact.ContactStatus = previousStatus
		return err
	}

	commit()
	return nil
}

// releaseContactTx performs the release writes on the given transaction —
// which may be a.DB or an open transaction — and returns the notifier that
// applies the in-memory changes and fires the audit entry / broadcast.
//
// The notifier must be called only once the transaction has committed:
// announcing the release before commit would broadcast a change that might
// still roll back. Callers that fail the transaction must simply drop it.
//
// Existing to let a close and its release share one transaction: marking a
// transfer closed and then failing to free the contact leaves a conversation
// that no automatic pass will ever revisit (the transfer is no longer active),
// with the contact silently pinned to an agent who is no longer serving it.
func (a *App) releaseContactTx(
	tx *gorm.DB,
	contact *models.Contact,
	actorID *uuid.UUID,
	reason string,
) (func(), error) {
	wasAssigned := contact.AssignedUserID != nil
	if wasAssigned {
		if err := tx.Model(&models.Contact{}).
			Where("id = ?", contact.ID).
			Update("assigned_user_id", nil).Error; err != nil {
			return nil, err
		}
	}

	statusChanged, oldStatus, err := a.transitionContactStatusDB(tx, contact, models.ContactStatusResolved, nil)
	if err != nil {
		return nil, err
	}

	return func() {
		if wasAssigned {
			contact.AssignedUserID = nil
		}
		if statusChanged {
			a.notifyContactStatusChange(contact, oldStatus, models.ContactStatusResolved, actorID)
		}
		a.Log.Info("Contact released", "contact_id", contact.ID, "reason", reason)
	}, nil
}
