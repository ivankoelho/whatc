package handlers

import (
	"time"

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
	Role         string   `json:"role"`          // support | follower (default follower)
	DepartmentID string   `json:"department_id"` // the department the person was picked from
}

type participantResponse struct {
	UserID         uuid.UUID  `json:"user_id"`
	UserName       string     `json:"user_name"`
	Capabilities   []string   `json:"capabilities"`
	Role           string     `json:"role"`
	DepartmentID   *uuid.UUID `json:"department_id,omitempty"`
	DepartmentName string     `json:"department_name,omitempty"`
	AddedBy        string     `json:"added_by,omitempty"`
	AddedAt        time.Time  `json:"added_at"`
	Active         bool       `json:"active"`
	RemovedBy      string     `json:"removed_by,omitempty"`
	RemovedAt      *time.Time `json:"removed_at,omitempty"`
}

func (a *App) participantViews(rows []models.OccurrenceParticipant) []participantResponse {
	var ids []uuid.UUID
	var deps []uuid.UUID
	for _, p := range rows {
		ids = append(ids, p.UserID)
		if p.AddedByID != nil {
			ids = append(ids, *p.AddedByID)
		}
		if p.RemovedByID != nil {
			ids = append(ids, *p.RemovedByID)
		}
		if p.DepartmentID != nil {
			deps = append(deps, *p.DepartmentID)
		}
	}
	names := a.chatUserNames(ids)
	depNames := map[uuid.UUID]string{}
	if len(deps) > 0 {
		var ds []models.Department
		a.DB.Unscoped().Select("id, name").Where("id IN ?", deps).Find(&ds)
		for _, d := range ds {
			depNames[d.ID] = d.Name
		}
	}
	out := make([]participantResponse, len(rows))
	for i, p := range rows {
		caps := p.Capabilities
		if caps == nil {
			caps = []string{}
		}
		role := p.Role
		if role == "" {
			role = models.ParticipantRoleFollower
		}
		v := participantResponse{UserID: p.UserID, UserName: names[p.UserID], Capabilities: caps, Role: role, DepartmentID: p.DepartmentID,
			AddedAt: p.CreatedAt, Active: !p.DeletedAt.Valid}
		if p.DepartmentID != nil {
			v.DepartmentName = depNames[*p.DepartmentID]
		}
		if p.AddedByID != nil {
			v.AddedBy = names[*p.AddedByID]
		}
		if p.DeletedAt.Valid {
			t := p.DeletedAt.Time
			v.RemovedAt = &t
			if p.RemovedByID != nil {
				v.RemovedBy = names[*p.RemovedByID]
			}
		}
		out[i] = v
	}
	return out
}

// ListOccurrenceParticipants returns who follows or supports the case, what each may do, and the history of
// the link (who added, who removed, when). Seeing the list needs only the right to read the case.
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
	a.DB.Unscoped().Where("occurrence_id = ? AND organization_id = ?", occ.ID, orgID).Order("created_at").Find(&rows)
	return r.SendEnvelope(map[string]any{"participants": a.participantViews(rows)})
}

// supportDepartmentsFor: the departments a person may pick support from. A supervisor of the case may use
// any active department of the organization; anyone else only their own department and the case team's.
func (a *App) supportDepartmentsFor(orgID, userID uuid.UUID, occ *models.Occurrence, supervisor bool) []models.Department {
	q := a.DB.Where("organization_id = ? AND active = ?", orgID, true).Order("name")
	if !supervisor {
		var allowed []uuid.UUID
		var me models.User
		if a.DB.Select("id, department_id").First(&me, "id = ?", userID).Error == nil && me.DepartmentID != nil {
			allowed = append(allowed, *me.DepartmentID)
		}
		if occ.TeamID != nil {
			var team models.Team
			if a.DB.Select("id, department_id").First(&team, "id = ? AND organization_id = ?", *occ.TeamID, orgID).Error == nil && team.DepartmentID != nil {
				allowed = append(allowed, *team.DepartmentID)
			}
		}
		if len(allowed) == 0 {
			return nil
		}
		q = q.Where("id IN ?", allowed)
	}
	var ds []models.Department
	q.Find(&ds)
	return ds
}

// ListSupportOptions answers the two pickers of "Acompanhamento e apoio": with no department_id, the
// departments the caller may choose from; with one, ONLY the active users linked to that department.
func (a *App) ListSupportOptions(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, true)
	if err != nil {
		return nil
	}
	deps := a.supportDepartmentsFor(orgID, userID, occ, acc.supervisor)
	dep := string(r.RequestCtx.QueryArgs().Peek("department_id"))
	if dep == "" {
		type d struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		}
		out := make([]d, len(deps))
		for i, x := range deps {
			out[i] = d{x.ID, x.Name}
		}
		return r.SendEnvelope(map[string]any{"departments": out})
	}
	depID, perr := uuid.Parse(dep)
	allowed := false
	for _, x := range deps {
		allowed = allowed || (perr == nil && x.ID == depID)
	}
	if !allowed {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot pick support from this department", nil, "")
	}
	var users []models.User
	a.DB.Select("id, full_name").Where("organization_id = ? AND department_id = ? AND is_active = ?", orgID, depID, true).Order("full_name").Find(&users)
	type u struct {
		ID       uuid.UUID `json:"id"`
		FullName string    `json:"full_name"`
	}
	out := make([]u, len(users))
	for i, x := range users {
		out[i] = u{x.ID, x.FullName}
	}
	return r.SendEnvelope(map[string]any{"users": out})
}

// AddOccurrenceParticipant lets someone follow or support the case. Anyone who works the case may invite a
// colleague to follow and comment; granting more (status, claim, reassign, resolve) is a supervisor's call.
// A department may be named: the person must be ACTIVE and linked to it, and the department must be one the
// caller may pick from. Being on the case grants no access to the customer's WhatsApp or to restricted
// files. Re-adding updates the link (and revives a removed one).
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
	role := req.Role
	if role == "" {
		role = models.ParticipantRoleFollower
	}
	if role != models.ParticipantRoleSupport && role != models.ParticipantRoleFollower {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "role must be support or follower", nil, "")
	}
	var tu models.User
	if a.DB.Select("id, full_name, is_active, department_id").Where("organization_id = ? AND id = ?", orgID, target).First(&tu).Error != nil {
		// a member of the organization through the membership table is also valid
		var member int64
		a.DB.Model(&models.UserOrganization{}).Where("organization_id = ? AND user_id = ?", orgID, target).Count(&member)
		if member == 0 || a.DB.Select("id, full_name, is_active, department_id").First(&tu, "id = ?", target).Error != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "User not found", nil, "")
		}
	}
	if !tu.IsActive {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "That user is not active", nil, "")
	}
	var depID *uuid.UUID
	depName := ""
	if req.DepartmentID != "" {
		id, perr := uuid.Parse(req.DepartmentID)
		if perr != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
		}
		allowed := false
		for _, d := range a.supportDepartmentsFor(orgID, userID, occ, acc.supervisor) {
			if d.ID == id {
				allowed, depName = true, d.Name
			}
		}
		if !allowed {
			return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You cannot pick support from this department", nil, "")
		}
		if tu.DepartmentID == nil || *tu.DepartmentID != id {
			return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity, "That user does not belong to the department", nil, "")
		}
		depID = &id
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

	actor := a.chatUserNames([]uuid.UUID{userID})[userID]
	part := models.OccurrenceParticipant{OrganizationID: orgID, OccurrenceID: occ.ID, UserID: target, AddedByID: &userID, Capabilities: caps, Role: role, DepartmentID: depID}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "occurrence_id"}, {Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"capabilities", "added_by_id", "role", "department_id", "updated_at", "deleted_at", "removed_by_id"}),
		}).Create(&part).Error; err != nil {
			return err
		}
		roleLabel := map[string]string{models.ParticipantRoleSupport: "apoio", models.ParticipantRoleFollower: "acompanhamento"}[role]
		text := actor + " adicionou " + tu.FullName + " como " + roleLabel
		if depName != "" {
			text += " (" + depName + ")"
		}
		return tx.Create(&models.OccurrenceEvent{
			OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventParticipantAdded,
			Content:     text,
			Metadata:    models.JSONB{"user_id": target.String(), "user_name": tu.FullName, "role": role, "department": depName, "capabilities": caps},
			CreatedByID: &userID,
		}).Error
	})
	if err != nil {
		a.Log.Error("Failed to add occurrence participant", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to add participant", nil, "")
	}
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: occurrenceToResponse(*occ)})
	var saved models.OccurrenceParticipant
	a.DB.Where("occurrence_id = ? AND user_id = ?", occ.ID, target).First(&saved)
	return r.SendEnvelope(a.participantViews([]models.OccurrenceParticipant{saved})[0])
}

// RemoveOccurrenceParticipant ends the link (kept as history, with who and when). A participant may leave on
// their own; removing others needs a hand on the case.
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
	var cur models.OccurrenceParticipant
	if a.DB.Where("occurrence_id = ? AND organization_id = ? AND user_id = ?", occ.ID, orgID, target).First(&cur).Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Participant not found", nil, "")
	}
	a.DB.Model(&cur).Update("removed_by_id", userID)
	if err := a.DB.Delete(&cur).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to remove participant", nil, "")
	}
	names := a.chatUserNames([]uuid.UUID{userID, target})
	a.DB.Create(&models.OccurrenceEvent{
		OrganizationID: orgID, OccurrenceID: occ.ID, Type: models.OccurrenceEventParticipantRemoved,
		Content:  names[userID] + " removeu " + names[target] + " do acompanhamento",
		Metadata: models.JSONB{"user_id": target.String(), "user_name": names[target]}, CreatedByID: &userID,
	})
	a.broadcastOccurrence(orgID, occ.ID, websocket.WSMessage{Type: websocket.TypeOccurrenceChanged, Payload: occurrenceToResponse(*occ)})
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
