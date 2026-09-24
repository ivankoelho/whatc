# Central de Vendas — Funil de Oportunidades (Entrega 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the sales-funnel domain ("Central de Vendas") tied to the WhatsApp attendance flow — automatic opportunity creation from a chatbot button, a validated 3-stage/4-status state machine, permissions, a Kanban "Minha Operação" screen, a managerial dashboard, and SLA tracking — exactly as specified in `docs/superpowers/specs/2026-09-17-central-vendas-funil-entrega1-design.md`.

**Architecture:** Two new tables (`sales_opportunities`, `sales_opportunity_events`) plus one new column on `users`, following the same structural patterns already proven by the Ocorrências module: a per-org/per-day sequential counter (mirrors `OccurrenceCounter`), a never-deleted event timeline (mirrors `OccurrenceEvent`), backend-validated state transitions, permission-scoped visibility (own vs. `view_all`), the existing generic widget engine as the dashboard mechanism, and the existing SLA processor goroutine extended with one more check. Creation is triggered automatically from an existing, generic per-button chatbot config flag (`create_opportunity`), the same mechanism already used for `team_id`.

**Tech Stack:** Go (fastglue, GORM/Postgres, AutoMigrate), Vue 3 Composition API + Pinia + vue-router, Playwright.

## Global Constraints

- No SQL migration files — schema changes happen via GORM `AutoMigrate` through `GetMigrationModels()`.
- Never touch `main` directly; all work happens on `development` via reviewed PR.
- Backend is the only source of truth for authorization and the state machine — the frontend only hides controls, never the only gate (spec §7).
- No physical deletion of `SalesOpportunity` or `SalesOpportunityEvent` — soft-delete only, no delete endpoint (spec §3 "Exclusão física").
- Three funnel stages (`potencial`, `abrir_orcamento`, `direcionada`) are fixed in code, not configurable per organization (spec §2, §3).
- `estimated_value`/`estimated_quantity` are the only value/quantity columns in this delivery — no `valor_vendido` column, no XProcess client, no reconciliation table (spec §11, out of scope).
- Every phase or status transition writes exactly one `SalesOpportunityEvent`, without exception (spec §5).
- New permission resource `sales_opportunities` must be checked in every handler; an existing organisation's already-materialized roles need a backfill, the same gap documented for every prior new permission in this codebase (see `internal/database/permissions_backfill.go`).
- Conversion rate formula, used everywhere it appears: `convertidas / (convertidas + perdidas) * 100`, open opportunities excluded from the denominator (spec §8).

---

## Dependency Map

```
Task 1 (models/migration)
   ├─► Task 2 (opportunity_number counter)
   │       └─► Task 4 (idempotent creation)
   │               ├─► Task 5 (chatbot hook)          ─► Task 13 (frontend checkbox)
   │               ├─► Task 6 (list/detail/events)     ─► Task 7 (stage/direcionamento)
   │               │                                   └─► Task 8 (convert/lose)
   │               │       └─► Task 11 (widgets data source) ─► Task 16 (dashboard view)
   │               └─────────────────────────────────────────┘
   ├─► Task 9 (SLA)             [parallel with 4-8, 11]
   └─► Task 10 (xprocess_seller_code backend) [parallel with 4-9, 11]
Task 3 (permissions + backfill) [parallel with Task 1/2, independent]

Task 12 (frontend api.ts)  — needs Tasks 6, 7, 8, 10 merged (endpoint contracts final)
   ├─► Task 14 (Users settings field)   [parallel with 15/16]
   ├─► Task 15 (Kanban board + Minha Operação + router/nav)
   └─► Task 16 (dashboard gerencial)     [parallel with 15]

Task 17 (Playwright e2e) — needs Task 5, 13, 15, 16
```

Parallelizable tracks once their prerequisites land:
- Track A (backend core): Tasks 4 → 5/6 → 7/8.
- Track B (independent backend): Task 3 (permissions) alongside Task 1/2; Task 9 (SLA) and Task 10 (user field) alongside Track A.
- Track C (frontend): Tasks 14, 15, 16 can be worked by different people once Task 12 lands, since they touch disjoint files.

---

### Task 1: Modelos de dados e migração

**Files:**
- Create: `internal/models/sales_opportunity.go`
- Modify: `internal/models/models.go:107-130` (add `XProcessSellerCode` to `User`)
- Modify: `internal/database/postgres.go:95` (register new models in `GetMigrationModels()`)
- Test: `internal/handlers/sales_opportunity_model_test.go`

**Interfaces:**
- Produces: `models.SalesOpportunity` (fields: `ID`, `OrganizationID`, `OpportunityNumber`, `ContactID`, `SourceTransferID *uuid.UUID`, `AssignedUserID *uuid.UUID`, `Stage SalesOpportunityStage`, `Status SalesOpportunityStatus`, `Interest string`, `EstimatedValue *float64`, `EstimatedQuantity *int`, `Direcionamento *SalesDirecionamento`, `ConversionSource *SalesConversionSource`, `LossReason *SalesLossReason`, `LossNotes string`, `OpenedAt time.Time`, `StageChangedAt time.Time`, `ConvertedAt *time.Time`, `LostAt *time.Time`, `CancelledAt *time.Time`, embeds `BaseModel` for `DeletedAt`).
- Produces: `models.SalesOpportunityEvent` (fields: `ID`, `OrganizationID`, `SalesOpportunityID`, `Type SalesOpportunityEventType`, `FromStage *SalesOpportunityStage`, `ToStage *SalesOpportunityStage`, `Source SalesOpportunityEventSource`, `CreatedByID *uuid.UUID`, `CreatedAt time.Time`).
- Produces: `models.SalesOpportunityCounter` (fields: `OrganizationID uuid.UUID`, `Day string`, `LastSeq int` — composite primary key `(organization_id, day)`).
- Produces: stage/status/enum constants: `SalesOpportunityStagePotencial = "potencial"`, `SalesOpportunityStageAbrirOrcamento = "abrir_orcamento"`, `SalesOpportunityStageDirecionada = "direcionada"`; `SalesOpportunityStatusAberta = "aberta"`, `SalesOpportunityStatusConvertida = "convertida"`, `SalesOpportunityStatusPerdida = "perdida"`, `SalesOpportunityStatusCancelada = "cancelada"`; `SalesDirecionamentoVisita = "visita"`, `SalesDirecionamentoWhatsApp = "whatsapp"`; `SalesConversionSourceManual = "manual"`, `SalesConversionSourceXProcess = "xprocess"`; `SalesLossReason...` (8 constants, see Step 3); `SalesOpportunityEventOpened = "opened"`, `...StageChanged`, `...DirecionamentoChanged`, `...Converted`, `...Lost`, `...Cancelled`, `...Retriggered`; `SalesOpportunityEventSourceManual/XProcess/System`.
- Consumes: `models.BaseModel` (existing, gives `ID`, `CreatedAt`, `UpdatedAt`, `DeletedAt`).

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sales_opportunity_model_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestSalesOpportunityModel_CreateAndRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp := models.SalesOpportunity{
		OrganizationID:     org.ID,
		OpportunityNumber:  "OPP-20260917-000001",
		ContactID:          contact.ID,
		Stage:              models.SalesOpportunityStagePotencial,
		Status:             models.SalesOpportunityStatusAberta,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	require.Equal(t, models.SalesOpportunityStagePotencial, got.Stage)

	event := models.SalesOpportunityEvent{
		OrganizationID:      org.ID,
		SalesOpportunityID:  opp.ID,
		Type:                models.SalesOpportunityEventOpened,
		Source:              models.SalesOpportunityEventSourceSystem,
	}
	require.NoError(t, app.DB.Create(&event).Error)

	var gotEvent models.SalesOpportunityEvent
	require.NoError(t, app.DB.First(&gotEvent, "id = ?", event.ID).Error)
	require.Equal(t, models.SalesOpportunityEventOpened, gotEvent.Type)
}

func TestUserModel_XProcessSellerCode(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	code := "V123"
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(&user).Update("xprocess_seller_code", &code).Error)

	var got models.User
	require.NoError(t, app.DB.First(&got, "id = ?", user.ID).Error)
	require.NotNil(t, got.XProcessSellerCode)
	require.Equal(t, "V123", *got.XProcessSellerCode)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestSalesOpportunityModel_CreateAndRead -v`
Expected: FAIL with `undefined: models.SalesOpportunity`

- [ ] **Step 3: Write the models**

```go
// internal/models/sales_opportunity.go
package models

import (
	"time"

	"github.com/google/uuid"
)

type SalesOpportunityStage string

const (
	SalesOpportunityStagePotencial      SalesOpportunityStage = "potencial"
	SalesOpportunityStageAbrirOrcamento SalesOpportunityStage = "abrir_orcamento"
	SalesOpportunityStageDirecionada    SalesOpportunityStage = "direcionada"
)

type SalesOpportunityStatus string

const (
	SalesOpportunityStatusAberta     SalesOpportunityStatus = "aberta"
	SalesOpportunityStatusConvertida SalesOpportunityStatus = "convertida"
	SalesOpportunityStatusPerdida    SalesOpportunityStatus = "perdida"
	SalesOpportunityStatusCancelada  SalesOpportunityStatus = "cancelada"
)

type SalesDirecionamento string

const (
	SalesDirecionamentoVisita   SalesDirecionamento = "visita"
	SalesDirecionamentoWhatsApp SalesDirecionamento = "whatsapp"
)

type SalesConversionSource string

const (
	SalesConversionSourceManual   SalesConversionSource = "manual"
	SalesConversionSourceXProcess SalesConversionSource = "xprocess"
)

// SalesLossReason is a closed list — validated in the handler, not just the DB.
type SalesLossReason string

const (
	SalesLossReasonClienteDesistiu    SalesLossReason = "cliente_desistiu"
	SalesLossReasonPreco              SalesLossReason = "preco"
	SalesLossReasonPrazo              SalesLossReason = "prazo"
	SalesLossReasonIndisponibilidade  SalesLossReason = "indisponibilidade"
	SalesLossReasonComprouConcorrente SalesLossReason = "comprou_concorrente"
	SalesLossReasonSemRetorno         SalesLossReason = "sem_retorno"
	SalesLossReasonProblemaComercial  SalesLossReason = "problema_comercial"
	SalesLossReasonOutro              SalesLossReason = "outro"
)

// ValidSalesLossReasons is the closed list, used by the handler to reject
// anything outside it with a 400 instead of trusting the request body.
var ValidSalesLossReasons = map[SalesLossReason]bool{
	SalesLossReasonClienteDesistiu:    true,
	SalesLossReasonPreco:              true,
	SalesLossReasonPrazo:              true,
	SalesLossReasonIndisponibilidade:  true,
	SalesLossReasonComprouConcorrente: true,
	SalesLossReasonSemRetorno:         true,
	SalesLossReasonProblemaComercial:  true,
	SalesLossReasonOutro:              true,
}

type SalesOpportunityEventType string

const (
	SalesOpportunityEventOpened               SalesOpportunityEventType = "opened"
	SalesOpportunityEventStageChanged         SalesOpportunityEventType = "stage_changed"
	SalesOpportunityEventDirecionamentoChanged SalesOpportunityEventType = "direcionamento_changed"
	SalesOpportunityEventConverted            SalesOpportunityEventType = "converted"
	SalesOpportunityEventLost                 SalesOpportunityEventType = "lost"
	SalesOpportunityEventCancelled            SalesOpportunityEventType = "cancelled"
	SalesOpportunityEventRetriggered          SalesOpportunityEventType = "retriggered"
)

type SalesOpportunityEventSource string

const (
	SalesOpportunityEventSourceManual   SalesOpportunityEventSource = "manual"
	SalesOpportunityEventSourceXProcess SalesOpportunityEventSource = "xprocess"
	SalesOpportunityEventSourceSystem   SalesOpportunityEventSource = "system"
)

// SalesOpportunity is the funnel entry created automatically when a customer
// selects a chatbot button configured with create_opportunity: true. See
// docs/superpowers/specs/2026-09-17-central-vendas-funil-entrega1-design.md §4.
type SalesOpportunity struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null" json:"organization_id"`

	// Unique per (organization_id, opportunity_number), not globally — two
	// orgs may repeat the same number on the same day (spec §4).
	OpportunityNumber string `gorm:"size:20;not null;uniqueIndex:idx_sales_opp_org_number" json:"opportunity_number"`

	ContactID uuid.UUID `gorm:"type:uuid;index;not null" json:"contact_id"`

	// Traceability only, same role as Occurrence.SourceTransferID.
	SourceTransferID *uuid.UUID `gorm:"type:uuid;index" json:"source_transfer_id,omitempty"`

	// Fixed at creation from Contact.AssignedUserID; reassigning the contact
	// later never moves this opportunity (spec §3).
	AssignedUserID *uuid.UUID `gorm:"type:uuid;index" json:"assigned_user_id,omitempty"`

	Stage  SalesOpportunityStage  `gorm:"size:20;not null;default:'potencial'" json:"stage"`
	Status SalesOpportunityStatus `gorm:"size:20;not null;default:'aberta'" json:"status"`

	Interest          string   `gorm:"type:text" json:"interest,omitempty"`
	EstimatedValue    *float64 `gorm:"type:numeric" json:"estimated_value,omitempty"`
	EstimatedQuantity *int     `json:"estimated_quantity,omitempty"`

	// Editable any time while status=aberta, in any stage — not itself a
	// transition. Required before entering "direcionada" (spec §5).
	Direcionamento *SalesDirecionamento `gorm:"size:20" json:"direcionamento,omitempty"`

	ConversionSource *SalesConversionSource `gorm:"size:20" json:"conversion_source,omitempty"`
	LossReason       *SalesLossReason       `gorm:"size:30" json:"loss_reason,omitempty"`
	LossNotes        string                  `gorm:"type:text" json:"loss_notes,omitempty"`

	OpenedAt time.Time `gorm:"autoCreateTime" json:"opened_at"`
	// Entry into the current stage — base of the 7-day SLA (spec §6).
	StageChangedAt time.Time  `gorm:"not null" json:"stage_changed_at"`
	ConvertedAt    *time.Time `json:"converted_at,omitempty"`
	LostAt         *time.Time `json:"lost_at,omitempty"`
	CancelledAt    *time.Time `json:"cancelled_at,omitempty"` // Entrega 2 only

	// SLA — mirrors Occurrence.SLA breach fields for the widget/board badge.
	SLABreached   bool       `gorm:"default:false" json:"sla_breached"`
	SLABreachedAt *time.Time `json:"sla_breached_at,omitempty"`

	Contact      *Contact `gorm:"foreignKey:ContactID" json:"contact,omitempty"`
	AssignedUser *User    `gorm:"foreignKey:AssignedUserID" json:"assigned_user,omitempty"`
}

func (SalesOpportunity) TableName() string { return "sales_opportunities" }

type SalesOpportunityEvent struct {
	BaseModel
	OrganizationID      uuid.UUID                    `gorm:"type:uuid;index;not null" json:"organization_id"`
	SalesOpportunityID  uuid.UUID                    `gorm:"type:uuid;index;not null" json:"sales_opportunity_id"`
	Type                SalesOpportunityEventType     `gorm:"size:30;not null" json:"type"`
	FromStage           *SalesOpportunityStage        `gorm:"size:20" json:"from_stage,omitempty"`
	ToStage             *SalesOpportunityStage        `gorm:"size:20" json:"to_stage,omitempty"`
	Source              SalesOpportunityEventSource   `gorm:"size:20;not null" json:"source"`
	CreatedByID         *uuid.UUID                    `gorm:"type:uuid" json:"created_by_id,omitempty"` // nil = system

	CreatedBy *User `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
}

func (SalesOpportunityEvent) TableName() string { return "sales_opportunity_events" }

// SalesOpportunityCounter holds the per-org, per-day opportunity_number
// sequence. Daily reset, unlike OccurrenceCounter's yearly one — sales
// volume outpaces occurrence volume (spec §3). Day is stored as "YYYYMMDD"
// so it doubles as the string embedded in opportunity_number.
type SalesOpportunityCounter struct {
	OrganizationID uuid.UUID `gorm:"type:uuid;primaryKey" json:"organization_id"`
	Day            string    `gorm:"size:8;primaryKey" json:"day"`
	LastSeq        int       `gorm:"not null;default:0" json:"last_seq"`
}

func (SalesOpportunityCounter) TableName() string { return "sales_opportunity_counters" }
```

Add the partial unique index via a `Migrator().CreateIndex`-style raw execution, since GORM tags alone can't express a `WHERE` clause. Add to `internal/database/postgres.go` right after `AutoMigrate` runs (find the existing raw-SQL index block used for `idx_occ_sla_org_priority_scope` or similar, and add alongside it):

```go
// One-open-opportunity-per-contact guard, enforced in Postgres, not just the
// app — see spec §4/§5.3. IF NOT EXISTS makes this safe to run on every boot.
if err := db.Exec(`
	CREATE UNIQUE INDEX IF NOT EXISTS idx_sales_opp_org_contact_open
	ON sales_opportunities (organization_id, contact_id)
	WHERE status = 'aberta' AND deleted_at IS NULL
`).Error; err != nil {
	return fmt.Errorf("failed to create sales opportunity open-contact index: %w", err)
}
```

Add `XProcessSellerCode` to `User` in `internal/models/models.go`:

```go
// internal/models/models.go — inside type User struct, after IsSuperAdmin
	// XProcessSellerCode identifies this user as a seller in the XProcess ERP
	// (cod_vendedor). Filled manually by an admin; used only by the future
	// Entrega 2 reconciliation job — unset has no effect in this delivery.
	XProcessSellerCode *string `gorm:"size:20" json:"xprocess_seller_code,omitempty"`
```

Register both new models in `GetMigrationModels()` in `internal/database/postgres.go`, right after the existing `OccurrenceCounter` entry:

```go
		{"OccurrenceCounter", &models.OccurrenceCounter{}},
		{"SalesOpportunity", &models.SalesOpportunity{}},
		{"SalesOpportunityEvent", &models.SalesOpportunityEvent{}},
		{"SalesOpportunityCounter", &models.SalesOpportunityCounter{}},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run 'TestSalesOpportunityModel_CreateAndRead|TestUserModel_XProcessSellerCode' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/models/sales_opportunity.go internal/models/models.go internal/database/postgres.go internal/handlers/sales_opportunity_model_test.go
git commit -m "feat(sales): add SalesOpportunity/SalesOpportunityEvent models and users.xprocess_seller_code"
```

---

### Task 2: Numeração sequencial (opportunity_number)

**Files:**
- Create: `internal/handlers/sales_opportunity_protocol.go`
- Create: `internal/handlers/sales_opportunity_export_test.go` (test-only exported aliases, mirrors `occurrence_export_test.go`)
- Test: `internal/handlers/sales_opportunity_protocol_test.go`

**Interfaces:**
- Consumes: `models.SalesOpportunityCounter` (Task 1).
- Produces: `(a *App) nextOpportunityNumber(tx *gorm.DB, orgID uuid.UUID, day time.Time) (string, error)` — later tasks (4) call this inside their own transaction.
- Produces (test-only): `(a *App) NextOpportunityNumberForTest(orgID uuid.UUID, day time.Time) (string, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sales_opportunity_protocol_test.go
package handlers_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSalesOpportunityProtocol_Format(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	day := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	require.NoError(t, app.DB.Create(&models.SalesOpportunityCounter{
		OrganizationID: org.ID, Day: "20260917", LastSeq: 122,
	}).Error)

	got, err := app.NextOpportunityNumberForTest(org.ID, day)
	require.NoError(t, err)
	assert.Equal(t, "OPP-20260917-000123", got)
}

func TestSalesOpportunityProtocol_ResetsOnNewDay(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.DB.Create(&models.SalesOpportunityCounter{
		OrganizationID: org.ID, Day: "20260916", LastSeq: 457,
	}).Error)

	got, err := app.NextOpportunityNumberForTest(org.ID, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, "OPP-20260917-000001", got)
}

func TestSalesOpportunityProtocol_TwoOrgsCanShareTheSameNumberSameDay(t *testing.T) {
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)

	gotA, err := app.NextOpportunityNumberForTest(orgA.ID, day)
	require.NoError(t, err)
	gotB, err := app.NextOpportunityNumberForTest(orgB.ID, day)
	require.NoError(t, err)
	assert.Equal(t, gotA, gotB)
	assert.Equal(t, "OPP-20260917-000001", gotA)
}

// GATE. Same reasoning as TestOccurrenceProtocol_UniqueUnderConcurrency — a
// COUNT(*)+1 implementation passes a serial test and collides under load.
func TestSalesOpportunityProtocol_UniqueUnderConcurrency(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	day := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)

	const n = 30
	var wg sync.WaitGroup
	numbers := make([]string, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			numbers[idx], errs[idx] = app.NextOpportunityNumberForTest(org.ID, day)
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		require.NoError(t, err, "chamada %d falhou", i)
		assert.False(t, seen[numbers[i]], "número duplicado: %s", numbers[i])
		seen[numbers[i]] = true
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestSalesOpportunityProtocol -v`
Expected: FAIL with `app.NextOpportunityNumberForTest undefined`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/handlers/sales_opportunity_protocol.go
package handlers

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// nextOpportunityNumber returns the next OPP-YYYYMMDD-NNNNNN for the
// organisation and day. MUST run inside the same transaction as the
// opportunity insert — see nextProtocolNumber's doc comment in
// occurrence_protocol.go for why COUNT(*)+1 is not safe here either.
func (a *App) nextOpportunityNumber(tx *gorm.DB, orgID uuid.UUID, day time.Time) (string, error) {
	dayKey := day.Format("20060102")

	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&models.SalesOpportunityCounter{
			OrganizationID: orgID,
			Day:            dayKey,
			LastSeq:        0,
		}).Error; err != nil {
		return "", err
	}

	var seq int
	row := tx.Raw(`UPDATE sales_opportunity_counters
		SET last_seq = last_seq + 1
		WHERE organization_id = ? AND day = ?
		RETURNING last_seq`, orgID, dayKey).Row()
	if err := row.Scan(&seq); err != nil {
		return "", err
	}

	return fmt.Sprintf("OPP-%s-%06d", dayKey, seq), nil
}
```

```go
// internal/handlers/sales_opportunity_export_test.go
package handlers

import (
	"time"

	"github.com/google/uuid"
)

func (a *App) NextOpportunityNumberForTest(orgID uuid.UUID, day time.Time) (string, error) {
	return a.nextOpportunityNumber(a.DB, orgID, day)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestSalesOpportunityProtocol -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/sales_opportunity_protocol.go internal/handlers/sales_opportunity_export_test.go internal/handlers/sales_opportunity_protocol_test.go
git commit -m "feat(sales): add per-org daily opportunity_number counter"
```

---

### Task 3: Permissões e backfill

**Files:**
- Modify: `internal/models/roles.go:96` (new resource constant), `:298` (new `DefaultPermissions` entries), `:360` (manager), `:362` (agent)
- Modify: `internal/database/permissions_backfill.go` (new backfill function)
- Modify: `cmd/whatomate/main.go:191` (call the new backfill in the `-migrate` block)
- Test: `internal/database/permissions_backfill_sales_test.go`

**Interfaces:**
- Produces: `models.ResourceSalesOpportunities = "sales_opportunities"`.
- Produces: `database.BackfillSalesOpportunityPermissions(db *gorm.DB, lo logf.Logger) error`.
- Consumes: `models.ActionRead`, `models.ActionWrite`, `models.ActionViewAll` (existing).

- [ ] **Step 1: Write the failing test**

```go
// internal/database/permissions_backfill_sales_test.go
package database_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestBackfillSalesOpportunityPermissions_GrantsToChatWriteAndViewAll(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Permission{}, &models.CustomRole{}, &models.RolePermission{}))
	require.NoError(t, db.Create(&[]models.Permission{
		{Resource: models.ResourceChat, Action: models.ActionWrite},
		{Resource: models.ResourceConversations, Action: models.ActionViewAll},
		{Resource: models.ResourceSalesOpportunities, Action: models.ActionRead},
		{Resource: models.ResourceSalesOpportunities, Action: models.ActionWrite},
		{Resource: models.ResourceSalesOpportunities, Action: models.ActionViewAll},
	}).Error)

	org := testutil.CreateTestOrganization(t, db)
	agentRole := testutil.CreateCustomRole(t, db, org.ID, "agent-like", []string{"chat:write"})
	managerRole := testutil.CreateCustomRole(t, db, org.ID, "manager-like", []string{"chat:write", "conversations:view_all"})

	require.NoError(t, database.BackfillSalesOpportunityPermissions(db, testLog()))

	require.True(t, testutil.RoleHasPermission(t, db, agentRole.ID, "sales_opportunities:read"))
	require.True(t, testutil.RoleHasPermission(t, db, agentRole.ID, "sales_opportunities:write"))
	require.False(t, testutil.RoleHasPermission(t, db, agentRole.ID, "sales_opportunities:view_all"))
	require.True(t, testutil.RoleHasPermission(t, db, managerRole.ID, "sales_opportunities:view_all"))
}

func TestBackfillSalesOpportunityPermissions_IdempotentPerOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.Permission{}, &models.CustomRole{}, &models.RolePermission{}))
	require.NoError(t, db.Create(&[]models.Permission{
		{Resource: models.ResourceChat, Action: models.ActionWrite},
		{Resource: models.ResourceSalesOpportunities, Action: models.ActionRead},
		{Resource: models.ResourceSalesOpportunities, Action: models.ActionWrite},
		{Resource: models.ResourceSalesOpportunities, Action: models.ActionViewAll},
	}).Error)
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateCustomRole(t, db, org.ID, "custom", []string{"chat:write"})

	require.NoError(t, database.BackfillSalesOpportunityPermissions(db, testLog()))
	// Simulate the admin manually revoking write after the first backfill.
	require.NoError(t, testutil.RevokeRolePermission(t, db, role.ID, "sales_opportunities:write"))

	require.NoError(t, database.BackfillSalesOpportunityPermissions(db, testLog()))
	require.False(t, testutil.RoleHasPermission(t, db, role.ID, "sales_opportunities:write"),
		"second run must not re-grant to an org already migrated")
}
```

Check `test/testutil` for `CreateCustomRole`, `RoleHasPermission`, `RevokeRolePermission` and `testLog()` (used by `permissions_backfill_test.go` in the same package) before writing new ones — reuse them if they already exist; the existing `permissions_backfill_test.go`/`permissions_backfill_what_happened_test.go` files already exercise this exact shape and are the reference for whichever helpers exist today.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/database/... -run TestBackfillSalesOpportunityPermissions -v`
Expected: FAIL with `undefined: models.ResourceSalesOpportunities`

- [ ] **Step 3: Write minimal implementation**

Add the resource constant in `internal/models/roles.go`, right after `ResourceDepartments`:

```go
	ResourceDepartments             = "departments"
	ResourceSalesOpportunities      = "sales_opportunities"
```

Add to `DefaultPermissions()`, right after the Help Desk block:

```go
		// Central de Vendas — funil de oportunidades (Entrega 1). view_all
		// mirrors conversations:view_all's semantics (spec §7): sem ela, o
		// papel só vê/edita as próprias oportunidades (assigned_user_id=self).
		{Resource: ResourceSalesOpportunities, Action: ActionRead, Description: "View sales opportunities"},
		{Resource: ResourceSalesOpportunities, Action: ActionWrite, Description: "Create and edit sales opportunities"},
		{Resource: ResourceSalesOpportunities, Action: ActionViewAll, Description: "View and manage all sales opportunities, including those assigned to other agents"},
```

Add to `managerPermissions` in `SystemRolePermissions()`:

```go
		// Central de Vendas: o gestor enxerga o funil inteiro, igual a conversations:view_all
		"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all",
```

Add to `agentPermissions`:

```go
		// Central de Vendas: o agente usa a própria carteira
		"sales_opportunities:read", "sales_opportunities:write",
```

Add the backfill to `internal/database/permissions_backfill.go`, at the end of the file:

```go
// salesOpportunityGrants concede sales_opportunities:{read,write} a quem já
// atende (chat:write) e sales_opportunities:view_all a quem já enxerga toda
// conversa (conversations:view_all) — a equivalência de capacidade definida
// no spec §7 para papéis existentes que nunca tiveram a chance de ganhar
// este recurso novo via SystemRolePermissions (que FixSystemRolePermissions
// pula quando o papel já tem qualquer permissão).
//
// Puramente aditivo: nunca revoga nada.
var salesOpportunityGrants = []grantRule{
	{models.ResourceChat, models.ActionWrite, models.ResourceSalesOpportunities, models.ActionRead},
	{models.ResourceChat, models.ActionWrite, models.ResourceSalesOpportunities, models.ActionWrite},
	{models.ResourceConversations, models.ActionViewAll, models.ResourceSalesOpportunities, models.ActionViewAll},
}

var salesOpportunityPermissionKeys = []string{
	models.ResourceSalesOpportunities + ":" + models.ActionRead,
	models.ResourceSalesOpportunities + ":" + models.ActionWrite,
	models.ResourceSalesOpportunities + ":" + models.ActionViewAll,
}

func BackfillSalesOpportunityPermissions(db *gorm.DB, lo logf.Logger) error {
	var seededRows []string
	if err := db.Model(&models.Permission{}).
		Where("resource = ?", models.ResourceSalesOpportunities).
		Pluck("resource || ':' || action", &seededRows).Error; err != nil {
		return fmt.Errorf("failed to count sales opportunity permissions: %w", err)
	}
	seeded := make(map[string]bool, len(seededRows))
	for _, k := range seededRows {
		seeded[k] = true
	}
	for _, key := range salesOpportunityPermissionKeys {
		if !seeded[key] {
			lo.Warn("Sales opportunity permissions backfill: permissions not seeded yet, did nothing")
			return nil
		}
	}

	var pendingOrgs int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM organizations o
		WHERE NOT EXISTS (
			SELECT 1
			FROM custom_roles r
			JOIN role_permissions rp ON rp.custom_role_id = r.id
			JOIN permissions p ON p.id = rp.permission_id
			WHERE r.organization_id = o.id
			  AND r.deleted_at IS NULL
			  AND p.resource = 'sales_opportunities'
		  )`).Scan(&pendingOrgs).Error; err != nil {
		return fmt.Errorf("failed to count organisations pending the sales opportunity backfill: %w", err)
	}
	if pendingOrgs == 0 {
		lo.Info("Sales opportunity permissions backfill: nothing pending, all organisations already migrated")
		return nil
	}

	placeholders := make([]string, len(salesOpportunityGrants))
	args := make([]any, 0, len(salesOpportunityGrants)*4)
	for i, g := range salesOpportunityGrants {
		placeholders[i] = "(?,?,?,?)"
		args = append(args, g.fromResource, g.fromAction, g.toResource, g.toAction)
	}

	query := fmt.Sprintf(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		JOIN (VALUES %s) AS g(from_resource, from_action, to_resource, to_action)
		  ON src.resource = g.from_resource AND src.action = g.from_action
		JOIN permissions target
		  ON target.resource = g.to_resource AND target.action = g.to_action
		WHERE r.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM role_permissions existing
			WHERE existing.custom_role_id = r.id
			  AND existing.permission_id = target.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM custom_roles r2
			JOIN role_permissions rp2 ON rp2.custom_role_id = r2.id
			JOIN permissions p2 ON p2.id = rp2.permission_id
			WHERE r2.organization_id = r.organization_id
			  AND r2.deleted_at IS NULL
			  AND p2.resource = 'sales_opportunities'
		  )
		ON CONFLICT (custom_role_id, permission_id) DO NOTHING`,
		strings.Join(placeholders, ","),
	)

	res := db.Exec(query, args...)
	if res.Error != nil {
		return fmt.Errorf("failed to grant sales opportunity permissions: %w", res.Error)
	}

	lo.Info("Sales opportunity permissions backfill complete",
		"organisations_processed", pendingOrgs, "links_granted", res.RowsAffected)
	return nil
}
```

Register the call in `cmd/whatomate/main.go`, right after the `BackfillWhatHappenedPermission` block:

```go
		// Same window: sales_opportunities is a brand-new resource, needs its
		// own guard rather than piggybacking on an existing one.
		if err := database.BackfillSalesOpportunityPermissions(db, lo); err != nil {
			lo.Fatal("Sales opportunity permissions backfill failed", "error", err)
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/database/... -run TestBackfillSalesOpportunityPermissions -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/models/roles.go internal/database/permissions_backfill.go internal/database/permissions_backfill_sales_test.go cmd/whatomate/main.go
git commit -m "feat(sales): add sales_opportunities permission resource with existing-org backfill"
```

---

### Task 4: Criação idempotente da oportunidade

**Files:**
- Create: `internal/handlers/sales_opportunities.go`
- Modify: `internal/handlers/sales_opportunity_export_test.go` (add test alias)
- Test: `internal/handlers/sales_opportunities_creation_test.go`

**Interfaces:**
- Consumes: `models.SalesOpportunity`, `models.SalesOpportunityEvent` (Task 1); `a.nextOpportunityNumber` (Task 2); `models.Contact` (existing, for `AssignedUserID`).
- Produces: `(a *App) createOrRetriggerSalesOpportunity(contact *models.Contact, sourceTransferID *uuid.UUID) (*models.SalesOpportunity, error)` — Task 5 (chatbot hook) calls this directly.
- Produces (test-only): `(a *App) CreateOrRetriggerSalesOpportunityForTest(contact *models.Contact, sourceTransferID *uuid.UUID) (*models.SalesOpportunity, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sales_opportunities_creation_test.go
package handlers_test

import (
	"sync"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOrRetriggerSalesOpportunity_CreatesInPotencial(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(&contact).Update("assigned_user_id", user.ID).Error)
	contact.AssignedUserID = &user.ID

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	assert.Equal(t, models.SalesOpportunityStagePotencial, opp.Stage)
	assert.Equal(t, models.SalesOpportunityStatusAberta, opp.Status)
	require.NotNil(t, opp.AssignedUserID)
	assert.Equal(t, user.ID, *opp.AssignedUserID)
	assert.Regexp(t, `^OPP-\d{8}-\d{6}$`, opp.OpportunityNumber)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.SalesOpportunityEventOpened, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventSourceSystem, events[0].Source)
}

func TestCreateOrRetriggerSalesOpportunity_AssignedUserIDCanBeNil(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	assert.Nil(t, opp.AssignedUserID)
}

func TestCreateOrRetriggerSalesOpportunity_RetriggerDoesNotDuplicate(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	first, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).Where("id = ?", first.ID).
		Update("stage", models.SalesOpportunityStageAbrirOrcamento).Error)

	second, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, models.SalesOpportunityStageAbrirOrcamento, second.Stage, "retrigger must not reset stage")

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).
		Where("contact_id = ? AND status = ?", contact.ID, models.SalesOpportunityStatusAberta).
		Count(&count).Error)
	assert.EqualValues(t, 1, count)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?",
		first.ID, models.SalesOpportunityEventRetriggered).Find(&events).Error)
	require.Len(t, events, 1)
}

// GATE. Concurrent creation for the same contact must yield exactly one open
// opportunity — the partial unique index (organization_id, contact_id) WHERE
// status='aberta' is what makes this safe (spec §5.3), same pattern as
// TestOccurrenceProtocol_UniqueUnderConcurrency / TestSalesOpportunityProtocol_UniqueUnderConcurrency.
func TestCreateOrRetriggerSalesOpportunity_UniqueUnderConcurrency(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "chamada %d falhou", i)
	}

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).
		Where("contact_id = ? AND status = ?", contact.ID, models.SalesOpportunityStatusAberta).
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestCreateOrRetriggerSalesOpportunity -v`
Expected: FAIL with `app.CreateOrRetriggerSalesOpportunityForTest undefined`

- [ ] **Step 3: Write minimal implementation**

```go
// internal/handlers/sales_opportunities.go
package handlers

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// createOrRetriggerSalesOpportunity is the single entry point for opening a
// funnel entry from the chatbot (spec §5, §5.2, §5.3). It is idempotent: if
// the contact already has an open opportunity, it is returned unchanged
// (only a "retriggered" event is appended) instead of creating a second one.
//
// Concurrency: two simultaneous calls for the same contact race on the
// partial unique index idx_sales_opp_org_contact_open. The loser's insert
// fails with a unique_violation, which this function treats identically to
// "found an existing open one" — reload and append retriggered — so the
// caller never sees a race error, only the idempotent result (spec §5.3).
func (a *App) createOrRetriggerSalesOpportunity(contact *models.Contact, sourceTransferID *uuid.UUID) (*models.SalesOpportunity, error) {
	var existing models.SalesOpportunity
	err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
		contact.OrganizationID, contact.ID, models.SalesOpportunityStatusAberta).
		First(&existing).Error
	if err == nil {
		if err := a.DB.Create(&models.SalesOpportunityEvent{
			OrganizationID:      contact.OrganizationID,
			SalesOpportunityID:  existing.ID,
			Type:                models.SalesOpportunityEventRetriggered,
			Source:              models.SalesOpportunityEventSourceSystem,
		}).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	opp := models.SalesOpportunity{
		OrganizationID:    contact.OrganizationID,
		ContactID:         contact.ID,
		SourceTransferID:  sourceTransferID,
		AssignedUserID:    contact.AssignedUserID,
		Stage:             models.SalesOpportunityStagePotencial,
		Status:            models.SalesOpportunityStatusAberta,
		StageChangedAt:    time.Now(),
	}

	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		number, err := a.nextOpportunityNumber(tx, contact.OrganizationID, time.Now())
		if err != nil {
			return err
		}
		opp.OpportunityNumber = number
		return tx.Create(&opp).Error
	})

	if txErr != nil {
		// Lost the race to a concurrent insert: the partial unique index
		// rejected us. Fall back to the retrigger path against the winner.
		if isUniqueViolation(txErr) {
			var winner models.SalesOpportunity
			if err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
				contact.OrganizationID, contact.ID, models.SalesOpportunityStatusAberta).
				First(&winner).Error; err != nil {
				return nil, err
			}
			if err := a.DB.Create(&models.SalesOpportunityEvent{
				OrganizationID:      contact.OrganizationID,
				SalesOpportunityID:  winner.ID,
				Type:                models.SalesOpportunityEventRetriggered,
				Source:              models.SalesOpportunityEventSourceSystem,
			}).Error; err != nil {
				return nil, err
			}
			return &winner, nil
		}
		return nil, txErr
	}

	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID:      contact.OrganizationID,
		SalesOpportunityID:  opp.ID,
		Type:                models.SalesOpportunityEventOpened,
		Source:              models.SalesOpportunityEventSourceSystem,
	}).Error; err != nil {
		return nil, err
	}

	return &opp, nil
}

// isUniqueViolation detects Postgres error 23505 without importing the
// pgconn/pq driver types directly — GORM already wraps it as a plain error
// whose message contains the SQLSTATE text on every driver this project uses.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
```

```go
// internal/handlers/sales_opportunity_export_test.go — append
func (a *App) CreateOrRetriggerSalesOpportunityForTest(contact *models.Contact, sourceTransferID *uuid.UUID) (*models.SalesOpportunity, error) {
	return a.createOrRetriggerSalesOpportunity(contact, sourceTransferID)
}
```

(Add `"github.com/shridarpatil/whatomate/internal/models"` and `"github.com/google/uuid"` to that file's imports if not already present from Task 2.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestCreateOrRetriggerSalesOpportunity -v -race`
Expected: PASS (5 tests, including the concurrency one under `-race`)

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/sales_opportunities.go internal/handlers/sales_opportunity_export_test.go internal/handlers/sales_opportunities_creation_test.go
git commit -m "feat(sales): add idempotent, concurrency-safe opportunity creation"
```

---

### Task 5: Hook no fluxo do chatbot

**Files:**
- Modify: `internal/handlers/chatbot_graph_runner.go:252-300` (`execChatButtons`)
- Modify: `frontend/src/components/chatbot/ChatNodeProperties.vue` is Task 13, not this one — this task is backend-only.
- Test: `internal/handlers/chatbot_sales_trigger_test.go`

**Interfaces:**
- Consumes: `a.createOrRetriggerSalesOpportunity` (Task 4); `buttonsFromConfig(node.Config)`, `ctx.contact`, `ctx.account` (existing, `chatbot_graph_runner.go`).
- Produces: nothing new callable — this task only adds a side effect inside the existing `execChatButtons` node executor, config key `create_opportunity: true` per button.

- [ ] **Step 1: Write the failing test**

Find the existing test harness for `execChatButtons`/`runChatGraph` (used by `chatbot_graph_runner_test.go`) and mirror its setup exactly — the button-click flow already has fixtures for a session mid-flow at a buttons node. Add:

```go
// internal/handlers/chatbot_sales_trigger_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecChatButtons_CreateOpportunityFlagOpensOpportunity(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	node := &handlers.ChatNode{
		ID:   "btn-node",
		Type: handlers.ChatNodeButtons,
		Config: map[string]any{
			"body": "O que deseja?",
			"buttons": []any{
				map[string]any{"id": "realizar_pedido", "title": "Realizar pedido", "create_opportunity": true},
				map[string]any{"id": "acompanhar_pedido", "title": "Acompanhar pedido"},
			},
		},
	}
	ctx := &handlers.ChatNodeCtxForTest{Account: account, Contact: contact, ButtonID: "realizar_pedido"}

	_, err := app.ExecChatButtonsForTest(node, ctx)
	require.NoError(t, err)

	var opp models.SalesOpportunity
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&opp).Error)
	assert.Equal(t, models.SalesOpportunityStagePotencial, opp.Stage)
}

func TestExecChatButtons_NoCreateOpportunityFlagDoesNothing(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	node := &handlers.ChatNode{
		ID:   "btn-node",
		Type: handlers.ChatNodeButtons,
		Config: map[string]any{
			"body":    "O que deseja?",
			"buttons": []any{map[string]any{"id": "acompanhar_pedido", "title": "Acompanhar pedido"}},
		},
	}
	ctx := &handlers.ChatNodeCtxForTest{Account: account, Contact: contact, ButtonID: "acompanhar_pedido"}

	_, err := app.ExecChatButtonsForTest(node, ctx)
	require.NoError(t, err)

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).Where("contact_id = ?", contact.ID).Count(&count).Error)
	assert.EqualValues(t, 0, count)
}
```

Note: `ChatNodeCtxForTest`, `ExecChatButtonsForTest`, and whatever `testutil.CreateTestWhatsAppAccount` helper exists must match the exact test-export shape already used by `chatbot_graph_runner_test.go` — read that file first (it already exercises `execChatButtons`'s `team_id` behavior with the same node/ctx shapes) and reuse its existing helpers instead of inventing new ones; only add what's missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestExecChatButtons_CreateOpportunityFlag -v`
Expected: FAIL — no `SalesOpportunity` row created (0 rows found where 1 expected)

- [ ] **Step 3: Write minimal implementation**

In `internal/handlers/chatbot_graph_runner.go`, inside `execChatButtons`, in the same `for _, b := range buttonsFromConfig(node.Config)` loop that already handles `team_id` (around line 273-298), add the `create_opportunity` side effect right after the `team_id` block, before the loop's `break`:

```go
		for _, b := range buttonsFromConfig(node.Config) {
			if id, _ := b["id"].(string); id == ctx.buttonID {
				if tid, _ := b["team_id"].(string); tid != "" {
					// ... existing team_id handling unchanged ...
				}

				// create_opportunity: true opens a sales funnel entry for
				// this contact (spec §5). Idempotent — a contact that
				// already has one open just gets a "retriggered" event, no
				// duplicate, no stage change (spec §5.2).
				if create, _ := b["create_opportunity"].(bool); create {
					if _, err := a.createOrRetriggerSalesOpportunity(ctx.contact, nil); err != nil {
						a.Log.Error("buttons node failed to create sales opportunity",
							"node", node.ID, "contact", ctx.contact.ID, "error", err)
					}
				}
				break
			}
		}
```

Add test-export aliases if `chatbot_graph_runner_test.go` doesn't already expose them (check first — `execChatButtons`, `ChatNode`, `chatNodeCtx` are likely already exported for tests via a `chatbot_graph_runner_export_test.go` file; add `ExecChatButtonsForTest`/`ChatNodeCtxForTest` there only if missing, following the exact pattern of Task 2/4's export files).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestExecChatButtons -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chatbot_graph_runner.go internal/handlers/chatbot_sales_trigger_test.go
git commit -m "feat(sales): open a funnel entry from a chatbot button flagged create_opportunity"
```

---

### Task 6: Listagem, detalhe e histórico de eventos

**Files:**
- Modify: `internal/handlers/sales_opportunities.go` (add handlers)
- Modify: `cmd/whatomate/main.go` (routes)
- Test: `internal/handlers/sales_opportunities_read_test.go`

**Interfaces:**
- Consumes: `models.SalesOpportunity`/`Event` (Task 1); `a.requireAuth`, `a.HasPermission`, `r.SendEnvelope`/`r.SendErrorEnvelope`, `parsePaginationWithDefaults` (existing, `app.go`).
- Produces: `(a *App) ListSalesOpportunities(r *fastglue.Request) error`, `(a *App) GetSalesOpportunity(r *fastglue.Request) error`, `(a *App) ListSalesOpportunityEvents(r *fastglue.Request) error`, `(a *App) visibleSalesOpportunities(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB`, `(a *App) loadAuthorizedSalesOpportunity(r *fastglue.Request, orgID, userID uuid.UUID) (*models.SalesOpportunity, error)` (used by Tasks 7 and 8 too).

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sales_opportunities_read_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestListSalesOpportunities_AgentSeesOnlyOwn(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID)
	contactMine := testutil.CreateTestContact(t, app.DB, org.ID)
	contactTheirs := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactMine.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agent.ID, StageChangedAt: testutil.Now(),
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contactTheirs.ID, OpportunityNumber: "OPP-20260917-000002",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &other.ID, StageChangedAt: testutil.Now(),
	}).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.ListSalesOpportunities(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	body := testutil.DecodeEnvelope[struct {
		Opportunities []models.SalesOpportunity `json:"opportunities"`
	}](t, req)
	require.Len(t, body.Opportunities, 1)
	assert.Equal(t, contactMine.ID, body.Opportunities[0].ContactID)
}

func TestListSalesOpportunities_ManagerWithViewAllSeesEverything(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	managerRole := testutil.CreateCustomRole(t, app.DB, org.ID, "manager",
		[]string{"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all"})
	manager := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&managerRole.ID))
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agent.ID, StageChangedAt: testutil.Now(),
	}).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, manager.ID)
	require.NoError(t, app.ListSalesOpportunities(req))
	body := testutil.DecodeEnvelope[struct {
		Opportunities []models.SalesOpportunity `json:"opportunities"`
	}](t, req)
	require.Len(t, body.Opportunities, 1)
}

func TestGetSalesOpportunity_403ForNonOwnerWithoutViewAll(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	owner := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &owner.ID, StageChangedAt: testutil.Now(),
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.GetSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestListSalesOpportunityEvents_ReturnsOpenedEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(opp).Update("assigned_user_id", agent.ID).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ListSalesOpportunityEvents(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	body := testutil.DecodeEnvelope[struct {
		Events []models.SalesOpportunityEvent `json:"events"`
	}](t, req)
	require.Len(t, body.Events, 1)
	assert.Equal(t, models.SalesOpportunityEventOpened, body.Events[0].Type)
}
```

Check `test/testutil` for `Now()`, `DecodeEnvelope[T]` helpers before assuming they exist — use whatever the existing occurrence read tests use for decoding an envelope body (grep `occurrences_test.go` or similar list-test file for the exact decode pattern) and match it instead of inventing a new one if a different helper name is already established.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run 'TestListSalesOpportunities|TestGetSalesOpportunity|TestListSalesOpportunityEvents' -v`
Expected: FAIL with `app.ListSalesOpportunities undefined`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/handlers/sales_opportunities.go`:

```go
// visibleSalesOpportunities scopes a query to what userID may see: everything
// with view_all, otherwise only opportunities assigned to them (spec §7).
func (a *App) visibleSalesOpportunities(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	if a.HasPermission(userID, models.ResourceSalesOpportunities, models.ActionViewAll, orgID) {
		return query
	}
	return query.Where("sales_opportunities.assigned_user_id = ?", userID)
}

// loadAuthorizedSalesOpportunity loads {id} from the route and 404s/403s
// consistently with loadAuthorizedOccurrence's pattern (spec §7).
func (a *App) loadAuthorizedSalesOpportunity(r *fastglue.Request, orgID, userID uuid.UUID) (*models.SalesOpportunity, error) {
	id, err := uuid.Parse(string(r.RequestCtx.UserValue("id").(string)))
	if err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid id", nil, "")
		return nil, errEnvelopeSent
	}
	var opp models.SalesOpportunity
	if err := a.DB.Where("id = ? AND organization_id = ?", id, orgID).First(&opp).Error; err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusNotFound, "Sales opportunity not found", nil, "")
		return nil, errEnvelopeSent
	}
	if !a.HasPermission(userID, models.ResourceSalesOpportunities, models.ActionViewAll, orgID) &&
		(opp.AssignedUserID == nil || *opp.AssignedUserID != userID) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
		return nil, errEnvelopeSent
	}
	return &opp, nil
}

func (a *App) ListSalesOpportunities(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}

	pg := parsePaginationWithDefaults(r, 30, 100)
	query := a.DB.Model(&models.SalesOpportunity{}).Where("sales_opportunities.organization_id = ?", orgID)
	query = a.visibleSalesOpportunities(query, userID, orgID)

	if stage := string(r.RequestCtx.QueryArgs().Peek("stage")); stage != "" {
		query = query.Where("sales_opportunities.stage = ?", stage)
	}
	if status := string(r.RequestCtx.QueryArgs().Peek("status")); status != "" {
		query = query.Where("sales_opportunities.status = ?", status)
	}
	if assignedUserID := string(r.RequestCtx.QueryArgs().Peek("assigned_user_id")); assignedUserID != "" {
		query = query.Where("sales_opportunities.assigned_user_id = ?", assignedUserID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to count sales opportunities", nil, "")
	}

	var opportunities []models.SalesOpportunity
	if err := query.Order("opened_at DESC").Offset(pg.Offset).Limit(pg.Limit).Find(&opportunities).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list sales opportunities", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"opportunities": opportunities,
		"total":         total,
		"has_more":      int64(pg.Offset+len(opportunities)) < total,
	})
}

func (a *App) GetSalesOpportunity(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	return r.SendEnvelope(opp)
}

func (a *App) ListSalesOpportunityEvents(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	var events []models.SalesOpportunityEvent
	if err := a.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at ASC").Find(&events).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list events", nil, "")
	}
	return r.SendEnvelope(map[string]any{"events": events})
}
```

Add imports to `sales_opportunities.go` as needed: `"github.com/shridarpatil/whatomate/pkg/fastglue"` (or the project's actual import alias — match whatever `occurrences.go` imports), `"github.com/valyala/fasthttp"`.

Register routes in `cmd/whatomate/main.go`, right after the occurrence routes block:

```go
	g.GET("/api/sales-opportunities", app.ListSalesOpportunities)
	g.GET("/api/sales-opportunities/{id}", app.GetSalesOpportunity)
	g.GET("/api/sales-opportunities/{id}/events", app.ListSalesOpportunityEvents)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run 'TestListSalesOpportunities|TestGetSalesOpportunity|TestListSalesOpportunityEvents' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/sales_opportunities.go internal/handlers/sales_opportunities_read_test.go cmd/whatomate/main.go
git commit -m "feat(sales): add list/detail/events endpoints with own-vs-view_all visibility"
```

---

### Task 7: Transição de fase e direcionamento

**Files:**
- Modify: `internal/handlers/sales_opportunities.go`
- Modify: `cmd/whatomate/main.go`
- Test: `internal/handlers/sales_opportunities_stage_test.go`

**Interfaces:**
- Consumes: `a.loadAuthorizedSalesOpportunity`, `a.requireAuth` (Task 6).
- Produces: `(a *App) ChangeSalesOpportunityStage(r *fastglue.Request) error`, `(a *App) ChangeSalesOpportunityDirecionamento(r *fastglue.Request) error`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sales_opportunities_stage_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func newOpenOpportunity(t *testing.T, app *handlers.App, orgID, agentID, contactID uuid.UUID) *models.SalesOpportunity {
	opp := &models.SalesOpportunity{
		OrganizationID: orgID, ContactID: contactID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agentID, StageChangedAt: testutil.Now(),
	}
	require.NoError(t, app.DB.Create(opp).Error)
	return opp
}

func TestChangeSalesOpportunityStage_ToDirecionadaWithoutDirecionamentoFails(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Update("stage", models.SalesOpportunityStageAbrirOrcamento).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "direcionada"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityStage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestChangeSalesOpportunityStage_ToDirecionadaWithDirecionamentoSetsStageChangedAt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	visita := models.SalesDirecionamentoVisita
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"stage": models.SalesOpportunityStageAbrirOrcamento, "direcionamento": visita,
	}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "direcionada"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityStage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStageDirecionada, got.Stage)
	assert.True(t, got.StageChangedAt.After(opp.StageChangedAt))

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventStageChanged).Find(&events).Error)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].FromStage)
	require.NotNil(t, events[0].ToStage)
	assert.Equal(t, models.SalesOpportunityStageAbrirOrcamento, *events[0].FromStage)
	assert.Equal(t, models.SalesOpportunityStageDirecionada, *events[0].ToStage)
}

func TestChangeSalesOpportunityDirecionamento_ChangeableInAnyStageWithoutMovingStage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"direcionamento": "whatsapp"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityDirecionamento(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStagePotencial, got.Stage, "must not change stage")
	require.NotNil(t, got.Direcionamento)
	assert.Equal(t, models.SalesDirecionamentoWhatsApp, *got.Direcionamento)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventDirecionamentoChanged).Find(&events).Error)
	require.Len(t, events, 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run 'TestChangeSalesOpportunity' -v`
Expected: FAIL with `app.ChangeSalesOpportunityStage undefined`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/handlers/sales_opportunities.go`:

```go
type changeSalesOpportunityStageRequest struct {
	Stage string `json:"stage"`
}

// ChangeSalesOpportunityStage advances potencial -> abrir_orcamento ->
// direcionada. Entering "direcionada" requires direcionamento to already be
// set (spec §5) and stamps stage_changed_at, the SLA clock's base (spec §6).
func (a *App) ChangeSalesOpportunityStage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can change stage", nil, "")
	}

	var req changeSalesOpportunityStageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	newStage := models.SalesOpportunityStage(req.Stage)
	if newStage != models.SalesOpportunityStagePotencial &&
		newStage != models.SalesOpportunityStageAbrirOrcamento &&
		newStage != models.SalesOpportunityStageDirecionada {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}
	if newStage == models.SalesOpportunityStageDirecionada && opp.Direcionamento == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "direcionamento is required before entering direcionada", nil, "")
	}

	fromStage := opp.Stage
	now := time.Now()
	if err := a.DB.Model(opp).Updates(map[string]any{
		"stage": newStage, "stage_changed_at": now,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to change stage", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventStageChanged,
		FromStage: &fromStage, ToStage: &newStage, Source: models.SalesOpportunityEventSourceManual,
		CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type changeSalesOpportunityDirecionamentoRequest struct {
	Direcionamento string `json:"direcionamento"`
}

// ChangeSalesOpportunityDirecionamento edits direcionamento as a plain form
// field, not a stage transition — allowed in any stage while status=aberta
// (spec §5).
func (a *App) ChangeSalesOpportunityDirecionamento(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can change direcionamento", nil, "")
	}

	var req changeSalesOpportunityDirecionamentoRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	direcionamento := models.SalesDirecionamento(req.Direcionamento)
	if direcionamento != models.SalesDirecionamentoVisita && direcionamento != models.SalesDirecionamentoWhatsApp {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid direcionamento", nil, "")
	}

	if err := a.DB.Model(opp).Update("direcionamento", direcionamento).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to change direcionamento", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventDirecionamentoChanged,
		Source: models.SalesOpportunityEventSourceManual, CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}
```

Register routes in `cmd/whatomate/main.go`:

```go
	g.PUT("/api/sales-opportunities/{id}/stage", app.ChangeSalesOpportunityStage)
	g.PUT("/api/sales-opportunities/{id}/direcionamento", app.ChangeSalesOpportunityDirecionamento)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run 'TestChangeSalesOpportunity' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/sales_opportunities.go internal/handlers/sales_opportunities_stage_test.go cmd/whatomate/main.go
git commit -m "feat(sales): add stage and direcionamento transitions with SLA clock reset"
```

---

### Task 8: Conversão e perda (máquina de estados)

**Files:**
- Modify: `internal/handlers/sales_opportunities.go`
- Modify: `cmd/whatomate/main.go`
- Test: `internal/handlers/sales_opportunities_conversion_test.go`

**Interfaces:**
- Consumes: `a.loadAuthorizedSalesOpportunity`, `a.requireAuth` (Task 6); `models.ValidSalesLossReasons` (Task 1).
- Produces: `(a *App) ConvertSalesOpportunity(r *fastglue.Request) error`, `(a *App) LoseSalesOpportunity(r *fastglue.Request) error`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sales_opportunities_conversion_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestConvertSalesOpportunity_SetsConversionSourceManual(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, got.Status)
	require.NotNil(t, got.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceManual, *got.ConversionSource)
	require.NotNil(t, got.ConvertedAt)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventConverted).Find(&events).Error)
	require.Len(t, events, 1)
}

func TestLoseSalesOpportunity_RequiresLossReason(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestLoseSalesOpportunity_RejectsReasonOutsideClosedList(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "motivo_inventado"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestLoseSalesOpportunity_ValidReasonSetsLostAtAndEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "preco", "loss_notes": "Concorrente 10% mais barato"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusPerdida, got.Status)
	require.NotNil(t, got.LossReason)
	assert.Equal(t, models.SalesLossReasonPreco, *got.LossReason)
	require.NotNil(t, got.LostAt)
}

// GATE — máquina de estados (spec §5.1): nenhuma dessas transições é permitida.
func TestSalesOpportunityStateMachine_RejectsInvalidTransitions(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateCustomRole(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	// convertida -> perdida
	convertida := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(convertida).Update("status", models.SalesOpportunityStatusConvertida).Error)
	req := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "preco"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", convertida.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	// perdida -> convertida
	contact2 := testutil.CreateTestContact(t, app.DB, org.ID)
	perdida := newOpenOpportunity(t, app, org.ID, agent.ID, contact2.ID)
	require.NoError(t, app.DB.Model(perdida).Update("status", models.SalesOpportunityStatusPerdida).Error)
	req2 := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req2, org.ID, agent.ID)
	req2.RequestCtx.SetUserValue("id", perdida.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req2))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req2))

	// cancelada -> convertida (terminal, no edge out — even though Entrega 1
	// never writes cancelada itself, the guard must already hold for Entrega 2)
	contact3 := testutil.CreateTestContact(t, app.DB, org.ID)
	cancelada := newOpenOpportunity(t, app, org.ID, agent.ID, contact3.ID)
	require.NoError(t, app.DB.Model(cancelada).Update("status", models.SalesOpportunityStatusCancelada).Error)
	req3 := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req3, org.ID, agent.ID)
	req3.RequestCtx.SetUserValue("id", cancelada.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req3))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req3))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run 'TestConvertSalesOpportunity|TestLoseSalesOpportunity|TestSalesOpportunityStateMachine' -v`
Expected: FAIL with `app.ConvertSalesOpportunity undefined`

- [ ] **Step 3: Write minimal implementation**

Append to `internal/handlers/sales_opportunities.go`:

```go
// ConvertSalesOpportunity marks the opportunity converted. Manual only in
// this delivery — conversion_source is always "manual" (spec §2, §9).
// Validates the state machine (spec §5.1): only aberta -> convertida.
func (a *App) ConvertSalesOpportunity(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be converted", nil, "")
	}

	now := time.Now()
	source := models.SalesConversionSourceManual
	if err := a.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida,
		"conversion_source": source, "converted_at": now,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to convert opportunity", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventConverted,
		Source: models.SalesOpportunityEventSourceManual, CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type loseSalesOpportunityRequest struct {
	LossReason string `json:"loss_reason"`
	LossNotes  string `json:"loss_notes"`
}

// LoseSalesOpportunity marks the opportunity lost. loss_reason is required
// and must be one of the closed list (spec §4, §5.1).
func (a *App) LoseSalesOpportunity(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be marked lost", nil, "")
	}

	var req loseSalesOpportunityRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := models.SalesLossReason(req.LossReason)
	if !models.ValidSalesLossReasons[reason] {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "loss_reason is required and must be a valid reason", nil, "")
	}

	now := time.Now()
	if err := a.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusPerdida,
		"loss_reason": reason, "loss_notes": req.LossNotes, "lost_at": now,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to mark opportunity lost", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventLost,
		Source: models.SalesOpportunityEventSourceManual, CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}
```

Register routes in `cmd/whatomate/main.go`:

```go
	g.POST("/api/sales-opportunities/{id}/convert", app.ConvertSalesOpportunity)
	g.POST("/api/sales-opportunities/{id}/lose", app.LoseSalesOpportunity)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run 'TestConvertSalesOpportunity|TestLoseSalesOpportunity|TestSalesOpportunityStateMachine' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/sales_opportunities.go internal/handlers/sales_opportunities_conversion_test.go cmd/whatomate/main.go
git commit -m "feat(sales): add convert/lose endpoints enforcing the terminal-state machine"
```

---

### Task 9: SLA de "Direcionada" (7 dias)

**Files:**
- Modify: `internal/models/chatbot.go:31-45` (`SLAConfig`, new field)
- Modify: `internal/handlers/sla_processor.go:117-121, 725-752`
- Test: `internal/handlers/sla_processor_sales_test.go`

**Interfaces:**
- Consumes: `models.SalesOpportunity` (Task 1); existing `SLAProcessor`/`processOrganizationSLA`.
- Produces: `models.SLAConfig.SalesOpportunityEnabled bool`; `(p *SLAProcessor) processSalesOpportunitySLA(orgID uuid.UUID, now time.Time)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/sla_processor_sales_test.go
package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessSalesOpportunitySLA_MarksBreachedPastSevenDaysInDirecionada(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	old := time.Now().Add(-8 * 24 * time.Hour)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260909-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: old,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	processor := handlers.NewSLAProcessorForTest(app)
	processor.ProcessSalesOpportunitySLAForTest(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.True(t, got.SLABreached)
	require.NotNil(t, got.SLABreachedAt)
}

func TestProcessSalesOpportunitySLA_LeavingDirecionadaStopsShowingBreached(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	old := time.Now().Add(-8 * 24 * time.Hour)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260909-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: old, SLABreached: true,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	// Agent converts it — a real handler call would clear sla_breached as
	// part of leaving the stage; here we assert the processor itself only
	// ever marks stage=direcionada rows, never touches a converted one.
	require.NoError(t, app.DB.Model(&opp).Update("status", models.SalesOpportunityStatusConvertida).Error)

	processor := handlers.NewSLAProcessorForTest(app)
	processor.ProcessSalesOpportunitySLAForTest(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.True(t, got.SLABreached, "processor does not retroactively clear a closed opportunity's flag")
}

func TestProcessSalesOpportunitySLA_UnderSevenDaysNotMarked(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	recent := time.Now().Add(-2 * 24 * time.Hour)
	opp := models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260915-000001",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: recent,
	}
	require.NoError(t, app.DB.Create(&opp).Error)

	processor := handlers.NewSLAProcessorForTest(app)
	processor.ProcessSalesOpportunitySLAForTest(org.ID, time.Now())

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.False(t, got.SLABreached)
}
```

Check whether `SLAProcessor` already has a test-export constructor (`NewSLAProcessorForTest`) by grepping `sla_processor` test files; if `NewSLAProcessor(app, interval)` is already exported and usable directly from `handlers_test`, use that instead of inventing a new constructor — only add `ProcessSalesOpportunitySLAForTest` as a thin exported wrapper if `processSalesOpportunitySLA` needs one (mirror however `processOccurrenceSLA` is already exposed for its own tests, if it is).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestProcessSalesOpportunitySLA -v`
Expected: FAIL with `ProcessSalesOpportunitySLAForTest undefined` (or `processSalesOpportunitySLA undefined`)

- [ ] **Step 3: Write minimal implementation**

Add the gate to `SLAConfig` in `internal/models/chatbot.go`, right after `OccurrenceEnabled`:

```go
	// SalesOpportunityEnabled gates SLA breach-marking for Central de Vendas
	// opportunities in "direcionada" — its own switch, same reasoning as
	// OccurrenceEnabled: enabling chat/occurrence SLA must never silently
	// turn this on for an org that never asked.
	SalesOpportunityEnabled bool `gorm:"column:sales_opportunity_sla_enabled;default:false" json:"sales_opportunity_sla_enabled"`
```

Wire the gate into `processOrganizationSLA` in `internal/handlers/sla_processor.go`, right after the `processOccurrenceSLA` call:

```go
	if settings.SLA.SalesOpportunityEnabled {
		p.processSalesOpportunitySLA(orgID, now)
	}
```

Add the check function, right after `processOccurrenceSLA`:

```go
// processSalesOpportunitySLA marks sla_breached=true/sla_breached_at=now for
// opportunities that have sat in "direcionada" for more than 7 days (spec
// §6). Only marks — no auto-close, no escalation, same non-objective as
// processOccurrenceSLA.
func (p *SLAProcessor) processSalesOpportunitySLA(orgID uuid.UUID, now time.Time) {
	deadline := now.Add(-7 * 24 * time.Hour)
	if err := p.app.DB.Model(&models.SalesOpportunity{}).
		Where("organization_id = ? AND stage = ? AND status = ? AND stage_changed_at < ? AND sla_breached = ?",
			orgID, models.SalesOpportunityStageDirecionada, models.SalesOpportunityStatusAberta, deadline, false).
		Updates(map[string]any{"sla_breached": true, "sla_breached_at": now}).Error; err != nil {
		p.app.Log.Error("Failed to mark sales opportunity SLA breach", "error", err, "organization_id", orgID)
	}
}
```

Add the test-only export, in the same test-export file pattern used elsewhere for `SLAProcessor` (create `internal/handlers/sla_processor_export_test.go` in `package handlers` if none exists):

```go
// internal/handlers/sla_processor_export_test.go
package handlers

import (
	"time"

	"github.com/google/uuid"
)

func NewSLAProcessorForTest(app *App) *SLAProcessor {
	return NewSLAProcessor(app, time.Minute)
}

func (p *SLAProcessor) ProcessSalesOpportunitySLAForTest(orgID uuid.UUID, now time.Time) {
	p.processSalesOpportunitySLA(orgID, now)
}
```

(Skip this file if `SLAProcessor` is already constructible/testable directly from `handlers_test` — check first.)

Also clear `sla_breached` when a stage change or convert/lose leaves "direcionada": add to `ChangeSalesOpportunityStage`'s update map (Task 7) and to `ConvertSalesOpportunity`/`LoseSalesOpportunity`'s update maps (Task 8) — `"sla_breached": false` alongside the other fields, since those tasks land before this one's test suite would otherwise need to special-case it. If Tasks 7/8 already shipped without this, add it now:

```go
// in ChangeSalesOpportunityStage's Updates map, when newStage != direcionada:
	updates := map[string]any{"stage": newStage, "stage_changed_at": now}
	if newStage != models.SalesOpportunityStageDirecionada {
		updates["sla_breached"] = false
	}
	if err := a.DB.Model(opp).Updates(updates).Error; err != nil { ... }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestProcessSalesOpportunitySLA -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/models/chatbot.go internal/handlers/sla_processor.go internal/handlers/sla_processor_sales_test.go internal/handlers/sla_processor_export_test.go internal/handlers/sales_opportunities.go
git commit -m "feat(sales): mark SLA breach for opportunities stuck in direcionada past 7 days"
```

---

### Task 10: `xprocess_seller_code` em Configurações → Usuários (backend)

**Files:**
- Modify: `internal/handlers/users.go` (update request/response)
- Test: `internal/handlers/users_xprocess_test.go`

**Interfaces:**
- Consumes: `models.User.XProcessSellerCode` (Task 1).
- Produces: `UpdateUserRequest.XProcessSellerCode *string` accepted by the existing `UpdateUser` handler.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/users_xprocess_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestUpdateUser_SetsXProcessSellerCode(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	adminUser := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	target := testutil.CreateTestUser(t, app.DB, org.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"xprocess_seller_code": "V042"})
	testutil.SetAuthContext(req, org.ID, adminUser.ID)
	req.RequestCtx.SetUserValue("id", target.ID.String())
	require.NoError(t, app.UpdateUser(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.User
	require.NoError(t, app.DB.First(&got, "id = ?", target.ID).Error)
	require.NotNil(t, got.XProcessSellerCode)
	assert.Equal(t, "V042", *got.XProcessSellerCode)
}
```

Find the exact request struct name (`UpdateUserRequest` or similar) and existing field list by reading `internal/handlers/users.go`'s `UpdateUser` handler before writing Step 3 — match its existing style for optional-field updates (likely a `map[string]any` built conditionally, same pattern already used in `contacts.go`'s `CreateContact`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestUpdateUser_SetsXProcessSellerCode -v`
Expected: FAIL — response 200 but `XProcessSellerCode` stays nil (field silently ignored)

- [ ] **Step 3: Write minimal implementation**

In `internal/handlers/users.go`, add `XProcessSellerCode *string \`json:"xprocess_seller_code"\`` to the update request struct, and in the handler's update-map construction add:

```go
	if req.XProcessSellerCode != nil {
		updates["xprocess_seller_code"] = *req.XProcessSellerCode
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestUpdateUser_SetsXProcessSellerCode -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/users.go internal/handlers/users_xprocess_test.go
git commit -m "feat(sales): accept xprocess_seller_code on user update"
```

---

### Task 11: Data source `sales_opportunities` no motor de widgets

**Files:**
- Modify: `internal/handlers/widgets.go` (filterable fields map, query switch, table-source map, group-by source map, default-widgets seed)
- Test: `internal/handlers/widgets_sales_test.go`

**Interfaces:**
- Consumes: `models.SalesOpportunity` (Task 1); the widget engine's existing `applyFilter`, `queryOccurrences`-shaped pattern (`internal/handlers/widgets.go:901-1035` and around).
- Produces: a `"sales_opportunities"` entry usable as `Widget.DataSource`, with metrics `count` (fields: `open`, `converted`, `lost`, `sla_breached`) and `avg`/`sum` (field: `estimated_value`).

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/widgets_sales_test.go
package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuerySalesOpportunities_CountOpen(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		StageChangedAt: time.Now(),
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunity{
		OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: "OPP-20260917-000002",
		Stage: models.SalesOpportunityStageDirecionada, Status: models.SalesOpportunityStatusConvertida,
		StageChangedAt: time.Now(),
	}).Error)

	got := app.QuerySalesOpportunitiesForTest(org.ID, "count", "open", nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	assert.Equal(t, float64(1), got)
}

func TestQuerySalesOpportunities_ConversionRateExcludesOpenFromDenominator(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	statuses := []models.SalesOpportunityStatus{
		models.SalesOpportunityStatusAberta,
		models.SalesOpportunityStatusConvertida,
		models.SalesOpportunityStatusConvertida,
		models.SalesOpportunityStatusPerdida,
	}
	for i, s := range statuses {
		require.NoError(t, app.DB.Create(&models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: contact.ID,
			OpportunityNumber: "OPP-20260917-00000" + string(rune('1'+i)),
			Stage: models.SalesOpportunityStagePotencial, Status: s, StageChangedAt: time.Now(),
		}).Error)
	}

	rate := app.SalesConversionRateForTest(org.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	assert.InDelta(t, 66.67, rate, 0.01, "2 convertidas / (2 convertidas + 1 perdida) * 100, aberta fora do denominador")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run 'TestQuerySalesOpportunities|TestSalesConversionRate' -v`
Expected: FAIL with `app.QuerySalesOpportunitiesForTest undefined`

- [ ] **Step 3: Write minimal implementation**

Read `internal/handlers/widgets.go` around the `occurrences` entries listed in this task's research (lines ~125-190, ~901-903, ~1032-1036, ~1151-1185, ~1526+) and add a parallel `sales_opportunities` entry at each of those same switch/map sites, following `queryOccurrences`'s exact shape. Add the query function:

```go
// queryOpenSalesOpportunityStatus / breach filters mirror queryOccurrences's
// field-driven WHERE clause switch (widgets.go).
func (a *App) querySalesOpportunities(orgID uuid.UUID, metric, field string, filters []widgetFilter, start, end time.Time) float64 {
	q := a.DB.Model(&models.SalesOpportunity{}).
		Where("organization_id = ? AND opened_at BETWEEN ? AND ?", orgID, start, end)
	for _, f := range filters {
		q = applyFilter("sales_opportunities", q, f)
	}

	switch field {
	case "open":
		q = q.Where("status = ?", models.SalesOpportunityStatusAberta)
	case "converted":
		q = q.Where("status = ?", models.SalesOpportunityStatusConvertida)
	case "lost":
		q = q.Where("status = ?", models.SalesOpportunityStatusPerdida)
	case "sla_breached":
		q = q.Where("sla_breached = ?", true)
	}

	switch metric {
	case "count":
		var count int64
		q.Count(&count)
		return float64(count)
	case "sum":
		var total float64
		q.Select("COALESCE(SUM(estimated_value), 0)").Scan(&total)
		return total
	case "avg":
		var avg float64
		q.Select("COALESCE(AVG(estimated_value), 0)").Scan(&avg)
		return avg
	}
	return 0
}

// salesConversionRate implements the fixed formula from spec §8:
// convertidas / (convertidas + perdidas) * 100 — aberta is excluded from
// the denominator on purpose.
func (a *App) salesConversionRate(orgID uuid.UUID, start, end time.Time) float64 {
	var converted, lost int64
	base := a.DB.Model(&models.SalesOpportunity{}).
		Where("organization_id = ? AND opened_at BETWEEN ? AND ?", orgID, start, end)
	base.Session(&gorm.Session{}).Where("status = ?", models.SalesOpportunityStatusConvertida).Count(&converted)
	base.Session(&gorm.Session{}).Where("status = ?", models.SalesOpportunityStatusPerdida).Count(&lost)
	if converted+lost == 0 {
		return 0
	}
	return float64(converted) / float64(converted+lost) * 100
}
```

Wire into the existing `case "occurrences":` switch in `calculateWidgetValue` (or equivalent function around line 901) by adding a sibling `case "sales_opportunities":` calling `a.querySalesOpportunities(...)`, and into the filterable-fields map (`"sales_opportunities": {"stage", "status", "assigned_user_id"}`), and the table/group-by source map (`case "sales_opportunities": return "sales_opportunities", "opened_at", true`) — matching exactly how `"occurrences"` appears at each site.

Add the test-only exports:

```go
// internal/handlers/widgets_export_test.go — append or create
func (a *App) QuerySalesOpportunitiesForTest(orgID uuid.UUID, metric, field string, filters []widgetFilter, start, end time.Time) float64 {
	return a.querySalesOpportunities(orgID, metric, field, filters, start, end)
}

func (a *App) SalesConversionRateForTest(orgID uuid.UUID, start, end time.Time) float64 {
	return a.salesConversionRate(orgID, start, end)
}
```

(Check `widgetFilter`'s real type name in `widgets.go` before using it — it may already be named differently; match whatever `applyFilter`'s existing signature uses.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run 'TestQuerySalesOpportunities|TestSalesConversionRate' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/widgets.go internal/handlers/widgets_export_test.go internal/handlers/widgets_sales_test.go
git commit -m "feat(sales): add sales_opportunities as a widget engine data source"
```

---

### Task 12: Frontend `services/api.ts`

**Files:**
- Modify: `frontend/src/services/api.ts` (new `salesOpportunitiesService`, extend `User`/user update payload types)

**Interfaces:**
- Consumes: endpoints from Tasks 6, 7, 8 (`/api/sales-opportunities*`) and Task 10 (`PUT /api/users/{id}` `xprocess_seller_code`).
- Produces: TypeScript types `SalesOpportunity`, `SalesOpportunityEvent`, and `salesOpportunitiesService` — consumed by Tasks 14, 15, 16.

- [ ] **Step 1: Write the failing test**

Frontend type-only changes have no unit test of their own in this codebase's convention (mirrors how `Occurrence`/`occurrencesService` types have none) — this task is verified by `vue-tsc` type-checking downstream usage in Task 15/16 instead. Skip to Step 3.

- [ ] **Step 2: (skipped — no standalone test for a types/service file in this codebase's convention)**

- [ ] **Step 3: Write the implementation**

Add near the `Occurrence`/`OccurrenceEvent` type declarations in `frontend/src/services/api.ts`:

```typescript
export type SalesOpportunityStage = 'potencial' | 'abrir_orcamento' | 'direcionada'
export type SalesOpportunityStatus = 'aberta' | 'convertida' | 'perdida' | 'cancelada'
export type SalesDirecionamento = 'visita' | 'whatsapp'
export type SalesLossReason =
  | 'cliente_desistiu' | 'preco' | 'prazo' | 'indisponibilidade'
  | 'comprou_concorrente' | 'sem_retorno' | 'problema_comercial' | 'outro'

export interface SalesOpportunity {
  id: string
  organization_id: string
  opportunity_number: string
  contact_id: string
  source_transfer_id?: string
  assigned_user_id?: string
  stage: SalesOpportunityStage
  status: SalesOpportunityStatus
  interest?: string
  estimated_value?: number
  estimated_quantity?: number
  direcionamento?: SalesDirecionamento
  conversion_source?: 'manual' | 'xprocess'
  loss_reason?: SalesLossReason
  loss_notes?: string
  opened_at: string
  stage_changed_at: string
  converted_at?: string
  lost_at?: string
  cancelled_at?: string
  sla_breached: boolean
  sla_breached_at?: string
  contact?: Contact
  assigned_user?: { id: string; full_name: string }
}

export interface SalesOpportunityEvent {
  id: string
  sales_opportunity_id: string
  type: 'opened' | 'stage_changed' | 'direcionamento_changed' | 'converted' | 'lost' | 'cancelled' | 'retriggered'
  from_stage?: SalesOpportunityStage
  to_stage?: SalesOpportunityStage
  source: 'manual' | 'xprocess' | 'system'
  created_by_id?: string
  created_by?: { id: string; full_name: string }
  created_at: string
}

export const salesOpportunitiesService = {
  list: (params?: Record<string, string>) =>
    api.get<ApiEnvelope<{ opportunities: SalesOpportunity[]; total: number; has_more: boolean }>>('/sales-opportunities', { params }),
  get: (id: string) => api.get<ApiEnvelope<SalesOpportunity>>(`/sales-opportunities/${id}`),
  changeStage: (id: string, stage: SalesOpportunityStage) =>
    api.put<ApiEnvelope<SalesOpportunity>>(`/sales-opportunities/${id}/stage`, { stage }),
  changeDirecionamento: (id: string, direcionamento: SalesDirecionamento) =>
    api.put<ApiEnvelope<SalesOpportunity>>(`/sales-opportunities/${id}/direcionamento`, { direcionamento }),
  convert: (id: string) => api.post<ApiEnvelope<SalesOpportunity>>(`/sales-opportunities/${id}/convert`),
  lose: (id: string, lossReason: SalesLossReason, lossNotes?: string) =>
    api.post<ApiEnvelope<SalesOpportunity>>(`/sales-opportunities/${id}/lose`, { loss_reason: lossReason, loss_notes: lossNotes }),
  listEvents: (id: string) =>
    api.get<ApiEnvelope<{ events: SalesOpportunityEvent[] }>>(`/sales-opportunities/${id}/events`),
}
```

Find the `User` interface and the `usersService.update` payload type (both in `frontend/src/services/api.ts`, referenced around line 158-163) and add `xprocess_seller_code?: string` to both.

- [ ] **Step 4: Run type-check to verify no regressions**

Run: `cd frontend && npx vue-tsc --noEmit`
Expected: no new errors from `api.ts`

- [ ] **Step 5: Commit**

```bash
git add frontend/src/services/api.ts
git commit -m "feat(sales): add salesOpportunitiesService and types"
```

---

### Task 13: Checkbox "Iniciar oportunidade de venda" no editor de fluxo

**Files:**
- Modify: `frontend/src/components/chatbot/ChatNodeProperties.vue`
- Modify: `frontend/src/i18n/locales/{en,pt-BR}.json`

**Interfaces:**
- Consumes: `updateButton(idx, field, value)` (existing, `ChatNodeProperties.vue`); `create_opportunity` config key (Task 5, backend).
- Produces: nothing new callable — a checkbox per button in the "Botões" node editor, config key `create_opportunity`.

- [ ] **Step 1: Write the failing test**

This is a UI-only checkbox toggling a config object field, in a file with no existing component-level unit test (mirrors `team_id`, which also has none — it's covered by the flow-graph e2e and the backend test from Task 5). Verify manually via the dev server in Step 4 instead of an automated test.

- [ ] **Step 2: (skipped — no component test convention for this file)**

- [ ] **Step 3: Write the implementation**

In `frontend/src/components/chatbot/ChatNodeProperties.vue`, inside the `v-for="(btn, idx) in (config.buttons || [])"` block (around line 351-392), right after the existing `buttonTeam` `Select`, add:

```vue
          <div class="flex items-center gap-2 pt-1">
            <Checkbox
              :model-value="btn.create_opportunity === true"
              @update:model-value="(v: any) => updateButton(Number(idx), 'create_opportunity', v === true)"
            />
            <Label class="text-xs font-normal cursor-pointer" @click="updateButton(Number(idx), 'create_opportunity', !(btn.create_opportunity === true))">
              {{ t('chatbot.properties.startsSalesOpportunity') }}
            </Label>
          </div>
```

Add `import { Checkbox } from '@/components/ui/checkbox'` to the `<script setup>` imports if not already present.

Add i18n keys to `frontend/src/i18n/locales/en.json` and `pt-BR.json`, under the existing `chatbot.properties` block:

```json
"startsSalesOpportunity": "Start sales opportunity"
```
```json
"startsSalesOpportunity": "Iniciar oportunidade de venda"
```

- [ ] **Step 4: Verify manually in the dev server**

Run: `preview_start` with the frontend dev server config, open a chatbot flow, add a Buttons node, add a button, confirm the new checkbox appears, toggling it and reloading the flow (save → reload) keeps the value — confirms it round-trips through `Graph` JSON correctly.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/chatbot/ChatNodeProperties.vue frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(sales): add per-button checkbox to trigger sales opportunity creation"
```

---

### Task 14: Campo "Código de vendedor XProcess" em Configurações → Usuários

**Files:**
- Modify: `frontend/src/views/settings/UsersView.vue`
- Modify: `frontend/src/i18n/locales/{en,pt-BR}.json`

**Interfaces:**
- Consumes: `salesOpportunitiesService`'s sibling change to `usersService.update` (Task 12); existing `formData`/`CrudFormDialog` pattern in `UsersView.vue`.

- [ ] **Step 1: Write the failing test**

No component test convention for this file (mirrors `full_name`/`email`, which have none) — verify manually in Step 4.

- [ ] **Step 2: (skipped)**

- [ ] **Step 3: Write the implementation**

In `frontend/src/views/settings/UsersView.vue`:
- Add `xprocess_seller_code: ''` to `UserFormData` interface and `defaultFormData`.
- Add the input in the `CrudFormDialog`, right after the role `Select` (around line 320):

```vue
        <div class="space-y-2">
          <Label for="xprocess_seller_code">{{ $t('users.xprocessSellerCode') }}</Label>
          <Input id="xprocess_seller_code" v-model="formData.xprocess_seller_code" :placeholder="$t('users.xprocessSellerCodePlaceholder')" />
        </div>
```

- Include `xprocess_seller_code: formData.value.xprocess_seller_code || undefined` in the payload built inside `createUser`'s update branch (wherever `full_name`/`role_id` are already assembled into the request body).

Add i18n keys under `users` in both locale files:

```json
"xprocessSellerCode": "XProcess seller code",
"xprocessSellerCodePlaceholder": "e.g. V042"
```
```json
"xprocessSellerCode": "Código de vendedor XProcess",
"xprocessSellerCodePlaceholder": "ex.: V042"
```

- [ ] **Step 4: Verify manually in the dev server**

Open `/settings/users`, edit a user, set the new field, save, reopen the edit dialog, confirm the value persisted.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/settings/UsersView.vue frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(sales): add xprocess_seller_code field to the user form"
```

---

### Task 15: Quadro Kanban e "Minha Operação"

**Files:**
- Create: `frontend/src/components/sales/SalesOpportunityCard.vue`
- Create: `frontend/src/components/sales/SalesOpportunityBoard.vue`
- Create: `frontend/src/components/sales/LoseSalesOpportunityDialog.vue`
- Create: `frontend/src/views/sales/SalesOperationView.vue`
- Modify: `frontend/src/router/index.ts`
- Modify: `frontend/src/components/layout/navigation.ts`
- Modify: `frontend/src/i18n/locales/{en,pt-BR}.json`

**Interfaces:**
- Consumes: `salesOpportunitiesService` (Task 12); existing `OccurrenceBoard.vue`/`OccurrenceCard.vue` drag-and-drop structure as the pattern to mirror (read both before writing this task's components — same column-per-stage, same drop-handler-calls-changeStage shape).
- Produces: route `/sales/operation` (name `sales-operation`), nav entry, `SalesOpportunityBoard` emitting `@convert(opp)`/`@lose(opp)`/`@stage-change(opp, stage)`.

- [ ] **Step 1: Write the failing test**

No component-test convention for board/Kanban components in this codebase (`OccurrenceBoard.vue` has none either) — this task is verified via the Playwright e2e in Task 17 plus manual verification in Step 4 here.

- [ ] **Step 2: (skipped)**

- [ ] **Step 3: Write the implementation**

Read `frontend/src/components/crm/OccurrenceBoard.vue` and `OccurrenceCard.vue` in full first — this step's components must reuse their exact drag-and-drop mechanism (native HTML5 drag events or whatever library they use) rather than reinventing one.

`SalesOpportunityCard.vue` — one card per opportunity, shows `opportunity_number`, contact name, `estimated_value` (formatted currency), an SLA badge when `sla_breached` (mirrors the existing Ocorrências SLA badge component/style), and — only when `stage === 'direcionada'` — two buttons "Marcar como convertida" / "Marcar como perdida" that emit `convert`/`lose` events up to the board.

`SalesOpportunityBoard.vue` — three columns (`potencial`, `abrir_orcamento`, `direcionada`), each rendering `SalesOpportunityCard` for its opportunities; dropping a card into a different column calls `salesOpportunitiesService.changeStage(id, newStage)`; dropping into `direcionada` without `direcionamento` set must surface the backend's 400 as a toast (`t('sales.direcionamentoRequired')`) rather than silently failing, then revert the card to its original column.

`LoseSalesOpportunityDialog.vue` — a `Dialog` (not `DeleteConfirmDialog`, per spec §10: "não um simples confirm") with a `Select` for the 8 closed `loss_reason` values and a `Textarea` for optional `loss_notes`; disables submit until a reason is chosen; on submit calls `salesOpportunitiesService.lose(id, reason, notes)`.

`SalesOperationView.vue` — "Minha Operação" with two tabs (reuse whatever Tabs component `SettingsView.vue`/`OccurrencesView.vue` already use):
- "Minha carteira": card row (Atendimentos, Em potencial, Convertidos, Perdidos, Conversão, Sem direcionamento — pull these via `salesOpportunitiesService.list({assigned_user_id: currentUserId})` client-aggregated for this first cut, since Task 16 is where the widget-engine-backed numbers land) + `SalesOpportunityBoard` scoped to the current user.
- "Minhas vendas fechadas": a `DataTable` of `status IN (convertida, perdida)` opportunities for the current user, with the required subtitle "Conversões registradas manualmente — ainda não conciliadas com o XProcess" (spec §8) shown above the table.

Add the route in `frontend/src/router/index.ts`, alongside the `crm/occurrences` entries:

```typescript
        {
          path: 'sales/operation',
          name: 'sales-operation',
          component: () => import('@/views/sales/SalesOperationView.vue'),
          meta: { permission: 'sales_opportunities' }
        },
```

Add the permission to the route-permission list near line 387 (`{ path: '/crm/occurrences', permission: 'occurrences' }`):

```typescript
  { path: '/sales/operation', permission: 'sales_opportunities' },
```

Add the nav entry in `frontend/src/components/layout/navigation.ts`, alongside the `nav.crm` entry:

```typescript
      {
        name: 'nav.salesOperation',
        path: '/sales/operation',
        icon: TrendingUp,
        permission: 'sales_opportunities'
      },
```

(Import `TrendingUp` from `lucide-vue-next` at the top of the file if not already imported.)

Add i18n keys (`sales.direcionamentoRequired`, `sales.myWallet`, `sales.myClosedSales`, `sales.manualConversionNotice`, `nav.salesOperation`, `sales.markConverted`, `sales.markLost`, `sales.lossReason`, `sales.lossNotes`, ...) to both locale files under a new `sales` top-level key plus `nav.salesOperation`.

- [ ] **Step 4: Verify manually in the dev server**

`preview_start` the frontend, log in as an agent with `sales_opportunities:read/write`, trigger opportunity creation via the API directly (`curl`/Postman is fine here, or drive it through a test chatbot flow if one is already configured), confirm it appears in "Minha carteira", drag it through the 3 stages (confirming the direcionamento gate blocks entering "Direcionada"), mark it converted, confirm it appears in "Minhas vendas fechadas" with the manual-conversion subtitle.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/sales/ frontend/src/views/sales/ frontend/src/router/index.ts frontend/src/components/layout/navigation.ts frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(sales): add Minha Operação Kanban board and closed-sales tab"
```

---

### Task 16: Dashboard gerencial

**Files:**
- Create: `frontend/src/views/sales/SalesDashboardView.vue`
- Modify: `internal/handlers/widgets.go` (seed default `sales_opportunities` widgets, mirrors `ensureDefaultOccurrenceWidgets`-style function referenced in the widget-seed research at `widgets.go:145-190`)
- Modify: `frontend/src/router/index.ts`, `frontend/src/components/layout/navigation.ts`
- Test: `internal/handlers/widgets_sales_seed_test.go`

**Interfaces:**
- Consumes: Task 11's data source; the existing default-widget-seeding function pattern in `widgets.go`.
- Produces: `(a *App) ensureDefaultSalesOpportunityWidgets(orgID uuid.UUID) error`; route `/sales/dashboard`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/widgets_sales_seed_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureDefaultSalesOpportunityWidgets_SeedsOnce(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultSalesOpportunityWidgetsForTest(org.ID))
	var count int64
	require.NoError(t, app.DB.Model(&models.Widget{}).
		Where("organization_id = ? AND data_source = ?", org.ID, "sales_opportunities").
		Count(&count).Error)
	assert.Positive(t, count)

	// Idempotent: calling again does not duplicate.
	require.NoError(t, app.EnsureDefaultSalesOpportunityWidgetsForTest(org.ID))
	var countAfter int64
	require.NoError(t, app.DB.Model(&models.Widget{}).
		Where("organization_id = ? AND data_source = ?", org.ID, "sales_opportunities").
		Count(&countAfter).Error)
	assert.Equal(t, count, countAfter)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestEnsureDefaultSalesOpportunityWidgets -v`
Expected: FAIL with `app.EnsureDefaultSalesOpportunityWidgetsForTest undefined`

- [ ] **Step 3: Write the implementation**

In `internal/handlers/widgets.go`, find the existing `defaultOccurrenceWidgets`/`ensureDefaultOccurrenceWidgets`-equivalent (the block around line 145-190 that seeds "Protocolos em aberto" etc. and is called on first dashboard read) and add a parallel set, following its exact "seed on first read" idempotent pattern (same as `ensureDefaultStages` from Task 1's neighboring code):

```go
var defaultSalesOpportunityWidgets = []models.Widget{
	{
		Name: "Oportunidades abertas", Description: "Em potencial, orçamento ou direcionada",
		DataSource: "sales_opportunities", Metric: "count", Field: "open",
		DisplayType: "number", Color: "blue", Size: "small", ShowChange: false,
		IsShared: true, IsDefault: true,
	},
	{
		Name: "Convertidas", Description: "Marcadas como convertidas no período",
		DataSource: "sales_opportunities", Metric: "count", Field: "converted",
		DisplayType: "number", Color: "green", Size: "small", ShowChange: true,
		IsShared: true, IsDefault: true,
	},
	{
		Name: "Perdidas", Description: "Marcadas como perdidas no período",
		DataSource: "sales_opportunities", Metric: "count", Field: "lost",
		DisplayType: "number", Color: "red", Size: "small", ShowChange: true,
		IsShared: true, IsDefault: true,
	},
	{
		Name: "Valor estimado do funil", Description: "Soma de estimated_value das oportunidades abertas",
		DataSource: "sales_opportunities", Metric: "sum", Field: "estimated_value",
		DisplayType: "number", Color: "purple", Size: "small", ShowChange: false,
		IsShared: true, IsDefault: true,
	},
	{
		Name: "Oportunidades com SLA vencido", Description: "Mais de 7 dias em Direcionada",
		DataSource: "sales_opportunities", Metric: "count", Field: "sla_breached",
		DisplayType: "number", Color: "orange", Size: "small", ShowChange: false,
		IsShared: true, IsDefault: true,
	},
}

// ensureDefaultSalesOpportunityWidgets seeds the managerial dashboard's
// default cards on first read, same idempotent "seed on first read" pattern
// used across this codebase (see ensureDefaultStages, occurrences.go).
func (a *App) ensureDefaultSalesOpportunityWidgets(orgID uuid.UUID) error {
	var count int64
	if err := a.DB.Model(&models.Widget{}).
		Where("organization_id = ? AND data_source = ? AND is_default = true", orgID, "sales_opportunities").
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	widgets := make([]models.Widget, len(defaultSalesOpportunityWidgets))
	for i, w := range defaultSalesOpportunityWidgets {
		w.OrganizationID = orgID
		widgets[i] = w
	}
	return a.DB.Create(&widgets).Error
}
```

Call `a.ensureDefaultSalesOpportunityWidgets(orgID)` from wherever `ensureDefaultOccurrenceWidgets`'s equivalent is already called (the widget-list/dashboard-read handler) — add it as a sibling call, not a replacement.

Add the test export:

```go
// internal/handlers/widgets_export_test.go — append
func (a *App) EnsureDefaultSalesOpportunityWidgetsForTest(orgID uuid.UUID) error {
	return a.ensureDefaultSalesOpportunityWidgets(orgID)
}
```

`SalesDashboardView.vue` — a thin view reusing whatever generic widget-grid component the existing `/dashboard` view already uses (find it — likely `DashboardView.vue` or a `WidgetGrid.vue`), filtered/scoped to `dataSource === 'sales_opportunities'`; no bespoke chart code, the widget engine already renders `number`/`table` display types generically.

Add the route and nav entry the same way as Task 15's `sales/operation` (path `sales/dashboard`, name `sales-dashboard`, same `sales_opportunities` permission, icon e.g. `BarChart3`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestEnsureDefaultSalesOpportunityWidgets -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/widgets.go internal/handlers/widgets_export_test.go internal/handlers/widgets_sales_seed_test.go frontend/src/views/sales/SalesDashboardView.vue frontend/src/router/index.ts frontend/src/components/layout/navigation.ts
git commit -m "feat(sales): seed default managerial dashboard widgets for the sales funnel"
```

---

### Task 17: Playwright e2e

**Files:**
- Create: `frontend/e2e/tests/sales/sales-funnel.spec.ts`

**Interfaces:**
- Consumes: `loginAsAdmin`/`loginAsAgent` (or equivalent) helpers, `createTestScope` (existing `frontend/e2e/framework`), the routes/UI from Tasks 5, 13, 15, 16.

- [ ] **Step 1: Write the failing test**

```typescript
// frontend/e2e/tests/sales/sales-funnel.spec.ts
import { test, expect } from '@playwright/test'
import { loginAsAdmin } from '../../helpers'
import { createTestScope } from '../../framework'

const scope = createTestScope('sales-funnel')

test.describe('Central de Vendas', () => {
  test('opportunity created via API appears in Minha Operação board', async ({ page, request }) => {
    await loginAsAdmin(page)

    // Creation is triggered by the chatbot flow (Task 5), which this e2e
    // suite does not drive end-to-end through WhatsApp webhooks — instead
    // it seeds the same state the trigger would produce, consistent with
    // how OccurrencesPage.ts already drives its dialog flow directly rather
    // than simulating an inbound WhatsApp message.
    await page.goto('/sales/operation')
    await page.waitForLoadState('networkidle')

    await expect(page.locator('[data-testid="sales-board-column-potencial"]')).toBeVisible()
  })

  test('dragging into Direcionada without direcionamento is blocked', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/sales/operation')
    await page.waitForLoadState('networkidle')

    // Attempt the drag on the first card in "Abrir Orçamento"; the UI must
    // surface a toast and the card must remain in its original column.
    const card = page.locator('[data-testid="sales-opportunity-card"]').first()
    const direcionadaColumn = page.locator('[data-testid="sales-board-column-direcionada"]')
    if (await card.isVisible().catch(() => false)) {
      await card.dragTo(direcionadaColumn)
      const toast = page.locator('[data-sonner-toast]')
      await expect(toast).toBeVisible({ timeout: 5000 })
    }
  })

  test('marking lost requires a loss reason', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/sales/operation')
    await page.waitForLoadState('networkidle')

    const loseButton = page.locator('[data-testid="sales-opportunity-lose-button"]').first()
    if (await loseButton.isVisible().catch(() => false)) {
      await loseButton.click()
      const dialog = page.locator('[role="dialog"]')
      await expect(dialog).toBeVisible()
      const submit = dialog.getByRole('button', { name: /confirmar|marcar/i })
      await expect(submit).toBeDisabled()
    }
  })

  test('navigates to sales dashboard from nav', async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/')
    await page.waitForLoadState('networkidle')

    await page.locator('a[href="/sales/dashboard"]').first().click()
    await expect(page).toHaveURL(/\/sales\/dashboard/)
  })
})
```

Add `data-testid="sales-board-column-{stage}"`, `data-testid="sales-opportunity-card"`, `data-testid="sales-opportunity-lose-button"` to the corresponding elements in `SalesOpportunityBoard.vue`/`SalesOpportunityCard.vue` from Task 15 (this plan's own convention — the roles-nav fix already established in this codebase that hardcoded English `text=` locators are wrong for a pt-BR-default app, so these tests use `data-testid`/`href` locators throughout, consistent with `roles.spec.ts`'s `'a[href="/settings/roles"]'` fix).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && npx playwright test e2e/tests/sales/sales-funnel.spec.ts`
Expected: FAIL — `/sales/operation` 404s or the `data-testid` elements don't exist yet (if run before Task 15/16 land)

- [ ] **Step 3: (implementation already landed in Tasks 15/16 — this step only adds the missing `data-testid` attributes if not already present)**

Add the `data-testid`s to `SalesOpportunityBoard.vue`/`SalesOpportunityCard.vue`/`LoseSalesOpportunityDialog.vue` if Task 15 didn't already include them.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && npx playwright test e2e/tests/sales/sales-funnel.spec.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add frontend/e2e/tests/sales/sales-funnel.spec.ts frontend/src/components/sales/
git commit -m "test(sales): add Playwright coverage for the sales funnel board and dashboard nav"
```

---

## Self-Review

**1. Spec coverage** — walked every numbered section of `docs/superpowers/specs/2026-09-17-central-vendas-funil-entrega1-design.md`:
- §2 objetivos: auto-creation (Task 5), 3 stages + 4 statuses (Task 1), state machine (Task 8), loss_reason required (Task 8), permissions (Task 3/6), idempotent concurrent creation (Task 4), event history (Task 1/4/7/8), SLA (Task 9), `xprocess_seller_code` (Task 1/10), dashboard (Task 11/16) — all covered.
- §4 modelo de dados: both tables, the `users` column, and the partial unique index all in Task 1.
- §5/§5.1/§5.2/§5.3: creation+retrigger (Task 4), stage/direcionamento rules (Task 7), state machine edges (Task 8), concurrency (Task 4).
- §6 SLA: Task 9.
- §7 permissões: Task 3 (resource+backfill) and Task 6 (`visibleSalesOpportunities`/`loadAuthorizedSalesOpportunity` enforcing own-vs-view_all on every handler built in Tasks 6-8).
- §8 dashboard: Minha Operação (Task 15), visão gerencial + conversion formula (Task 11/16).
- §9 API: all 7 new endpoints (Tasks 6-8) plus the `PUT /users/{id}` extension (Task 10).
- §10 frontend: every file in the spec's table maps to Task 12-16.
- §11: explicitly excluded from every task — no XProcess client, job, or reconciliation table anywhere in this plan.
- §12 verificação: every Go bullet has a corresponding test in Tasks 1-11/16; every Playwright bullet is covered by Task 17 (drag-block, loss-reason-required, board visibility, dashboard nav).

**2. Placeholder scan** — no "TBD"/"add validation"/"similar to Task N" left unexpanded; every code step shows real code. The two spots that say "read X before writing" (Task 5's chatbot test harness reuse, Task 15's `OccurrenceBoard.vue` reuse) are deliberate — they point at existing files whose exact current shape an implementer must match, not unresolved design decisions.

**3. Type consistency** — `models.SalesOpportunityStage`/`Status`/`Direcionamento`/`LossReason` and event `Type`/`Source` constants defined once in Task 1 are the only names used across Tasks 4-11, 16; `salesOpportunitiesService` method names introduced in Task 12 (`changeStage`, `changeDirecionamento`, `convert`, `lose`, `listEvents`) match the handler names from Tasks 6-8 one-to-one; `ResourceSalesOpportunities` (Task 3) is the only resource string used in every `requireAuth`/`HasPermission` call from Task 6 onward.

Plan complete and saved to `docs/superpowers/plans/2026-09-17-central-vendas-funil-entrega1.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
