package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// KnowledgeScopeItem is a unit or department as the Knowledge screens need it:
// nothing but its id, its name and whether it is still active (no CNPJ, X2 store
// or any other field of those domains).
type KnowledgeScopeItem struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Active bool      `json:"active"`
}

// KnowledgeScopes GET /api/knowledge/scopes (knowledge:read): the units and
// departments the caller may pick or see, so the Knowledge screens do not depend on
// occurrences:read.
//
// Who gets what follows knowledgeCapabilities (the same rule as search and listing):
//   - may choose a context (administration or conversations:view_all): every unit and
//     department of the caller's organization, inactive ones included and marked;
//   - plain reader: only their OWN unit and department (to show names), or nothing.
//
// Always limited to the caller's organization; deleted rows never appear.
func (a *App) KnowledgeScopes(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceKnowledge, models.ActionRead)
	if err != nil {
		return nil
	}
	_, chooses := a.knowledgeCapabilities(userID, orgID)

	var u models.User
	if err := a.DB.Select("id", "unit_id", "department_id").First(&u, "id = ?", userID).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load user", nil, "")
	}

	units := a.DB.Model(&models.Unit{}).Select("id", "name", "active").Where("organization_id = ?", orgID)
	depts := a.DB.Model(&models.Department{}).Select("id", "name", "active").Where("organization_id = ?", orgID)
	if !chooses {
		units = units.Where("id = ?", u.UnitID)
		depts = depts.Where("id = ?", u.DepartmentID)
	}
	unitItems, deptItems := []KnowledgeScopeItem{}, []KnowledgeScopeItem{}
	if err := units.Order("name ASC").Find(&unitItems).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load units", nil, "")
	}
	if err := depts.Order("name ASC").Find(&deptItems).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load departments", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"can_choose_context": chooses,
		"units":              unitItems,
		"departments":        deptItems,
		"own":                map[string]any{"unit_id": u.UnitID, "department_id": u.DepartmentID},
	})
}
