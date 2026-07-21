package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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
	query = a.scopeAssignedContact(query, userID, orgID)

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

	// Resolving is a close: it must free the contact, not merely flip a label.
	if req.ContactStatus == models.ContactStatusResolved {
		if err := a.releaseContact(contact, &userID, "manual resolve"); err != nil {
			a.Log.Error("Failed to release contact", "error", err)
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
	oldStatus := contact.ContactStatus
	if oldStatus == to {
		return false, nil
	}

	q := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID)
	if len(from) > 0 {
		q = q.Where("contact_status IN ?", from)
	}

	res := q.Update("contact_status", to)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}

	contact.ContactStatus = to

	if actorID != nil {
		userName := audit.GetUserName(a.DB, *actorID)
		audit.LogAudit(a.DB, contact.OrganizationID, *actorID, userName,
			"contact", contact.ID, models.AuditActionUpdated,
			map[string]any{"contact_status": string(oldStatus)},
			map[string]any{"contact_status": string(to)},
		)
	}

	if a.WSHub != nil {
		a.WSHub.BroadcastToOrg(contact.OrganizationID, websocket.WSMessage{
			Type: websocket.TypeContactStatusChanged,
			Payload: websocket.ContactStatusChangedPayload{
				ContactID:       contact.ID,
				OldStatus:       string(oldStatus),
				NewStatus:       string(to),
				ChangedByUserID: actorID,
				ChangedAt:       time.Now(),
			},
		})
	}

	return true, nil
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
func (a *App) releaseContact(contact *models.Contact, actorID *uuid.UUID, reason string) error {
	if contact.AssignedUserID != nil {
		if err := a.DB.Model(&models.Contact{}).
			Where("id = ?", contact.ID).
			Update("assigned_user_id", nil).Error; err != nil {
			return err
		}
		contact.AssignedUserID = nil
	}

	if _, err := a.transitionContactStatus(contact, models.ContactStatusResolved, nil, actorID); err != nil {
		return err
	}

	a.Log.Info("Contact released", "contact_id", contact.ID, "reason", reason)
	return nil
}
