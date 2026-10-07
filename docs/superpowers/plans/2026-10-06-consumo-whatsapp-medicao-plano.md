# Consumo do WhatsApp: base de medição — Plano de implementação

> **Para quem executar:** SUB-SKILL OBRIGATÓRIA: `superpowers:subagent-driven-development` (recomendado) ou `superpowers:executing-plans`. Os passos usam checkbox (`- [ ]`).
>
> **Estado: plano aprovado (revisão do dono). Implementação autorizada somente para a Entrega A; parar ao concluí-la.** Histórico: plano para revisão. Nenhum código, migration ou teste foi escrito.** Regra de trabalho: plano → revisão do dono → só então código. Sem push, PR, merge ou deploy nesta fase.

**Objetivo:** gravar, para cada mensagem do WhatsApp, quem a originou, o que a Meta informou sobre a cobrança e um custo estimado por tabela interna, mais o histórico de status do contato.

**Arquitetura:** novo pacote `internal/usage` (independente de `App`, só precisa de `*gorm.DB`, config e logger, para que o worker e o job o usem). Quatro tabelas novas (`message_usage`, `message_pricing_events`, `whatsapp_rates`, `contact_status_events`). A liquidação é uma **função pura do conjunto de eventos** (`Compute`), chamada pelo webhook, pelo envio (quando a linha ganha o wamid) e pelo job de integridade. A origem do envio viaja em `Origin` (campo de `OutgoingMessageRequest` + `context`).

**Stack:** Go (fastglue, GORM, koanf), Postgres, Redis (só nos testes), Vue 3 + Pinia + vue-i18n, Vitest, Playwright.

**Base:** `development`. Design aprovado: [`2026-10-06-consumo-whatsapp-medicao-design.md`](../specs/2026-10-06-consumo-whatsapp-medicao-design.md). Este plano usa as seções do design como `D§n`.

## Restrições globais

- A tabela `messages` **não é alterada**. Nenhuma tabela nova guarda conteúdo de mensagem nem telefone completo (só `recipient_country`).
- `billable` é **nulável**: `null` (desconhecido) ≠ `false`. Entrada = `false`.
- `estimated_cost` **nunca** é sobrescrito por valor da Meta (`meta_cost` é coluna separada e fica nula nesta fase).
- A Meta prevalece em categoria, cobrável e modelo; a tabela interna prevalece no valor.
- Gravar consumo **nunca bloqueia nem falha o envio** (log + contador).
- Os prazos de D§9 vêm **só** de `Config.Usage`; nenhum número de prazo no código. Valor inválido falha o load da config. Testes injetam os valores.
- Idempotência garantida **no banco** (índices únicos + `ON CONFLICT DO NOTHING`).
- `unlinked` não é perda; `unconfirmed` é classificação operacional, reliquida se a entrega chegar.
- Migration aditiva e idempotente (nenhum `DROP`, nenhuma alteração de coluna existente).
- Toda query nova é escopada por `organization_id`.
- Testes Go exigem `TEST_DATABASE_URL` e `TEST_REDIS_URL` (containers `whatomate_test_pg`/`whatomate_test_redis`); rodar com `-p 1`.
- Textos de UI em `pt-BR` e `en` (i18n), Vue com `<script setup>`; filtros de unidade usam `selectableOptions`.
- Fora de escopo: dashboard de eficiência, conciliação com `pricing_analytics`, retroativo, voz/VoIP, tela do histórico de status.

---

## Itens 1–7: mapa da mudança

### (1) Migrations

| O quê | Onde | Como |
|---|---|---|
| 4 modelos novos | `internal/models/usage.go` (novo): `MessageUsage`, `MessagePricingEvent`, `WhatsAppRate`; `internal/models/contact_status_event.go` (novo): `ContactStatusEvent` | Structs com tags GORM; `uuid` PK, `organization_id` com FK como nos demais modelos |
| Registro no AutoMigrate | `internal/database/postgres.go` `GetMigrationModels()` (hoje `{"Message", &models.Message{}}` na linha ~87) | Acrescentar as 4 entradas **no fim** da lista (aditivo) |
| Índices únicos parciais | `internal/database/postgres.go` `getIndexes()` (~287) | SQL idempotente (`CREATE UNIQUE INDEX IF NOT EXISTS`): `message_usage(message_id) WHERE message_id IS NOT NULL`; `message_usage(organization_id, whatsapp_account, wamid) WHERE wamid <> ''`; `message_pricing_events(organization_id, whatsapp_account, wamid, status, event_at)`; `whatsapp_rates(organization_id, country, category, valid_from)` |
| Índices de consulta | idem | `message_usage(organization_id, sent_at)`, `(organization_id, billing_state)`, `(organization_id, link_state, created_at) WHERE link_state='unlinked'`; `contact_status_events(contact_id, occurred_at)` |
| Limpeza nos testes | `test/testutil/db.go` (as **duas** listas de truncate, ~215–240) | Incluir as 4 tabelas, antes das tabelas-pai / com `CASCADE` |
| Teste de migração | `internal/database/postgres_test.go` (ou arquivo vizinho existente) | Rodar a migração duas vezes (idempotente); índices existem; `messages` sem coluna nova |

Padrão a copiar para índice parcial: `internal/database/active_transfer_index.go`.

### (2) Domínio e repositório

Novo pacote `internal/usage` (não importa `handlers`):

| Arquivo | Responsabilidade | Interface (assinaturas exatas) |
|---|---|---|
| `types.go` | Enums e `Origin` | `type ActorType string` (`agent, ai, flow, campaign, system, external_app, contact`); `type BillingState string` (`pending, awaiting_pricing, priced, not_billable, no_rate, unconfirmed, send_failed, failed`); `type LinkState string` (`linked, unlinked`); `type UnitSource string` (`agent, team, none`); `type Origin struct { ActorType ActorType; ActorUserID *uuid.UUID; TeamID *uuid.UUID; FlowID *uuid.UUID; FlowNodeID string; CampaignID *uuid.UUID; OccurrenceID *uuid.UUID; AIUsageLogID *uuid.UUID; Detail string; Inferred bool }` |
| `origin_ctx.go` | Origem no contexto | `func WithOrigin(ctx context.Context, o Origin) context.Context`; `func OriginFrom(ctx context.Context) (Origin, bool)` |
| `rates.go` | Preço vigente | `func (r *Recorder) LookupRate(ctx context.Context, orgID uuid.UUID, country, category string, at time.Time) (*models.WhatsAppRate, error)` (prefixo E.164 mais longo → `*`) |
| `compute.go` | **Liquidação pura** | `type Settlement struct { BillingState BillingState; Billable *bool; BillingCategory string; PricingModel string; ConversationID string; MetaPricing, History json…; CategoryDiverged bool; EstimatedCost *decimal; Currency string; RateID *uuid.UUID }`; `func Compute(row models.MessageUsage, events []models.MessagePricingEvent, rate *models.WhatsAppRate, now time.Time, cfg config.UsageConfig) Settlement` (sem I/O; testável em tabela) |
| `record.go` | Criar a linha | `type Recorder struct { DB *gorm.DB; Cfg config.UsageConfig; Log logf.Logger }`; `func (r *Recorder) Record(ctx context.Context, tx *gorm.DB, msg *models.Message, o Origin) error` (`INSERT … ON CONFLICT DO NOTHING`; resolve unidade agente→equipe→none; erro só é logado pelo chamador) |
| `settle.go` | Aplicar | `func (r *Recorder) Settle(ctx context.Context, orgID uuid.UUID, account, wamid string) error` (lê linha + eventos + taxa, chama `Compute`, `UPDATE` condicional); `func (r *Recorder) AttachWamid(ctx context.Context, messageID uuid.UUID, orgID uuid.UUID, account, wamid string) error` (preenche `wamid`, associa `unlinked`, chama `Settle`) |
| `events.go` | Registrar evento da Meta | `func (r *Recorder) RecordStatusEvent(ctx context.Context, ev StatusEvent) (inserted bool, err error)` (`ON CONFLICT DO NOTHING`; `inserted=false` = duplicado) |
| `integrity.go` | Job | `func (r *Recorder) Sweep(ctx context.Context, now time.Time) (SweepResult, error)` (ver item 5) |
| `summary.go` | Leitura | `func (r *Recorder) Summary(ctx, orgID, Filter) (Summary, error)`; `func (r *Recorder) List(ctx, orgID, Filter, Page) ([]models.MessageUsage, int64, error)`; `func (r *Recorder) Reprice(ctx, orgID) (RepriceResult, error)` |

Config (`internal/config/config.go`): `type UsageConfig struct { RecordEnabled bool; UnlinkedAfterMinutes, UnlinkedAttentionHours, IntegrityIntervalMinutes, IntegrityWindowHours, UndeliveredAfterHours int }`, campo `Usage UsageConfig \`koanf:"usage"\`` em `Config` (linha ~18–35). Defaults de D§9; chave ausente = default; `record_enabled` ausente = `true`; valor não numérico, zero ou negativo → erro no load. Seguir o padrão de `*int`/limites já usado em `config.go`. Exemplo em `config.example.toml`; testes em `internal/config/config_test.go`.

`App` (`internal/handlers`): novo campo `Usage *usage.Recorder` (nil-safe: todos os pontos checam `a.Usage != nil && cfg.RecordEnabled`). Construído em `cmd/whatomate/main.go` e injetado no `App` e no `Worker`.

### (3) Pontos de envio

`Origin` entra em `OutgoingMessageRequest` (`messages.go`) como campo opcional `Origin usage.Origin`. O ponto central grava:

- `createOutgoingMessage` (`messages.go:342`) cria a `Message` `pending`; logo depois (mesma tx, se houver) → `a.Usage.Record(ctx, tx, msg, req.Origin)`.
- `finalizeMessageSend` (`messages.go:492`): com wamid aceito → `AttachWamid` (liquida eventos que chegaram antes); com erro da API → marca `send_failed`.

| Caso (D§5) | Arquivo / função | `Origin` entregue | Observação |
|---|---|---|---|
| Agente, chat | `messages.go:1090` (chamada a `SendOutgoingMessage`) | `agent` + `ActorUserID` (de `opts.SentByUserID`) | |
| Agente, contato | `contacts.go:718`, `:909` | `agent` | |
| Protocolo | `occurrence_reply.go:61`, `occurrence_send.go:89` | `agent` + `OccurrenceID` | |
| SLA | `sla_processor.go:381`, `:648`, `:686` | `system`, `Detail=sla` | |
| Legado / palavra-chave | `chatbot_processor.go:558`, `:626`, `:671`, `:687` | `flow`, `Detail=keyword` | |
| Fluxo (grafo) | `chatbot_graph_runner.go` (9 chamadas `sendAndSave*`) | `flow` + `FlowID` + `FlowNodeID` | via `context` |
| Fluxo legado e respostas | `chatbot_processor.go` (15 chamadas `sendAndSave*`; `Detail=chatbot_reply`) | `flow` | via `context` |
| Transferir | `agent_transfers.go` (3 `sendAndSave*`) | `flow` + `FlowID`/`FlowNodeID` | via `context` |
| IA | `generateAIResponse` e `ai_tool_confirmation.go` (2 `sendAndSave*`) | `ai` + `Detail=<feature>` + `AIUsageLogID` | via `context` |
| Campanha | `internal/worker/worker.go` `HandleRecipientJob` (linha 69): envia **e só depois** cria a `Message` | `campaign` + `CampaignID` + template | gravar `Record` ao criar a `Message`; `AttachWamid` com o wamid já conhecido |
| Eco do app Business | `webhook.go` `processMessageEcho` (623) | `external_app` | `Record` direto |
| Entrada | `chatbot_processor.go` `saveIncomingMessage` (1342) | `contact`, `billable=false`, `not_billable` | `Record` direto |

Os 4 helpers `sendAndSaveTextMessage` (`chatbot_processor.go:556`), `…InteractiveButtons` (570), `…CTAURLButton` (669), `…FlowMessage` (685) hoje usam `context.Background()` e não recebem origem. Mudança **mecânica e mínima**: cada helper passa a ler `usage.OriginFrom(ctx)` de um `ctx` recebido por parâmetro; os 29 chamadores passam `usage.WithOrigin(ctx, …)`. Sem `Origin` no contexto o helper grava `system` + `Detail=unclassified` + `Inferred=true` (o job de integridade também cobre).

Template × texto livre: template grava `template_name`, `template_id`, `declared_category`; demais tipos `service` provisório. Mídia, interativa e texto: `message_type` da própria `Message`.

### (4) Webhook e liquidação

`internal/handlers/webhook.go`:

- `processStatusUpdate` (368): passa a (a) chamar `a.Usage.RecordStatusEvent` com o evento (status, `event_at` do payload, `pricing`/`conversation` **se vierem**, `recipient_id` → país, erro se `failed`) **antes** de `updateMessageStatus`; (b) se `inserted == false`, não recalcula; (c) chamar `a.Usage.Settle(...)` do wamid.
- `updateMessageStatus` (403) e `statusPriority` (385): **inalterados**. O filtro de progressão continua só para o `status` da mensagem; pricing de `sent` tardio é aproveitado porque o evento já foi gravado antes do filtro.
- `Compute` (função pura) aplica D§6.2 e D§7: `billable` da Meta; `failed` sem pricing → `false`; entregue sem pricing → `awaiting_pricing`; pricing mais completo por `event_at`; estado final de maior prioridade (`failed` vence); pricing diferente → recalcula, move o anterior para `meta_pricing_history` (máx. 5), marca `repriced_at`; sem preço → `no_rate`; passado `undelivered_after_hours` sem `delivered|read|failed` → `unconfirmed` (custo 0, reliquida se chegar).
- Evento antes da linha: fica em `message_pricing_events`; `AttachWamid` (envio) e o job disparam `Settle` depois.
- Erro de liquidação nunca devolve erro HTTP ao webhook (log + contador); a Meta não deve reenviar por causa disso.

### (5) Job de integridade

Padrão: `internal/handlers/ai_tool_reconciler.go`; fiação em `cmd/whatomate/main.go` (~310–405) com `time.Ticker` de `Cfg.IntegrityIntervalMinutes`, cancelável pelo contexto de shutdown. Lógica em `usage.Recorder.Sweep`:

1. **Completude:** mensagens da janela `IntegrityWindowHours` sem `message_usage` → criar com `Inferred=true` (`SentByUserID` → `agent`; `campaign_id` no metadata → `campaign`; resto → `system` + `Detail=unclassified`).
2. **`unlinked`:** wamid com eventos em `message_pricing_events`, sem linha e há mais de `UnlinkedAfterMinutes` → criar `message_usage` com `link_state=unlinked`; se a `Message` já tiver o wamid, associar (`linked`).
3. **`unconfirmed`:** `pending` com mais de `UndeliveredAfterHours` → `Settle` (que decide `unconfirmed`).
4. **Paginado por chave** (keyset por `created_at, id`), em lotes de **500**, continuando de lote em lote até esgotar a janela; **nunca carrega a janela inteira em memória**. O 500 é um **limite técnico interno, constante privada do pacote, não configurável** (não é regra operacional nem de cobrança; pode mudar internamente se o volume pedir). Idempotente e seguro para execução concorrente (duas instâncias ou corrida com o envio): garantido pelos índices únicos e `ON CONFLICT DO NOTHING`.
5. `SweepResult` (criadas, associadas, unconfirmed, erros) vai para o log e para contadores.

### (6) `contact_status_events`

Ponto único: `transitionContactStatusDB` (`internal/handlers/contact_status.go:135`). Hoje recebe `(db, contact, to, from)`. Passa a receber também `cause StatusCause` (`ActorType`, `ActorUserID`, `Reason`) e, **quando `changed == true`, insere o evento na mesma `db`/tx** (rollback não deixa evento órfão).

Chamadores (todos mapeados):

| Chamador | Local | `Reason` / `ActorType` |
|---|---|---|
| Mudança manual (API) | `contact_status.go:88` | `api` / `agent` |
| `releaseContactTx` → resolve | `contact_status.go:269` (usado por `contact_status.go:217`, `agent_transfers.go:671` e `:740`) | `resolve_action` ou `transfer` / `agent` ou `flow` (vem do `reason` já recebido) |
| Reabertura por mensagem do cliente | `chatbot_processor.go:1409` | `inbound_reopen` / `contact` |
| Primeira resposta do agente (new → in_progress) | `messages.go:321` | `agent_reply` / `agent` |

`transitionContactStatus` (117) repassa o `cause`. Nota: D§4.5 lista `reason` ∈ {`resolve_action`, `auto_resolve`, `inbound_reopen`, `transfer`, `api`}; a primeira resposta do agente (new→in_progress) é uma transição real e precisa ser distinguível no histórico; **decisão aprovada:** o conjunto fechado de motivos é `agent_reply` + os 5 do design, com `auto_resolve` reservado e sem uso enquanto não houver caminho no código. **Nenhum outro motivo é criado por antecipação.**

### (7) API, permissões e UI

**Permissão** `whatsapp_usage:read|write`:

- `internal/models/roles.go`: constantes + admin (ambas) + manager (leitura).
- `internal/models/permission_catalog.go`: grupo Análises; o teste do catálogo passa de **117 → 119** chaves.
- `internal/database/permissions_backfill.go` (+ chamada em `cmd/whatomate/main.go` ~180–215): backfill aditivo e idempotente para roles existentes (admin: read+write; manager: read).
- i18n do nome/descrição da permissão (`pt-BR`, `en`) e specs frontend que listam permissões.

**Rotas** (`cmd/whatomate/main.go`, grupo autenticado; handlers em `internal/handlers/whatsapp_usage.go` e `whatsapp_rates.go`):

| Rota | Função | Permissão |
|---|---|---|
| `GET /api/whatsapp-rates` | listar vigências | read |
| `POST /api/whatsapp-rates` | criar; **recusa sobreposição** (país+categoria) e `valid_to < valid_from` | write |
| `PUT /api/whatsapp-rates/{id}` | editar **só** se nunca usada (`rate_id` não referenciado); senão 409 → criar nova vigência | write |
| `DELETE /api/whatsapp-rates/{id}` | só se nunca usada | write |
| `GET /api/whatsapp-usage/summary` | totais por moeda, `awaiting_pricing`, `no_rate`, não classificadas, `unlinked` (+ antigos), `provisional_cost` | read |
| `GET /api/whatsapp-usage/messages` | lista paginada sem conteúdo | read |
| `POST /api/whatsapp-usage/reprice` | reliquida `no_rate`, `awaiting_pricing`, `unconfirmed` | write |

Filtros: período (fuso da organização), conta, unidade, agente, categoria, direção; agrupar por dia, categoria, unidade, agente ou conta. Moedas **nunca somadas**. `provisional_cost` calculado em leitura, nunca gravado.

**Frontend** (`frontend/src`):

| Arquivo | Mudança |
|---|---|
| `services/api.ts` | `whatsappUsageService`, `whatsappRatesService` |
| `views/settings/WhatsAppUsageView.vue` (novo) | duas abas: **Consumo** (filtros, cards de totais por moeda, tabela agrupável) e **Tabela de preços** (CRUD de vigências, aviso de sobreposição) |
| `components/layout/navigation.ts` | Canais: hoje grupo de um item (Contas) → entrada **Consumo do WhatsApp**, visível com `whatsapp_usage:read` |
| `router/index.ts` | rota nova junto de `accounts` e entrada em `navigationOrder` (~447) |
| `i18n/locales/pt-BR.json`, `en.json` | chaves `usage.*`, permissões |
| Testes | Vitest do store/filtros; Playwright em `e2e/` |

---

## Item 8: sequência de implementação

Branch de trabalho: `feature/whatsapp-usage-measurement`, a partir de `development` (confirmar a branch antes de começar). Três entregas, cada uma com seu próprio PR (**só abertos com ordem do dono**), nesta ordem, porque **a coleta de dados é o que não pode esperar** e a tela é a última:

**Entrega A — Fundação (migrations, configuração, estruturas, domínio e testes da parte pura). Nada é ligado a envio, webhook ou status; a coleta NÃO começa.**
1. Task 1: `Config.Usage` (+ validação, example, testes).
2. Task 2: modelos, migrations, índices, testutil.
3. Task 3: pacote `usage`: tipos, `Compute` puro, `LookupRate`, `Record`, `RecordStatusEvent`, `Settle`, `AttachWamid` (testes de unidade e de banco).

**Entrega B — Gravação. É o ponto formal de início da coleta real** (pontos de envio, webhook, `message_pricing_events`, integridade e histórico de status). **B não entra em produção sem autorização explícita do dono.**
4. Task 4: `contact_status_events` (transição de status; agora parte de B).
5. Task 5: `Origin` + ponto central (`createOutgoingMessage`/`finalizeMessageSend`) + chamadores diretos de `SendOutgoingMessage`.
6. Task 6: contexto de origem nos 4 helpers `sendAndSave*` e nos 29 chamadores; worker de campanha; eco; entrada.
7. Task 7: webhook (`processStatusUpdate`).
8. Task 8: job de integridade e fiação em `main.go`.

**Entrega C — Leitura e operação**
9. Task 9: permissão `whatsapp_usage` (catálogo 117→119, roles, backfill, i18n).
10. Task 10: handlers/rotas de preços e consumo + `Reprice`.
11. Task 11: frontend (serviços, view, navegação, rota, i18n) + Vitest.
12. Task 12: E2E Playwright + manuais (guia do admin, manual completo, manual dev) + verificação final.

Cada task segue TDD (teste que falha → código mínimo → teste passa → commit). Ordem justificada: A tem a menor superfície de risco e destrava testes; B altera o caminho quente de envio e webhook, por isso vem com a regressão completa dos testes existentes de status; C só lê.

Fluxo: **A → revisão → B → revisão/validação → C**. A coleta começa em B, antes da API e da tela (nada disso precisa existir para acumular dados). Pontos de parada para revisão do dono: ao fim de A (a implementação **para** ali), ao fim de B (antes de qualquer deploy: B passa a gravar em produção) e ao fim de C. `usage.record_enabled=false` é o freio de emergência em B.

---

## Tasks

### Task 1: Configuração `[usage]`

**Files:** Modify `internal/config/config.go`, `config.example.toml`, `internal/config/config_test.go`.
**Produces:** `config.UsageConfig` e `Config.Usage` (assinaturas no item 2).

- [ ] Teste falho: defaults quando a seção é ausente (`RecordEnabled=true`, 15, 24, 5, 48, 72).
- [ ] Teste falho: valores válidos sobrescrevem; `0`, negativo e texto fazem o load falhar (uma tabela, uma linha por chave).
- [ ] Implementar a struct, defaults e validação.
- [ ] Documentar a seção em `config.example.toml` (com a nota "parâmetros operacionais, não regras de cobrança").
- [ ] `go test ./internal/config/... -v` → PASS.
- [ ] Commit `feat(usage): [usage] config section with validated operational parameters`.

### Task 2: Modelos, migrations e índices

**Files:** Create `internal/models/usage.go`, `internal/models/contact_status_event.go`. Modify `internal/database/postgres.go` (`GetMigrationModels`, `getIndexes`), `test/testutil/db.go` (2 listas). Test `internal/database/postgres_test.go`.
**Produces:** `models.MessageUsage`, `models.MessagePricingEvent`, `models.WhatsAppRate`, `models.ContactStatusEvent` com as colunas de D§4.

- [ ] Teste falho: após migrar duas vezes, as 4 tabelas existem e os índices únicos parciais existem (`pg_indexes`).
- [ ] Teste falho: inserir dois `message_usage` com o mesmo `message_id`, e dois com o mesmo `(org, account, wamid)` → o segundo falha; `wamid=''` repetido **não** conflita.
- [ ] Teste falho: `messages` não ganhou coluna (comparar `information_schema` com o conjunto atual).
- [ ] Implementar modelos, registro, índices e limpeza do testutil.
- [ ] `go test ./internal/database/... ./test/... -p 1` → PASS.
- [ ] Commit `feat(usage): models and idempotent migrations for message usage ledger`.

### Task 3: Pacote `internal/usage`

**Files:** Create `internal/usage/{types,origin_ctx,rates,compute,record,settle,events}.go` e `*_test.go`.
**Consumes:** Task 1 (`UsageConfig`), Task 2 (modelos).
**Produces:** a API do item 2.

- [ ] Testes falhos de `Compute` (tabela, sem banco, `now` e `cfg` injetados): cada linha de D§6.2 e D§7 — `sent`/`delivered`/`read` em **todas as permutações** com o mesmo resultado; evento sem pricing; entregue sem pricing → `awaiting_pricing` e `Billable=nil`; `billable=false` → custo 0 `not_billable`; cobrável sem preço → `no_rate`; `failed` vence; `unconfirmed` só após `UndeliveredAfterHours` **da config injetada** e reliquida com `delivered`; pricing igual → sem mudança; pricing diferente → histórico (máx. 5) e `repriced_at`; categoria divergente (`CategoryDiverged`, Meta prevalece).
- [ ] Testes falhos de `LookupRate`: limites inclusivos de `valid_from`/`valid_to`, prefixo mais longo, fallback `*`, moedas distintas.
- [ ] Testes falhos de `Record`: idempotência (duas vezes, concorrente com `-race`), unidade agente → equipe → none com `unit_source`, nada de conteúdo/telefone gravado, falha de insert não propaga pânico.
- [ ] Testes falhos de `RecordStatusEvent` (duplicado → `inserted=false`) e `AttachWamid` (evento anterior à linha é liquidado; `unlinked` vira `linked`).
- [ ] Implementar o mínimo para passar; `Compute` sem I/O.
- [ ] `go test ./internal/usage/... -race -p 1` → PASS.
- [ ] Commit `feat(usage): usage recorder, rate lookup and pure settlement`.

### Task 4: `contact_status_events`

**Files:** Modify `internal/handlers/contact_status.go` (`transitionContactStatus`, `transitionContactStatusDB`), `chatbot_processor.go:1409`, `messages.go:321`, `agent_transfers.go`; Test `internal/handlers/contact_status_test.go`.
**Produces:** `type StatusCause struct{ ActorType string; ActorUserID *uuid.UUID; Reason string }`; novas assinaturas com `cause`.

- [ ] Teste falho por caminho (um evento exato cada): manual API; resolve via `releaseContact`; resolve dentro de transferência (`releaseContactTx`); reabertura inbound; primeira resposta do agente.
- [ ] Teste falho: sem mudança (`changed=false`) → nenhum evento; transação revertida → nenhum evento órfão; duas transições concorrentes → um evento.
- [ ] Implementar `cause` e o insert na mesma `db`/tx.
- [ ] Regressão: `go test ./internal/handlers/ -run 'ContactStatus|Release|Transfer' -p 1`.
- [ ] Commit `feat(contacts): record every contact status transition in contact_status_events`.

### Task 5: Origem e gravação no ponto central

**Files:** Modify `internal/handlers/messages.go` (`OutgoingMessageRequest`, `createOutgoingMessage`, `finalizeMessageSend`), `contacts.go`, `occurrence_reply.go`, `occurrence_send.go`, `sla_processor.go`, `cmd/whatomate/main.go` (construir `Recorder`), `internal/handlers/export_test.go`.
**Consumes:** Task 3. **Produces:** campo `OutgoingMessageRequest.Origin usage.Origin`; `App.Usage`.

- [ ] Teste falho por ponto (D§10.4): agente chat, contato, protocolo (`occurrence_id`), SLA → linha com `actor_type`/`origin_detail` corretos.
- [ ] Teste falho: envio aceito → `AttachWamid` (wamid preenchido); API recusou → `send_failed`, custo 0; `record_enabled=false` → nenhuma linha e envio normal; falha do `Record` (injetada) → envio **segue**.
- [ ] Implementar.
- [ ] Regressão dos testes de `messages_test.go`.
- [ ] Commit `feat(usage): record message usage at the central send path`.

### Task 6: Origem nos helpers, campanha, eco e entrada

**Files:** Modify `chatbot_processor.go` (`sendAndSave*`, `saveIncomingMessage`, 15 chamadas), `chatbot_graph_runner.go` (9), `agent_transfers.go` (3), `ai_tool_confirmation.go` (2), `internal/worker/worker.go`, `webhook.go` (`processMessageEcho`).

- [ ] Teste falho por ponto de envio da tabela do item 3: fluxo (com `flow_id`/`flow_node_id`), palavra-chave, resposta do chatbot, transferir, IA (feature + `ai_usage_log_id`), confirmação de transferência, campanha (`campaign_id`, template, `HandleRecipientJob` cria a `Message` depois do envio e ainda assim fica `linked`), eco (`external_app`), entrada (`contact`, `not_billable`).
- [ ] Teste falho: sem `Origin` no contexto → `system`/`unclassified`/`inferred`.
- [ ] Implementar a passagem de `context` (mudança mecânica nas 29 chamadas).
- [ ] Regressão: `go test ./internal/handlers/ ./internal/worker/... -p 1`.
- [ ] Commit `feat(usage): attribute bot, AI, campaign, echo and inbound messages`.

### Task 7: Webhook

**Files:** Modify `internal/handlers/webhook.go` (`processStatusUpdate`); Test `internal/handlers/webhook_test.go`.

- [ ] Testes falhos (D§10.2): evento repetido (não duplica nem recalcula); permutações `sent/delivered/read`; pricing tardio igual e diferente; evento **sem** pricing registrado; entregue sem pricing; não cobrável; evento **antes** da linha liquidado depois; `unlinked` associado; `failed`; `sent` tardio depois de `delivered` ainda contribui com pricing **e** o `status` da mensagem não regride.
- [ ] Teste falho: erro interno de liquidação não muda a resposta HTTP do webhook.
- [ ] Implementar (gravar evento antes do filtro de progressão; `Settle` depois).
- [ ] Regressão completa dos testes de status existentes (`webhook_test.go`) **sem alteração neles**.
- [ ] Commit `feat(usage): settle message usage from Meta status webhooks`.

### Task 8: Job de integridade

**Files:** Create `internal/usage/integrity_test.go`; Modify `internal/usage/integrity.go`, `cmd/whatomate/main.go`.

- [ ] Testes falhos: mensagem sem linha na janela ganha linha inferida (3 regras de origem); fora da janela não; `unlinked` após `UnlinkedAfterMinutes` (e **não** antes); associa depois; `pending` antigo → `unconfirmed`; segunda passada não muda nada; corrida com `Record` não duplica; todos os prazos vêm da config injetada.
- [ ] Implementar `Sweep` em lotes e a fiação do ticker com shutdown limpo.
- [ ] `go test ./internal/usage/... -race -p 1`.
- [ ] Commit `feat(usage): integrity sweep for missing, unlinked and unconfirmed usage`.

### Task 9: Permissão `whatsapp_usage`

**Files:** Modify `internal/models/roles.go`, `permission_catalog.go` (+ teste 117→119), `internal/database/permissions_backfill.go` (+ chamada em `main.go`), i18n, specs frontend de permissões.

- [ ] Testes falhos: catálogo com 119 chaves; admin read+write; manager só read; backfill adiciona e **é idempotente** (rodar duas vezes) e não mexe em roles personalizadas além do aditivo definido.
- [ ] Implementar.
- [ ] `go test ./internal/models/... ./internal/database/... -p 1`; `npm run test` nos specs de permissões.
- [ ] Commit `feat(permissions): whatsapp_usage read/write with additive backfill`.

### Task 10: API de preços e consumo

**Files:** Create `internal/handlers/whatsapp_rates.go`, `whatsapp_usage.go` e testes; Modify `cmd/whatomate/main.go` (rotas).

- [ ] Testes falhos: CRUD de vigência; sobreposição recusada; edição/remoção de taxa usada → 409; permissões (manager lê e não escreve; sem permissão → 403); **isolamento entre organizações**; `summary` com moedas separadas, contagens de cada estado, `unlinked` antigo destacado pelo `unlinked_attention_hours`, `provisional_cost` não persistido; `messages` paginado **sem conteúdo**; `reprice` reliquida `no_rate`/`awaiting_pricing`/`unconfirmed`.
- [ ] Implementar.
- [ ] `go test ./internal/handlers/ -run 'WhatsAppRates|WhatsAppUsage' -p 1`.
- [ ] Commit `feat(usage): rates and usage API`.

### Task 11: Frontend

**Files:** Create `frontend/src/views/settings/WhatsAppUsageView.vue`; Modify `services/api.ts`, `components/layout/navigation.ts`, `router/index.ts`, i18n `pt-BR`/`en`; Test Vitest.

- [ ] Testes Vitest falhos: item de navegação só com `whatsapp_usage:read`; aba de preços só edita com `write`; filtro de unidade usa `selectableOptions`; totais renderizados **por moeda**.
- [ ] Implementar a view (duas abas) e as chaves i18n.
- [ ] `npm run test`, `npm run build`, lint/typecheck do projeto → sem erros.
- [ ] Commit `feat(usage): WhatsApp usage screen and price table UI`.

### Task 12: E2E, manuais e verificação final

**Files:** Create `frontend/e2e/…/whatsapp-usage.spec.ts`; Modify `manuais/*.html` (não versionados; atualizar localmente) e, se aplicável, doc técnico.

- [ ] E2E (config `-config` explícito, nunca `WHATOMATE_CONFIG`): cadastrar preço → simular mensagem e webhook → ver o total na tela; manager só lê.
- [ ] Atualizar manual do admin (tela e tabela de preços), manual completo e manual dev (tabelas, estados, job, `[usage]`).
- [ ] Rodar a suíte inteira (Go `-p 1`, Vitest, E2E) e comparar com a linha de base de falhas pré-existentes.
- [ ] Commit `docs(usage): manuals and e2e for WhatsApp usage measurement`.

---

## Item 9: estratégia de testes (matriz)

| Requisito (D§10) | Onde é provado |
|---|---|
| 1. Mensagem = no máximo 1 registro | Task 2 (índices recusam SQL direto), Task 3 (`Record` repetido/concorrente `-race`), Task 7 (webhook repetido), Task 8 (corrida job × envio) |
| 2. Webhook | Task 3 (`Compute` em tabela) + Task 7 (ponta a ponta) |
| 3. Cálculo | Task 3 (`LookupRate`, `Compute`) |
| 4. Origem por ponto de envio + unidade | Tasks 5 e 6 (um teste por linha do item 3) |
| 5. Status em mesma transação | Task 4 |
| 6. Segurança e dados | Task 2 (migração aditiva, sem conteúdo), Task 9 (permissões), Task 10 (isolamento de org, manager) |
| Configuração injetada | Task 1, Task 3, Task 8 (prazos nunca por constante) |
| Regressão | Testes existentes de `webhook_test.go`, `messages_test.go`, `contact_status_test.go` rodam **sem alteração** |

Princípios: lógica de liquidação testada **sem banco** (`Compute` pura); tudo que toca banco usa Postgres/Redis reais dos containers; relógio e config sempre injetados; `-race` em `usage`.

---

## Item 10: critérios de aceite objetivos

Cada critério é verificável por comando ou consulta. A entrega só fecha com todos satisfeitos.

| # | Critério | Como verificar |
|---|---|---|
| A1 | Migração aditiva e idempotente: 4 tabelas novas, `messages` sem coluna nova, segunda execução não altera nada | teste da Task 2 |
| A2 | Nenhum preço, prazo ou categoria embutido no código | `grep` de literais de prazo em `internal/usage` e `handlers` = vazio; testes passam com valores de config diferentes dos defaults |
| A3 | Config inválida (`0`, negativo, texto) impede o carregamento | teste da Task 1 |
| B1 | Todo ponto de envio da tabela do item 3 grava exatamente **1** linha com a origem esperada | testes das Tasks 5 e 6 |
| B2 | `SELECT count(*) FROM (SELECT message_id FROM message_usage WHERE message_id IS NOT NULL GROUP BY 1 HAVING count(*)>1) t` = 0 e idem para `(org, account, wamid)` | teste de integração + consulta de verificação |
| B3 | Webhooks duplicados ou fora de ordem levam ao mesmo estado final; evento sem pricing não vira `billable=false` | testes da Task 7 |
| B4 | Falha ao gravar consumo não impede nenhum envio; `record_enabled=false` desliga tudo | testes da Task 5 |
| B5 | Status de mensagem não regride (comportamento atual preservado) | testes existentes de status passam **inalterados** |
| B6 | Cada transição de status gera exatamente 1 evento, na mesma transação | testes da Task 4 |
| B7 | Job: nenhuma mensagem da janela fica sem registro após uma passada; segunda passada é no-op | testes da Task 8 |
| B8 | `unlinked` só é destacado após `unlinked_attention_hours`; `unconfirmed` só após `undelivered_after_hours`, e reliquida se a entrega chegar | testes das Tasks 3, 8 e 10 |
| C1 | `estimated_cost` nunca é alterado por `meta_cost`; moedas nunca somadas | testes das Tasks 3 e 10 |
| C2 | `whatsapp_usage:read|write` existe; catálogo = 119; manager só lê; backfill idempotente | testes da Task 9 |
| C3 | Isolamento total entre organizações em todas as rotas novas | teste da Task 10 |
| C4 | Nenhuma tabela ou resposta da API contém conteúdo de mensagem ou telefone completo | testes das Tasks 2 e 10 |
| C5 | Tela acessível por Configurações › Canais, filtros e totais por moeda funcionam; E2E verde | Tasks 11 e 12 |
| Z1 | Suítes Go (`-p 1`), Vitest, build do frontend e E2E: **nenhuma falha nova** em relação à linha de base | execução final da Task 12, relatório com a comparação |
| F1 | **Fail-open:** falha em `usage.Record`, `RecordStatusEvent`, `Settle`, gravação de pricing ou de `contact_status_events` (erro injetado) **nunca** impede envio, recebimento, processamento do webhook (status da mensagem continua atualizado, resposta HTTP igual) nem a transição de status do contato; só gera log e contador | testes com falha injetada nas Tasks 4, 5, 6 e 7 |
| F2 | Com `usage.record_enabled=false`, nenhum caminho grava `message_usage`, `message_pricing_events` ou `contact_status_events`, e o comportamento funcional do atendimento é idêntico ao anterior (testes existentes passam sem alteração) | testes das Tasks 4, 5, 6, 7 e 8 |
| F3 | B só vai para produção com autorização explícita do dono | combinado de processo |
| Z2 | Sem push, PR, merge ou deploy sem ordem explícita do dono | combinado de processo |

---

## Riscos específicos da execução

| Risco | Mitigação no plano |
|---|---|
| Mudança nas assinaturas de `transitionContactStatus*` quebra chamadores/testes | Task 4 isolada, com regressão dedicada |
| Caminho quente do webhook | Task 7 só **acrescenta** chamadas antes/depois; `updateMessageStatus` e `statusPriority` ficam intactos |
| 29 chamadas `sendAndSave*` (mudança grande e repetitiva) | Task 6 própria, mecânica, com teste por ponto e fallback `unclassified`; o job cobre o que escapar |
| Falha de `go test ./internal/handlers` por tempo (dívida conhecida do CI) | Rodar por `-run` direcionado a cada task; suíte completa só na Task 12, comparada com a linha de base |
| Campanha cria a `Message` depois do envio | Tratado na Task 6 (`AttachWamid` com wamid conhecido) e coberto pelo job |

## Decisões da revisão (aprovadas)

1. Motivo `agent_reply` incluído; `auto_resolve` reservado, sem uso; nenhum outro motivo por antecipação.
2. Divisão A/B/C aprovada; coleta começa em B; B não vai para produção sem autorização explícita; `record_enabled=false` desliga sem impacto funcional.
3. Lote de 500 é limite técnico interno, não configurável; job paginado, idempotente e seguro para concorrência.
4. Instrumentação fail-open é critério de aceite (F1, F2), em especial em `SendOutgoingMessage`, webhook da Meta e `transitionContactStatusDB`.
