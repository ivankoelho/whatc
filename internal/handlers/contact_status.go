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

	if _, err := a.transitionContactStatus(contact, req.ContactStatus, nil, &userID); err != nil {
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
