package handlers

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// Cases opened before team/unit scoping exist without either. They stay
// restricted (see occurrence_access.go) until an administrator regularizes
// them. This file audits them and offers two ways out, both recorded:
//   - apply the suggestion, only when the evidence is reliable;
//   - set the scope by hand, with a mandatory reason.
//
// Reliable evidence is limited to facts stored on the case's own records:
//   - the team's unit (a team belongs to exactly one unit);
//   - the team of the attendance the case came from (source_transfer_id);
//   - a unit+department pair that matches exactly ONE active team.
//
// Nothing is inferred from the customer's phone or from the assignee's teams.

type scopeSuggestion struct {
	TeamID *uuid.UUID `json:"team_id,omitempty"`
	UnitID *uuid.UUID `json:"unit_id,omitempty"`
	Reason string     `json:"reason"`
}

type scopeAuditRow struct {
	ID               uuid.UUID        `json:"id"`
	ProtocolNumber   string           `json:"protocol_number"`
	Title            string           `json:"title"`
	OpenedAt         time.Time        `json:"opened_at"`
	AssignedUserID   *uuid.UUID       `json:"assigned_user_id,omitempty"`
	SourceTransferID *uuid.UUID       `json:"source_transfer_id,omitempty"`
	TeamID           *uuid.UUID       `json:"team_id,omitempty"`
	UnitID           *uuid.UUID       `json:"unit_id,omitempty"`
	Suggestion       *scopeSuggestion `json:"suggestion,omitempty"`
}

// requireScopeAdmin: regularizing scope decides who can see a case, so it is
// reserved to those who already see every case (supervisors of the whole org).
func (a *App) requireScopeAdmin(r *fastglue.Request) (orgID, userID uuid.UUID, ok bool) {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	if !a.seesAllOccurrences(userID, orgID) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, userID, true
}

type teamIndex struct {
	byID      map[uuid.UUID]models.Team
	byUnitDep map[[2]uuid.UUID][]models.Team
}

func (a *App) loadTeamIndex(orgID uuid.UUID) teamIndex {
	var teams []models.Team
	a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).Find(&teams)
	idx := teamIndex{byID: map[uuid.UUID]models.Team{}, byUnitDep: map[[2]uuid.UUID][]models.Team{}}
	for _, t := range teams {
		idx.byID[t.ID] = t
		if t.UnitID != nil && t.DepartmentID != nil {
			k := [2]uuid.UUID{*t.UnitID, *t.DepartmentID}
			idx.byUnitDep[k] = append(idx.byUnitDep[k], t)
		}
	}
	return idx
}

// suggestScope returns a reliable scope for a case, or nil.
func (a *App) suggestScope(orgID uuid.UUID, occ *models.Occurrence, idx teamIndex) *scopeSuggestion {
	if occ.TeamID != nil {
		t, ok := idx.byID[*occ.TeamID]
		if ok && occ.UnitID == nil && t.UnitID != nil {
			return &scopeSuggestion{UnitID: t.UnitID, Reason: "team_unit"}
		}
		return nil
	}
	if occ.SourceTransferID != nil {
		if tid := a.teamFromTransfer(orgID, *occ.SourceTransferID); tid != nil {
			s := &scopeSuggestion{TeamID: tid, Reason: "source_transfer_team"}
			if t, ok := idx.byID[*tid]; ok && occ.UnitID == nil {
				s.UnitID = t.UnitID
			}
			return s
		}
	}
	if occ.UnitID != nil && occ.DepartmentID != nil {
		if ts := idx.byUnitDep[[2]uuid.UUID{*occ.UnitID, *occ.DepartmentID}]; len(ts) == 1 {
			id := ts[0].ID
			return &scopeSuggestion{TeamID: &id, Reason: "unit_department_single_team"}
		}
	}
	return nil
}

// GetOccurrenceScopeAudit reports the cases whose scope is incomplete.
func (a *App) GetOccurrenceScopeAudit(r *fastglue.Request) error {
	orgID, _, ok := a.requireScopeAdmin(r)
	if !ok {
		return nil
	}
	pg := parsePaginationWithDefaults(r, 50, 200)
	idx := a.loadTeamIndex(orgID)
	only := string(r.RequestCtx.QueryArgs().Peek("only")) // "restricted" (default) | "incomplete"

	base := func() *gorm.DB {
		return a.DB.Model(&models.Occurrence{}).Where("organization_id = ?", orgID)
	}
	var total, noTeam, noUnit, restricted, restrictedAssigned, restrictedLinked int64
	base().Count(&total)
	base().Where("team_id IS NULL").Count(&noTeam)
	base().Where("unit_id IS NULL").Count(&noUnit)
	base().Where("team_id IS NULL AND unit_id IS NULL").Count(&restricted)
	base().Where("team_id IS NULL AND unit_id IS NULL AND assigned_user_id IS NOT NULL").Count(&restrictedAssigned)
	base().Where("team_id IS NULL AND unit_id IS NULL AND source_transfer_id IS NOT NULL").Count(&restrictedLinked)

	listQ := base().Where("team_id IS NULL AND unit_id IS NULL")
	if only == "incomplete" {
		listQ = base().Where("team_id IS NULL OR unit_id IS NULL")
	}
	var matches int64
	listQ.Count(&matches)
	var occs []models.Occurrence
	if err := listQ.Order("opened_at ASC").Limit(pg.Limit).Offset(pg.Offset).Find(&occs).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to audit occurrences", nil, "")
	}
	rows := make([]scopeAuditRow, len(occs))
	for i := range occs {
		o := &occs[i]
		rows[i] = scopeAuditRow{ID: o.ID, ProtocolNumber: o.ProtocolNumber, Title: o.Title, OpenedAt: o.OpenedAt,
			AssignedUserID: o.AssignedUserID, SourceTransferID: o.SourceTransferID, TeamID: o.TeamID, UnitID: o.UnitID,
			Suggestion: a.suggestScope(orgID, o, idx)}
	}

	// Determinable over the whole backlog, not only this page.
	var determinable int64
	var batch []models.Occurrence
	a.DB.Where("organization_id = ? AND (team_id IS NULL OR unit_id IS NULL)", orgID).
		FindInBatches(&batch, 500, func(_ *gorm.DB, _ int) error {
			for i := range batch {
				if a.suggestScope(orgID, &batch[i], idx) != nil {
					determinable++
				}
			}
			return nil
		})

	return r.SendEnvelope(map[string]any{
		"summary": map[string]int64{
			"total": total, "without_team": noTeam, "without_unit": noUnit, "restricted": restricted,
			"restricted_with_assignee": restrictedAssigned, "restricted_linked_to_conversation": restrictedLinked,
			"determinable": determinable,
		},
		"occurrences": rows, "total": matches, "page": pg.Page, "limit": pg.Limit,
	})
}

type applyScopeRequest struct {
	OccurrenceIDs []string `json:"occurrence_ids"`
}

// ApplyOccurrenceScopeSuggestions regularizes the listed cases using only
// their reliable suggestion. Cases without one are reported as skipped.
func (a *App) ApplyOccurrenceScopeSuggestions(r *fastglue.Request) error {
	orgID, userID, ok := a.requireScopeAdmin(r)
	if !ok {
		return nil
	}
	var req applyScopeRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if len(req.OccurrenceIDs) == 0 || len(req.OccurrenceIDs) > 500 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "occurrence_ids must have 1 to 500 ids", nil, "")
	}
	idx := a.loadTeamIndex(orgID)
	applied, skipped := 0, 0
	for _, raw := range req.OccurrenceIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			skipped++
			continue
		}
		var occ models.Occurrence
		if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&occ).Error; err != nil {
			skipped++
			continue
		}
		s := a.suggestScope(orgID, &occ, idx)
		if s == nil || !a.applyScope(orgID, userID, &occ, s.TeamID, s.UnitID, "suggestion:"+s.Reason) {
			skipped++
			continue
		}
		applied++
	}
	return r.SendEnvelope(map[string]int{"applied": applied, "skipped": skipped})
}

type setScopeRequest struct {
	TeamID string `json:"team_id"`
	UnitID string `json:"unit_id"`
	Reason string `json:"reason"`
}

// SetOccurrenceScope is the manual regularization: an administrator names the
// team (and unit) of a case and says why. It only fills what is missing; a
// case that already has a team is moved with the normal reassignment flow.
func (a *App) SetOccurrenceScope(r *fastglue.Request) error {
	orgID, userID, ok := a.requireScopeAdmin(r)
	if !ok {
		return nil
	}
	occID, err := parsePathUUID(r, "id", "occurrence")
	if err != nil {
		return nil
	}
	occ, err := findByIDAndOrg[models.Occurrence](a.DB, r, occID, orgID, "Occurrence")
	if err != nil {
		return nil
	}
	var req setScopeRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if strings.TrimSpace(req.Reason) == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "reason is required", nil, "")
	}
	if occ.TeamID != nil {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This occurrence already has a team", nil, "")
	}
	var teamID, unitID *uuid.UUID
	if req.TeamID != "" {
		id, err := uuid.Parse(req.TeamID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid team_id", nil, "")
		}
		team, err := findByIDAndOrg[models.Team](a.DB, r, id, orgID, "Team")
		if err != nil {
			return nil
		}
		if !team.IsActive {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Team is inactive", nil, "")
		}
		teamID = &id
		unitID = team.UnitID
	}
	if req.UnitID != "" {
		id, err := uuid.Parse(req.UnitID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		}
		if _, err := findByIDAndOrg[models.Unit](a.DB, r, id, orgID, "Unit"); err != nil {
			return nil
		}
		if unitID != nil && *unitID != id {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "unit_id does not match the team's unit", nil, "")
		}
		unitID = &id
	}
	if teamID == nil && unitID == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "team_id or unit_id is required", nil, "")
	}
	if !a.applyScope(orgID, userID, occ, teamID, unitID, "manual: "+strings.TrimSpace(req.Reason)) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Nothing to regularize", nil, "")
	}
	return r.SendEnvelope(map[string]any{"regularized": true})
}

// applyScope fills the missing team/unit with a conditional update (never
// overwrites a value set meanwhile) and writes the audit trail.
func (a *App) applyScope(orgID, userID uuid.UUID, occ *models.Occurrence, teamID, unitID *uuid.UUID, reason string) bool {
	updates := map[string]any{}
	if teamID != nil && occ.TeamID == nil {
		updates["team_id"] = *teamID
	}
	if unitID != nil && occ.UnitID == nil {
		updates["unit_id"] = *unitID
	}
	if len(updates) == 0 {
		return false
	}
	q := a.DB.Model(&models.Occurrence{}).Where("id = ? AND organization_id = ?", occ.ID, orgID)
	if _, ok := updates["team_id"]; ok {
		q = q.Where("team_id IS NULL")
	}
	if _, ok := updates["unit_id"]; ok {
		q = q.Where("unit_id IS NULL")
	}
	res := q.Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		return false
	}
	meta := models.JSONB{"reason": reason}
	if v, ok := updates["team_id"]; ok {
		meta["team_id"] = v.(uuid.UUID).String()
	}
	if v, ok := updates["unit_id"]; ok {
		meta["unit_id"] = v.(uuid.UUID).String()
	}
	a.DB.Create(&models.OccurrenceEvent{OrganizationID: orgID, OccurrenceID: occ.ID,
		Type: models.OccurrenceEventScopeRegularized, Content: reason, Metadata: meta, CreatedByID: &userID})
	a.logAudit(orgID, userID, "occurrence", occ.ID, models.AuditActionUpdated,
		map[string]any{"team_id": occ.TeamID, "unit_id": occ.UnitID}, updates)
	return true
}
