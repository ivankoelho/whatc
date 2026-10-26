package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Forwarding and returning a case that already has a team.
//
// Four things are kept apart:
//   - forward:  the main responsibility moves to another team (department), optionally to one person of it;
//   - return:   it goes back to where it came from, with a mandatory reason;
//   - support:  other people help without taking responsibility (participants, see occurrence_participants.go);
//   - follow:   people stay informed (participants too).
// A transfer never creates another protocol and never deletes anything: protocol, files, discussion and the
// whole timeline stay with the case. Access follows the new responsibility (it is derived from team and
// assignee, so the previous team stops seeing the case by itself); the person who handed it over keeps following
// it as a participant who may comment, and nothing here grants WhatsApp, restricted files or view_all.

var (
	errTransferConflict = errors.New("transfer conflict")
	minTransferReason   = 3
)

type forwardRequest struct {
	TeamID         string  `json:"team_id"`
	AssignedUserID *string `json:"assigned_user_id"`
	Reason         string  `json:"reason"`
}

type returnRequest struct {
	Reason string `json:"reason"`
}

// canMoveCase: those who work the case (assignee, members of its team, supervisors of it). A person who only
// follows or supports it cannot hand it over.
func canMoveCase(acc occurrenceAccess) bool { return acc.interact || acc.supervisor }

func (a *App) lastTransfer(orgID, occID uuid.UUID) *models.OccurrenceTransfer {
	var t models.OccurrenceTransfer
	if err := a.DB.Where("organization_id = ? AND occurrence_id = ?", orgID, occID).Order("created_at DESC, id DESC").First(&t).Error; err != nil {
		return nil
	}
	return &t
}

// returnTarget says where a "return" would send the case: the team it came from (nil team = its requester).
// ok is false when there is nothing to return: never forwarded, or the last move already was a return.
func (a *App) returnTarget(orgID uuid.UUID, occ *models.Occurrence) (teamID *uuid.UUID, user *uuid.UUID, ok bool) {
	last := a.lastTransfer(orgID, occ.ID)
	if last == nil {
		// cases forwarded before transfers were recorded: the timeline knows who asked
		var e models.OccurrenceEvent
		if occ.TeamID != nil && a.DB.Where("occurrence_id = ? AND organization_id = ? AND type = ?", occ.ID, orgID, models.OccurrenceEventForwarded).
			Order("created_at ASC").First(&e).Error == nil {
			if id, err := uuid.Parse(strings.TrimSpace(strings.Trim(asString(e.Metadata["requester_id"]), `"`))); err == nil {
				return nil, &id, true
			}
		}
		return nil, nil, false
	}
	if last.Kind != models.TransferKindForward {
		return nil, nil, false
	}
	// the move must still be the current state of the case
	if (last.ToTeamID == nil) != (occ.TeamID == nil) || (last.ToTeamID != nil && occ.TeamID != nil && *last.ToTeamID != *occ.TeamID) {
		return nil, nil, false
	}
	user = last.FromUserID
	return last.FromTeamID, user, true
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

type transferView struct {
	ID           uuid.UUID  `json:"id"`
	Kind         string     `json:"kind"`
	FromTeam     string     `json:"from_team,omitempty"`
	ToTeam       string     `json:"to_team,omitempty"`
	ToUser       string     `json:"to_user,omitempty"`
	By           string     `json:"by"`
	Reason       string     `json:"reason"`
	CreatedAt    time.Time  `json:"created_at"`
	FromTeamID   *uuid.UUID `json:"from_team_id,omitempty"`
	ToTeamID     *uuid.UUID `json:"to_team_id,omitempty"`
}

// ListOccurrenceTransfers is what the case screen needs: the movements, who may move the case, and where a
// return would go.
func (a *App) ListOccurrenceTransfers(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	var rows []models.OccurrenceTransfer
	a.DB.Where("organization_id = ? AND occurrence_id = ?", orgID, occ.ID).Order("created_at ASC, id ASC").Find(&rows)

	teamIDs, userIDs := []uuid.UUID{}, []uuid.UUID{}
	for _, t := range rows {
		for _, id := range []*uuid.UUID{t.FromTeamID, t.ToTeamID} {
			if id != nil {
				teamIDs = append(teamIDs, *id)
			}
		}
		userIDs = append(userIDs, t.ByUserID)
		if t.ToUserID != nil {
			userIDs = append(userIDs, *t.ToUserID)
		}
	}
	teamName := map[uuid.UUID]string{}
	if len(teamIDs) > 0 {
		var ts []models.Team
		a.DB.Select("id, name").Where("id IN ? AND organization_id = ?", teamIDs, orgID).Find(&ts)
		for _, t := range ts {
			teamName[t.ID] = t.Name
		}
	}
	names := a.chatUserNames(userIDs)
	nameOf := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		return teamName[*id]
	}
	out := make([]transferView, len(rows))
	for i, t := range rows {
		v := transferView{ID: t.ID, Kind: t.Kind, FromTeam: nameOf(t.FromTeamID), ToTeam: nameOf(t.ToTeamID), By: names[t.ByUserID],
			Reason: t.Reason, CreatedAt: t.CreatedAt, FromTeamID: t.FromTeamID, ToTeamID: t.ToTeamID}
		if t.ToUserID != nil {
			v.ToUser = names[*t.ToUserID]
		}
		out[i] = v
	}

	closed := occ.GeneralState == models.GeneralStateResolved
	canMove := canMoveCase(acc) && a.HasPermission(userID, models.ResourceOccurrences, models.ActionWrite, orgID) && !closed
	resp := map[string]any{"transfers": out, "can_forward": canMove}
	if tid, _, ok := a.returnTarget(orgID, occ); ok && canMove {
		resp["can_return"] = true
		name := ""
		if tid != nil {
			name = teamName[*tid]
			if name == "" {
				a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", *tid, orgID).Select("name").Scan(&name)
			}
		}
		resp["return_to"] = name // empty: back to the requester
	} else {
		resp["can_return"] = false
	}
	return r.SendEnvelope(resp)
}

// ForwardOccurrence hands the main responsibility to another team.
func (a *App) ForwardOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, true)
	if err != nil {
		return nil
	}
	if !canMoveCase(acc) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot forward this occurrence", nil, "")
	}
	var req forwardRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < minTransferReason {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A reason is required", nil, "")
	}
	teamID, perr := uuid.Parse(req.TeamID)
	if perr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid team_id", nil, "")
	}
	var team models.Team
	if a.DB.Where("id = ? AND organization_id = ?", teamID, orgID).First(&team).Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Team not found", nil, "")
	}
	if !team.IsActive {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "The destination team is inactive", nil, "")
	}
	if occ.TeamID != nil && *occ.TeamID == team.ID {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "The occurrence is already with this team", nil, "")
	}
	if occ.GeneralState == models.GeneralStateResolved {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Reopen the occurrence before forwarding it", nil, "")
	}
	var toUser *uuid.UUID
	if req.AssignedUserID != nil && *req.AssignedUserID != "" {
		id, perr := uuid.Parse(*req.AssignedUserID)
		if perr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid assigned_user_id", nil, "")
		}
		var u models.User
		if a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&u).Error != nil || !u.IsActive {
			return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "The chosen person is not an active user of this organization", nil, "")
		}
		if !a.userInTeam(id, team.ID) {
			return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "The chosen person does not belong to the destination team", nil, "")
		}
		toUser = &id
	}
	return a.applyTransfer(r, orgID, userID, occ, models.TransferKindForward, &team, toUser, reason)
}

// ReturnOccurrence sends the case back to where it came from, with a mandatory reason.
func (a *App) ReturnOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, true)
	if err != nil {
		return nil
	}
	if !canMoveCase(acc) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot return this occurrence", nil, "")
	}
	var req returnRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < minTransferReason {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A reason is required", nil, "")
	}
	if occ.GeneralState == models.GeneralStateResolved {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Reopen the occurrence before returning it", nil, "")
	}
	teamID, prevUser, ok := a.returnTarget(orgID, occ)
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "There is nothing to return: this occurrence was not forwarded, or it was already returned", nil, "")
	}
	var team *models.Team
	if teamID != nil {
		var tm models.Team
		if a.DB.Where("id = ? AND organization_id = ?", *teamID, orgID).First(&tm).Error != nil || !tm.IsActive {
			return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "The previous team no longer exists or is inactive; forward the occurrence to another team", nil, "")
		}
		team = &tm
	}
	// the person who held it before gets it back only if still active and still in that team (or, for the
	// requester, always when active); otherwise it returns to the team's queue
	var toUser *uuid.UUID
	if prevUser != nil {
		var u models.User
		if a.DB.Where("id = ? AND organization_id = ?", *prevUser, orgID).First(&u).Error == nil && u.IsActive && (team == nil || a.userInTeam(*prevUser, team.ID)) {
			toUser = prevUser
		}
	}
	return a.applyTransfer(r, orgID, userID, occ, models.TransferKindReturn, team, toUser, reason)
}

// applyTransfer performs the move in one transaction. The update is guarded by the team the request saw, so two
// simultaneous moves cannot both win: the second gets 409 and reloads.
func (a *App) applyTransfer(r *fastglue.Request, orgID, userID uuid.UUID, occ *models.Occurrence, kind string, to *models.Team, toUser *uuid.UUID, reason string) error {
	oldTeam, oldUser := occ.TeamID, occ.AssignedUserID
	var newTeamID, newUnitID *uuid.UUID
	newUnitID = occ.UnitID
	if to != nil {
		newTeamID = &to.ID
		if to.UnitID != nil {
			newUnitID = to.UnitID
		}
	}

	var stageID uuid.UUID = occ.StageID
	var cur models.OccurrenceStage
	if a.DB.Where("id = ?", occ.StageID).First(&cur).Error == nil && cur.TeamID != nil && (newTeamID == nil || *cur.TeamID != *newTeamID) {
		// the current stage belongs to the previous team's own pipeline: enter the new owner's pipeline
		if st, e := a.initialStageFor(orgID, newTeamID); e == nil {
			stageID = st.ID
		}
	}
	var stage models.OccurrenceStage
	_ = a.DB.Where("id = ?", stageID).First(&stage).Error

	err := a.DB.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&models.Occurrence{}).Where("id = ? AND organization_id = ?", occ.ID, orgID)
		if oldTeam == nil {
			q = q.Where("team_id IS NULL")
		} else {
			q = q.Where("team_id = ?", *oldTeam)
		}
		updates := map[string]any{"team_id": newTeamID, "unit_id": newUnitID, "assigned_user_id": toUser, "stage_id": stageID}
		if stageID != occ.StageID {
			updates["general_state"] = stage.EffectiveState()
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTransferConflict
		}
		if err := tx.Create(&models.OccurrenceTransfer{OrganizationID: orgID, OccurrenceID: occ.ID, Kind: kind,
			FromTeamID: oldTeam, ToTeamID: newTeamID, FromUserID: oldUser, ToUserID: toUser, ByUserID: userID, Reason: reason}).Error; err != nil {
			return err
		}
		meta := models.JSONB{"kind": kind, "reason": reason, "to_team_id": ptrStr(newTeamID), "from_team_id": ptrStr(oldTeam)}
		if toUser != nil {
			meta["to_user_id"] = toUser.String()
		}
		typ := models.OccurrenceEventForwarded
		if kind == models.TransferKindReturn {
			typ = models.OccurrenceEventReturned
		}
		if err := tx.Create(&models.OccurrenceEvent{OrganizationID: orgID, OccurrenceID: occ.ID, Type: typ, Content: reason, Metadata: meta, CreatedByID: &userID}).Error; err != nil {
			return err
		}
		// whoever handed the case over (and its previous holder) keeps following it, able to comment, when the
		// new responsibility no longer covers them
		for _, id := range []*uuid.UUID{&userID, oldUser} {
			if id == nil || a.seesAllOccurrences(*id, orgID) || (newTeamID != nil && a.userInTeam(*id, *newTeamID)) || (toUser != nil && *toUser == *id) {
				continue
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "occurrence_id"}, {Name: "user_id"}},
				DoUpdates: clause.Assignments(map[string]any{"deleted_at": nil}),
			}).Create(&models.OccurrenceParticipant{OrganizationID: orgID, OccurrenceID: occ.ID, UserID: *id, AddedByID: &userID,
				Capabilities: []string{models.OccurrenceCapComment}, Role: models.ParticipantRoleFollower}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errTransferConflict) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "The occurrence was moved in the meantime; reload and try again", nil, "")
	}
	if err != nil {
		a.Log.Error("Failed to transfer occurrence", "error", err, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to move the occurrence", nil, "")
	}

	a.DB.Preload("Stage").Preload("AssignedUser").Preload("Unit").Preload("Department").Preload("Category").Preload("WhatHappened").Preload("Process").First(occ, occ.ID)
	resp := occurrenceToResponse(*occ)
	if occ.TeamID != nil {
		a.DB.Model(&models.Team{}).Where("id = ? AND organization_id = ?", *occ.TeamID, orgID).Select("name").Scan(&resp.TeamName)
	}
	// who is told is who can see the case NOW (the hub asks the same authorizer)
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: resp})
	return r.SendEnvelope(resp)
}

func ptrStr(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// recordInitialTransfer is written with the case when it is opened for a team: the "from" is the requester.
func recordInitialTransfer(tx *gorm.DB, occ *models.Occurrence, userID uuid.UUID) error {
	if occ.TeamID == nil {
		return nil
	}
	return tx.Create(&models.OccurrenceTransfer{OrganizationID: occ.OrganizationID, OccurrenceID: occ.ID, Kind: models.TransferKindForward,
		FromUserID: &userID, ToTeamID: occ.TeamID, ByUserID: userID, Reason: "Abertura da ocorrência"}).Error
}
