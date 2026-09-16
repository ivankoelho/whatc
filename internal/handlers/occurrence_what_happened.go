package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm/clause"
)

// defaultWhatHappened are the six root causes the product owner specified
// directly, seeded the first time an organisation reads this list — same
// pattern as ensureDefaultStages/ensureDefaultCategories.
var defaultWhatHappened = []string{
	"Atraso na Entrega",
	"Produto com Avaria",
	"Diferença de Tonalidade",
	"Quantidade Divergente",
	"Erro no cadastro Fiscal",
	"Duvida sobre o Produto",
}

// ensureDefaultWhatHappened seeds the six default reasons on first read.
// Idempotency here is a plain count-then-insert, not a partial unique
// index + ON CONFLICT like ensureDefaultStages: OccurrenceWhatHappened has
// no uniqueness constraint on name (matching OccurrenceCategory, which
// doesn't either), so a race double-seeding is a cosmetic duplicate, not a
// correctness bug — accepted for the same reason documented on
// ensureDefaultSACWidgets.
func (a *App) ensureDefaultWhatHappened(orgID uuid.UUID) error {
	var count int64
	if err := a.DB.Model(&models.OccurrenceWhatHappened{}).
		Where("organization_id = ?", orgID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	rows := make([]models.OccurrenceWhatHappened, len(defaultWhatHappened))
	for i, name := range defaultWhatHappened {
		rows[i] = models.OccurrenceWhatHappened{
			OrganizationID: orgID, Name: name, Position: i, IsActive: true,
		}
	}
	return a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// OccurrenceWhatHappenedRequest is the create/update body.
type OccurrenceWhatHappenedRequest struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
	IsActive *bool  `json:"is_active"`
}

// ListOccurrenceWhatHappened returns the org's reasons, seeding defaults on
// first use. Gated on occurrences:read, same reasoning as ListUnits.
func (a *App) ListOccurrenceWhatHappened(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	if err := a.ensureDefaultWhatHappened(orgID); err != nil {
		a.Log.Error("Failed to seed default what-happened reasons", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load reasons", nil, "")
	}

	var reasons []models.OccurrenceWhatHappened
	if err := a.DB.Where("organization_id = ?", orgID).
		Order("position ASC").Find(&reasons).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load reasons", nil, "")
	}

	return r.SendEnvelope(map[string]any{"reasons": reasons})
}

// CreateOccurrenceWhatHappened adds a reason.
func (a *App) CreateOccurrenceWhatHappened(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceWhatHappened, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req OccurrenceWhatHappenedRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	reason := models.OccurrenceWhatHappened{
		OrganizationID: orgID, Name: req.Name, Position: req.Position, IsActive: isActive,
	}
	if err := a.DB.Create(&reason).Error; err != nil {
		a.Log.Error("Failed to create what-happened reason", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create reason", nil, "")
	}

	return r.SendEnvelope(reason)
}

// UpdateOccurrenceWhatHappened edits a reason.
func (a *App) UpdateOccurrenceWhatHappened(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceWhatHappened, models.ActionWrite)
	if err != nil {
		return nil
	}

	id, err := parsePathUUID(r, "id", "reason")
	if err != nil {
		return nil
	}
	reason, err := findByIDAndOrg[models.OccurrenceWhatHappened](a.DB, r, id, orgID, "Reason")
	if err != nil {
		return nil
	}

	var req OccurrenceWhatHappenedRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	updates := map[string]any{"name": req.Name, "position": req.Position}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	if err := a.DB.Model(reason).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update what-happened reason", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update reason", nil, "")
	}

	return r.SendEnvelope(reason)
}

// DeleteOccurrenceWhatHappened removes a reason, refusing when an occurrence
// still references it.
func (a *App) DeleteOccurrenceWhatHappened(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceWhatHappened, models.ActionDelete)
	if err != nil {
		return nil
	}

	id, err := parsePathUUID(r, "id", "reason")
	if err != nil {
		return nil
	}
	reason, err := findByIDAndOrg[models.OccurrenceWhatHappened](a.DB, r, id, orgID, "Reason")
	if err != nil {
		return nil
	}

	var occCount int64
	a.DB.Model(&models.Occurrence{}).Where("what_happened_id = ?", id).Count(&occCount)
	if occCount > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Reason is in use by existing occurrences", nil, "")
	}

	if err := a.DB.Delete(reason).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete reason", nil, "")
	}

	return r.SendEnvelope(map[string]any{"deleted": true})
}
