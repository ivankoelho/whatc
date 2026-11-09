# Agentes de IA — Projeto 1 (agente base) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Vários agentes de IA configuráveis dentro do chatbot atual, com instruções estruturadas, desligamento imediato quando um humano assume, teto mensal de tokens e filtro de promessas.

**Architecture:** Nova tabela `ai_agents` por organização; `chatbot_settings` aponta o agente padrão (por organização ou conta) e o nó `ai_response` pode escolher outro. `generateAIReply` monta o prompt em camadas (plataforma → agente → `AIContext` → conhecimento) e passa a devolver um motivo de transferência (`Handoff`) quando o marcador, o filtro ou o teto dispararem. Um helper único `humanTookOver` é chamado antes de cada envio do agente. Sem agente resolvido, o caminho atual (`SystemPrompt` livre) fica intacto.

**Tech Stack:** Go (fastglue, GORM, Postgres), Vue 3 + TypeScript, vue-i18n (PT-BR e EN).

**Spec:** `docs/superpowers/specs/2026-11-09-agentes-ia-base-design.md`

## Global Constraints

- Base `origin/development`; branch `feature/agente-ia-base`; worktree `../whatc-agente-ia`. Nunca tocar `main`. Sem push e sem deploy sem confirmação do Ivan.
- Isolamento por `organization_id` em toda consulta e rota nova; a chave de IA continua só em `ChatbotSettings.AI` (criptografada, só `resolveAIAPIKey` decifra) e nunca aparece em log, resposta de API ou cache em claro.
- Migração aditiva e idempotente (registro em `internal/database/postgres.go` e `test/testutil/db.go`); nenhuma coluna existente muda.
- Organização sem agente configurado: comportamento idêntico ao de hoje.
- Toda chamada de IA continua passando por `completeAI` (grava `AIUsageLog`).
- Textos de interface em PT-BR e EN. Reaproveitar a permissão de configurações do chatbot; sem permissão nova.
- Testes Go: precisam de `TEST_DATABASE_URL` e `TEST_REDIS_URL` (containers `whatomate_test_pg` e `whatomate_test_redis`); rodar com `-p 1`.
- Commits terminam com a linha `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. PR para `development` leva a linha `Roadmap: id=status` do fluxo de roadmap automático.

## Review Focus

1. Mensagem do cliente chega com o agente ainda gerando e um atendente assume no meio: nada é enviado.
2. Resposta do agente com "não garanto estoque": não é bloqueada; "garanto o preço" é.
3. Agente padrão desativado ou apagado: cai no `SystemPrompt` livre, nunca em silêncio.
4. Teto estourado: cliente recebe a mensagem de espera e a conversa vai para a fila; nunca erro técnico.
5. Marcador `[[TRANSFERIR]]` nunca chega ao cliente, nem quando vem no meio do texto ou repetido.

---

### Task 1: Modelo `AIAgent` e API CRUD

**Files:**
- Create: `internal/models/ai_agent.go`, `internal/handlers/ai_agents.go`, `internal/handlers/ai_agents_test.go`
- Modify: `internal/database/postgres.go` (lista de modelos, perto da linha 108), `test/testutil/db.go` (perto da linha 127), `cmd/whatomate/main.go` (rotas, perto da linha 1188)

**Interfaces:**
- Produces: `models.AIAgent` (tabela `ai_agents`: `BaseModel`, `OrganizationID uuid.UUID`, `Name string`, `IsEnabled bool`, `RoleObjective string`, `Tone string`, `AlwaysRules StringArray`, `NeverRules StringArray`, `HandoffRules StringArray`, `CompanyInfo string`, `Model string`, `Temperature *float64`, `MaxTokens *int`); rotas `GET/POST /api/chatbot/ai-agents`, `GET/PUT/DELETE /api/chatbot/ai-agents/{id}`; `func (a *App) getAIAgent(orgID, id uuid.UUID) (*models.AIAgent, error)`.

- [ ] **Step 1: Escrever os testes** em `ai_agents_test.go`, no estilo de `chatbot_test.go` (AIContext): `TestAIAgents_CreateListGetUpdateDelete`; `TestAIAgents_OrgIsolation` (agente da org A dá 404 para a org B em GET, PUT e DELETE); `TestAIAgents_NeverRulesDefault` (criar sem `never_rules` devolve as proibições padrão de promessa de preço, prazo, estoque e reserva).
- [ ] **Step 2: Rodar e ver falhar:** `go test ./internal/handlers -run TestAIAgents -p 1 -v` → FAIL (símbolos inexistentes).
- [ ] **Step 3: Implementar** o modelo e os handlers copiando o padrão de `ListAIContexts`/`CreateAIContext`/`UpdateAIContext`/`DeleteAIContext` (`internal/handlers/chatbot.go:1235-1466`), incluindo validação de tamanho dos campos (nome ≤ 255; listas ≤ 50 itens de ≤ 500 caracteres; textos ≤ 4000). Padrão de `NeverRules` fica em constante exportada `models.DefaultAINeverRules`. Registrar modelo e rotas.
- [ ] **Step 4: Rodar e ver passar** (mesmo comando) → PASS.
- [ ] **Step 5: Commit** `feat(ai): AIAgent model and CRUD API`.

### Task 2: Campos novos em `ChatbotSettings`

**Files:**
- Modify: `internal/models/chatbot.go` (struct `ChatbotSettings`, perto da linha 133), `internal/handlers/chatbot.go` (`GetChatbotSettings` linha 128 e `UpdateChatbotSettings` linha 308; os campos `ai_*` seguem o padrão das linhas 38, 205, 337, 553)
- Test: `internal/handlers/chatbot_test.go`

**Interfaces:**
- Consumes: `models.AIAgent` (Task 1).
- Produces em `ChatbotSettings`: `DefaultAIAgentID *uuid.UUID` (`default_ai_agent_id`), `AIMonthlyTokenLimit int` (`ai_monthly_token_limit`, 0 = sem teto), `AIHandoffMessage string` (`ai_handoff_message`, vazio = padrão "Vou chamar um atendente para continuar com você."), `AIPromiseTermsAdd StringArray` e `AIPromiseTermsRemove StringArray` (`ai_promise_terms_add/remove`); `func (a *App) resolveAIAgent(settings *models.ChatbotSettings, nodeAgentID *uuid.UUID) *models.AIAgent` (ordem: id do nó → `DefaultAIAgentID` da linha da conta → `DefaultAIAgentID` da linha padrão da organização; ignora agente inexistente, de outra organização ou desativado; devolve nil se nada servir) e `func aiHandoffMessage(settings *models.ChatbotSettings) string`.

- [ ] **Step 1: Escrever testes:** `TestChatbotSettings_AgentFieldsRoundTrip`; `TestResolveAIAgent` com casos nó vence conta, conta vence organização, agente desativado → nil, agente apagado → nil, agente de outra organização → nil; `TestAIHandoffMessage_Default`.
- [ ] **Step 2: Rodar e ver falhar:** `go test ./internal/handlers -run "TestChatbotSettings_AgentFields|TestResolveAIAgent|TestAIHandoffMessage" -p 1 -v`.
- [ ] **Step 3: Implementar** os campos (colunas novas, `NOT NULL DEFAULT` onde couber), leitura e escrita nos dois handlers e as duas funções. Validar `default_ai_agent_id` pertencente à organização.
- [ ] **Step 4: Rodar e ver passar.**
- [ ] **Step 5: Commit** `feat(ai): agent selection and limits fields on chatbot settings`.

### Task 3: Montagem do prompt em camadas

**Files:**
- Create: `internal/handlers/ai_agent_prompt.go`, `internal/handlers/ai_agent_prompt_test.go`
- Modify: `internal/handlers/chatbot_processor.go` (`generateAIReply`, linha 888)

**Interfaces:**
- Consumes: `resolveAIAgent` (Task 2).
- Produces: `const aiPlatformRules string` (camada fixa: nunca prometer preço, prazo, estoque ou reserva; confirmar é papel do vendedor; não inventar; pedido de humano → emitir o marcador `[[TRANSFERIR]]`); `func buildAgentPrompt(agent *models.AIAgent) string` (seções rotuladas na ordem papel, tom, empresa, sempre, nunca, quando transferir; "nunca" por último e em bloco próprio); `generateAIReply` passa a usar: plataforma + `buildAgentPrompt` quando há agente, e `settings.AI.SystemPrompt` como hoje quando não há; `Model`, `Temperature` e `MaxTokens` do agente, quando preenchidos, sobrescrevem os de `settings.AI`. Assinatura ganha o id de agente opcional do nó: `generateAIReply(settings, session, userMessage, feature string, nodeAgentID *uuid.UUID)`; atualizar os chamadores existentes.

- [ ] **Step 1: Testes** (função pura): `TestBuildAgentPrompt_SectionOrder` (a posição de "NUNCA" é maior que a de "SEMPRE"), `TestBuildAgentPrompt_OmitsEmptySections`, `TestGenerateAIReply_NoAgentKeepsLegacyPrompt` (prompt enviado ao provedor falso é exatamente `SystemPrompt` + contexto, como antes).
- [ ] **Step 2: Rodar e ver falhar:** `go test ./internal/handlers -run "TestBuildAgentPrompt|TestGenerateAIReply_NoAgent" -p 1 -v`.
- [ ] **Step 3: Implementar** conforme as assinaturas. Provedor falso: reaproveitar o usado em `ai_runtime_test.go`.
- [ ] **Step 4: Rodar e ver passar**, e rodar a suíte `-run "AI|Chatbot"` para garantir que nada mudou sem agente.
- [ ] **Step 5: Commit** `feat(ai): layered agent prompt`.

### Task 4: Filtro de promessas e marcador de transferência

**Files:**
- Create: `internal/handlers/ai_guardrails.go`, `internal/handlers/ai_guardrails_test.go`

**Interfaces:**
- Produces: `var defaultPromiseTerms []string` (inclui "garanto", "prometo", "reservei", "está garantido", "garantido", "fica reservado"); `func effectivePromiseTerms(add, remove []string) []string`; `func containsPromise(text string, terms []string) bool` (palavra inteira, sem diferenciar caixa e acento, ignora termo precedido de "não"/"nao" em até 3 palavras antes); `const handoffMarker = "[[TRANSFERIR]]"`; `func extractHandoff(text string) (clean string, handoff bool)` (remove todas as ocorrências, em qualquer posição, e faz trim).

- [ ] **Step 1: Testes:** `TestContainsPromise` com tabela: "garanto o preço" → true; "Não garanto estoque" → false; "nao garanto" → false; "GARANTO" → true; "garantos" → false (palavra inteira); "garantia de fábrica" → false; remoção via `remove` torna "prometo" aceitável; adição via `add` passa a bloquear. `TestExtractHandoff`: marcador no fim, no meio, repetido e sozinho (clean vazio, handoff true).
- [ ] **Step 2: Rodar e ver falhar:** `go test ./internal/handlers -run "TestContainsPromise|TestExtractHandoff" -p 1 -v`.
- [ ] **Step 3: Implementar** com normalização via `golang.org/x/text` se já estiver no `go.mod`, senão com tabela de acentos própria.
- [ ] **Step 4: Rodar e ver passar.**
- [ ] **Step 5: Commit** `feat(ai): promise filter and handoff marker`.

### Task 5: Guarda "humano assumiu" e auditoria dos caminhos do atendente

**Files:**
- Modify: `internal/handlers/agent_transfers.go` (helper junto de `hasActiveAgentTransfer`, linha 1422), `internal/handlers/chatbot_processor.go` (envio na linha ~414-424), `internal/handlers/chatbot_graph_runner.go` (`execChatAIResponse`, linha ~707-745), e os pontos de envio de atendente que a auditoria apontar
- Test: `internal/handlers/agent_transfers_test.go`, `internal/handlers/chatbot_processor_test.go`

**Interfaces:**
- Produces: `func (a *App) humanTookOver(orgID, contactID uuid.UUID) bool` (verdadeiro quando há `AgentTransfer` ativo; **não** considera `Contact.AssignedUserID`); o envio do agente nos dois caminhos chama o helper imediatamente antes de enviar e, se verdadeiro, descarta a resposta e grava `AIUsageLog` com `ErrorKind = "discarded_human"`.

- [ ] **Step 1: Testes:** `TestHumanTookOver_ActiveTransfer` (true com transferência ativa, false sem, false só com `AssignedUserID`); `TestAIReply_DiscardedWhenTransferBecomesActive` (provedor falso cria a transferência durante a chamada; nada é enviado; `AIUsageLog` marcado); mesmo cenário para o nó `ai_response`.
- [ ] **Step 2: Rodar e ver falhar:** `go test ./internal/handlers -run "TestHumanTookOver|TestAIReply_Discarded" -p 1 -v`.
- [ ] **Step 3: Implementar** o helper e plugar nos dois caminhos.
- [ ] **Step 4: Auditar** todo caminho em que um atendente envia mensagem (inbox, API, fila; `grep` por `sendAndSave` e pelo handler de envio de mensagens) em conversa sem transferência ativa. A criação de `TransferSourceAgentInitiated` hoje está só em `agent_transfers.go:1689`. Escrever `TestAgentSend_CreatesAgentInitiatedTransfer` para cada caminho que não a criar e corrigir só essas lacunas.
- [ ] **Step 5: Rodar e ver passar**, depois `-run "AgentTransfer|Chatbot"`.
- [ ] **Step 6: Commit** `feat(ai): stop the agent as soon as a human takes over`.

### Task 6: Transferência do agente, teto de tokens e filtro no fluxo

**Files:**
- Create: `internal/handlers/ai_handoff.go`, `internal/handlers/ai_handoff_test.go`
- Modify: `internal/handlers/chatbot_processor.go` (`aiReply` linha ~881, `generateAIReply`, envio da linha ~414), `internal/handlers/chatbot_graph_runner.go` (`execChatAIResponse`)

**Interfaces:**
- Consumes: `extractHandoff`, `containsPromise`, `effectivePromiseTerms` (Task 4); `aiHandoffMessage` (Task 2); `humanTookOver` (Task 5).
- Produces: `aiReply` ganha `Handoff string` (`""`, `"marker"`, `"promise"` ou `"cap"`); `func (a *App) aiTokensThisMonth(orgID uuid.UUID, now time.Time) (int64, error)` (soma `TotalTokens` de `chatbot_reply` e `chatbot_flow_node`, mês civil em `America/Sao_Paulo`); `func (a *App) handoffFromAI(account *models.WhatsAppAccount, contact *models.Contact, settings *models.ChatbotSettings, reason string)` (envia `aiHandoffMessage` e chama `createTransferToQueue(account, contact, models.TransferSourceAI...)`; usar a origem existente que melhor couber, ou criar `TransferSourceAIHandoff`, e registrar o motivo).
- Regras: só com agente resolvido. Teto: limite > 0 e consumo ≥ limite → `Handoff="cap"` **antes** da chamada ao provedor (a chamada que cruza o limite passa; a seguinte é cortada). Marcador → texto limpo é enviado se não vazio e depois transfere. Filtro de promessa casa → resposta **não** é enviada, transfere.

- [ ] **Step 1: Testes:** `TestAITokensThisMonth` (ignora outros meses, outras organizações e outras features); `TestGenerateAIReply_CapReached` (provedor falso não é chamado); `TestGenerateAIReply_CapCrossingCallAllowed`; `TestGenerateAIReply_PromiseBlocked`; `TestGenerateAIReply_MarkerStrippedAndHandoff` (marcador no meio e repetido nunca chega ao cliente); `TestHandoffFromAI_SendsWaitMessageAndTransfers`; `TestNoAgent_NoCapNoFilter` (sem agente, nada disso roda).
- [ ] **Step 2: Rodar e ver falhar:** `go test ./internal/handlers -run "TestAITokens|TestGenerateAIReply_|TestHandoffFromAI|TestNoAgent" -p 1 -v`.
- [ ] **Step 3: Implementar** e ligar nos dois caminhos de envio (processador e nó `ai_response`, que passa a usar `generateAIReply` e lê `agent_id` da config do nó).
- [ ] **Step 4: Rodar e ver passar**, depois a suíte `-run "AI|Chatbot|AgentTransfer"`.
- [ ] **Step 5: Commit** `feat(ai): handoff, token cap and promise filter`.

### Task 7: Interface

**Files:**
- Create: `frontend/src/views/chatbot/AIAgentsView.vue`, `frontend/src/views/chatbot/AIAgentDetailView.vue`
- Modify: `frontend/src/services/api.ts` (perto de `listAIContexts`, linha 445), `frontend/src/router/index.ts`, `frontend/src/views/chatbot/ChatbotView.vue` (cartão de acesso ao lado de "Contextos de IA", linha ~263), `frontend/src/i18n/locales/pt-BR.json` e `en.json`, tela de configurações de IA existente (agente padrão, teto de tokens, mensagem de espera, expressões bloqueadas), editor do nó `ai_response` no `ChatbotFlowBuilderView.vue` (seletor de agente)
- Test: specs Vitest ao lado de cada view nova (padrão dos `*.spec.ts` do projeto)

- [ ] **Step 1: Testes:** `AIAgentsView.spec.ts` (lista, criar e apagar chamam a API); `AIAgentDetailView.spec.ts` (editar listas "sempre"/"nunca"/"transferir" e salvar envia os campos do contrato da Task 1; "nunca" já vem com o padrão); spec do seletor de agente do nó (envia `agent_id`).
- [ ] **Step 2: Rodar e ver falhar:** `npm --prefix frontend run test -- AIAgent`.
- [ ] **Step 3: Implementar** seguindo `AIContextsView.vue`/`AIContextDetailView.vue`; rotas sob `/chatbot/agents`; chaves i18n nos dois idiomas.
- [ ] **Step 4: Rodar e ver passar**, mais `npm --prefix frontend run typecheck` e `npm --prefix frontend run lint`.
- [ ] **Step 5: Commit** `feat(ai): agents UI`.

### Task 8: Verificação final

- [ ] **Step 1:** `go build ./...`, `go vet ./...` e `go test ./... -p 1` com as variáveis de teste; anotar falhas preexistentes separadamente (CI de `development` já tem falhas conhecidas).
- [ ] **Step 2:** Subir o app local e conferir no navegador: criar agente, definir como padrão, simular conversa com provedor falso ou real de teste, assumir como atendente e ver o agente calar.
- [ ] **Step 3:** Abrir o PR para `development` com `Roadmap:`, sem deploy.

---

**Cobertura da spec:** §3.1 e §6.1 → Tasks 1, 7; §3.2 e §6.3 → Task 3; §3.3 e §6.4 → Task 5; §3.4 e §6.6 → Tasks 3, 4, 6; §3.5 e §6.5 → Task 6; §3.6 → Tasks 2, 3 (testes de compatibilidade); §6.2 → Tasks 2, 3, 6, 7; §6.7 → Task 7; §9 → distribuído nas tarefas acima.
