package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// orgPlacement is the optional Unit/Department pair a Contact or User can be
// placed in. Each Set flag distinguishes "absent, leave alone" from "present":
// a present field with a nil ID means "clear it".
type orgPlacement struct {
	UnitSet, DepartmentSet bool
	UnitID, DepartmentID   *uuid.UUID
}

// parseOrgPlacement parses the optional unit_id/department_id of a request and
// checks that each one belongs to orgID. It never trusts an ID from the client:
// an ID from another organization is a 404, the same as a missing one. When ok
// is false the error response has already been sent.
func (a *App) parseOrgPlacement(r *fastglue.Request, orgID uuid.UUID, unit, department *string) (p orgPlacement, ok bool) {
	unitID, clearUnit, err := parseOptionalUUID(unit)
	if err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		return p, false
	}
	deptID, clearDept, err := parseOptionalUUID(department)
	if err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
		return p, false
	}
	if unitID != nil {
		if _, err := findByIDAndOrg[models.Unit](a.DB, r, *unitID, orgID, "Unit"); err != nil {
			return p, false
		}
	}
	if deptID != nil {
		if _, err := findByIDAndOrg[models.Department](a.DB, r, *deptID, orgID, "Department"); err != nil {
			return p, false
		}
	}
	p.UnitSet, p.UnitID = unitID != nil || clearUnit, unitID
	p.DepartmentSet, p.DepartmentID = deptID != nil || clearDept, deptID
	return p, true
}

// apply copies the fields that were sent into a map-based update. The keys are
// the literal column names (GORM does not translate map keys).
func (p orgPlacement) apply(updates map[string]any) {
	if p.UnitSet {
		updates["unit_id"] = p.UnitID
	}
	if p.DepartmentSet {
		updates["department_id"] = p.DepartmentID
	}
}

// isPlacementInUse reports whether any team, contact or user still points at the
// unit/department (column is "unit_id" or "department_id"), so deleting it
// cannot leave a dangling reference. Soft-deleted contacts and users count too (teams: only live ones):
// restoring one would otherwise bring back a pointer to a deleted record.
func (a *App) isPlacementInUse(column string, id uuid.UUID) bool {
	var n int64
	// A soft-deleted team no longer routes anything, so it does not block.
	a.DB.Model(&models.Team{}).Where(column+" = ?", id).Limit(1).Count(&n)
	if n > 0 {
		return true
	}
	for _, m := range []any{&models.Contact{}, &models.User{}} {
		a.DB.Unscoped().Model(m).Where(column+" = ?", id).Limit(1).Count(&n)
		if n > 0 {
			return true
		}
	}
	return false
}
