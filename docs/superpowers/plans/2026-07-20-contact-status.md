# Contact Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dar a cada conversa um estado de atendimento (`new` / `in_progress` / `resolved`) com transições automáticas, endpoint manual, filtro na lista e atualização em tempo real via WebSocket.

**Architecture:** Um helper central `transitionContactStatus()` concentra toda a regra: UPDATE condicional (que resolve corridas e garante um broadcast por mudança real), audit log e evento WebSocket. Três gatilhos chamam esse helper. No frontend, a sidebar ganha chips de filtro server-side, uma barra de status por linha e prévia da última mensagem; o evento WS mantém tudo sincronizado sem reload.

**Tech Stack:** Go 1.x + GORM + fastglue + Postgres; Vue 3 + Pinia + Tailwind + shadcn-vue; Playwright para e2e.

**Spec:** `docs/superpowers/specs/2026-07-20-contact-status-design.md`

## Global Constraints

- **Branch:** `feature/contact-status`, PR com base em `development`. Nunca commitar direto em `main`.
- **Broadcast WebSocket:** sempre `websocket.WSMessage{Type: ..., Payload: ...}` com payload de struct tipada. **Nunca** `map[string]any` como mensagem — quebra o build.
- **Campo JSON novo:** `contact_status`. O campo legado `status` do `ContactResponse` continua retornando `"active"` e **não pode ser alterado** (quebra integrações).
- **Valores do enum:** exatamente `new`, `in_progress`, `resolved`. Default `new`.
- **Audit log:** `models.AuditLog.UserID` é `NOT NULL`. Logo, **só transições manuais** (com ator) geram audit. Transições automáticas não geram — e isso é intencional, não um esquecimento.
- **i18n:** toda string visível entra em `frontend/src/i18n/locales/en.json` **e** `pt-BR.json`. Nenhum texto hardcoded no template.
- **Cores de status:** `new` → `emerald-500`; `in_progress` → `sky-400`; `resolved` → `white/30`. Toda cor acompanhada de texto/`aria-label` — cor nunca carrega informação sozinha.
- **Testes Go:** exigem `TEST_DATABASE_URL` e `TEST_REDIS_URL`; sem eles os testes fazem skip. Rodar com `make test`.
- **`audit.LogAudit` é assíncrono** (dispara goroutine). Testes que verificam audit usam `require.Eventually`.

---

### Task 1: Modelo, enum e migration

**Files:**
- Modify: `internal/models/constants.go`
- Modify: `internal/models/models.go:345-373` (struct `Contact`)
- Modify: `internal/database/postgres.go:233` (`getIndexes`), `:390` (junto de `BackfillLastInboundAt`), `:211` (chamada do backfill)
- Test: `internal/database/database_test.go`

**Interfaces:**
- Consumes: nada
- Produces: `models.ContactStatus` (string), constantes `models.ContactStatusNew` / `ContactStatusInProgress` / `ContactStatusResolved`, campo `Contact.ContactStatus`, `database.BackfillContactStatus(db *gorm.DB) error`

- [ ] **Step 1: Escrever o teste do backfill (falhando)**

Em `internal/database/database_test.go`:

```go
func TestBackfillContactStatus(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)

	// Sem mensagens → permanece 'new'
	silent := testutil.CreateTestContact(t, db, org.ID)

	// Inbound recente → 'in_progress'
	recent := testutil.CreateTestContact(t, db, org.ID)
	now := time.Now()
	require.NoError(t, db.Model(recent).Update("last_inbound_at", now).Error)

	// Não lido → 'in_progress'
	unread := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(unread).Update("is_read", false).Error)

	// Histórico antigo, lido → 'resolved'
	old := testutil.CreateTestContact(t, db, org.ID)
	longAgo := now.Add(-72 * time.Hour)
	require.NoError(t, db.Model(old).Updates(map[string]any{
		"last_inbound_at": longAgo,
		"is_read":         true,
	}).Error)

	require.NoError(t, database.BackfillContactStatus(db))

	statusOf := func(id uuid.UUID) string {
		var s string
		require.NoError(t, db.Model(&models.Contact{}).Where("id = ?", id).Pluck("contact_status", &s).Error)
		return s
	}

	assert.Equal(t, "new", statusOf(silent.ID))
	assert.Equal(t, "in_progress", statusOf(recent.ID))
	assert.Equal(t, "in_progress", statusOf(unread.ID))
	assert.Equal(t, "resolved", statusOf(old.ID))
}

func TestBackfillContactStatus_Idempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	c := testutil.CreateTestContact(t, db, org.ID)

	require.NoError(t, database.BackfillContactStatus(db))
	// Um agente conclui manualmente
	require.NoError(t, db.Model(c).Update("contact_status", "resolved").Error)
	// Segunda execução não pode sobrescrever estado real
	require.NoError(t, database.BackfillContactStatus(db))

	var s string
	require.NoError(t, db.Model(&models.Contact{}).Where("id = ?", c.ID).Pluck("contact_status", &s).Error)
	assert.Equal(t, "resolved", s)
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `make test 2>&1 | grep -i contactstatus`
Expected: FAIL — `undefined: database.BackfillContactStatus`

- [ ] **Step 3: Adicionar o enum**

Em `internal/models/constants.go`, junto das constantes existentes:

```go
// ContactStatus is the service state of a conversation.
type ContactStatus string

const (
	ContactStatusNew        ContactStatus = "new"
	ContactStatusInProgress ContactStatus = "in_progress"
	ContactStatusResolved   ContactStatus = "resolved"
)

// IsValid reports whether s is one of the three known contact statuses.
func (s ContactStatus) IsValid() bool {
	switch s {
	case ContactStatusNew, ContactStatusInProgress, ContactStatusResolved:
		return true
	}
	return false
}
```

- [ ] **Step 4: Adicionar o campo ao modelo**

Em `internal/models/models.go`, na struct `Contact`, logo após `IsRead`:

```go
	ContactStatus ContactStatus `gorm:"size:20;not null;default:'new';index" json:"contact_status"`
```

- [ ] **Step 5: Escrever o backfill**

Em `internal/database/postgres.go`, ao lado de `BackfillLastInboundAt`:

```go
// BackfillContactStatus derives contact_status for existing contacts.
// Guarded by contact_status = 'new' (the column default), so it is idempotent
// and can never overwrite a status an agent actually set.
func BackfillContactStatus(db *gorm.DB) error {
	// Active conversations: recent inbound message or unread.
	if err := db.Exec(`
		UPDATE contacts
		SET contact_status = 'in_progress'
		WHERE contact_status = 'new'
		  AND deleted_at IS NULL
		  AND (
		        (last_inbound_at IS NOT NULL AND last_inbound_at > NOW() - INTERVAL '24 hours')
		     OR is_read = false
		  )
	`).Error; err != nil {
		return err
	}

	// Contacts with message history but no recent activity are considered done.
	return db.Exec(`
		UPDATE contacts c
		SET contact_status = 'resolved'
		WHERE c.contact_status = 'new'
		  AND c.deleted_at IS NULL
		  AND EXISTS (
		        SELECT 1 FROM messages m
		        WHERE m.contact_id = c.id AND m.deleted_at IS NULL
		  )
	`).Error
}
```

- [ ] **Step 6: Adicionar CHECK e índice composto**

Em `getIndexes()` (`internal/database/postgres.go:234`), no fim da lista retornada:

```go
		// Contact status: CHECK constraint (Postgres has no ADD CONSTRAINT IF NOT EXISTS)
		`DO $$ BEGIN
			ALTER TABLE contacts ADD CONSTRAINT chk_contacts_contact_status
				CHECK (contact_status IN ('new','in_progress','resolved'));
		EXCEPTION WHEN duplicate_object THEN NULL;
		END $$`,
		// Composite index matching the ListContacts filter + ordering
		`CREATE INDEX IF NOT EXISTS idx_contacts_org_status_lastmsg
			ON contacts(organization_id, contact_status, last_message_at DESC NULLS LAST)`,
```

O CHECK roda antes do backfill na ordem real de `RunMigrationWithProgress`, e isso é seguro: o `AutoMigrate` cria a coluna `NOT NULL DEFAULT 'new'`, então toda linha já satisfaz a constraint.

- [ ] **Step 7: Chamar o backfill na migration**

Em `RunMigrationWithProgress`, logo após o bloco de `BackfillLastInboundAt` (~linha 214):

```go
	// Backfill contact_status from existing activity
	if err := BackfillContactStatus(silentDB); err != nil {
		fmt.Printf("\n  \033[31m✗ Failed to backfill contact_status\033[0m\n\n")
		return err
	}
```

- [ ] **Step 8: Rodar os testes**

Run: `make test 2>&1 | grep -i contactstatus`
Expected: PASS nos dois testes

- [ ] **Step 9: Commit**

```bash
git add internal/models/constants.go internal/models/models.go internal/database/postgres.go internal/database/database_test.go
git commit -m "feat(contacts): add contact_status column, CHECK constraint, index and backfill"
```

---

### Task 2: Tipo e payload do evento WebSocket

**Files:**
- Modify: `internal/websocket/messages.go`

**Interfaces:**
- Consumes: nada
- Produces: `websocket.TypeContactStatusChanged` (string `"contact_status_changed"`), `websocket.ContactStatusChangedPayload{ContactID uuid.UUID, OldStatus, NewStatus string, ChangedByUserID *uuid.UUID, ChangedAt time.Time}`

- [ ] **Step 1: Adicionar a constante e a struct**

Em `internal/websocket/messages.go`, na lista de constantes de tipo (após `TypeContactUpdate`):

```go
	TypeContactStatusChanged = "contact_status_changed"
```

E, após a definição de `BroadcastMessage`:

```go
// ContactStatusChangedPayload is the payload for contact_status_changed events.
// Typed struct — never broadcast a bare map.
type ContactStatusChangedPayload struct {
	ContactID       uuid.UUID  `json:"contact_id"`
	OldStatus       string     `json:"old_status"`
	NewStatus       string     `json:"new_status"`
	ChangedByUserID *uuid.UUID `json:"changed_by_user_id,omitempty"`
	ChangedAt       time.Time  `json:"changed_at"`
}
```

Adicionar `"time"` ao bloco de imports do arquivo (hoje ele importa apenas `github.com/google/uuid`).

- [ ] **Step 2: Verificar que compila**

Run: `go build ./...`
Expected: sem saída (sucesso)

- [ ] **Step 3: Commit**

```bash
git add internal/websocket/messages.go
git commit -m "feat(ws): add contact_status_changed event type and typed payload"
```

---

### Task 3: Helper central de transição

**Files:**
- Create: `internal/handlers/contact_status.go`
- Test: `internal/handlers/contact_status_test.go`

**Interfaces:**
- Consumes: `models.ContactStatus` (Task 1), `websocket.TypeContactStatusChanged` + `websocket.ContactStatusChangedPayload` (Task 2)
- Produces: `func (a *App) transitionContactStatus(contact *models.Contact, to models.ContactStatus, from []models.ContactStatus, actorID *uuid.UUID) (bool, error)` — retorna `true` quando a transição realmente ocorreu

- [ ] **Step 1: Escrever os testes (falhando)**

Em `internal/handlers/contact_status_test.go`:

```go
package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransitionContactStatus(t *testing.T) {
	t.Parallel()

	t.Run("transitions when current status matches from", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("contact_status", models.ContactStatusResolved).Error)
		contact.ContactStatus = models.ContactStatusResolved

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusResolved},
			nil)

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, models.ContactStatusInProgress, contact.ContactStatus)

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, contact.ID).Error)
		assert.Equal(t, models.ContactStatusInProgress, stored.ContactStatus)
	})

	t.Run("no-op when current status is outside from", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // 'new'

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusResolved},
			nil)

		require.NoError(t, err)
		assert.False(t, changed)

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, contact.ID).Error)
		assert.Equal(t, models.ContactStatusNew, stored.ContactStatus)
	})

	t.Run("empty from allows any origin", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusResolved, nil, nil)

		require.NoError(t, err)
		assert.True(t, changed)
	})

	t.Run("manual transition writes an audit log", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusResolved, nil, &user.ID)
		require.NoError(t, err)
		require.True(t, changed)

		// audit.LogAudit writes asynchronously
		require.Eventually(t, func() bool {
			var count int64
			app.DB.Model(&models.AuditLog{}).
				Where("resource_type = ? AND resource_id = ? AND user_id = ?", "contact", contact.ID, user.ID).
				Count(&count)
			return count == 1
		}, 3*time.Second, 50*time.Millisecond)
	})

	t.Run("automatic transition writes no audit log", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusResolved, nil, nil)
		require.NoError(t, err)
		require.True(t, changed)

		time.Sleep(200 * time.Millisecond) // give any stray goroutine a chance
		var count int64
		app.DB.Model(&models.AuditLog{}).
			Where("resource_type = ? AND resource_id = ?", "contact", contact.ID).
			Count(&count)
		assert.Equal(t, int64(0), count)
	})
}
```

Como o helper é minúsculo (não exportado) e os testes vivem em `handlers_test`, adicionar o export de teste em `internal/handlers/stubs.go` — ou, se o projeto já tiver um `export_test.go` no pacote (o `internal/websocket` tem), criar `internal/handlers/export_test.go`:

```go
package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// TransitionContactStatusForTest exposes transitionContactStatus to tests.
func (a *App) TransitionContactStatusForTest(
	contact *models.Contact,
	to models.ContactStatus,
	from []models.ContactStatus,
	actorID *uuid.UUID,
) (bool, error) {
	return a.transitionContactStatus(contact, to, from, actorID)
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `make test 2>&1 | grep -i TestTransitionContactStatus`
Expected: FAIL — `undefined: transitionContactStatus`

- [ ] **Step 3: Implementar o helper**

Criar `internal/handlers/contact_status.go`:

```go
package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
)

// transitionContactStatus moves a contact to a new status, but only if its
// current status is in `from` (an empty `from` allows any origin).
//
// The UPDATE is conditional on the current value, which is what makes
// concurrent triggers safe: an inbound message and an agent reply landing at
// the same instant produce exactly one transition and one broadcast, not two.
//
// actorID is nil for automatic transitions. Only manual transitions produce an
// audit entry — models.AuditLog.UserID is NOT NULL, so there is no valid row to
// write without an actor.
//
// Returns true only when a row actually changed.
func (a *App) transitionContactStatus(
	contact *models.Contact,
	to models.ContactStatus,
	from []models.ContactStatus,
	actorID *uuid.UUID,
) (bool, error) {
	oldStatus := contact.ContactStatus
	if oldStatus == to {
		return false, nil
	}

	q := a.DB.Model(&models.Contact{}).Where("id = ?", contact.ID)
	if len(from) > 0 {
		q = q.Where("contact_status IN ?", from)
	}

	res := q.Update("contact_status", to)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}

	contact.ContactStatus = to
	changedAt := time.Now()

	if actorID != nil {
		userName := audit.GetUserName(a.DB, *actorID)
		audit.LogAudit(a.DB, contact.OrganizationID, *actorID, userName,
			"contact", contact.ID, models.AuditActionUpdated,
			map[string]any{"contact_status": string(oldStatus)},
			map[string]any{"contact_status": string(to)},
		)
	}

	if a.WSHub != nil {
		a.WSHub.BroadcastToOrg(contact.OrganizationID, websocket.WSMessage{
			Type: websocket.TypeContactStatusChanged,
			Payload: websocket.ContactStatusChangedPayload{
				ContactID:       contact.ID,
				OldStatus:       string(oldStatus),
				NewStatus:       string(to),
				ChangedByUserID: actorID,
				ChangedAt:       changedAt,
			},
		})
	}

	return true, nil
}
```

- [ ] **Step 4: Rodar os testes**

Run: `make test 2>&1 | grep -i TestTransitionContactStatus`
Expected: PASS nos cinco subtestes

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/contact_status.go internal/handlers/contact_status_test.go internal/handlers/export_test.go
git commit -m "feat(contacts): add central contact status transition helper"
```

---

### Task 4: Endpoint `PUT /contacts/{id}/status`

**Files:**
- Modify: `internal/handlers/contact_status.go`
- Modify: `cmd/whatomate/main.go:632` (após a rota `/tags`)
- Test: `internal/handlers/contact_status_test.go`

**Interfaces:**
- Consumes: `transitionContactStatus` (Task 3)
- Produces: `func (a *App) UpdateContactStatus(r *fastglue.Request) error`, `type UpdateContactStatusRequest struct { ContactStatus models.ContactStatus \`json:"contact_status"\` }`

- [ ] **Step 1: Escrever os testes (falhando)**

Anexar a `internal/handlers/contact_status_test.go`:

```go
func TestApp_UpdateContactStatus(t *testing.T) {
	t.Parallel()

	t.Run("resolves a conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, contact.ID).Error)
		assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus)
	})

	t.Run("rejects an invalid status", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "done"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	})

	t.Run("denies a user without write permission", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		user := testutil.CreateTestUser(t, app.DB, org.ID) // no role
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})

	t.Run("does not reach a contact from another org", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		otherOrg := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		foreign := testutil.CreateTestContact(t, app.DB, otherOrg.ID)

		req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", foreign.ID.String())

		require.NoError(t, app.UpdateContactStatus(req))
		assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
	})
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `make test 2>&1 | grep -i TestApp_UpdateContactStatus`
Expected: FAIL — `app.UpdateContactStatus undefined`

- [ ] **Step 3: Implementar o handler**

Anexar a `internal/handlers/contact_status.go` (e adicionar `"github.com/valyala/fasthttp"` e `"github.com/zerodha/fastglue"` aos imports):

```go
// UpdateContactStatusRequest is the body of PUT /contacts/{id}/status.
type UpdateContactStatusRequest struct {
	ContactStatus models.ContactStatus `json:"contact_status"`
}

// UpdateContactStatus manually sets a conversation's service status.
func (a *App) UpdateContactStatus(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	if !a.HasPermission(userID, models.ResourceContacts, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have permission to change contact status", nil, "")
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var req UpdateContactStatusRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	if !req.ContactStatus.IsValid() {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"contact_status must be one of: new, in_progress, resolved", nil, "")
	}

	contact, err := findByIDAndOrg[models.Contact](a.DB, r, contactID, orgID, "Contact")
	if err != nil {
		return nil
	}

	if _, err := a.transitionContactStatus(contact, req.ContactStatus, nil, &userID); err != nil {
		a.Log.Error("Failed to update contact status", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact status", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"id":             contact.ID,
		"contact_status": contact.ContactStatus,
	})
}
```

- [ ] **Step 4: Registrar a rota**

Em `cmd/whatomate/main.go`, após a linha 632 (`/api/contacts/{id}/tags`):

```go
	g.PUT("/api/contacts/{id}/status", app.UpdateContactStatus)
```

- [ ] **Step 5: Rodar os testes**

Run: `make test 2>&1 | grep -i TestApp_UpdateContactStatus`
Expected: PASS nos quatro subtestes

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/contact_status.go internal/handlers/contact_status_test.go cmd/whatomate/main.go
git commit -m "feat(api): add PUT /contacts/{id}/status endpoint"
```

---

### Task 5: Filtro na listagem, campo na resposta e contador

**Files:**
- Modify: `internal/handlers/contacts.go:26-45` (`ContactResponse`), `:87-198` (`ListContacts`), `:1560-1600` (montagem de resposta do `GetContact`)
- Modify: `internal/handlers/contact_status.go` (novo handler de contagem)
- Modify: `cmd/whatomate/main.go`
- Test: `internal/handlers/contact_status_test.go`

**Interfaces:**
- Consumes: `models.ContactStatus` (Task 1)
- Produces: campo `ContactResponse.ContactStatus`, query param `status` em `ListContacts`, `func (a *App) GetContactStatusCounts(r *fastglue.Request) error` respondendo `{"new": <int64>}`

- [ ] **Step 1: Escrever os testes (falhando)**

Anexar a `internal/handlers/contact_status_test.go`:

```go
func TestApp_ListContacts_StatusFilter(t *testing.T) {
	t.Parallel()

	newTestOrgWithContacts := func(t *testing.T) (*handlers.App, uuid.UUID, uuid.UUID) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

		c1 := testutil.CreateTestContact(t, app.DB, org.ID) // new
		c2 := testutil.CreateTestContact(t, app.DB, org.ID)
		c3 := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(c2).Update("contact_status", models.ContactStatusInProgress).Error)
		require.NoError(t, app.DB.Model(c3).Update("contact_status", models.ContactStatusResolved).Error)
		_ = c1
		return app, org.ID, user.ID
	}

	t.Run("filters by status and reports a matching total", func(t *testing.T) {
		app, orgID, userID := newTestOrgWithContacts(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)
		testutil.SetQueryParam(req, "status", "in_progress")

		require.NoError(t, app.ListContacts(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp struct {
			Data struct {
				Contacts []handlers.ContactResponse `json:"contacts"`
				Total    int64                      `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, int64(1), resp.Data.Total)
		require.Len(t, resp.Data.Contacts, 1)
		assert.Equal(t, models.ContactStatusInProgress, resp.Data.Contacts[0].ContactStatus)
	})

	t.Run("rejects an invalid status", func(t *testing.T) {
		app, orgID, userID := newTestOrgWithContacts(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)
		testutil.SetQueryParam(req, "status", "bogus")

		require.NoError(t, app.ListContacts(req))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
	})

	t.Run("counts only new conversations", func(t *testing.T) {
		app, orgID, userID := newTestOrgWithContacts(t)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, orgID, userID)

		require.NoError(t, app.GetContactStatusCounts(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp struct {
			Data struct {
				New int64 `json:"new"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		assert.Equal(t, int64(1), resp.Data.New)
	})
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `make test 2>&1 | grep -i TestApp_ListContacts_StatusFilter`
Expected: FAIL — `resp.Data.Contacts[0].ContactStatus undefined` e `app.GetContactStatusCounts undefined`

- [ ] **Step 3: Adicionar o campo à resposta**

Em `internal/handlers/contacts.go`, na struct `ContactResponse`, logo após o campo `Status`:

```go
	ContactStatus      models.ContactStatus `json:"contact_status"`
```

O campo `Status string \`json:"status"\`` fica **inalterado**, continuando a receber `"active"`.

Preencher nos dois pontos de montagem: em `ListContacts` (~linha 178, junto de `Status: "active"`) e no handler `GetContact` (~linha 1580):

```go
			ContactStatus:      c.ContactStatus,
```

(no `GetContact` a variável é `contact`, então `ContactStatus: contact.ContactStatus`)

- [ ] **Step 4: Adicionar o filtro em ListContacts**

Em `internal/handlers/contacts.go`, após a leitura de `tagsParam` (~linha 96):

```go
	statusParam := string(r.RequestCtx.QueryArgs().Peek("status"))
	if statusParam != "" && !models.ContactStatus(statusParam).IsValid() {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			"status must be one of: new, in_progress, resolved", nil, "")
	}
```

E, após o bloco do filtro de tags e **antes** do `Order` (~linha 136):

```go
	if statusParam != "" {
		query = query.Where("contact_status = ?", statusParam)
	}
```

Como o `Count` acontece depois (linha 140), o `total` já reflete o filtro sem mudança adicional.

- [ ] **Step 5: Implementar o contador**

Anexar a `internal/handlers/contact_status.go`:

```go
// GetContactStatusCounts returns conversation counts by status, scoped to what
// the requesting user can actually see. Only 'new' is reported — it is the only
// count the sidebar surfaces.
func (a *App) GetContactStatusCounts(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	query := a.ScopeToOrg(a.DB, userID, orgID)
	query = a.scopeAssignedContact(query, userID, orgID)

	var newCount int64
	if err := query.Model(&models.Contact{}).
		Where("contact_status = ?", models.ContactStatusNew).
		Count(&newCount).Error; err != nil {
		a.Log.Error("Failed to count contacts by status", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to count contacts", nil, "")
	}

	return r.SendEnvelope(map[string]any{"new": newCount})
}
```

- [ ] **Step 6: Registrar a rota**

Em `cmd/whatomate/main.go`, junto das demais rotas de contato:

```go
	g.GET("/api/contacts/counts", app.GetContactStatusCounts)
```

Registrar **antes** de `/api/contacts/{id}` para que `counts` não seja capturado como um `{id}`.

- [ ] **Step 7: Rodar os testes**

Run: `make test 2>&1 | grep -i "TestApp_ListContacts"`
Expected: PASS (os três novos subtestes e os testes de listagem já existentes)

- [ ] **Step 8: Commit**

```bash
git add internal/handlers/contacts.go internal/handlers/contact_status.go internal/handlers/contact_status_test.go cmd/whatomate/main.go
git commit -m "feat(api): expose contact_status, add status filter and new-count endpoint"
```

---

### Task 6: Transições automáticas

**Files:**
- Modify: `internal/handlers/chatbot_processor.go:1603` (gatilho INCOMING)
- Modify: `internal/handlers/messages.go:255-262` (gatilho de envio do agente)
- Test: `internal/handlers/contact_status_test.go`

**Interfaces:**
- Consumes: `transitionContactStatus` (Task 3)
- Produces: nada de novo — apenas comportamento

- [ ] **Step 1: Escrever os testes (falhando)**

Anexar a `internal/handlers/contact_status_test.go`:

```go
func TestContactStatusAutoTransitions(t *testing.T) {
	t.Parallel()

	t.Run("incoming message reopens a resolved conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("contact_status", models.ContactStatusResolved).Error)
		contact.ContactStatus = models.ContactStatusResolved

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusResolved},
			nil)

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, models.ContactStatusInProgress, contact.ContactStatus)
	})

	t.Run("incoming message leaves a new conversation in the queue", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // 'new'

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusResolved},
			nil)

		require.NoError(t, err)
		assert.False(t, changed)

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, contact.ID).Error)
		assert.Equal(t, models.ContactStatusNew, stored.ContactStatus,
			"an inbound message must not pull a contact out of the 'new' queue")
	})

	t.Run("agent reply takes a new conversation into progress", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // 'new'

		changed, err := app.TransitionContactStatusForTest(contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusNew},
			nil)

		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, models.ContactStatusInProgress, contact.ContactStatus)
	})
}
```

- [ ] **Step 2: Rodar e confirmar que passa nos três**

Run: `make test 2>&1 | grep -i TestContactStatusAutoTransitions`
Expected: PASS — estes testes cobrem a *regra* via o helper. Os steps 3 e 4 ligam a regra aos call sites reais.

- [ ] **Step 3: Ligar o gatilho de mensagem INCOMING**

Em `internal/handlers/chatbot_processor.go`, imediatamente após o bloco `a.DB.Model(contact).Updates(map[string]any{...})` que termina na linha ~1608:

```go
	// An inbound message reopens a resolved conversation. A 'new' contact stays
	// 'new' — it only leaves the queue when an agent actually replies.
	if _, err := a.transitionContactStatus(contact,
		models.ContactStatusInProgress,
		[]models.ContactStatus{models.ContactStatusResolved},
		nil); err != nil {
		a.Log.Error("Failed to auto-transition contact status", "error", err, "contact_id", contact.ID)
	}
```

- [ ] **Step 4: Ligar o gatilho de envio do agente**

Em `internal/handlers/messages.go`, dentro de `SendOutgoingMessage`, logo após `a.updateContactLastMessage(req.Contact, preview)` (linha ~261):

```go
	// An agent replying is what starts the service — it is the only thing that
	// takes a conversation out of the 'new' queue. Chatbot sends have no
	// SentByUserID and deliberately do not count.
	if opts.SentByUserID != nil {
		if _, err := a.transitionContactStatus(req.Contact,
			models.ContactStatusInProgress,
			[]models.ContactStatus{models.ContactStatusNew},
			opts.SentByUserID); err != nil {
			a.Log.Error("Failed to auto-transition contact status", "error", err, "contact_id", req.Contact.ID)
		}
	}
```

Nota: aqui o ator **é** conhecido, então esta transição gera audit log — correto, foi um agente que agiu.

- [ ] **Step 5: Verificar build e suíte completa**

Run: `go build ./... && make test`
Expected: build limpo; nenhuma regressão na suíte

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/chatbot_processor.go internal/handlers/messages.go internal/handlers/contact_status_test.go
git commit -m "feat(contacts): auto-transition status on inbound message and agent reply"
```

---

### Task 7: Tipos, serviço de API e store no frontend

**Files:**
- Modify: `frontend/src/stores/contacts.ts:17-34` (interface `Contact`), `:93-175` (estado e fetch)
- Modify: `frontend/src/services/api.ts:191-204` (`contactsService`)

**Interfaces:**
- Consumes: endpoints das Tasks 4 e 5
- Produces: `Contact.contact_status` e `Contact.last_message_preview`; `contactsService.updateStatus(id, status)`, `contactsService.statusCounts()`; no store: `statusFilter` (ref), `newCount` (ref), `setStatusFilter(status)`, `fetchStatusCounts()`, `applyStatusChange(payload)`

- [ ] **Step 1: Estender o tipo Contact e o serviço**

Em `frontend/src/stores/contacts.ts`, na interface `Contact`, após `status: string`:

```ts
  contact_status: 'new' | 'in_progress' | 'resolved'
  last_message_preview?: string
```

Em `frontend/src/services/api.ts`, dentro de `contactsService`:

```ts
  updateStatus: (id: string, status: 'new' | 'in_progress' | 'resolved') =>
    api.put(`/contacts/${id}/status`, { contact_status: status }),
  statusCounts: () => api.get('/contacts/counts'),
```

E estender a assinatura de `list`:

```ts
  list: (params?: { search?: string; page?: number; limit?: number; tags?: string; status?: string }) =>
    api.get('/contacts', { params }),
```

- [ ] **Step 2: Adicionar estado e ações ao store**

Em `frontend/src/stores/contacts.ts`, junto de `selectedTags` (linha ~94):

```ts
  const statusFilter = ref<'all' | 'new' | 'in_progress' | 'resolved'>('all')
  const newCount = ref(0)
```

Em `fetchContacts` e `loadMoreContacts`, passar o filtro adiante. Em `fetchContacts`, após a linha do `tagsParam`:

```ts
      const statusParam = statusFilter.value === 'all' ? undefined : statusFilter.value
```

e incluir `status: statusParam,` no objeto passado a `contactsService.list({...})`. Repetir o mesmo par de linhas em `loadMoreContacts`, senão a paginação infinita ignora o filtro e mistura status.

Adicionar as ações:

```ts
  async function setStatusFilter(status: 'all' | 'new' | 'in_progress' | 'resolved') {
    if (statusFilter.value === status) return
    statusFilter.value = status
    const search = normalizeContactSearch(searchQuery.value) || undefined
    await fetchContacts({ search })
  }

  async function fetchStatusCounts() {
    try {
      const response = await contactsService.statusCounts()
      const data = response.data.data || response.data
      newCount.value = data.new ?? 0
    } catch (error) {
      console.error('Failed to fetch status counts:', error)
    }
  }

  async function updateContactStatus(id: string, status: 'new' | 'in_progress' | 'resolved') {
    await contactsService.updateStatus(id, status)
    // The WebSocket event does the list bookkeeping; keep the open chat header
    // in sync immediately so the button does not lag behind the click.
    if (currentContact.value?.id === id) {
      currentContact.value.contact_status = status
    }
  }

  // applyStatusChange reconciles a contact_status_changed event with the list.
  function applyStatusChange(payload: {
    contact_id: string
    old_status: string
    new_status: string
  }) {
    const status = payload.new_status as Contact['contact_status']

    if (payload.old_status === 'new' && status !== 'new') {
      newCount.value = Math.max(0, newCount.value - 1)
    } else if (payload.old_status !== 'new' && status === 'new') {
      newCount.value += 1
    }

    const contact = contacts.value.find(c => c.id === payload.contact_id)
    if (contact) {
      contact.contact_status = status
      // Drop it from the list when it no longer matches the active filter.
      if (statusFilter.value !== 'all' && statusFilter.value !== status) {
        contacts.value = contacts.value.filter(c => c.id !== payload.contact_id)
        contactsTotal.value = Math.max(0, contactsTotal.value - 1)
      }
    }

    if (currentContact.value?.id === payload.contact_id) {
      currentContact.value.contact_status = status
    }
  }
```

Exportar tudo no `return` do store: `statusFilter`, `newCount`, `setStatusFilter`, `fetchStatusCounts`, `updateContactStatus`, `applyStatusChange`.

- [ ] **Step 3: Verificar tipos**

Run: `cd frontend && npx vue-tsc --noEmit`
Expected: sem erros

- [ ] **Step 4: Commit**

```bash
git add frontend/src/stores/contacts.ts frontend/src/services/api.ts
git commit -m "feat(frontend): add contact status state, filter and API calls to store"
```

---

### Task 8: Chips de filtro e item da lista

**Files:**
- Create: `frontend/src/components/chat/ConversationStatusFilter.vue`
- Create: `frontend/src/components/chat/ConversationListItem.vue`
- Modify: `frontend/src/views/chat/ChatView.vue:1660-1795` (cabeçalho da sidebar e lista)
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`

**Interfaces:**
- Consumes: `statusFilter`, `newCount`, `setStatusFilter` (Task 7)
- Produces: `<ConversationStatusFilter :model-value :new-count @update:model-value>`, `<ConversationListItem :contact :active @click>`

- [ ] **Step 1: Adicionar as strings de i18n**

Em `frontend/src/i18n/locales/en.json`, dentro do objeto `chat`:

```json
      "filterAll": "All",
      "statusNew": "New",
      "statusInProgress": "In progress",
      "statusResolved": "Resolved",
      "conversationStatus": "Conversation status: {status}",
      "resolveConversation": "Resolve conversation",
      "reopenConversation": "Reopen conversation",
      "conversationResolved": "Conversation resolved",
      "conversationReopened": "Conversation reopened",
      "statusChangeFailed": "Could not change the conversation status. Try again.",
```

Em `frontend/src/i18n/locales/pt-BR.json`, nas mesmas chaves:

```json
      "filterAll": "Todos",
      "statusNew": "Novo",
      "statusInProgress": "Em andamento",
      "statusResolved": "Concluído",
      "conversationStatus": "Status do atendimento: {status}",
      "resolveConversation": "Concluir atendimento",
      "reopenConversation": "Reabrir atendimento",
      "conversationResolved": "Atendimento concluído",
      "conversationReopened": "Atendimento reaberto",
      "statusChangeFailed": "Não foi possível alterar o status do atendimento. Tente de novo.",
```

- [ ] **Step 2: Criar o componente de filtro**

`frontend/src/components/chat/ConversationStatusFilter.vue`:

```vue
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

type StatusFilter = 'all' | 'new' | 'in_progress' | 'resolved'

const props = defineProps<{
  modelValue: StatusFilter
  newCount: number
}>()

const emit = defineEmits<{ 'update:modelValue': [StatusFilter] }>()

const { t } = useI18n()

const options = computed(() => [
  { value: 'all' as const, label: t('chat.filterAll') },
  { value: 'new' as const, label: t('chat.statusNew') },
  { value: 'in_progress' as const, label: t('chat.statusInProgress') },
  { value: 'resolved' as const, label: t('chat.statusResolved') }
])
</script>

<template>
  <!-- Scrolling pills, not a 4-column segmented control: "Em andamento" alone
       does not fit the 320px sidebar without truncating, and truncation would
       only get worse in longer locales. -->
  <div
    class="flex items-center gap-1.5 overflow-x-auto px-2 pb-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
    role="tablist"
  >
    <button
      v-for="opt in options"
      :key="opt.value"
      role="tab"
      :aria-selected="props.modelValue === opt.value"
      :class="[
        'flex-shrink-0 inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500',
        props.modelValue === opt.value
          ? 'bg-emerald-600 text-white'
          : 'bg-white/[0.08] text-white/70 hover:text-white hover:bg-white/[0.12] light:bg-gray-100 light:text-gray-600 light:hover:bg-gray-200 light:hover:text-gray-900'
      ]"
      @click="emit('update:modelValue', opt.value)"
    >
      {{ opt.label }}
      <span
        v-if="opt.value === 'new' && props.newCount > 0"
        class="inline-flex h-4 min-w-[16px] items-center justify-center rounded-full px-1 text-[10px]"
        :class="props.modelValue === 'new'
          ? 'bg-white/25 text-white'
          : 'bg-emerald-500 text-white'"
      >
        {{ props.newCount }}
      </span>
    </button>
  </div>
</template>
```

- [ ] **Step 3: Criar o item da lista**

`frontend/src/components/chat/ConversationListItem.vue`:

```vue
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import type { Contact } from '@/stores/contacts'

const props = defineProps<{
  contact: Contact
  active: boolean
  formatTime: (value?: string) => string
  getInitials: (value: string) => string
  getAvatarGradient: (value: string) => string
}>()

const { t } = useI18n()

const statusLabel = computed(() => {
  switch (props.contact.contact_status) {
    case 'new': return t('chat.statusNew')
    case 'in_progress': return t('chat.statusInProgress')
    default: return t('chat.statusResolved')
  }
})

// Resolved conversations carry no bar — the absence of a signal is the signal.
const statusBarClass = computed(() => {
  switch (props.contact.contact_status) {
    case 'new': return 'bg-emerald-500'
    case 'in_progress': return 'bg-sky-400'
    default: return 'bg-transparent'
  }
})

const displayName = computed(() => props.contact.name || props.contact.phone_number)
</script>

<template>
  <div
    :class="[
      'relative flex items-center gap-2 px-3 py-2 cursor-pointer hover:bg-white/[0.04] light:hover:bg-gray-50 transition-colors',
      props.active && 'bg-white/[0.08] light:bg-gray-100'
    ]"
    :title="t('chat.conversationStatus', { status: statusLabel })"
  >
    <span
      class="absolute left-0 top-0 bottom-0 w-0.5"
      :class="statusBarClass"
      :aria-label="t('chat.conversationStatus', { status: statusLabel })"
    />
    <Avatar class="h-9 w-9 ring-2 ring-white/[0.1] light:ring-gray-200">
      <AvatarImage :src="props.contact.avatar_url" />
      <AvatarFallback :class="'text-xs bg-gradient-to-br text-white ' + props.getAvatarGradient(displayName)">
        {{ props.getInitials(displayName) }}
      </AvatarFallback>
    </Avatar>
    <div class="flex-1 min-w-0">
      <div class="flex items-center justify-between gap-2">
        <p
          class="flex-1 min-w-0 text-sm font-medium truncate text-white light:text-gray-900"
          :title="displayName"
        >
          {{ displayName }}
        </p>
        <span class="flex-shrink-0 text-[11px] text-white/40 light:text-gray-500">
          {{ props.formatTime(props.contact.last_message_at) }}
        </span>
      </div>
      <div class="flex items-center justify-between gap-2">
        <p class="flex-1 min-w-0 text-xs text-white/50 light:text-gray-500 truncate">
          {{ props.contact.last_message_preview || props.contact.phone_number }}
        </p>
        <Badge
          v-if="props.contact.unread_count > 0"
          class="flex-shrink-0 h-5 text-[10px] bg-emerald-500/20 text-emerald-400 light:bg-emerald-100 light:text-emerald-700"
        >
          {{ props.contact.unread_count }}
        </Badge>
      </div>
    </div>
  </div>
</template>
```

Nota: quando não há prévia (contato recém-criado, sem mensagens) a segunda linha cai de volta para o telefone — nenhuma linha fica vazia.

- [ ] **Step 4: Ligar no ChatView**

Em `frontend/src/views/chat/ChatView.vue`, adicionar aos imports do `<script setup>`:

```ts
import ConversationStatusFilter from '@/components/chat/ConversationStatusFilter.vue'
import ConversationListItem from '@/components/chat/ConversationListItem.vue'
```

Logo após o `</div>` que fecha o cabeçalho de busca (linha ~1751), inserir:

```vue
      <ConversationStatusFilter
        :model-value="contactsStore.statusFilter"
        :new-count="contactsStore.newCount"
        @update:model-value="contactsStore.setStatusFilter"
      />
```

Substituir todo o bloco `<div v-for="contact in contactsStore.sortedContacts" ...>...</div>` (linhas ~1756-1791) por:

```vue
          <ConversationListItem
            v-for="contact in contactsStore.sortedContacts"
            :key="contact.id"
            :contact="contact"
            :active="contactsStore.currentContact?.id === contact.id"
            :format-time="formatContactTime"
            :get-initials="getInitials"
            :get-avatar-gradient="getAvatarGradient"
            @click="handleContactClick(contact)"
          />
```

No `onMounted` do `ChatView` (~linha 451), junto das demais cargas iniciais:

```ts
  contactsStore.fetchStatusCounts()
```

- [ ] **Step 5: Verificar tipos e build**

Run: `cd frontend && npx vue-tsc --noEmit && npm run build`
Expected: sem erros de tipo; build conclui

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/chat/ConversationStatusFilter.vue frontend/src/components/chat/ConversationListItem.vue frontend/src/views/chat/ChatView.vue frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(chat): add status filter pills, status bar and message preview to conversation list"
```

---

### Task 9: Botão no cabeçalho do chat

**Files:**
- Modify: `frontend/src/views/chat/ChatView.vue:1875-1895` (grupo de ações do cabeçalho)

**Interfaces:**
- Consumes: `contactsStore.updateContactStatus` (Task 7), chaves de i18n (Task 8)
- Produces: nada de novo

- [ ] **Step 1: Adicionar estado e handler**

No `<script setup>` de `ChatView.vue`, junto dos demais refs de UI:

```ts
import { CheckCircle2, RotateCcw } from 'lucide-vue-next'

const isChangingStatus = ref(false)

const isConversationResolved = computed(
  () => contactsStore.currentContact?.contact_status === 'resolved'
)

async function toggleConversationStatus() {
  const contact = contactsStore.currentContact
  if (!contact || isChangingStatus.value) return

  const next = isConversationResolved.value ? 'in_progress' : 'resolved'
  isChangingStatus.value = true
  try {
    await contactsStore.updateContactStatus(contact.id, next)
    toast.success(next === 'resolved'
      ? t('chat.conversationResolved')
      : t('chat.conversationReopened'))
  } catch {
    toast.error(t('chat.statusChangeFailed'))
  } finally {
    isChangingStatus.value = false
  }
}
```

Reusar o `toast` e o `t` já importados no arquivo — se `toast` ainda não estiver em escopo, importar de `@/composables/useAppToast` seguindo o padrão já usado nas outras ações do `ChatView`.

- [ ] **Step 2: Adicionar o botão ao cabeçalho**

Imediatamente antes do bloco `<Tooltip>` do botão de notas (linha ~1890), inserir:

```vue
            <Button
              variant="ghost"
              size="sm"
              class="h-8 gap-1.5 px-2 text-xs font-medium"
              :class="isConversationResolved
                ? 'text-white/60 hover:text-white hover:bg-white/[0.08] light:text-gray-500 light:hover:text-gray-900 light:hover:bg-gray-100'
                : 'text-emerald-400 hover:text-emerald-300 hover:bg-emerald-500/10 light:text-emerald-600 light:hover:bg-emerald-50'"
              :disabled="isChangingStatus"
              @click="toggleConversationStatus"
            >
              <component :is="isConversationResolved ? RotateCcw : CheckCircle2" class="h-4 w-4" />
              <span>{{ isConversationResolved ? $t('chat.reopenConversation') : $t('chat.resolveConversation') }}</span>
            </Button>
```

O rótulo é visível (não só ícone) porque é ação de consequência, e o verbo do botão é o mesmo do toast: "Concluir atendimento" → "Atendimento concluído".

- [ ] **Step 3: Verificar tipos e build**

Run: `cd frontend && npx vue-tsc --noEmit && npm run build`
Expected: sem erros

- [ ] **Step 4: Commit**

```bash
git add frontend/src/views/chat/ChatView.vue
git commit -m "feat(chat): add resolve/reopen conversation button to chat header"
```

---

### Task 10: Evento WebSocket no frontend e teste e2e

**Files:**
- Modify: `frontend/src/services/websocket.ts:44-90` (constantes), `:217-290` (dispatch)
- Create: `frontend/e2e/contact-status.spec.ts`

**Interfaces:**
- Consumes: `contactsStore.applyStatusChange` (Task 7), evento `contact_status_changed` (Tasks 2 e 3)
- Produces: nada de novo

- [ ] **Step 1: Registrar o tipo e o dispatch**

Em `frontend/src/services/websocket.ts`, junto das demais constantes (após `WS_TYPE_STATUS_UPDATE`):

```ts
const WS_TYPE_CONTACT_STATUS_CHANGED = 'contact_status_changed'
```

No `switch` de `handleMessage`, após o `case WS_TYPE_STATUS_UPDATE`:

```ts
        case WS_TYPE_CONTACT_STATUS_CHANGED:
          store.applyStatusChange(message.payload)
          break
```

- [ ] **Step 2: Escrever o teste e2e**

`frontend/e2e/contact-status.spec.ts`, seguindo o padrão dos specs existentes em `frontend/e2e/`:

```ts
import { test, expect } from '@playwright/test'

test.describe('contact status', () => {
  test('filters the conversation list by status', async ({ page }) => {
    await page.goto('/chat')

    const list = page.getByRole('tablist')
    await expect(list).toBeVisible()

    await page.getByRole('tab', { name: /Em andamento|In progress/ }).click()
    await expect(page.getByRole('tab', { name: /Em andamento|In progress/ }))
      .toHaveAttribute('aria-selected', 'true')
  })

  test('resolves a conversation from the chat header', async ({ page }) => {
    await page.goto('/chat')

    // Open the first conversation in the list
    await page.locator('[data-testid="conversation-item"]').first().click()

    const resolveButton = page.getByRole('button', { name: /Concluir atendimento|Resolve conversation/ })
    await expect(resolveButton).toBeVisible()
    await resolveButton.click()

    // The button flips to the reopen action once the change lands
    await expect(page.getByRole('button', { name: /Reabrir atendimento|Reopen conversation/ }))
      .toBeVisible()
  })
})
```

Para o seletor funcionar, adicionar `data-testid="conversation-item"` à `<div>` raiz de `ConversationListItem.vue`.

- [ ] **Step 3: Rodar o e2e**

Run: `cd frontend && npx playwright test contact-status.spec.ts`
Expected: PASS. Se o ambiente e2e exigir seed/login, seguir exatamente o setup usado pelos specs vizinhos em `frontend/e2e/` antes de rodar.

- [ ] **Step 4: Rodar a verificação completa**

Run: `go build ./... && make test && cd frontend && npx vue-tsc --noEmit && npm run build`
Expected: tudo verde

- [ ] **Step 5: Commit**

```bash
git add frontend/src/services/websocket.ts frontend/src/components/chat/ConversationListItem.vue frontend/e2e/contact-status.spec.ts
git commit -m "feat(chat): apply contact_status_changed events live and add e2e coverage"
```

---

## Verificação final antes do PR

- [ ] `go build ./...` limpo
- [ ] `make test` sem falhas nem novos skips
- [ ] `make lint` limpo
- [ ] `cd frontend && npx vue-tsc --noEmit && npm run build` limpo
- [ ] Nenhum broadcast usando `map[string]any` como `WSMessage`
- [ ] `ContactResponse.status` continua retornando `"active"`
- [ ] Chaves de i18n presentes em `en.json` **e** `pt-BR.json`
- [ ] PR aberto com base em `development`
