package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// defaultSLAPolicies are the organisation-wide, priority-only starting point —
// values the product owner specified directly in the approved spec.
var defaultSLAPolicies = []models.OccurrenceSLAPolicy{
	{Priority: models.OccurrencePriorityLow, ResponseMinutes: 480, ResolutionMinutes: 1440},
	{Priority: models.OccurrencePriorityNormal, ResponseMinutes: 240, ResolutionMinutes: 720},
	{Priority: models.OccurrencePriorityHigh, ResponseMinutes: 120, ResolutionMinutes: 480},
	{Priority: models.OccurrencePriorityUrgent, ResponseMinutes: 30, ResolutionMinutes: 240},
}

// ensureDefaultSLAPolicies seeds the four priority-only policies the first
// time an organisation's SLA is read. Mirrors ensureDefaultStages exactly,
// including the race handling: two concurrent first-reads both attempt the
// insert, and the partial unique index makes the loser's rows a no-op instead
// of a duplicate.
func (a *App) ensureDefaultSLAPolicies(orgID uuid.UUID) error {
	var count int64
	if err := a.DB.Model(&models.OccurrenceSLAPolicy{}).
		Where("organization_id = ? AND department_id IS NULL AND unit_id IS NULL AND category_id IS NULL", orgID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	policies := make([]models.OccurrenceSLAPolicy, len(defaultSLAPolicies))
	for i, p := range defaultSLAPolicies {
		p.OrganizationID = orgID
		policies[i] = p
	}
	return a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&policies).Error
}

// getSLAPolicy resolves the organisation-wide policy for a priority, seeding
// defaults first if none exist yet. This phase never matches a
// department/unit/category-scoped row — that is future work the schema is
// ready for (see the model's doc comment).
func (a *App) getSLAPolicy(orgID uuid.UUID, priority models.OccurrencePriority) (*models.OccurrenceSLAPolicy, error) {
	if err := a.ensureDefaultSLAPolicies(orgID); err != nil {
		return nil, err
	}
	var policy models.OccurrenceSLAPolicy
	err := a.DB.Where(
		"organization_id = ? AND priority = ? AND department_id IS NULL AND unit_id IS NULL AND category_id IS NULL",
		orgID, priority,
	).First(&policy).Error
	return &policy, err
}

// resolveOccurrenceSLAMinutes returns the response/resolution minutes to use
// for a new or re-prioritised occurrence: the linked OccurrenceProcess's own
// values when it has them, falling back to the priority-based
// OccurrenceSLAPolicy otherwise. This is the single place both CreateOccurrence
// and UpdateOccurrence call, so the override rule exists exactly once.
func (a *App) resolveOccurrenceSLAMinutes(orgID uuid.UUID, priority models.OccurrencePriority, processID *uuid.UUID) (responseMinutes, resolutionMinutes int, err error) {
	policy, err := a.getSLAPolicy(orgID, priority)
	if err != nil {
		return 0, 0, err
	}
	responseMinutes, resolutionMinutes = policy.ResponseMinutes, policy.ResolutionMinutes

	if processID == nil {
		return responseMinutes, resolutionMinutes, nil
	}
	var process models.OccurrenceProcess
	if err := a.DB.Where("id = ? AND organization_id = ?", *processID, orgID).First(&process).Error; err != nil {
		// A missing process falls back silently, same as an absent id; any other
		// error is a real failure worth a log line before falling back.
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			a.Log.Warn("Failed to load occurrence process for SLA override", "error", err, "process_id", *processID)
		}
		return responseMinutes, resolutionMinutes, nil
	}
	if process.ResponseMinutes != nil {
		responseMinutes = *process.ResponseMinutes
	}
	if process.ResolutionMinutes != nil {
		resolutionMinutes = *process.ResolutionMinutes
	}
	return responseMinutes, resolutionMinutes, nil
}

// ListOccurrenceSLAPolicies returns the org's four priority policies, seeding
// defaults on first use. Gated on occurrences:read, same reasoning as ListUnits.
func (a *App) ListOccurrenceSLAPolicies(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	if err := a.ensureDefaultSLAPolicies(orgID); err != nil {
		a.Log.Error("Failed to seed SLA policies", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load SLA policies", nil, "")
	}

	var policies []models.OccurrenceSLAPolicy
	if err := a.DB.Where("organization_id = ? AND department_id IS NULL AND unit_id IS NULL AND category_id IS NULL", orgID).
		Find(&policies).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load SLA policies", nil, "")
	}

	return r.SendEnvelope(map[string]any{"policies": policies})
}

// UpsertSLAPolicyRequest is the body for PUT /occurrence-sla-policies/{priority}.
type UpsertSLAPolicyRequest struct {
	ResponseMinutes   int `json:"response_minutes"`
	ResolutionMinutes int `json:"resolution_minutes"`
}

// UpsertOccurrenceSLAPolicy edits one priority's response/resolution minutes.
func (a *App) UpsertOccurrenceSLAPolicy(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceSLAPolicies, models.ActionWrite)
	if err != nil {
		return nil
	}

	priority := models.OccurrencePriority(r.RequestCtx.UserValue("priority").(string))
	switch priority {
	case models.OccurrencePriorityLow, models.OccurrencePriorityNormal,
		models.OccurrencePriorityHigh, models.OccurrencePriorityUrgent:
	default:
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid priority", nil, "")
	}

	var req UpsertSLAPolicyRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.ResponseMinutes <= 0 || req.ResolutionMinutes <= 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "response_minutes and resolution_minutes must be positive", nil, "")
	}

	if err := a.ensureDefaultSLAPolicies(orgID); err != nil {
		a.Log.Error("Failed to seed SLA policies", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update SLA policy", nil, "")
	}

	if err := a.DB.Model(&models.OccurrenceSLAPolicy{}).
		Where("organization_id = ? AND priority = ? AND department_id IS NULL AND unit_id IS NULL AND category_id IS NULL",
			orgID, priority).
		Updates(map[string]any{
			"response_minutes":   req.ResponseMinutes,
			"resolution_minutes": req.ResolutionMinutes,
		}).Error; err != nil {
		a.Log.Error("Failed to update SLA policy", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update SLA policy", nil, "")
	}

	policy, err := a.getSLAPolicy(orgID, priority)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to reload SLA policy", nil, "")
	}
	return r.SendEnvelope(policy)
}
