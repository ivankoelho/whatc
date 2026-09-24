package handlers

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// UnitRequest is the create/update body for a Unit.
type UnitRequest struct {
	Name   string `json:"name"`
	CNPJ   string `json:"cnpj"`
	Type   string `json:"type"`
	Active bool   `json:"active"`
}

// ListUnits returns the org's units. Gated on occurrences:read: seeing unit
// names is part of using the CRM (assigning a case, filtering a queue), not
// the administrative permission that governs editing the catalog.
func (a *App) ListUnits(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	var units []models.Unit
	if err := a.DB.Where("organization_id = ?", orgID).Order("name ASC").Find(&units).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load units", nil, "")
	}

	return r.SendEnvelope(map[string]any{"units": units})
}

// CreateUnit adds a unit.
func (a *App) CreateUnit(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceUnits, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req UnitRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}
	cnpj := normalizeDocument(req.CNPJ)
	if cnpj != "" && !validCNPJ(cnpj) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid CNPJ", nil, "")
	}

	unit := models.Unit{
		OrganizationID: orgID,
		Name:           req.Name,
		CNPJ:           cnpj,
		Type:           req.Type,
		Active:         req.Active,
	}
	if err := a.DB.Create(&unit).Error; err != nil {
		if isUniqueCNPJViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A unit with this CNPJ already exists", nil, "")
		}
		if isUniqueNameViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A unit with this name already exists", nil, "")
		}
		a.Log.Error("Failed to create unit", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create unit", nil, "")
	}

	return r.SendEnvelope(unit)
}

// UpdateUnit edits a unit.
func (a *App) UpdateUnit(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceUnits, models.ActionWrite)
	if err != nil {
		return nil
	}

	unitID, err := parsePathUUID(r, "id", "unit")
	if err != nil {
		return nil
	}
	unit, err := findByIDAndOrg[models.Unit](a.DB, r, unitID, orgID, "Unit")
	if err != nil {
		return nil
	}

	var req UnitRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}
	cnpj := normalizeDocument(req.CNPJ)
	if cnpj != "" && !validCNPJ(cnpj) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid CNPJ", nil, "")
	}

	if err := a.DB.Model(unit).Updates(map[string]any{
		"name": req.Name, "cnpj": cnpj, "type": req.Type, "active": req.Active,
	}).Error; err != nil {
		if isUniqueCNPJViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A unit with this CNPJ already exists", nil, "")
		}
		if isUniqueNameViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A unit with this name already exists", nil, "")
		}
		a.Log.Error("Failed to update unit", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update unit", nil, "")
	}

	return r.SendEnvelope(unit)
}

// DeleteUnit removes a unit, refusing when a team still references it — an
// orphaned team.unit_id would silently stop reporting where its cases open.
func (a *App) DeleteUnit(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceUnits, models.ActionDelete)
	if err != nil {
		return nil
	}

	unitID, err := parsePathUUID(r, "id", "unit")
	if err != nil {
		return nil
	}
	unit, err := findByIDAndOrg[models.Unit](a.DB, r, unitID, orgID, "Unit")
	if err != nil {
		return nil
	}

	var inUse int64
	a.DB.Model(&models.Team{}).Where("unit_id = ?", unitID).Count(&inUse)
	if inUse > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Unit is in use by an existing team", nil, "")
	}

	if err := a.DB.Delete(unit).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete unit", nil, "")
	}

	return r.SendEnvelope(map[string]any{"deleted": true})
}

// validCNPJ checks a digits-only CNPJ: 14 digits, not all equal, and both
// check digits correct.
func validCNPJ(c string) bool {
	if len(c) != 14 || strings.Count(c, c[:1]) == 14 {
		return false
	}
	digit := func(n int) byte {
		weights := []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}[13-n:]
		sum := 0
		for i := 0; i < n; i++ {
			sum += int(c[i]-'0') * weights[i]
		}
		if r := sum % 11; r >= 2 {
			return byte('0' + 11 - r)
		}
		return '0'
	}
	return c[12] == digit(12) && c[13] == digit(13)
}

// isUniqueCNPJViolation tells a duplicate CNPJ apart from a duplicate name:
// both are unique violations on units.
func isUniqueCNPJViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_units_org_cnpj"
}
