package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ClaimOccurrence makes the caller the owner of an unassigned occurrence.
//
// The ownership change is a single conditional UPDATE, so when two agents claim
// at once exactly one row matches and the other gets 409 — no read-then-write
// window and no lock held by a browser session. Claiming one's own occurrence
// is an idempotent 200. Taking a case from another agent is a reassignment
// (UpdateOccurrence), not a claim.
func (a *App) ClaimOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}

	if !acc.can(models.OccurrenceCapClaim) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot claim this occurrence", nil, "")
	}

	res := a.DB.Model(&models.Occurrence{}).
		Where("id = ? AND organization_id = ? AND (assigned_user_id IS NULL OR assigned_user_id = ?)",
			occ.ID, orgID, userID).
		Update("assigned_user_id", userID)
	if res.Error != nil {
		a.Log.Error("Failed to claim occurrence", "error", res.Error)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to claim occurrence", nil, "")
	}
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Occurrence is already assigned to another agent", nil, "")
	}

	alreadyMine := occ.AssignedUserID != nil && *occ.AssignedUserID == userID
	a.DB.Preload("Stage").Preload("AssignedUser").Preload("Unit").Preload("Department").Preload("Category").Preload("WhatHappened").Preload("Process").First(occ, occ.ID)
	resp := occurrenceToResponse(*occ)
	if alreadyMine {
		return r.SendEnvelope(resp)
	}

	a.DB.Create(&models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventAssignment,
		CreatedByID:    &userID,
	})
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{
		Type:    websocket.TypeOccurrenceChanged,
		Payload: resp,
	})
	return r.SendEnvelope(resp)
}
