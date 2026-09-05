# Help Desk — Unidade, Departamento, Categoria e SLA (Fase 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Evolve the `Occurrences` module into the Help Desk core described in `docs/superpowers/specs/2026-09-04-helpdesk-unidade-departamento-sla-design.md` — Unit, Department and Category as first-class entities, SLA computed by priority with a schema pre-widened for future department/unit/category scoping, and a "reply" event distinct from an internal note for first-response SLA timing.

**Architecture:** Purely additive on top of the shipped Occurrences Phase 1 (`internal/models/occurrences.go`, `internal/handlers/occurrences.go`). Five new tables, four new nullable columns on `teams`/`occurrences`, one new event type, one new endpoint (`/reply`), one new step in the existing `sla_processor.go` loop. No existing table's semantics change, no existing endpoint's behavior changes for callers that don't send the new optional fields.

**Tech Stack:** Go 1.25, GORM 1.25 + Postgres, Fastglue/fasthttp, `google/uuid`. Backend only — this plan does not include frontend screens (see Scope Note below).

## Scope Note

This plan covers the backend only: models, migrations, permissions, handlers, and the SLA processor. The spec's frontend section (§9) — four new settings screens, occurrence detail/list changes — is deliberately a separate follow-up plan, written after these endpoints exist and their real response shapes can be checked against running code, so the frontend plan isn't written against guessed contracts.

## Global Constraints

- Every new table follows `BaseModel` (`internal/models/models.go:82-87`: `ID uuid` default `gen_random_uuid()`, `CreatedAt`/`UpdatedAt`/`DeletedAt` soft-delete).
- Every handler that reads/writes an occurrence-adjacent resource authenticates via `a.requireAuth(r, resource, action)` (`internal/handlers/app.go:262`), never a hand-rolled check, except `UpdateTeam` which keeps its existing team-manager carve-out.
- New GORM models are added to `GetMigrationModels()` (`internal/database/postgres.go:56`) in the same call — no separate migration mechanism exists in this codebase (`AutoMigrate` only, per `postgres.go:129-135`).
- Partial unique indexes are declared via GORM struct tags (`uniqueIndex:name,where:...`), never added to `getIndexes()` — confirmed by precedent: `OccurrenceStage.Name`'s partial index is tag-only and does not appear in `getIndexes()` (`internal/database/postgres.go:246-311`), which is reserved for indexes without a corresponding struct.
- New permission resources are added to `DefaultPermissions()` and, where a role should have them, to the relevant list inside `SystemRolePermissions()` (`internal/models/roles.go:110`, `:274`) — `CreateAdminRole` in tests reads all of `DefaultPermissions()` automatically (`test/testutil/fixtures.go:289`), so admin-role tests need no fixture change.
- Money/time-sensitive logic (SLA deadline computation, breach marking, partial-index uniqueness) gets a real test, per every task below.

---

### Task 1: Unit and Department — models, migration, permissions, CRUD handlers

**Files:**
- Create: `internal/models/units.go`
- Modify: `internal/database/postgres.go:56-126` (add `Unit`, `Department` to `GetMigrationModels()`)
- Modify: `internal/models/roles.go` (new resource constants + `DefaultPermissions()` + `SystemRolePermissions()`)
- Create: `internal/handlers/units.go`
- Create: `internal/handlers/departments.go`
- Modify: `cmd/whatomate/main.go` (routes)
- Test: `internal/handlers/units_test.go`
- Test: `internal/handlers/departments_test.go`

**Interfaces:**
- Produces: `models.Unit{BaseModel, OrganizationID, Name, Code, Type, Active, Address, Phone, BusinessHoursID, Metadata}`, `models.Department{BaseModel, OrganizationID, Name, Active}`, `models.ResourceUnits = "units"`, `models.ResourceDepartments = "departments"`, handlers `ListUnits`/`CreateUnit`/`UpdateUnit`/`DeleteUnit`/`ListDepartments`/`CreateDepartment`/`UpdateDepartment`/`DeleteDepartment`.
- Consumes: `a.requireAuth` (`app.go:262`), `a.decodeRequest` (existing), `parsePathUUID`/`findByIDAndOrg` (existing generics, used identically to `internal/handlers/teams.go`), `isUniqueNameViolation` (`internal/handlers/occurrence_stages.go:264` — package-level, reused unmodified).

- [ ] **Step 1: Write `models.Unit` and `models.Department`**

```go
// internal/models/units.go
package models

import "github.com/google/uuid"

// Unit is a physical location (store, branch, headquarters) an organisation
// operates. The schema is wider than the Fase 3 UI exposes on purpose —
// Address/Phone/BusinessHoursID have no screen yet, so a later request for
// them costs a UI change, not a second migration.
type Unit struct {
	BaseModel
	OrganizationID  uuid.UUID  `gorm:"type:uuid;index;not null;uniqueIndex:idx_units_org_name,where:deleted_at IS NULL" json:"organization_id"`
	Name            string     `gorm:"size:255;not null;uniqueIndex:idx_units_org_name,where:deleted_at IS NULL" json:"name"`
	Code            string     `gorm:"size:50" json:"code,omitempty"`
	Type            string     `gorm:"size:50" json:"type,omitempty"`
	Active          bool       `gorm:"default:true" json:"active"`
	Address         string     `gorm:"size:500" json:"address,omitempty"`
	Phone           string     `gorm:"size:50" json:"phone,omitempty"`
	BusinessHoursID *uuid.UUID `gorm:"type:uuid" json:"business_hours_id,omitempty"`
	Metadata        JSONB      `gorm:"type:jsonb;default:'{}'" json:"metadata"`
}

func (Unit) TableName() string { return "units" }

// Department is a functional sector (Logística, ADM, TI...) shared across
// every Unit — one row per sector, not one per (unit, sector) combination.
type Department struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_departments_org_name,where:deleted_at IS NULL" json:"organization_id"`
	Name           string    `gorm:"size:255;not null;uniqueIndex:idx_departments_org_name,where:deleted_at IS NULL" json:"name"`
	Active         bool      `gorm:"default:true" json:"active"`
}

func (Department) TableName() string { return "departments" }
```

- [ ] **Step 2: Register the two models for migration**

In `internal/database/postgres.go`, inside `GetMigrationModels()`, immediately after the `OccurrenceCounter` entry and before `AuditLog`:

```go
		// CRM de ocorrências
		{"OccurrenceStage", &models.OccurrenceStage{}},
		{"Occurrence", &models.Occurrence{}},
		{"OccurrenceEvent", &models.OccurrenceEvent{}},
		{"OccurrenceCounter", &models.OccurrenceCounter{}},

		// Help Desk — unidade e departamento
		{"Unit", &models.Unit{}},
		{"Department", &models.Department{}},

		{"AuditLog", &models.AuditLog{}},
```

- [ ] **Step 3: Add permission resources**

In `internal/models/roles.go`, inside the `PermissionResource constants` block, after `ResourceOccurrenceStages`:

```go
	ResourceOccurrenceStages        = "occurrences.stages"
	ResourceUnits                   = "units"
	ResourceDepartments             = "departments"
```

Inside `DefaultPermissions()`, after the occurrence-stages entries:

```go
		{Resource: ResourceOccurrenceStages, Action: ActionDelete, Description: "Delete occurrence stages"},

		// Help Desk — unidade e departamento. Listing is gated on occurrences:read
		// (agents need unit/department names to work a case); these permissions
		// govern administering the catalog itself.
		{Resource: ResourceUnits, Action: ActionRead, Description: "View units"},
		{Resource: ResourceUnits, Action: ActionWrite, Description: "Create and edit units"},
		{Resource: ResourceUnits, Action: ActionDelete, Description: "Delete units"},
		{Resource: ResourceDepartments, Action: ActionRead, Description: "View departments"},
		{Resource: ResourceDepartments, Action: ActionWrite, Description: "Create and edit departments"},
		{Resource: ResourceDepartments, Action: ActionDelete, Description: "Delete departments"},
```

Inside `SystemRolePermissions()`, in `managerPermissions` (manager administers the CRM catalog, same reasoning as `occurrences.stages`), right after the `occurrences.stages` line:

```go
		"occurrences.stages:read", "occurrences.stages:write", "occurrences.stages:delete",
		"units:read", "units:write", "units:delete",
		"departments:read", "departments:write", "departments:delete",
```

Do **not** add these to `agentPermissions` — administering the catalog is not the agent's role, mirroring `occurrences.stages`.

- [ ] **Step 4: Write handlers — `internal/handlers/units.go`**

```go
package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// UnitRequest is the create/update body for a Unit.
type UnitRequest struct {
	Name   string `json:"name"`
	Code   string `json:"code"`
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

	unit := models.Unit{
		OrganizationID: orgID,
		Name:           req.Name,
		Code:           req.Code,
		Type:           req.Type,
		Active:         req.Active,
	}
	if err := a.DB.Create(&unit).Error; err != nil {
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

	if err := a.DB.Model(unit).Updates(map[string]any{
		"name": req.Name, "code": req.Code, "type": req.Type, "active": req.Active,
	}).Error; err != nil {
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
```

- [ ] **Step 5: Write handlers — `internal/handlers/departments.go`** (identical shape, `Department` instead of `Unit`, checked against `Team.department_id` instead of `Team.unit_id`)

```go
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
```

- [ ] **Step 6: Register routes**

In `cmd/whatomate/main.go`, right after the existing `/api/occurrences...` block:

```go
	// CRM — unidades
	g.GET("/api/units", app.ListUnits)
	g.POST("/api/units", app.CreateUnit)
	g.PUT("/api/units/{id}", app.UpdateUnit)
	g.DELETE("/api/units/{id}", app.DeleteUnit)

	// CRM — departamentos
	g.GET("/api/departments", app.ListDepartments)
	g.POST("/api/departments", app.CreateDepartment)
	g.PUT("/api/departments/{id}", app.UpdateDepartment)
	g.DELETE("/api/departments/{id}", app.DeleteDepartment)
```

- [ ] **Step 7: Write the failing tests**

```go
// internal/handlers/units_test.go
package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUnits_CreateAndList(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Loja Alagoinhas", "type": "loja", "active": true})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	listReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(listReq, org.ID, user.ID)
	require.NoError(t, app.ListUnits(listReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(listReq))
	assert.Contains(t, string(testutil.GetResponseBody(listReq)), "Loja Alagoinhas")
}

func TestUnits_CreateRejectedForAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Matriz"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req),
		"agents administer occurrences, not the unit catalog")
}

func TestUnits_DeleteRejectedWhenTeamReferencesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	unit := models.Unit{OrganizationID: org.ID, Name: "Loja Feira"}
	require.NoError(t, app.DB.Create(&unit).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Feira Logística", UnitID: &unit.ID}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", unit.ID.String())
	require.NoError(t, app.DeleteUnit(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}

func TestUnits_DuplicateNameRejected(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	first := testutil.NewJSONRequest(t, map[string]any{"name": "Matriz"})
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(first))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	second := testutil.NewJSONRequest(t, map[string]any{"name": "Matriz"})
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.CreateUnit(second))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(second))
}
```

```go
// internal/handlers/departments_test.go
package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestDepartments_CreateAndList(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Logística", "active": true})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateDepartment(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	listReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(listReq, org.ID, user.ID)
	require.NoError(t, app.ListDepartments(listReq))
	assert.Contains(t, string(testutil.GetResponseBody(listReq)), "Logística")
}

func TestDepartments_DeleteRejectedWhenTeamReferencesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	department := models.Department{OrganizationID: org.ID, Name: "ADM"}
	require.NoError(t, app.DB.Create(&department).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Alagoinhas ADM", DepartmentID: &department.ID}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", department.ID.String())
	require.NoError(t, app.DeleteDepartment(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}
```

Note: `TestUnits_DeleteRejectedWhenTeamReferencesIt` and `TestDepartments_DeleteRejectedWhenTeamReferencesIt` reference `Team.UnitID`/`Team.DepartmentID`, which do not exist yet — that's Task 2. Run only the tests that don't need it first (`-run` filter below); the other two will compile-fail until Task 2 lands, so implement Task 2 before running the full package, or comment those two out temporarily. Simplest: do Task 2 immediately after this step and before Step 8's full-package run.

- [ ] **Step 8: Implement Task 2 inline dependency, then run all tests**

Run: `go build ./... 2>&1 | head -50` — expect compile errors naming `Team.UnitID`/`Team.DepartmentID` undefined. This confirms Task 1's tests are correctly written against the target end-state; proceed to Task 2 before the first green run of this package.

- [ ] **Step 9: Commit (after Task 2 makes the package compile and pass — see Task 2's own commit step, which covers both)**

---

### Task 2: Team gets `unit_id`/`department_id`

**Files:**
- Modify: `internal/models/models.go:169-189` (`Team` struct)
- Modify: `internal/handlers/teams.go` (`TeamRequest`, `TeamResponse`, `buildTeamResponse`, `UpdateTeam`, `CreateTeam`)
- Test: `internal/handlers/teams_unit_department_test.go`

**Interfaces:**
- Consumes: `models.Unit`, `models.Department` (Task 1).
- Produces: `Team.UnitID *uuid.UUID`, `Team.DepartmentID *uuid.UUID` — consumed by Task 4 (Occurrence inheriting unit/department from the originating attendance's Team).

- [ ] **Step 1: Add columns to `Team`**

In `internal/models/models.go`, inside `type Team struct`, after `UpdatedByID`:

```go
	CreatedByID         *uuid.UUID         `gorm:"type:uuid" json:"created_by_id,omitempty"`
	UpdatedByID         *uuid.UUID         `gorm:"type:uuid" json:"updated_by_id,omitempty"`

	// UnitID/DepartmentID tag which store+sector combination this Team
	// represents. Additive metadata only — chat routing, round-robin
	// assignment and everything else about Team is unaffected; these two
	// fields exist only for Occurrence to read at creation time (see
	// docs/superpowers/specs/2026-09-04-helpdesk-unidade-departamento-sla-design.md §3).
	UnitID       *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	DepartmentID *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`

	// Relations
	Organization *Organization `gorm:"foreignKey:OrganizationID" json:"organization,omitempty"`
	Members      []TeamMember  `gorm:"foreignKey:TeamID" json:"members,omitempty"`
	CreatedBy    *User         `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
	UpdatedBy    *User         `gorm:"foreignKey:UpdatedByID" json:"updated_by,omitempty"`
	Unit         *Unit         `gorm:"foreignKey:UnitID" json:"unit,omitempty"`
	Department   *Department   `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
```

- [ ] **Step 2: Extend `TeamRequest` in `internal/handlers/teams.go`**

Find the existing struct (`teams.go:13-19`) and add two fields:

```go
type TeamRequest struct {
	Name                string                    `json:"name" validate:"required"`
	Description         string                    `json:"description"`
	AssignmentStrategy  models.AssignmentStrategy `json:"assignment_strategy"`
	PerAgentTimeoutSecs int                       `json:"per_agent_timeout_secs"`
	IsActive            bool                      `json:"is_active"`
	UnitID              *string                   `json:"unit_id,omitempty"`
	DepartmentID        *string                   `json:"department_id,omitempty"`
}
```

- [ ] **Step 3: Apply the fields in `UpdateTeam` and `CreateTeam`**

In `UpdateTeam` (`teams.go:194-264`), inside the block that copies `req` fields onto `team` (right before `team.UpdatedByID = &userID`):

```go
	team.PerAgentTimeoutSecs = req.PerAgentTimeoutSecs
	if req.UnitID != nil {
		if *req.UnitID == "" {
			team.UnitID = nil
		} else if id, err := uuid.Parse(*req.UnitID); err == nil {
			team.UnitID = &id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		}
	}
	if req.DepartmentID != nil {
		if *req.DepartmentID == "" {
			team.DepartmentID = nil
		} else if id, err := uuid.Parse(*req.DepartmentID); err == nil {
			team.DepartmentID = &id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
		}
	}
	team.UpdatedByID = &userID
```

Apply the identical parse-and-assign block in `CreateTeam` when building the new `models.Team{...}` (same file, wherever that struct literal is constructed) — set `team.UnitID`/`team.DepartmentID` from `req.UnitID`/`req.DepartmentID` the same way, before `a.DB.Create(&team)`.

- [ ] **Step 4: Extend `TeamResponse` and `buildTeamResponse`**

In `TeamResponse` (`teams.go:28-43`), add:

```go
	UnitID       *uuid.UUID `json:"unit_id,omitempty"`
	DepartmentID *uuid.UUID `json:"department_id,omitempty"`
```

In `buildTeamResponse` (`teams.go:520-559`), add to the returned struct literal:

```go
		UnitID:       team.UnitID,
		DepartmentID: team.DepartmentID,
```

- [ ] **Step 5: Write the failing tests**

```go
// internal/handlers/teams_unit_department_test.go
package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUpdateTeam_TagsUnitAndDepartment(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	unit := models.Unit{OrganizationID: org.ID, Name: "Loja Alagoinhas"}
	require.NoError(t, app.DB.Create(&unit).Error)
	department := models.Department{OrganizationID: org.ID, Name: "Logística"}
	require.NoError(t, app.DB.Create(&department).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Alagoinhas Logística"}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name": team.Name, "is_active": true,
		"unit_id": unit.ID.String(), "department_id": department.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", team.ID.String())
	require.NoError(t, app.UpdateTeam(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var reloaded models.Team
	require.NoError(t, app.DB.First(&reloaded, "id = ?", team.ID).Error)
	require.NotNil(t, reloaded.UnitID)
	require.NotNil(t, reloaded.DepartmentID)
	assert.Equal(t, unit.ID, *reloaded.UnitID)
	assert.Equal(t, department.ID, *reloaded.DepartmentID)
}

func TestUpdateTeam_ClearsUnitWithEmptyString(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	unit := models.Unit{OrganizationID: org.ID, Name: "Matriz"}
	require.NoError(t, app.DB.Create(&unit).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Time X", UnitID: &unit.ID}
	require.NoError(t, app.DB.Create(&team).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name": team.Name, "is_active": true, "unit_id": "",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", team.ID.String())
	require.NoError(t, app.UpdateTeam(req))

	var reloaded models.Team
	require.NoError(t, app.DB.First(&reloaded, "id = ?", team.ID).Error)
	assert.Nil(t, reloaded.UnitID)
}
```

- [ ] **Step 6: Run all tests for both tasks**

Run: `go test ./internal/... -run 'TestUnits_|TestDepartments_|TestUpdateTeam_' -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/models/units.go internal/models/models.go internal/models/roles.go \
        internal/database/postgres.go internal/handlers/units.go internal/handlers/departments.go \
        internal/handlers/teams.go cmd/whatomate/main.go \
        internal/handlers/units_test.go internal/handlers/departments_test.go \
        internal/handlers/teams_unit_department_test.go
git commit -m "$(cat <<'EOF'
feat(crm): add Unit and Department entities, tag Team with both

Separates the two dimensions Team today conflates by naming
convention (loja+setor), per docs/superpowers/specs/2026-09-04-helpdesk-unidade-departamento-sla-design.md.
Purely additive: chat routing and round-robin assignment are untouched.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: OccurrenceCategory — model, permissions, CRUD handlers

**Files:**
- Create: `internal/models/occurrence_categories.go`
- Modify: `internal/database/postgres.go:56-126`
- Modify: `internal/models/roles.go`
- Create: `internal/handlers/occurrence_categories.go`
- Modify: `cmd/whatomate/main.go`
- Test: `internal/handlers/occurrence_categories_test.go`

**Interfaces:**
- Produces: `models.OccurrenceCategory{BaseModel, OrganizationID, Name, ParentID, Position, IsActive}`, `models.ResourceOccurrenceCategories = "occurrences.categories"`, handlers `ListOccurrenceCategories`/`CreateOccurrenceCategory`/`UpdateOccurrenceCategory`/`DeleteOccurrenceCategory`.
- Consumes: same generic helpers as Task 1.

- [ ] **Step 1: Write the model**

```go
// internal/models/occurrence_categories.go
package models

import "github.com/google/uuid"

// OccurrenceCategory is a category or subcategory an occurrence is classified
// under. ParentID nil means a top-level category; set means a subcategory —
// one level of nesting only in this phase (a subcategory of a subcategory is
// rejected at the handler, not the schema). This is what makes an automation
// rule engine possible later (unit+department+category → team/priority/SLA)
// without a further migration.
type OccurrenceCategory struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null" json:"organization_id"`
	Name           string     `gorm:"size:100;not null" json:"name"`
	ParentID       *uuid.UUID `gorm:"type:uuid;index" json:"parent_id,omitempty"`
	Position       int        `gorm:"not null;default:0" json:"position"`
	IsActive       bool       `gorm:"default:true" json:"is_active"`

	Parent *OccurrenceCategory `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
}

func (OccurrenceCategory) TableName() string { return "occurrence_categories" }
```

- [ ] **Step 2: Register for migration**

In `internal/database/postgres.go`, `GetMigrationModels()`, after the `Department` entry from Task 1:

```go
		{"Unit", &models.Unit{}},
		{"Department", &models.Department{}},
		{"OccurrenceCategory", &models.OccurrenceCategory{}},
```

- [ ] **Step 3: Add permission resource**

In `internal/models/roles.go`, constants block:

```go
	ResourceDepartments             = "departments"
	ResourceOccurrenceCategories    = "occurrences.categories"
```

`DefaultPermissions()`:

```go
		{Resource: ResourceDepartments, Action: ActionDelete, Description: "Delete departments"},
		{Resource: ResourceOccurrenceCategories, Action: ActionRead, Description: "View occurrence categories"},
		{Resource: ResourceOccurrenceCategories, Action: ActionWrite, Description: "Create and edit occurrence categories"},
		{Resource: ResourceOccurrenceCategories, Action: ActionDelete, Description: "Delete occurrence categories"},
```

`SystemRolePermissions()`, `managerPermissions`:

```go
		"departments:read", "departments:write", "departments:delete",
		"occurrences.categories:read", "occurrences.categories:write", "occurrences.categories:delete",
```

- [ ] **Step 4: Write handlers**

```go
// internal/handlers/occurrence_categories.go
package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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
```

Add the two sentinel errors near the top of the same file (package-level, alongside the functions that use them):

```go
var (
	errInvalidParent     = errBadRequest("invalid parent_id")
	errNestedSubcategory = errBadRequest("a subcategory cannot itself have a parent")
)
```

If `errBadRequest` (a `string -> error` helper) does not already exist in the `handlers` package, define it inline instead using the standard library:

```go
import "errors"

var (
	errInvalidParent     = errors.New("invalid parent_id")
	errNestedSubcategory = errors.New("a subcategory cannot itself have a parent")
)
```

(Check first with `grep -rn "func errBadRequest" internal/handlers/` — use whichever already exists; do not define both.)

- [ ] **Step 5: Register routes**

In `cmd/whatomate/main.go`, after the units/departments block:

```go
	// CRM — categorias de ocorrência
	g.GET("/api/occurrence-categories", app.ListOccurrenceCategories)
	g.POST("/api/occurrence-categories", app.CreateOccurrenceCategory)
	g.PUT("/api/occurrence-categories/{id}", app.UpdateOccurrenceCategory)
	g.DELETE("/api/occurrence-categories/{id}", app.DeleteOccurrenceCategory)
```

- [ ] **Step 6: Write the failing tests**

```go
// internal/handlers/occurrence_categories_test.go
package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOccurrenceCategories_CreateTopLevelAndSubcategory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	top := testutil.NewJSONRequest(t, map[string]any{"name": "Impressora", "is_active": true})
	testutil.SetAuthContext(top, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(top))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(top))

	var parent models.OccurrenceCategory
	require.NoError(t, app.DB.Where("name = ?", "Impressora").First(&parent).Error)

	sub := testutil.NewJSONRequest(t, map[string]any{
		"name": "Impressora não imprime", "parent_id": parent.ID.String(), "is_active": true,
	})
	testutil.SetAuthContext(sub, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(sub))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(sub))
}

func TestOccurrenceCategories_RejectsSubcategoryOfSubcategory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	top := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Impressora"}
	require.NoError(t, app.DB.Create(&top).Error)
	sub := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Não imprime", ParentID: &top.ID}
	require.NoError(t, app.DB.Create(&sub).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"name": "Tela azul", "parent_id": sub.ID.String(), "is_active": true,
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestOccurrenceCategories_DeleteRejectedWhenOccurrenceUsesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	category := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Impressora"}
	require.NoError(t, app.DB.Create(&category).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Sem tinta",
		StageID: stage.ID, OpenedByUserID: user.ID, CategoryID: &category.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", category.ID.String())
	require.NoError(t, app.DeleteOccurrenceCategory(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}
```

Note: the third test references `Occurrence.CategoryID`, which does not exist until Task 4 — same cross-task dependency as Task 1/2. Implement Task 4's model column before running this file's full suite.

- [ ] **Step 7: Run tests, then commit**

Run: `go test ./internal/handlers/... -run 'TestOccurrenceCategories_' -v`
Expected: PASS (after Task 4's `CategoryID` column exists).

```bash
git add internal/models/occurrence_categories.go internal/models/roles.go internal/database/postgres.go \
        internal/handlers/occurrence_categories.go cmd/whatomate/main.go \
        internal/handlers/occurrence_categories_test.go
git commit -m "$(cat <<'EOF'
feat(crm): add occurrence categories and subcategories

One level of nesting (parent_id), enforced at the handler. This is
the field that makes an automation rule engine (unit+department+
category -> team/priority/SLA) possible later without another
migration -- the engine itself is out of scope here.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Occurrence gets `unit_id`, `department_id`, `category_id`, `source`

**Files:**
- Modify: `internal/models/occurrences.go` (`Occurrence` struct)
- Modify: `internal/handlers/occurrences.go` (`CreateOccurrenceRequest`, `UpdateOccurrenceRequest`, `OccurrenceResponse`, `occurrenceToResponse`, `CreateOccurrence`, `UpdateOccurrence`, `ListOccurrences` filters)
- Test: `internal/handlers/occurrence_unit_department_test.go`

**Interfaces:**
- Consumes: `models.Unit`, `models.Department`, `models.OccurrenceCategory` (Tasks 1, 3), `Team.UnitID`/`Team.DepartmentID` (Task 2), `models.AgentTransfer` (existing, `internal/models/chatbot.go:308`).
- Produces: `Occurrence.UnitID`, `Occurrence.DepartmentID`, `Occurrence.CategoryID`, `Occurrence.Source` — consumed by Task 6 (SLA policy lookup does not use these yet, but the columns must exist first) and by the eventual frontend plan.

- [ ] **Step 1: Add fields to `Occurrence`**

In `internal/models/occurrences.go`, inside `type Occurrence struct`, after `SourceTransferID`:

```go
	// SourceTransferID records which attendance spawned this occurrence. It is
	// traceability only — the lifecycles stay independent, so closing the chat
	// attendance never closes the occurrence.
	SourceTransferID *uuid.UUID `gorm:"type:uuid;index" json:"source_transfer_id,omitempty"`

	// UnitID/DepartmentID default from the originating attendance's Team
	// (AgentTransfer.TeamID, via SourceTransferID) when the occurrence is
	// opened from a conversation; both are editable directly otherwise.
	UnitID       *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	DepartmentID *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`
	CategoryID   *uuid.UUID `gorm:"type:uuid;index" json:"category_id,omitempty"`

	// Source documents where this case originated. Whatomate has one channel
	// today (WhatsApp), so this is a placeholder for a future channel, not
	// active logic — "manual" when there is no SourceTransferID.
	Source string `gorm:"size:20;not null;default:'whatsapp'" json:"source"`
```

And add relations next to the existing `Contact`/`Stage`/`AssignedUser` relations:

```go
	Contact      *Contact         `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	Stage        *OccurrenceStage `gorm:"foreignKey:StageID" json:"stage,omitempty"`
	AssignedUser *User            `gorm:"foreignKey:AssignedUserID" json:"assigned_user,omitempty"`
	Unit         *Unit               `gorm:"foreignKey:UnitID" json:"unit,omitempty"`
	Department   *Department         `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	Category     *OccurrenceCategory `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
```

- [ ] **Step 2: Add a helper to resolve unit/department from a source transfer**

In `internal/handlers/occurrences.go`, near `resolveAssignee`:

```go
// unitDepartmentFromTransfer looks up the Team behind an AgentTransfer and
// returns the unit/department it's tagged with. Both come back nil when the
// transfer doesn't exist, has no team, or the team isn't tagged yet — a case
// simply opens without them, editable by hand, matching the spec's decision
// not to require every existing Team to be tagged before this feature works.
func (a *App) unitDepartmentFromTransfer(orgID, transferID uuid.UUID) (unitID, departmentID *uuid.UUID) {
	var transfer models.AgentTransfer
	if err := a.DB.Where("id = ? AND organization_id = ?", transferID, orgID).First(&transfer).Error; err != nil {
		return nil, nil
	}
	if transfer.TeamID == nil {
		return nil, nil
	}
	var team models.Team
	if err := a.DB.Where("id = ?", *transfer.TeamID).First(&team).Error; err != nil {
		return nil, nil
	}
	return team.UnitID, team.DepartmentID
}
```

- [ ] **Step 3: Extend `CreateOccurrenceRequest`/`OccurrenceResponse` and wire `CreateOccurrence`**

In `CreateOccurrenceRequest` (`occurrences.go:17-24`):

```go
type CreateOccurrenceRequest struct {
	ContactID        string  `json:"contact_id"`
	Title            string  `json:"title"`
	Description      string  `json:"description"`
	Priority         string  `json:"priority"`
	AssignedUserID   *string `json:"assigned_user_id"`
	SourceTransferID *string `json:"source_transfer_id"`
	UnitID           *string `json:"unit_id"`
	DepartmentID     *string `json:"department_id"`
	CategoryID       *string `json:"category_id"`
}
```

In `OccurrenceResponse` (`occurrences.go:27-42`), add fields and populate them in `occurrenceToResponse`:

```go
type OccurrenceResponse struct {
	ID               uuid.UUID  `json:"id"`
	ProtocolNumber   string     `json:"protocol_number"`
	ContactID        uuid.UUID  `json:"contact_id"`
	ContactName      string     `json:"contact_name"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	StageID          uuid.UUID  `json:"stage_id"`
	StageName        string     `json:"stage_name"`
	Priority         string     `json:"priority"`
	AssignedUserID   *uuid.UUID `json:"assigned_user_id,omitempty"`
	AssignedUserName string     `json:"assigned_user_name,omitempty"`
	OpenedAt         time.Time  `json:"opened_at"`
	ClosedAt         *time.Time `json:"closed_at,omitempty"`
	SourceTransferID *uuid.UUID `json:"source_transfer_id,omitempty"`
	UnitID           *uuid.UUID `json:"unit_id,omitempty"`
	DepartmentID     *uuid.UUID `json:"department_id,omitempty"`
	CategoryID       *uuid.UUID `json:"category_id,omitempty"`
	Source           string     `json:"source"`
}
```

```go
func occurrenceToResponse(o models.Occurrence) OccurrenceResponse {
	resp := OccurrenceResponse{
		ID:               o.ID,
		ProtocolNumber:   o.ProtocolNumber,
		ContactID:        o.ContactID,
		Title:            o.Title,
		Description:      o.Description,
		StageID:          o.StageID,
		Priority:         string(o.Priority),
		AssignedUserID:   o.AssignedUserID,
		OpenedAt:         o.OpenedAt,
		ClosedAt:         o.ClosedAt,
		SourceTransferID: o.SourceTransferID,
		UnitID:           o.UnitID,
		DepartmentID:     o.DepartmentID,
		CategoryID:       o.CategoryID,
		Source:           o.Source,
	}
	if o.Contact != nil {
		resp.ContactName = o.Contact.ProfileName
	}
	if o.Stage != nil {
		resp.StageName = o.Stage.Name
	}
	if o.AssignedUser != nil {
		resp.AssignedUserName = o.AssignedUser.FullName
	}
	return resp
}
```

In `CreateOccurrence` (`occurrences.go:137-235`), right after the existing `SourceTransferID` parse block and before `insertOccurrenceWithProtocol`:

```go
	if req.SourceTransferID != nil && *req.SourceTransferID != "" {
		if id, err := uuid.Parse(*req.SourceTransferID); err == nil {
			occ.SourceTransferID = &id
			occ.Source = "whatsapp"
		}
	} else {
		occ.Source = "manual"
	}

	// Inherit unit/department from the originating attendance's Team, then let
	// an explicit request field override — manual entry always wins, since the
	// case may not even have a source transfer.
	if occ.SourceTransferID != nil {
		occ.UnitID, occ.DepartmentID = a.unitDepartmentFromTransfer(orgID, *occ.SourceTransferID)
	}
	if req.UnitID != nil && *req.UnitID != "" {
		if id, err := uuid.Parse(*req.UnitID); err == nil {
			occ.UnitID = &id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		}
	}
	if req.DepartmentID != nil && *req.DepartmentID != "" {
		if id, err := uuid.Parse(*req.DepartmentID); err == nil {
			occ.DepartmentID = &id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
		}
	}
	if req.CategoryID != nil && *req.CategoryID != "" {
		if id, err := uuid.Parse(*req.CategoryID); err == nil {
			occ.CategoryID = &id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid category_id", nil, "")
		}
	}
```

- [ ] **Step 4: Extend `UpdateOccurrenceRequest` and `UpdateOccurrence`**

```go
type UpdateOccurrenceRequest struct {
	Title          string  `json:"title"`
	Description    *string `json:"description"`
	Priority       string  `json:"priority"`
	AssignedUserID *string `json:"assigned_user_id"`
	UnitID         *string `json:"unit_id"`
	DepartmentID   *string `json:"department_id"`
	CategoryID     *string `json:"category_id"`
}
```

In `UpdateOccurrence`, after the existing `if req.Priority != ""` block:

```go
	if req.UnitID != nil {
		if *req.UnitID == "" {
			updates["unit_id"] = nil
		} else if id, err := uuid.Parse(*req.UnitID); err == nil {
			updates["unit_id"] = id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid unit_id", nil, "")
		}
	}
	if req.DepartmentID != nil {
		if *req.DepartmentID == "" {
			updates["department_id"] = nil
		} else if id, err := uuid.Parse(*req.DepartmentID); err == nil {
			updates["department_id"] = id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
		}
	}
	if req.CategoryID != nil {
		if *req.CategoryID == "" {
			updates["category_id"] = nil
		} else if id, err := uuid.Parse(*req.CategoryID); err == nil {
			updates["category_id"] = id
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid category_id", nil, "")
		}
	}
```

- [ ] **Step 5: Add filters to `ListOccurrences`**

After the existing `contact_id` filter in `ListOccurrences` (`occurrences.go:253-255`):

```go
	if unitID := string(r.RequestCtx.QueryArgs().Peek("unit_id")); unitID != "" {
		query = query.Where("occurrences.unit_id = ?", unitID)
	}
	if departmentID := string(r.RequestCtx.QueryArgs().Peek("department_id")); departmentID != "" {
		query = query.Where("occurrences.department_id = ?", departmentID)
	}
	if categoryID := string(r.RequestCtx.QueryArgs().Peek("category_id")); categoryID != "" {
		query = query.Where("occurrences.category_id = ?", categoryID)
	}
```

- [ ] **Step 6: Write the failing tests**

```go
// internal/handlers/occurrence_unit_department_test.go
package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestCreateOccurrence_InheritsUnitDepartmentFromSourceTransfer(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	unit := models.Unit{OrganizationID: org.ID, Name: "Loja Alagoinhas"}
	require.NoError(t, app.DB.Create(&unit).Error)
	department := models.Department{OrganizationID: org.ID, Name: "Logística"}
	require.NoError(t, app.DB.Create(&department).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Alagoinhas Logística", UnitID: &unit.ID, DepartmentID: &department.ID}
	require.NoError(t, app.DB.Create(&team).Error)
	transfer := models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "test",
		PhoneNumber: contact.PhoneNumber, TeamID: &team.ID,
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Sem entrega",
		"source_transfer_id": transfer.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.UnitID)
	require.NotNil(t, occ.DepartmentID)
	assert.Equal(t, unit.ID, *occ.UnitID)
	assert.Equal(t, department.ID, *occ.DepartmentID)
	assert.Equal(t, "whatsapp", occ.Source)
}

func TestCreateOccurrence_ManualUnitOverridesInheritedOne(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	inherited := models.Unit{OrganizationID: org.ID, Name: "Loja Feira"}
	require.NoError(t, app.DB.Create(&inherited).Error)
	override := models.Unit{OrganizationID: org.ID, Name: "Matriz"}
	require.NoError(t, app.DB.Create(&override).Error)
	team := models.Team{OrganizationID: org.ID, Name: "Feira Logística", UnitID: &inherited.ID}
	require.NoError(t, app.DB.Create(&team).Error)
	transfer := models.AgentTransfer{
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "test",
		PhoneNumber: contact.PhoneNumber, TeamID: &team.ID,
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Reclassificado",
		"source_transfer_id": transfer.ID.String(), "unit_id": override.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.UnitID)
	assert.Equal(t, override.ID, *occ.UnitID)
}

func TestCreateOccurrence_ManualHasSourceManual(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Aberto sem conversa",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	assert.Equal(t, "manual", occ.Source)
	assert.Nil(t, occ.UnitID)
}
```

- [ ] **Step 7: Run tests, then commit**

Run: `go build ./... && go test ./internal/handlers/... -run 'TestCreateOccurrence_|TestUnits_|TestDepartments_|TestUpdateTeam_|TestOccurrenceCategories_' -v`
Expected: PASS across all four tasks now that every cross-task dependency exists.

```bash
git add internal/models/occurrences.go internal/handlers/occurrences.go \
        internal/handlers/occurrence_unit_department_test.go
git commit -m "$(cat <<'EOF'
feat(crm): occurrence inherits unit/department from its source team

Manual entry always overrides the inherited value. Source defaults
to whatsapp when opened from a conversation, manual otherwise --
placeholder for a future second channel, no active branching on it yet.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `occurrence_sla_policies` — model, seed, endpoints

**Files:**
- Create: `internal/models/occurrence_sla_policy.go`
- Modify: `internal/database/postgres.go:56-126`
- Modify: `internal/models/roles.go`
- Create: `internal/handlers/occurrence_sla_policies.go`
- Modify: `cmd/whatomate/main.go`
- Test: `internal/handlers/occurrence_sla_policies_test.go`

**Interfaces:**
- Produces: `models.OccurrenceSLAPolicy{...}`, `a.getSLAPolicy(orgID, priority) (*models.OccurrenceSLAPolicy, error)`, `a.ensureDefaultSLAPolicies(orgID) error` — both consumed by Task 6.
- Consumes: `models.OccurrencePriority` (existing, `occurrences.go:22-29`).

- [ ] **Step 1: Write the model**

```go
// internal/models/occurrence_sla_policy.go
package models

import "github.com/google/uuid"

// OccurrenceSLAPolicy is one response/resolution target for a priority level.
// DepartmentID/UnitID/CategoryID exist so a future phase can scope SLA more
// narrowly than "by priority, organisation-wide" without a schema change —
// this phase only ever creates and matches rows where all three are nil.
type OccurrenceSLAPolicy struct {
	BaseModel
	OrganizationID uuid.UUID  `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_sla_org_priority_scope,where:department_id IS NULL AND unit_id IS NULL AND category_id IS NULL AND deleted_at IS NULL" json:"organization_id"`
	DepartmentID   *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`
	UnitID         *uuid.UUID `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	CategoryID     *uuid.UUID `gorm:"type:uuid;index" json:"category_id,omitempty"`

	Priority          OccurrencePriority `gorm:"size:20;not null;uniqueIndex:idx_occ_sla_org_priority_scope,where:department_id IS NULL AND unit_id IS NULL AND category_id IS NULL AND deleted_at IS NULL" json:"priority"`
	ResponseMinutes   int                `gorm:"not null" json:"response_minutes"`
	ResolutionMinutes int                `gorm:"not null" json:"resolution_minutes"`
}

func (OccurrenceSLAPolicy) TableName() string { return "occurrence_sla_policies" }
```

- [ ] **Step 2: Register for migration**

In `GetMigrationModels()`, after `OccurrenceCategory`:

```go
		{"OccurrenceCategory", &models.OccurrenceCategory{}},
		{"OccurrenceSLAPolicy", &models.OccurrenceSLAPolicy{}},
```

- [ ] **Step 3: Add permission resource**

Constants:

```go
	ResourceOccurrenceCategories    = "occurrences.categories"
	ResourceOccurrenceSLAPolicies   = "occurrences.sla_policies"
```

`DefaultPermissions()` (no delete action — policies are upserted per priority, never removed):

```go
		{Resource: ResourceOccurrenceCategories, Action: ActionDelete, Description: "Delete occurrence categories"},
		{Resource: ResourceOccurrenceSLAPolicies, Action: ActionRead, Description: "View occurrence SLA policies"},
		{Resource: ResourceOccurrenceSLAPolicies, Action: ActionWrite, Description: "Edit occurrence SLA policies"},
```

`managerPermissions`:

```go
		"occurrences.categories:read", "occurrences.categories:write", "occurrences.categories:delete",
		"occurrences.sla_policies:read", "occurrences.sla_policies:write",
```

- [ ] **Step 4: Write the seed + lookup helpers and the handlers**

```go
// internal/handlers/occurrence_sla_policies.go
package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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
```

- [ ] **Step 5: Register routes**

```go
	// CRM — políticas de SLA
	g.GET("/api/occurrence-sla-policies", app.ListOccurrenceSLAPolicies)
	g.PUT("/api/occurrence-sla-policies/{priority}", app.UpsertOccurrenceSLAPolicy)
```

- [ ] **Step 6: Write the failing tests**

```go
// internal/handlers/occurrence_sla_policies_test.go
package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestOccurrenceSLAPolicies_SeedsDefaultsOnFirstRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListOccurrenceSLAPolicies(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var count int64
	app.DB.Model(&models.OccurrenceSLAPolicy{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.EqualValues(t, 4, count)
}

func TestOccurrenceSLAPolicies_UpsertChangesOnlyThatPriority(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"response_minutes": 15, "resolution_minutes": 60})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "priority", "urgent")
	require.NoError(t, app.UpsertOccurrenceSLAPolicy(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	urgent, err := app.getSLAPolicy(org.ID, models.OccurrencePriorityUrgent)
	require.NoError(t, err)
	assert.Equal(t, 15, urgent.ResponseMinutes)

	normal, err := app.getSLAPolicy(org.ID, models.OccurrencePriorityNormal)
	require.NoError(t, err)
	assert.Equal(t, 240, normal.ResponseMinutes, "unrelated priority must be untouched")
}

func TestOccurrenceSLAPolicies_RejectedForAgent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"response_minutes": 5, "resolution_minutes": 10})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "priority", "urgent")
	require.NoError(t, app.UpsertOccurrenceSLAPolicy(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

// This is the test that matters most: it proves the partial unique index
// leaves room for the future department/unit/category-scoped phase without
// a schema change, by inserting a more specific row next to the org-wide one.
func TestOccurrenceSLAPolicies_ScopedRowDoesNotConflictWithOrgWideRow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	require.NoError(t, app.ensureDefaultSLAPolicies(org.ID))

	department := models.Department{OrganizationID: org.ID, Name: "Logística"}
	require.NoError(t, app.DB.Create(&department).Error)

	scoped := models.OccurrenceSLAPolicy{
		OrganizationID: org.ID, DepartmentID: &department.ID,
		Priority: models.OccurrencePriorityUrgent, ResponseMinutes: 10, ResolutionMinutes: 30,
	}
	require.NoError(t, app.DB.Create(&scoped).Error, "a department-scoped row must not conflict with the org-wide one")

	var count int64
	app.DB.Model(&models.OccurrenceSLAPolicy{}).
		Where("organization_id = ? AND priority = ?", org.ID, models.OccurrencePriorityUrgent).
		Count(&count)
	assert.EqualValues(t, 2, count, "org-wide row plus the new department-scoped row")
}
```

- [ ] **Step 7: Run tests, then commit**

Run: `go test ./internal/handlers/... -run 'TestOccurrenceSLAPolicies_' -v`
Expected: all PASS.

```bash
git add internal/models/occurrence_sla_policy.go internal/models/roles.go internal/database/postgres.go \
        internal/handlers/occurrence_sla_policies.go cmd/whatomate/main.go \
        internal/handlers/occurrence_sla_policies_test.go
git commit -m "$(cat <<'EOF'
feat(crm): SLA policy per priority, schema ready for narrower scopes

Table carries department_id/unit_id/category_id from day one; this
phase only creates and matches rows where all three are null. The
partial unique index is what lets a future scoped row coexist with
the org-wide one without a migration.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Occurrence SLA tracking fields + deadline computation

**Files:**
- Modify: `internal/models/occurrences.go` (`Occurrence` struct — embed `SLATracking`, add `FirstResponseAt`/`FirstResponseByID`)
- Modify: `internal/handlers/occurrences.go` (`CreateOccurrence` computes deadlines; `UpdateOccurrence` recomputes on priority change)
- Test: `internal/handlers/occurrence_sla_tracking_test.go`

**Interfaces:**
- Consumes: `models.SLATracking` (existing, `internal/models/chatbot.go:294`), `a.getSLAPolicy` (Task 5).
- Produces: `Occurrence.SLA.ResponseDeadline/ResolutionDeadline/Breached/BreachedAt`, `Occurrence.FirstResponseAt`, `Occurrence.FirstResponseByID` — consumed by Task 7 (`/reply` sets `FirstResponseAt`) and Task 8 (SLA processor reads/writes `Breached`/`BreachedAt`).

- [ ] **Step 1: Add fields to `Occurrence`**

In `internal/models/occurrences.go`, after the `Source` field added in Task 4:

```go
	// SLA embeds the same struct AgentTransfer already uses for chat SLA
	// (internal/models/chatbot.go:294) — response/resolution deadline, breach
	// flag and timestamp. FirstResponseAt/FirstResponseByID are Occurrence-only:
	// they are set exclusively by the "reply" event (Task 7), never by an
	// internal note — that distinction is the whole point of first-response SLA.
	SLA               SLATracking `gorm:"embedded"`
	FirstResponseAt   *time.Time  `json:"first_response_at,omitempty"`
	FirstResponseByID *uuid.UUID  `gorm:"type:uuid" json:"first_response_by_id,omitempty"`

	FirstResponseBy *User `gorm:"foreignKey:FirstResponseByID" json:"first_response_by,omitempty"`
```

- [ ] **Step 2: Compute deadlines in `CreateOccurrence`**

Right after the `priority := models.OccurrencePriority(req.Priority)` / default-to-normal block (`occurrences.go:174-177`), before the `occ := models.Occurrence{...}` literal:

```go
	var responseDeadline, resolutionDeadline *time.Time
	if policy, err := a.getSLAPolicy(orgID, priority); err != nil {
		a.Log.Error("Failed to resolve SLA policy", "error", err, "organization_id", orgID)
	} else {
		now := time.Now()
		rd := now.Add(time.Duration(policy.ResponseMinutes) * time.Minute)
		xd := now.Add(time.Duration(policy.ResolutionMinutes) * time.Minute)
		responseDeadline, resolutionDeadline = &rd, &xd
	}
```

Then, inside the `occ := models.Occurrence{...}` literal, add:

```go
	occ := models.Occurrence{
		OrganizationID: orgID,
		ContactID:      contactID,
		Title:          req.Title,
		Description:    req.Description,
		StageID:        stage.ID,
		Priority:       priority,
		OpenedByUserID: userID,
		SLA: models.SLATracking{
			ResponseDeadline:   responseDeadline,
			ResolutionDeadline: resolutionDeadline,
		},
	}
```

Note the failure mode deliberately chosen here: if `getSLAPolicy` errors (e.g. a database hiccup), the occurrence is still created — just without deadlines — rather than failing the whole create. An organisation should never be unable to open a case because its SLA policy lookup had a transient error.

- [ ] **Step 3: Recompute deadlines on priority change in `UpdateOccurrence`**

Replace the existing block:

```go
	if req.Priority != "" {
		updates["priority"] = req.Priority
	}
```

with:

```go
	if req.Priority != "" && req.Priority != string(occ.Priority) {
		updates["priority"] = req.Priority
		if policy, err := a.getSLAPolicy(orgID, models.OccurrencePriority(req.Priority)); err != nil {
			a.Log.Error("Failed to resolve SLA policy on priority change", "error", err, "occurrence", occ.ID)
		} else {
			now := time.Now()
			rd := now.Add(time.Duration(policy.ResponseMinutes) * time.Minute)
			xd := now.Add(time.Duration(policy.ResolutionMinutes) * time.Minute)
			updates["sla_response_deadline"] = rd
			updates["sla_resolution_deadline"] = xd
		}
	}
```

- [ ] **Step 4: Write the failing tests**

```go
// internal/handlers/occurrence_sla_tracking_test.go
package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOccurrence_ComputesDeadlinesFromPriority(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Urgente", "priority": "urgent",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.SLA.ResponseDeadline)
	require.NotNil(t, occ.SLA.ResolutionDeadline)
	// Default urgent policy: 30min response, 4h resolution (see defaultSLAPolicies).
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), *occ.SLA.ResponseDeadline, 5*time.Second)
	assert.WithinDuration(t, time.Now().Add(4*time.Hour), *occ.SLA.ResolutionDeadline, 5*time.Second)
}

func TestUpdateOccurrence_RecomputesDeadlinesOnPriorityChange(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	createReq := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Baixa prioridade", "priority": "low",
	})
	testutil.SetAuthContext(createReq, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(createReq))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	originalDeadline := *occ.SLA.ResponseDeadline

	updateReq := testutil.NewJSONRequest(t, map[string]any{"title": occ.Title, "priority": "urgent"})
	testutil.SetAuthContext(updateReq, org.ID, user.ID)
	testutil.SetPathParam(updateReq, "id", occ.ID.String())
	require.NoError(t, app.UpdateOccurrence(updateReq))

	require.NoError(t, app.DB.First(&occ, "id = ?", occ.ID).Error)
	assert.True(t, occ.SLA.ResponseDeadline.Before(originalDeadline),
		"urgent's response deadline must be sooner than low's")
}
```

- [ ] **Step 5: Run tests, then commit**

Run: `go test ./internal/handlers/... -run 'TestCreateOccurrence_ComputesDeadlines|TestUpdateOccurrence_RecomputesDeadlines' -v`
Expected: PASS.

```bash
git add internal/models/occurrences.go internal/handlers/occurrences.go \
        internal/handlers/occurrence_sla_tracking_test.go
git commit -m "$(cat <<'EOF'
feat(crm): compute occurrence SLA deadlines from priority policy

Reuses SLATracking (already embedded in AgentTransfer for chat SLA).
A policy lookup failure never blocks case creation -- the occurrence
opens without deadlines rather than not opening at all.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: `/reply` endpoint — public reply distinct from internal note

**Files:**
- Modify: `internal/models/occurrences.go` (new `OccurrenceEventReply` constant)
- Create: `internal/handlers/occurrence_reply.go`
- Modify: `cmd/whatomate/main.go`
- Test: `internal/handlers/occurrence_reply_test.go`

**Interfaces:**
- Consumes: `a.SendOutgoingMessage` (existing, `internal/handlers/messages.go:150`), `serviceWindowOpen` (existing, `internal/handlers/occurrence_protocol.go`), `a.loadAuthorizedOccurrence` (existing).
- Produces: `models.OccurrenceEventReply`, handler `ReplyToOccurrence` — sets `Occurrence.FirstResponseAt`/`FirstResponseByID` the first time only.

- [ ] **Step 1: Add the event type**

In `internal/models/occurrences.go`, inside the `OccurrenceEventType` const block:

```go
const (
	OccurrenceEventOpened       OccurrenceEventType = "opened"
	OccurrenceEventNote         OccurrenceEventType = "note"
	OccurrenceEventReply        OccurrenceEventType = "reply"
	OccurrenceEventStageChange  OccurrenceEventType = "stage_change"
	OccurrenceEventAssignment   OccurrenceEventType = "assignment"
	OccurrenceEventProtocolSent OccurrenceEventType = "protocol_sent"
	OccurrenceEventClosed       OccurrenceEventType = "closed"
)
```

- [ ] **Step 2: Write the handler, mirroring `SendOccurrenceProtocol` exactly**

```go
// internal/handlers/occurrence_reply.go
package handlers

import (
	"context"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// OccurrenceReplyRequest is the body for sending a public reply.
type OccurrenceReplyRequest struct {
	Content string `json:"content"`
}

// ReplyToOccurrence sends a message to the contact and records it as a public
// reply — distinct from CreateOccurrenceEvent's internal note. This is the
// only thing that sets FirstResponseAt: an internal note must never stop the
// first-response SLA clock, and an explicit action here avoids the ambiguity
// of guessing which of a contact's several open occurrences an ordinary chat
// message was meant to answer.
func (a *App) ReplyToOccurrence(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}

	occ, err := a.loadAuthorizedOccurrence(r, orgID, userID, true)
	if err != nil {
		return nil
	}

	var req OccurrenceReplyRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Content == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "content is required", nil, "")
	}

	contact := occ.Contact
	if !serviceWindowOpen(contact) {
		return r.SendErrorEnvelope(fasthttp.StatusUnprocessableEntity,
			"The 24-hour service window is closed; only templates can be sent", nil, "")
	}

	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", orgID, contact.WhatsAppAccount).
		First(&account).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"WhatsApp account not found for this contact", nil, "")
	}

	if _, err := a.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: &account,
		Contact: contact,
		Type:    models.MessageTypeText,
		Content: req.Content,
	}, DefaultSendOptions()); err != nil {
		a.Log.Error("Failed to send occurrence reply", "error", err, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to send reply", nil, "")
	}

	event := models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventReply,
		Content:        req.Content,
		CreatedByID:    &userID,
	}
	if err := a.DB.Create(&event).Error; err != nil {
		a.Log.Error("Failed to record occurrence reply event", "error", err, "occurrence", occ.ID)
	}

	// Only the first reply sets these — a second reply on an already-answered
	// case must not push the SLA clock forward again.
	if occ.FirstResponseAt == nil {
		if err := a.DB.Model(occ).Updates(map[string]any{
			"first_response_at":    event.CreatedAt,
			"first_response_by_id": userID,
		}).Error; err != nil {
			a.Log.Error("Failed to stamp first response", "error", err, "occurrence", occ.ID)
		}
	}

	return r.SendEnvelope(map[string]any{"sent": true})
}
```

- [ ] **Step 3: Register the route**

```go
	g.POST("/api/occurrences/{id}/send-protocol", app.SendOccurrenceProtocol)
	g.POST("/api/occurrences/{id}/reply", app.ReplyToOccurrence)
```

- [ ] **Step 4: Write the failing tests**

Reuses the mock-WhatsApp-server test app (`newMsgTestApp`, `createTestAccount`) from `internal/handlers/messages_test.go:127-156`, same as `occurrence_send_test.go`.

```go
// internal/handlers/occurrence_reply_test.go
package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestReplyToOccurrence_RejectedOutsideServiceWindow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	old := time.Now().Add(-30 * time.Hour)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", old).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Fora da janela",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, map[string]any{"content": "Olá, tudo bem?"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(req))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, testutil.GetResponseStatusCode(req))

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.Nil(t, reloaded.FirstResponseAt, "a rejected reply must not stamp first response")
}

func TestReplyToOccurrence_SetsFirstResponseOnlyOnce(t *testing.T) {
	mockServer := newMockWhatsAppServer(t)
	defer mockServer.Close()
	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	createTestAccount(t, app, org.ID)

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	recent := time.Now().Add(-1 * time.Hour)
	require.NoError(t, app.DB.Model(contact).Update("last_inbound_at", recent).Error)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Peça quebrada",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	first := testutil.NewJSONRequest(t, map[string]any{"content": "Já estamos verificando"})
	testutil.SetAuthContext(first, org.ID, user.ID)
	testutil.SetPathParam(first, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(first))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	var afterFirst models.Occurrence
	require.NoError(t, app.DB.First(&afterFirst, "id = ?", occ.ID).Error)
	require.NotNil(t, afterFirst.FirstResponseAt)
	firstStamp := *afterFirst.FirstResponseAt

	second := testutil.NewJSONRequest(t, map[string]any{"content": "Update: chegou hoje"})
	testutil.SetAuthContext(second, org.ID, user.ID)
	testutil.SetPathParam(second, "id", occ.ID.String())
	require.NoError(t, app.ReplyToOccurrence(second))

	var afterSecond models.Occurrence
	require.NoError(t, app.DB.First(&afterSecond, "id = ?", occ.ID).Error)
	assert.Equal(t, firstStamp, *afterSecond.FirstResponseAt, "second reply must not move first_response_at")

	var replyEvents int64
	app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventReply).Count(&replyEvents)
	assert.EqualValues(t, 2, replyEvents)
}

func TestCreateOccurrenceEvent_NoteNeverSetsFirstResponse(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Caso comum",
		StageID: stage.ID, OpenedByUserID: user.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, map[string]any{"content": "Aguardando fornecedor confirmar"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.CreateOccurrenceEvent(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.Nil(t, reloaded.FirstResponseAt, "an internal note must never set first_response_at")
}
```

`newMockWhatsAppServer` is whatever the existing `occurrence_send_test.go` uses to construct its mock server — check that file's setup (`internal/handlers/occurrence_send_test.go`, the successful-send test around line 109) for the exact helper name and copy its construction verbatim if it differs from `newMockWhatsAppServer`; don't invent a second mock server helper.

- [ ] **Step 5: Run tests, then commit**

Run: `go test ./internal/handlers/... -run 'TestReplyToOccurrence_|TestCreateOccurrenceEvent_NoteNeverSetsFirstResponse' -v`
Expected: PASS.

```bash
git add internal/models/occurrences.go internal/handlers/occurrence_reply.go cmd/whatomate/main.go \
        internal/handlers/occurrence_reply_test.go
git commit -m "$(cat <<'EOF'
feat(crm): add public reply endpoint, distinct from internal note

Mirrors /send-protocol exactly (24h window, SendOutgoingMessage
reused unmodified). Sets first_response_at on the first reply only --
an internal note never touches it, and a second reply never moves it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: SLA processor — mark breached occurrences

**Files:**
- Modify: `internal/models/chatbot.go:31-40` (`SLAConfig` gets `OccurrenceEnabled`)
- Modify: `internal/handlers/cache.go:361-394` (`getSLAEnabledSettingsCached` WHERE clause)
- Modify: `internal/handlers/sla_processor.go` (new `processOccurrenceSLA`, hooked into `processOrganizationSLA`)
- Test: `internal/handlers/sla_processor_occurrence_test.go`

**Interfaces:**
- Consumes: `models.Occurrence.SLA` (Task 6), `getSLAEnabledSettingsCached` (existing).
- Produces: `SLAConfig.OccurrenceEnabled`, `(*SLAProcessor).processOccurrenceSLA(orgID uuid.UUID, now time.Time)`.

- [ ] **Step 1: Add the gate field to `SLAConfig`**

In `internal/models/chatbot.go`, inside `type SLAConfig struct`, after `EscalationNotifyIDs`:

```go
type SLAConfig struct {
	Enabled             bool        `gorm:"column:sla_enabled;default:false" json:"sla_enabled"`
	ResponseMinutes     int         `gorm:"column:sla_response_minutes;default:15" json:"sla_response_minutes"`
	ResolutionMinutes   int         `gorm:"column:sla_resolution_minutes;default:60" json:"sla_resolution_minutes"`
	EscalationMinutes   int         `gorm:"column:sla_escalation_minutes;default:30" json:"sla_escalation_minutes"`
	AutoCloseHours      int         `gorm:"column:sla_auto_close_hours;default:24" json:"sla_auto_close_hours"`
	AutoCloseMessage    string      `gorm:"column:sla_auto_close_message;type:text" json:"sla_auto_close_message"`
	WarningMessage      string      `gorm:"column:sla_warning_message;type:text" json:"sla_warning_message"`
	EscalationNotifyIDs StringArray `gorm:"column:sla_escalation_notify_ids;type:jsonb;default:'[]'" json:"sla_escalation_notify_ids"`
	// OccurrenceEnabled gates SLA breach-marking for Occurrences, independent of
	// Enabled (that one is chat's own SLA). Mirrors ClientInactivityConfig's
	// CloseInactiveAttendances: its own switch, so enabling chat SLA never
	// silently turns on occurrence SLA processing for an org that never asked.
	OccurrenceEnabled bool `gorm:"column:occurrence_sla_enabled;default:false" json:"occurrence_sla_enabled"`
}
```

- [ ] **Step 2: Extend the cached-settings query**

In `internal/handlers/cache.go`, inside `getSLAEnabledSettingsCached` (`cache.go:361-394`):

```go
	var settings []models.ChatbotSettings
	if err := a.DB.Where("sla_enabled = ? OR close_inactive_attendances = ? OR occurrence_sla_enabled = ?",
		true, true, true).Find(&settings).Error; err != nil {
		return nil, err
	}
```

This is the correctness-critical part: without extending this WHERE clause, an organisation that turns on `occurrence_sla_enabled` alone (chat SLA off, inactivity sweep off) would never be loaded by the processor loop at all, and `processOccurrenceSLA` — however correct in isolation — would simply never run for it.

- [ ] **Step 3: Add `processOccurrenceSLA` and hook it in**

In `internal/handlers/sla_processor.go`, inside `processOrganizationSLA` (`sla_processor.go:75-116`), right after the existing `ClientInactivity.CloseInactiveAttendances` block (mirroring its independence from the `settings.SLA.Enabled` gate above it):

```go
	// Human-attendance inactivity sweep — decoupled from SLA. ...
	if settings.ClientInactivity.CloseInactiveAttendances {
		p.closeInactiveAttendances(orgID, settings, now)
	}

	// Occurrence SLA — its own gate, independent of chat's SLA.Enabled, same
	// reasoning as CloseInactiveAttendances above.
	if settings.SLA.OccurrenceEnabled {
		p.processOccurrenceSLA(orgID, now)
	}
}
```

Then add the new method at the end of the file:

```go
// processOccurrenceSLA marks breached=true/breached_at=now for occurrences
// past their response or resolution deadline. It only marks — no auto-close,
// no escalation, no notification for occurrences in this phase (see the
// approved spec's non-objectives).
func (p *SLAProcessor) processOccurrenceSLA(orgID uuid.UUID, now time.Time) {
	// Response breach: past the response deadline, no public reply yet, not
	// already marked breached (this call runs every tick — without the
	// sla_breached=false guard it would rewrite breached_at forever).
	if err := p.app.DB.Model(&models.Occurrence{}).
		Where("organization_id = ? AND sla_response_deadline < ? AND first_response_at IS NULL AND sla_breached = ?",
			orgID, now, false).
		Updates(map[string]any{"sla_breached": true, "sla_breached_at": now}).Error; err != nil {
		p.app.Log.Error("Failed to mark occurrence response SLA breach", "error", err, "organization_id", orgID)
	}

	// Resolution breach: past the resolution deadline, still open (closed_at
	// nil), not already marked.
	if err := p.app.DB.Model(&models.Occurrence{}).
		Where("organization_id = ? AND sla_resolution_deadline < ? AND closed_at IS NULL AND sla_breached = ?",
			orgID, now, false).
		Updates(map[string]any{"sla_breached": true, "sla_breached_at": now}).Error; err != nil {
		p.app.Log.Error("Failed to mark occurrence resolution SLA breach", "error", err, "organization_id", orgID)
	}
}
```

- [ ] **Step 4: Write the failing tests**

```go
// internal/handlers/sla_processor_occurrence_test.go
package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessOccurrenceSLA_MarksResponseBreach(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Hour)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Sem resposta",
		StageID: stage.ID, OpenedByUserID: user.ID,
		SLA: models.SLATracking{ResponseDeadline: &past},
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	processor := NewSLAProcessor(app, time.Minute)
	processor.processOccurrenceSLA(org.ID, time.Now())

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.True(t, reloaded.SLA.Breached)
	assert.NotNil(t, reloaded.SLA.BreachedAt)
}

func TestProcessOccurrenceSLA_DoesNotBreachWhenAlreadyReplied(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)

	past := time.Now().Add(-1 * time.Hour)
	replied := time.Now().Add(-30 * time.Minute)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "Já respondido",
		StageID: stage.ID, OpenedByUserID: user.ID,
		SLA: models.SLATracking{ResponseDeadline: &past}, FirstResponseAt: &replied,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	processor := NewSLAProcessor(app, time.Minute)
	processor.processOccurrenceSLA(org.ID, time.Now())

	var reloaded models.Occurrence
	require.NoError(t, app.DB.First(&reloaded, "id = ?", occ.ID).Error)
	assert.False(t, reloaded.SLA.Breached, "a case with a reply before the deadline must not be marked breached")
}

func TestGetSLAEnabledSettingsCached_IncludesOccurrenceOnlyOrgs(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA:            models.SLAConfig{Enabled: false, OccurrenceEnabled: true},
	}
	require.NoError(t, app.DB.Create(&settings).Error)

	loaded, err := app.getSLAEnabledSettingsCached()
	require.NoError(t, err)

	var found bool
	for _, s := range loaded {
		if s.OrganizationID == org.ID {
			found = true
		}
	}
	assert.True(t, found, "an org with only occurrence_sla_enabled must still be loaded by the SLA processor loop")
}
```

- [ ] **Step 5: Run tests, then commit**

Run: `go test ./internal/handlers/... -run 'TestProcessOccurrenceSLA_|TestGetSLAEnabledSettingsCached_' -v`
Expected: PASS.

Run the full package once to confirm nothing in the prior seven tasks regressed: `go test ./internal/... -v 2>&1 | tail -100`
Expected: no FAIL lines.

```bash
git add internal/models/chatbot.go internal/handlers/cache.go internal/handlers/sla_processor.go \
        internal/handlers/sla_processor_occurrence_test.go
git commit -m "$(cat <<'EOF'
feat(crm): mark occurrence SLA breaches in the existing processor loop

Own gate (occurrence_sla_enabled), independent of chat's SLA.Enabled.
Extends getSLAEnabledSettingsCached's WHERE clause so an org that only
wants occurrence SLA is still loaded -- without this the new gate
would silently never fire.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**Spec coverage** (against `docs/superpowers/specs/2026-09-04-helpdesk-unidade-departamento-sla-design.md`):
- §4 `units`/`departments` → Task 1. §4 `Team` columns → Task 2. §4 `occurrence_categories` → Task 3. §4 `Occurrence` columns (unit/department/category/source) → Task 4. §4 `occurrence_sla_policies` + seed → Task 5. §4 `Occurrence` SLA fields → Task 6. §4 `occurrence_events` new type + §5 reply vs. note → Task 7. §4 `OccurrenceSLAEnabled` + §6 processor → Task 8.
- §7 "Fila = filtro" → the `unit_id`/`department_id`/`category_id` filters added to `ListOccurrences` in Task 4 are exactly this; no separate task needed since there is no separate table.
- §11 Go verification items are covered: protocol numbering under concurrency and stage transitions were already tested in Phase 1 (untouched here); this plan's own new surfaces (SLA policy partial index, inheritance, reply-vs-note, breach marking) each have an explicit test above.
- §9 Frontend and §14 Fase 4 roadmap are explicitly out of this plan (Scope Note).

**Placeholder scan:** no TBD/TODO; the one deliberately open item (Step 4 of Task 7, "check that file's setup... copy its construction verbatim if it differs") is not a placeholder for undecided logic — it's an instruction to verify one existing helper's exact name before use, since this plan was written without re-reading `occurrence_send_test.go` byte-for-byte a second time. Resolve it by reading that file's top before writing the test, not by guessing.

**Type consistency:** `models.Unit`/`models.Department` field names (`UnitID`, `DepartmentID`) are used identically across Tasks 1, 2, 4, 5. `getSLAPolicy`/`ensureDefaultSLAPolicies` signatures introduced in Task 5 are called unchanged in Tasks 6 and 8. `OccurrenceEventReply` introduced in Task 7 matches the constant name used nowhere else. `SLATracking` field names (`ResponseDeadline`, `ResolutionDeadline`, `Breached`, `BreachedAt`) match `chatbot.go:294` exactly, not renamed anywhere in Task 6 or 8.
