package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

type participantRequest struct {
	UserID       string   `json:"user_id"`
	Capabilities []string `json:"capabilities"`
}

type participantResponse struct {
	UserID       uuid.UUID `json:"user_id"`
	UserName     string    `json:"user_name"`
	Capabilities []string  `json:"capabilities"`
}

// ListOccurrenceParticipants returns who follows the case and what each may do.
func (a *App) ListOccurrenceParticipants(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, _, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	var rows []models.OccurrenceParticipant
	a.DB.Where("occurrence_id = ? AND organization_id = ?", occ.ID, orgID).Order("created_at").Find(&rows)
	out := make([]participantResponse, len(rows))
	for i, p := range rows {
		var u models.User
		a.DB.Select("id, full_name").First(&u, "id = ?", p.UserID)
		caps := p.Capabilities
		if caps == nil {
			caps = []string{}
		}
		out[i] = participantResponse{UserID: p.UserID, UserName: u.FullName, Capabilities: caps}
	}
	return r.SendEnvelope(map[string]any{"participants": out})
}

// AddOccurrenceParticipant lets someone follow the case. Anyone who works the
// case may invite a colleague to follow and comment; granting more (status,
// claim, reassign, resolve) is a supervisor's call. Re-adding updates the
// capabilities.
func (a *App) AddOccurrenceParticipant(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, true)
	if err != nil {
		return nil
	}
	var req participantRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	target, err := uuid.Parse(req.UserID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid user_id", nil, "")
	}
	var member int64
	a.DB.Model(&models.UserOrganization{}).Where("organization_id = ? AND user_id = ?", orgID, target).Count(&member)
	if member == 0 {
		a.DB.Model(&models.User{}).Where("organization_id = ? AND id = ?", orgID, target).Count(&member)
	}
	if member == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "User not found", nil, "")
	}

	caps := make([]string, 0, len(req.Capabilities))
	seen := map[string]bool{}
	for _, c := range req.Capabilities {
		if !models.IsValidOccurrenceCapability(c) {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unknown capability: "+c, nil, "")
		}
		if c != models.OccurrenceCapComment && !acc.supervisor {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden,
				"Only a supervisor can grant "+c, nil, "")
		}
		if !seen[c] {
			seen[c] = true
			caps = append(caps, c)
		}
	}

	part := models.OccurrenceParticipant{OrganizationID: orgID, OccurrenceID: occ.ID, UserID: target, AddedByID: &userID, Capabilities: caps}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "occurrence_id"}, {Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"capabilities", "added_by_id", "updated_at"}),
		}).Create(&part).Error; err != nil {
			return err
		}
		return tx.Create(&models.OccurrenceEvent{
			OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventParticipantAdded,
			Content:     "",
			Metadata:    models.JSONB{"user_id": target.String(), "capabilities": caps},
			CreatedByID: &userID,
		}).Error
	})
	if err != nil {
		a.Log.Error("Failed to add occurrence participant", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to add participant", nil, "")
	}
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: occurrenceToResponse(*occ)})
	return r.SendEnvelope(participantResponse{UserID: target, Capabilities: caps})
}

// RemoveOccurrenceParticipant stops someone following. A participant may leave
// on their own; removing others needs a hand on the case.
func (a *App) RemoveOccurrenceParticipant(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	target, err := parsePathUUID(r, "userId", "user")
	if err != nil {
		return nil
	}
	if target != userID && !acc.interact && !acc.supervisor {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot remove other participants", nil, "")
	}
	res := a.DB.Where("occurrence_id = ? AND organization_id = ? AND user_id = ?", occ.ID, orgID, target).
		Delete(&models.OccurrenceParticipant{})
	if res.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to remove participant", nil, "")
	}
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Participant not found", nil, "")
	}
	a.DB.Create(&models.OccurrenceEvent{
		OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventParticipantRemoved,
		Metadata: models.JSONB{"user_id": target.String()}, CreatedByID: &userID,
	})
	return r.SendEnvelope(map[string]bool{"removed": true})
}

// ---------------------------------------------------------------------------
// Redistribution
// ---------------------------------------------------------------------------

type reassignRequest struct {
	AssignedUserID string `json:"assigned_user_id"`
}

// ReassignOccurrence hands a case to another agent of its team. It is allowed
// to supervisors, to the current assignee, or to a participant granted
// "reassign". The target must belong to the case's team (a supervisor may name
// anyone of the organization), and the change is a compare-and-set on the
// previous assignee so two redistributions cannot both win.
func (a *App) ReassignOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	isAssignee := occ.AssignedUserID != nil && *occ.AssignedUserID == userID
	if !acc.supervisor && !isAssignee && !acc.caps[models.OccurrenceCapReassign] {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot redistribute this occurrence", nil, "")
	}
	var req reassignRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	target, err := uuid.Parse(req.AssignedUserID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid assigned_user_id", nil, "")
	}
	if _, err := a.resolveAssignee(orgID, &req.AssignedUserID); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	if !acc.supervisor && (occ.TeamID == nil || !a.userInTeam(target, *occ.TeamID)) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity,
			"The new assignee must belong to the occurrence's team", nil, "")
	}

	q := a.DB.Model(&models.Occurrence{}).Where("id = ? AND organization_id = ?", occ.ID, orgID)
	if occ.AssignedUserID == nil {
		q = q.Where("assigned_user_id IS NULL")
	} else {
		q = q.Where("assigned_user_id = ?", *occ.AssignedUserID)
	}
	res := q.Update("assigned_user_id", target)
	if res.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to redistribute", nil, "")
	}
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "The assignee changed in the meantime; reload and try again", nil, "")
	}
	meta := models.JSONB{"to_user_id": target.String()}
	if occ.AssignedUserID != nil {
		meta["from_user_id"] = occ.AssignedUserID.String()
	}
	a.DB.Create(&models.OccurrenceEvent{OrganizationID: orgID, OccurrenceID: occ.ID,
		Type: models.OccurrenceEventAssignment, Metadata: meta, CreatedByID: &userID})
	a.DB.Preload("Stage").Preload("AssignedUser").Preload("Unit").Preload("Department").Preload("Category").Preload("WhatHappened").Preload("Process").First(occ, occ.ID)
	resp := occurrenceToResponse(*occ)
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: resp})
	return r.SendEnvelope(resp)
}
