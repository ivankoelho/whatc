package handlers

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

// OccurrenceStageRequest is the create/update body for a pipeline stage.
type OccurrenceStageRequest struct {
	Name      string `json:"name"`
	Color     string `json:"color"`
	Position  int    `json:"position"`
	IsInitial bool   `json:"is_initial"`
	IsClosing bool   `json:"is_closing"`

	// GeneralState is one of new, in_progress, waiting_customer, resolved. Old
	// clients omit it; it is then derived from is_initial/is_closing.
	GeneralState string `json:"general_state"`
	// TeamID scopes the stage to one team (set at creation only).
	TeamID *string `json:"team_id"`
	// AllowedFrom restricts which stages may precede this one (empty = any).
	AllowedFrom []string `json:"allowed_from"`
}

// ListOccurrenceStages returns the org's pipeline, seeding defaults on first use.
// Gated on occurrences:read: reading stage names is part of using the CRM, not
// the administrative permission that governs editing the pipeline.
func (a *App) ListOccurrenceStages(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	if err := a.ensureDefaultStages(orgID); err != nil {
		a.Log.Error("Failed to seed occurrence stages", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Failed to load stages", nil, "")
	}

	// Organization-wide stages for everyone; a team's own stages only to its
	// members and to those who administer the pipeline or supervise everything.
	q := a.DB.Where("organization_id = ?", orgID)
	seesAll := a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) ||
		a.HasPermission(userID, models.ResourceOccurrenceStages, models.ActionWrite, orgID)
	if !seesAll {
		q = q.Where("team_id IS NULL OR team_id IN (?)", a.myTeamIDs(userID))
	}
	if t := string(r.RequestCtx.QueryArgs().Peek("team_id")); t != "" {
		q = q.Where("team_id IS NULL OR team_id = ?", t)
	}
	var stages []models.OccurrenceStage
	if err := q.Order("position ASC").Find(&stages).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Failed to load stages", nil, "")
	}

	return r.SendEnvelope(map[string]any{"stages": stages})
}

// CreateOccurrenceStage adds a stage. Administering the pipeline lives under
// occurrences.stages, separate from settings.general so that configuring the
// funnel does not require the organisation's general settings.
func (a *App) CreateOccurrenceStage(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceStages, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req OccurrenceStageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}
	// A stage that is both initial and closing would birth every new
	// occurrence already closed with closed_at unset — CreateOccurrence never
	// stamps it — so the open filter and the UI badge would disagree forever.
	if req.IsInitial && req.IsClosing {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"A stage cannot be both initial and closing", nil, "")
	}

	var teamID *uuid.UUID
	if req.TeamID != nil && *req.TeamID != "" {
		id, err := uuid.Parse(*req.TeamID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid team_id", nil, "")
		}
		if _, err := findByIDAndOrg[models.Team](a.DB, r, id, orgID, "Team"); err != nil {
			return nil
		}
		teamID = &id
	}
	state, serr := resolveStageState(&req, nil)
	if serr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, serr.Error(), nil, "")
	}
	if serr := a.validateAllowedFrom(orgID, uuid.Nil, req.AllowedFrom); serr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, serr.Error(), nil, "")
	}

	stage := models.OccurrenceStage{
		OrganizationID: orgID,
		Name:           req.Name,
		Color:          req.Color,
		Position:       req.Position,
		IsInitial:      req.IsInitial,
		IsClosing:      req.IsClosing,
		TeamID:         teamID,
		GeneralState:   state,
		AllowedFrom:    req.AllowedFrom,
	}

	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if req.IsInitial {
			if err := unsetOtherInitial(tx, orgID, uuid.Nil, teamID); err != nil {
				return err
			}
		}
		return tx.Create(&stage).Error
	})
	if err != nil {
		if isUniqueNameViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"A stage with this name already exists", nil, "")
		}
		a.Log.Error("Failed to create occurrence stage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Failed to create stage", nil, "")
	}

	return r.SendEnvelope(stage)
}

// UpdateOccurrenceStage edits a stage, keeping exactly one initial stage and
// refusing to leave the org without a closing stage.
func (a *App) UpdateOccurrenceStage(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceStages, models.ActionWrite)
	if err != nil {
		return nil
	}

	stageID, err := parsePathUUID(r, "id", "stage")
	if err != nil {
		return nil
	}

	stage, err := findByIDAndOrg[models.OccurrenceStage](a.DB, r, stageID, orgID, "Stage")
	if err != nil {
		return nil
	}

	var req OccurrenceStageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}
	// Same as CreateOccurrenceStage: initial + closing at once births cases
	// that are closed on arrival without a closed_at, so open filter and badge
	// disagree.
	if req.IsInitial && req.IsClosing {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"A stage cannot be both initial and closing", nil, "")
	}

	// Unsetting the sole initial stage without designating a replacement would
	// leave the org with none — initialStage() would then fail every new
	// occurrence.
	if stage.TeamID == nil && stage.IsInitial && !req.IsInitial {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			"An initial stage is required; mark another stage as initial instead", nil, "")
	}

	// A stage's scope is fixed at creation: moving it would strand the cases
	// sitting in it outside the pipeline that now owns it.
	if req.TeamID != nil {
		same := (*req.TeamID == "" && stage.TeamID == nil) ||
			(stage.TeamID != nil && *req.TeamID == stage.TeamID.String())
		if !same {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
				"A stage's team cannot be changed", nil, "")
		}
	}
	state, serr := resolveStageState(&req, stage)
	if serr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, serr.Error(), nil, "")
	}
	if serr := a.validateAllowedFrom(orgID, stageID, req.AllowedFrom); serr != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, serr.Error(), nil, "")
	}

	// Clearing the last closing stage would make it impossible to close any
	// occurrence at all.
	if stage.TeamID == nil && stage.IsClosing && !req.IsClosing {
		hasOther, err := hasOtherClosingStage(a.DB, orgID, stageID)
		if err != nil {
			a.Log.Error("Failed to check closing stages", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
				"Failed to update stage", nil, "")
		}
		if !hasOther {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"At least one closing stage is required", nil, "")
		}
	}

	allowedJSON := "[]"
	if len(req.AllowedFrom) > 0 {
		b, _ := json.Marshal(req.AllowedFrom)
		allowedJSON = string(b)
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if req.IsInitial {
			if err := unsetOtherInitial(tx, orgID, stageID, stage.TeamID); err != nil {
				return err
			}
		}
		if err := tx.Model(stage).Updates(map[string]any{
			"name":          req.Name,
			"color":         req.Color,
			"position":      req.Position,
			"is_initial":    req.IsInitial,
			"is_closing":    req.IsClosing,
			"general_state": state,
			"allowed_from":  gorm.Expr("?::jsonb", allowedJSON),
		}).Error; err != nil {
			return err
		}
		// Cases sitting in the stage follow its (possibly new) general state,
		// so counters and boards never disagree with the pipeline.
		return tx.Model(&models.Occurrence{}).
			Where("organization_id = ? AND stage_id = ?", orgID, stageID).
			Update("general_state", state).Error
	})
	if err != nil {
		if isUniqueNameViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"A stage with this name already exists", nil, "")
		}
		a.Log.Error("Failed to update occurrence stage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Failed to update stage", nil, "")
	}

	return r.SendEnvelope(stage)
}

// DeleteOccurrenceStage removes a stage only when nothing depends on it.
func (a *App) DeleteOccurrenceStage(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceStages, models.ActionDelete)
	if err != nil {
		return nil
	}

	stageID, err := parsePathUUID(r, "id", "stage")
	if err != nil {
		return nil
	}

	stage, err := findByIDAndOrg[models.OccurrenceStage](a.DB, r, stageID, orgID, "Stage")
	if err != nil {
		return nil
	}

	var inUse int64
	if err := a.DB.Model(&models.Occurrence{}).
		Where("organization_id = ? AND stage_id = ?", orgID, stageID).Count(&inUse).Error; err != nil {
		a.Log.Error("Failed to check stage usage", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Failed to delete stage", nil, "")
	}
	if inUse > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			"Stage is in use by existing occurrences", nil, "")
	}

	if stage.IsInitial {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			"Cannot delete the initial stage", nil, "")
	}

	if stage.IsClosing {
		hasOther, err := hasOtherClosingStage(a.DB, orgID, stageID)
		if err != nil {
			a.Log.Error("Failed to check closing stages", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
				"Failed to delete stage", nil, "")
		}
		if !hasOther {
			return r.SendErrorEnvelope(fasthttp.StatusConflict,
				"At least one closing stage is required", nil, "")
		}
	}

	if err := a.DB.Delete(stage).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError,
			"Failed to delete stage", nil, "")
	}

	return r.SendEnvelope(map[string]any{"deleted": true})
}

// unsetOtherInitial clears is_initial everywhere except keepID.
func unsetOtherInitial(tx *gorm.DB, orgID, keepID uuid.UUID, teamID *uuid.UUID) error {
	scope := tx.Model(&models.OccurrenceStage{}).Where("organization_id = ? AND id <> ?", orgID, keepID)
	if teamID == nil {
		scope = scope.Where("team_id IS NULL")
	} else {
		scope = scope.Where("team_id = ?", *teamID)
	}
	return scope.Update("is_initial", false).Error
}

// hasOtherClosingStage reports whether the org keeps at least one closing stage
// besides excludeID. An occurrence can only be concluded by entering a closing
// stage, so the last one must never be removable.
func hasOtherClosingStage(db *gorm.DB, orgID, excludeID uuid.UUID) (bool, error) {
	var closing int64
	err := db.Model(&models.OccurrenceStage{}).
		Where("organization_id = ? AND team_id IS NULL AND is_closing = ? AND id <> ?", orgID, true, excludeID).
		Count(&closing).Error
	return closing > 0, err
}

// isUniqueNameViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — specifically the (organization_id, name) index
// on occurrence_stages. Both create and rename can race a concurrent writer
// past an application-level pre-check, so the DB constraint is the only
// reliable signal; without translating it here, callers would leak a raw
// database error as a 500 instead of a clean 409.
func isUniqueNameViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// resolveStageState fixes the stage's general state and keeps the legacy
// flags consistent with it. Clients that predate general_state send only the
// flags; the state is then derived (or kept, on update). is_closing always
// mirrors "resolved", so closed_at and the state can never disagree.
func resolveStageState(req *OccurrenceStageRequest, current *models.OccurrenceStage) (string, error) {
	state := req.GeneralState
	if state == "" {
		switch {
		case req.IsClosing:
			state = models.GeneralStateResolved
		case req.IsInitial:
			state = models.GeneralStateNew
		case current != nil:
			state = current.EffectiveState()
			if state == models.GeneralStateResolved || state == models.GeneralStateNew {
				state = models.GeneralStateInProgress
			}
		default:
			state = models.GeneralStateInProgress
		}
	}
	if !models.IsValidGeneralState(state) {
		return "", errors.New("general_state must be new, in_progress, waiting_customer or resolved")
	}
	req.IsClosing = state == models.GeneralStateResolved
	if req.IsInitial && state != models.GeneralStateNew {
		return "", errors.New("an initial stage must have the general state \"new\"")
	}
	return state, nil
}

// validateAllowedFrom checks that every referenced stage exists in the org and
// that a stage does not list itself.
func (a *App) validateAllowedFrom(orgID, selfID uuid.UUID, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	parsed := make([]uuid.UUID, 0, len(ids))
	for _, s := range ids {
		id, err := uuid.Parse(s)
		if err != nil || id == selfID {
			return errors.New("allowed_from must list other stages' ids")
		}
		parsed = append(parsed, id)
	}
	var n int64
	a.DB.Model(&models.OccurrenceStage{}).Where("organization_id = ? AND id IN ?", orgID, parsed).Count(&n)
	if int(n) != len(parsed) {
		return errors.New("allowed_from references an unknown stage")
	}
	return nil
}
