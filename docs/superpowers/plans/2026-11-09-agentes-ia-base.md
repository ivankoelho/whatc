# Agentes de IA — MVP (rev. 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agente de IA no chatbot que atende, qualifica, registra a oportunidade no CRM, transfere à equipe da unidade e cala quando um humano assume, com teto mensal de tokens.

**Architecture:** Tabela `ai_agents` + agente padrão em `ChatbotSettings`. `generateAIReply` monta o prompt em camadas e passa a devolver um motivo de transferência. Um único ponto (`handoffFromAI`) faz extração → oportunidade → transferência à equipe do número. `humanTookOver` guarda cada envio. O teto reserva tokens no próprio `AIUsageLog` sob `pg_advisory_xact_lock`. Nenhum módulo paralelo.

**Tech Stack:** Go (fastglue, GORM, Postgres), Vue 3 + TypeScript, vue-i18n (PT-BR e EN).

**Spec:** `docs/superpowers/specs/2026-11-09-agentes-ia-base-design.md` (rev. 2). Seções citadas como §.

## Global Constraints

- Base `origin/development`; branch local `feature/agente-ia-base`, worktree `../whatc-agente-ia`. Sem push, merge ou deploy sem o Ivan.
- Isolamento por `organization_id` em toda consulta e rota; chave de IA só em `ChatbotSettings.AI`, nunca em log, API ou cache em claro.
- Migração aditiva e idempotente (registrar em `internal/database/postgres.go` e `test/testutil/db.go`); nenhuma coluna existente muda.
- Sem agente padrão ativo, comportamento idêntico ao atual. Toda chamada de IA passa por `completeAI`.
- Interface em PT-BR e EN; permissão de configurações do chatbot; sem permissão nova.
- Testes Go: `TEST_DATABASE_URL` e `TEST_REDIS_URL` (containers `whatomate_test_pg` e `whatomate_test_redis`), com `-p 1`, direcionados com `-run`.
- Commits terminam com `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`; PR para `development` leva a linha `Roadmap: id=status`.

## Review Focus

1. Duas mensagens do cliente quase simultâneas: uma transferência, uma mensagem de espera, uma oportunidade.
2. Atendente assume **a qualquer instante** (antes da checagem, entre a checagem e o envio, durante a geração ou a extração): nenhuma mensagem do agente sai depois da confirmação do atendimento (trava por contato, T3).
3. A extração devolve metragem ou produto que o cliente nunca disse: o campo vira "não informado".
4. Número sem equipe de unidade: o agente não atua. Equipe desativada no meio do caminho: fila geral marcada, cliente atendido.
5. Duas chamadas simultâneas perto do teto, chamada demorada, falha do provedor ou falha ao gravar o uso: a soma nunca passa do teto e nunca fica zerada entre a reserva e o uso real.
7. Organização com vários números: o agente do piloto atua só no número listado, em resposta direta e no nó `ai_response`; os demais números, mesmo com equipe e unidade, seguem como hoje.
6. Marcador `[[TRANSFERIR]]` e JSON de extração nunca chegam ao cliente.

---

### Task 1: Agente, configurações e resolução (complexidade: média)

**Files:**
- Create: `internal/models/ai_agent.go`, `internal/handlers/ai_agents.go`, `internal/handlers/ai_agents_test.go`
- Modify: `internal/models/chatbot.go` (`ChatbotSettings`), `internal/handlers/chatbot.go` (`GetChatbotSettings`:128, `UpdateChatbotSettings`:308, padrão dos campos `ai_*`), `internal/database/postgres.go` (~108), `test/testutil/db.go` (~127), `cmd/whatomate/main.go` (rotas, ~1188)

**Interfaces — Produces:**
- `models.AIAgent` (`ai_agents`: `BaseModel`, `OrganizationID`, `Name`, `Objective`, `Instructions`, `CompanyInfo`, `Tone`, `HandoffRules` — todos `string` —, `IsEnabled bool`, `WhatsAppAccounts StringArray` (`whatsapp_accounts`, jsonb: números permitidos, §5.8)); rotas `GET/POST /api/chatbot/ai-agents`, `GET/PUT/DELETE /api/chatbot/ai-agents/{id}`.
- `ChatbotSettings.DefaultAIAgentID *uuid.UUID` (`default_ai_agent_id`), `ChatbotSettings.AIMonthlyTokenLimit int64` (`ai_monthly_token_limit`, 0 = sem teto).
- `func (a *App) resolveAIAgent(settings *models.ChatbotSettings, account *models.WhatsAppAccount) *models.AIAgent` — nil (fail-closed) se: sem padrão, agente apagado/desativado/de outra organização, **`account.Name` fora de `agent.WhatsAppAccounts` (lista vazia = nenhum número)**, `account.DefaultTeamID` nulo, equipe inativa/de outra organização, ou equipe sem `UnitID` (§5.2, §5.8).
- `func (a *App) aiAgentAccountIssues(agent *models.AIAgent) []AIAgentAccountIssue` (`{Account, Problem string}` por número permitido sem equipe de unidade válida, para o aviso da tela; `GET /api/chatbot/ai-agents/{id}/readiness`). O CRUD valida que cada nome em `whatsapp_accounts` é um número da própria organização.

- [ ] **Step 1: Testes** (estilo `chatbot_test.go`, AIContext): `TestAIAgents_CRUD`; `TestAIAgents_OrgIsolation` (404 cruzado em GET/PUT/DELETE); `TestResolveAIAgent` (tabela: ok; sem padrão; desativado; apagado; outra organização; **lista de números vazia; número fora da lista**; número sem equipe; equipe inativa; equipe sem unidade → nil em todos menos o primeiro); **`TestResolveAIAgent_PilotIsolation`** (organização com 3 números, todos com equipe e unidade, agente padrão listando só o número A: A resolve o agente; B e C não); `TestAIAgents_RejectsForeignAccountName`; `TestChatbotSettings_AgentFieldsRoundTrip`; `TestAIAgentAccountIssues`.
- [ ] **Step 2:** `go test ./internal/handlers -run "TestAIAgents|TestResolveAIAgent|TestChatbotSettings_AgentFields|TestAIAgentAccountIssues" -p 1 -v` → FAIL.
- [ ] **Step 3: Implementar**, copiando o padrão de `ListAIContexts`…`DeleteAIContext` (`chatbot.go:1235-1466`); validar tamanhos (nome ≤ 255, demais textos ≤ 4000) e que `default_ai_agent_id` é da organização.
- [ ] **Step 4:** mesmo comando → PASS. **Step 5:** commit `feat(ai): agent model, settings and resolution`.

### Task 2: Prompt em camadas e proteção comercial (complexidade: média)

**Files:**
- Create: `internal/handlers/ai_agent_prompt.go`, `internal/handlers/ai_guardrails.go` e os dois `_test.go`
- Modify: `internal/handlers/chatbot_processor.go` (`generateAIReply`:888, chamadores nas linhas ~414 e ~874), `internal/handlers/chatbot_graph_runner.go` (`execChatAIResponse`:~707)

**Interfaces:**
- Produces: `const aiPlatformRules string` (§5.3 + roteiro de qualificação + "pedido de humano ou qualificação concluída → emitir `[[TRANSFERIR]]`"); `func buildAgentPrompt(agent *models.AIAgent) string` (seções rotuladas: objetivo, tom, empresa, instruções, quando transferir); `const handoffMarker = "[[TRANSFERIR]]"`; `func extractHandoff(text string) (clean string, handoff bool)`; `func violatesCommercialSafety(text, allowedText string) bool` (compromissos fora de negação; "R$ n" ausente de `allowedText`; "n% de desconto"; afirmação de estoque/disponibilidade; "prazo/entrega em n dias"); `aiReply` ganha `Handoff string` (`""`, `"marker"`, `"unsafe"`).
- `generateAIReply` passa a resolver o agente (`resolveAIAgent`); com agente: plataforma + `buildAgentPrompt` no lugar de `SystemPrompt`, depois `AIContext` e conhecimento como hoje; resposta passa por `extractHandoff` e `violatesCommercialSafety`; violação → **uma** nova chamada com a instrução corretiva de §5.3 e, se violar de novo, `Handoff="unsafe"` sem texto. Sem agente: caminho atual intacto.

- [ ] **Step 1: Testes:** `TestBuildAgentPrompt`; `TestExtractHandoff` (fim, meio, repetido, sozinho); `TestViolatesCommercialSafety` (tabela: "garanto o preço" sim; "Não garanto estoque" não; "R$ 25,99" fora do texto configurado sim, dentro não; "10% de desconto" sim; "temos em estoque" sim; "o vendedor confirma o prazo" não; "prazo de 5 dias" sim); `TestGenerateAIReply_NoAgentIsUnchanged` (prompt enviado é exatamente o atual); `TestGenerateAIReply_UnsafeRetryThenHandoff` (provedor falso conta 2 chamadas).
- [ ] **Step 2:** `go test ./internal/handlers -run "TestBuildAgentPrompt|TestExtractHandoff|TestViolatesCommercialSafety|TestGenerateAIReply_" -p 1 -v` → FAIL.
- [ ] **Step 3: Implementar.** Normalização de caixa/acento e palavra inteira; "não/nao" até 3 palavras antes anula o compromisso. Provedor falso: o de `ai_runtime_test.go`.
- [ ] **Step 4:** PASS, depois `-run "AI|Chatbot"` sem regressão. **Step 5:** commit `feat(ai): layered prompt and commercial safety`.

### Task 3: Humano assumiu → agente cala (complexidade: média/alta; revisor independente)

**Files:**
- Modify: `internal/handlers/agent_transfers.go` (helper junto de `hasActiveAgentTransfer`:1422), `chatbot_processor.go` (envio ~414-424), `chatbot_graph_runner.go` (`execChatAIResponse`), e os caminhos de envio do atendente que a auditoria apontar
- Test: `agent_transfers_test.go`, `chatbot_processor_test.go`, `chatbot_graph_runner_test.go`

**Interfaces — Produces:**
- `func (a *App) humanTookOver(orgID, contactID uuid.UUID) bool` (true com `AgentTransfer` ativo; **ignora** `AssignedUserID`).
- `func contactLockKey(orgID, contactID uuid.UUID) int64` (hash estável) e `func (a *App) sendAIReplyIfNoHuman(ctx context.Context, orgID, contactID uuid.UUID, send func(context.Context) error) (sent bool, err error)`: abre transação, `SET LOCAL lock_timeout = '3s'`, `pg_advisory_xact_lock(key)`, reconfere `AgentTransfer` ativo **na mesma transação**; se ativo ou sem a trava, devolve `sent=false` e grava `AIUsageLog` (`ErrorKind="discarded_human"` ou `"discarded_lock_timeout"`); senão chama `send` **com a trava segurada** e só então confirma a transação.
- `createTransferRow` (`agent_transfers.go`) passa a executar o INSERT numa transação que adquire a **mesma** trava antes (§5.5). É o ponto único de criação de atendimento ativo; conferir por busca que nenhum outro `Create(&models.AgentTransfer` fica fora dele.
- Os dois caminhos de envio da IA (resposta direta e `execChatAIResponse`) enviam via `sendAIReplyIfNoHuman`.

- [ ] **Step 1: Testes:** `TestHumanTookOver` (ativo true; sem false; só `AssignedUserID` false); **corrida, determinística:** `TestAIReply_HumanCommitsBeforeCheck_NothingSent` (a transferência é confirmada antes da trava do agente → `send` nunca é chamado); `TestAIReply_HumanWaitsForInFlightSend` (o `send` do agente bloqueia num canal; o atendente tenta abrir o atendimento: o INSERT fica esperando; ao liberar o envio, o INSERT conclui; `sentAt < transferCommittedAt`); `TestAIReply_LockTimeoutDiscards` (outra transação segura a trava >3 s → descartado, nada enviado); **corrida, estresse:** `TestAIReply_Race_NoAgentMessageAfterTransfer` (200 rodadas com goroutines do agente e do atendente em ordem aleatória; invariante: nenhuma mensagem do agente com carimbo posterior ao `transferred_at` confirmado; usar relógio do banco); idem para o nó `ai_response`; `TestCreateTransferRow_TakesContactLock` (outra transação com a trava bloqueia o INSERT); `TestNoOtherActiveTransferInsertPath` (varre o código: só `createTransferRow` cria `AgentTransfer` ativo).
- [ ] **Step 2:** `go test ./internal/handlers -run "TestHumanTookOver|TestAIReply_|TestCreateTransferRow_|TestNoOtherActiveTransferInsertPath" -p 1 -v` → FAIL.
- [ ] **Step 3: Implementar** o helper, a trava no `createTransferRow` e plugar nos dois caminhos.
- [ ] **Step 4: Auditar** todo envio de atendente em conversa sem atendimento ativo (inbox, API, fila; buscar `sendAndSave*` e o handler de mensagens). Hoje `openAgentInitiatedTransfer` (`agent_transfers.go:~1670`) é o único criador de `agent_initiated`. Para cada caminho sem ele, escrever `TestAgentSend_OpensAttendance_<caminho>` e corrigir só essa lacuna.
- [ ] **Step 5:** PASS e `-run "AgentTransfer|Chatbot"`. **Step 6:** commit `feat(ai): stop the agent when a human takes over`.

### Task 4: Teto mensal de tokens com reserva (complexidade: média/alta; revisor independente)

**Files:**
- Create: `internal/handlers/ai_token_cap.go`, `internal/handlers/ai_token_cap_test.go`
- Modify: `internal/handlers/ai_runtime.go` (`completeAI`)

**Interfaces — Produces:**
- `const aiCallDeadline = 2 * time.Minute` e `const aiReservationStale = 10 * time.Minute` (validade muito maior que o prazo: chamada viva nunca perde a reserva, §5.7).
- `func (a *App) reserveAITokens(orgID uuid.UUID, limit, tokens int64, now time.Time) (*aiReservation, error)` (`ErrAICapReached` quando `consumo + tokens > limit`; transação com `pg_advisory_xact_lock` por organização; consumo = soma de `TotalTokens` das features `chatbot_reply`, `chatbot_flow_node`, `agent_qualification`, `ai_reservation_unreconciled` e de `ai_reservation` com menos de `aiReservationStale`, no mês civil em `America/Sao_Paulo`; insere a linha `ai_reservation`).
- `func (r *aiReservation) Settle(usageRecorded bool)`: com `true`, fecha a reserva (`ai_reservation_closed`, 0 tokens) **depois** de as linhas reais existirem; com `false`, converte em `ai_reservation_unreconciled` mantendo o valor reservado. Idempotente.
- `recordAIUsage` passa a devolver `error` (hoje só loga); os chamadores atuais continuam ignorando o retorno.
- `completeAI`, quando há agente: aplica `context.WithTimeout(ctx, aiCallDeadline)` a toda a chamada (inclusive o laço de ferramentas); reserva (`MaxTokens` + len(prompt)/3) se o teto > 0; grava o uso real; chama `Settle` num `defer`, em sucesso, erro ou cancelamento. Teto 0 ou sem agente: nenhuma consulta nova e comportamento atual.

- [ ] **Step 1: Testes:** `TestReserveAITokens_BelowAndAboveLimit`; `TestReserveAITokens_ConcurrentNeverExceeds` (N goroutines; soma reservada ≤ teto); `TestReserveAITokens_LongCallKeepsReservation` (relógio injetado: com a chamada ainda aberta aos 90 s, outra chamada que estouraria o teto continua recusada); `TestCompleteAI_DeadlineCancelsAndSettles` (provedor falso lento é cancelado em `aiCallDeadline`, reserva fechada); `TestSettle_UsageRecordedBeforeClose` (consulta concorrente durante o `Settle` nunca vê o consumo zerado); `TestSettle_UsageRecordFailureBecomesUnreconciled` (falha ao gravar o uso real: o valor reservado continua contando); `TestReserveAITokens_ReleasedOnProviderError`; `TestReserveAITokens_IgnoresAbandonedReservationOtherMonthsAndOrgs` (reserva com mais de 10 min é ignorada; outro mês e outra organização também); `TestCompleteAI_NoLimitNoExtraQueries`.
- [ ] **Step 2:** `go test ./internal/handlers -run "TestReserveAITokens|TestSettle_|TestCompleteAI_" -p 1 -v` → FAIL.
- [ ] **Step 3: Implementar.** **Step 4:** PASS. **Step 5:** commit `feat(ai): monthly token cap with reservations`.

### Task 5: Transferência do agente, qualificação e oportunidade (complexidade: alta; revisor independente)

**Files:**
- Create: `internal/handlers/ai_handoff.go`, `internal/handlers/ai_qualification.go` e os dois `_test.go`
- Modify: `internal/handlers/agent_transfers.go` (`transferToTeam` com resultado), `chatbot_processor.go`, `chatbot_graph_runner.go`

**Interfaces:**
- Consumes: `aiReply.Handoff` (T2), `humanTookOver` (T3), `reserveAITokens` (T4), `resolveAIAgent` (T1), `createOrRetriggerSalesOpportunity`.
- Produces: `func (a *App) transferToTeam(ctx, account, contact, teamID, source, notes, routingReason string) (TransferResult, error)` — extrai o miolo de `createTransferToTeam` (`agent_transfers.go:1766`), que passa a delegar a ele sem mudar comportamento; `type Qualification struct` com `Produto, Categoria, Ambiente, Intencao string`, `Quantidade *float64`, `Unidade *models.SalesUnitOfMeasure`, `Metragem *float64`, `Resumo string` e `NaoInformado []string`; `func (a *App) extractQualification(ctx, settings, account, contact, session) (*Qualification, error)` (feature `agent_qualification`, JSON `{campo:{valor,evidencia}}`, descarta campo cuja `evidencia` não é substring normalizada de uma mensagem do **cliente**; para metragem e quantidade aplica `validateNumericField`); `func validateNumericField(kind numericKind, value float64, unit string, customerMessages []string) bool` com `numericKind` ∈ {`kindArea`, `kindCount`}: extrai **todas** as menções do tipo (número + unidade compatível: m²/m2/metros quadrados para área; caixas/cx/peças/pacotes/unidades/metros para contagem) nas mensagens do cliente, descarta as negadas ou corrigidas ("não", "nem", "em vez de", "ao invés de", "menos" até 3 palavras antes, ou "X, não Y" negando Y) e aceita só se sobrar **exatamente um valor distinto igual a `value`** (ambíguo ou divergente → false → "não informado", §5.4); `func formatInterest(q *Qualification) string` (seções "Informado pelo cliente" e "Ainda não coletado" + resumo); `func (a *App) handoffFromAI(ctx, account, contact, session, settings, reason string)`.
- Regras de `handoffFromAI` (§5.4–5.6): 1) `humanTookOver` → retorna; 2) extrai (pula se `reason=="cap"`; erro → qualificação vazia com a pendência "qualificação não extraída"); 3) `createOrRetriggerSalesOpportunity(contact, nil, models.SalesOpportunityEventSourceSystem, formatInterest(q), nil, nil, apply)` com quantidade/unidade só se validadas; 4) `humanTookOver` de novo; 5) transfere com `transferToTeam` para `account.DefaultTeamID`; equipe inválida → `transferToQueue` com `routingReason="ai_no_unit_team"` e log de aviso; 6) **só com `TransferCreated`** envia a mensagem de espera fixa; `AlreadyActive` não envia nada; `OutsideHours` envia a mensagem de fora de horário existente uma vez (sinalizador `ai_handed_off` em `SessionData`) e a oportunidade já foi registrada.
- Falha do provedor em `generateAIReply`: sem mensagem de espera, comportamento atual de resposta padrão; contador `ai_failures` em `SessionData`; na 2ª falha seguida chama `handoffFromAI(..., "ai_failure")`. Sucesso zera o contador.

- [ ] **Step 1: Testes:** `TestValidateNumericField` (tabela: "40 m², não 20 m²" aceita 40 e rejeita 20; "quero 40 m2" aceita 40; "40 m² ou 50 m²" rejeita ambos (ambíguo); "em vez de 20 m² vai ser 40 m²" aceita 40; "40 caixas" rejeitado como área; "uns 40 metros quadrados" aceita; valor do modelo ausente das menções rejeitado; mesmas menções repetidas do mesmo valor aceitas); `TestExtractQualification_DropsUnsupportedFields` (cliente disse "uns 40 m²" e "porcelanato"; o modelo devolve também "ambiente: cozinha" sem citação → ambiente fica não informado); `TestExtractQualification_RejectsQuoteFromAssistantMessage`; `TestFormatInterest_SeparatesProvidedFromMissing`; `TestHandoffFromAI_CreatesOpportunityThenTeamTransfer`; `TestHandoffFromAI_ExtractionFailureStillTransfers`; `TestHandoffFromAI_CapSkipsExtraction`; `TestHandoffFromAI_NoDuplicateOnConcurrentCalls` (duas chamadas: 1 transferência, 1 mensagem de espera, 1 oportunidade); `TestHandoffFromAI_HumanTakesOverDuringExtraction`; `TestHandoffFromAI_InactiveTeamFallsBackToMarkedQueue`; `TestHandoffFromAI_OutsideHoursMessageOnce`; `TestProviderFailure_NoWaitMessageThenHandoffOnSecond`; `TestCreateTransferToTeam_BehaviourUnchanged`.
- [ ] **Step 2:** `go test ./internal/handlers -run "TestExtractQualification|TestFormatInterest|TestHandoffFromAI|TestProviderFailure|TestCreateTransferToTeam" -p 1 -v` → FAIL.
- [ ] **Step 3: Implementar** e ligar `Handoff` nos dois caminhos de envio (processador e nó `ai_response`, que passa a usar `generateAIReply`). Verificar e cobrir com teste que, após o vendedor pegar o atendimento, a oportunidade fica visível e atribuída a ele (`assignOpenSalesOpportunityToAgent` hoje depende de `AssignToSameAgent`); se não ficar, ajustar o mínimo.
- [ ] **Step 4:** PASS; depois `-run "AI|Chatbot|AgentTransfer|Sales"`. **Step 5:** commit `feat(ai): handoff with qualification and CRM opportunity`.

### Task 6: Interface mínima (complexidade: média)

**Files:**
- Create: `frontend/src/views/chatbot/AIAgentsView.vue` e `AIAgentsView.spec.ts`
- Modify: `frontend/src/services/api.ts` (~445), `frontend/src/router/index.ts`, `frontend/src/views/chatbot/ChatbotView.vue` (cartão ~263), `frontend/src/i18n/locales/pt-BR.json` e `en.json`

- [ ] **Step 1: Testes** (`AIAgentsView.spec.ts`): lista; criar/editar/apagar chamam a API com os campos de T1; ativar/desativar; escolher os **números permitidos** (multisseleção) e o agente padrão, e salvar o teto; o aviso lista, entre os números permitidos, os sem equipe de unidade; salvar sem nenhum número permitido mostra que o agente não atuará em lugar nenhum.
- [ ] **Step 2:** `npm --prefix frontend run test -- AIAgentsView` → FAIL.
- [ ] **Step 3: Implementar** uma view com diálogo de formulário, seguindo `AIContextsView.vue`; rota `/chatbot/agents`.
- [ ] **Step 4:** PASS + `npm --prefix frontend run typecheck` + `npm --prefix frontend run lint`. **Step 5:** commit `feat(ai): agents screen`.

### Task 7: Ponta a ponta e homologação (complexidade: média)

**Files:** Create `internal/handlers/ai_agent_e2e_test.go`, `docs/homologacao/agentes-ia-roteiro.md`.

- [ ] **Step 1: Teste E2E automatizado** `TestAIAgent_EndToEnd` (fixtures de `newGraphTestFixtures`, provedor de IA falso com respostas roteirizadas, envio do WhatsApp capturado): organização com equipe de unidade e número; agente padrão; (1) cliente inicia → agente responde e pergunta; (2) cliente informa produto, metragem e ambiente → agente emite o marcador; (3) existe 1 oportunidade com os campos "informado" e "não coletado" corretos, 1 transferência ativa para a equipe da unidade, 1 mensagem de espera, nenhum marcador no texto enviado; (4) novo cliente-mensagem com transferência ativa → o agente não responde; (5) atendente assume (envio) → `agent_initiated`/transferência ativa preservada e o agente segue calado; (6) outra organização não vê nada disso; (7) **dois números** na mesma organização, ambos com equipe e unidade, agente permitido só no número A: uma conversa pelo número B não recebe resposta do agente, nem oportunidade, nem transferência dele.
- [ ] **Step 2:** `go test ./internal/handlers -run TestAIAgent_EndToEnd -p 1 -v` → FAIL, implementar o que faltar, PASS.
- [ ] **Step 3: Roteiro de homologação** (`agentes-ia-roteiro.md`): um cenário por critério de liberação (1–10) mais os casos de §Review Focus; cada um com passos, resultado esperado e evidência (captura da conversa, linha do CRM, registro do `AIUsageLog`); a sequência completa vira o roteiro da demonstração à diretoria.
- [ ] **Step 4: Execução manual** em homologação com um número de WhatsApp de teste (organização de teste, unidade e equipe criadas): rodar o roteiro inteiro, anexar evidências, abrir defeitos. **Pedir revisão independente** das Tarefas 3, 4 e 5 (atendimento e segurança: isolamento por organização, vazamento de chave, envio indevido, duplicidade) antes do PR.
- [ ] **Step 5:** `go build ./... && go vet ./...`; suíte completa com `-p 1` (separar falhas preexistentes); commit `test(ai): agent end-to-end and homologation script`.

---

## Complexidade e esforço

| Tarefa | Complexidade | Revisor independente |
|---|---|---|
| 1 Agente, configurações, resolução | Média | não |
| 2 Prompt e proteção comercial | Média | não |
| 3 Humano assume | Média/alta | **sim** |
| 4 Teto com reserva | Média/alta | **sim** |
| 5 Transferência, qualificação, oportunidade | Alta | **sim** |
| 6 Interface | Média | não |
| 7 Ponta a ponta e homologação | Média | revisão final da branch |

## Entrada em produção

1. **Homologação:** roteiro da Tarefa 7 com número de teste; sem defeito de severidade alta aberto; evidências arquivadas.
2. **Piloto controlado:** uma unidade, **um número** (com equipe de unidade configurada) na lista `whatsapp_accounts` do agente (único mecanismo de habilitação; `ChatbotSettings` é por organização, §5.8), teto baixo, antes de ativar conferir pelo `readiness` e por uma conversa de teste no segundo número, acompanhamento diário de `AIUsageLog`, transferências e oportunidades. Demais números e organizações seguem como hoje.
3. **Desligar:** limpar o agente padrão ou desativar o agente interrompe tudo na hora, sem deploy.
4. **Deploy:** só com confirmação do Ivan, pelo script de deploy vigente. Ampliação para outras unidades só depois de uma semana de piloto sem incidente de atendimento.

## Cobertura da spec

§5.1 → T1; §5.2 → T1, T5; §5.3 → T2; §5.4 → T5; §5.5 → T3, T5; §5.6 → T5; §5.7 → T4; §6 → T6; §8 → T7.

---

## Desvios em relação ao plano, registrados na construção

- **Tarefa 1:** `default_ai_agent_id` e `ai_monthly_token_limit` ficaram dentro de `models.AIConfig` (`settings.AI.DefaultAgentID`, `settings.AI.MonthlyTokenLimit`), onde já estão os demais campos `ai_*`.
- **Tarefa 3:** além da trava por contato, a revisão independente levou a: semáforo de envios simultâneos do agente, espera limitada em `createTransferRow`, parada do fluxo (`halt`) quando o nó `ai_response` descarta, e a resposta padrão passando pela trava (ver spec §5.5).
- **Tarefa 4:** `aiReservation.Settle()` não recebe parâmetro; a falha de gravação do uso é avisada por `recordAIUsage` (que agora devolve erro) via `noteUsageFailure()`.
- **Tarefa 5:** `claimTransfer` passou a atribuir a oportunidade aberta a quem assume um atendimento de origem `ai_agent`, independentemente da opção "atribuir ao mesmo agente" (sem isso o vendedor não veria a oportunidade no funil). `createTransferToTeam` delega a `transferToTeam`, que devolve o resultado.
- **Tarefa 6:** uma única tela (`AIAgentsView.vue`) com diálogo; a escolha de números permitidos entrou na tela porque é ela que limita o piloto.
