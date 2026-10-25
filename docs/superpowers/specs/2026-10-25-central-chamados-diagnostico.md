# Central de Chamados — diagnóstico (Fase 0) e plano

Base auditada: `origin/development` @ 8ea43948 + commit dos perfis (`feature/team-role-profiles`, worktree `whatc-profiles`).
A worktree principal (`whatc`, branch `feature/otp-security`) tem trabalho de OTP/segurança não commitado e **não foi tocada**.

## 1. O que já existe e será reutilizado

| Necessidade | Já existe | Onde |
|---|---|---|
| Chamado | **Occurrence** (Protocolo): `protocol_number` único e nunca reaproveitado, contato, etapa, prioridade, equipe, responsável, unidade, departamento, categoria, processo/motivo, SLA, origem (`source_transfer_id`), linha do tempo, documentos | `models/occurrences.go`, `handlers/occurrences*.go` |
| Kanban de chamados | `OccurrenceBoard.vue` / `OccurrenceCard.vue`, etapas configuráveis | `frontend/src/components/crm` |
| Ciclos independentes | Fechar atendimento não fecha chamado (já documentado no modelo) | `Occurrence.SourceTransferID` |
| Unidade/Equipe | `Team.UnitID/DepartmentID`; roteamento `contact_placement` | `models/models.go`, `flow_transfer_routing.go` |
| Contas WhatsApp | `WhatsAppAccount` (nome único, `DefaultTeamID`, default in/out) | `models/models.go` |
| Chat interno | `team_chat` (canais, membros, leitura, anexos, WS, auditoria) | `models/team_chat.go`, `handlers/team_chat*.go` |
| Tempo real | WebSocket hub, `TypeOccurrenceChanged/EventCreated` | `internal/websocket` |
| Templates | `SendTemplateMessage`, `templates`, uso/custo | `handlers/messages.go` |
| SLA | `SLATracking` + processador | `handlers/sla_processor.go` |
| Perfis | SAC, Vendas, Logística, Supervisor de Loja, Supervisor de SAC | `models/team_profiles.go` |

Conclusão: **não criar um modelo "Ticket" novo.** Evoluir `Occurrence`.

## 2. Lacunas e riscos encontrados

1. **Estados gerais incompletos.** `ContactStatus` tem 3 valores (`new`, `in_progress`, `resolved`); não existe "Aguardando cliente". As etapas (`OccurrenceStage`) são da organização, só com `is_initial`/`is_closing`, sem estado geral nem dono (equipe).
2. **Visibilidade do chamado deriva da conversa** (`visibleOccurrences` → `scopeVisibleConversations`). Um chamado de Logística de uma loja só aparece para quem enxerga a conversa SAC do contato. O isolamento por unidade/equipe do chamado não existe por si.
3. **Sem concorrência segura.** `UpdateOccurrence` e `ChangeOccurrenceStage` usam `Updates` sem condição: dois agentes "assumindo" ao mesmo tempo, o último vence sem erro. Não existe ação "assumir".
4. **Transição de etapa livre.** `ChangeOccurrenceStage` aceita qualquer etapa da organização; não há regra por equipe.
5. **Contato único por telefone com uma `WhatsAppAccount`.** Mensagens já guardam a conta; a conversa por conta (9600 vs 9602) precisa de decisão (ver §4).
6. **Chat interno sem vínculo com chamado.** Falta `occurrence_id` no canal e a garantia de que mensagem interna nunca sai ao WhatsApp.
7. **Template automático sem idempotência/fila própria** para o evento "chamado criado".
8. **Dashboard** existente é de conversas/agentes; faltam métricas de chamados (definição separada).
9. Riscos de regressão: `occurrences.stage_id` é lido pelo quadro, SLA, exportação e widgets. Qualquer mudança deve ser **aditiva** (colunas novas anuláveis, backfill).
10. Trabalho de segurança em branches paralelas (`fix/security-*`, `feature/otp-security`): mexe em `campaigns`, `worker`, auth. Evitar tocar esses arquivos.

## 3. Modelo de dados proposto (aditivo)

- `occurrence_stages`: `+team_id` (nulo = padrão da organização), `+general_state` (`new|in_progress|waiting_customer|done`), `+allowed_from` (JSONB de transições, nulo = livre). Backfill: `is_initial → new`, `is_closing → done`, demais `in_progress`.
- `occurrences`: `+general_state` (denormalizado da etapa, indexado), `+version int` (concorrência otimista), `+origin_account` e `+origin_conversation_id`, `+order_ref` (pedido), `+parent_occurrence_id`/`reopened_count`.
- `occurrence_conversations(occurrence_id, contact_id, whatsapp_account, role)` — vínculo N:N entre chamado e conversas (SAC, Logística).
- `occurrence_participants(occurrence_id, user_id|team_id, access)` — leitura/colaboração explícita.
- `occurrence_notifications` / `outbound_template_jobs(idempotency_key unique, occurrence_id, template, status, attempts, provider_message_id, error)` — envio assíncrono, idempotente por `occurrence_id + evento`.
- `team_chat_channels`: `+occurrence_id` (nulo-único), canal interno criado sob demanda.
- `ContactStatus`: adicionar `waiting_customer` (aditivo; consumidores com `switch` precisam do caso novo).

## 4. Decisões

- **Conversas por conta:** manter 1 contato por telefone, mas o chamado referencia a conta de origem e as conversas vinculadas; a Logística responde pela conta configurada em `WhatsAppAccount` (marcada por configuração, não por constante). Sem "transferir thread entre números".
- **Autorização do chamado** passa a ser: `unidade` ∈ escopo do usuário **ou** `equipe` ∈ equipes do usuário **ou** responsável **ou** participante; supervisores por `view_team`/`view_all`, nunca por papel.
- **Mensagem interna ≠ externa:** tabela/canal distintos; o endpoint de envio WhatsApp não aceita `channel_id`; teste de contrato garante isso.

## 5. Plano por etapas (cada uma = commits pequenos, testes, sem deploy)

1. **Concorrência e "assumir"**: `POST /occurrences/{id}/claim` com UPDATE condicional + `version`; mudança de etapa/atribuição com checagem de versão (409). *(primeira entrega, ver §7)*
2. Estados gerais + etapas por equipe + transições validadas no backend; `waiting_customer`.
3. Escopo de visibilidade próprio do chamado (unidade/equipe/participante) + testes de isolamento.
4. Criar chamado a partir da conversa SAC → Logística da unidade (roteamento por `Team.UnitID`), vínculo conversa↔chamado.
5. Template automático (fila, idempotência, tentativas, auditoria) — com mock de provedor.
6. Chat interno por chamado.
7. Kanban (por estado operacional, mobile com alternativa ao arrastar) e Lista com contexto.
8. Dashboard de chamados (definições documentadas).
9. Testes no navegador dos 15 cenários e documentação operacional.

## 6. Matriz de permissões (alvo)

| Ação | SAC | Vendas | Logística | Sup. Loja | Sup. SAC |
|---|---|---|---|---|---|
| Criar chamado | ✔ (qualquer equipe-destino autorizada) | ✔ (da loja) | ✔ | ✔ | ✔ |
| Ver chamado | criados por ele / participante | da loja | equipe+unidade | unidade | todas as lojas |
| Assumir | — | — | equipe da unidade | equipe da unidade | ✔ |
| Mudar estado | — | — | próprio | unidade | ✔ |
| Redistribuir | — | — | — | unidade | ✔ |
| Dashboard | — | — | próprio | unidade | consolidado |
| Chat interno do chamado | ✔ participante | ✔ | ✔ | ✔ | ✔ |

## 7. Estratégia de testes

Go (Postgres/Redis de teste): concorrência com goroutines (apenas um `claim` vence), isolamento por unidade/equipe, idempotência de criação e de template, contrato "interno nunca vai ao WhatsApp". Frontend: vitest dos cards/colunas. Navegador: os 15 cenários, com provedor WhatsApp mockado, registrando evidências.
