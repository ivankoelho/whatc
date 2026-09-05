# Help Desk nativo — Unidade, Departamento, Categoria e SLA (Fase 3 de Ocorrências)

- **Data:** 2026-09-04
- **Status:** Aprovada — pronta para plano de implementação
- **Autor/revisão:** Ivan Coelho (product) · design colaborativo
- **Escopo:** Fase 3 do roadmap de Ocorrências (Fase 1: [protocolo e ticket](2026-08-15-crm-ocorrencias-protocolo-design.md); Fase 2, Kanban, ainda não especificada).

## 1. Contexto

O produto pediu originalmente "um sistema de chamados tipo osTicket". A investigação mostrou que isso já existe em construção: o módulo **Ocorrências** (Fase 1, implementada) já cobre ticket numerado (protocolo), etapas configuráveis, timeline de eventos, prioridade e responsável — ver `internal/models/occurrences.go`. O que faltava era o que o roadmap original já previa como Fase 3 (SLA) mais três conceitos que a Fase 1 não cobria: Unidade, Departamento e Categoria.

**Não é "construir um osTicket dentro do Whatomate".** É evoluir Ocorrências até virar o Help Desk nativo do Whatomate — usando osTicket só como referência funcional, não como arquitetura a replicar. A vantagem sobre um osTicket externo integrado por API é estrutural: ticket e conversa compartilham `organization_id`/`contact_id` no mesmo Postgres, sem sincronização, sem duas fontes de verdade.

### O achado que redesenhou o escopo original

O pedido inicial trazia "Unidade" e "Departamento" como conceito novo. A investigação da estrutura real de equipes revelou que **já existe uma solução informal pra isso, e ela não escala**: hoje uma organização (ex: rede com 23 lojas, um único número de WhatsApp corporativo) usa `Team` para representar a combinação loja+setor via convenção de nome — `"Loja Alagoinhas: Equipe Logística"`, `"Loja Alagoinhas: Equipe ADM"`, uma por combinação. Isso tende a uma lista de dezenas de Equipes só de nome.

A decisão tomada: separar as duas dimensões que `Team` hoje mistura. `Unit` (a loja) e `Department` (o setor) passam a existir uma vez cada, e `Team` ganha duas colunas que **taggeiam** qual combinação ela representa — sem mudar nada do roteamento de chat existente.

## 2. Objetivos e não-objetivos

**Objetivos da Fase 3**

- `Unit` e `Department` como entidades de primeira classe, independentes.
- `Team` (chat) tagueada com `unit_id`/`department_id`, de forma aditiva.
- `Occurrence` ganha `unit_id`, `department_id`, `category_id`, `source`, herdados da Team de origem quando aberta a partir de uma conversa.
- Categoria/Subcategoria configuráveis por organização (um nível de aninhamento).
- SLA por prioridade, com o schema já preparado (mas não implementado) para SLA por departamento/unidade/categoria no futuro.
- Distinção explícita entre nota interna e resposta pública para fins de SLA de primeira resposta.
- "Fila" como conceito de API/UI (filtro), sem tabela nova.

**Não-objetivos (explicitamente fora desta fase)**

- Tabela `queues` dedicada.
- SLA variando por departamento, unidade ou categoria (schema pronto, lógica de match não).
- Escalonamento automático e notificações de violação de SLA (só marca `breached`).
- Migrar as Equipes de chat existentes para a estrutura nova — `Team` continua funcionando exatamente como hoje; `unit_id`/`department_id` são tags opcionais.
- Motor de regras de automação (unidade+departamento+categoria → equipe/prioridade/SLA) — as colunas que ele vai precisar já existem depois desta fase, mas o motor em si é projeto futuro.
- Portal do solicitante, e-mail → chamado, integrações externas (GLPI, Ansible), IA de classificação.

## 3. Decisões

| Questão | Decisão | Motivo |
|---|---|---|
| Unidade × Departamento × Equipe | Duas entidades novas + tags em `Team` | `Team` hoje mistura loja+setor por convenção de nome; separar permite configurar SLA/permissão por Departamento uma vez, não por combinação |
| Unidade rica desde já | Schema largo (`code`, `type`, `address`, `phone`, `business_hours_id`, `metadata`), UI estreita (só `name`/`code`/`type`/`active`) | Evita uma segunda migração quando o produto pedir endereço/horário de funcionamento; custo de coluna nula é zero |
| SLA: granularidade | Por prioridade nesta fase; schema com `department_id`/`unit_id`/`category_id` nulos prontos para regras mais específicas depois | Prioridade é o mínimo pra SLA fazer sentido; departamento/unidade ficam de fora até haver demanda real, sem pagar o custo de regras de match agora |
| Primeira resposta | Ação explícita "Responder" na tela do chamado, distinta de nota interna | Nota interna não deve nunca parar o relógio de SLA; ação explícita evita ambiguidade quando há mais de um chamado aberto pro mesmo contato, e reaproveita o padrão já estabelecido do `/send-protocol` (janela de 24h, `SendOutgoingMessage`) em vez de instrumentar o fluxo genérico de chat |
| Fila | Conceito de API/UI, não tabela | `department_id = X AND assigned_user_id IS NULL` sobre a listagem existente; tabela `Queue` só quando existir regra de roteamento pra guardar |
| Categoria/Subcategoria | Uma tabela self-referenciada (`parent_id` nulo = categoria, preenchido = subcategoria), um nível só | Mesmo padrão já usado em `occurrence_stages`; é o campo que habilita automação futura (unidade+departamento+categoria → equipe/prioridade/SLA), por isso entra já e não é adiado |
| Origem da ocorrência | Campo `source` (string, hoje sempre `"whatsapp"`) | Documenta intenção multi-canal sem construir lógica nova; Whatomate só tem um canal hoje |

## 4. Modelo de dados

Cinco tabelas novas, quatro colunas novas em tabelas existentes. **Nenhuma coluna existente removida ou alterada em semântica.**

### `units`

| Campo | Tipo | Observação |
|---|---|---|
| `organization_id` | uuid | |
| `name` | string(255) | |
| `code` | string(50), nulo | |
| `type` | string(50), nulo | livre — "matriz", "filial", "loja"; sem enum fechado nesta fase |
| `active` | bool | default true |
| `address` | string(500), nulo | sem tela na Fase 3 |
| `phone` | string(50), nulo | sem tela na Fase 3 |
| `business_hours_id` | uuid, nulo | FK solta — não existe tabela `business_hours` ainda |
| `metadata` | jsonb | default `'{}'`, extensibilidade sem migração |

Índice único parcial `(organization_id, name) WHERE deleted_at IS NULL` (mesmo padrão de `occurrence_stages`).

### `departments`

`organization_id`, `name`, `active` (default true). Índice único parcial igual ao de `units`.

### `occurrence_categories`

| Campo | Tipo | Observação |
|---|---|---|
| `organization_id` | uuid | |
| `name` | string(100) | |
| `parent_id` | uuid, nulo, FK para `occurrence_categories` | nulo = categoria; preenchido = subcategoria |
| `position` | int | ordenação, default 0 |
| `is_active` | bool | default true |

Um nível só de aninhamento nesta fase (subcategoria não tem filhos) — validado na criação, não a nível de banco.

### `occurrence_sla_policies`

| Campo | Tipo | Observação |
|---|---|---|
| `organization_id` | uuid | |
| `department_id` | uuid, nulo | não populado nesta fase |
| `unit_id` | uuid, nulo | não populado nesta fase |
| `category_id` | uuid, nulo | não populado nesta fase |
| `priority` | string(20) | `low`/`normal`/`high`/`urgent` |
| `response_minutes` | int | |
| `resolution_minutes` | int | |

Índice único parcial `(organization_id, priority) WHERE department_id IS NULL AND unit_id IS NULL AND category_id IS NULL AND deleted_at IS NULL` — garante uma política por prioridade por organização nesta fase, sem impedir linhas mais específicas no futuro (a extensão troca só a query de match, não o schema).

Seed automático na primeira leitura sem política configurada, valores de exemplo do próprio pedido do produto: baixa 8h/24h, normal 4h/12h, alta 2h/8h, urgente 30min/4h — mesmo padrão de `ensureDefaultStages`.

### Alterações em `teams` (aditivas)

`unit_id` (uuid, nulo), `department_id` (uuid, nulo). Não usadas por nenhuma lógica de roteamento/round-robin existente — só metadado consultado pela criação de Ocorrência.

### Alterações em `occurrences` (aditivas)

| Campo | Tipo | Observação |
|---|---|---|
| `unit_id` | uuid, nulo | herdado de `Team.unit_id` da equipe do atendimento de origem; editável |
| `department_id` | uuid, nulo | idem, `Team.department_id` |
| `category_id` | uuid, nulo | selecionado manualmente, FK para `occurrence_categories` |
| `source` | string(20) | default `"whatsapp"`; `"manual"` quando sem `source_transfer_id` |
| `response_deadline`, `resolution_deadline`, `breached`, `breached_at` | — | reaproveita a struct `SLATracking` já existente (`internal/models/chatbot.go:294`), embutida |
| `first_response_at` | timestamp, nulo | preenchido pela ação "Responder" |
| `first_response_by_id` | uuid, nulo | idem |

`resolution_deadline` é comparado contra `ClosedAt`, que já existe e já é preenchido ao entrar em etapa `is_closing` — nenhuma mudança nesse campo.

### `occurrence_events` — novo tipo

`OccurrenceEventReply = "reply"`, ao lado dos já existentes (`opened`, `note`, `stage_change`, `assignment`, `protocol_sent`, `closed`). Distinção que importa para SLA: `note` é interna e nunca conta como resposta; `reply` é a mensagem que de fato saiu pro cliente via WhatsApp.

### Organização — um campo novo

`OccurrenceSLAEnabled` (bool, default false), no mesmo struct `ChatbotSettings` onde `SLA.Enabled` já vive — reaproveita a leitura já cacheada (`getSLAEnabledSettingsCached`) que o `sla_processor.go` já faz por organização, em vez de criar uma segunda fonte de configuração. Gate próprio, no padrão de `ClientInactivity.CloseInactiveAttendances`: liga o processamento de SLA de Ocorrências sem afetar o SLA de chat existente.

## 5. Resposta pública vs. nota interna

Ação nova na tela do chamado: **Responder**. Reaproveita exatamente o padrão de `/send-protocol` (`internal/handlers` — mesmo grupo de handlers de Ocorrências):

- Reusa `SendOutgoingMessage` (`internal/handlers/messages.go:148`) sem modificá-lo.
- Recusa fora da janela de 24h (`last_inbound_at`), mesma regra do envio de protocolo.
- Grava `OccurrenceEvent` tipo `reply` na timeline.
- Na primeira vez (por ocorrência), preenche `first_response_at`/`first_response_by_id`.

Nota interna (`POST /events` tipo `note`) continua existindo exatamente como na Fase 1, sem nenhum efeito sobre SLA.

**Não fazer:** detectar resposta automaticamente a partir do envio de mensagem no chat comum. Ficaria ambíguo quando o contato tem mais de um chamado aberto, e exigiria instrumentar `SendOutgoingMessage` (usado por todo o chat, não só por Ocorrências) para saber sobre um conceito que ele hoje ignora.

## 6. SLA — cálculo e processamento

- Na criação da Ocorrência (ou quando a prioridade muda), busca a política `occurrence_sla_policies` para `(organization_id, priority)` com `department_id`/`unit_id`/`category_id` nulos — a única forma de política que existe nesta fase — e calcula `response_deadline`/`resolution_deadline` a partir de `opened_at`.
- Reaproveita o processo já existente em `internal/handlers/sla_processor.go`: novo passo `processOccurrenceSLA`, ao lado de `processOrganizationSLA`, gated por `occurrence_sla_enabled`. Nesta fase, o passo só **marca** `breached`/`breached_at` quando os prazos estouram — sem auto-fechar, sem escalonamento, sem notificação (viram insumo de relatório numa fase própria).
- Mudar a prioridade depois de aberto recalcula os prazos a partir do momento da mudança (não retroage `opened_at`).
- `first_response_at` só é preenchido pelo evento `reply` (§5) — uma resposta pública efetivamente enviada ao contato via WhatsApp. `note` nunca o preenche, nunca o encerra e nunca o adia; as duas semânticas não se confundem em nenhum ponto do cálculo de SLA.

## 7. Fila — conceito de API/UI

Sem tabela nova. "Fila de Logística" é `GET /api/occurrences?department_id=<id>&assigned_user_id=null`, sobre a listagem e a visibilidade que já existem (mesmo padrão de subconsulta da Fase 1). O rótulo "Fila" aparece só na interface — o backend não sabe o que é uma fila, só filtra.

## 8. API

Novos endpoints, todos sob o mesmo gate de permissão que Ocorrências já usa (`chat:read`/`chat:write`; CRUD de configuração sob `settings.general:write`, mesmo padrão de `occurrence-stages`):

```
GET    /api/units
POST   /api/units
PUT    /api/units/{id}
DELETE /api/units/{id}

GET    /api/departments
POST   /api/departments
PUT    /api/departments/{id}
DELETE /api/departments/{id}

PUT    /api/teams/{id}                        -- aceita unit_id/department_id (endpoint existente, campos novos)

GET    /api/occurrence-categories
POST   /api/occurrence-categories
PUT    /api/occurrence-categories/{id}
DELETE /api/occurrence-categories/{id}
PUT    /api/occurrence-categories/reorder

GET    /api/occurrence-sla-policies
PUT    /api/occurrence-sla-policies/{priority}

POST   /api/occurrences/{id}/reply             -- envia mensagem, grava evento reply, seta first_response
```

`GET /api/occurrences` ganha filtros `unit_id`, `department_id`, `category_id` (a "fila" é só uma chamada com esses filtros).

## 9. Frontend

| Arquivo | Responsabilidade |
|---|---|
| `views/settings/UnitsView.vue` | CRUD de Unidade — só `name`/`code`/`type`/`active` |
| `views/settings/DepartmentsView.vue` | CRUD de Departamento |
| `views/settings/OccurrenceCategoriesView.vue` | CRUD de Categoria/Subcategoria, com reorder |
| `views/settings/OccurrenceSLAPoliciesView.vue` | Uma linha por prioridade, response/resolution em minutos |
| `views/settings/TeamsView.vue` | Ganha seletor de Unidade/Departamento (arquivo existente, campo novo) |
| `views/crm/OccurrencesView.vue` | Ganha filtros de Unidade/Departamento/Categoria; atalhos "Fila de {Departamento}" |
| `views/crm/OccurrenceDetailView.vue` | Campos de Unidade/Departamento/Categoria; botão "Responder" ao lado do de nota interna; badge de prazo de SLA (no prazo/em risco/estourado) |
| `components/chat/ContactOccurrencesPanel.vue` | Continua igual; herda unit/department da Team ativa ao criar |
| `stores/occurrences.ts`, `stores/units.ts`, `stores/departments.ts` | Estado |

Chaves de i18n em `pt-BR.json` e `en.json`, paralelas linha a linha (convenção existente).

## 10. Por que nada em produção muda

| Superfície | Garantia |
|---|---|
| Roteamento de chat / `Team` | Não tocado — `unit_id`/`department_id` são colunas novas, nulas por padrão, lidas só na criação de Ocorrência |
| SLA de chat (`ChatbotSettings.SLA`) | Não tocado — SLA de Ocorrência tem gate (`occurrence_sla_enabled`) e struct própria |
| `SendOutgoingMessage` | Não modificado — `/reply` o chama, não o altera |
| Ocorrências Fase 1 (notas, etapas, protocolo) | Comportamento idêntico; campos novos são nulos até preenchidos |
| Visibilidade | Nenhuma regra nova; `/reply` e as configurações novas passam pelos mesmos gates de `chat:read`/`chat:write`/`settings.general:write` já existentes |

Tudo aditivo: tabelas novas, colunas nulas, endpoints novos, telas novas.

## 11. Compatibilidade com fases seguintes

- **SLA por departamento/unidade/categoria:** popular linhas mais específicas em `occurrence_sla_policies` e mudar a query de match para "mais específico vence" — nenhuma migração de schema.
- **Motor de automação** (unidade+departamento+categoria → equipe/prioridade/SLA): as colunas que ele precisa ler (`unit_id`, `department_id`, `category_id` em `Occurrence` e `Team`) já existem depois desta fase.
- **`Queue` como entidade real** (agentes, roteamento): quando existir regra de roteamento pra guardar, a tabela nasce; até lá o filtro basta.
- **Business hours:** `units.business_hours_id` já reserva o lugar.
- **Fase 2 (Kanban)** de Ocorrências: independente desta fase, sem conflito de schema.

## 12. Verificação

**Go**

- Herança de `unit_id`/`department_id` da `Team` do atendimento de origem ao criar Ocorrência — com e sem Team tagueada (caso comum hoje: Team sem tag ainda).
- Cálculo de `response_deadline`/`resolution_deadline` a partir da política de prioridade — inclui fallback quando a organização não configurou política (seed automático).
- Recalcular prazos ao mudar prioridade depois de aberto.
- `/reply`: recusa fora da janela de 24h (mesmo teste do `/send-protocol`); grava evento `reply`; seta `first_response_at`/`first_response_by_id` só na primeira vez; segunda chamada não sobrescreve.
- Nota interna (`POST /events` tipo `note`) nunca seta `first_response_at` — teste negativo explícito, é o ponto que motivou a decisão.
- `processOccurrenceSLA`: marca `breached`/`breached_at` passado o prazo; não roda quando `occurrence_sla_enabled=false`; não interfere no processamento de SLA de chat existente.
- Índice único parcial de `occurrence_sla_policies`: recusa segunda política org-wide pra mesma prioridade; aceita linha mais específica (department_id preenchido) sem conflitar — teste que já antecipa a fase seguinte.
- Categoria: recusar subcategoria de subcategoria (mais de um nível); recusar excluir categoria em uso.

**Playwright**

- Tagueiar uma Team existente com Unidade/Departamento; abrir Ocorrência pela conversa dessa Team; conferir herança.
- Botão "Responder" vs. nota interna na tela do chamado — conferir que só o primeiro afeta o badge de SLA.
- Filtro de "Fila de {Departamento}" na listagem.

**Ambiente:** igual à Fase 1 — Postgres e Redis em Docker, backend em `:8080`, Vite em `:3000`.

## 13. Riscos conhecidos

| Risco | Mitigação |
|---|---|
| Confundir "responder" com "nota interna" e vazar SLA incorreto | Botões visualmente distintos; teste negativo explícito na suíte |
| Organização nunca configura política de SLA e Ocorrência nasce sem prazo | Seed automático de política padrão na primeira leitura, mesmo padrão de `ensureDefaultStages` |
| Índice parcial de `occurrence_sla_policies` mal desenhado bloquear a fase seguinte (SLA por departamento) | Testado explicitamente: inserir linha mais específica junto com a org-wide sem conflito |
| Tag de Unidade/Departamento na Team ficar incompleta (23 lojas × N setores pra marcar manualmente) | Fora do código: é trabalho de configuração único, não bloqueia a feature — Ocorrência sem Team tagueada simplesmente nasce sem unit/department, editável à mão |

## 14. Roadmap futuro (fora desta fase)

Objetivo de produto declarado: substituir o recurso de chamados do SULTS pelo Whatomate. A Fase 3 é o núcleo correto pra isso, mas não é paridade — falta o que vira **Fase 4 — Portal, Colaboração e Gestão de Chamados**, deliberadamente numa fase própria em vez de entrar no mesmo sprint:

- Portal web do solicitante (fora do WhatsApp).
- Participantes/colaboradores num chamado além do responsável único.
- Etiquetas (tags livres, complementares à Categoria/Subcategoria estruturada).
- Relatórios e dashboard (volume, SLA cumprido/violado, por unidade/departamento/agente).
- Satisfação e reabertura de chamado encerrado.

Nenhum item desta lista muda o schema decidido nas seções 3–4: `unit_id`/`department_id`/`category_id` em `Occurrence`, a tabela `occurrence_sla_policies` com colunas de escopo já prontas, e `occurrence_events` como timeline única são a base sobre a qual a Fase 4 se apoia — é por isso que a Fase 3 é implementada como está, sem adiantar nada desta lista.
