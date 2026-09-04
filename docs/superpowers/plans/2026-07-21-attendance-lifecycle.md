# Ciclo de Vida do Atendimento — Ciclo 1 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Encerrar um atendimento — manual, por SLA ou por inatividade — passa a liberar o contato; e uma conversa iniciada por agente passa a abrir atendimento, para o chatbot não sequestrar a resposta do cliente.

**Architecture:** Um helper único `releaseContact` concentra a liberação e é chamado dos quatro caminhos de encerramento. O bug do chatbot é corrigido registrando o atendimento que hoje não existe: o envio de agente cria uma `AgentTransfer` ativa, reaproveitando `saveAndFinalizeTransfer`. Nenhum conceito novo, nenhuma coluna nova, nenhuma migração.

**Tech Stack:** Go 1.25+ + GORM + fastglue + Postgres; Vue 3 + Pinia + Tailwind.

**Spec:** `docs/superpowers/specs/2026-07-21-attendance-lifecycle-design.md`

## Global Constraints

- **Branch:** `feature/attendance-lifecycle`, PR com base em `development`. Nunca commitar em `main`.
- **Sem migração.** Nenhuma coluna nova, nenhum backfill, nenhuma liberação retroativa em massa. Contatos presos hoje são liberados no próximo encerramento.
- **Broadcast WebSocket:** sempre `websocket.WSMessage{Type: ..., Payload: ...}` com struct tipada. Nunca `map[string]any` como mensagem.
- **Os dois campos de atribuição são distintos e não podem ser confundidos:** `AgentTransfer.AgentID` = "quem atende agora"; `Contact.AssignedUserID` = "de quem é este cliente".
- **`ReturnAgentTransfersToQueue` só limpa `Contact.AssignedUserID` quando ele aponta para o agente removido** — essa regra conservadora deve ser preservada em qualquer código que a reaproveite.
- **O gatilho de atendimento iniciado por agente é `opts.SentByUserID != nil`.** Campanhas em massa não passam pelo `SendOutgoingMessage` e envios do chatbot não preenchem esse campo — nenhum dos dois pode abrir atendimento.
- **i18n:** toda string visível entra em `frontend/src/i18n/locales/en.json` **e** `pt-BR.json`.
- **Testes Go:**
  `export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/whatomate_test?sslmode=disable"`
  `export TEST_REDIS_URL="redis://localhost:6379"`
  Containers `whatc-pg` e `whatc-redis` já rodando no Docker.
- **Duas falhas pré-existentes** no repo, não são regressões: `TestApp_ServeMedia_RejectsSymlink` e `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`. Ignore-as.

---

### Task 1: Conversa iniciada por agente abre atendimento (correção do bug)

**Files:**
- Modify: `internal/models/constants.go:142-145` (fontes de transferência)
- Modify: `internal/handlers/agent_transfers.go` (novo helper após `createTransferToQueue`, ~linha 1258)
- Modify: `internal/handlers/messages.go:261` (dentro de `SendOutgoingMessage`)
- Test: `internal/handlers/agent_initiated_test.go`

**Interfaces:**
- Consumes: `saveAndFinalizeTransfer(transfer, account, contact, settings, endChatbotSession bool) error` e `hasActiveAgentTransfer(orgID, contactID) bool`, ambos já existentes
- Produces: `models.TransferSourceAgentInitiated`; `func (a *App) createAgentInitiatedTransfer(account *models.WhatsAppAccount, contact *models.Contact, agentID uuid.UUID)`

- [ ] **Step 1: Escrever o teste (falhando)**

Criar `internal/handlers/agent_initiated_test.go`:

```go
package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentInitiatedTransfer(t *testing.T) {
	t.Parallel()

	t.Run("agent send opens an attendance assigned to the sender", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)

		var transfer models.AgentTransfer
		require.NoError(t, app.DB.Where("contact_id = ? AND status = ?",
			contact.ID, models.TransferStatusActive).First(&transfer).Error)
		assert.Equal(t, models.TransferSourceAgentInitiated, transfer.Source)
		require.NotNil(t, transfer.AgentID)
		assert.Equal(t, user.ID, *transfer.AgentID)
	})

	t.Run("does not open a second attendance when one is active", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)
		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)

		var count int64
		app.DB.Model(&models.AgentTransfer{}).
			Where("contact_id = ? AND status = ?", contact.ID, models.TransferStatusActive).
			Count(&count)
		assert.Equal(t, int64(1), count)
	})

	t.Run("cancels an active chatbot session", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		session := models.ChatbotSession{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			ContactID:       contact.ID,
			WhatsAppAccount: account.Name,
			PhoneNumber:     contact.PhoneNumber,
			Status:          models.SessionStatusActive,
		}
		require.NoError(t, app.DB.Create(&session).Error)

		app.CreateAgentInitiatedTransferForTest(account, contact, user.ID)

		var stored models.ChatbotSession
		require.NoError(t, app.DB.First(&stored, "id = ?", session.ID).Error)
		assert.Equal(t, models.SessionStatusCancelled, stored.Status,
			"human intervention must win over the bot")
	})
}
```

Se `testutil.CreateTestWhatsAppAccount` tiver outra assinatura, conferir em `test/testutil/fixtures.go` e ajustar a chamada; a fixture existe (usada por `send_template_test.go`).

E o hook de teste em `internal/handlers/export_test.go`:

```go
// CreateAgentInitiatedTransferForTest exposes createAgentInitiatedTransfer.
func (a *App) CreateAgentInitiatedTransferForTest(account *models.WhatsAppAccount, contact *models.Contact, agentID uuid.UUID) {
	a.createAgentInitiatedTransfer(account, contact, agentID)
}
```

Adicionar `"github.com/shridarpatil/whatomate/internal/models"` aos imports de `export_test.go` se ainda não estiver lá (já está).

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestAgentInitiatedTransfer 2>&1 | head -5`
Expected: FAIL — `undefined: models.TransferSourceAgentInitiated` e `a.createAgentInitiatedTransfer undefined`

- [ ] **Step 3: Adicionar a fonte de transferência**

Em `internal/models/constants.go`, no bloco de `TransferSource`:

```go
	// TransferSourceAgentInitiated marks an attendance opened because an agent
	// messaged the customer first. Without this record the chatbot would take
	// over the customer's reply.
	TransferSourceAgentInitiated TransferSource = "agent_initiated"
```

- [ ] **Step 4: Implementar o helper**

Em `internal/handlers/agent_transfers.go`, após o fim de `createTransferToQueue`:

```go
// createAgentInitiatedTransfer opens an attendance when an agent messages a
// contact that has none. The system models "a human owns this conversation"
// solely as an active AgentTransfer, and no send path used to create one — so
// the chatbot hijacked the customer's reply.
//
// Deliberately does NOT suppress outside business hours, unlike
// createTransferToQueue: an agent messaging a customer at 11pm is a human
// choosing to work, not an automated handoff.
func (a *App) createAgentInitiatedTransfer(account *models.WhatsAppAccount, contact *models.Contact, agentID uuid.UUID) {
	if a.hasActiveAgentTransfer(account.OrganizationID, contact.ID) {
		return
	}

	settings, _ := a.getChatbotSettingsCached(account.OrganizationID, account.Name)

	transfer := models.AgentTransfer{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  account.OrganizationID,
		ContactID:       contact.ID,
		WhatsAppAccount: account.Name,
		PhoneNumber:     contact.PhoneNumber,
		Status:          models.TransferStatusActive,
		Source:          models.TransferSourceAgentInitiated,
		AgentID:         &agentID,
		TransferredAt:   time.Now(),
	}

	// endChatbotSession = true: human intervention wins over the bot.
	if err := a.saveAndFinalizeTransfer(&transfer, account, contact, settings, true); err != nil {
		a.Log.Error("Failed to open agent-initiated attendance",
			"error", err, "contact_id", contact.ID, "agent_id", agentID)
	}
}
```

- [ ] **Step 5: Ligar no caminho de envio**

Em `internal/handlers/messages.go`, dentro de `SendOutgoingMessage`, imediatamente após `a.updateContactLastMessage(req.Contact, preview)` (linha ~261):

```go
	// A human messaging the customer owns the conversation. Without an
	// attendance record the chatbot takes over the customer's reply — the
	// bug this fixes. Campaigns bypass this function entirely and chatbot
	// sends leave SentByUserID nil, so neither opens an attendance.
	if opts.SentByUserID != nil {
		a.createAgentInitiatedTransfer(req.Account, req.Contact, *opts.SentByUserID)
	}
```

- [ ] **Step 6: Rodar os testes**

Run: `go test ./internal/handlers/ -run TestAgentInitiatedTransfer -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS nos três subtestes

- [ ] **Step 7: Teste de regressão do bug relatado, pelo caminho real de envio**

Anexar a `internal/handlers/agent_initiated_test.go`:

```go
func TestSendTemplateOpensAttendance(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID,
		testutil.WithContactAccount(account.Name))
	tpl := createTestTemplate(t, app, org.ID, account.Name)

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":  contact.ID.String(),
		"template_id": tpl.ID.String(),
	})
	testutil.SetAuthContext(req, org.ID, user.ID)

	require.NoError(t, app.SendTemplateMessage(req))

	// The send itself is async and may fail against the fake Meta endpoint;
	// the attendance record is created synchronously and is what matters here.
	var count int64
	app.DB.Model(&models.AgentTransfer{}).
		Where("contact_id = ? AND status = ? AND source = ?",
			contact.ID, models.TransferStatusActive, models.TransferSourceAgentInitiated).
		Count(&count)
	assert.Equal(t, int64(1), count,
		"an agent-sent template must open an attendance so the bot does not hijack the reply")
}
```

`createTestTemplate` já existe em `internal/handlers/send_template_test.go` (mesmo pacote de teste). Conferir lá o formato exato do corpo aceito por `SendTemplateMessage` e ajustar as chaves do JSON se divergirem.

- [ ] **Step 8: Rodar e verificar build**

Run: `go build ./... && go test ./internal/handlers/ -run "TestAgentInitiated|TestSendTemplateOpensAttendance" -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS em todos

- [ ] **Step 9: Commit**

```bash
git add internal/models/constants.go internal/handlers/agent_transfers.go internal/handlers/messages.go internal/handlers/agent_initiated_test.go internal/handlers/export_test.go
git commit -m "fix(chat): open an attendance when an agent messages a contact

The chatbot is only skipped when an active AgentTransfer exists, and no agent
send path created one, so a conversation started by an agent let the flow
hijack the customer's reply."
```

---

### Task 2: `releaseContact` e o botão "Concluir atendimento"

**Files:**
- Modify: `internal/handlers/contact_status.go`
- Test: `internal/handlers/contact_status_test.go`

**Interfaces:**
- Consumes: `transitionContactStatus(contact, to, from, actorID) (bool, error)` já existente
- Produces: `func (a *App) releaseContact(contact *models.Contact, actorID *uuid.UUID, reason string) error`

- [ ] **Step 1: Escrever os testes (falhando)**

Anexar a `internal/handlers/contact_status_test.go`:

```go
func TestReleaseContact(t *testing.T) {
	t.Parallel()

	t.Run("clears the assigned agent and resolves the conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"contact_status":   models.ContactStatusInProgress,
		}).Error)
		contact.AssignedUserID = &agent.ID
		contact.ContactStatus = models.ContactStatusInProgress

		require.NoError(t, app.ReleaseContactForTest(contact, nil, "test"))

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
		assert.Nil(t, stored.AssignedUserID, "closing must not leave the contact pinned")
		assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus)
	})

	t.Run("is idempotent on an already free contact", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("contact_status", models.ContactStatusResolved).Error)
		contact.ContactStatus = models.ContactStatusResolved

		require.NoError(t, app.ReleaseContactForTest(contact, nil, "test"))

		var stored models.Contact
		require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
		assert.Nil(t, stored.AssignedUserID)
		assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus)
	})

	t.Run("records the actor when a person closed it", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		user := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		require.NoError(t, app.ReleaseContactForTest(contact, &user.ID, "manual close"))

		require.Eventually(t, func() bool {
			var count int64
			app.DB.Model(&models.AuditLog{}).
				Where("resource_type = ? AND resource_id = ? AND user_id = ?", "contact", contact.ID, user.ID).
				Count(&count)
			return count == 1
		}, 3*time.Second, 50*time.Millisecond)
	})
}

func TestUpdateContactStatus_ResolveReleasesContact(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", agent.ID).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"contact_status": "resolved"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())

	require.NoError(t, app.UpdateContactStatus(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.Contact
	require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Nil(t, stored.AssignedUserID, "resolving must free the contact")
}
```

E o hook em `internal/handlers/export_test.go`:

```go
// ReleaseContactForTest exposes releaseContact.
func (a *App) ReleaseContactForTest(contact *models.Contact, actorID *uuid.UUID, reason string) error {
	return a.releaseContact(contact, actorID, reason)
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run "TestReleaseContact|TestUpdateContactStatus_Resolve" 2>&1 | head -5`
Expected: FAIL — `a.releaseContact undefined`

- [ ] **Step 3: Implementar `releaseContact`**

Em `internal/handlers/contact_status.go`, após `transitionContactStatus`:

```go
// releaseContact frees a contact at the end of an attendance: it clears the
// relationship manager and marks the conversation resolved, so the next
// inbound message starts a fresh cycle through the flow instead of returning
// to whoever happened to serve it last.
//
// actorID is the user who closed the attendance, or nil for automatic closes
// (SLA, inactivity) — AuditLog.UserID is NOT NULL, so only actor-driven
// closes produce an audit entry.
//
// Idempotent: releasing an already-free contact is a no-op.
func (a *App) releaseContact(contact *models.Contact, actorID *uuid.UUID, reason string) error {
	if contact.AssignedUserID != nil {
		if err := a.DB.Model(&models.Contact{}).
			Where("id = ?", contact.ID).
			Update("assigned_user_id", nil).Error; err != nil {
			return err
		}
		contact.AssignedUserID = nil
	}

	if _, err := a.transitionContactStatus(contact, models.ContactStatusResolved, nil, actorID); err != nil {
		return err
	}

	a.Log.Info("Contact released", "contact_id", contact.ID, "reason", reason)
	return nil
}
```

- [ ] **Step 4: Usar no botão "Concluir atendimento"**

Em `internal/handlers/contact_status.go`, dentro de `UpdateContactStatus`, substituir o bloco:

```go
	if _, err := a.transitionContactStatus(contact, req.ContactStatus, nil, &userID); err != nil {
		a.Log.Error("Failed to update contact status", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact status", nil, "")
	}
```

por:

```go
	// Resolving is a close: it must free the contact, not merely flip a label.
	if req.ContactStatus == models.ContactStatusResolved {
		if err := a.releaseContact(contact, &userID, "manual resolve"); err != nil {
			a.Log.Error("Failed to release contact", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact status", nil, "")
		}
	} else if _, err := a.transitionContactStatus(contact, req.ContactStatus, nil, &userID); err != nil {
		a.Log.Error("Failed to update contact status", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update contact status", nil, "")
	}
```

- [ ] **Step 5: Rodar os testes**

Run: `go test ./internal/handlers/ -run "TestReleaseContact|TestUpdateContactStatus" -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS, incluindo os testes de `UpdateContactStatus` já existentes

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/contact_status.go internal/handlers/contact_status_test.go internal/handlers/export_test.go
git commit -m "feat(contacts): release the contact when an attendance is resolved"
```

---

### Task 3: Liberar no encerramento manual e no SLA

**Files:**
- Modify: `internal/handlers/agent_transfers.go:626-680` (`ResumeFromTransfer`)
- Modify: `internal/handlers/sla_processor.go:138-150` (`autoCloseExpiredTransfers`)
- Test: `internal/handlers/contact_status_test.go`

**Interfaces:**
- Consumes: `releaseContact(contact, actorID, reason) error` (Task 2)
- Produces: nada de novo — apenas comportamento

- [ ] **Step 1: Escrever os testes (falhando)**

Anexar a `internal/handlers/contact_status_test.go`:

```go
func TestResumeFromTransferReleasesContact(t *testing.T) {
	t.Parallel()

	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", agent.ID).Error)

	transfer := models.AgentTransfer{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		ContactID:      contact.ID,
		PhoneNumber:    contact.PhoneNumber,
		Status:         models.TransferStatusActive,
		Source:         models.TransferSourceManual,
		AgentID:        &agent.ID,
		TransferredAt:  time.Now(),
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", transfer.ID.String())

	require.NoError(t, app.ResumeFromTransfer(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.Contact
	require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Nil(t, stored.AssignedUserID, "closing an attendance must free the contact")
	assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus)
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestResumeFromTransferReleasesContact -v 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: FAIL — `assigned_user_id` continua preenchido

- [ ] **Step 3: Liberar no encerramento manual**

Em `internal/handlers/agent_transfers.go`, dentro de `ResumeFromTransfer`, substituir o bloco:

```go
	// Get contact for webhook data
	var contact models.Contact
	a.DB.Where("id = ?", transfer.ContactID).First(&contact)
```

por:

```go
	// Get contact for webhook data
	var contact models.Contact
	a.DB.Where("id = ?", transfer.ContactID).First(&contact)

	// Closing an attendance frees the contact: the next inbound message must
	// start a fresh cycle through the flow, not return to whoever served it last.
	if err := a.releaseContact(&contact, &userID, "attendance closed"); err != nil {
		a.Log.Error("Failed to release contact on close", "error", err, "contact_id", contact.ID)
	}
```

Manter a chamada existente `a.ClearContactChatbotTracking(transfer.ContactID)` — ela zera o rastreio de inatividade do bot e continua correta.

- [ ] **Step 4: Liberar no auto-close do SLA**

Em `internal/handlers/sla_processor.go`, dentro de `autoCloseExpiredTransfers`, logo após o `Updates` que marca a transferência como `expired` e antes de `closedCount++`:

```go
		// Same release rules as a manual close — an automatic close must not
		// leave the contact pinned to the agent who never answered.
		var contact models.Contact
		if err := p.app.DB.First(&contact, "id = ?", transfer.ContactID).Error; err == nil {
			if err := p.app.releaseContact(&contact, nil, "SLA auto-close"); err != nil {
				p.app.Log.Error("Failed to release contact on SLA auto-close",
					"error", err, "contact_id", contact.ID)
			}
		}
```

- [ ] **Step 5: Rodar os testes**

Run: `go test ./internal/handlers/ -run "TestResumeFromTransfer|TestReleaseContact" -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/agent_transfers.go internal/handlers/sla_processor.go internal/handlers/contact_status_test.go
git commit -m "feat(transfers): free the contact on manual close and SLA auto-close"
```

---

### Task 4: Inatividade encerra também atendimento humano

**Files:**
- Modify: `internal/handlers/sla_processor.go:466-502` (`processClientInactivity`)
- Test: `internal/handlers/sla_processor_test.go`

**Interfaces:**
- Consumes: `releaseContact` (Task 2)
- Produces: `func (p *SLAProcessor) closeInactiveAttendances(orgID uuid.UUID, settings models.ChatbotSettings, now time.Time)`

- [ ] **Step 1: Escrever o teste (falhando)**

Anexar a `internal/handlers/sla_processor_test.go` (seguir o padrão de construção de `SLAProcessor` já usado nesse arquivo; se ele expõe um construtor de teste, reutilizá-lo em vez de criar outro):

```go
func TestCloseInactiveAttendances(t *testing.T) {
	t.Parallel()

	t.Run("closes an attendance idle beyond the threshold and frees the contact", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		idle := time.Now().Add(-90 * time.Minute)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"last_message_at":  idle,
		}).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  idle,
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		app.CloseInactiveAttendancesForTest(org.ID, settings, time.Now())

		var storedTransfer models.AgentTransfer
		require.NoError(t, app.DB.First(&storedTransfer, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusExpired, storedTransfer.Status)

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.Nil(t, storedContact.AssignedUserID)
		assert.Equal(t, models.ContactStatusResolved, storedContact.ContactStatus)
	})

	t.Run("leaves a recently active attendance alone", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("last_message_at", time.Now().Add(-5*time.Minute)).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now(),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		app.CloseInactiveAttendancesForTest(org.ID, settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status)
	})

	t.Run("does nothing when auto-close is disabled", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("last_message_at", time.Now().Add(-10*time.Hour)).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now().Add(-10 * time.Hour),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 0

		app.CloseInactiveAttendancesForTest(org.ID, settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status)
	})
}
```

E o hook em `internal/handlers/export_test.go`:

```go
// CloseInactiveAttendancesForTest exposes the SLA processor's inactive-attendance
// pass without requiring the caller to build a processor.
func (a *App) CloseInactiveAttendancesForTest(orgID uuid.UUID, settings models.ChatbotSettings, now time.Time) {
	NewSLAProcessorForTest(a).closeInactiveAttendances(orgID, settings, now)
}
```

Conferir em `internal/handlers/sla_processor.go` como o `SLAProcessor` é construído em produção e adicionar, no mesmo `export_test.go`, um construtor de teste equivalente:

```go
// NewSLAProcessorForTest builds an SLAProcessor bound to this App.
func NewSLAProcessorForTest(a *App) *SLAProcessor {
	return &SLAProcessor{app: a}
}
```

Se o campo interno tiver outro nome, ajustar conforme a struct real.

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestCloseInactiveAttendances 2>&1 | head -5`
Expected: FAIL — `closeInactiveAttendances` indefinido

- [ ] **Step 3: Implementar a nova passada**

Em `internal/handlers/sla_processor.go`, após `processClientInactivity`:

```go
// closeInactiveAttendances closes human attendances with no message in either
// direction for AutoCloseMinutes, then applies the same release rules as a
// manual close.
//
// processClientInactivity cannot cover this: it selects on
// chatbot_last_message_at, which ClearContactChatbotTracking zeroes the moment
// a contact moves to a human — so attendances never entered that loop. Here the
// anchor is Contact.LastMessageAt, which is what "no interaction" means in a
// conversation between people.
func (p *SLAProcessor) closeInactiveAttendances(orgID uuid.UUID, settings models.ChatbotSettings, now time.Time) {
	if settings.ClientInactivity.AutoCloseMinutes <= 0 {
		return
	}

	threshold := now.Add(-time.Duration(settings.ClientInactivity.AutoCloseMinutes) * time.Minute)

	var transfers []models.AgentTransfer
	if err := p.app.DB.
		Joins("JOIN contacts ON contacts.id = agent_transfers.contact_id").
		Where("agent_transfers.organization_id = ? AND agent_transfers.status = ?",
			orgID, models.TransferStatusActive).
		Where("contacts.last_message_at IS NOT NULL AND contacts.last_message_at < ?", threshold).
		Find(&transfers).Error; err != nil {
		p.app.Log.Error("Failed to find inactive attendances", "error", err, "org_id", orgID)
		return
	}

	for i := range transfers {
		transfer := transfers[i]

		if settings.ClientInactivity.AutoCloseMessage != "" {
			p.sendSLATextToCustomer(transfer, "inactivity auto-close message", settings.ClientInactivity.AutoCloseMessage)
		}

		if err := p.app.DB.Model(&transfer).Updates(map[string]any{
			"status":     models.TransferStatusExpired,
			"resumed_at": now,
			"notes":      transfer.Notes + "\n[Auto-closed: no interaction within the inactivity window]",
		}).Error; err != nil {
			p.app.Log.Error("Failed to close inactive attendance", "error", err, "transfer_id", transfer.ID)
			continue
		}

		var contact models.Contact
		if err := p.app.DB.First(&contact, "id = ?", transfer.ContactID).Error; err == nil {
			if err := p.app.releaseContact(&contact, nil, "inactivity auto-close"); err != nil {
				p.app.Log.Error("Failed to release contact on inactivity close",
					"error", err, "contact_id", contact.ID)
			}
		}

		p.broadcastTransferUpdate(transfer, websocket.TypeTransferExpired)
	}
}
```

- [ ] **Step 4: Ligar no ciclo do processador**

Em `internal/handlers/sla_processor.go`, dentro de `processOrganizationSLA`, após o bloco 4 (`processClientInactivity`):

```go
	// 5. Close human attendances idle past the inactivity window
	p.closeInactiveAttendances(orgID, settings, now)
```

- [ ] **Step 5: Rodar os testes**

Run: `go test ./internal/handlers/ -run TestCloseInactiveAttendances -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS nos três subtestes

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/sla_processor.go internal/handlers/sla_processor_test.go internal/handlers/export_test.go
git commit -m "feat(sla): close idle human attendances and free the contact"
```

---

### Task 5: `PUT /api/chatbot/transfers/{id}/unassign`

**Files:**
- Modify: `internal/handlers/agent_transfers.go`
- Modify: `cmd/whatomate/main.go` (junto das rotas de transferência)
- Test: `internal/handlers/agent_transfers_test.go`

**Interfaces:**
- Consumes: nada das tasks anteriores
- Produces: `func (a *App) UnassignTransfer(r *fastglue.Request) error`, rota `PUT /api/chatbot/transfers/{id}/unassign`

- [ ] **Step 1: Escrever os testes (falhando)**

Anexar a `internal/handlers/agent_transfers_test.go`:

```go
func TestApp_UnassignTransfer(t *testing.T) {
	t.Parallel()

	t.Run("returns the attendance to the team queue", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", agent.ID).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now(),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", transfer.ID.String())

		require.NoError(t, app.UnassignTransfer(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Nil(t, stored.AgentID)
		assert.Equal(t, models.TransferStatusActive, stored.Status, "it stays open, just unassigned")

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.Nil(t, storedContact.AssignedUserID)
	})

	t.Run("preserves a relationship manager pointing at someone else", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		other := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", other.ID).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now(),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", transfer.ID.String())

		require.NoError(t, app.UnassignTransfer(req))

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		require.NotNil(t, storedContact.AssignedUserID)
		assert.Equal(t, other.ID, *storedContact.AssignedUserID,
			"unassigning one agent must not wipe another agent's relationship")
	})

	t.Run("denies a user without transfer write permission", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		user := testutil.CreateTestUser(t, app.DB, org.ID) // no role
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now(),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", transfer.ID.String())

		require.NoError(t, app.UnassignTransfer(req))
		assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	})
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestApp_UnassignTransfer 2>&1 | head -5`
Expected: FAIL — `app.UnassignTransfer undefined`

- [ ] **Step 3: Implementar o handler**

Em `internal/handlers/agent_transfers.go`, após `AssignAgentTransfer`:

```go
// UnassignTransfer removes the responsible agent from an attendance and returns
// it to the team queue, leaving the attendance itself open.
//
// The relationship manager on the contact is cleared only when it points at the
// agent being removed — the same conservative rule ReturnAgentTransfersToQueue
// uses, so a manually set manager is never wiped as a side effect.
func (a *App) UnassignTransfer(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	if !a.HasPermission(userID, models.ResourceTransfers, models.ActionWrite, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "You do not have permission to unassign transfers", nil, "")
	}

	transferID, err := parsePathUUID(r, "id", "transfer")
	if err != nil {
		return nil
	}

	transfer, err := findByIDAndOrg[models.AgentTransfer](a.DB, r, transferID, orgID, "Transfer")
	if err != nil {
		return nil
	}

	if transfer.Status != models.TransferStatusActive {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Transfer is not active", nil, "")
	}

	previousAgentID := transfer.AgentID
	if err := a.DB.Model(transfer).Update("agent_id", nil).Error; err != nil {
		a.Log.Error("Failed to unassign transfer", "error", err, "transfer_id", transfer.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to unassign transfer", nil, "")
	}
	transfer.AgentID = nil

	if previousAgentID != nil {
		a.DB.Model(&models.Contact{}).
			Where("id = ? AND assigned_user_id = ?", transfer.ContactID, *previousAgentID).
			Update("assigned_user_id", nil)
	}

	a.broadcastTransferAssigned(transfer)

	return r.SendEnvelope(map[string]any{
		"message": "Transfer returned to the team queue",
	})
}
```

- [ ] **Step 4: Registrar a rota**

Em `cmd/whatomate/main.go`, junto às demais rotas de transferência (procurar `"/api/transfers/{id}/assign"` e inserir logo abaixo):

```go
	g.PUT("/api/chatbot/transfers/{id}/unassign", app.UnassignTransfer)
```

- [ ] **Step 5: Rodar os testes**

Run: `go build ./... && go test ./internal/handlers/ -run TestApp_UnassignTransfer -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS nos três subtestes

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/agent_transfers.go internal/handlers/agent_transfers_test.go cmd/whatomate/main.go
git commit -m "feat(transfers): add explicit unassign returning the attendance to the queue"
```

---

### Task 6: Rótulos, semântica e validação do formulário

**Files:**
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`
- Modify: `frontend/src/views/settings/ChatbotSettingsView.vue`

**Interfaces:**
- Consumes: nada
- Produces: nada — apenas texto e validação

- [ ] **Step 1: Corrigir as strings**

Em `frontend/src/i18n/locales/pt-BR.json`, na seção que contém `allowQueuePickup` (~linha 1872):

```json
    "allowQueuePickup": "Permitir que agentes assumam atendimentos da fila",
    "allowQueuePickupDesc": "Quando ativado, os agentes podem assumir atendimentos disponíveis na fila da sua equipe",
```

Localizar as chaves de `assignToSameAgent` e do fechamento automático por inatividade no mesmo arquivo (`grep -n "assignToSameAgent\|clientAutoClose" frontend/src/i18n/locales/pt-BR.json`) e ajustar:

```json
    "assignToSameAgent": "Manter o mesmo agente durante o atendimento",
    "assignToSameAgentDesc": "Transferências dentro de um atendimento aberto voltam para o mesmo agente. A atribuição não sobrevive ao encerramento: ao encerrar, o contato é liberado e uma nova mensagem inicia um novo atendimento.",
    "clientAutoCloseMinutes": "Encerrar após (minutos) de inatividade",
    "clientAutoCloseMinutesDesc": "Contado a partir da última mensagem da conversa, em qualquer direção. Deve ser maior que o tempo do lembrete.",
```

Em `frontend/src/i18n/locales/en.json`, nas mesmas chaves:

```json
    "allowQueuePickup": "Allow Agents to Take Attendances from the Queue",
    "allowQueuePickupDesc": "When enabled, agents can take available attendances from their team's queue",
    "assignToSameAgent": "Keep the same agent during an attendance",
    "assignToSameAgentDesc": "Transfers within an open attendance return to the same agent. The assignment does not survive closing: closing frees the contact, and a new message starts a new attendance.",
    "clientAutoCloseMinutes": "Close after (minutes) of inactivity",
    "clientAutoCloseMinutesDesc": "Measured from the last message in the conversation, in either direction. Must be greater than the reminder time.",
```

Se alguma dessas chaves ainda não existir, criá-la e referenciá-la no template do passo 2. Se existir com outro nome, manter o nome existente e trocar apenas o valor — não renomear chaves em uso.

- [ ] **Step 2: Validar a ordem dos tempos**

Em `frontend/src/views/settings/ChatbotSettingsView.vue`, no `<script setup>`:

```ts
const inactivityError = computed(() => {
  const reminder = settings.value.client_reminder_minutes
  const close = settings.value.client_auto_close_minutes
  if (!close || !reminder) return ''
  // A close time at or below the reminder means the reminder is never sent.
  return close <= reminder ? t('settings.chatbot.inactivityOrderError') : ''
})
```

Ajustar os nomes `settings.value.*` aos usados no arquivo (conferir com `grep -n "client_auto_close_minutes" frontend/src/views/settings/ChatbotSettingsView.vue`).

Exibir a mensagem abaixo do campo de encerramento e desabilitar o botão de salvar quando `inactivityError` não estiver vazio, seguindo o padrão de validação já usado nessa tela.

Adicionar a string em ambos os locales:

```json
    "inactivityOrderError": "O tempo de encerramento deve ser maior que o do lembrete."
```

```json
    "inactivityOrderError": "The close time must be greater than the reminder time."
```

- [ ] **Step 3: Verificar tipos e build**

Run: `cd frontend && npx vue-tsc --noEmit 2>&1 | grep -v AccountDetailView; npm run build 2>&1 | grep -E "error|built in"`
Expected: sem erros novos; o único erro de tipo é o pré-existente em `AccountDetailView.vue`

- [ ] **Step 4: Validar o JSON dos locales**

Run: `cd frontend && node -e "JSON.parse(require('fs').readFileSync('src/i18n/locales/en.json','utf8'));JSON.parse(require('fs').readFileSync('src/i18n/locales/pt-BR.json','utf8'));console.log('JSON OK')"`
Expected: `JSON OK`

- [ ] **Step 5: Commit**

```bash
git add frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json frontend/src/views/settings/ChatbotSettingsView.vue
git commit -m "feat(settings): correct attendance setting labels and validate inactivity order"
```

---

## Verificação final antes do PR

- [ ] `go build ./...` limpo
- [ ] `go vet ./...` limpo
- [ ] `go test -p 1 ./...` — apenas as duas falhas pré-existentes
- [ ] `cd frontend && npx vue-tsc --noEmit` — apenas o erro pré-existente em `AccountDetailView.vue`
- [ ] `cd frontend && npm run build` limpo
- [ ] Nenhuma migração, nenhuma coluna nova, nenhum backfill
- [ ] Campanha em massa continua **não** abrindo atendimento (envio do worker não passa pelo `SendOutgoingMessage`)
- [ ] Envio do chatbot continua **não** abrindo atendimento (`SentByUserID == nil`)
- [ ] Chaves de i18n presentes em `en.json` **e** `pt-BR.json`
- [ ] PR aberto com base em `development`
