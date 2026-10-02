# Fase 9B — Governança de Tools: autorização, catálogo, ator de IA e auditoria (nota de design)

Status: **revisão 2 (incorpora os 4 ajustes da revisão do design e fecha as 7 perguntas). Nada foi implementado; implementação só após aprovação formal desta versão.** Branch: `feature/fase-9b-tools-governance`, criada a partir de `development` em `91d9fd1` (9A mergeada, PR #83).

### Mudanças da revisão 2
1. `Factory` só roda em `GovernedTool.Execute`, depois de autorização + `requested` gravado; `Resolve` nunca instancia a tool real (§4.1, §4.2).
2. Falha ao atualizar o desfecho da auditoria: não desfaz nem repete a execução, a linha fica em `requested`, log `ERROR` (§4.2).
3. Argumentos auditados com HMAC-SHA256 canônico (`args_hmac_sha256`), não SHA-256 simples (§8).
4. `DeniedTool` explícita: `Definitions()` só lista o disponível, `Resolve` aceita qualquer nome, resposta genérica ao modelo, motivo só na auditoria (§4.1, §4.2).
5. As 7 perguntas abertas viraram decisões (§15).

## 1. Pergunta que a 9B responde

> Quem pode permitir que a IA do chatbot execute uma tool, como cada tentativa é autorizada, quem consta como autor e o que fica registrado, sem que nenhuma tool real exista ainda?

A 9B é **governança**, não capacidade. Entrega o mecanismo que 9C (tools read-only) e 9D (primeira escrita + confirmação humana) vão usar. Nenhuma tool real entra: o catálogo de produção fica **vazio**.

Premissas já fechadas (9A e revisões): só o chatbot aciona; sem Agent Assistant, sem IA em nome de usuário humano, sem tools administrativas, sem criar/alterar fluxos por IA; toda escrita exigirá confirmação humana explícita (9D); `RunToolLoop` não faz retry; `ToolResolver` e `Tool` são portas separadas, e é nelas que a 9B se encaixa.

## 2. Estado atual (auditado)

- `internal/ai` (9A): `RunToolLoop(ctx, p, req, ToolResolver, Limits)`. O loop chama `Resolve(name)` e depois `Execute(ctx, call)`; devolve `LoopResult` sem `Opaque`. Nada em produção o chama; `completeAI`/`generateAIResponse` seguem como antes.
- Permissões: RBAC por `Resource`+`Action` (`internal/models/roles.go`), checado por `requireAuth`/`HasPermission`. Precedente de recurso novo: `ResourceKnowledge` (permissões semeadas + `BackfillKnowledgePermissions`, só para o papel de sistema `admin`, idempotente por organização).
- Auditoria existente: `audit.LogAudit` grava `AuditLog` **de forma assíncrona** (goroutine, falha só é logada) e exige `UserID`/`UserName` **não nulos**. Serve para ações de usuários humanos; **não serve** para a IA (não é usuário, e fire-and-forget não é aceitável para autorizar escrita).
- Uso de IA: `AIUsageLog` (uma linha por chamada ao provedor, sem prompts/respostas), sem conceito de execução de tool, e a decisão da 9A é **não** usá-lo para isso.
- Flags opt-in: `knowledge.rag_enabled` (config global) **e** `ChatbotSettings.knowledge_enabled` (por organização, padrão `false`): os dois precisam estar ligados.
- Transferências do chatbot usam funções internas (`createTransferToQueue`/`createTransferToTeam`); `AgentTransfer.TransferredByUserID` nulo significa "sistema". A 9D precisará distinguir IA de sistema (ver §9).

## 3. Princípios

1. **Negar por padrão.** Sem habilitação explícita, a tool não existe para a organização.
2. **Menor privilégio.** A IA só vê e só executa o que (a) existe no catálogo, (b) a organização habilitou e (c) a política deixa para aquela classe de risco. A tool recebe apenas o contexto mínimo.
3. **Capacidade técnica ≠ autorização de negócio.** "O provedor/modelo sabe chamar tools" (`Capabilities.ToolCalling`, 9A) nunca autoriza nada por si. A autorização é decisão do Whatc, tomada fora de `internal/ai`.
4. **O modelo não escolhe o escopo.** Organização, contato, sessão e conta vêm da sessão do servidor, **nunca de argumentos da tool**. Uma tool não pode receber um ID de organização/contato vindo do modelo para decidir o escopo.
5. **Fail-closed.** Se não for possível decidir ou registrar, a tool não executa.
6. **A IA é um ator próprio**, nunca o cliente que conversa nem um usuário humano.

## 4. Camadas e responsabilidades

```
internal/ai (9A)              contrato, adaptadores, RunToolLoop. NÃO sabe de organização,
                              permissão, catálogo ou auditoria.
internal/aitools (novo, 9B)   domínio de governança: catálogo, política, ator, escopo, auditoria,
                              ToolResolver governado. Sem HTTP; usa GORM só via store.
internal/handlers             cola: monta o escopo a partir da sessão, chama o resolver governado
                              dentro de completeAI, expõe a API de habilitação.
```

`internal/ai` muda **o mínimo** (§10, commit 1). Todo o resto novo vive em `internal/aitools` e na cola dos handlers.

### 4.1 Fluxo de uma chamada

```
RunToolLoop
  └─ Definitions()   só as tools EFETIVAMENTE disponíveis para a organização
  └─ Resolve(name)   aceita QUALQUER nome vindo do modelo e devolve sempre uma tool governada:
        ├─ nome disponível       → GovernedTool   (ainda NÃO instancia a tool real)
        └─ qualquer outro caso   → DeniedTool     (desconhecida, não habilitada, chave global
                                                   desligada, risco não permitido)

GovernedTool.Execute(call):
   1. política.Authorize(escopo, tool, risco)       → negado: segue o caminho de DeniedTool
   2. auditoria: grava "requested" (síncrono)       → falhou: NÃO executa (audit_unavailable)
   3. Factory(escopo) instancia a tool real          ← só aqui, depois de 1 e 2
   4. tool real Execute(...)                         (com a ToolCall sem Opaque)
   5. auditoria: atualiza "executed" | "failed"      (ver 4.2 se esta etapa falhar)

DeniedTool.Execute(call):
   grava UMA linha "denied" com o motivo; devolve ao modelo um ToolResult de erro GENÉRICO;
   nunca recebe Factory nem implementação real.
```

### 4.2 Regras de fronteira

- **`Resolve()` não instancia a implementação real.** A `Factory` roda **somente** em `GovernedTool.Execute`, depois de autorização **e** de `requested` gravado. Uma `Factory` com inicialização que tenha efeito colateral ou abra recurso nunca roda antes da decisão de segurança. Teste: `Factory` falsa que falha o teste se chamada em `Resolve`, em tool negada ou quando a gravação de `requested` falha.
- **`Definitions()` expõe só o que está efetivamente disponível.** `Resolve(name)` pode receber qualquer string do modelo, inclusive nomes que nunca foram oferecidos: o resultado é uma `DeniedTool`, nunca "inexistente" cru (para que a tentativa seja registrada).
- **O modelo não distingue as causas.** A resposta da `DeniedTool` é a mesma para `unknown_tool`, `not_enabled`, `global_off`, `confirmation_unavailable` e `loop_limit`, sem texto que revele catálogo, política ou habilitação. O motivo existe **somente** na auditoria.
- **Falha da auditoria depois da execução** (assimetria assumida):
  - falha ao gravar `requested` ⇒ **não executa** (fail-closed);
  - falha ao atualizar o desfecho ⇒ **não desfaz a execução**: a ação já aconteceu. O registro permanece em `requested`, o `ToolResult` segue para o modelo normalmente, e a falha gera `ERROR` operacional no log (com `run_id`, `tool_name`, `organization_id`, sem argumentos/resultado);
  - **a tool nunca é reexecutada** porque a atualização da auditoria falhou. Nenhum retry de execução; no máximo uma segunda tentativa de **gravar o desfecho** (idempotente, mesma linha), sem relançar a tool;
  - uma linha que fica em `requested` sem desfecho significa "tentada, resultado desconhecido" e deve ser tratada como tal por quem investigar (relevante para a 9D, em que escrita com desfecho desconhecido não pode ser assumida como não ocorrida).

## 5. Catálogo de tools por organização

**Definição central (código).** Cada tool é uma `ToolSpec` registrada no código, não no banco:

```go
type ToolSpec struct {
    Name        string               // = ai.ToolDefinition.Name
    Description string
    Parameters  json.RawMessage
    Risk        Risk                 // RiskRead | RiskWrite
    Factory     func(Scope) ai.Tool  // chamada SÓ em GovernedTool.Execute, depois de autorização + "requested" (4.2)
}
```

- O **catálogo** é o conjunto de `ToolSpec` compiladas. Na 9B ele está **vazio** em produção; só testes registram tools falsas. 9C/9D adicionam specs.
- **Disponibilidade efetiva** = tool no catálogo **E** habilitada para a organização **E** chave global ligada **E** política permite a classe de risco.
- **Habilitação por organização** em tabela, **padrão desabilitada** (ausência de linha = desabilitada). Sem chave "mestra" por organização: a habilitação é por tool.
- **Chave global** `ai_tools.enabled` (config, `false` por padrão, booleano estrito como `knowledge.rag_enabled`). Desligada: o fluxo é **idêntico ao de hoje**.

### Política de risco (decisão central da 9B)

| Risco | 9B | Futuro |
|---|---|---|
| `RiskRead` | permitido se habilitada | 9C |
| `RiskWrite` | **sempre negado** com motivo `confirmation_unavailable` | 9D troca por "exige confirmação humana vinculada à ação/argumentos" |

Isso garante que **nenhuma tool de escrita possa executar** enquanto não existir o mecanismo de confirmação, mesmo que alguém registre uma e a habilite. A confirmação em si (solicitação, vínculo com argumentos, expiração, quem confirma) é desenho da 9D.

## 6. Autorização: quem decide o quê

- **Quem habilita/desabilita** uma tool para a organização: usuário **humano** com a nova permissão `ai_tools:write` (leitura: `ai_tools:read`). Concedida **só ao papel de sistema `admin`** por backfill idempotente por organização, igual ao precedente de `knowledge` (um admin que revogar depois não é re-concedido). A mudança é gravada no `audit_logs` existente, com o usuário humano (ele serve aqui).
- **Quem executa** em runtime: não há usuário. A "autoridade" da IA é a política (§5) sobre a habilitação feita por um humano autorizado. A IA **não herda** permissões de ninguém: nem do cliente, nem do admin que habilitou, nem de agente.
- **Escopo da execução** (`Scope`): `{OrganizationID, ContactID, SessionID, WhatsAppAccount}`, montado pelo servidor a partir da sessão do chatbot. É o **único** contexto que a tool recebe, e ela não pode ampliá-lo. Todo acesso a dados numa tool deve filtrar por `Scope.OrganizationID` (regra de revisão de 9C/9D, coberta por teste de isolamento).
- **Argumentos do modelo** são dados não confiáveis: validados pela tool contra o schema; nunca definem organização/contato.

## 7. Ator de IA

```go
type ActorKind string // "human" | "system" | "ai"
type Actor struct {
    Kind ActorKind
    Ref  string // para "ai": a feature de origem, ex. "chatbot_reply" ou "chatbot_flow_node"
}
```

- Toda execução registrada pela 9B tem `Kind="ai"`. O **cliente** aparece como `subject_contact_id` (sobre quem), **nunca** como ator.
- Não se reutiliza `audit.LogAudit` nem `AuditLog` para a IA (exigem usuário humano); não se cria usuário "fantasma" para a IA.
- Humano e sistema existem no tipo para a 9D (confirmação humana) e para não reabrir o modelo, mas **nada os usa na 9B**.

## 8. Auditoria

**Tabela própria**, append-only na prática, `ai_tool_calls` (uma linha por `ToolCall` tentada). `audit_logs` e `ai_usage_logs` **não mudam**.

| Campo | Observação |
|---|---|
| `id`, `organization_id` | org obrigatória e indexada |
| `run_id` | uuid por `RunToolLoop`; agrupa as chamadas de uma resposta |
| `step`, `call_id` | passo do loop e ID da chamada **do provedor** (não confiável como único; unicidade só dentro do `run_id`) |
| `tool_name`, `risk` | |
| `actor_kind`, `actor_ref` | `ai` + feature |
| `subject_contact_id`, `session_id`, `whatsapp_account` | contexto mínimo |
| `status` | `requested`, `denied`, `executed`, `failed` |
| `denial_reason` | enum fechado: `unknown_tool`, `not_enabled`, `global_off`, `confirmation_unavailable`, `loop_limit`, `audit_unavailable` |
| `args_keys`, `args_bytes`, `args_hmac_sha256` | **só chaves de topo, tamanho e HMAC dos argumentos**, nunca os valores (podem ter CPF, telefone, endereço); ver "HMAC dos argumentos" abaixo |
| `result_bytes`, `result_truncated`, `result_is_error` | **nunca o conteúdo** do resultado |
| `error_kind` | `tool_error`, `panic`, `timeout`; sem texto de erro interno |
| `requested_at`, `finished_at`, `duration_ms` | |

- **HMAC dos argumentos (não SHA-256 simples).** Um SHA-256 de um CPF, telefone ou ID curto pode ser quebrado por dicionário offline e comparado ao campo. Por isso: `args_hmac_sha256 = HMAC-SHA256(chave_derivada, argumentos_canônicos)`.
  - **Chave:** derivada do segredo do servidor que já existe (`app.encryption_key`), com rótulo de propósito: `chave_derivada = HMAC-SHA256(encryption_key, "whatc/ai_tools/args/v1")`. Nenhum segredo novo. Se o segredo não estiver configurado, o campo fica **nulo** (nunca cai para hash sem chave).
  - **Serialização canônica antes do HMAC:** os argumentos são decodificados como JSON com números preservados em texto (`json.Number`, sem normalizar float), chaves de objetos ordenadas recursivamente, sem espaços, UTF-8 e sem escape de HTML. Mesma informação, mesma sequência de bytes, independente de ordem de chaves ou espaçamento do JSON do modelo.
  - **Para que serve:** detectar repetição/igualdade entre chamadas (mesmos argumentos), não recuperar o conteúdo. Rotação da chave quebra a comparabilidade com linhas antigas, e isso é aceito (a versão `v1` no rótulo permite evoluir).
  - `args_keys` = só nomes das chaves de topo, nunca valores, e limitados em número e tamanho.
- **Escrita síncrona e fail-closed:** `requested` é gravada **antes** de `Execute`; se a gravação falhar, a tool não executa (`audit_unavailable`). Em seguida a mesma linha é atualizada com o desfecho; se **essa** atualização falhar, a execução **não** é desfeita nem repetida (ver 4.2). Isso difere de `LogAudit` (assíncrono) de propósito: auditoria que pode se perder não pode autorizar ação.
- **`Opaque` nunca é persistido nem entregue à tool.** Dois níveis: (a) o loop passa a limpar `Opaque` da `ToolCall` antes de `Execute` (commit 1, pequena mudança em `internal/ai` com teste); (b) a auditoria só lê `ID`, `Name` e `Arguments` (para chaves/tamanho/HMAC).
- **Limites do loop:** quando o `RunToolLoop` aborta uma rodada por `MaxCallsStep`/`MaxToolCalls`, nada executou; a camada de integração grava essas chamadas (da última resposta do `LoopResult`) como `denied/loop_limit`, para o registro de tentativas ficar completo. Tool call **malformada** pelo provedor (`KindInvalidToolCall`) não gera `ToolCall` utilizável: fica no `AIUsageLog` (`error_kind=invalid_tool_call`), como hoje.
- **Retenção/consulta:** fora da 9B (sem job de limpeza, sem tela). Consulta por SQL/teste; a visualização é decisão da 9C (pergunta 5).

## 9. Integração com a arquitetura existente

**`internal/ai`** (único toque, commit 1): limpar `Opaque` antes de `Execute`; `LoopResult` passa a informar a duração de cada resposta do provedor (campo paralelo a `Responses`), para o `completeAI` registrar uma linha de `AIUsageLog` **por chamada ao provedor** com latência real. Sem mudança de contrato nem de comportamento do loop.

**`internal/aitools`** (novo): `Catalog`, `Policy` (função pura e testável: dado global, habilitação, risco → permitido/negado+motivo), `Actor`, `Scope`, `AuditStore`, `Resolver` (implementa `ai.ToolResolver`, §4.1).

**`internal/handlers`** (cola, commit final):
- `aiCallMeta` ganha um campo opcional com o escopo/execução de tools. `generateAIResponse` o preenche **somente se** a chave global estiver ligada **e** a organização tiver ≥1 tool habilitada **e** o catálogo tiver a tool.
- `completeAI` continua sendo a **única porta**: se houver tools no meta, chama `RunToolLoop` com o resolver governado e grava uma linha de `AIUsageLog` por chamada ao provedor; se não houver, o caminho é **exatamente o atual**. Não existe `completeAIWithTools`.
- Falha do loop (erro de provedor, tool call malformada, `ErrToolLoopLimit`, timeout) volta como erro de IA e cai no **mesmo fallback de hoje**; nada novo vai ao cliente.
- API de habilitação: `GET /api/ai-tools` (catálogo + estado da organização, `ai_tools:read`) e `PUT /api/ai-tools/{name}` (habilitar/desabilitar, `ai_tools:write`, grava `audit_logs` com o usuário humano). Sem frontend na 9B.

**Models/config/banco:**
- Tabelas novas: `ai_tool_settings` (`organization_id`, `tool_name`, `enabled` NOT NULL DEFAULT false, `updated_by_id`, timestamps; único `(organization_id, tool_name)`) e `ai_tool_calls` (§8; índices `(organization_id, requested_at desc)`, `(organization_id, tool_name)`, `run_id`).
- Config: `[ai_tools] enabled = false` em `config.example.toml`.
- Permissão `ai_tools:{read,write}` semeada + `BackfillAIToolsPermissions` (só `admin`), chamada onde `BackfillKnowledgePermissions` é chamada.
- `GetMigrationModels()` e a lista de modelos do `testutil` ganham os dois modelos.
- Nenhuma coluna alterada em tabela existente (`chatbot_settings`, `ai_usage_logs`, `audit_logs`, `agent_transfers` intactas).

**Para a 9D (registrado, não feito aqui):** hoje `AgentTransfer.TransferredByUserID == nil` significa "sistema". Uma transferência feita pela IA não pode se passar por "sistema" nem por usuário; a 9D deve acrescentar um valor explícito de origem (ex.: `TransferSource` "ai") ou campo equivalente, e a camada de serviço extraída das funções de transferência deve receber o `Actor`.

## 10. Plano de commits (locais, sem push até revisão)

1. `feat(ai): tools never see Opaque; LoopResult reports per-call duration` (+ testes)
2. `feat(models): ai_tool_settings and ai_tool_calls, ai_tools permission and backfill` (+ migração via `GetMigrationModels`, testutil, `config` `[ai_tools]`)
3. `feat(aitools): catalog, policy, actor, scope and audit store` (+ testes, sem handlers)
4. `feat(aitools): governed ToolResolver` (+ testes de negação, fail-closed, isolamento)
5. `feat(handlers): ai-tools settings API (list, enable/disable)` (+ testes de permissão e isolamento)
6. `feat(handlers): completeAI runs the governed loop when tools are enabled (dormant)` (+ testes de regressão: com a chave global desligada ou sem tool habilitada, o fluxo e o `AIUsageLog` são idênticos aos de hoje)

O commit 6 é separado de propósito: pode ser adiado para a 9C sem perder o resto; a decisão atual é incluí-lo, dormente (§15).

## 11. Retrocompatibilidade

- Aditivo: duas tabelas novas, uma permissão nova, uma chave de config nova. Nada existente é alterado ou preenchido.
- Padrão **desligado em três níveis** (chave global, habilitação por tool, catálogo vazio): nenhuma organização muda de comportamento sem ação explícita de um admin humano **e** sem tools existirem.
- Migração: `AutoMigrate` via `-migrate` (único mecanismo do projeto) + backfill de permissão idempotente. Reversão: nenhuma tabela é lida pelo código existente; remover as tabelas não afeta o chatbot atual.

## 12. Critérios de aceite e testes

**Política e autorização**
- Chave global desligada ⇒ nenhuma tool oferecida nem executada; fluxo idêntico ao atual.
- Organização sem tool habilitada ⇒ `Definitions()` vazio; chamada forçada por nome é negada (`not_enabled`) e auditada.
- Tool de risco `write` habilitada ⇒ **negada** (`confirmation_unavailable`), não executa, auditada.
- Tool desconhecida ⇒ `unknown_tool`, auditada, resposta genérica ao modelo (sem revelar o motivo).
- **Negação nunca chama `Execute`** (tool falsa que falha o teste se executada).
- Falha ao gravar `requested` ⇒ não executa (`audit_unavailable`).

**Ator e escopo**
- Toda linha tem `actor_kind=ai` e `actor_ref` da feature; o cliente só em `subject_contact_id`.
- O escopo vem da sessão; argumentos do modelo com IDs de outra organização/contato não mudam o escopo (teste com tool falsa que tenta ler `org_id` dos argumentos).

**Auditoria**
- Ciclo completo: `requested` → `executed`/`failed`, com `duration_ms`, `result_bytes`, `result_truncated`, `result_is_error`.
- Argumentos: só chaves de topo, tamanho e HMAC; **nenhum valor** em nenhuma coluna (teste procura um valor de CPF/telefone sentinela em todas as colunas da linha).
- HMAC: mesma informação com ordem de chaves/espaços diferentes ⇒ mesmo HMAC; valor diferente ⇒ HMAC diferente; números preservados (`1.0` ≠ `1`); chave derivada diferente ⇒ HMAC diferente; sem `encryption_key` ⇒ campo nulo (nunca SHA-256 simples); o HMAC de um valor de baixa entropia **não** é igual ao SHA-256 dele (prova que não é hash sem chave).
- `Factory` nunca roda em `Resolve`, em tool negada nem quando a gravação de `requested` falha; roda uma vez, depois de autorização + `requested`.
- Falha ao atualizar o desfecho: a tool não é reexecutada, a linha fica em `requested`, o `ToolResult` segue para o modelo e há log `ERROR` sem argumentos/resultado; no máximo uma nova tentativa de gravar o desfecho, nunca de executar.
- `DeniedTool`: resposta ao modelo idêntica para `unknown_tool`, `not_enabled`, `global_off` e `confirmation_unavailable`; nunca recebe `Factory`; `Definitions()` não lista tools indisponíveis, mas `Resolve` aceita qualquer nome sem panic e sem executar nada.
- `Opaque` ausente: não chega à tool (teste com tool falsa que inspeciona a `ToolCall`) e não aparece em nenhuma linha.
- Rodada abortada por limite ⇒ `denied/loop_limit` para cada chamada, nada executado.
- Panic da tool ⇒ `failed/panic`; o processo continua.

**Isolamento entre organizações**
- Habilitação em A não vale para B; linhas de auditoria de A não aparecem em consultas de B; `GET /api/ai-tools` só mostra o estado da própria organização; `PUT` em B não altera A.
- Permissão: usuário sem `ai_tools:write` não habilita (403); sem `ai_tools:read` não lista; backfill concede só ao `admin` e é idempotente (revogação posterior não é desfeita).

**Regressão**
- Sem tools: `generateAIResponse`/`completeAI` e o `AIUsageLog` idênticos aos atuais (prompt, número de linhas de uso, colunas).
- Com tools falsas habilitadas (testes): uma linha de `AIUsageLog` por chamada ao provedor, com latência por passo.
- `go build`, `go vet`, suíte completa com apenas as falhas conhecidas.

## 13. Fronteira 9B → 9C → 9D

| | 9B (esta) | 9C | 9D |
|---|---|---|---|
| Catálogo | mecanismo, **vazio** | tools read-only reais (ex.: consultas do próprio contato/organização) | tool de transferência |
| Política | read permitido, write negado | afina escopo por tool | write exige confirmação humana vinculada a ação+argumentos |
| Ator | `ai` | idem | `human` (quem confirma) + `ai` (quem propõe) |
| Auditoria | tentativa, decisão, desfecho | possível consulta/tela | + confirmação, expiração, vínculo com a ação de domínio |
| Gemini/Groq reais | não exigido | **teste real antes de habilitar** | idem |

## 14. Fora da 9B

Qualquer tool real (inclusive Knowledge como tool, X2, ocorrência, oportunidade, transferência), confirmação humana, UI/telas, tela de auditoria, retenção/limpeza, Agent Assistant, ferramentas administrativas, criação/alteração de fluxos por IA, catálogo por conta do WhatsApp (só por organização), limites por organização (usam-se os padrões da 9A).

## 15. Decisões fechadas

| Questão | Decisão |
|---|---|
| `completeAI` na 9B | **Sim, dormente** (catálogo de produção vazio ⇒ comportamento idêntico, coberto por teste) |
| Frontend na 9B | Não (só API) |
| Auditoria síncrona e fail-closed também para leitura | Sim, uma regra só |
| Argumentos na auditoria | Chaves de topo + tamanho + **HMAC-SHA256** canônico; sem valores. Resumo seguro por tool só se a 9C/9D precisar |
| API/tela para ler `ai_tool_calls` | Não na 9B |
| Features que podem usar tools | `chatbot_reply` e `chatbot_flow_node` |
| Vincular `ai_tool_calls` a `AIUsageLog` | Não; tabelas separadas, correlação por `session_id` + tempo |

Regra arquitetural para 9C e 9D: **o modelo fornece intenção e parâmetros; o servidor fornece identidade e escopo.** Organização, contato, sessão e conta do WhatsApp nunca entram nos argumentos de uma tool.
