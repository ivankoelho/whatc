# Consumo do WhatsApp: base de medição por mensagem

Nota de design. Estado: **aprovada**; nada implementado (sem plano, código, migration, teste, push ou PR). Base: `development` em `0e86c01`.

## 1. Princípio e objetivo

O Whatc é desenvolvido com foco em **resolução**, não em conversas longas. Com a cobrança por mensagem da Meta, cada mensagem tem custo e precisa ser atribuível: a quem, a qual unidade, a qual atendimento, protocolo ou oportunidade.

Esta fase entrega **só a base de medição**: um registro de consumo por mensagem, o histórico de mudanças de status do contato e um custo **estimado** por tabela de preços. Os indicadores de eficiência (mensagens até resolver, resolvido pela IA × humano, custo por atendimento, por protocolo, por venda) vêm depois, sobre dados confiáveis. **O histórico anterior não pode ser reconstruído**: o `pricing` da Meta nunca foi guardado. Por isso a medição começa já, antes do VoIP.

### Fora desta fase
Dashboard de eficiência; conciliação com o `pricing_analytics` da Meta; preenchimento retroativo; custo de ligação de voz (WhatsApp Calling e o futuro cliente VoIP); tela do histórico de status.

## 2. Estado atual (verificado no código)

- O parser do webhook já declara `status.pricing` (`billable`, `pricing_model`, `category`) e `status.conversation.id`, mas **nada os usa**. `Message.ConversationID` existe e nunca é gravado.
- `processStatusUpdate` → `updateMessageStatus` busca a mensagem pelo `whats_app_message_id` e **descarta** qualquer status que não seja progressão (um `sent` tardio depois de `delivered`); se a mensagem não é encontrada, o evento se perde.
- Quase todo envio passa por `SendOutgoingMessage` (`messages.go`), que cria a `Message` como `pending` (`createOutgoingMessage`) e a atualiza depois (`finalizeMessageSend`). Exceções: a campanha (`worker/worker.go` envia e **só depois** cria a `Message`) e o eco do app do WhatsApp Business (`processMessageEcho`).
- Os bots enviam por 4 helpers `sendAndSave*` (29 chamadas: `chatbot_graph_runner` 9, `chatbot_processor` 15, `agent_transfers` 3, `ai_tool_confirmation` 2), que usam `context.Background()` e não recebem quem os originou.
- O contato só tem o **status atual** (`new`, `in_progress`, `resolved`), sem histórico de quando e por quem mudou. A mudança passa por `transitionContactStatusDB` (`contact_status.go`).
- Já existe a visão **agregada** da Meta (aba "Insights da Meta", `pricing_analytics`, moeda da WABA). Ela não atribui custo a unidade, agente ou origem; este registro, sim.
- Unidade só existe para contato colaborador. `User` e `Team` têm `unit_id` opcional.

## 3. Decisões fechadas

| # | Decisão |
|---|---|
| 1 | **Unidade** do custo: a do agente que enviou; senão a da equipe da conversa; senão vazia. Guarda de onde veio (`unit_source`). |
| 2 | **Preço**: tabela interna versionada por vigência, agora; **conciliação** com o `pricing_analytics` da Meta numa fase seguinte. |
| 3 | **Histórico de status** do contato registrado nesta fase, na mesma transação da mudança. |
| 4 | Abordagem **A**: tabela própria `message_usage`, uma linha por mensagem, ligada a ela e atualizada **só pela liquidação** (nunca editada à mão); a tabela `messages` não é alterada. |
| 5 | Nomes separam **o que a Meta informou** do **que o Whatc calculou**. `estimated_cost` nunca é sobrescrito por valor da Meta. |
| 6 | O registro de consumo **não guarda conteúdo** de mensagem. |

## 4. Modelo de dados

### 4.1 `message_usage` (uma linha por mensagem, entrada e saída)

| Grupo | Colunas |
|---|---|
| Identidade | `id`; `organization_id`; `whatsapp_account` (nome, como em `messages`); `message_id` (uuid, **nulo** até a mensagem ser localizada); `wamid` (nulo até o envio ser aceito); `direction` |
| Contexto | `contact_id` (nulo quando sem vínculo); `conversation_id` (da Meta, quando informado); `message_type`; `recipient_country` (código, ex. `55`; **não** guarda o telefone) |
| Template | `template_name`; `template_id`; `declared_category` (`MARKETING`, `UTILITY`, `AUTHENTICATION`; vazio para texto livre) |
| **Origem** | `actor_type` (`agent`, `ai`, `flow`, `campaign`, `system`, `external_app`, `contact`); `actor_user_id`; `team_id`; `unit_id`; `unit_source` (`agent`, `team`, `none`); `flow_id`; `flow_node_id`; `campaign_id`; `occurrence_id` (quando conhecido); `ai_usage_log_id`; `origin_detail` (ex. `sla`, `keyword`, `chatbot_reply`); `origin_inferred` (bool) |
| **O que a Meta informou** | `billing_category` (marketing, utility, authentication, authentication_international, service…); `billable` (bool, **nulo = desconhecido**); `pricing_model`; `meta_pricing` (jsonb: o snapshot que originou o valor atual); `meta_pricing_history` (jsonb, até 5 snapshots anteriores); `category_diverged` (bool); `meta_cost` e `meta_cost_currency` (reservados à conciliação; ficam nulos nesta fase) |
| **O que o Whatc calculou** | `quantity` (1); `estimated_cost` (`numeric(18,6)`, nulo se não calculado); `estimated_currency`; `rate_id`; `repriced_at` |
| Estado | `billing_state` (ver 4.4); `link_state` (`linked` ou `unlinked`, ver 4.4); `sent_at` (referência da vigência do preço); `settled_at` |

**Unicidade (idempotência):** `UNIQUE (message_id)` onde não nulo e `UNIQUE (organization_id, whatsapp_account, wamid)` onde `wamid` não vazio. Uma mensagem = no máximo um registro, no banco, não só no código.

### 4.2 `message_pricing_events` (só inserção)

Cada linha é **um evento de status** recebido da Meta. O evento de status e o snapshot de pricing são coisas distintas: **um evento nem sempre traz pricing**, e o design não pressupõe que traga.

```
message_pricing_events
    ├── evento de status: status (sent|delivered|read|failed), event_at, received_at, erro resumido (se failed)
    └── snapshot de pricing DAQUELE evento (opcional): has_pricing, pricing{billable, pricing_model, category}, conversation{id, origin}
```

Colunas: `id`; `organization_id`; `whatsapp_account`; `wamid`; `status`; `event_at` (timestamp do payload); `received_at`; `has_pricing`; `pricing` (jsonb, nulo se o evento não trouxe); `conversation` (jsonb); `recipient_country`; `error_code`/`error_title` (só em `failed`). **Único** `(organization_id, whatsapp_account, wamid, status, event_at)`. Serve de auditoria e de insumo de reprocessamento: se a regra da Meta mudar, sabe-se o que ela informou naquele momento. Guarda os campos relevantes, não o webhook inteiro, nem conteúdo de mensagem.

### 4.3 `whatsapp_rates` (tabela de preços versionada)

`id`; `organization_id`; `country` (código do país ou `*` para qualquer); `category`; `price` (`numeric(18,6)`); `currency` (ISO 4217); `valid_from` (data); `valid_to` (data, nulo = em aberto); `notes`; `created_by`; `created_at`. Único `(organization_id, country, category, valid_from)`. A API recusa vigências sobrepostas para o mesmo país e categoria. **Nenhum preço fica no código.** Uma linha já usada por algum `message_usage` é imutável: mudar preço é criar nova vigência.

### 4.4 Estados

`billing_state` (o que se sabe sobre a cobrança):

| Estado | Significado |
|---|---|
| `pending` | Enviada ou em envio; a Meta ainda não informou nada conclusivo |
| `awaiting_pricing` | Houve entrega, mas nenhum evento trouxe pricing. `billable` e `estimated_cost` nulos |
| `priced` | Cobrável, com preço achado: `estimated_cost` calculado |
| `not_billable` | A Meta informou `billable=false` (inclui toda entrada) |
| `no_rate` | Cobrável, mas sem preço cadastrado para país, categoria e data |
| `unconfirmed` | Passou o prazo sem entrega (ver 6.2). Custo estimado 0; reliquida se a entrega chegar |
| `send_failed` | A API recusou antes de aceitar (sem wamid). Não cobrável |
| `failed` | Falhou depois de aceita (webhook `failed`) |

`link_state` (se a mensagem interna foi localizada) é **independente** do estado de cobrança:

- `linked`: `message_id` preenchido.
- `unlinked`: o registro está **vinculado ao `wamid`**, mas a mensagem interna **ainda não foi localizada**. A liquidação pode associá-lo depois, quando a mensagem ganhar o wamid. **Não é perda**: costuma ser só atraso ou corrida entre o webhook e o envio assíncrono. Só um `unlinked` antigo (mais de `unlinked_attention_hours`, default 24) é sinalizado como atenção.

### 4.5 `contact_status_events` (só inserção)

`id`; `organization_id`; `contact_id`; `from_status`; `to_status`; `actor_type` (`agent`, `ai`, `flow`, `system`, `contact`); `actor_user_id`; `reason` (`resolve_action`, `auto_resolve`, `inbound_reopen`, `transfer`, `api`); `occurred_at`. Índice `(contact_id, occurred_at)`. Grava **toda** transição (`new → in_progress → resolved` e as demais) na mesma transação de `transitionContactStatusDB`. É a base para delimitar atendimentos depois; **atendimento não é uma coluna** de `message_usage`, é derivado desses eventos.

## 5. Gravação: onde cada registro nasce

Um único `usage.Record(ctx, mensagem, origem)`, com `INSERT … ON CONFLICT DO NOTHING`, logo depois de a `Message` ser criada. **Falhar em gravar nunca bloqueia o envio** (log e contador).

| Caso | Ponto no código | Origem registrada |
|---|---|---|
| Agente (chat, contato, API) | `messages.go`, `contacts.go` | `agent` + `actor_user_id` |
| Resposta de protocolo | `occurrence_reply`, `occurrence_send` | `agent` + usuário + `occurrence_id` |
| Fluxo | `chatbot_graph_runner` e mensagens do nó Transferir (`agent_transfers`) | `flow` + `flow_id` + `flow_node_id` |
| Fluxo legado / palavra-chave | `chatbot_processor` | `flow` + `origin_detail=keyword` |
| IA | `generateAIResponse` e confirmação da transferência (`ai_tool_confirmation`) | `ai` + `origin_detail` = feature + `ai_usage_log_id` |
| Campanha | `worker/worker.go` | `campaign` + `campaign_id` + template |
| Sistema | `sla_processor` | `system` + `origin_detail=sla` |
| Eco do app Business | `processMessageEcho` | `external_app` |
| Entrada | `saveIncomingMessage` | `contact`, `direction=incoming`, `billable=false`, `not_billable` |

- **Template × livre:** template grava nome, id e `declared_category`. Texto livre, mídia e interativas entram com categoria provisória `service`. **A categoria final é a da Meta.**
- **Falha de envio:** erro da API antes de aceitar → `send_failed`, custo 0. Falha depois de aceita → `failed`, e é cobrável só se a Meta disser.
- **Reenvio:** hoje não há retry automático (o worker devolve "Don't retry"). Reenviar é outra mensagem, com outro wamid e outra linha. Se algum dia houver retry com o mesmo wamid, a unicidade garante uma linha só.
- **Origem:** `Origin` entra em `OutgoingMessageRequest` e viaja no `context` até os helpers `sendAndSave*` (mudança mecânica nas 29 chamadas).
- **Unidade:** `unit_id` do agente; senão da equipe da conversa; senão nulo; `unit_source` diz qual.
- **Job de integridade** (intervalo e janela configuráveis, ver seção 9): cria registro para toda mensagem sem `message_usage`, com `origin_inferred=true` (`SentByUserID` → `agent`; `campaign_id` do metadata → `campaign`; resto → `system` + `origin_detail=unclassified`). O total de "não classificadas" aparece no resumo.

## 6. Webhook da Meta

### 6.1 O que muda no tratamento

`processStatusUpdate` passa a: (1) **gravar o evento** em `message_pricing_events` (com ou sem pricing) **antes** do filtro de progressão de status; (2) **liquidar** o `message_usage` do wamid. O filtro de progressão continua valendo **só para o campo `status` da mensagem**, não para o pricing. Um `sent` tardio depois de `delivered` ainda contribui com pricing.

### 6.2 Regras

| Situação | Regra |
|---|---|
| Eventos que atualizam | `sent`, `delivered`, `read`, `failed` |
| Obter pricing | `status.pricing` (billable, pricing_model, category) e `status.conversation` (id, origem), se presentes naquele evento |
| `billable` | O que a Meta disser. Sem pricing em nenhum evento → `null` (desconhecido, **diferente de `false`**). `failed` sem pricing → `false`. Entrada → `false` |
| Webhook duplicado | O índice único descarta. Nada é recalculado |
| Fora de ordem | A liquidação é função do **conjunto** de eventos: o pricing vem do evento mais completo por `event_at`; o estado final é o de maior prioridade (`failed` vence). Nunca regride |
| Evento antes de existir a linha | Fica em `message_pricing_events`. A liquidação roda de novo quando a linha ganha o wamid (`finalizeMessageSend` e `worker`) |
| Linha ausente além do prazo de reconciliação (`unlinked_after_minutes`) | O job cria um `message_usage` com `link_state=unlinked` (ver 4.4). Mantém o custo conhecido pelo wamid; **não é tratado como perda** |
| **Início da medição** (baseline) | O job de integridade **não reconstrói consumo anterior ao início da medição**. O início é o `MIN(created_at)` de `message_usage` (lido, nunca gravado; com o ledger vazio o job não faz nada). Mensagens anteriores a ele são ignoradas; `unlinked` só nasce para um wamid cujo **envio foi testemunhado** depois do início, isto é, com um evento `sent` registrado com `event_at` igual ou posterior ao início, e cuja mensagem (se existir) também não é anterior a ele. Um evento de entrega ou leitura sozinho **não basta**: a mensagem pode ter sido enviada antes. **Exceção assumida:** uma mensagem posterior ao início cujo webhook `sent` se perdeu não é registrada como `unlinked` |
| Enviada, nunca entregue | Fica `pending`. Passado `undelivered_after_hours` sem `delivered`, `read` ou `failed` → `unconfirmed`, custo 0. Se a entrega chegar depois, reliquida |
| Entregue sem pricing | `awaiting_pricing`, `billable` e `estimated_cost` nulos. O resumo mostra em "sem informação da Meta" e um `provisional_cost` calculado pela categoria declarada, **separado** e nunca gravado |
| Pricing tardio ou diferente | Aceito a qualquer tempo. Igual ao gravado: não faz nada. Diferente: recalcula, move o anterior para `meta_pricing_history` (máx. 5) e marca `repriced_at` |

## 7. Cálculo

`estimated_cost = quantity × preço vigente`, com `quantity = 1`. O preço vem de `whatsapp_rates` por organização, país do destinatário (prefixo E.164 mais longo; sem país exato usa `*`), `billing_category` e `sent_at` dentro da vigência.

- `billable = true` e preço achado → `estimated_cost` calculado, `priced`.
- `billable = false` → `0.00`, `not_billable`.
- `billable = null` → nulo (nunca zero por omissão).
- Cobrável sem preço → nulo, `no_rate` (visível, nunca silencioso).

**Precedência quando a Meta e a tabela interna divergem:**

| Assunto | Prevalece |
|---|---|
| Categoria, cobrável ou não, modelo de preço | **A Meta.** A categoria do template vira só indício; se diverge, `category_diverged = true` |
| Valor unitário | **A tabela interna**, a única fonte: o webhook não traz valor por mensagem |
| Valor informado pela Meta depois (conciliação) | Vai para `meta_cost`, **campo separado**. **Nunca sobrescreve `estimated_cost`.** A diferença será um ajuste de conciliação, fase futura |

Regras adicionais: moedas diferentes **nunca se somam** (totais por moeda). Preço já usado é imutável (nova vigência). Cadastrar um preço faltante e acionar "reprecificar" reliquida as linhas `no_rate` da vigência. Arredondamento: `numeric(18,6)` por mensagem, soma sem arredondar, exibição com casas configuráveis.

## 8. API, permissão e tela

**Permissão nova** `whatsapp_usage:read|write`: admin tem as duas, manager só leitura. Entra no grupo Análises do catálogo (`permission_catalog.go`) com backfill aditivo e idempotente. Nenhuma permissão existente serve.

| Rota | Função | Permissão |
|---|---|---|
| `GET/POST/PUT /api/whatsapp-rates` (`DELETE` só se nunca usado) | Tabela de preços | read / write |
| `GET /api/whatsapp-usage/summary` | Totais | read |
| `GET /api/whatsapp-usage/messages` | Lista paginada de registros (sem conteúdo) | read |
| `POST /api/whatsapp-usage/reprice` | Reliquida `no_rate`, `awaiting_pricing` e `unconfirmed` | write |

**Filtros** (período no fuso da organização, conta, unidade, agente, categoria, direção). **O resumo traz:** total de mensagens; mensagens cobradas; "sem informação da Meta" (`awaiting_pricing`); `no_rate`; não classificadas; "aguardando vínculo" (`unlinked`, com os mais antigos que `unlinked_attention_hours` destacados); `estimated_cost` **por moeda**; `provisional_cost`.

**Tela:** Configurações › Canais › **Consumo do WhatsApp**, duas abas: **Consumo** (filtros, totais, tabela agrupável por dia, categoria, unidade, agente ou conta) e **Tabela de preços** (cadastro). Sem gráficos nem dashboard de eficiência. Os filtros de unidade usam `selectableOptions`.

## 9. Configuração

Seção `[usage]` do `config.toml`. Os prazos abaixo são **parâmetros operacionais configuráveis**; os valores são só **defaults**, pontos de partida sem base empírica ainda. A implementação **não espalha esses números pelo código**: eles são lidos de uma única estrutura (`Config.Usage`) e injetados em quem precisa.

| Chave | Default | O que é |
|---|---|---|
| `record_enabled` | `true` | Chave desligadora do registro de consumo |
| `unlinked_after_minutes` | `15` | Quanto esperar por uma mensagem interna antes de criar o registro `unlinked` |
| `unlinked_attention_hours` | `24` | A partir de quando um `unlinked` é destacado como atenção no resumo (só apresentação) |
| `integrity_interval_minutes` | `5` | De quanto em quanto tempo o job de integridade roda |
| `integrity_window_hours` | `48` | Até quanto tempo para trás o job procura mensagens sem registro |
| `undelivered_after_hours` | `72` | Depois de quanto tempo sem `delivered`, `read` ou `failed` a mensagem passa a `unconfirmed` |

**Natureza dessas decisões: são operacionais, não regras de negócio nem de cobrança.**

- `undelivered_after_hours` **não** conclui nada sobre cobrança nem sobre a mensagem: a ausência de `delivered`, `read` e `failed` leva apenas a uma classificação operacional de **"não confirmado"**. Se a confirmação chegar depois, a liquidação reclassifica normalmente (6.2).
- `unlinked_after_minutes` é um **mecanismo de reconciliação** entre o webhook e a mensagem interna. Não é regra de cobrança, e `unlinked` não é perda (4.4).
- `integrity_interval_minutes` e `integrity_window_hours` dimensionam o job de completude; mudá-los não altera nenhum valor já liquidado. `unlinked_attention_hours` só afeta o destaque na tela.

Valor inválido (não numérico, zero ou negativo) **falha o carregamento da configuração**, como nas demais seções. Os testes usam os mesmos parâmetros por injeção, nunca por constante embutida.

## 10. Testes obrigatórios

1. **Uma mensagem = no máximo um registro:** gravar duas vezes a mesma mensagem; gravação concorrente; send e job de integridade em corrida; inserção SQL direta que o índice único deve recusar; webhook repetido que não duplica nem recalcula.
2. **Webhook:** evento repetido; todas as permutações de `sent`/`delivered`/`read` com o mesmo resultado final; pricing tardio igual (nada muda) e diferente (recalcula, grava histórico); evento **sem** pricing (o evento é registrado e o pricing continua ausente); entregue sem pricing (`awaiting_pricing`, custo nulo); mensagem **não cobrável** (custo 0); evento antes da linha (liquida depois); `unlinked` que depois é associado; nunca entregue (`unconfirmed`) e depois entregue; `failed`.
3. **Cálculo:** limites da vigência; fallback de país; sem preço (`no_rate`) e reprecificação; moedas distintas; categoria divergente (Meta prevalece, `estimated_cost` não é sobrescrito por `meta_cost`).
4. **Origem:** um teste por ponto de envio da seção 5, mais entrada e eco. Unidade: agente → equipe → vazio.
5. **Histórico de status:** cada caminho de transição grava exatamente um evento, na mesma transação (rollback não deixa evento órfão).
6. **Segurança e dados:** isolamento entre organizações; manager só lê; migração aditiva e idempotente; nenhum conteúdo de mensagem nas tabelas novas.

## 11. Riscos e mitigação

| Risco | Mitigação |
|---|---|
| A medição só vale daqui em diante | Começar já; a base nasce aditiva e desligável (`usage.record_enabled`) |
| Mexer no caminho central do webhook | Regressão dos testes de status existentes; a progressão de `status` não muda |
| Envio sem origem | Job de integridade e contador de "não classificadas" |
| Estimado ≠ fatura da Meta | Nomes e campos separados; conciliação planejada |
| Volume (uma linha a mais por mensagem; campanhas grandes) | Insert único e leve, índices mínimos, lote nas campanhas |
| `unlinked` virar falso alarme | Estado próprio, só destacado quando antigo (`unlinked_attention_hours`, default 24; 4.4) |
| Meta mudar o payload de pricing | Os campos relevantes ficam em `message_pricing_events`; campo novo não quebra o parser |
| Gaps de país ou preço | Estado `no_rate` visível e reprecificação |
| Privacidade | Sem conteúdo; só ids e código do país |

## 12. Fora de escopo e próximos passos

Este documento só descreve a base de medição. Em ordem: revisão deste design → plano de implementação → implementação (migration, registro, webhook, cálculo, API, tela, testes) → fase de indicadores de eficiência e conciliação → cliente VoIP.
