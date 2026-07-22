# Visibilidade de Conversas — Ciclo 2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Uma conversa atribuída a um agente é visível e acionável apenas por esse agente, mais supervisores/managers/admins via `conversations:view_all` — imposto na API (403, não só escondido) e refletido na UI, configurável por empresa.

**Architecture:** Uma decisão de autorização central (`authorizeConversation`) projeta `canView`/`canInteract`; `scopeVisibleConversations` é exclusivamente a tradução SQL de `canView`, blindada por um teste-oráculo. Uma flag por org (`strict_conversation_visibility`, default false) liga a regra estrita; desligada, o comportamento é idêntico ao de hoje.

**Tech Stack:** Go 1.25+ + GORM + fastglue + Postgres; Vue 3 + Pinia + Tailwind.

**Spec:** `docs/superpowers/specs/2026-07-21-conversation-visibility-design.md`

## Global Constraints

- **Branch:** `feature/conversation-visibility`, PR com base em `development`. Nunca commitar em `main`.
- **Compatibilidade:** com `strict_conversation_visibility = false` (default), o comportamento é **idêntico** ao atual — `scopeVisibleConversations` reproduz `scopeAssignedContact`. Todo task tem um teste de regressão da flag desligada.
- **Uma fonte de verdade:** a regra vive só em `authorizeConversation`. `scopeVisibleConversations` é a tradução SQL de `canView` e nada mais; um teste-oráculo garante que as duas concordam.
- **Precedência (invariante):** `Contact.AssignedUserID` só é consultado quando **não há `AgentTransfer` ativa** para o contato. Transferência ativa sempre vence.
- **Permissão nova:** `conversations:view_all`. Não sobrecarregar `contacts:read`.
- **Migração:** aditiva apenas — uma coluna (`strict_conversation_visibility`, default false) + seed idempotente da permissão. Sem backfill destrutivo.
- **RBAC existente:** `admin` recebe automaticamente toda permissão de `DefaultPermissions()`; `manager` recebe as listadas em `managerPermissions`; `agent` só as de `agentPermissions`.
- **i18n:** toda string visível em `en.json` **e** `pt-BR.json`.
- **Testes Go:**
  `export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/whatomate_test?sslmode=disable"`
  `export TEST_REDIS_URL="redis://localhost:6379"`
  Containers `whatc-pg` e `whatc-redis` já rodando no Docker.
- **Falhas pré-existentes** (não são regressões): `TestApp_ServeMedia_RejectsSymlink`, `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`, e o flaky `TestApp_RegisterPhoneNumber_Success_GeneratedPIN` (passa isolado).

---

## ETAPA 1 — Infraestrutura

### Task 1: Permissão `conversations:view_all`

**Files:**
- Modify: `internal/models/roles.go`
- Test: `internal/models/models_test.go` (ou `internal/database/database_test.go` para o seed)

**Interfaces:**
- Consumes: nada
- Produces: `models.ResourceConversations = "conversations"`, `models.ActionViewAll = "view_all"`; a permissão `conversations:view_all` em `DefaultPermissions()` e em `managerPermissions`

- [ ] **Step 1: Escrever o teste (falhando)**

Em `internal/models/models_test.go`:

```go
func TestConversationsViewAllPermission(t *testing.T) {
	// The permission must exist in the default set.
	found := false
	for _, p := range models.DefaultPermissions() {
		if p.Resource == models.ResourceConversations && p.Action == models.ActionViewAll {
			found = true
		}
	}
	assert.True(t, found, "conversations:view_all must be a default permission")

	roles := models.SystemRolePermissions()
	// admin gets every default permission automatically.
	assert.Contains(t, roles["admin"], "conversations:view_all")
	// manager is a supervisor and must see all conversations.
	assert.Contains(t, roles["manager"], "conversations:view_all")
	// agent must NOT — that is the whole point.
	assert.NotContains(t, roles["agent"], "conversations:view_all")
}
```

Se `models_test.go` não existir ou não importar `assert`, seguir o padrão de import dos testes vizinhos do pacote `models`.

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/models/ -run TestConversationsViewAllPermission 2>&1 | head -5`
Expected: FAIL — `undefined: models.ResourceConversations`

- [ ] **Step 3: Adicionar o recurso e a ação**

Em `internal/models/roles.go`, no bloco de constantes de recurso (após `ResourceCannedResponses`, ~linha 80):

```go
	ResourceConversations           = "conversations"
```

E no bloco de constantes de ação (após `ActionAssign`, ~linha 100):

```go
	ActionViewAll = "view_all"
```

- [ ] **Step 4: Declarar a permissão e atribuí-la**

Em `internal/models/roles.go`, dentro de `DefaultPermissions()`, junto das demais (ex.: após as de `contacts`):

```go
		// Conversations
		{Resource: ResourceConversations, Action: ActionViewAll, Description: "View and act on all conversations, including those assigned to other agents"},
```

E em `managerPermissions`, junto do grupo de chat/contatos:

```go
		"conversations:view_all",
```

`admin` recebe automaticamente (monta a partir de `DefaultPermissions()`). `agent` **não** recebe — não adicionar.

- [ ] **Step 5: Rodar os testes**

Run: `go test ./internal/models/ -run TestConversationsViewAllPermission -v 2>&1 | grep -E "^(--- |ok|FAIL)"`
Expected: PASS

- [ ] **Step 6: Verificar o seed idempotente**

O seed de permissões (`SeedPermissionsAndRoles`) já roda em migração e cria permissões faltantes. Confirmar build:
Run: `go build ./...`
Expected: sem saída

- [ ] **Step 7: Commit**

```bash
git add internal/models/roles.go internal/models/models_test.go
git commit -m "feat(rbac): add conversations:view_all permission for supervisors"
```

---

### Task 2: Flag por empresa `strict_conversation_visibility`

**Files:**
- Modify: `internal/models/chatbot.go` (`AgentAssignmentConfig`)
- Modify: `internal/handlers/chatbot.go` (read + write da API de settings)
- Test: `internal/handlers/chatbot_test.go`

**Interfaces:**
- Consumes: nada
- Produces: campo `models.AgentAssignmentConfig.StrictConversationVisibility bool`; a chave JSON `strict_conversation_visibility` na resposta e no update de settings

- [ ] **Step 1: Escrever o teste (falhando)**

Em `internal/handlers/chatbot_test.go`:

```go
func TestChatbotSettings_StrictVisibilityRoundTrip(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	// Default is false.
	getReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(getReq, org.ID, user.ID)
	require.NoError(t, app.GetChatbotSettings(getReq))
	var getResp struct {
		Data struct {
			Settings struct {
				StrictConversationVisibility bool `json:"strict_conversation_visibility"`
			} `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(getReq), &getResp))
	assert.False(t, getResp.Data.Settings.StrictConversationVisibility, "default must be false")

	// Turn it on.
	putReq := testutil.NewJSONRequest(t, map[string]any{"strict_conversation_visibility": true})
	testutil.SetAuthContext(putReq, org.ID, user.ID)
	require.NoError(t, app.UpdateChatbotSettings(putReq))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(putReq))

	var stored models.ChatbotSettings
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).First(&stored).Error)
	assert.True(t, stored.AgentAssignment.StrictConversationVisibility)
}
```

Ajustar o nome do campo na struct de resposta/request se `GetChatbotSettings` usar uma struct nomeada diferente — conferir com `grep -n "AllowAgentQueuePickup" internal/handlers/chatbot.go` e espelhar.

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestChatbotSettings_StrictVisibilityRoundTrip 2>&1 | head -5`
Expected: FAIL — campo desconhecido / permanece false

- [ ] **Step 3: Adicionar o campo ao modelo**

Em `internal/models/chatbot.go`, na struct `AgentAssignmentConfig`, junto de `CloseInactiveAttendances` (nota: `CloseInactiveAttendances` está em `ClientInactivityConfig`; a nova flag é de atribuição/visibilidade e vai em `AgentAssignmentConfig`):

```go
	// StrictConversationVisibility gates per-agent conversation visibility.
	// Default false: behaviour is unchanged from before this feature. When true,
	// an assigned conversation is visible/actionable only by the assigned agent
	// plus users with conversations:view_all.
	StrictConversationVisibility bool `gorm:"column:strict_conversation_visibility;default:false" json:"strict_conversation_visibility"`
```

- [ ] **Step 4: Expor na leitura da API**

Em `internal/handlers/chatbot.go`, na struct de resposta de settings (onde estão `AllowAgentQueuePickup`, `AssignToSameAgent`), adicionar o campo:

```go
	StrictConversationVisibility bool `json:"strict_conversation_visibility"`
```

e preenchê-lo na montagem (junto de `AssignToSameAgent: settings.AgentAssignment.AssignToSameAgent`):

```go
	StrictConversationVisibility: settings.AgentAssignment.StrictConversationVisibility,
```

E no map de resposta alternativo (onde há `"assign_to_same_agent": s.AgentAssignment.AssignToSameAgent`):

```go
	"strict_conversation_visibility": s.AgentAssignment.StrictConversationVisibility,
```

- [ ] **Step 5: Aceitar no update**

Em `internal/handlers/chatbot.go`, na struct do request de `UpdateChatbotSettings` (onde há `AssignToSameAgent *bool`):

```go
	StrictConversationVisibility *bool `json:"strict_conversation_visibility"`
```

e aplicar, junto dos demais campos de `AgentAssignment`:

```go
	if req.StrictConversationVisibility != nil {
		settings.AgentAssignment.StrictConversationVisibility = *req.StrictConversationVisibility
	}
```

Se houver um flag `*Touched` que decide persistir a seção de assignment, incluir `req.StrictConversationVisibility != nil` nele — espelhar o que `AssignToSameAgent` faz.

- [ ] **Step 6: Rodar os testes**

Run: `go build ./... && go test ./internal/handlers/ -run TestChatbotSettings_StrictVisibilityRoundTrip -v 2>&1 | grep -E "^(--- |ok|FAIL)"`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/models/chatbot.go internal/handlers/chatbot.go internal/handlers/chatbot_test.go
git commit -m "feat(settings): add per-org strict_conversation_visibility flag (default off)"
```

---

## ETAPA 2 — Autorização (a regra central)

### Task 3: `authorizeConversation` + `canView` / `canInteract`

**Files:**
- Create: `internal/handlers/conversation_visibility.go`
- Test: `internal/handlers/conversation_visibility_test.go`

**Interfaces:**
- Consumes: `models.AgentAssignmentConfig.StrictConversationVisibility` (Task 2); `conversations:view_all` (Task 1); `a.getChatbotSettingsCached(orgID, "")`; `a.HasPermission`
- Produces:
  - `type conversationAccess struct { canView, canInteract bool }`
  - `func (a *App) authorizeConversation(userID, orgID uuid.UUID, contact *models.Contact) conversationAccess`
  - `func (a *App) canViewConversation(userID, orgID uuid.UUID, contact *models.Contact) bool`
  - `func (a *App) canInteractWithConversation(userID, orgID uuid.UUID, contact *models.Contact) bool`

- [ ] **Step 1: Escrever os testes da matriz (falhando)**

Criar `internal/handlers/conversation_visibility_test.go`. Helper para ligar a flag numa org e criar transferências:

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

// enableStrictVisibility flips the org flag on and clears the settings cache.
func enableStrictVisibility(t *testing.T, app *handlers.App, orgID uuid.UUID) {
	t.Helper()
	require.NoError(t, app.DB.Model(&models.ChatbotSettings{}).
		Where("organization_id = ?", orgID).
		Update("strict_conversation_visibility", true).Error)
	app.InvalidateChatbotSettingsCacheForTest(orgID)
}

// activeTransfer creates an active transfer for a contact.
func activeTransfer(t *testing.T, app *handlers.App, orgID, contactID uuid.UUID, agentID, teamID *uuid.UUID) {
	t.Helper()
	require.NoError(t, app.DB.Create(&models.AgentTransfer{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		ContactID:      contactID,
		PhoneNumber:    "+550000",
		Status:         models.TransferStatusActive,
		Source:         models.TransferSourceManual,
		AgentID:        agentID,
		TeamID:         teamID,
	}).Error)
}

func TestAuthorizeConversation(t *testing.T) {
	t.Parallel()

	t.Run("flag off preserves current behaviour", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		activeTransfer(t, app, org.ID, contact.ID, &agent.ID, nil)

		// Flag off: an "other" agent with contacts:read still sees it (today's behaviour).
		assert.True(t, app.CanViewConversationForTest(other.ID, org.ID, contact))
	})

	t.Run("strict: assigned agent sees and interacts, other agent does not", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := createAgentRole(t, app, org.ID) // agent role: no view_all
		agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		activeTransfer(t, app, org.ID, contact.ID, &agent.ID, nil)
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(agent.ID, org.ID, contact))
		assert.True(t, app.CanInteractWithConversationForTest(agent.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(other.ID, org.ID, contact))
		assert.False(t, app.CanInteractWithConversationForTest(other.ID, org.ID, contact))
	})

	t.Run("strict: view_all sees any conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := createAgentRole(t, app, org.ID)
		managerRole := testutil.CreateAdminRole(t, app.DB, org.ID) // admin role has all perms incl view_all
		agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		manager := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&managerRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		activeTransfer(t, app, org.ID, contact.ID, &agent.ID, nil)
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(manager.ID, org.ID, contact))
	})

	t.Run("strict: team queue visible to team members only", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := createAgentRole(t, app, org.ID)
		member := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		outsider := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		team := createTeamWithMember(t, app, org.ID, member.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		activeTransfer(t, app, org.ID, contact.ID, nil, &team.ID) // queued to team, no agent
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(member.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(outsider.ID, org.ID, contact))
	})

	t.Run("strict: general queue (no team) visible to authorized agents", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := createAgentRole(t, app, org.ID)
		anyAgent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		activeTransfer(t, app, org.ID, contact.ID, nil, nil) // general queue
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(anyAgent.ID, org.ID, contact))
	})

	t.Run("strict: carteira governs only without an active transfer", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := createAgentRole(t, app, org.ID)
		owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", owner.ID).Error)
		contact.AssignedUserID = &owner.ID
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(owner.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(other.ID, org.ID, contact))
	})

	t.Run("strict: active transfer wins over carteira (precedence)", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := createAgentRole(t, app, org.ID)
		serving := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		carteira := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", carteira.ID).Error)
		contact.AssignedUserID = &carteira.ID
		activeTransfer(t, app, org.ID, contact.ID, &serving.ID, nil)
		enableStrictVisibility(t, app, org.ID)

		// The active transfer's agent governs; the carteira agent does not.
		assert.True(t, app.CanViewConversationForTest(serving.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(carteira.ID, org.ID, contact))
	})
}
```

E os helpers de teste em `internal/handlers/export_test.go`:

```go
// CanViewConversationForTest / CanInteractWithConversationForTest expose the
// authorization functions to the external test package.
func (a *App) CanViewConversationForTest(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.canViewConversation(userID, orgID, contact)
}
func (a *App) CanInteractWithConversationForTest(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.canInteractWithConversation(userID, orgID, contact)
}

// InvalidateChatbotSettingsCacheForTest clears the cached settings so a flag
// flip is seen immediately.
func (a *App) InvalidateChatbotSettingsCacheForTest(orgID uuid.UUID) {
	a.invalidateChatbotSettingsCache(orgID) // use the real cache-invalidation method name
}
```

Conferir o nome real do método de invalidação de cache de settings: `grep -n "func (a \*App).*[Ii]nvalidate.*[Cc]hatbot\|chatbotSettingsCache" internal/handlers/cache.go`. Se o cache não tiver invalidação pública, escrever a flag **antes** de qualquer leitura que popule o cache (criar org, ligar flag, só então chamar as funções) e ajustar o helper para no-op.

Os helpers `createAgentRole` e `createTeamWithMember`: conferir se já existem em `testutil` (`grep -rn "func CreateAgentRole\|func CreateTeam" test/testutil/`). Se não, criá-los em `internal/handlers/conversation_visibility_test.go` como helpers locais: `createAgentRole` cria um `CustomRole` com exatamente as permissões de `agentPermissions` (sem `view_all`); `createTeamWithMember` cria um `Team` e um `TeamMember` ligando o usuário.

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestAuthorizeConversation 2>&1 | head -5`
Expected: FAIL — `a.canViewConversation undefined`

- [ ] **Step 3: Implementar a decisão central**

Criar `internal/handlers/conversation_visibility.go`:

```go
package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// conversationAccess is the single authorization decision for a conversation,
// computed once from the contact's state. canView and canInteract are separate
// concepts even though cycle 2 derives both from the same rule — see the spec
// section "A função central".
type conversationAccess struct {
	canView     bool
	canInteract bool
}

// authorizeConversation is the ONLY place the visibility rule lives.
//
// Precedence invariant: Contact.AssignedUserID (carteira) is consulted only
// when there is no active AgentTransfer for the contact. An active transfer
// always wins, so a queued/closed/transferred conversation is never governed
// by a stale carteira pointer.
func (a *App) authorizeConversation(userID, orgID uuid.UUID, contact *models.Contact) conversationAccess {
	settings, _ := a.getChatbotSettingsCached(orgID, "")

	// Flag off (default): preserve today's behaviour exactly — contacts:read
	// sees all, otherwise only own/assigned. Mirror scopeAssignedContact.
	if settings == nil || !settings.AgentAssignment.StrictConversationVisibility {
		if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
			return conversationAccess{canView: true, canInteract: true}
		}
		ok := a.userOwnsContact(userID, orgID, contact)
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// Strict mode.
	// Supervisors/managers/admins with view_all always pass.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return conversationAccess{canView: true, canInteract: true}
	}

	// Is there an active transfer? It is the primary authority.
	transfer, hasActive := a.activeTransferFor(orgID, contact.ID)
	if hasActive {
		switch {
		case transfer.AgentID != nil:
			ok := *transfer.AgentID == userID
			return conversationAccess{canView: ok, canInteract: ok}
		case transfer.TeamID != nil:
			ok := a.userInTeam(userID, *transfer.TeamID)
			return conversationAccess{canView: ok, canInteract: ok}
		default:
			// General queue (no team): any authorized agent.
			return conversationAccess{canView: true, canInteract: true}
		}
	}

	// No active transfer: carteira governs, if set.
	if contact.AssignedUserID != nil {
		ok := *contact.AssignedUserID == userID
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// No transfer, no carteira: general pool, authorized agents.
	return conversationAccess{canView: true, canInteract: true}
}

func (a *App) canViewConversation(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.authorizeConversation(userID, orgID, contact).canView
}

func (a *App) canInteractWithConversation(userID, orgID uuid.UUID, contact *models.Contact) bool {
	return a.authorizeConversation(userID, orgID, contact).canInteract
}

// activeTransferFor returns the contact's active transfer, if any.
func (a *App) activeTransferFor(orgID, contactID uuid.UUID) (models.AgentTransfer, bool) {
	var t models.AgentTransfer
	err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
		orgID, contactID, models.TransferStatusActive).
		Order("transferred_at DESC").First(&t).Error
	if err != nil {
		return models.AgentTransfer{}, false
	}
	return t, true
}

// userInTeam reports whether the user is a member of the team.
func (a *App) userInTeam(userID, teamID uuid.UUID) bool {
	var count int64
	a.DB.Model(&models.TeamMember{}).
		Where("team_id = ? AND user_id = ?", teamID, userID).
		Count(&count)
	return count > 0
}

// userOwnsContact mirrors the old scopeAssignedContact "mine" condition, for
// the flag-off path: the contact is assigned to the user, or an active transfer
// is assigned to them.
func (a *App) userOwnsContact(userID, orgID uuid.UUID, contact *models.Contact) bool {
	if contact.AssignedUserID != nil && *contact.AssignedUserID == userID {
		return true
	}
	var count int64
	a.DB.Model(&models.AgentTransfer{}).
		Where("organization_id = ? AND contact_id = ? AND agent_id = ? AND status = ?",
			orgID, contact.ID, userID, models.TransferStatusActive).
		Count(&count)
	return count > 0
}
```

Conferir o nome exato de `getChatbotSettingsCached` (assinatura `(orgID, accountName)`), já usado em `sla_processor.go` e `agent_transfers.go`.

- [ ] **Step 4: Rodar os testes**

Run: `go test ./internal/handlers/ -run TestAuthorizeConversation -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS em todos os subtestes

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/conversation_visibility.go internal/handlers/conversation_visibility_test.go internal/handlers/export_test.go
git commit -m "feat(visibility): central conversation authorization decision"
```

---

### Task 4: `scopeVisibleConversations` + teste-oráculo

**Files:**
- Modify: `internal/handlers/conversation_visibility.go`
- Test: `internal/handlers/conversation_visibility_test.go`

**Interfaces:**
- Consumes: a decisão de `authorizeConversation` (Task 3)
- Produces: `func (a *App) scopeVisibleConversations(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB`

- [ ] **Step 1: Escrever o teste-oráculo (falhando)**

Anexar a `internal/handlers/conversation_visibility_test.go`:

```go
// TestVisibilityScopeMatchesFunction is the anti-divergence guard: the SQL scope
// must return exactly the contacts for which canViewConversation is true.
func TestVisibilityScopeMatchesFunction(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := createAgentRole(t, app, org.ID)
	viewer := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	otherAgent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	team := createTeamWithMember(t, app, org.ID, viewer.ID)

	// One contact per branch of the tree.
	assignedToViewer := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, assignedToViewer.ID, &viewer.ID, nil)

	assignedToOther := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, assignedToOther.ID, &otherAgent.ID, nil)

	teamQueue := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, teamQueue.ID, nil, &team.ID)

	generalQueue := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, generalQueue.ID, nil, nil)

	carteira := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(carteira).Update("assigned_user_id", otherAgent.ID).Error)

	idle := testutil.CreateTestContact(t, app.DB, org.ID) // no transfer, no carteira

	enableStrictVisibility(t, app, org.ID)

	// All contacts in the org.
	all := []*models.Contact{assignedToViewer, assignedToOther, teamQueue, generalQueue, carteira, idle}

	// Expected set per the function.
	expected := map[uuid.UUID]bool{}
	for _, c := range all {
		var fresh models.Contact
		require.NoError(t, app.DB.First(&fresh, "id = ?", c.ID).Error)
		if app.CanViewConversationForTest(viewer.ID, org.ID, &fresh) {
			expected[c.ID] = true
		}
	}

	// Actual set from the SQL scope.
	var visible []models.Contact
	q := app.ScopeVisibleConversationsForTest(
		app.DB.Where("organization_id = ?", org.ID), viewer.ID, org.ID)
	require.NoError(t, q.Find(&visible).Error)

	got := map[uuid.UUID]bool{}
	for i := range visible {
		got[visible[i].ID] = true
	}

	assert.Equal(t, expected, got,
		"scopeVisibleConversations must return exactly the contacts canViewConversation allows")
}
```

E o hook em `export_test.go`:

```go
func (a *App) ScopeVisibleConversationsForTest(q *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	return a.scopeVisibleConversations(q, userID, orgID)
}
```

Adicionar `"gorm.io/gorm"` aos imports de `export_test.go` se necessário.

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestVisibilityScopeMatchesFunction 2>&1 | head -5`
Expected: FAIL — `a.scopeVisibleConversations undefined`

- [ ] **Step 3: Implementar a tradução SQL**

Anexar a `internal/handlers/conversation_visibility.go`:

```go
// scopeVisibleConversations is the SQL translation of authorizeConversation.canView
// (see spec §"A função central"). It must return exactly the contacts for which
// canViewConversation is true — TestVisibilityScopeMatchesFunction guards that.
// It replaces scopeAssignedContact at every listing/read site.
func (a *App) scopeVisibleConversations(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	settings, _ := a.getChatbotSettingsCached(orgID, "")

	// Flag off: preserve scopeAssignedContact exactly.
	if settings == nil || !settings.AgentAssignment.StrictConversationVisibility {
		if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
			return query
		}
		return query.Where("assigned_user_id = ? OR id IN (?)",
			userID,
			a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
				Where("agent_id = ? AND organization_id = ? AND status = ?",
					userID, orgID, models.TransferStatusActive),
		)
	}

	// Strict: view_all sees everything.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return query
	}

	// A contact is visible when, considering only its LATEST active transfer:
	//   - that transfer is assigned to the user, OR
	//   - that transfer is a team queue whose team the user belongs to, OR
	//   - that transfer is the general queue (no team), OR
	//   - there is NO active transfer and (carteira == user OR no carteira).
	//
	// "latest active transfer" mirrors activeTransferFor's Order(transferred_at DESC).
	// Expressed as: the contact has NO active transfer OTHER than ones the user
	// may see, is a delicate SQL. Simpler and provably equivalent to the function:
	// a contact is visible iff it has an active transfer the user may see, OR it
	// has no active transfer and the carteira rule passes.

	activeSub := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ?", orgID, models.TransferStatusActive)

	// contact_ids with an active transfer the user MAY see.
	visibleTransferSub := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ?", orgID, models.TransferStatusActive).
		Where(`
			agent_id = ?
			OR (agent_id IS NULL AND team_id IS NULL)
			OR (agent_id IS NULL AND team_id IN (?))
		`,
			userID,
			a.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID),
		)

	return query.Where(`
		id IN (?)
		OR (id NOT IN (?) AND (assigned_user_id IS NULL OR assigned_user_id = ?))
	`,
		visibleTransferSub,
		activeSub,
		userID,
	)
}
```

**Nota sobre a "latest active transfer".** A função usa a transferência ativa mais recente; a SQL acima considera "existe alguma transferência ativa que o usuário pode ver". Se o modelo permitir **mais de uma** transferência ativa por contato ao mesmo tempo, as duas podem divergir e o teste-oráculo vai acusar. Nesse caso, restringir ambas (função e SQL) à transferência mais recente — mas o Ciclo 1 trata "uma transferência ativa por contato" como invariante (`hasActiveAgentTransfer` bloqueia criar uma segunda). Se o teste-oráculo passar, o invariante vale na prática; se falhar, ajustar a SQL para casar com o `Order(transferred_at DESC).First` da função.

- [ ] **Step 4: Rodar o teste-oráculo e a matriz**

Run: `go test ./internal/handlers/ -run "TestVisibilityScopeMatchesFunction|TestAuthorizeConversation" -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/conversation_visibility.go internal/handlers/conversation_visibility_test.go internal/handlers/export_test.go
git commit -m "feat(visibility): SQL scope translation with oracle test against the function"
```

---

## ETAPA 3 — Backend (aplicar a regra nos endpoints)

### Task 5: Migrar listagem/leitura para `scopeVisibleConversations`

**Files:**
- Modify: `internal/handlers/contacts.go` (ListContacts:113, GetContact:252, GetMessages:281, MarkContactRead:473, GetContactSessionData:1195), `internal/handlers/contact_status.go` (GetContactStatusCounts:25), `internal/handlers/media.go` (ServeMedia:161)
- Modify: `internal/handlers/contacts.go` (remover `scopeAssignedContact` ao fim)
- Test: `internal/handlers/conversation_visibility_test.go`

**Interfaces:**
- Consumes: `scopeVisibleConversations` (Task 4)
- Produces: nada — apenas troca de chamada

- [ ] **Step 1: Escrever o teste de listagem estrita (falhando)**

Anexar a `internal/handlers/conversation_visibility_test.go`:

```go
func TestListContacts_StrictVisibility(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := createAgentRole(t, app, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	mine := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, mine.ID, &agent.ID, nil)
	theirs := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, theirs.ID, &other.ID, nil)

	enableStrictVisibility(t, app, org.ID)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	require.NoError(t, app.ListContacts(req))

	var resp struct {
		Data struct {
			Contacts []handlers.ContactResponse `json:"contacts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))

	ids := map[string]bool{}
	for _, c := range resp.Data.Contacts {
		ids[c.ID.String()] = true
	}
	assert.True(t, ids[mine.ID.String()], "agent sees own conversation")
	assert.False(t, ids[theirs.ID.String()], "agent must not see another agent's conversation")
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestListContacts_StrictVisibility 2>&1 | tail -5`
Expected: FAIL — `theirs` aparece (ainda usando `scopeAssignedContact`)

- [ ] **Step 3: Trocar nos 7 pontos de listagem/leitura**

Em cada um dos pontos abaixo, trocar `a.scopeAssignedContact(query, userID, orgID)` por `a.scopeVisibleConversations(query, userID, orgID)`:

- `internal/handlers/contacts.go:113` (ListContacts)
- `internal/handlers/contacts.go:252` (GetContact)
- `internal/handlers/contacts.go:281` (GetMessages)
- `internal/handlers/contacts.go:473` (MarkContactRead)
- `internal/handlers/contacts.go:1195` (GetContactSessionData)
- `internal/handlers/contact_status.go:25` (GetContactStatusCounts)
- `internal/handlers/media.go:161` (ServeMedia — a chamada está encadeada num `a.DB.Where(...)`; trocar só o nome da função)

Os pontos de **ação** (SendMessage:583, SendMediaMessage:836, SendReaction:977, NotifyTyping em typing.go:33) ficam para o Task 6 — não trocar agora.

- [ ] **Step 4: Rodar o teste de listagem**

Run: `go test ./internal/handlers/ -run TestListContacts_StrictVisibility -v 2>&1 | grep -E "^(--- |ok|FAIL)"`
Expected: PASS

- [ ] **Step 5: Regressão da flag desligada**

Rodar os testes de listagem já existentes (que rodam sem a flag) para garantir comportamento idêntico:
Run: `go test ./internal/handlers/ -run "TestApp_ListContacts" -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS (todos os já existentes)

- [ ] **Step 6: Remover `scopeAssignedContact`**

Confirmar que os pontos de ação restantes serão migrados no Task 6 e que nenhuma outra referência sobrou:
Run: `grep -rn "scopeAssignedContact" --include="*.go" internal/handlers | grep -v "_test"`
Expected: só as 4 chamadas de ação (send/media/reaction/typing). **Não** remover a função ainda — ela será removida no fim do Task 6, quando os últimos 4 pontos migrarem.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/contacts.go internal/handlers/contact_status.go internal/handlers/media.go internal/handlers/conversation_visibility_test.go
git commit -m "feat(visibility): enforce strict visibility on listing and read endpoints"
```

---

### Task 6: 403 nos endpoints de ação + remover `scopeAssignedContact`

**Files:**
- Modify: `internal/handlers/contacts.go` (SendMessage:583, SendMediaMessage:836, SendReaction:977), `internal/handlers/typing.go` (NotifyTyping:33), `internal/handlers/contact_status.go` (UpdateContactStatus)
- Modify: `internal/handlers/contacts.go` (remover `scopeAssignedContact`)
- Test: `internal/handlers/conversation_visibility_test.go`

**Interfaces:**
- Consumes: `canInteractWithConversation` (Task 3)
- Produces: nada

- [ ] **Step 1: Escrever os testes de ação (falhando)**

Anexar a `internal/handlers/conversation_visibility_test.go`. Cobrem o Ajuste 2 (pós-transferência) e o Ajuste 3 (multiconta):

```go
func TestSendMessage_403AfterTransfer(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := createAgentRole(t, app, org.ID)
	source := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	dest := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))

	// Initially served by source.
	activeTransfer(t, app, org.ID, contact.ID, &source.ID, nil)
	enableStrictVisibility(t, app, org.ID)

	// Transfer to dest: close source's transfer, open dest's (mirror the real
	// reassignment: only one active transfer at a time).
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Where("contact_id = ? AND status = ?", contact.ID, models.TransferStatusActive).
		Update("status", models.TransferStatusResumed).Error)
	activeTransfer(t, app, org.ID, contact.ID, &dest.ID, nil)

	// Source now tries to send — must be 403.
	req := testutil.NewJSONRequest(t, map[string]any{"content": map[string]string{"body": "hi"}})
	testutil.SetAuthContext(req, org.ID, source.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, app.SendMessage(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req),
		"the source agent loses interaction access immediately at transfer")
}

func TestSendMessage_MultiTenantIsolation(t *testing.T) {
	app := newTestApp(t)
	orgX := testutil.CreateTestOrganization(t, app.DB)
	orgY := testutil.CreateTestOrganization(t, app.DB)
	// Manager of X with view_all (admin role has it).
	adminRole := testutil.CreateAdminRole(t, app.DB, orgX.ID)
	managerX := testutil.CreateTestUser(t, app.DB, orgX.ID, testutil.WithRoleID(&adminRole.ID))
	// A contact in Y.
	contactY := testutil.CreateTestContact(t, app.DB, orgY.ID)
	enableStrictVisibility(t, app, orgX.ID)
	enableStrictVisibility(t, app, orgY.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"content": map[string]string{"body": "hi"}})
	testutil.SetAuthContext(req, orgX.ID, managerX.ID) // acting as org X
	testutil.SetPathParam(req, "id", contactY.ID.String())
	require.NoError(t, app.SendMessage(req))
	code := testutil.GetResponseStatusCode(req)
	assert.True(t, code == fasthttp.StatusNotFound || code == fasthttp.StatusForbidden,
		"view_all in org X must never reach a contact in org Y, got %d", code)
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run "TestSendMessage_403AfterTransfer|TestSendMessage_MultiTenant" 2>&1 | tail -6`
Expected: FAIL — o envio pós-transferência retorna 200 (ainda sem checagem)

- [ ] **Step 3: Adicionar a checagem de interação**

Nos handlers de ação, **após** carregar o contato (o `First(&contact)` que hoje usa `scopeAssignedContact`), trocar o scope pela busca org-scoped simples + checagem explícita. Padrão a aplicar em cada um:

Antes:
```go
	query := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID)
	query = a.scopeAssignedContact(query, userID, orgID)
	if err := query.First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
```

Depois:
```go
	if err := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID).
		First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}
	if !a.canInteractWithConversation(userID, orgID, &contact) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden,
			"You do not have access to this conversation", nil, "")
	}
```

Aplicar em: `SendMessage` (contacts.go:583), `SendMediaMessage` (contacts.go:836), `SendReaction` (contacts.go:977), `NotifyTyping` (typing.go:33). Em `NotifyTyping`, a variável de contato pode ter outro nome — ajustar.

Para `UpdateContactStatus` (contact_status.go): hoje usa `findByIDAndOrg` (org-scoped, sem visibilidade). Após o `findByIDAndOrg`, adicionar a mesma checagem `canInteractWithConversation → 403`. Mudar status é uma ação de atendimento.

**Isolamento multiconta:** a busca `Where("id = ? AND organization_id = ?", contactID, orgID)` já garante que um contato de outra org retorna 404 antes de qualquer checagem — é o que faz o teste multiconta passar com 404.

- [ ] **Step 4: Rodar os testes de ação**

Run: `go test ./internal/handlers/ -run "TestSendMessage_403AfterTransfer|TestSendMessage_MultiTenant" -v 2>&1 | grep -E "^(--- |ok|FAIL)"`
Expected: PASS

- [ ] **Step 5: Remover `scopeAssignedContact`**

Agora que nenhum ponto a usa, remover a função `scopeAssignedContact` de `internal/handlers/contacts.go` (linhas ~216-233).

Run: `grep -rn "scopeAssignedContact" --include="*.go" internal`
Expected: nenhuma ocorrência (nem em teste)

Se algum teste antigo referenciava `scopeAssignedContact` direto, migrá-lo para `scopeVisibleConversations` ou removê-lo.

- [ ] **Step 6: Verificar build e a suíte de visibilidade + regressão**

Run: `go build ./... && go test ./internal/handlers/ -run "TestAuthorize|TestVisibility|TestListContacts|TestSendMessage" 2>&1 | grep -E "^(--- FAIL|ok|FAIL)"`
Expected: sem `--- FAIL`; `ok`

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/contacts.go internal/handlers/typing.go internal/handlers/contact_status.go internal/handlers/conversation_visibility_test.go
git commit -m "feat(visibility): 403 on interaction with conversations outside access; drop scopeAssignedContact"
```

---

## ETAPA 4 — Frontend

### Task 7: Toggle da flag + UI respeitando o backend

**Files:**
- Modify: `frontend/src/views/settings/ChatbotSettingsView.vue`
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`
- Modify: `frontend/src/stores/contacts.ts` (tipo Contact ganha o que a API já devolve; sem lógica de permissão no cliente)
- Create: `frontend/e2e/tests/chat/conversation-visibility.spec.ts`

**Interfaces:**
- Consumes: a flag na API de settings (Task 2); a listagem já filtrada (Task 5)
- Produces: nada

- [ ] **Step 1: Adicionar o toggle nas configurações**

Em `frontend/src/views/settings/ChatbotSettingsView.vue`, no card de atribuição de agentes (onde estão "assumir da fila" e "atribuir ao mesmo agente"), adicionar um Switch ligado a `settings.strict_conversation_visibility`, seguindo o padrão dos toggles vizinhos. Strings novas:

`frontend/src/i18n/locales/en.json` (dentro do grupo de settings do chatbot):
```json
    "strictConversationVisibility": "Restrict conversations to the assigned agent",
    "strictConversationVisibilityDesc": "When on, an assigned conversation is visible and answerable only by the responsible agent. Supervisors and managers keep access via permissions."
```

`frontend/src/i18n/locales/pt-BR.json`:
```json
    "strictConversationVisibility": "Restringir conversas ao agente atribuído",
    "strictConversationVisibilityDesc": "Quando ligado, uma conversa atribuída é visível e respondível apenas pelo agente responsável. Supervisores e managers mantêm acesso conforme permissões."
```

Conferir o nome real das chaves no card (grep `allowQueuePickup` no arquivo) e espelhar o formato exato.

- [ ] **Step 2: Garantir o round-trip do campo no store de settings**

Onde o frontend lê/grava as configurações do chatbot, incluir `strict_conversation_visibility` no payload de leitura e de escrita, espelhando `assign_to_same_agent`. Conferir com `grep -rn "assign_to_same_agent\|close_inactive_attendances" frontend/src`.

- [ ] **Step 3: A UI confia no backend (sem lógica de permissão no cliente)**

A lista de conversas já vem filtrada da API (Task 5), então conversas não autorizadas não aparecem — nenhuma mudança de filtragem no cliente. Confirmar que o tipo `Contact` em `frontend/src/stores/contacts.ts` não precisa de campo novo para isso (a filtragem é server-side). Nenhum código de "esconder" no cliente: a ausência na resposta já esconde. O 403 numa ação é tratado pelo interceptor de erro existente (toast), o que já ocorre para outros 403 do app — conferir que `frontend/src/services/api.ts` não engole 403 silenciosamente.

- [ ] **Step 4: Verificar tipos e build**

Run: `cd frontend && npx vue-tsc --noEmit 2>&1 | grep -v AccountDetailView; npm run build 2>&1 | grep -E "error|built in"`
Expected: sem erros novos; só o pré-existente de `AccountDetailView.vue`

- [ ] **Step 5: Validar JSON dos locales**

Run: `cd frontend && node -e "JSON.parse(require('fs').readFileSync('src/i18n/locales/en.json','utf8'));JSON.parse(require('fs').readFileSync('src/i18n/locales/pt-BR.json','utf8'));console.log('JSON OK')"`
Expected: `JSON OK`

- [ ] **Step 6: Escrever o e2e**

Criar `frontend/e2e/tests/chat/conversation-visibility.spec.ts` seguindo o padrão de `contact-status.spec.ts` (login, `createTestScope`, SQL direto via `pg` para ligar a flag e criar transferências). Dois agentes: um atribuído vê e abre a conversa; o outro não a vê na lista. O setup liga `strict_conversation_visibility` via SQL na org de teste.

Nota: se o ambiente e2e não tiver dois papéis de agente distintos prontos, usar o setup de papéis já existente em `global-setup.ts` (admin/manager/agent) e ajustar as expectativas ao que esses papéis concedem.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/views/settings/ChatbotSettingsView.vue frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json frontend/src/stores/contacts.ts frontend/e2e/tests/chat/conversation-visibility.spec.ts
git commit -m "feat(chat): strict visibility toggle and UI respecting server-side filtering"
```

---

## Verificação final antes do PR

- [ ] `go build ./...` limpo
- [ ] `go vet ./...` limpo
- [ ] `go test -p 1 ./...` — só as falhas pré-existentes conhecidas
- [ ] `TestVisibilityScopeMatchesFunction` passa (scope e função concordam)
- [ ] `cd frontend && npx vue-tsc --noEmit` — só o erro pré-existente
- [ ] `cd frontend && npm run build` limpo
- [ ] `grep -rn "scopeAssignedContact" internal` — nenhuma ocorrência
- [ ] Migração aditiva apenas; flag default false
- [ ] Chaves de i18n em `en.json` **e** `pt-BR.json`

## Validação manual (ao final, conforme pedido)

Subir o app (`server -migrate`, banco e2e limpo) e, com `strict_conversation_visibility` ligado numa org de teste, confirmar manualmente:

1. **Atendimento:** agente A recebe/assume uma conversa; agente B (mesma equipe, sem `view_all`) **não** a vê na lista e recebe 403 ao tentar enviar por API direta.
2. **Supervisor:** um manager (`view_all`) vê e responde a conversa de A.
3. **Transferência:** A transfere para B; **imediatamente** A perde a conversa da lista e B passa a vê-la.
4. **Fila de equipe:** conversa devolvida à fila da equipe aparece para os membros da equipe; some para não-membros.
5. **Fila geral:** conversa sem equipe aparece para qualquer agente autorizado.
6. **Encerramento:** ao concluir, a conversa é liberada; nova mensagem do cliente reinicia o ciclo sem prender a A.
7. **Flag desligada:** repetir (1) com a flag off e confirmar que o comportamento é o de hoje (todos veem).
