# Fase 9D — Primeira ação de escrita da IA: `request_agent_transfer` com confirmação do cliente (nota de design)

Status: **revisão 2 (incorpora a revisão de segurança e consistência transacional). Nada foi implementado.** Branch: `feature/fase-9d-ai-transfer-confirmation`, criada de `development` em `b3a35b9` (9C mergeada, PR #85). Sequência: esta nota → revisão → aprovação explícita → implementação.

### Mudanças da revisão 2
1. **Blocker corrigido:** a recuperação de uma confirmação presa em `confirmed` **não** reutiliza o botão nem o token. Após `pending → confirmed` o token não autoriza mais nada; a recuperação é um caminho interno de **reconciliação** idempotente, com gatilho determinístico (§10).
2. As validações que não consomem a confirmação ficam **antes** do CAS; o CAS só ocorre quando tudo já passou (§8).
3. Removida a contagem de itens da reautorização.
4. `args` esclarecido como payload de integridade da ação, não registro de auditoria (§11).
5. Significado de `executed` na primeira linha de `ai_tool_calls` documentado sem ambiguidade (§12).
6. `GET /api/ai-tools/confirmations` mantida; teste manual do botão real mantido como pré-requisito (§17).

## 1. Pergunta que a 9D responde

> Como a IA do chatbot pode *propor* uma ação que altera dados (transferir a conversa para a fila de atendimento), de modo que só o cliente a autorize, só uma vez, exatamente como lhe foi mostrada, com tudo registrado e sem nenhuma chance de a IA dizer que algo aconteceu sem ter acontecido?

É a primeira tool `RiskWrite`. A 9B deixou a escrita **sempre negada** (`confirmation_unavailable`); a 9D substitui esse bloqueio por um mecanismo real, mas deixa tudo desligado por padrão.

## 2. Decisões já fechadas

| # | Decisão |
|---|---|
| 1 | Tool `request_agent_transfer`, única de escrita; **sem destino/equipe** (sempre a fila); `reason` opcional, até 200 caracteres, sanitizado, dado não confiável |
| 2 | A IA **não executa**: a tool só cria uma proposta pendente; o retorno ao modelo diz explicitamente que a transferência **não foi realizada**; o texto do modelo nunca pode afirmar que ocorreu |
| 3 | Confirmação **exclusivamente pelo cliente**, **exclusivamente pelo botão** gerado pelo servidor; "sim", "quero" etc. digitados não confirmam; token aleatório, uso único, **CAS** contra reuso e duplo processamento |
| 4 | Mensagem de confirmação **integralmente montada pelo servidor**; o texto que o modelo produziu naquele turno não é reaproveitado |
| 5 | Validade, limite e pausa são **configuração**, com defaults 10 min, 3 propostas por 60 min por contato e 30 min após recusa |
| 6 | Cadeia de ativação com `ai_tools.write_enabled = false` por padrão |
| 7 | Tabela própria `ai_tool_confirmations` (não `ChatbotSession.SessionData`) |
| 8 | **Reautorização completa** no momento da confirmação |
| 9 | Fora do horário: `outside_hours`, nenhuma transferência; a mensagem de fora do horário é do servidor |
| 10 | Origem `ai_confirmed`, `TransferredByUserID = NULL`; quem confirmou é registrado à parte, como o contato |
| 11 | Escopo só `chatbot_reply`; o nó `ai_response` de fluxo fica fora |
| 12 | Nenhuma alteração de frontend |
| 13 | Idempotente também do lado da transferência, não só do token. `executed` = a transferência **foi criada**; `confirmed` = apenas que o cliente autorizou |
| 14 | Tokens, digest e demais segredos **nunca** aparecem nas APIs de auditoria |
| 15 | **Após `pending → confirmed` o token não pode autorizar uma nova execução.** Recuperação só por reconciliação interna idempotente |

## 3. Estado atual (auditado)

- `createTransferToQueue`/`createTransferToTeam` (`agent_transfers.go`) **não devolvem resultado**: logam erro e, fora do horário, enviam elas mesmas a `OutOfHoursMessage`. O corpo comum é `saveAndFinalizeTransfer` (SLA, linha, auditoria, broadcast; para a fila, `endChatbotSession=false`).
- **Uma transferência ativa por contato é garantida pelo banco**: índice único parcial (`EnsureActiveTransferUniqueness`), e `createTransferRow` converte a violação em `ErrTransferAlreadyActive`. Se o índice tiver sido pulado por duplicatas antigas (a migração avisa), vale a checagem `hasActiveAgentTransfer`. É isso que torna a execução da transferência idempotente.
- `TransferSource`: `manual`, `flow`, `keyword`, `chatbot_disabled`, `agent_initiated` (coluna de texto de 20 caracteres, sem enum no banco).
- Webhook: `extractMessageContent` entrega o `ButtonID` de respostas de botão e lista; `sendAndSaveInteractiveButtons(account, contact, body, buttons)` envia botões de resposta. O ramo de IA de `chatbot_reply` é `generateAIResponse(...)` seguido de `sendAndSaveTextMessage`.
- Há um padrão de varredura periódica no servidor: `PresenceReaper` (`presence_reaper.go`), criado e iniciado em `cmd/whatomate/main.go` com um `ticker` de 10 s e parado no encerramento. O reconciliador da §10 segue o mesmo padrão.
- `ai_tool_calls.status` é `varchar(12)`: os estados da confirmação **não** entram nessa coluna (ela não muda); ficam em `ai_tool_confirmations`.
- 9B/9C: `Authorize` nega escrita; `governedTool.Execute` audita `requested`, constrói a `Factory` só depois e registra o desfecho; `Definitions()` só oferece o que está efetivamente disponível; `[ai_tools] providers = []` impede qualquer tool.

## 4. Fluxo e máquina de estados

```
cliente: "quero falar com uma pessoa"
  └ IA (chatbot_reply) chama request_agent_transfer {reason?}
      └ GovernedTool.Execute (write): autoriza → grava "requested" → constrói a tool
          └ Propose: valida args, checa estado (já com atendente? fora do horário? limite? pausa?)
              ├ não cabe propor      → ToolResult informativo ao modelo, nenhuma confirmação
              └ cabe propor          → cria confirmation = pending; ToolResult:
                                        {"status":"awaiting_customer_confirmation","transfer_performed":false}
  └ o servidor descarta o texto do modelo e envia SUA mensagem com os botões [Sim, transferir] [Não, obrigado]

cliente toca [Sim]  (button_reply "aitc:<token>")
  └ 1. validações que NÃO consomem a confirmação (§8)
         ├ alguma negou        → a proposta fecha direto (denied / not_executed); nunca passa por "confirmed"
         └ todas passaram
  └ 2. CAS  pending → confirmed        (perdeu o CAS: nada acontece)
  └ 3. serviço de transferência (idempotente) → created | already_active | outside_hours | erro
  └ 4. registra o desfecho + auditoria; responde ao cliente

cliente toca [Não]  (button_reply "aitd:<token>") → CAS pending → declined; "Tudo bem, seguimos por aqui."
nada acontece até a validade                      → expired (verificado no uso)
nova proposta com uma pendente                    → a anterior vira superseded
processo cai entre 2 e 4                          → reconciliador interno (§10); o botão NÃO é reaproveitado
```

Estados de `ai_tool_confirmations.status` e transições (toda transição é um `UPDATE … WHERE status = '<estado anterior>'`, isto é, um CAS):

| Estado | Significado | Vem de |
|---|---|---|
| `pending` | proposta feita, esperando o cliente | criação |
| `confirmed` | o cliente autorizou e o CAS foi ganho; desfecho ainda desconhecido | `pending` |
| `executed` | **a transferência foi criada** (`outcome=created`) | `confirmed` |
| `not_executed` | nada foi criado por razão de negócio: `outcome=already_active` ou `outside_hours` | `confirmed`, ou `pending` direto quando a checagem pré-CAS já mostra isso |
| `failed` | autorizada, mas erro ao executar (ou reconciliação esgotada/vencida) | `confirmed` |
| `declined` | o cliente recusou | `pending` |
| `expired` | passou da validade sem confirmação | `pending` |
| `superseded` | substituída por uma proposta mais nova do mesmo contato | `pending` |
| `denied` | a reautorização negou antes do CAS (`denial_reason`) | `pending` |

`confirmed` é **transitório**: só existe entre o CAS e o desfecho. Uma linha que permanece em `confirmed` é "autorizada, desfecho desconhecido" e é tratada **somente** pelo reconciliador (§10).

## 5. A tool

**`request_agent_transfer`** (risco `write`). Descrição para o modelo: pede *uma proposta* de transferência para a fila de atendimento humano; **não transfere**; o cliente decide; usar quando o cliente pede falar com uma pessoa ou o assunto exige humano. Schema (só palavras-chave portáveis):

```json
{"type":"object","properties":{"reason":{"type":"string","description":"Short reason, in the customer's words. Optional."}}}
```

- **Argumentos** por `DecodeArgs` estrito: só `reason`; qualquer outro campo (inclusive `team_id`, `contact_id`, `agent_id`) é recusado.
- **`reason`**: `cleanText` (controles, bidi e zero-width removidos, espaços colapsados) e corte em **200 caracteres**; vazio vira ausente. É **dado não confiável do modelo em todo o percurso**: nunca é interpretado como instrução, nunca decide nada (nem destino, nem prioridade), só vira texto na `Note` da transferência, limitado e rotulado como tal.
- **Resultado ao modelo** (JSON fixo, sem texto do cliente):
  - proposta criada: `{"status":"awaiting_customer_confirmation","transfer_performed":false}`;
  - não cabe propor: `{"status":"not_available","reason":"already_with_agent|outside_business_hours|too_many_requests|recently_declined"}`. Estados de **negócio** são informativos (o modelo precisa explicar ao cliente); negações de **governança** (chave desligada, sem opt-in, provedor) continuam genéricas, como na 9B.
- A descrição e o resultado instruem o modelo a **não afirmar** que a transferência ocorreu; mesmo assim a garantia é do servidor (§7), não da obediência do modelo.

## 6. Política e cadeia de ativação

```
ai_tools.enabled → opt-in da organização para a tool → provedor em ai_tools.providers
→ ai_tools.write_enabled → política permite RiskWrite → (proposta) → confirmação do cliente
→ reautorização → execução
```

- `PolicyInput` ganha `WriteEnabled` e `ConfirmationAvailable`. Ordem: `global_off` → `unknown_tool` → `not_enabled` → para escrita: `write_disabled` (nova razão) se `write_enabled` estiver desligado → `confirmation_unavailable` se não houver confirmador ligado → permitido **com `NeedsConfirmation`**.
- `Definitions()` só oferece a tool de escrita se **toda** a cadeia valer. Chamada forçada a ela fora disso é negada e auditada como hoje, sem construir nada.
- `feature`: a tool só existe para `chatbot_reply`; no nó `ai_response` de fluxo ela não é oferecida nem resolvida.
- `ToolSpec` de escrita **exige** `WriteFactory` (e não `Factory`); a validação do catálogo recusa uma spec `write` sem confirmação definida. Uma tool de escrita **nunca** recebe o `ReadDB`: recebe `WriteDeps{Transfers TransferService}`.

## 7. A IA não pode afirmar a transferência

- A tool não executa nada; o retorno diz `transfer_performed:false`.
- Se, durante um turno, **qualquer** confirmação foi criada, o servidor **descarta o texto final do modelo** e envia só a mensagem da confirmação. O texto do modelo nunca chega ao cliente naquele turno e **não é reaproveitado** depois.
- `generateAIResponse` passa a ter um irmão interno que devolve `aiReply{Text, Buttons}`; o ramo `chatbot_reply` envia texto ou botões conforme o caso. O nó de fluxo continua recebendo só texto, e nunca recebe a tool de escrita.
- A mensagem é fixa, em português, montada pelo servidor (nada do modelo, nem o `reason`):
  - confirmação: *"Posso transferir seu atendimento para um atendente humano? Toque em “Sim, transferir” para confirmar. Esta solicitação vale por N minutos."* com os botões **Sim, transferir** e **Não, obrigado**;
  - após `created`: *"Pronto! Você entrou na fila de atendimento. Um atendente vai responder em breve."*;
  - recusa: *"Tudo bem, seguimos por aqui."*; expirada: *"Essa solicitação expirou. Se ainda quiser falar com um atendente, é só pedir."*;
  - `outside_hours`: a `OutOfHoursMessage` configurada (como hoje); `already_active`: *"Você já está sendo atendido por nossa equipe."*
- Efeito colateral aceito: o cliente vê só a mensagem do servidor naquele turno, não a prosa do modelo.

## 8. Reautorização na confirmação

Nada do estado da proposta é confiado. Ao tocar o botão, o servidor refaz **toda** a verificação de segurança, em duas fases com papéis diferentes.

### 8.1 Antes do CAS: validações que não consomem a confirmação

- `ai_tools.enabled` e `ai_tools.write_enabled` ligados agora;
- a tool existe no catálogo e segue habilitada para a organização (opt-in lido agora);
- o provedor **atual** da organização está em `ai_tools.providers`;
- a política atual permite `RiskWrite` com confirmação;
- a confirmação existe e pertence à mesma **organização + contato + sessão** do remetente (o contato vem do webhook verificado), está `pending`, não passou de `expires_at` e o **digest recalculado** (HMAC de organização, contato, tool e `args` canônicos) coincide com o guardado;
- o chatbot continua habilitado para a conta;
- o contato ainda não tem transferência ativa e o horário de atendimento permite (consulta ao serviço de transferência, sem criar nada).

Se alguma falhar, a proposta **fecha direto**, também por CAS a partir de `pending`, sem passar por `confirmed`: `denied` (com `denial_reason`) para falhas de segurança, `not_executed` (com `outcome`) para `already_active`/`outside_hours`, `expired` para validade. Nada foi "autorizado", então nada foi consumido por uma autorização. Qualquer negação responde ao cliente com texto genérico do servidor.

### 8.2 O CAS e a execução

- Só com **todas** as validações da fase anterior aprovadas: `UPDATE … SET status='confirmed', confirmed_at=…, confirmed_by_contact_id=…, confirm_wamid=… WHERE id=? AND status='pending'`. Quem perde o CAS não faz nada.
- Depois do CAS começa a execução (§9) e, no fim, o desfecho (§4).
- Há uma janela inevitável entre a checagem de transferência ativa e o CAS (outro caminho pode criar uma transferência no meio). Ela é inofensiva porque **a última barreira é o próprio serviço de transferência**: `createTransferRow` + índice único (ou `hasActiveAgentTransfer`) devolvem `already_active`, que vira `not_executed/already_active`. Nenhuma segunda transferência nasce.

## 9. Serviço de transferência (extração)

```go
type TransferOutcome string // created | already_active | outside_hours
type TransferResult struct { Outcome TransferOutcome; TransferID uuid.UUID }

// TransferToQueue não envia mensagem alguma e devolve o desfecho; erro só para falha real.
func (a *App) TransferToQueue(ctx, account, contact, source, notes string) (TransferResult, error)
```

- `createTransferToQueue` passa a ser um invólucro fino: chama o serviço e, se `outside_hours`, envia a `OutOfHoursMessage` (o mesmo que faz hoje). **Comportamento e testes existentes preservados** (suíte inalterada).
- O serviço reutiliza `hasActiveAgentTransfer`, a regra de horário (`internal/businesshours`) e `saveAndFinalizeTransfer`; a origem `ai_confirmed` e a `Note` entram como parâmetros. `TransferredByUserID` fica `NULL`.
- `Note` da transferência (visível ao atendente): *"Solicitado pelo assistente de IA e confirmado pelo cliente em <data UTC>."* e, se houver, *"Motivo informado pela IA (não verificado): <reason>"*.
- `createTransferToTeam` **não** é tocada (a 9D só usa a fila).

## 10. Idempotência e reconciliação

### 10.1 Regra central

**Depois de `pending → confirmed`, o token não autoriza mais nada.** Um toque em botão cuja confirmação não está `pending` nunca executa, nunca reconcilia e nunca reenvia mensagem. O botão é um mecanismo de **autorização de uso único**; a recuperação de uma execução interrompida é outro mecanismo, interno, que **não** é acionado por botão.

### 10.2 Entradas do webhook (toque no botão)

| Situação | Resultado |
|---|---|
| Mesmo botão tocado duas vezes / webhook reentregue | o 2º CAS perde; **nenhuma ação e nenhuma mensagem**; o `wamid` do toque é guardado e uma repetição dele é ignorada |
| Dois toques simultâneos (instâncias diferentes) | um único CAS vence; o outro sai sem fazer nada |
| Toque em proposta expirada | CAS `pending → expired`; resposta "expirou" **uma única vez** (quem ganha o CAS responde) |
| Toque em `superseded`/`declined`/`denied`/`not_executed`/`executed`/`failed` | sem ação, sem mensagem repetida |
| **Toque em `confirmed`** | **sem ação e sem mensagem**: nunca é um gatilho de recuperação |
| Cliente digita "sim" | nada acontece; a mensagem do servidor já diz para tocar no botão |
| Cliente ignora os botões | a proposta expira; nenhuma pendência bloqueia o chat |
| `already_active` (outro caminho criou uma transferência antes) | `not_executed/already_active`; nenhuma segunda transferência (índice único + checagem) |

### 10.3 Reconciliação de uma execução interrompida

Uma confirmação em `confirmed` por mais tempo que o normal significa que o processo caiu (ou falhou ao gravar) entre o CAS e o desfecho. O gatilho é **determinístico e independente do cliente**: um **reconciliador periódico**, no mesmo padrão do `PresenceReaper` (um `ticker` iniciado em `cmd/whatomate/main.go` e parado no encerramento), configurável e desligado só se o intervalo for 0.

A cada varredura ele seleciona confirmações com `status='confirmed'` e `confirmed_at` mais antigo que `reconcile_min_age_seconds` (para não competir com a execução normal em curso) e as **reivindica** por CAS: `UPDATE … SET reconcile_claimed_at=now(), reconcile_attempts=reconcile_attempts+1 WHERE id=? AND status='confirmed' AND (reconcile_claimed_at IS NULL OR reconcile_claimed_at < now() - <min_age>)`. Só quem ganha a reivindicação trabalha, o que impede dois processos (ou duas varreduras) de reconciliarem a mesma linha ao mesmo tempo. Para cada uma reivindicada:

1. **Idade máxima:** se `confirmed_at` é mais antigo que `confirmation_ttl_minutes`, a autorização está velha demais; não executa: vira `failed` com `error_kind=reconcile_stale` e nenhuma transferência.
2. **Reautorização** (a mesma verificação de segurança da §8.1: chaves, opt-in, provedor, política, chatbot); se negar, `denied`, sem executar. Reconciliar nunca amplia a autoridade que valia no momento do toque.
3. **O que já existe:** procura a transferência `ai_confirmed` do contato criada **depois de `confirmed_at`**; se existir, registra `executed` com o `transfer_id` dela (a execução original tinha chegado a criá-la).
4. **Senão, executa de forma idempotente** pelo mesmo serviço: ou cria, ou devolve `already_active`/`outside_hours` (`not_executed`).
5. Grava o desfecho e a auditoria; envia ao cliente a mensagem do desfecho (uma vez; é "melhor esforço", e uma queda entre gravar e enviar perde apenas a mensagem, nunca repete a ação).
6. Após `reconcile_max_attempts` tentativas sem sucesso: `failed` com `error_kind=reconcile_exhausted`; o erro é logado sem conteúdo.

Cada tentativa deixa linha de auditoria; a execução reconciliada usa o mesmo ator (`human`/`customer_confirmation`) porque a autorização foi do cliente, e marca `error_kind`/`outcome` conforme o caso.

### 10.4 Falha ao registrar o desfecho (caminho normal)

Se a transferência foi criada mas o desfecho não pôde ser gravado, a transferência **não** é desfeita nem repetida (regra da 9B); a linha fica em `confirmed` e o reconciliador a fecha pelo passo 3 acima, sem criar nada novo.

## 11. Armazenamento: `ai_tool_confirmations` (migração aditiva)

| Coluna | Observação |
|---|---|
| `id`, `organization_id` | |
| `contact_id`, `session_id`, `run_id` | contexto; nunca vindos do modelo |
| `tool_name`, `risk` | `request_agent_transfer`, `write` |
| `token_hash` (único) | SHA-256 do token; o token em claro só existe no botão enviado ao cliente |
| `args` (jsonb) | **payload de integridade da ação**: os argumentos canônicos e sanitizados (`{"reason":"…"}`), exatamente o que será executado. **Não é registro de auditoria legível**, serve só para reproduzir com fidelidade a ação confirmada (e para recalcular o digest); nenhuma API o devolve |
| `action_digest` | HMAC-SHA256 (segredo do servidor, rótulo `whatc/ai_tools/confirm/v1`) de organização, contato, tool e `args` canônicos |
| `status`, `outcome`, `denial_reason`, `error_kind` | §4 |
| `proposed_at`, `expires_at`, `confirmed_at`, `finished_at` | |
| `confirmed_by_contact_id`, `confirm_wamid` | quem confirmou (o contato) e a mensagem do toque |
| `reconcile_claimed_at`, `reconcile_attempts` | controle do reconciliador (§10.3) |
| `proposal_call_id`, `execution_call_id`, `transfer_id` | elos com `ai_tool_calls` e `agent_transfers` (sem tocar nessas tabelas) |

Índices: `(organization_id, contact_id, proposed_at DESC)` (também serve ao limite e à pausa), único em `token_hash`, `(status, confirmed_at)` (o reconciliador) e `(status, expires_at)`. **Nenhuma tabela existente muda** (`ai_tool_calls`, `agent_transfers`, `chatbot_sessions` intactas; `ai_tool_calls.status` continua sem os novos estados). Token: 16 bytes de `crypto/rand` em base64url; ids dos botões `aitc:<token>` e `aitd:<token>` (bem abaixo do limite de 256 do WhatsApp).

## 12. Auditoria

- **Primeira linha, a proposta.** A chamada da IA gera a linha usual em `ai_tool_calls` (`requested` → `executed`), ator `ai`/`chatbot_reply`, assunto o contato. **Nesta linha, `executed` significa somente que a execução da tool `request_agent_transfer` foi concluída e resultou na criação da proposta. Não significa que um `AgentTransfer` foi criado.** O schema de `ai_tool_calls` não comporta um campo novo para distinguir isso sem alterar uma tabela existente, então a distinção fica documentada aqui, no comentário do campo e no DTO, e a verdade sobre a transferência está em `ai_tool_confirmations` (`status`, `outcome`, `transfer_id`).
- **Segunda linha, a execução autorizada pelo cliente.** Uma segunda linha em `ai_tool_calls` (`requested` antes de executar → `executed`/`failed`, ou `denied` se a reautorização negar), com `actor_kind="human"`, `actor_ref="customer_confirmation"`, assunto o contato, **sem argumentos** (chaves e HMAC vêm da proposta). Aqui `executed` significa que o serviço foi executado; se a transferência foi realmente **criada** ou não (`already_active`, `outside_hours`) está em `ai_tool_confirmations.outcome`. A execução reconciliada (§10.3) usa a mesma forma.
- As duas linhas se ligam pela `ai_tool_confirmations`; a cadeia fica reconstruível: IA propôs → cliente confirmou (ou não) → o que foi feito. Ator `ai` (propõe) e ator `human` (autoriza) permanecem separados; a IA nunca consta como quem executou.
- **APIs**:
  - `GET /api/ai-tools/calls` continua como na 9C (metadados; a linha do cliente aparece com `actor_kind=human`);
  - **`GET /api/ai-tools/confirmations`** (`ai_tools:read`, só leitura, paginado, filtros `status`, `outcome`, `contact_id`, `from`/`to` em UTC): permite saber quantas propostas estão pendentes, recusadas, expiradas, executadas, presas em `confirmed` ou falhas. DTO explícito com `id`, `tool_name`, `status`, `outcome`, `denial_reason`, `error_kind`, `proposed_at`, `expires_at`, `confirmed_at`, `finished_at`, `subject_contact_id`, `session_id`, `run_id`, `transfer_id`. **Nunca**: token, `token_hash`, `action_digest`, `args`/`reason`, `confirm_wamid`, `reconcile_*`.
- Sem tela.

## 13. Configuração (defaults exatamente nestes números)

```toml
[ai_tools]
write_enabled = false                 # chave extra para a primeira escrita
confirmation_ttl_minutes = 10         # validade da proposta (e idade máxima de uma reconciliação)
proposal_limit = 3                    # propostas por contato...
proposal_window_minutes = 60          # ...nesta janela
decline_cooldown_minutes = 30         # sem nova proposta depois de uma recusa
reconcile_interval_seconds = 30       # varredura do reconciliador (0 = desligado)
reconcile_min_age_seconds = 60        # só reconcilia o que está em "confirmed" há mais que isto
reconcile_max_attempts = 3
```

Valor ausente ou fora dos limites cai no default (limites: TTL 1–60, limite 1–20, janela 1–1440, pausa 0–1440 com 0 = sem pausa, intervalo 0–3600, idade mínima 10–3600, tentativas 1–10). O limite conta propostas criadas na janela, em qualquer estado; a pausa conta a partir do `declined` mais recente. Nenhum número fica fixo no código além dos defaults.

## 14. Migração e compatibilidade

- Uma tabela nova (aditiva, `AutoMigrate` via `-migrate`) e o valor `ai_confirmed` em `TransferSource` (coluna de texto, sem alteração de schema). Nada em `chatbot_settings`, `ai_usage_logs`, `audit_logs`, `ai_tool_calls`, `agent_transfers`.
- Desligado por padrão em quatro níveis (`ai_tools.enabled`, opt-in da organização, `providers = []`, `write_enabled = false`). Enquanto `providers = []`, **nenhuma escrita pode ser ativada**. O fluxo atual do chatbot não muda.
- Refatoração da transferência: comportamento idêntico (mesmos testes).
- O reconciliador não faz nada enquanto não existir confirmação em `confirmed` (o que só ocorre com tudo ligado).

## 14.1 Fora da 9D

Escolha de equipe/destino, `createTransferToTeam` pela IA, confirmação por texto livre, confirmação por atendente, escrita no nó `ai_response`, tela de qualquer tipo, mais tools de escrita (ocorrência, oportunidade), job de limpeza/retenção das confirmações, mudança no Knowledge/RAG, X2.

## 15. Plano de commits (locais, sem push até revisão)

1. `feat(models): ai_tool_confirmations, ai_confirmed source and write config` (+ defaults/limites e testes de config)
2. `refactor(handlers): transfer service with a typed outcome` (`TransferToQueue`; `createTransferToQueue` como invólucro; testes de equivalência)
3. `feat(aitools): write policy chain and write-tool spec` (`write_enabled`, `write_disabled`, `NeedsConfirmation`, validação do catálogo)
4. `feat(aitools): confirmation store` (propor, validações pré-CAS, CAS por estado, expirar, substituir, limite e pausa, digest, hash do token, reivindicação do reconciliador)
5. `feat(aitools): request_agent_transfer` (propose/execute, `reason` sanitizado, `WriteDeps`; fora do catálogo de produção até o commit 7)
6. `feat(aitools): governed write path and confirmer` (`Execute` de escrita propõe; reautorização em duas fases; segunda linha de auditoria; reconciliação como função pura e testável)
7. `feat(handlers): customer confirmation over WhatsApp` (mensagem do servidor no lugar do texto do modelo, tratamento de `aitc:`/`aitd:`, idempotência de webhook, fiação e inclusão no catálogo)
8. `feat(handlers): confirmation reconciler and GET /api/ai-tools/confirmations` (varredura no padrão do `PresenceReaper`, iniciada em `main.go`; API de consulta)
9. `test: end-to-end confirmation scenarios` (os cenários de §16 com provedor simulado)

## 16. Critérios de aceite e testes

**Segurança da escrita**
- Com qualquer elo da cadeia desligado (`enabled`, opt-in, `providers`, `write_enabled`), a tool não é oferecida; chamada forçada é negada e auditada, e **nada** é construído nem criado.
- A tool nunca cria `AgentTransfer`: após uma proposta há uma `pending` e **zero** transferências.
- O retorno ao modelo contém `transfer_performed:false`; o texto do modelo daquele turno **não é enviado** ao cliente; o cliente recebe só a mensagem do servidor (teste com um modelo que "mente" dizendo "transferi!").
- Texto digitado ("sim", "quero", "ok") não confirma nada. Só `aitc:<token>` válido.

**Confirmação e CAS**
- Token de uso único: segundo toque, reentrega do mesmo `wamid` e toques simultâneos produzem **exatamente uma** transferência e no máximo uma mensagem.
- **Toque em `confirmed` não faz nada** (nem executa, nem reconcilia, nem responde); só o reconciliador age sobre `confirmed`.
- Token de outro contato, de outra organização, expirado, recusado, substituído ou adulterado: nenhuma ação.
- Digest: alterar os `args` guardados invalida a confirmação.
- **Validações pré-CAS não consomem a autorização**: com `write_enabled` desligado, opt-in removido, provedor fora da lista, chatbot desligado, transferência já ativa ou fora do horário entre a proposta e o toque, a proposta fecha direto (`denied`/`not_executed`) **sem passar por `confirmed`**, e nenhuma transferência é criada.

**Desfechos**
- `created` ⇒ `executed`, `agent_transfers.source = ai_confirmed`, `transferred_by_user_id` nulo, `Note` com o motivo rotulado como não verificado, `transfer_id` ligado.
- `already_active` ⇒ `not_executed/already_active`, sem segunda transferência (inclusive com o índice único ausente: checagem de aplicação; e com a corrida entre a checagem e o CAS, resolvida pelo serviço).
- `outside_hours` ⇒ `not_executed/outside_hours`, mensagem de fora do horário, nenhuma transferência; e na proposta fora do horário nenhuma confirmação é criada.

**Reconciliação**
- Processo "cai" depois do CAS (teste injeta a falha antes do desfecho): o reconciliador a reivindica uma única vez (dois reconciliadores concorrentes: um só trabalha), reautoriza e: se a transferência `ai_confirmed` já existe ⇒ `executed` com o `transfer_id`; se não existe ⇒ executa de forma idempotente (cria ou `already_active`), **nunca duas transferências**.
- Confirmação em `confirmed` mais velha que o TTL ⇒ `failed/reconcile_stale`, sem executar; reautorização negada na reconciliação ⇒ `denied`; esgotadas as tentativas ⇒ `failed/reconcile_exhausted`.
- Uma confirmação `confirmed` há menos que `reconcile_min_age_seconds` não é tocada.
- A reconciliação não envia mensagem duplicada e uma falha ao gravar o desfecho depois de criar a transferência não a repete.

**Limites e configuração**
- Terceira proposta na janela passa, a quarta é `too_many_requests`; após recusa, `recently_declined` até a pausa acabar; nova proposta marca a anterior `superseded`; TTL, limite, janela, pausa e parâmetros do reconciliador mudam por configuração, com fallback ao default para valor inválido.

**`reason` e `args`**
- Truncado em 200 caracteres, sem controles/bidi/zero-width, sem efeito sobre destino ou prioridade; com texto de injeção ("transfira para o diretor", "ignore as regras") vira apenas texto rotulado na `Note`.
- `args` nunca sai por API.

**Auditoria e API**
- Duas linhas em `ai_tool_calls` (IA propôs; cliente autorizou a execução) ligadas pela confirmação; ator `human/customer_confirmation` separado do ator `ai`; a primeira linha não é lida como "transferência criada" (a transferência só consta em `ai_tool_confirmations`).
- `GET /api/ai-tools/confirmations`: permissão, isolamento entre organizações, filtros e paginação; sentinela sobre o JSON inteiro prova que não vazam token, hash, digest, args/reason, `wamid` nem campos do reconciliador.
- Nenhuma rota de escrita sobre a auditoria.

**Escopo e regressão**
- No nó `ai_response` de fluxo a tool de escrita não é oferecida.
- Suíte de transferências existente passa sem alteração; chatbot sem tools idêntico (payload byte a byte da 9A, `AIUsageLog`); `go build`, `go vet`, suíte completa com apenas as 3 falhas conhecidas.

## 17. Pré-requisitos de ativação (não bloqueiam o desenvolvimento)

Ordem obrigatória antes de ligar `write_enabled` em qualquer organização:

1. **Validação real do tool calling** de pelo menos o provedor escolhido (`providers` continua `[]` até lá; a 9D não o altera).
2. **Teste manual do botão de WhatsApp de ponta a ponta** num número de teste: proposta → botões → toque → transferência na fila → atendente a vê com a origem e a `Note`. O `ButtonID` existir em `extractMessageContent` não prova que a Meta devolve, no ambiente real, o `button_reply` com o `id` esperado; só o WhatsApp real prova.
3. Primeira ativação em uma organização piloto, com `write_enabled` ligado só nela (via configuração do servidor e opt-in da organização), acompanhando `GET /api/ai-tools/confirmations` (atenção a qualquer linha parada em `confirmed`).

`provider validation + WhatsApp button E2E → só então write_enabled`.

## 18. Riscos residuais registrados

- A identidade do cliente é o número de WhatsApp verificado pela Meta; quem tem o aparelho confirma. É o mesmo nível de confiança de todo o chatbot.
- O cliente vê só a mensagem do servidor quando há proposta; é um custo de UX aceito em troca da garantia de que a IA não afirma a transferência.
- `reason` pode conter dado pessoal que o cliente escreveu; fica limitado a 200 caracteres, em `Notes` (visível a atendentes, como a conversa) e em `args` da confirmação (payload de integridade, nunca exposto). Retenção/limpeza das confirmações fica para outra fase.
- A mensagem do desfecho da reconciliação é melhor esforço: uma queda no instante entre gravar e enviar perde a mensagem, nunca repete a ação.
- Reentrega do webhook depende do `wamid` e do CAS; o índice único de transferência ativa é a última barreira contra duplicação.
