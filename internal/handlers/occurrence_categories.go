package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

var (
	errInvalidParent     = errors.New("invalid parent_id")
	errNestedSubcategory = errors.New("a subcategory cannot itself have a parent")
)

// OccurrenceCategoryRequest is the create/update body for a category or subcategory.
type OccurrenceCategoryRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id"`
	Position int     `json:"position"`
	IsActive bool    `json:"is_active"`
}

// ListOccurrenceCategories returns the org's category tree, flat (parent_id
// tells the frontend how to nest it). Gated on occurrences:read, same
// reasoning as ListUnits.
func (a *App) ListOccurrenceCategories(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	var categories []models.OccurrenceCategory
	if err := a.DB.Where("organization_id = ?", orgID).
		Order("position ASC").Find(&categories).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load categories", nil, "")
	}

	return r.SendEnvelope(map[string]any{"categories": categories})
}

// resolveCategoryParent validates a parent_id: it must exist in this org and
// must itself be top-level. Without the second check, a subcategory of a
// subcategory would silently produce a tree deeper than the frontend (and the
// future automation engine) is designed to render.
func (a *App) resolveCategoryParent(orgID uuid.UUID, raw *string) (*uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, errInvalidParent
	}
	var parent models.OccurrenceCategory
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&parent).Error; err != nil {
		return nil, errInvalidParent
	}
	if parent.ParentID != nil {
		return nil, errNestedSubcategory
	}
	return &id, nil
}

// CreateOccurrenceCategory adds a category or subcategory.
func (a *App) CreateOccurrenceCategory(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceCategories, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req OccurrenceCategoryRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	parentID, err := a.resolveCategoryParent(orgID, req.ParentID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}

	category := models.OccurrenceCategory{
		OrganizationID: orgID, Name: req.Name, ParentID: parentID,
		Position: req.Position, IsActive: req.IsActive,
	}
	if err := a.DB.Create(&category).Error; err != nil {
		a.Log.Error("Failed to create occurrence category", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create category", nil, "")
	}

	return r.SendEnvelope(category)
}

// UpdateOccurrenceCategory edits a category or subcategory.
func (a *App) UpdateOccurrenceCategory(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceCategories, models.ActionWrite)
	if err != nil {
		return nil
	}

	categoryID, err := parsePathUUID(r, "id", "category")
	if err != nil {
		return nil
	}
	category, err := findByIDAndOrg[models.OccurrenceCategory](a.DB, r, categoryID, orgID, "Category")
	if err != nil {
		return nil
	}

	var req OccurrenceCategoryRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	parentID, err := a.resolveCategoryParent(orgID, req.ParentID)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}
	if parentID != nil && *parentID == categoryID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "A category cannot be its own parent", nil, "")
	}

	if err := a.DB.Model(category).Updates(map[string]any{
		"name": req.Name, "parent_id": parentID, "position": req.Position, "is_active": req.IsActive,
	}).Error; err != nil {
		a.Log.Error("Failed to update occurrence category", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update category", nil, "")
	}

	return r.SendEnvelope(category)
}

// DeleteOccurrenceCategory removes a category only when nothing depends on it
// — neither an occurrence classified under it, nor a subcategory of it.
func (a *App) DeleteOccurrenceCategory(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceCategories, models.ActionDelete)
	if err != nil {
		return nil
	}

	categoryID, err := parsePathUUID(r, "id", "category")
	if err != nil {
		return nil
	}
	category, err := findByIDAndOrg[models.OccurrenceCategory](a.DB, r, categoryID, orgID, "Category")
	if err != nil {
		return nil
	}

	var childCount int64
	a.DB.Model(&models.OccurrenceCategory{}).Where("parent_id = ?", categoryID).Count(&childCount)
	if childCount > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Category has subcategories", nil, "")
	}

	var occCount int64
	a.DB.Model(&models.Occurrence{}).Where("category_id = ?", categoryID).Count(&occCount)
	if occCount > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Category is in use by existing occurrences", nil, "")
	}

	if err := a.DB.Delete(category).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete category", nil, "")
	}

	return r.SendEnvelope(map[string]any{"deleted": true})
}
