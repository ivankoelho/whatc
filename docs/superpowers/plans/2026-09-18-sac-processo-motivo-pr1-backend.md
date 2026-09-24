# SAC Processo/Motivo — PR1: Domain + Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `OccurrenceProcess`/`OccurrenceProcessMessage` domain, seeded from the real validated process map, fully wired into occurrence creation/SLA/permissions/audit — a complete, independently mergeable backend slice. No frontend changes in this PR.

**Architecture:** `OccurrenceProcess` sits beside the existing `OccurrenceCategory`/`OccurrenceWhatHappened` flat dimensions (does not replace or merge with them), optionally overrides the two SLA-minutes integers the existing `getSLAPolicy` mechanism already produces, and lists which of `Occurrence`'s existing optional fields are required — no new dynamic-field engine, no `ProcessFieldValues` escape hatch (considered and dropped: nothing would read it in this phase). Exactly one active process per `(organization, WhatHappenedID)` is enforced both at the application layer (409 on conflict) and with a partial unique index as a race backstop, because `ResolveOccurrenceProcess` relies on `.First()` returning a deterministic row. Messages resolve `[Bracket]`-style variables (matching the real seed content's own syntax) via one small new string-replace helper, and reuse the *existing* `OccurrenceEvent` timeline to log usage — no second history table.

**Tech Stack:** Go 1.x + GORM (AutoMigrate, no migration files) + fastglue handlers, Postgres.

**Source of truth for real process data:** `C:\Users\Ivan Coelho\Downloads\central-sac-vendas (4) (1).html`, nodes with a `sacMsg` field in its embedded `const A = [...]` tree. The five real SAC-opening processes are nodes `1.2`, `1.3`, `2.1`, `2.2`, `2.3`. Task 3 transcribes their exact guidance/restrictions/messages/evidence/timing — do not invent additional processes.

## Global Constraints

- Never touch `main` directly; branch `feature/sac-processo-motivo-pr1` from `development` (per project git-workflow rule).
- No new SLA mechanism, no new protocol numbering, no new Kanban, no new contact system, no new timeline table, no new permission *system* (only new permission *keys* following the exact existing pattern), no new message/variable engine beyond the one small resolver this plan adds.
- `internal/database/postgres.go`'s `GetMigrationModels()` + GORM `AutoMigrate` is the only migration mechanism in this repo — no `.sql` migration files anywhere.
- Every new permission resource needs a **standalone backfill function** (own idempotency guard) — a grouped backfill's "already migrated" check would silently skip orgs that predate this feature (the Fase 3 lesson).
- Every new/changed response DTO field must actually be read by something later in this plan or a documented follow-up PR — no dead fields (this is why `ProcessFieldValues` was cut).
- **Exactly one active `OccurrenceProcess` per `(organization_id, what_happened_id)`** — enforced by both application validation and a DB partial unique index. `ResolveOccurrenceProcess`'s `.First()` depends on this invariant; do not weaken it without also changing that endpoint to handle multiple matches explicitly.
- **Time semantics:** `OccurrenceProcess.ResponseMinutes`/`ResolutionMinutes` use the exact same calendar-minutes semantics `OccurrenceSLAPolicy` already uses — there is no business-hours calendar anywhere in this codebase's SLA handling, in this phase or before it. A seed comment quoting "5 dias úteis" names the *source text's wording*, not a claim that the stored integer models business days.
- **Business decision, not a technical one:** the Category/WhatHappened mapping chosen in Task 3's seed table, and the calendar-minutes values derived from each process's prazo text, are this plan's first-cut interpretation of the validated HTML — not something the HTML states in the system's own Category/WhatHappened vocabulary. Task 9's final report must present both as open pendências for the product owner, not as settled technical decisions.

---

### Task 1: `OccurrenceProcess` + `OccurrenceProcessMessage` models, AutoMigrate registration, `Occurrence.ProcessID`

**Files:**
- Create: `internal/models/occurrence_process.go`
- Modify: `internal/models/occurrences.go` (add `ProcessID` field + `Process` relation to `Occurrence`; add `OccurrenceEventProcessMessageUsed` const)
- Modify: `internal/database/postgres.go:120-126` (register the two new models)
- Test: `internal/models/occurrence_process_test.go`

**Interfaces:**
- Produces: `models.OccurrenceProcess{BaseModel, OrganizationID, Name, Description, CategoryID *uuid.UUID, WhatHappenedID *uuid.UUID, Guidance, Restrictions string, EvidenceChecklist, RequiredFields models.JSONBArray, ResponseMinutes *int, ResolutionMinutes *int, DepartmentID *uuid.UUID, IsActive bool, Position int}`, `TableName() string`.
- Produces: `models.OccurrenceProcessMessageStage` (string enum: `registration`/`documents`/`follow_up`/`forwarding`/`closing`), `models.OccurrenceProcessMessage{BaseModel, OrganizationID, ProcessID uuid.UUID, Stage OccurrenceProcessMessageStage, Content string, IsActive bool}`.
- Produces: `Occurrence.ProcessID *uuid.UUID`, `Occurrence.Process *OccurrenceProcess`, `models.OccurrenceEventProcessMessageUsed OccurrenceEventType`.
- **Note:** no `ProcessFieldValues` field is added to `Occurrence` — considered in an earlier draft of this plan and dropped: every field a process can require in this phase already has a dedicated `Occurrence` column (`invoice_number`/`product_description`/`purchase_date`/`sale_channel`), so a JSONB escape hatch would be a dead field from day one.

- [ ] **Step 1: Write the failing model test**

```go
// internal/models/occurrence_process_test.go
package models_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestOccurrenceProcess_TableName(t *testing.T) {
	assert.Equal(t, "occurrence_processes", models.OccurrenceProcess{}.TableName())
}

func TestOccurrenceProcessMessage_TableName(t *testing.T) {
	assert.Equal(t, "occurrence_process_messages", models.OccurrenceProcessMessage{}.TableName())
}

func TestOccurrenceProcess_RequiredFieldsRoundTrip(t *testing.T) {
	p := models.OccurrenceProcess{
		OrganizationID: uuid.New(),
		Name:           "Divergência no recebimento",
		RequiredFields: models.JSONBArray{"invoice_number", "product_description"},
	}
	val, err := p.RequiredFields.Value()
	assert.NoError(t, err)
	var back models.JSONBArray
	assert.NoError(t, back.Scan(val))
	assert.Equal(t, models.JSONBArray{"invoice_number", "product_description"}, back)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/... -run TestOccurrenceProcess -v`
Expected: FAIL — `models.OccurrenceProcess` undefined.

- [ ] **Step 3: Create the model file**

```go
// internal/models/occurrence_process.go
package models

import "github.com/google/uuid"

// OccurrenceProcessMessageStage identifies which point of the SAC lifecycle a
// suggested message belongs to — the five stages the product spec asked for.
type OccurrenceProcessMessageStage string

const (
	OccurrenceProcessMessageRegistration OccurrenceProcessMessageStage = "registration"
	OccurrenceProcessMessageDocuments    OccurrenceProcessMessageStage = "documents"
	OccurrenceProcessMessageFollowUp     OccurrenceProcessMessageStage = "follow_up"
	OccurrenceProcessMessageForwarding   OccurrenceProcessMessageStage = "forwarding"
	OccurrenceProcessMessageClosing      OccurrenceProcessMessageStage = "closing"
)

// OccurrenceProcess is the "Processo/Motivo" a WhatHappened reason maps to:
// it does not replace OccurrenceCategory/OccurrenceWhatHappened (both stay
// independent flat dimensions, per their own doc comments) — it sits beside
// them and tells the agent/system how a case with that reason should be
// handled: internal guidance, restrictions, which of Occurrence's existing
// optional fields are required, SLA parameters, and routing.
//
// Exactly one ACTIVE process may exist per (OrganizationID, WhatHappenedID) —
// see the unique index below and the validation in CreateOccurrenceProcess/
// UpdateOccurrenceProcess (Task 4). ResolveOccurrenceProcess (Task 5) resolves
// a reason to a process with a plain .First() and depends on this invariant
// being real, not just documented.
//
// ResponseMinutes/ResolutionMinutes are deliberately *pointers*: nil means
// "no process-specific override, fall back to the existing priority-based
// OccurrenceSLAPolicy" — this is not a second SLA mechanism, just an optional
// second source for the same two integers getSLAPolicy already produces.
// Both use the same calendar-minutes semantics OccurrenceSLAPolicy's own
// four default rows already use — no business-hours calendar in this phase.
//
// RequiredFields lists keys from Occurrence's existing optional fields
// ("invoice_number", "product_description", "purchase_date", "sale_channel")
// that this process needs filled — not a dynamic custom-field system. Every
// field the five real seeded processes need already exists as a plain
// Occurrence column.
type OccurrenceProcess struct {
	BaseModel
	OrganizationID uuid.UUID `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_process_what_happened,where:deleted_at IS NULL AND is_active = true" json:"organization_id"`

	Name        string `gorm:"size:150;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`

	CategoryID     *uuid.UUID `gorm:"type:uuid;index" json:"category_id,omitempty"`
	WhatHappenedID *uuid.UUID `gorm:"type:uuid;index;uniqueIndex:idx_occ_process_what_happened,where:deleted_at IS NULL AND is_active = true" json:"what_happened_id,omitempty"`

	// Guidance is for the agent only — never sent to the customer. Restrictions
	// is the "what NOT to say" list (§6 of the spec), same rule.
	Guidance     string `gorm:"type:text" json:"guidance"`
	Restrictions string `gorm:"type:text" json:"restrictions"`

	// EvidenceChecklist is a read-only informational list ("documentos a
	// solicitar ao cliente") — not real attachment/upload fields. A
	// document/evidence system is explicitly out of scope for this phase.
	EvidenceChecklist JSONBArray `gorm:"type:jsonb;default:'[]'" json:"evidence_checklist"`

	RequiredFields JSONBArray `gorm:"type:jsonb;default:'[]'" json:"required_fields"`

	ResponseMinutes   *int `json:"response_minutes,omitempty"`
	ResolutionMinutes *int `json:"resolution_minutes,omitempty"`

	DepartmentID *uuid.UUID `gorm:"type:uuid;index" json:"department_id,omitempty"`

	IsActive bool `gorm:"default:true" json:"is_active"`
	Position int  `gorm:"not null;default:0" json:"position"`

	Category     *OccurrenceCategory     `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	WhatHappened *OccurrenceWhatHappened `gorm:"foreignKey:WhatHappenedID" json:"what_happened,omitempty"`
	Department   *Department             `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
}

func (OccurrenceProcess) TableName() string { return "occurrence_processes" }

// OccurrenceProcessMessage is one stage's suggested message template for a
// process. Content uses the same `[Nome]`/`[Protocolo]`/... bracket syntax as
// the real, already-validated process map this feature is seeded from.
type OccurrenceProcessMessage struct {
	BaseModel
	OrganizationID uuid.UUID                     `gorm:"type:uuid;index;not null" json:"organization_id"`
	ProcessID      uuid.UUID                     `gorm:"type:uuid;index;not null;uniqueIndex:idx_occ_process_msg_stage,where:deleted_at IS NULL" json:"process_id"`
	Stage          OccurrenceProcessMessageStage `gorm:"size:20;not null;uniqueIndex:idx_occ_process_msg_stage,where:deleted_at IS NULL" json:"stage"`
	Content        string                        `gorm:"type:text;not null" json:"content"`
	IsActive       bool                          `gorm:"default:true" json:"is_active"`
}

func (OccurrenceProcessMessage) TableName() string { return "occurrence_process_messages" }
```

- [ ] **Step 4: Add `ProcessID`/`Process` to `Occurrence`, and the new event type const**

In `internal/models/occurrences.go`, add to the `OccurrenceEventType` const block:

```go
	OccurrenceEventProcessMessageUsed OccurrenceEventType = "process_message_used"
```

Add right after the `WhatHappenedID` field on `Occurrence`:

```go
	// ProcessID links the case to the Processo/Motivo that determines its
	// guidance, required fields and SLA parameters. Nil means the case was
	// opened without a resolvable process (e.g. WhatHappenedID left blank, or
	// no OccurrenceProcess configured for that reason yet) — the form and SLA
	// then behave exactly as before this feature existed.
	ProcessID *uuid.UUID `gorm:"type:uuid;index" json:"process_id,omitempty"`
```

And to the `Relations` block (after `WhatHappened *OccurrenceWhatHappened`):

```go
	Process      *OccurrenceProcess      `gorm:"foreignKey:ProcessID" json:"process,omitempty"`
```

- [ ] **Step 5: Register both new models in `GetMigrationModels()`**

In `internal/database/postgres.go`, change the `// CRM de ocorrências` block to:

```go
		// CRM de ocorrências
		{"OccurrenceStage", &models.OccurrenceStage{}},
		{"OccurrenceCategory", &models.OccurrenceCategory{}},
		{"OccurrenceWhatHappened", &models.OccurrenceWhatHappened{}},
		{"OccurrenceSLAPolicy", &models.OccurrenceSLAPolicy{}},
		{"OccurrenceProcess", &models.OccurrenceProcess{}},
		{"Occurrence", &models.Occurrence{}},
		{"OccurrenceEvent", &models.OccurrenceEvent{}},
		{"OccurrenceCounter", &models.OccurrenceCounter{}},
		{"OccurrenceProcessMessage", &models.OccurrenceProcessMessage{}},
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/models/... -run TestOccurrenceProcess -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git checkout -b feature/sac-processo-motivo-pr1 development
git add internal/models/occurrence_process.go internal/models/occurrence_process_test.go internal/models/occurrences.go internal/database/postgres.go
git commit -m "feat(sac): add OccurrenceProcess/OccurrenceProcessMessage models

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: Permission keys + standalone backfill

**Files:**
- Modify: `internal/models/roles.go:90-94` (resource constant), `internal/models/roles.go:280-287` (DefaultPermissions entries)
- Modify: `internal/database/permissions_backfill.go` (new backfill function, appended near `BackfillWhatHappenedPermission`)
- Modify: `cmd/whatomate/main.go:189-191` (invoke the new backfill)
- Test: `internal/database/permissions_backfill_test.go` — **read this file first** for its exact test-bootstrap helper names before writing; mirror whichever existing `TestBackfillWhatHappenedPermission_*` test uses rather than guessing.

**Interfaces:**
- Consumes: `models.ResourceOccurrenceCategories` (existing), `database.BackfillWhatHappenedPermission`'s exact shape (existing, to mirror).
- Produces: `models.ResourceOccurrenceProcesses = "occurrences.processes"`, `database.BackfillOccurrenceProcessesPermission(db *gorm.DB, lo logf.Logger) error`.

- [ ] **Step 1: Add the resource constant**

In `internal/models/roles.go`, add to the resource-constant block:

```go
	ResourceOccurrenceProcesses     = "occurrences.processes"
```

- [ ] **Step 2: Add the three default permission entries**

Right after the existing `ResourceOccurrenceSLAPolicies` pair in `DefaultPermissions()`:

```go
		{Resource: ResourceOccurrenceProcesses, Action: ActionRead, Description: "View occurrence processes and their message templates"},
		{Resource: ResourceOccurrenceProcesses, Action: ActionWrite, Description: "Create and edit occurrence processes and message templates"},
		{Resource: ResourceOccurrenceProcesses, Action: ActionDelete, Description: "Delete occurrence processes"},
```

- [ ] **Step 3: Write the failing backfill test**

Read `internal/database/permissions_backfill_test.go` first, then add a test that mirrors its existing `TestBackfillWhatHappenedPermission_*` test's exact bootstrap calls, asserting: a role granted `occurrences.categories:write` ends up with `occurrences.processes:write` after calling `database.BackfillOccurrenceProcessesPermission(db, lo)`.

- [ ] **Step 4: Run test to verify it fails**

Run: `go test ./internal/database/... -run TestBackfillOccurrenceProcessesPermission -v`
Expected: FAIL — `database.BackfillOccurrenceProcessesPermission` undefined.

- [ ] **Step 5: Implement the backfill**

Append to `internal/database/permissions_backfill.go`:

```go
// BackfillOccurrenceProcessesPermission concede occurrences.processes:{read,write,delete}
// aos papéis que já administram occurrences.categories — mesmo raciocínio de
// BackfillWhatHappenedPermission: telas de catálogo do SAC nascem juntas e são
// geridas pelas mesmas pessoas.
//
// Backfill PRÓPRIO (não entra em nenhum grupo existente) pela mesma razão
// documentada em BackfillWhatHappenedPermission: qualquer guarda de
// idempotência compartilhada marcaria como "já migrada" uma organização que
// só tinha as permissões antigas, e ela nunca receberia esta nova. Puramente
// aditivo: nunca revoga nada.
func BackfillOccurrenceProcessesPermission(db *gorm.DB, lo logf.Logger) error {
	var seeded int64
	if err := db.Model(&models.Permission{}).
		Where("resource = ?", models.ResourceOccurrenceProcesses).
		Count(&seeded).Error; err != nil {
		return fmt.Errorf("failed to count the occurrence-processes permission: %w", err)
	}
	if seeded == 0 {
		lo.Warn("occurrences.processes permissions not seeded yet, did nothing")
		return nil
	}

	res := db.Exec(`
		INSERT INTO role_permissions (custom_role_id, permission_id)
		SELECT DISTINCT r.id, target.id
		FROM custom_roles r
		JOIN role_permissions rp ON rp.custom_role_id = r.id
		JOIN permissions src ON src.id = rp.permission_id
		JOIN permissions target ON target.resource = ? AND target.action = src.action
		WHERE r.deleted_at IS NULL
		  AND src.resource = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM role_permissions existing
		    WHERE existing.custom_role_id = r.id AND existing.permission_id = target.id
		  )
		ON CONFLICT DO NOTHING`,
		models.ResourceOccurrenceProcesses, models.ResourceOccurrenceCategories,
	)
	if res.Error != nil {
		return fmt.Errorf("failed to grant the occurrence-processes permission: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		lo.Info("occurrence-processes permission backfill: nothing pending")
		return nil
	}
	lo.Info("occurrence-processes permission backfill complete", "links_granted", res.RowsAffected)
	return nil
}
```

- [ ] **Step 6: Wire it into boot, right after `BackfillWhatHappenedPermission`**

In `cmd/whatomate/main.go`:

```go
		// Same window: occurrences.processes is a new resource added after the
		// what-happened backfill above, so it needs its own guard rather than
		// piggybacking on that one's already-migrated check.
		if err := database.BackfillOccurrenceProcessesPermission(db, lo); err != nil {
			lo.Fatal("Occurrence processes permission backfill failed", "error", err)
		}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/database/... -run TestBackfillOccurrenceProcessesPermission -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/models/roles.go internal/database/permissions_backfill.go internal/database/permissions_backfill_test.go cmd/whatomate/main.go
git commit -m "feat(sac): add occurrences.processes permission + backfill

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3: Seed the five real processes from the validated HTML

**Files:**
- Create: `internal/handlers/occurrence_processes.go` (seed + find-or-create helpers only in this task)
- Test: `internal/handlers/occurrence_processes_test.go`

**Interfaces:**
- Consumes: `models.OccurrenceCategory`, `models.OccurrenceWhatHappened`, `a.ensureDefaultCategories`/`a.ensureDefaultWhatHappened` (existing, unexported methods on `*App`).
- Produces: `a.ensureDefaultOccurrenceProcesses(orgID uuid.UUID) error`, `a.findOrCreateWhatHappened(orgID uuid.UUID, name string) (*models.OccurrenceWhatHappened, error)`, `a.findOrCreateCategory(orgID uuid.UUID, name string) (*models.OccurrenceCategory, error)`.

- [ ] **Step 1: Write the failing seed test**

```go
// internal/handlers/occurrence_processes_test.go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOccurrenceProcesses_SeedsFiveRealProcessesOnFirstRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var processes []models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).
		Order("position ASC").Find(&processes).Error)
	require.Len(t, processes, 5)

	names := make([]string, len(processes))
	for i, p := range processes {
		names[i] = p.Name
	}
	assert.Equal(t, []string{
		"Devolução após 24h",
		"Material já separado ou em romaneio",
		"Divergência no ato do recebimento",
		"Avaria — comunicação e abertura",
		"Desistência sem avaria",
	}, names)

	// The avaria process must carry its real restrictions/guidance text, not a
	// placeholder — proof the transcription from the validated HTML landed.
	var avaria models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").
		First(&avaria).Error)
	assert.Contains(t, avaria.Restrictions, "Vamos trocar seu produto.")
	assert.NotNil(t, avaria.ResponseMinutes)
	require.NotNil(t, avaria.WhatHappenedID)
	require.NotNil(t, avaria.CategoryID)

	var whatHappened models.OccurrenceWhatHappened
	require.NoError(t, app.DB.First(&whatHappened, "id = ?", *avaria.WhatHappenedID).Error)
	assert.Equal(t, "Produto com Avaria", whatHappened.Name, "must reuse the existing seeded reason, not create a duplicate")
}

func TestOccurrenceProcesses_SeedIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var count int64
	app.DB.Model(&models.OccurrenceProcess{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.EqualValues(t, 5, count)
}

func TestFindOrCreateWhatHappened_ReusesExistingByName(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID)) // seeds the six defaults, including "Produto com Avaria"

	found, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	var count int64
	app.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ? AND name = ?", org.ID, "Produto com Avaria").Count(&count)
	assert.EqualValues(t, 1, count, "must not create a duplicate row for a name that already exists")
	assert.Equal(t, "Produto com Avaria", found.Name)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestOccurrenceProcesses -v`
Expected: FAIL — `app.EnsureDefaultOccurrenceProcessesForTest` undefined.

- [ ] **Step 3: Implement the seed, transcribed from the validated HTML's nodes 1.2/1.3/2.1/2.2/2.3**

```go
// internal/handlers/occurrence_processes.go
package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm/clause"
)

func intPtr(v int) *int { return &v }

// occurrenceProcessSeed is the shape used only while seeding — plain names
// for category/what-happened instead of IDs, resolved via
// findOrCreateCategory/findOrCreateWhatHappened at seed time.
type occurrenceProcessSeed struct {
	name, category, whatHappened, guidance, restrictions string
	evidence, required                                   []string
	responseMinutes, resolutionMinutes                   *int
	registrationMessage, documentsMessage                string
}

// defaultOccurrenceProcesses are the five real processes that open a SAC
// protocol, transcribed from the product owner's validated process map
// (nodes 1.2, 1.3, 2.1, 2.2, 2.3 of the HTML's `sacMsg`-tagged tree — see
// this plan's header for the source file). Guidance/Restrictions/messages
// are the real `conduta`/`pr`/`dont`/`sacMsg`/`msg` text, not invented.
//
// responseMinutes/resolutionMinutes are calendar minutes — the same
// semantics OccurrenceSLAPolicy's own four rows already use, not a
// business-hours calendar. A comment quoting "5 dias úteis" names the
// source text, not a literal business-day conversion claim.
//
// Category/WhatHappened names: two (Quantidade Divergente, Produto com
// Avaria) reuse the six existing seeded defaults exactly. The other three
// what-happened names and all five category assignments are THIS PLAN'S
// interpretation — the HTML groups nodes by delivery stage ("Pedido
// efetuado"/"Material recebido"), not by the Category/WhatHappened
// vocabulary this codebase already uses. This mapping is a business
// decision pending product-owner validation (see this file's seed-time
// warning log and this plan's final report task), not a settled fact.
var defaultOccurrenceProcesses = []occurrenceProcessSeed{
	{
		name:         "Devolução após 24h",
		category:     "Devolução com Estorno",
		whatHappened: "Devolução Após 24h", // new — no existing default fits
		guidance: "Registrar a solicitação e encaminhar para validação, sem prometer data de crédito.\n" +
			"Abrir a solicitação de devolução no sistema.\n" +
			"Encaminhar à supervisora geral de contas a receber para validação.\n" +
			"Emitir o recibo de crédito, ou lançar como devolução a ser tratada no financeiro.\n" +
			"Liberar o material de volta ao estoque.",
		restrictions:    "Cancelamento é direito seu.\nVocê pode cancelar quando quiser.\nA gente troca sem problema.",
		evidence:        []string{"Nota fiscal", "Documento do titular", "Comprovante de pagamento"},
		required:        []string{"invoice_number"},
		responseMinutes: intPtr(72 * 60), // "72h" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• documento do titular;\n• confirmação de que o material não foi retirado;\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 1 dia útil com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• devolução com emissão de recibo de crédito, válido por 90 dias;\n• devolução com estorno, conforme a forma de pagamento;\n• manutenção da compra, caso a solicitação esteja fora das condições;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
	{
		name:         "Material já separado ou em romaneio",
		category:     "Reagendamento de Entrega",
		whatHappened: "Alteração de Pedido em Separação", // new
		guidance: "Conferir o material, reprogramar e confirmar a nova data por escrito.\n" +
			"Conferir e devolver o material separado ao estoque.\n" +
			"Retirar o pedido do romaneio da data original e reprogramar.\n" +
			"Recalcular o frete pela nova quantidade e peso.\n" +
			"Emitir a NF de ajuste.\n" +
			"Confirmar a nova data com o cliente por escrito.",
		restrictions:    "Já está separado, não dá para mudar.\nA entrega continua na mesma data.",
		evidence:        []string{"Nota fiscal", "Documento do titular", "Conferência do material separado", "Romaneio da data agendada"},
		required:        []string{"invoice_number", "product_description"},
		responseMinutes: intPtr(8 * 60), // "até 1 dia útil" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• documento do titular;\n• indicação do produto desejado;\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 1 dia útil com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• alteração do pedido com emissão de NF de ajuste;\n• alteração com cobrança ou estorno da diferença de frete;\n• manutenção do pedido original, se o produto desejado não tiver disponibilidade;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
	{
		name:         "Divergência no ato do recebimento",
		category:     "Troca de Produto",
		whatHappened: "Quantidade Divergente", // existing default — exact match
		guidance: "Acionar operações no mesmo atendimento e retornar em 48 horas úteis.\n" +
			"Acionar imediatamente a assistente de operações para a apuração.\n" +
			"Conferir a NF contra o romaneio e contra o registro fotográfico da entrega.\n" +
			"Quantidade a menor: incluir no registro da entrega para averiguação.\n" +
			"Produto ou lote divergente: retornar todo o material e replanejar a entrega.",
		restrictions:    "A loja errou.\nO motorista errou.\nVocê que escolheu errado.",
		evidence:        []string{"Nota fiscal", "Foto do produto recebido", "Foto da etiqueta da caixa", "Foto de todos os volumes"},
		required:        []string{"invoice_number", "product_description"},
		responseMinutes: intPtr(48 * 60), // "48h úteis" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• fotos do produto recebido;\n• foto da etiqueta da caixa;\n• fotos de todos os volumes entregues;\n\n" +
			"Pedimos que preserve o material e as embalagens até a conclusão da análise.\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 48 horas úteis com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• reenvio do material correto, por nossa conta, quando a divergência for da entrega;\n• complementação da quantidade faltante;\n• abertura de troca, caso o cliente opte por outro produto;\n• manutenção da entrega, quando o material conferir com a nota fiscal;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
	{
		name:         "Avaria — comunicação e abertura",
		category:     "Troca de Produto",
		whatHappened: "Produto com Avaria", // existing default — exact match
		guidance: "Registrar sem classificar a causa e informar o número do ticket ao cliente.\n" +
			"Aplicar apenas a triagem de exclusão — o atendente NÃO classifica a causa.\n" +
			"Abrir o ticket e informar o número ao cliente.\n" +
			"Solicitar as fotos e a descrição.\n" +
			"Puxar as imagens da entrega registradas no app do motorista.",
		restrictions:    "Vamos trocar seu produto.\nRealmente veio com defeito.\nIsso foi no transporte.\nA fábrica que resolve.",
		evidence:        []string{"Fotos do produto", "Fotos da embalagem", "Nota fiscal", "Quantidade afetada em m² e caixas"},
		required:        []string{"invoice_number", "product_description"},
		responseMinutes: intPtr(5 * 24 * 60), // "retorno ao cliente: 5 dias úteis" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• fotos do produto;\n• fotos da embalagem;\n• quantidade de caixas afetadas;\n\n" +
			"Importante: pedimos que NÃO utilize o material e que NÃO retire as peças da embalagem até a conclusão da análise.\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 5 dias úteis com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• substituição do material avariado;\n• abatimento proporcional do preço;\n• restituição do valor pago;\n• conclusão de que não há avaria coberta, com apresentação das evidências;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
		documentsMessage: "Olá, Sr.(a) [Nome].\n\nLamentamos o ocorrido e agradecemos por nos informar.\n\n" +
			"Para darmos andamento, pedimos que nos encaminhe:\n• Nota Fiscal da compra;\n• fotos do produto;\n• fotos da embalagem;\n• a quantidade de caixas afetadas.\n\n" +
			"Importante: pedimos que NÃO utilize o material e que NÃO retire as peças da embalagem até a conclusão da análise. Isso é necessário para preservar a avaliação e as alternativas do seu caso.\n\n" +
			"Seu ticket será aberto e nossa equipe retornará em até 5 dias úteis com o encaminhamento.",
	},
	{
		name:         "Desistência sem avaria",
		category:     "Devolução com Estorno",
		whatHappened: "Desistência do Pedido", // new
		guidance: "Analisar as imagens antes de autorizar e explicar o prazo de devolução.\n" +
			"Aplicar a triagem de exclusão antes de abrir o protocolo.\n" +
			"Abrir o protocolo pelo WhatsApp da empresa.\n" +
			"Analisar as condições de recebimento cruzando as imagens do cliente com as fotos do app do motorista.\n" +
			"Autorizar ou recusar a devolução com base na análise de imagens.",
		restrictions:      "Você tem 7 dias de arrependimento.\nTrocamos qualquer produto.\nPode trazer que a gente resolve.",
		evidence:          []string{"Nota fiscal", "Fotos das caixas fechadas", "Registro do canal da venda"},
		required:          []string{"invoice_number", "sale_channel"},
		responseMinutes:   intPtr(2 * 24 * 60), // "análise em 2 dias úteis" in the source text — calendar minutes
		resolutionMinutes: intPtr(5 * 24 * 60), // "devolução em 5 dias" in the source text — calendar minutes
		registrationMessage: "Olá, Sr.(a) [Nome]. Aqui é [Atendente], do Serviço de Atendimento ao Cliente do Atacadão dos Pisos.\n\n" +
			"Recebemos seu contato referente à compra da nota fiscal [NF] e registramos o protocolo [Protocolo] para acompanhar sua solicitação.\n\n" +
			"Para darmos andamento, pedimos o envio de:\n• nota fiscal da compra;\n• fotos das caixas fechadas;\n• foto da etiqueta de lote;\n\n" +
			"A análise compara as imagens enviadas com o registro fotográfico feito na entrega.\n\n" +
			"A partir do recebimento faremos a análise e retornaremos em até 2 dias úteis com o encaminhamento do seu caso.\n\n" +
			"As soluções possíveis para situações como a sua são:\n• autorização da devolução, com entrega do material no ponto de saída em até 5 dias;\n• emissão de recibo de crédito após a conferência do material;\n• recusa da devolução, quando as condições do produto não permitirem;\n\n" +
			"A definição depende da análise, por isso não conseguimos antecipar o resultado neste momento. Nossa equipe acompanhará todas as etapas e manterá você informado.",
	},
}

// findOrCreateWhatHappened looks up a reason by exact name within the org,
// creating it at the end of the list when absent.
func (a *App) findOrCreateWhatHappened(orgID uuid.UUID, name string) (*models.OccurrenceWhatHappened, error) {
	if err := a.ensureDefaultWhatHappened(orgID); err != nil {
		return nil, err
	}
	var existing models.OccurrenceWhatHappened
	err := a.DB.Where("organization_id = ? AND name = ?", orgID, name).First(&existing).Error
	if err == nil {
		return &existing, nil
	}

	var maxPosition int
	a.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ?", orgID).
		Select("COALESCE(MAX(position), -1)").Scan(&maxPosition)

	row := models.OccurrenceWhatHappened{OrganizationID: orgID, Name: name, Position: maxPosition + 1, IsActive: true}
	if err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// findOrCreateCategory mirrors findOrCreateWhatHappened for OccurrenceCategory.
func (a *App) findOrCreateCategory(orgID uuid.UUID, name string) (*models.OccurrenceCategory, error) {
	if err := a.ensureDefaultCategories(orgID); err != nil {
		return nil, err
	}
	var existing models.OccurrenceCategory
	err := a.DB.Where("organization_id = ? AND name = ?", orgID, name).First(&existing).Error
	if err == nil {
		return &existing, nil
	}

	var maxPosition int
	a.DB.Model(&models.OccurrenceCategory{}).Where("organization_id = ?", orgID).
		Select("COALESCE(MAX(position), -1)").Scan(&maxPosition)

	row := models.OccurrenceCategory{OrganizationID: orgID, Name: name, Position: maxPosition + 1, IsActive: true}
	if err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ensureDefaultOccurrenceProcesses seeds the five real SAC-opening processes
// on first read, same count-then-insert idempotency as
// ensureDefaultWhatHappened. Logs a warning naming the Category/WhatHappened
// mapping as a pending business decision every time it actually seeds, so it
// stays visible in server logs rather than only in a code comment nobody
// reads before go-live.
func (a *App) ensureDefaultOccurrenceProcesses(orgID uuid.UUID) error {
	var count int64
	if err := a.DB.Model(&models.OccurrenceProcess{}).
		Where("organization_id = ?", orgID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	a.Log.Warn("Seeding default occurrence processes — Category/WhatHappened mapping and SLA minutes are a first-cut interpretation of the validated process map, pending product-owner validation",
		"organization_id", orgID)

	for i, seed := range defaultOccurrenceProcesses {
		category, err := a.findOrCreateCategory(orgID, seed.category)
		if err != nil {
			return err
		}
		whatHappened, err := a.findOrCreateWhatHappened(orgID, seed.whatHappened)
		if err != nil {
			return err
		}

		required := make(models.JSONBArray, len(seed.required))
		for j, r := range seed.required {
			required[j] = r
		}
		evidence := make(models.JSONBArray, len(seed.evidence))
		for j, e := range seed.evidence {
			evidence[j] = e
		}

		process := models.OccurrenceProcess{
			OrganizationID:    orgID,
			Name:              seed.name,
			CategoryID:        &category.ID,
			WhatHappenedID:    &whatHappened.ID,
			Guidance:          seed.guidance,
			Restrictions:      seed.restrictions,
			EvidenceChecklist: evidence,
			RequiredFields:    required,
			ResponseMinutes:   seed.responseMinutes,
			ResolutionMinutes: seed.resolutionMinutes,
			IsActive:          true,
			Position:          i,
		}
		if err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&process).Error; err != nil {
			return err
		}

		messages := []models.OccurrenceProcessMessage{
			{OrganizationID: orgID, ProcessID: process.ID, Stage: models.OccurrenceProcessMessageRegistration, Content: seed.registrationMessage, IsActive: true},
		}
		if seed.documentsMessage != "" {
			messages = append(messages, models.OccurrenceProcessMessage{
				OrganizationID: orgID, ProcessID: process.ID, Stage: models.OccurrenceProcessMessageDocuments, Content: seed.documentsMessage, IsActive: true,
			})
		}
		if err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&messages).Error; err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Add test-only export shims**

Append to `internal/handlers/occurrence_export_test.go`:

```go
func (a *App) EnsureDefaultOccurrenceProcessesForTest(orgID uuid.UUID) error {
	return a.ensureDefaultOccurrenceProcesses(orgID)
}

func (a *App) EnsureDefaultWhatHappenedForTest(orgID uuid.UUID) error {
	return a.ensureDefaultWhatHappened(orgID)
}

func (a *App) FindOrCreateWhatHappenedForTest(orgID uuid.UUID, name string) (*models.OccurrenceWhatHappened, error) {
	return a.findOrCreateWhatHappened(orgID, name)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run 'TestOccurrenceProcesses|TestFindOrCreateWhatHappened' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/occurrence_processes.go internal/handlers/occurrence_processes_test.go internal/handlers/occurrence_export_test.go
git commit -m "feat(sac): seed the five real SAC processes from the validated process map

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 4: Process CRUD handlers, one-active-process-per-reason validation, audit logging

**Files:**
- Modify: `internal/handlers/occurrence_processes.go` (append CRUD)
- Modify: `internal/handlers/occurrence_processes_test.go` (append CRUD tests)

**Interfaces:**
- Consumes: `audit.LogAudit`, `audit.GetUserName` (existing), `a.requireAuth`, `a.decodeRequest`, `parsePathUUID`, `findByIDAndOrg[T]` (existing helpers).
- Produces: `a.ListOccurrenceProcesses`, `a.CreateOccurrenceProcess`, `a.UpdateOccurrenceProcess`, `a.DeleteOccurrenceProcess`, `a.assertNoActiveProcessForReason(orgID uuid.UUID, whatHappenedID *uuid.UUID, excludeProcessID *uuid.UUID) error`.

- [ ] **Step 1: Write failing CRUD + uniqueness tests**

```go
func TestCreateOccurrenceProcess_RequiresWritePermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Novo Processo"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestCreateOccurrenceProcess_WritesAuditLog(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "Processo Manual", "is_active": true})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	require.Eventually(t, func() bool {
		var count int64
		app.DB.Model(&models.AuditLog{}).
			Where("organization_id = ? AND resource_type = ? AND action = ?",
				org.ID, models.ResourceOccurrenceProcesses, models.AuditActionCreated).
			Count(&count)
		return count == 1
	}, time.Second, 10*time.Millisecond)
}

func TestCreateOccurrenceProcess_RejectsSecondActiveProcessForSameWhatHappened(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	reason, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	first := testutil.NewJSONRequest(t, map[string]any{"name": "Processo A", "what_happened_id": reason.ID.String(), "is_active": true})
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceProcess(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	second := testutil.NewJSONRequest(t, map[string]any{"name": "Processo B", "what_happened_id": reason.ID.String(), "is_active": true})
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceProcess(second))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(second),
		"only one ACTIVE process may exist per reason — ResolveOccurrenceProcess's .First() depends on this")
}

func TestCreateOccurrenceProcess_AllowsSecondInactiveProcessForSameWhatHappened(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	reason, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	first := testutil.NewJSONRequest(t, map[string]any{"name": "Processo A", "what_happened_id": reason.ID.String(), "is_active": true})
	testutil.SetAuthContext(first, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceProcess(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	second := testutil.NewJSONRequest(t, map[string]any{"name": "Processo B (rascunho)", "what_happened_id": reason.ID.String(), "is_active": false})
	testutil.SetAuthContext(second, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrenceProcess(second))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(second), "an inactive draft must not collide with the active one")
}

func TestDeleteOccurrenceProcess_RefusesWhenOccurrenceUsesIt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	process := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Em Uso", IsActive: true}
	require.NoError(t, app.DB.Create(&process).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: &process.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewDELETERequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", process.ID.String())
	require.NoError(t, app.DeleteOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}
```

(Add `"time"` to the test file's imports. If `testutil.NewDELETERequest`/`testutil.CreateTestContact` are named differently, check `occurrence_categories_test.go`/`contacts_test.go` for the real names — do not invent a helper.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run 'TestCreateOccurrenceProcess|TestDeleteOccurrenceProcess' -v`
Expected: FAIL — undefined `app.CreateOccurrenceProcess` etc.

- [ ] **Step 3: Implement the CRUD handlers with the uniqueness guard**

Append to `internal/handlers/occurrence_processes.go`:

```go
// OccurrenceProcessRequest is the create/update body for a process.
type OccurrenceProcessRequest struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	CategoryID        *string  `json:"category_id"`
	WhatHappenedID    *string  `json:"what_happened_id"`
	Guidance          string   `json:"guidance"`
	Restrictions      string   `json:"restrictions"`
	EvidenceChecklist []string `json:"evidence_checklist"`
	RequiredFields    []string `json:"required_fields"`
	ResponseMinutes   *int     `json:"response_minutes"`
	ResolutionMinutes *int     `json:"resolution_minutes"`
	DepartmentID      *string  `json:"department_id"`
	Position          int      `json:"position"`
	IsActive          *bool    `json:"is_active"`
}

func toJSONBArray(values []string) models.JSONBArray {
	arr := make(models.JSONBArray, len(values))
	for i, v := range values {
		arr[i] = v
	}
	return arr
}

// parseOptionalOrgUUID validates an optional foreign-key id against a table
// that embeds organization_id, refusing an id that doesn't belong to orgID.
func (a *App) parseOptionalOrgUUID(raw *string, orgID uuid.UUID, table string) (*uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, err
	}
	var count int64
	if err := a.DB.Table(table).Where("id = ? AND organization_id = ?", id, orgID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, errors.New(table + " not found in this organization")
	}
	return &id, nil
}

// errActiveProcessExistsForReason is returned by assertNoActiveProcessForReason.
var errActiveProcessExistsForReason = errors.New("an active process already exists for this reason")

// assertNoActiveProcessForReason enforces "exactly one active process per
// (organization, WhatHappenedID)" at the application layer, ahead of the
// partial unique index that backstops it against races. ResolveOccurrenceProcess
// resolves a reason to a process with a plain .First() and needs this
// invariant to actually hold, not just be documented.
func (a *App) assertNoActiveProcessForReason(orgID uuid.UUID, whatHappenedID *uuid.UUID, excludeProcessID *uuid.UUID) error {
	if whatHappenedID == nil {
		return nil
	}
	query := a.DB.Model(&models.OccurrenceProcess{}).
		Where("organization_id = ? AND what_happened_id = ? AND is_active = true", orgID, *whatHappenedID)
	if excludeProcessID != nil {
		query = query.Where("id <> ?", *excludeProcessID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errActiveProcessExistsForReason
	}
	return nil
}

// ListOccurrenceProcesses returns the org's processes, seeding the five real
// defaults on first read.
func (a *App) ListOccurrenceProcesses(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionRead)
	if err != nil {
		return nil
	}
	if err := a.ensureDefaultOccurrenceProcesses(orgID); err != nil {
		a.Log.Error("Failed to seed default occurrence processes", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load processes", nil, "")
	}

	var processes []models.OccurrenceProcess
	if err := a.DB.Where("organization_id = ?", orgID).
		Preload("Category").Preload("WhatHappened").Preload("Department").
		Order("position ASC").Find(&processes).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load processes", nil, "")
	}
	return r.SendEnvelope(map[string]any{"processes": processes})
}

// CreateOccurrenceProcess adds a process/motivo.
func (a *App) CreateOccurrenceProcess(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionWrite)
	if err != nil {
		return nil
	}

	var req OccurrenceProcessRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	categoryID, err := a.parseOptionalOrgUUID(req.CategoryID, orgID, "occurrence_categories")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid category_id", nil, "")
	}
	whatHappenedID, err := a.parseOptionalOrgUUID(req.WhatHappenedID, orgID, "occurrence_what_happened")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid what_happened_id", nil, "")
	}
	departmentID, err := a.parseOptionalOrgUUID(req.DepartmentID, orgID, "departments")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	if isActive {
		if err := a.assertNoActiveProcessForReason(orgID, whatHappenedID, nil); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, err.Error(), nil, "")
		}
	}

	process := models.OccurrenceProcess{
		OrganizationID: orgID, Name: req.Name, Description: req.Description,
		CategoryID: categoryID, WhatHappenedID: whatHappenedID, DepartmentID: departmentID,
		Guidance: req.Guidance, Restrictions: req.Restrictions,
		EvidenceChecklist: toJSONBArray(req.EvidenceChecklist),
		RequiredFields:    toJSONBArray(req.RequiredFields),
		ResponseMinutes:   req.ResponseMinutes, ResolutionMinutes: req.ResolutionMinutes,
		Position: req.Position, IsActive: isActive,
	}
	if err := a.DB.Create(&process).Error; err != nil {
		a.Log.Error("Failed to create occurrence process", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create process", nil, "")
	}

	userName := audit.GetUserName(a.DB, userID)
	audit.LogAudit(a.DB, orgID, userID, userName, models.ResourceOccurrenceProcesses, process.ID,
		models.AuditActionCreated, nil, process)

	return r.SendEnvelope(process)
}

// UpdateOccurrenceProcess edits a process/motivo.
func (a *App) UpdateOccurrenceProcess(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionWrite)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	process, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, processID, orgID, "Process")
	if err != nil {
		return nil
	}
	before := *process

	var req OccurrenceProcessRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Name == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "name is required", nil, "")
	}

	categoryID, err := a.parseOptionalOrgUUID(req.CategoryID, orgID, "occurrence_categories")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid category_id", nil, "")
	}
	whatHappenedID, err := a.parseOptionalOrgUUID(req.WhatHappenedID, orgID, "occurrence_what_happened")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid what_happened_id", nil, "")
	}
	departmentID, err := a.parseOptionalOrgUUID(req.DepartmentID, orgID, "departments")
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid department_id", nil, "")
	}

	isActive := process.IsActive
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	if isActive {
		if err := a.assertNoActiveProcessForReason(orgID, whatHappenedID, &processID); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusConflict, err.Error(), nil, "")
		}
	}

	updates := map[string]any{
		"name": req.Name, "description": req.Description,
		"category_id": categoryID, "what_happened_id": whatHappenedID, "department_id": departmentID,
		"guidance": req.Guidance, "restrictions": req.Restrictions,
		"evidence_checklist": toJSONBArray(req.EvidenceChecklist),
		"required_fields":    toJSONBArray(req.RequiredFields),
		"response_minutes":   req.ResponseMinutes, "resolution_minutes": req.ResolutionMinutes,
		"position": req.Position, "is_active": isActive,
	}

	if err := a.DB.Model(process).Updates(updates).Error; err != nil {
		a.Log.Error("Failed to update occurrence process", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update process", nil, "")
	}

	userName := audit.GetUserName(a.DB, userID)
	audit.LogAudit(a.DB, orgID, userID, userName, models.ResourceOccurrenceProcesses, process.ID,
		models.AuditActionUpdated, before, *process)

	return r.SendEnvelope(process)
}

// DeleteOccurrenceProcess removes a process, refusing when an occurrence
// still references it — same guard shape as DeleteOccurrenceCategory.
func (a *App) DeleteOccurrenceProcess(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionDelete)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	process, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, processID, orgID, "Process")
	if err != nil {
		return nil
	}

	var occCount int64
	a.DB.Model(&models.Occurrence{}).Where("process_id = ?", processID).Count(&occCount)
	if occCount > 0 {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "Process is in use by existing occurrences", nil, "")
	}

	if err := a.DB.Delete(process).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to delete process", nil, "")
	}

	userName := audit.GetUserName(a.DB, userID)
	audit.LogAudit(a.DB, orgID, userID, userName, models.ResourceOccurrenceProcesses, processID,
		models.AuditActionDeleted, process, nil)

	return r.SendEnvelope(map[string]any{"deleted": true})
}
```

Add `"errors"` and `"github.com/shridarpatil/whatomate/internal/audit"` to this file's imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run 'TestCreateOccurrenceProcess|TestDeleteOccurrenceProcess' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/occurrence_processes.go internal/handlers/occurrence_processes_test.go
git commit -m "feat(sac): add OccurrenceProcess CRUD, one-active-per-reason guard, audit logging

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 5: Resolve endpoint + message CRUD + variable resolution + preview + usage logging

**Files:**
- Create: `internal/handlers/occurrence_process_messages.go`
- Test: `internal/handlers/occurrence_process_messages_test.go`

**Interfaces:**
- Consumes: `models.Occurrence`, `models.Contact.ProfileName`, `a.canViewConversation(userID, orgID uuid.UUID, contact *models.Contact) bool` (existing, `internal/handlers/conversation_visibility.go:89`).
- Produces: `a.ResolveOccurrenceProcess`, `a.ListOccurrenceProcessMessages`, `a.UpsertOccurrenceProcessMessage`, `a.PreviewOccurrenceProcessMessage`, `a.LogOccurrenceProcessMessageUse`, `resolveProcessMessageVariables(content string, occ *models.Occurrence, agentName string) string`, `fallbackProcessMessage(stage string, occ *models.Occurrence) (content string, hasFallback bool)`.

**Fix applied from review:** `PreviewOccurrenceProcessMessage` must NOT call `loadAuthorizedOccurrence` — that helper reads the occurrence id from the `{id}` path param (`parsePathUUID(r, "id", "occurrence")`, confirmed at `internal/handlers/occurrences.go:619`), but on this endpoint's route `{id}` is the **process** id and the occurrence id arrives as the `occurrence_id` query param. Calling it here would look up the wrong row entirely. The fix below loads the occurrence explicitly by the query param, re-applies the same `canViewConversation` visibility check `loadAuthorizedOccurrence` would have applied, and additionally checks the occurrence is actually linked to the process being previewed (a user must not be able to preview process B's message shape against an occurrence linked to process A).

**Fix applied from review:** fallback is now explicit and stage-aware, not a silent empty string. `registration` falls back to today's exact hardcoded protocol text (so a process-less occurrence's suggested message is byte-identical to current production behaviour). Every other stage with no template returns `has_template: false` and empty content — the caller (PR3's frontend) must show "no message registered for this stage" and let the agent write one manually, never send a generated customer-facing message nobody wrote.

- [ ] **Step 1: Write failing tests**

```go
// internal/handlers/occurrence_process_messages_test.go
package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestResolveOccurrenceProcess_FindsProcessByWhatHappened(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var avaria models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Produto com Avaria").First(&avaria).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "what_happened_id", avaria.ID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(t, req)), "Avaria — comunicação e abertura")
}

func TestResolveOccurrenceProcess_NoMatchReturnsNullProcess(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID))

	var unrelated models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Duvida sobre o Produto").First(&unrelated).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "what_happened_id", unrelated.ID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "no process for this reason is not an error — the form falls back to the generic path")
}

func TestResolveProcessMessageVariables_SubstitutesRealVariables(t *testing.T) {
	occ := &models.Occurrence{
		ProtocolNumber: "202600123",
		InvoiceNumber:  "NF-9988",
		Contact:        &models.Contact{ProfileName: "João"},
	}
	out := handlers.ResolveProcessMessageVariablesForTest("Olá [Nome], protocolo [Protocolo], NF [NF], atendente [Atendente].", occ, "Maria")
	assert.Equal(t, "Olá João, protocolo 202600123, NF NF-9988, atendente Maria.", out)
}

func TestPreviewOccurrenceProcessMessage_UsesTheOccurrenceFromTheQueryParam_NotThePathParam(t *testing.T) {
	// This is the regression test for the loadAuthorizedOccurrence bug caught
	// in review: the route's {id} is the PROCESS id, never an occurrence id.
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var process models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").First(&process).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: &process.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", process.ID.String()) // the process id, deliberately NOT occ.ID
	testutil.SetPathParam(req, "stage", "registration")
	testutil.SetQueryParam(req, "occurrence_id", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req),
		"must resolve the occurrence from occurrence_id, never from the {id} path param")

	body := string(testutil.GetResponseBody(t, req))
	assert.NotContains(t, body, "[Nome]", "the real seeded template must have been found and its variables substituted")
}

func TestPreviewOccurrenceProcessMessage_RejectsOccurrenceLinkedToADifferentProcess(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var avaria, divergencia models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").First(&avaria).Error)
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Divergência no ato do recebimento").First(&divergencia).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: &divergencia.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", avaria.ID.String()) // wrong process for this occurrence
	testutil.SetPathParam(req, "stage", "registration")
	testutil.SetQueryParam(req, "occurrence_id", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestPreviewOccurrenceProcessMessage_NoTemplateReturnsHasTemplateFalse_NotAGeneratedMessage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var process models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").First(&process).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: &process.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	// "closing" has no seeded template for this process.
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", process.ID.String())
	testutil.SetPathParam(req, "stage", "closing")
	testutil.SetQueryParam(req, "occurrence_id", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(t, req))
	assert.Contains(t, body, `"has_template":false`)
	assert.Contains(t, body, `"content":""`, "must not silently generate a customer-facing message nobody wrote")
}

func TestPreviewOccurrenceProcessMessage_RegistrationFallsBackToLegacyProtocolText(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	process := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Sem template", IsActive: true}
	require.NoError(t, app.DB.Create(&process).Error)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: &process.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", process.ID.String())
	testutil.SetPathParam(req, "stage", "registration")
	testutil.SetQueryParam(req, "occurrence_id", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	body := string(testutil.GetResponseBody(t, req))
	assert.Contains(t, body, occ.ProtocolNumber, "registration must always fall back to the legacy protocol text, matching current production behaviour")
}

func TestLogOccurrenceProcessMessageUse_CreatesTimelineEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID, OpenedByUserID: user.ID}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "registration"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.LogOccurrenceProcessMessageUse(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var count int64
	app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventProcessMessageUsed).Count(&count)
	assert.EqualValues(t, 1, count, "must reuse the existing occurrence_events timeline, not a new table")
}
```

(`testutil.SetQueryParam`/`testutil.GetResponseBody` — check `test/testutil` for the actual names before writing; other read-only handlers that read query filters, e.g. `ListOccurrences`, show the real helper name.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/handlers/... -run 'TestResolveOccurrenceProcess|TestResolveProcessMessageVariables|TestPreviewOccurrenceProcessMessage|TestLogOccurrenceProcessMessageUse' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement**

```go
// internal/handlers/occurrence_process_messages.go
package handlers

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// ResolveOccurrenceProcess returns the OccurrenceProcess configured for a
// given WhatHappened reason, or a null process when none is configured yet —
// that is the fallback path (§15 of the spec), not an error: the form must
// keep working exactly as before this feature existed. Relies on the
// one-active-process-per-reason invariant enforced in
// CreateOccurrenceProcess/UpdateOccurrenceProcess (Task 4) — .First() here is
// deterministic only because that invariant is real.
func (a *App) ResolveOccurrenceProcess(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}

	raw := r.RequestCtx.QueryArgs().Peek("what_happened_id")
	if len(raw) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "what_happened_id is required", nil, "")
	}
	id, err := uuid.Parse(string(raw))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid what_happened_id", nil, "")
	}

	var process models.OccurrenceProcess
	err = a.DB.Where("organization_id = ? AND what_happened_id = ? AND is_active = true", orgID, id).
		Preload("Category").Preload("WhatHappened").Preload("Department").
		First(&process).Error
	if err != nil {
		return r.SendEnvelope(map[string]any{"process": nil})
	}
	return r.SendEnvelope(map[string]any{"process": process})
}

// ListOccurrenceProcessMessages returns every stage's template for a process.
func (a *App) ListOccurrenceProcessMessages(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionRead)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	if _, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, processID, orgID, "Process"); err != nil {
		return nil
	}

	var messages []models.OccurrenceProcessMessage
	if err := a.DB.Where("organization_id = ? AND process_id = ?", orgID, processID).Find(&messages).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load messages", nil, "")
	}
	return r.SendEnvelope(map[string]any{"messages": messages})
}

// UpsertOccurrenceProcessMessageRequest is the body for PUT .../messages/{stage}.
type UpsertOccurrenceProcessMessageRequest struct {
	Content  string `json:"content"`
	IsActive *bool  `json:"is_active"`
}

var validProcessMessageStages = map[string]models.OccurrenceProcessMessageStage{
	"registration": models.OccurrenceProcessMessageRegistration,
	"documents":    models.OccurrenceProcessMessageDocuments,
	"follow_up":    models.OccurrenceProcessMessageFollowUp,
	"forwarding":   models.OccurrenceProcessMessageForwarding,
	"closing":      models.OccurrenceProcessMessageClosing,
}

// UpsertOccurrenceProcessMessage creates or replaces one stage's template —
// same upsert-by-key shape as UpsertOccurrenceSLAPolicy.
func (a *App) UpsertOccurrenceProcessMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrenceProcesses, models.ActionWrite)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	if _, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, processID, orgID, "Process"); err != nil {
		return nil
	}

	stageRaw, _ := r.RequestCtx.UserValue("stage").(string)
	stage, ok := validProcessMessageStages[stageRaw]
	if !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}

	var req UpsertOccurrenceProcessMessageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.Content == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "content is required", nil, "")
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	var existing models.OccurrenceProcessMessage
	err = a.DB.Where("organization_id = ? AND process_id = ? AND stage = ?", orgID, processID, stage).First(&existing).Error
	var message models.OccurrenceProcessMessage
	if err == nil {
		before := existing
		if err := a.DB.Model(&existing).Updates(map[string]any{"content": req.Content, "is_active": isActive}).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update message", nil, "")
		}
		message = existing
		audit.LogAudit(a.DB, orgID, userID, audit.GetUserName(a.DB, userID),
			models.ResourceOccurrenceProcesses, processID, models.AuditActionUpdated, before, message)
	} else {
		message = models.OccurrenceProcessMessage{
			OrganizationID: orgID, ProcessID: processID, Stage: stage, Content: req.Content, IsActive: isActive,
		}
		if err := a.DB.Create(&message).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create message", nil, "")
		}
		audit.LogAudit(a.DB, orgID, userID, audit.GetUserName(a.DB, userID),
			models.ResourceOccurrenceProcesses, processID, models.AuditActionCreated, nil, message)
	}

	return r.SendEnvelope(message)
}

// resolveProcessMessageVariables substitutes the bracket variables the real,
// validated process map already uses in its own message text — a small
// standalone resolver, not a second templating engine. Nothing in the
// codebase resolves `[Bracket]`-style placeholders today; the closest
// existing thing, internal/templateutil, resolves WABA's `{{1}}`/`{{name}}`
// syntax for a different feature (approved WhatsApp message templates), and
// reusing its numbered-param model here would be a worse fit than this
// replacer.
func resolveProcessMessageVariables(content string, occ *models.Occurrence, agentName string) string {
	name := ""
	if occ.Contact != nil {
		name = occ.Contact.ProfileName
	}
	unit := ""
	if occ.Unit != nil {
		unit = occ.Unit.Name
	}
	deadline := ""
	if occ.SLA.ResponseDeadline != nil {
		days := int(time.Until(*occ.SLA.ResponseDeadline).Hours() / 24)
		if days > 0 {
			deadline = strconv.Itoa(days) + " dias"
		} else {
			hours := int(time.Until(*occ.SLA.ResponseDeadline).Hours())
			if hours < 1 {
				hours = 1
			}
			deadline = strconv.Itoa(hours) + " horas"
		}
	}

	replacer := strings.NewReplacer(
		"[Nome]", name,
		"[Atendente]", agentName,
		"[Protocolo]", occ.ProtocolNumber,
		"[NF]", occ.InvoiceNumber,
		"[Prazo]", deadline,
		"[Loja]", unit,
	)
	return replacer.Replace(content)
}

// fallbackProcessMessage is the explicit, stage-aware fallback the spec's
// §15 resilience requirement asks for. "registration" is the only stage that
// returns real content — the exact text SendOccurrenceProtocol already sends
// today, so a process-less occurrence's suggested message is byte-identical
// to current production behaviour. Every other stage returns ("", false):
// no generated customer-facing message is invented for a stage nobody wrote
// a template for — the caller must show "no message registered" and let the
// agent write one manually, not silently send a guess.
func fallbackProcessMessage(stage string, occ *models.Occurrence) (content string, hasFallback bool) {
	if stage == "registration" {
		return "Seu protocolo de atendimento é " + occ.ProtocolNumber + ". Guarde este número para consultas futuras.", true
	}
	return "", false
}

// PreviewOccurrenceProcessMessage renders one stage's message for one
// occurrence, substituting variables, and falls back explicitly when the
// process has no template for that stage.
//
// Deliberately does NOT call loadAuthorizedOccurrence: that helper reads the
// occurrence id from the {id} path param, but on this route {id} is the
// PROCESS id — the occurrence id is the occurrence_id query param instead.
// Calling it here would silently look up the wrong row (or 404 on a
// coincidental non-match). This loads the occurrence explicitly and
// reapplies the same canViewConversation visibility check
// loadAuthorizedOccurrence would have applied, plus a check that the
// occurrence is actually linked to the process being previewed.
func (a *App) PreviewOccurrenceProcessMessage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	processID, err := parsePathUUID(r, "id", "process")
	if err != nil {
		return nil
	}
	stageRaw, _ := r.RequestCtx.UserValue("stage").(string)
	if _, ok := validProcessMessageStages[stageRaw]; !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}

	occurrenceIDRaw := string(r.RequestCtx.QueryArgs().Peek("occurrence_id"))
	occurrenceID, err := uuid.Parse(occurrenceIDRaw)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid occurrence_id", nil, "")
	}

	var occ models.Occurrence
	if err := a.DB.Where("id = ? AND organization_id = ?", occurrenceID, orgID).
		Preload("Contact").Preload("Unit").First(&occ).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Occurrence not found", nil, "")
	}
	if !a.canViewConversation(userID, orgID, occ.Contact) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have access to this occurrence", nil, "")
	}
	if occ.ProcessID == nil || *occ.ProcessID != processID {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "This occurrence is not linked to the given process", nil, "")
	}

	var template models.OccurrenceProcessMessage
	dbErr := a.DB.Where("organization_id = ? AND process_id = ? AND stage = ? AND is_active = true",
		orgID, processID, stageRaw).First(&template).Error

	agentName := audit.GetUserName(a.DB, userID)
	var content string
	hasTemplate := dbErr == nil
	if hasTemplate {
		content = resolveProcessMessageVariables(template.Content, &occ, agentName)
	} else {
		content, hasTemplate = fallbackProcessMessage(stageRaw, &occ)
	}

	return r.SendEnvelope(map[string]any{"content": content, "has_template": hasTemplate})
}

// LogOccurrenceProcessMessageUseRequest is the body for logging that a
// suggested message was used.
type LogOccurrenceProcessMessageUseRequest struct {
	Stage       string `json:"stage"`
	ProcessName string `json:"process_name"`
}

// LogOccurrenceProcessMessageUse records, on the occurrence's EXISTING
// timeline, that a suggested message was used — §14 of the spec, reusing
// OccurrenceEvent rather than a second history table.
func (a *App) LogOccurrenceProcessMessageUse(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, err := a.loadAuthorizedOccurrence(r, orgID, userID, false)
	if err != nil {
		return nil
	}

	var req LogOccurrenceProcessMessageUseRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if _, ok := validProcessMessageStages[req.Stage]; !ok {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}

	content := "Mensagem de " + req.Stage + " utilizada."
	if req.ProcessName != "" {
		content = "Mensagem de " + req.Stage + " utilizada — processo: " + req.ProcessName + "."
	}

	if err := a.DB.Create(&models.OccurrenceEvent{
		OrganizationID: orgID,
		OccurrenceID:   occ.ID,
		Type:           models.OccurrenceEventProcessMessageUsed,
		Content:        content,
		CreatedByID:    &userID,
	}).Error; err != nil {
		a.Log.Error("Failed to log process message use", "error", err, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to log message use", nil, "")
	}

	return r.SendEnvelope(map[string]any{"logged": true})
}
```

Note: `LogOccurrenceProcessMessageUse` uses `loadAuthorizedOccurrence` correctly — its route (Task 8) is `/api/occurrences/{id}/process-messages/use`, where `{id}` genuinely is the occurrence id. This is the contrast that makes the bug in `PreviewOccurrenceProcessMessage` easy to miss: two sibling-looking handlers where only one of them has `{id}` mean "occurrence".

- [ ] **Step 4: Add the test-only export shim**

Append to `internal/handlers/occurrence_export_test.go`:

```go
func ResolveProcessMessageVariablesForTest(content string, occ *models.Occurrence, agentName string) string {
	return resolveProcessMessageVariables(content, occ, agentName)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/handlers/... -run 'TestResolveOccurrenceProcess|TestResolveProcessMessageVariables|TestPreviewOccurrenceProcessMessage|TestLogOccurrenceProcessMessageUse' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/occurrence_process_messages.go internal/handlers/occurrence_process_messages_test.go internal/handlers/occurrence_export_test.go internal/models/occurrences.go
git commit -m "feat(sac): resolve/preview/log process messages, explicit stage fallback

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 6: Wire `ProcessID` into `CreateOccurrence`/`UpdateOccurrence`, response DTO, preloads

**Files:**
- Modify: `internal/handlers/occurrences.go` (request struct, `CreateOccurrence`, `UpdateOccurrence`, `OccurrenceResponse`, `occurrenceToResponse`, 5 `Preload(` call sites)
- Modify: `internal/handlers/occurrence_sla_policies.go` (shared SLA-resolution helper)

**Interfaces:**
- Consumes: `models.OccurrenceProcess`, `findByIDAndOrg[T]`.
- Produces: `a.resolveOccurrenceSLAMinutes(orgID uuid.UUID, priority models.OccurrencePriority, processID *uuid.UUID) (responseMinutes, resolutionMinutes int, err error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/handlers/occurrences_process_test.go
package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestCreateOccurrence_WithProcessID_OverridesSLAAndIsReturnedInResponse(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var process models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").First(&process).Error)
	require.NotNil(t, process.ResponseMinutes)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":  contact.ID.String(),
		"description": "Produto chegou avariado",
		"process_id":  process.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.CreateOccurrence(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("organization_id = ? AND contact_id = ?", org.ID, contact.ID).First(&occ).Error)
	require.NotNil(t, occ.ProcessID)
	assert.Equal(t, process.ID, *occ.ProcessID)
	require.NotNil(t, occ.SLA.ResponseDeadline)

	expected := time.Now().Add(time.Duration(*process.ResponseMinutes) * time.Minute)
	assert.WithinDuration(t, expected, *occ.SLA.ResponseDeadline, time.Minute,
		"the process's own ResponseMinutes must win over the priority-based default policy")

	body := string(testutil.GetResponseBody(t, req))
	assert.Contains(t, body, process.ID.String(), "process_id must reach the API response, not just the DB row")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/... -run TestCreateOccurrence_WithProcessID -v`
Expected: FAIL — `process_id` unknown field / SLA not overridden / not in response.

- [ ] **Step 3: Add `ProcessID` to the request struct**

In `CreateOccurrenceRequest` (after `WhatHappenedID`):

```go
	ProcessID        *string `json:"process_id"`
```

- [ ] **Step 4: Add the shared SLA-resolution helper**

Add near `getSLAPolicy` in `internal/handlers/occurrence_sla_policies.go`:

```go
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
		return responseMinutes, resolutionMinutes, nil // unresolvable process id: fall back silently, same as an absent one
	}
	if process.ResponseMinutes != nil {
		responseMinutes = *process.ResponseMinutes
	}
	if process.ResolutionMinutes != nil {
		resolutionMinutes = *process.ResolutionMinutes
	}
	return responseMinutes, resolutionMinutes, nil
}
```

- [ ] **Step 5: Use it in `CreateOccurrence`**

Replace the existing SLA block:

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

with:

```go
	var processID *uuid.UUID
	if req.ProcessID != nil && *req.ProcessID != "" {
		id, err := uuid.Parse(*req.ProcessID)
		if err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid process_id", nil, "")
		}
		if _, err := findByIDAndOrg[models.OccurrenceProcess](a.DB, r, id, orgID, "Process"); err != nil {
			return nil
		}
		processID = &id
	}

	var responseDeadline, resolutionDeadline *time.Time
	if responseMinutes, resolutionMinutes, err := a.resolveOccurrenceSLAMinutes(orgID, priority, processID); err != nil {
		a.Log.Error("Failed to resolve SLA policy", "error", err, "organization_id", orgID)
	} else {
		now := time.Now()
		rd := now.Add(time.Duration(responseMinutes) * time.Minute)
		xd := now.Add(time.Duration(resolutionMinutes) * time.Minute)
		responseDeadline, resolutionDeadline = &rd, &xd
	}
```

Then add `ProcessID: processID,` to the `occ := models.Occurrence{...}` struct literal (next to `SaleChannel`/`InvoiceNumber`/etc).

- [ ] **Step 6: Use it in `UpdateOccurrence`'s priority-change branch**

Replace:

```go
		if policy, err := a.getSLAPolicy(orgID, models.OccurrencePriority(req.Priority)); err != nil {
			a.Log.Error("Failed to resolve SLA policy on priority change", "error", err, "occurrence", occ.ID)
		} else {
			now := time.Now()
			rd := now.Add(time.Duration(policy.ResponseMinutes) * time.Minute)
			xd := now.Add(time.Duration(policy.ResolutionMinutes) * time.Minute)
			updates["sla_response_deadline"] = rd
			updates["sla_resolution_deadline"] = xd
		}
```

with:

```go
		if responseMinutes, resolutionMinutes, err := a.resolveOccurrenceSLAMinutes(orgID, models.OccurrencePriority(req.Priority), occ.ProcessID); err != nil {
			a.Log.Error("Failed to resolve SLA policy on priority change", "error", err, "occurrence", occ.ID)
		} else {
			now := time.Now()
			rd := now.Add(time.Duration(responseMinutes) * time.Minute)
			xd := now.Add(time.Duration(resolutionMinutes) * time.Minute)
			updates["sla_response_deadline"] = rd
			updates["sla_resolution_deadline"] = xd
		}
```

- [ ] **Step 7: Add `ProcessID`/`ProcessName` to the response DTO**

In `OccurrenceResponse` (after `WhatHappenedName`):

```go
	ProcessID   *uuid.UUID `json:"process_id,omitempty"`
	ProcessName string     `json:"process_name,omitempty"`
```

In `occurrenceToResponse`, add `ProcessID: o.ProcessID,` to the initial struct literal, and after the `if o.WhatHappened != nil { ... }` block:

```go
	if o.Process != nil {
		resp.ProcessName = o.Process.Name
	}
```

- [ ] **Step 8: Add `.Preload("Process")` to all five call sites**

Append `.Preload("Process")` to each of the five `Preload(...)` chains in `occurrences.go` (lines ~513, 557, 667, 806, 918 as of this plan's writing — the important thing is "all five", not the exact line numbers, since earlier edits in this task shift them; grep `Preload("WhatHappened")` in the file and append `.Preload("Process")` right after every match).

- [ ] **Step 9: Run test to verify it passes**

Run: `go test ./internal/handlers/... -run TestCreateOccurrence_WithProcessID -v`
Expected: PASS

- [ ] **Step 10: Run the full occurrence test suite for regressions**

Run: `go test ./internal/handlers/... -run Occurrence -v`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add internal/handlers/occurrences.go internal/handlers/occurrence_sla_policies.go internal/handlers/occurrences_process_test.go
git commit -m "feat(sac): wire process_id into occurrence create/update, SLA override, response DTO

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 7: Optional message override on `SendOccurrenceProtocol`

**Files:**
- Modify: `internal/handlers/occurrence_send.go`
- Test: `internal/handlers/occurrence_send_test.go` (append)

**Interfaces:**
- Produces: `SendOccurrenceProtocolRequest{Message string}`.

- [ ] **Step 1: Write three failing tests covering all three compatibility cases**

Read `internal/handlers/occurrence_send_test.go` first to match its exact org/contact/24h-window/account setup, then append (reusing that setup for all three):

```go
func TestSendOccurrenceProtocol_NoBody_KeepsLegacyText(t *testing.T) {
	// ... existing file's setup ...
	req := testutil.NewJSONRequest(t, nil) // no body at all — must match today's exact production behaviour
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Order("created_at DESC").First(&msg).Error)
	assert.Contains(t, msg.Content, occ.ProtocolNumber)
	assert.Contains(t, msg.Content, "Guarde este número")
}

func TestSendOccurrenceProtocol_WithMessage_SendsExactlyThatText(t *testing.T) {
	// ... existing file's setup ...
	req := testutil.NewJSONRequest(t, map[string]any{"message": "Mensagem revisada pelo atendente."})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Order("created_at DESC").First(&msg).Error)
	assert.Equal(t, "Mensagem revisada pelo atendente.", msg.Content)
}

func TestSendOccurrenceProtocol_EmptyMessage_FallsBackToLegacyText(t *testing.T) {
	// ... existing file's setup ...
	req := testutil.NewJSONRequest(t, map[string]any{"message": ""})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.SendOccurrenceProtocol(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var msg models.Message
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).Order("created_at DESC").First(&msg).Error)
	assert.Contains(t, msg.Content, "Guarde este número", "an explicit empty string must behave exactly like an absent field")
}
```

- [ ] **Step 2: Run tests to verify they fail as expected**

Run: `go test ./internal/handlers/... -run TestSendOccurrenceProtocol -v`
Expected: `NoBody`/`EmptyMessage` PASS already (today's code ignores any body), `WithMessage` FAILS (today's code always sends the hardcoded text regardless of body).

- [ ] **Step 3: Implement**

In `internal/handlers/occurrence_send.go`, add before the function:

```go
// SendOccurrenceProtocolRequest is the (optional) body for sending the
// protocol/registration message. An absent or empty Message keeps today's
// exact hardcoded text — additive, not a behaviour change for existing
// callers that send no body at all.
type SendOccurrenceProtocolRequest struct {
	Message string `json:"message"`
}
```

Replace the body of `SendOccurrenceProtocol` from `body := fmt.Sprintf(...)` onward:

```go
	var req SendOccurrenceProtocolRequest
	_ = r.Decode(&req, "json") // best-effort: a caller sending no body at all must not fail here

	body := req.Message
	if body == "" {
		body = fmt.Sprintf("Seu protocolo de atendimento é %s. Guarde este número para consultas futuras.",
			occ.ProtocolNumber)
	}
```

- [ ] **Step 4: Run tests to verify all three pass**

Run: `go test ./internal/handlers/... -run TestSendOccurrenceProtocol -v`
Expected: PASS (all three new tests, plus every pre-existing test in the file).

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/occurrence_send.go internal/handlers/occurrence_send_test.go
git commit -m "feat(sac): allow an edited message to override the protocol registration text

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 8: Register routes

**Files:**
- Modify: `cmd/whatomate/main.go`

- [ ] **Step 1: Add routes**

After the existing occurrence-what-happened block in `cmd/whatomate/main.go`:

```go
	g.GET("/api/occurrence-processes", app.ListOccurrenceProcesses)
	g.POST("/api/occurrence-processes", app.CreateOccurrenceProcess)
	g.GET("/api/occurrence-processes/resolve", app.ResolveOccurrenceProcess)
	g.PUT("/api/occurrence-processes/{id}", app.UpdateOccurrenceProcess)
	g.DELETE("/api/occurrence-processes/{id}", app.DeleteOccurrenceProcess)
	g.GET("/api/occurrence-processes/{id}/messages", app.ListOccurrenceProcessMessages)
	g.PUT("/api/occurrence-processes/{id}/messages/{stage}", app.UpsertOccurrenceProcessMessage)
	g.GET("/api/occurrence-processes/{id}/messages/{stage}/preview", app.PreviewOccurrenceProcessMessage)
	g.POST("/api/occurrences/{id}/process-messages/use", app.LogOccurrenceProcessMessageUse)
```

(`/resolve` is registered before any `/{id}` route the same literal-before-param order every other route pair in this file already follows — confirm in Step 2 rather than assuming fastglue's router behaves that way.)

- [ ] **Step 2: Build and smoke-test routing**

Run: `go build ./... && go test ./internal/handlers/... -run 'TestResolveOccurrenceProcess|TestListOccurrenceProcesses' -v`
Expected: PASS. If `/resolve` gets captured by `/{id}` instead, rename it to a literal-safe path, e.g. `/api/occurrence-processes-resolve`, and update this task and Task 5/6's references.

- [ ] **Step 3: Commit**

```bash
git add cmd/whatomate/main.go
git commit -m "feat(sac): register occurrence-processes routes

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 9: Full backend regression pass + PR1 report

**Files:** none (verification task).

- [ ] **Step 1: Run the full backend test suite**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: PASS, zero failures, zero new `go vet` warnings. (Per the project's `TEST_DATABASE_URL`/`TEST_REDIS_URL` requirement, confirm those point at the `whatomate_test_pg`/`whatomate_test_redis` containers before running, or the suite panics on missing Redis.)

- [ ] **Step 2: If anything fails, fix forward**

Common expected friction points: the `/resolve` vs `/{id}` route order from Task 8, a `testutil` helper name that doesn't match what earlier tasks assumed (grep `test/testutil` for the real names).

- [ ] **Step 3: Commit any fixes**

```bash
git add -A
git commit -m "fix(sac): backend regression fixes for the process/motivo domain

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

(Skip if Step 1 passed clean.)

- [ ] **Step 4: Open the PR**

Title: `feat(sac): Processo/Motivo domain — backend`. Body must explicitly list, as open business pendências (not settled technical decisions):
1. The Category/WhatHappened mapping chosen for the five seeded processes, and the three brand-new reasons created ("Devolução Após 24h", "Alteração de Pedido em Separação", "Desistência do Pedido") — the validated HTML groups cases by delivery stage, not by this codebase's Category/WhatHappened vocabulary, so this mapping is this plan's interpretation pending product-owner sign-off.
2. The calendar-minutes values derived from each process's prazo text (e.g. "5 dias úteis" → 7200 minutes) — same semantics `OccurrenceSLAPolicy` already uses, explicitly not a business-hours calendar.

Also state plainly: only `registration` has a seeded message for all five processes; `documents` is additionally seeded only for "Avaria — comunicação e abertura". This is the intentional, spec-sanctioned partial state — PR2/PR3 are what let an agent use it, and later phases can add more per-stage templates without any schema change.

---

## Self-Review Notes (from the plan author, not a task to execute)

- **Bug fix applied from code review:** `PreviewOccurrenceProcessMessage` (Task 5) no longer calls `loadAuthorizedOccurrence`, which would have read the occurrence id from the wrong path param (`{id}` is the process id on this route). It now loads the occurrence explicitly by the `occurrence_id` query param, reapplies the same `canViewConversation` check, and additionally verifies the occurrence is linked to the process being previewed. Two new tests cover this directly.
- **Design fix applied from code review:** message fallback is now explicit and stage-aware (`fallbackProcessMessage` returns `(content, hasFallback)`), not a silent empty string masquerading as success. Only `registration` auto-fills; every other untemplated stage reports `has_template: false` for the caller to handle visibly.
- **Design fix applied from code review:** exactly one active `OccurrenceProcess` per `(organization, WhatHappenedID)` is now enforced by both a partial unique index (Task 1) and application-level validation with a clear 409 (Task 4) — `ResolveOccurrenceProcess`'s `.First()` is now provably deterministic instead of "probably fine in practice."
- **Removed from an earlier draft, per review:** `Occurrence.ProcessFieldValues` (a JSONB column nothing in this plan reads or writes) was cut entirely — every field a process can require already has a dedicated column.
- **Documentation fix applied from code review:** every seed comment referencing "X dias úteis" now says explicitly that the stored value is calendar minutes, not a business-hours conversion — this is also stated once in Global Constraints so it isn't easy to miss.
- **Scope fix applied from review:** this is now PR1 of 3 (backend only). PR2 (`docs/superpowers/plans/2026-09-18-sac-processo-motivo-pr2-protocolo.md`) covers the "Abrir protocolo" frontend and depends on this PR's endpoints existing. PR3 (`docs/superpowers/plans/2026-09-18-sac-processo-motivo-pr3-operacao-admin.md`) covers the occurrence detail view, admin settings screen, and final end-to-end regression.
