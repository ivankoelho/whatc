package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// DepartmentRequest is the create/update body for a Department.
type DepartmentRequest struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// ListDepartments returns the org's departments. Same read-gate reasoning as ListUnits.
func (a *App) ListDepartments(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	var departments []models.Department
	if err := a.DB.Where("organization_id = ?", orgID).Order("name ASC").Find(&departments).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load departments", nil, "")
	}

	return r.SendEnvelope(map[string]any{"departments": departments})
}

// CreateDepartment adds a department.
func (a *App) CreateDepartment(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceDepartments, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req DepartmentRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	department := models.Department{OrganizationID: orgID, Name: req.Name, Active: req.Active}
	if err := a.DB.Create(&department).Error; err != nil {
		if isUniqueNameViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A department with this name already exists", nil, "")
		}
		a.Log.Error("Failed to create department", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create department", nil, "")
	}

	return r.SendEnvelope(department)
}

// UpdateDepartment edits a department.
func (a *App) UpdateDepartment(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceDepartments, models.ActionWrite)
	if err != nil {
		return nil
	}

	departmentID, err := parsePathUUID(r, "id", "department")
	if err != nil {
		return nil
	}
	department, err := findByIDAndOrg[models.Department](a.DB, r, departmentID, orgID, "Department")
	if err != nil {
		return nil
	}

	var req DepartmentRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	if err := a.DB.Model(department).Updates(map[string]any{
		"name": req.Name, "active": req.Active,
	}).Error; err != nil {
		if isUniqueNameViolation(err) {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, "A department with this name already exists", nil, "")
		}
		a.Log.Error("Failed to update department", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update department", nil, "")
	}

	return r.SendEnvelope(department)
}

// DeleteDepartment removes a department, refusing when a team still references it.
func (a *App) DeleteDepartment(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceDepartments, models.ActionDelete)
	if err != nil {
		return nil
	}

	departmentID, err := parsePathUUID(r, "id", "department")
	if err != nil {
		return nil
	}
	department, err := findByIDAndOrg[models.Department](a.DB, r, departmentID, orgID, "Department")
	if err != nil {
		return nil
	}

	var inUse int64
	a.DB.Model(&models.Team{}).Where("department_id = ?", departmentID).Count(&inUse)
	if inUse > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Department is in use by an existing team", nil, "")
	}

	if err := a.DB.Delete(department).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete department", nil, "")
	}

	return r.SendEnvelope(map[string]any{"deleted": true})
}
