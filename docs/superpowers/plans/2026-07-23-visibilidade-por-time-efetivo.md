# Visibilidade por Time Efetivo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Com o modo estrito ligado, escopar a fase de triagem (pool geral) por um "time efetivo" da conversa, para que agentes só vejam conversas direcionadas ao time deles.

**Architecture:** Estende a função central `authorizeConversation` (Ciclo 2) com um time efetivo resolvido por precedência: transferência ativa → carteira → `Contact.TeamID` (setado pelo fluxo) → `WhatsAppAccount.DefaultTeamID` → senão `view_all` apenas. O SQL `scopeVisibleConversations` é a tradução fiel, guardada pelo teste-oráculo `TestVisibilityScopeMatchesFunction`.

**Tech Stack:** Go 1.25 + GORM + Postgres 16; Vue 3 + Pinia + Tailwind + reka-ui; Playwright.

## Global Constraints

- Só muda comportamento com `strict_conversation_visibility == true`. Com a flag **desligada**, o comportamento é **idêntico ao atual** (Ciclo 2) — todo caminho novo fica atrás do check da flag já existente em `authorizeConversation`/`scopeVisibleConversations`.
- Migração **aditiva**: colunas anuláveis via `AutoMigrate`, sem backfill.
- **Invariante-oráculo:** `scopeVisibleConversations(q, user, org).Find(&cs)` retorna **exatamente** os contatos onde `canViewConversation(user, org, c)` é `true`. `TestVisibilityScopeMatchesFunction` garante — nunca deixe as duas divergirem.
- **Precedência:** transferência-ativa-para-agente e carteira vencem o time efetivo (mais específico vence).
- Testes Go rodam com `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test'` e `TEST_REDIS_URL='redis://localhost:6379'`. Sem essas envs, os testes DB-backed **pulam** silenciosamente.
- Chaves i18n **em ambos** os locales (`en.json` e `pt-BR.json`); `npm run i18n:keys` deve passar.
- Coluna GORM de `Contact.WhatsAppAccount` é `whats_app_account`; a de `WhatsAppAccount.Name` é `name`. O vínculo é `contacts.whats_app_account = whatsapp_accounts.name` (mesma org).

---

## File Structure

```
internal/models/models.go                       + Contact.TeamID; + WhatsAppAccount.DefaultTeamID
internal/handlers/conversation_visibility.go     árvore estrita atualizada; accountDefaultTeamID; SQL do scope
internal/handlers/conversation_visibility_test.go novos casos da árvore + oráculo estendido
internal/handlers/chatbot_graph_runner.go        execChatButtons grava Contact.TeamID a partir do team_id do botão
internal/handlers/chatbot_graph_runner_test.go   teste do set-team no botão
internal/handlers/contact_status.go              releaseContactTx limpa TeamID
internal/handlers/contact_status_test.go (ou existente)  teste do reset
internal/handlers/accounts.go                    AccountRequest.DefaultTeamID; UpdateAccount aplica
internal/handlers/accounts_test.go               teste do default_team_id
frontend/src/components/chatbot/ChatNodeProperties.vue  team_id por botão no nó buttons
frontend/src/views/settings/AccountDetailView.vue        seletor de time padrão
frontend/src/services/api.ts                     default_team_id no tipo da conta
frontend/src/i18n/locales/{en,pt-BR}.json        strings
frontend/e2e/tests/chat/team-scoped-visibility.spec.ts   e2e (opcional)
```

---

## Task 1: Colunas de modelo (Contact.TeamID, WhatsAppAccount.DefaultTeamID)

**Files:**
- Modify: `internal/models/models.go:293-322` (WhatsAppAccount), `internal/models/models.go:345-377` (Contact)
- Test: `internal/handlers/conversation_visibility_test.go`

**Interfaces:**
- Produces: `Contact.TeamID *uuid.UUID` (col `team_id`); `WhatsAppAccount.DefaultTeamID *uuid.UUID` (col `default_team_id`). Consumidos pelas Tasks 2–8.

- [ ] **Step 1: Escrever o teste que falha (campos persistem)**

Em `internal/handlers/conversation_visibility_test.go`, adicionar:

```go
func TestContactAndAccountTeamColumns(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	team := createTeamWithMember(t, app, org.ID, uuid.New())

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("team_id", team.ID).Error)
	var freshContact models.Contact
	require.NoError(t, app.DB.First(&freshContact, "id = ?", contact.ID).Error)
	require.NotNil(t, freshContact.TeamID)
	assert.Equal(t, team.ID, *freshContact.TeamID)

	acct := &models.WhatsAppAccount{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "acct-" + uuid.New().String()[:8], PhoneID: "p", BusinessID: "b",
		AccessToken: "t", DefaultTeamID: &team.ID,
	}
	require.NoError(t, app.DB.Create(acct).Error)
	var freshAcct models.WhatsAppAccount
	require.NoError(t, app.DB.First(&freshAcct, "id = ?", acct.ID).Error)
	require.NotNil(t, freshAcct.DefaultTeamID)
	assert.Equal(t, team.ID, *freshAcct.DefaultTeamID)
}
```

- [ ] **Step 2: Rodar o teste — deve falhar na compilação (campos inexistentes)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run TestContactAndAccountTeamColumns -count=1`
Expected: FAIL — `freshContact.TeamID undefined` / `DefaultTeamID undefined`.

- [ ] **Step 3: Adicionar os campos aos structs**

Em `internal/models/models.go`, no struct `Contact` (após `AssignedUserID`, ~linha 351):

```go
	// TeamID is the conversation's effective team during triage — set by the
	// chatbot flow (per-button), NOT the active transfer (AgentTransfer.TeamID)
	// nor the carteira (AssignedUserID, a user). Governs visibility only when
	// there is no active transfer and no carteira. See conversation_visibility.go.
	TeamID *uuid.UUID `gorm:"type:uuid;index" json:"team_id,omitempty"`
```

No struct `WhatsAppAccount` (após `IsSMB`, ~linha 312):

```go
	// DefaultTeamID scopes conversations on this number to a team from the
	// first message, before any transfer (e.g. a dedicated Finance number).
	DefaultTeamID *uuid.UUID `gorm:"type:uuid;index" json:"default_team_id,omitempty"`
```

- [ ] **Step 4: Rodar o teste — deve passar (AutoMigrate cria as colunas)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run TestContactAndAccountTeamColumns -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/models/models.go internal/handlers/conversation_visibility_test.go
git commit -m "feat(models): add Contact.TeamID and WhatsAppAccount.DefaultTeamID"
```

---

## Task 2: Time efetivo na função central (`authorizeConversation`)

**Files:**
- Modify: `internal/handlers/conversation_visibility.go:24-67`
- Test: `internal/handlers/conversation_visibility_test.go`

**Interfaces:**
- Consumes: `Contact.TeamID`, `WhatsAppAccount.DefaultTeamID` (Task 1); `userInTeam`, `activeTransferFor` (existentes).
- Produces: `func (a *App) accountDefaultTeamID(orgID uuid.UUID, contact *models.Contact) *uuid.UUID`. Árvore estrita nova (usada pela Task 3 como referência do SQL).

- [ ] **Step 1: Escrever os testes que falham (novos ramos + precedência)**

Adicionar a `internal/handlers/conversation_visibility_test.go`, dentro de `TestAuthorizeConversation` (novos `t.Run`):

```go
	t.Run("strict: flow team (Contact.TeamID) scopes to that team only", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
		member := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		outsider := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		team := createTeamWithMember(t, app, org.ID, member.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // no transfer, no carteira
		require.NoError(t, app.DB.Model(contact).Update("team_id", team.ID).Error)
		contact.TeamID = &team.ID
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(member.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(outsider.ID, org.ID, contact))
	})

	t.Run("strict: account default team scopes a teamless conversation", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
		member := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		outsider := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		team := createTeamWithMember(t, app, org.ID, member.ID)
		acct := &models.WhatsAppAccount{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
			Name: "fin-" + uuid.New().String()[:8], PhoneID: "p", BusinessID: "b",
			AccessToken: "t", DefaultTeamID: &team.ID,
		}
		require.NoError(t, app.DB.Create(acct).Error)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("whats_app_account", acct.Name).Error)
		contact.WhatsAppAccount = acct.Name
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(member.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(outsider.ID, org.ID, contact))
	})

	t.Run("strict: teamless with no account default is view_all only", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
		anyAgent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID) // no transfer/carteira/team/account default
		enableStrictVisibility(t, app, org.ID)

		assert.False(t, app.CanViewConversationForTest(anyAgent.ID, org.ID, contact))
	})

	t.Run("strict: carteira wins over flow team", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
		owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		teamMember := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
		team := createTeamWithMember(t, app, org.ID, teamMember.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{"assigned_user_id": owner.ID, "team_id": team.ID}).Error)
		contact.AssignedUserID = &owner.ID
		contact.TeamID = &team.ID
		enableStrictVisibility(t, app, org.ID)

		assert.True(t, app.CanViewConversationForTest(owner.ID, org.ID, contact))
		assert.False(t, app.CanViewConversationForTest(teamMember.ID, org.ID, contact), "carteira is more specific than flow team")
	})
```

- [ ] **Step 2: Rodar — deve falhar (comportamento antigo: pool geral vê tudo)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run 'TestAuthorizeConversation' -count=1`
Expected: FAIL nos novos subtestes (`outsider`/`anyAgent`/`teamMember` retornam `true` hoje).

- [ ] **Step 3: Implementar o helper + a árvore nova**

Em `internal/handlers/conversation_visibility.go`, adicionar o helper (após `userInTeam`):

```go
// accountDefaultTeamID returns the default team configured on the contact's
// WhatsApp account, or nil. Used only in strict mode as the last team signal
// before falling back to view_all-only.
func (a *App) accountDefaultTeamID(orgID uuid.UUID, contact *models.Contact) *uuid.UUID {
	if contact == nil || contact.WhatsAppAccount == "" {
		return nil
	}
	var acct models.WhatsAppAccount
	if err := a.DB.Select("default_team_id").
		Where("organization_id = ? AND name = ?", orgID, contact.WhatsAppAccount).
		First(&acct).Error; err != nil {
		return nil
	}
	return acct.DefaultTeamID
}
```

Substituir o bloco "Strict mode" (linhas 37-66) por:

```go
	// Strict mode.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return conversationAccess{canView: true, canInteract: true}
	}

	// Active transfer is the primary authority.
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
			// Active general-queue transfer (no agent, no team): fall back to
			// the account default team, else view_all only.
			if team := a.accountDefaultTeamID(orgID, contact); team != nil {
				ok := a.userInTeam(userID, *team)
				return conversationAccess{canView: ok, canInteract: ok}
			}
			return conversationAccess{canView: false, canInteract: false}
		}
	}

	// No active transfer: carteira governs (more specific than any team).
	if contact.AssignedUserID != nil {
		ok := *contact.AssignedUserID == userID
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// No carteira: effective team = flow-set team, else account default team.
	effTeam := contact.TeamID
	if effTeam == nil {
		effTeam = a.accountDefaultTeamID(orgID, contact)
	}
	if effTeam != nil {
		ok := a.userInTeam(userID, *effTeam)
		return conversationAccess{canView: ok, canInteract: ok}
	}

	// No transfer, no carteira, no team: view_all only.
	return conversationAccess{canView: false, canInteract: false}
```

- [ ] **Step 4: Rodar — os novos subtestes E os do Ciclo 2 passam**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run 'TestAuthorizeConversation' -count=1 -v`
Expected: PASS em todos (inclusive "team queue visible to team members only" e "assigned agent sees" do Ciclo 2).

Nota: o subteste do Ciclo 2 "strict: general queue (no team) visible to authorized agents" (linha ~127) **muda de expectativa** — agora a fila geral sem time da conta é `view_all` apenas. Atualize esse subteste para `assert.False` e renomeie para refletir a nova regra, ou remova-o (o novo "teamless with no account default is view_all only" o cobre).

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/conversation_visibility.go internal/handlers/conversation_visibility_test.go
git commit -m "feat(visibility): resolve effective team (flow + account default) in strict mode"
```

---

## Task 3: SQL twin (`scopeVisibleConversations`) + oráculo estendido

**Files:**
- Modify: `internal/handlers/conversation_visibility.go:112-168`
- Test: `internal/handlers/conversation_visibility_test.go` (`TestVisibilityScopeMatchesFunction`, linha 174)

**Interfaces:**
- Consumes: a árvore da Task 2 (a SQL deve casar com ela).

- [ ] **Step 1: Estender o oráculo (novos ramos) — deve falhar**

Em `TestVisibilityScopeMatchesFunction` (linha 174), após `idle` (linha 198), adicionar contatos:

```go
	flowTeamMine := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(flowTeamMine).Update("team_id", team.ID).Error) // team has viewer

	flowTeamOther := testutil.CreateTestContact(t, app.DB, org.ID)
	otherTeam := createTeamWithMember(t, app, org.ID, otherAgent.ID)
	require.NoError(t, app.DB.Model(flowTeamOther).Update("team_id", otherTeam.ID).Error)

	acctMine := &models.WhatsAppAccount{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "am-" + uuid.New().String()[:8], PhoneID: "p", BusinessID: "b",
		AccessToken: "t", DefaultTeamID: &team.ID,
	}
	require.NoError(t, app.DB.Create(acctMine).Error)
	acctDefaultMine := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(acctDefaultMine).Update("whats_app_account", acctMine.Name).Error)
```

E incluí-los na lista `all`:

```go
	all := []*models.Contact{
		assignedToViewer, assignedToOther, teamQueue, generalQueue, carteira, idle,
		flowTeamMine, flowTeamOther, acctDefaultMine,
	}
```

- [ ] **Step 2: Rodar o oráculo — deve falhar (SQL não considera os novos ramos)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run TestVisibilityScopeMatchesFunction -count=1`
Expected: FAIL — o conjunto do SQL diverge (ex.: `flowTeamMine` não incluído, `generalQueue`/`idle` incluídos indevidamente).

- [ ] **Step 3: Reescrever o ramo estrito do SQL**

Substituir o bloco estrito de `scopeVisibleConversations` (após o check `view_all`, linhas 128-167) por:

```go
	// Strict: view_all sees everything.
	if a.HasPermission(userID, models.ResourceConversations, models.ActionViewAll, orgID) {
		return query
	}

	myTeams := a.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID)
	activeSub := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ?", orgID, models.TransferStatusActive)
	activeAgentMine := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ? AND agent_id = ?", orgID, models.TransferStatusActive, userID)
	activeTeamMine := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ? AND agent_id IS NULL AND team_id IN (?)",
			orgID, models.TransferStatusActive, myTeams)
	activeGeneral := a.DB.Model(&models.AgentTransfer{}).Select("contact_id").
		Where("organization_id = ? AND status = ? AND agent_id IS NULL AND team_id IS NULL",
			orgID, models.TransferStatusActive)

	// The contact's WhatsApp account default team is one of my teams.
	acctDefault := `EXISTS (SELECT 1 FROM whatsapp_accounts wa
		WHERE wa.name = contacts.whats_app_account
		  AND wa.organization_id = contacts.organization_id
		  AND wa.default_team_id IN (?))`

	return query.Where(
		a.DB.
			Where("id IN (?)", activeAgentMine). // A: active transfer to me
			Or("id IN (?)", activeTeamMine).     // B: active team queue, my team
			Or(a.DB.Where("id IN (?)", activeGeneral).Where(acctDefault, myTeams)). // C: general queue + account default mine
			Or(a.DB.Where("id NOT IN (?)", activeSub).Where("assigned_user_id = ?", userID)). // D: carteira mine
			Or(a.DB.Where("id NOT IN (?)", activeSub).
				Where("assigned_user_id IS NULL AND team_id IN (?)", myTeams)). // E: flow team mine
			Or(a.DB.Where("id NOT IN (?)", activeSub).
				Where("assigned_user_id IS NULL AND team_id IS NULL").Where(acctDefault, myTeams)), // F: account default mine
	)
```

- [ ] **Step 4: Rodar o oráculo até passar (iterar o SQL se necessário)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run 'TestVisibilityScopeMatchesFunction|TestListContacts_StrictVisibility' -count=1 -v`
Expected: PASS. Se falhar, o oráculo aponta qual contato diverge — ajuste o `WHERE` até o conjunto bater com a função. **Não** altere a função para casar com o SQL; a função é a verdade.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/conversation_visibility.go internal/handlers/conversation_visibility_test.go
git commit -m "feat(visibility): SQL scope for effective team; extend oracle test"
```

---

## Task 4: Fluxo grava `Contact.TeamID` no botão

**Files:**
- Modify: `internal/handlers/chatbot_graph_runner.go:252-270` (`execChatButtons`)
- Test: `internal/handlers/chatbot_graph_runner_test.go`

**Interfaces:**
- Consumes: `Contact.TeamID` (Task 1); `stringFromConfig`/`buttonsFromConfig` (existentes).

- [ ] **Step 1: Teste que falha (botão com team_id grava, sem criar transferência)**

Em `internal/handlers/chatbot_graph_runner_test.go`:

```go
func TestExecChatButtons_SetsContactTeamFromButton(t *testing.T) {
	app, org, account, contact, session := newGraphTestFixtures(t)
	team := createTeamWithMember(t, app, org.ID, uuid.New())

	flow := &models.ChatbotFlow{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		WhatsAppAccount: account.Name, Name: "menu-team", IsEnabled: true,
		Graph: models.JSONB{
			"version": 2, "entry_node": "b1",
			"nodes": []any{
				map[string]any{"id": "b1", "type": "buttons", "label": "menu",
					"config": map[string]any{"body": "Escolha",
						"buttons": []any{map[string]any{"id": "vendas", "title": "Vendas", "team_id": team.ID.String()}}}},
				map[string]any{"id": "p1", "type": "prompt", "label": "ask",
					"config": map[string]any{"body": "CEP?", "store_as": "cep"}},
			},
			"edges": []any{map[string]any{"from": "b1", "to": "p1", "condition": "button:vendas"}},
		},
	}
	require.NoError(t, app.DB.Create(flow).Error)

	require.NoError(t, app.runChatGraph(account, contact, session, flow, "start", "", nil))    // park at b1
	require.NoError(t, app.runChatGraph(account, contact, session, flow, "", "vendas", nil))   // pick Vendas → p1

	var fresh models.Contact
	require.NoError(t, app.DB.First(&fresh, "id = ?", contact.ID).Error)
	require.NotNil(t, fresh.TeamID, "button team_id must set Contact.TeamID")
	assert.Equal(t, team.ID, *fresh.TeamID)

	var transfers int64
	app.DB.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Count(&transfers)
	assert.Equal(t, int64(0), transfers, "setting team must NOT create a transfer")
}
```

- [ ] **Step 2: Rodar — deve falhar (`TeamID` nil)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run TestExecChatButtons_SetsContactTeamFromButton -count=1`
Expected: FAIL — `fresh.TeamID` é nil.

- [ ] **Step 3: Gravar o time no consumo do botão**

Em `execChatButtons` (`chatbot_graph_runner.go`), dentro do bloco `if !ctx.consumed && ctx.buttonID != "" {`, após persistir `store_as` (após linha ~267), antes do `return`:

```go
		// Optional per-button team: set the conversation's effective team for
		// triage-phase visibility (a lightweight field write, NOT a transfer,
		// so the bot keeps running). Matched by button id.
		for _, b := range buttonsFromConfig(node.Config) {
			if id, _ := b["id"].(string); id == ctx.buttonID {
				if tid, _ := b["team_id"].(string); tid != "" {
					if parsed, err := uuid.Parse(tid); err == nil {
						if err := a.DB.Model(&models.Contact{}).Where("id = ?", ctx.contact.ID).
							Update("team_id", parsed).Error; err != nil {
							a.Log.Error("buttons node failed to set contact team",
								"node", node.ID, "contact", ctx.contact.ID, "error", err)
						} else {
							ctx.contact.TeamID = &parsed
						}
					}
				}
				break
			}
		}
```

- [ ] **Step 4: Rodar — passa; e a suíte de buttons/prompt não regride**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run 'TestExecChatButtons_SetsContactTeamFromButton|TestRunChatGraph_Buttons|TestRunChatGraph_GoldenPath' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chatbot_graph_runner.go internal/handlers/chatbot_graph_runner_test.go
git commit -m "feat(chatbot): set Contact.TeamID from an optional per-button team_id"
```

---

## Task 5: Reset do time no encerramento

**Files:**
- Modify: `internal/handlers/contact_status.go:245-274` (`releaseContactTx`)
- Test: `internal/handlers/conversation_visibility_test.go`

- [ ] **Step 1: Teste que falha (release limpa TeamID)**

```go
func TestReleaseContact_ClearsTeamID(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	team := createTeamWithMember(t, app, org.ID, uuid.New())
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("team_id", team.ID).Error)
	contact.TeamID = &team.ID

	require.NoError(t, app.ReleaseContactForTest(contact, nil, "test"))

	var fresh models.Contact
	require.NoError(t, app.DB.First(&fresh, "id = ?", contact.ID).Error)
	assert.Nil(t, fresh.TeamID, "release must clear the effective team")
}
```

(`ReleaseContactForTest` já existe em `export_test.go`.)

- [ ] **Step 2: Rodar — deve falhar (TeamID persiste)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run TestReleaseContact_ClearsTeamID -count=1`
Expected: FAIL — `fresh.TeamID` não é nil.

- [ ] **Step 3: Limpar TeamID em `releaseContactTx`**

Em `releaseContactTx` (`contact_status.go`), após o bloco `if wasAssigned { ... }` (linha ~258), antes de `transitionContactStatusDB`:

```go
	hadTeam := contact.TeamID != nil
	if hadTeam {
		if err := tx.Model(&models.Contact{}).
			Where("id = ?", contact.ID).
			Update("team_id", nil).Error; err != nil {
			return nil, err
		}
	}
```

E no closure retornado (junto de `if wasAssigned { contact.AssignedUserID = nil }`):

```go
		if hadTeam {
			contact.TeamID = nil
		}
```

- [ ] **Step 4: Rodar — passa**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run 'TestReleaseContact_ClearsTeamID|TestReleaseContact' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/contact_status.go internal/handlers/conversation_visibility_test.go
git commit -m "feat(contacts): clear effective team on release so the funnel restarts"
```

---

## Task 6: API da conta — `default_team_id`

**Files:**
- Modify: `internal/handlers/accounts.go` (`AccountRequest` struct + `UpdateAccount:186-270`)
- Test: `internal/handlers/accounts_test.go`

**Interfaces:**
- Produces: `AccountRequest.DefaultTeamID *string`; `UpdateAccount` persiste em `WhatsAppAccount.DefaultTeamID`.

- [ ] **Step 1: Teste que falha (update grava default_team_id)**

Em `internal/handlers/accounts_test.go`, espelhando `TestApp_UpdateAccount_Success`:

```go
func TestApp_UpdateAccount_DefaultTeam(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	team := createTeamWithMember(t, app, org.ID, admin.ID)
	acct := &models.WhatsAppAccount{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "n", PhoneID: "p", BusinessID: "b", AccessToken: "t",
	}
	require.NoError(t, app.DB.Create(acct).Error)

	body := fmt.Sprintf(`{"default_team_id":"%s"}`, team.ID.String())
	req := testutil.NewRequestWithBody(t, "PUT", body) // ver helper existente nos testes de UpdateAccount
	testutil.SetAuthContext(req, org.ID, admin.ID)
	testutil.SetPathParam(req, "id", acct.ID.String())
	require.NoError(t, app.UpdateAccount(req))

	var fresh models.WhatsAppAccount
	require.NoError(t, app.DB.First(&fresh, "id = ?", acct.ID).Error)
	require.NotNil(t, fresh.DefaultTeamID)
	assert.Equal(t, team.ID, *fresh.DefaultTeamID)
}
```

(Ajuste os helpers de request `testutil.*` para os já usados por `TestApp_UpdateAccount_Success:386` — reutilize o mesmo padrão daquele teste.)

- [ ] **Step 2: Rodar — deve falhar (campo não existe na request)**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run TestApp_UpdateAccount_DefaultTeam -count=1`
Expected: FAIL (compilação: `AccountRequest.DefaultTeamID` inexistente, ou `DefaultTeamID` nil).

- [ ] **Step 3: Adicionar o campo à request e aplicar no handler**

No struct `AccountRequest` (em `accounts.go`), adicionar:

```go
	DefaultTeamID *string `json:"default_team_id"` // "" ou null limpa; uuid define
```

Em `UpdateAccount`, após `account.IsDefaultOutgoing = req.IsDefaultOutgoing` (~linha 263):

```go
	if req.DefaultTeamID != nil {
		if *req.DefaultTeamID == "" {
			account.DefaultTeamID = nil
		} else if tid, err := uuid.Parse(*req.DefaultTeamID); err == nil {
			account.DefaultTeamID = &tid
		} else {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid default_team_id", nil, "")
		}
	}
```

(O `a.DB.Save(account)` existente mais abaixo persiste o campo.)

- [ ] **Step 4: Rodar — passa; e os testes de UpdateAccount existentes não regridem**

Run: `TEST_DATABASE_URL='postgres://test:test@127.0.0.1:5432/whatomate_test' TEST_REDIS_URL='redis://localhost:6379' go test ./internal/handlers/ -run 'TestApp_UpdateAccount' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/accounts.go internal/handlers/accounts_test.go
git commit -m "feat(accounts): configurable default_team_id per WhatsApp account"
```

---

## Task 7: Frontend — seletor de time por botão no construtor de fluxo

**Files:**
- Modify: `frontend/src/components/chatbot/ChatNodeProperties.vue` (seção `buttons`)
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`

**Interfaces:**
- Consumes: `useTeamsStore` (existe); grava `team_id` em cada botão do `config.buttons`.

- [ ] **Step 1: Importar teams store e localizar a seção buttons**

Em `ChatNodeProperties.vue`, garantir `import { useTeamsStore } from '@/stores/teams'`, `const teamsStore = useTeamsStore()` e, no setup, `if (teamsStore.teams.length === 0) teamsStore.fetchTeams()`. Encontrar o editor de cada botão dentro de `<template v-if="node.type === 'buttons'">`.

- [ ] **Step 2: Adicionar o seletor de time por botão**

Dentro do bloco que edita cada botão (onde já há título/id), adicionar um `Select` opcional:

```vue
              <div class="space-y-1.5">
                <Label class="text-xs">{{ t('chatbot.properties.buttonTeam') }}</Label>
                <Select
                  :model-value="button.team_id || '_none'"
                  @update:model-value="(v: any) => updateButton(idx, 'team_id', v === '_none' ? undefined : v)"
                >
                  <SelectTrigger class="h-8 text-sm"><SelectValue :placeholder="t('chatbot.properties.buttonTeamNone')" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="_none">{{ t('chatbot.properties.buttonTeamNone') }}</SelectItem>
                    <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">{{ team.name }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
```

(Use o mesmo helper que os outros campos do botão usam para gravar — `updateButton(idx, key, value)` ou equivalente já presente no componente; se o componente edita botões por índice de outra forma, siga esse padrão.)

- [ ] **Step 3: Adicionar as chaves i18n (ambos os locales)**

Em `en.json`, na seção `chatbot.properties`:
```json
      "buttonTeam": "Team for this option",
      "buttonTeamNone": "No team (unchanged)",
```
Em `pt-BR.json`, na mesma seção:
```json
      "buttonTeam": "Time para esta opção",
      "buttonTeamNone": "Sem time (não altera)",
```

- [ ] **Step 4: Verificar i18n + typecheck**

Run: `cd frontend && npm run i18n:keys && npm run typecheck`
Expected: chaves batem (2 novas em cada); typecheck sem erros novos (o erro pré-existente de `AccountDetailView` é aceitável até a Task 8).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/chatbot/ChatNodeProperties.vue frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(flow-builder): optional team per menu button"
```

---

## Task 8: Frontend — time padrão da conta

**Files:**
- Modify: `frontend/src/views/settings/AccountDetailView.vue`
- Modify: `frontend/src/services/api.ts` (tipo da conta + payload de update)
- Modify: `frontend/src/i18n/locales/{en,pt-BR}.json`

- [ ] **Step 1: Adicionar `default_team_id` ao tipo/serviço**

Em `api.ts`, no tipo da conta WhatsApp e no payload de `updateAccount`, incluir `default_team_id?: string | null`.

- [ ] **Step 2: Adicionar o seletor na tela da conta**

Em `AccountDetailView.vue`, junto das outras configurações da conta, um `Select` de time (usando `useTeamsStore`), vinculado a `form.default_team_id` (com opção "Sem time padrão" → `null`), enviado no update.

```vue
        <div class="space-y-1.5">
          <Label>{{ t('settings.accounts.defaultTeam') }}</Label>
          <p class="text-sm text-muted-foreground">{{ t('settings.accounts.defaultTeamHint') }}</p>
          <Select :model-value="form.default_team_id || '_none'" @update:model-value="(v:any) => form.default_team_id = v === '_none' ? null : v">
            <SelectTrigger><SelectValue :placeholder="t('settings.accounts.defaultTeamNone')" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="_none">{{ t('settings.accounts.defaultTeamNone') }}</SelectItem>
              <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">{{ team.name }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
```

- [ ] **Step 3: i18n (ambos os locales)**

`en.json` → `settings.accounts`:
```json
      "defaultTeam": "Default team",
      "defaultTeamHint": "Conversations on this number are visible only to this team (before any transfer).",
      "defaultTeamNone": "No default team",
```
`pt-BR.json` → `settings.accounts`:
```json
      "defaultTeam": "Time padrão",
      "defaultTeamHint": "Conversas deste número ficam visíveis apenas para este time (antes de qualquer transferência).",
      "defaultTeamNone": "Sem time padrão",
```

- [ ] **Step 4: i18n + typecheck**

Run: `cd frontend && npm run i18n:keys && npm run typecheck`
Expected: chaves batem; typecheck sem erros novos.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/settings/AccountDetailView.vue frontend/src/services/api.ts frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(accounts-ui): default team selector per number"
```

---

## Task 9 (opcional): e2e — triagem escopada

**Files:**
- Create: `frontend/e2e/tests/chat/team-scoped-visibility.spec.ts`

- [ ] **Step 1: Escrever o spec (strict ON, dois times, conversa com Contact.team_id)**

Espelhar `conversation-visibility.spec.ts`: criar dois agentes em times distintos; semear um contato com `team_id` do time A (via SQL `execSQL`, como o spec existente faz com transfers); afirmar via API `/contacts` que o agente do time A vê e o do time B não. Ligar strict via `setStrictVisibility(true)`.

- [ ] **Step 2: Rodar contra o app**

Run (com app buildado+rodando, ver o pipeline em `.github/workflows/e2e-tests.yml`): `cd frontend && BASE_URL=http://localhost:8080 TEST_DATABASE_URL=... CI=true npx playwright test team-scoped-visibility`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add frontend/e2e/tests/chat/team-scoped-visibility.spec.ts
git commit -m "test(e2e): team-scoped triage visibility"
```

---

## Self-Review (feito)

- **Cobertura da spec:** modelos (T1), árvore/função (T2), SQL+oráculo (T3), fluxo grava time (T4), reset (T5), API conta (T6), UI botão (T7), UI conta (T8), e2e (T9). Precedência, fila-geral→view_all, e o caso dos N agentes-de-loja estão cobertos em T2/T3. ✔
- **Placeholders:** nenhum "TODO"/"similar a"; código real em cada passo. Os helpers `testutil.*` da Task 6 devem espelhar `TestApp_UpdateAccount_Success` (padrão existente) — apontado explicitamente. ✔
- **Consistência de tipos:** `Contact.TeamID *uuid.UUID`, `WhatsAppAccount.DefaultTeamID *uuid.UUID`, `accountDefaultTeamID(orgID, *Contact) *uuid.UUID`, `AccountRequest.DefaultTeamID *string` — usados igualmente entre tasks. ✔
