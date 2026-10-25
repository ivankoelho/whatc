package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// stageApplies reports whether target is part of the pipeline of a case routed
// to teamID: the organization-wide stages plus that team's own.
func stageApplies(target *models.OccurrenceStage, teamID *uuid.UUID) bool {
	if target.TeamID == nil {
		return true
	}
	return teamID != nil && *target.TeamID == *teamID
}

func stageAllowedFrom(target *models.OccurrenceStage, from uuid.UUID) bool {
	if len(target.AllowedFrom) == 0 {
		return true
	}
	for _, id := range target.AllowedFrom {
		if id == from.String() {
			return true
		}
	}
	return false
}

// ChangeOccurrenceStage moves a case and records the transition.
//
// Every rule lives here, never in the client, and the same endpoint serves old
// and new clients, so none of them can route around it:
//   - the caller must work the case (assignee, or a supervisor of its scope);
//     an unassigned case must be claimed first;
//   - the target must belong to the case's pipeline (org stages + its team's);
//   - a stage may restrict which stages can precede it (allowed_from);
//   - resolved stamps closed_at; leaving resolved reopens (counted);
//   - the write is a compare-and-set on the current stage, so two concurrent
//     moves cannot both win from the same starting point (409 for the loser).
func (a *App) ChangeOccurrenceStage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, true)
	if err != nil {
		return nil
	}

	var req ChangeStageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	stageID, err := uuid.Parse(req.StageID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage_id", nil, "")
	}
	target, err := findByIDAndOrg[models.OccurrenceStage](a.DB, r, stageID, orgID, "Stage")
	if err != nil {
		return nil
	}

	// Resending the current stage is a no-op, not a transition (no spurious
	// "X -> X" event, no restamped closed_at).
	if target.ID == occ.StageID {
		occ.Stage = target
		return r.SendEnvelope(occurrenceToResponse(*occ))
	}

	isAssignee := occ.AssignedUserID != nil && *occ.AssignedUserID == userID
	ownCase := occ.AssignedUserID == nil && occ.TeamID == nil && occ.OpenedByUserID == userID
	if !acc.supervisor && !isAssignee && !ownCase {
		if occ.AssignedUserID == nil {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"Claim the occurrence before changing its stage", nil, "")
		}
		return r.SendErrorEnvelope(fasthttp.StatusForbidden,
			"Only the assignee or a supervisor can change this occurrence's stage", nil, "")
	}

	if !stageApplies(target, occ.TeamID) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity,
			"This stage does not belong to the occurrence's pipeline", nil, "")
	}

	var from models.OccurrenceStage
	if err := a.DB.Where("id = ?", occ.StageID).First(&from).Error; err != nil {
		a.Log.Error("Occurrence points at a missing stage", "occurrence_id", occ.ID, "stage_id", occ.StageID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to change stage", nil, "")
	}
	if !stageAllowedFrom(target, from.ID) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity,
			"This transition is not allowed from the current stage", nil, "")
	}

	toState, fromState := target.EffectiveState(), from.EffectiveState()
	reopened := fromState == models.GeneralStateResolved && toState != models.GeneralStateResolved

	updates := map[string]any{"stage_id": target.ID, "general_state": toState}
	if toState == models.GeneralStateResolved {
		updates["closed_at"] = a.DB.NowFunc()
	} else {
		updates["closed_at"] = nil
	}
	if reopened {
		updates["reopened_count"] = occ.ReopenedCount + 1
	}

	res := a.DB.Model(&models.Occurrence{}).
		Where("id = ? AND organization_id = ? AND stage_id = ?", occ.ID, orgID, from.ID).
		Updates(updates)
	if res.Error != nil {
		a.Log.Error("Failed to change occurrence stage", "error", res.Error)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to change stage", nil, "")
	}
	if res.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			"The occurrence changed stage in the meantime; reload and try again", nil, "")
	}

	a.DB.Preload("Stage").Preload("AssignedUser").Preload("Unit").Preload("Department").Preload("Category").Preload("WhatHappened").Preload("Process").First(occ, occ.ID)
	resp := occurrenceToResponse(*occ)
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{
		Type:    websocket.TypeOccurrenceChanged,
		Payload: resp,
	})

	meta := models.JSONB{"from_stage_id": from.ID.String(), "to_stage_id": target.ID.String(),
		"from_state": fromState, "to_state": toState}
	if reopened {
		meta["reopened"] = true
	}
	stageChangeEvent := models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventStageChange,
		Content:        from.Name + " → " + target.Name,
		Metadata:       meta,
		CreatedByID:    &userID,
	}
	if err := a.DB.Create(&stageChangeEvent).Error; err == nil {
		a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{
			Type: websocket.TypeOccurrenceEventCreated,
			Payload: OccurrenceEventResponse{
				ID:            stageChangeEvent.ID,
				OccurrenceID:  stageChangeEvent.OccurrenceID,
				Type:          string(stageChangeEvent.Type),
				Content:       stageChangeEvent.Content,
				Metadata:      stageChangeEvent.Metadata,
				CreatedByID:   stageChangeEvent.CreatedByID,
				CreatedByName: audit.GetUserName(a.DB, userID),
				CreatedAt:     stageChangeEvent.CreatedAt,
			},
		})
	} else {
		a.Log.Error("Failed to create stage-change event", "error", err)
	}

	if toState == models.GeneralStateResolved {
		// No broadcast: the spec fixes exactly one occurrence_event_created
		// per stage change. The automatic "closed" event still lands on the
		// timeline and shows on the next fetch.
		a.DB.Create(&models.OccurrenceEvent{
			OrganizationID: orgID,
			OccurrenceID:   occ.ID,
			Type:           models.OccurrenceEventClosed,
			CreatedByID:    &userID,
		})
	}

	return r.SendEnvelope(resp)
}
