# Fase 9C — Primeiras tools de leitura da IA (nota de design)

Status: **design aprovado (revisão 2); nada foi implementado.** Branch: `feature/fase-9c-ai-read-tools`, criada de `development` em `997ca57` (9B mergeada, PR #84). A implementação começa só depois da conferência do commit desta nota.

### Mudanças da revisão 2
1. Os 4 pontos da seção 14 foram **aprovados** como propostos (seção 14).
2. Cenário 5 da validação reescrito: observar o comportamento real do provedor diante de uma resposta interrompida, em vez de impor `KindInvalidToolCall` como contrato (seção 9).
3. Precisão sobre a fronteira de segurança do `ReadDB`: ela é a transação `READ ONLY` do PostgreSQL, não o encapsulamento em Go (seção 5.1).
4. Regra explícita de escopo de `get_my_occurrences`, sem modo administrativo nem fallback (seção 6.2).

## 1. Pergunta que a 9C responde

> Como o chatbot passa a consultar dados reais, só de leitura e sempre dentro do escopo do cliente que está conversando, com a governança da 9B e com cada chamada registrada?

A 9C entrega **duas tools de leitura**, a imposição técnica de leitura, o registro de `args_too_large`, a API de consulta da auditoria e o roteiro de validação com provedores reais. Não entra escrita, transferência, confirmação humana, X2, oportunidades, mudança no Knowledge/RAG nem frontend.

## 2. Decisões já fechadas

| # | Decisão |
|---|---|
| 1 | `MaxArgsBytes` resolvido **antes** do `Resolve()`; `internal/ai` mantém o limite global (8 KiB); a governança registra `denied/args_too_large` por interface opcional, sem acoplar `ai` a `aitools` e sem repetir a regra em cada tool |
| 2 | Primeiras tools: `get_business_hours` e `get_my_occurrences` |
| 3 | Leitura **imposta tecnicamente** (não convenção): toda read tool executa por dependência read-only |
| 4 | `GET /api/ai-tools/calls` (somente leitura, paginado, filtros, `ai_tools:read`), só metadados, **sem tela** |
| 5 | Validação real antes da ativação, na ordem Groq, Gemini 3, OpenAI, Anthropic; o que não puder ser testado fica **"não validado"**, nunca "aprovado por inferência" |
| 6 | Somente leitura; sem escrita, transferência, confirmação humana, Knowledge/RAG, X2 ou oportunidades |

## 3. Estado atual (auditado)

- `internal/ai/toolloop.go:177`: `runOne` confere `len(c.Arguments) > MaxArgsBytes` **antes** de `Resolve()`. Uma chamada grande demais nunca chega ao `aitools.Resolver` e não gera linha em `ai_tool_calls` (única `ToolCall` sem registro).
- `aitools.ToolSpec.Factory` é `func(Scope) ai.Tool`: a tool não recebe banco. A 9C precisa de dependências injetadas.
- `Scope` já traz `OrganizationID`, `ContactID`, `SessionID` e `WhatsAppAccount`, montados pelo servidor a partir da sessão.
- Horário de atendimento: `ChatbotSettings.BusinessHours` (`Enabled`, `Hours` como `[{day 0-6, enabled, start_time, end_time}]`, domingo = 0, comparação de texto `HH:MM`), linha padrão da organização (`whats_app_account = ''`). A regra "dentro do horário" vive em `handlers.isWithinBusinessHours`, usa o relógio do servidor e **não há fuso por organização**.
- `Occurrence` tem `OrganizationID`, `ContactID`, `ProtocolNumber` (único por organização), `Title`, `StageID`, `OpenedAt`, `ClosedAt`. `OccurrenceStage` tem `Name` e `IsClosing`.
- Postgres/GORM: verifiquei neste ambiente que `db.Begin(&sql.TxOptions{ReadOnly: true})` abre uma transação `READ ONLY` de verdade: `INSERT` e `UPDATE` falham com `SQLSTATE 25006 (cannot execute INSERT in a read-only transaction)` e `SET LOCAL statement_timeout` funciona dentro dela.

## 4. `args_too_large`: registrar antes de executar

### 4.1 Contrato em `internal/ai` (genérico, sem conhecer `aitools`)

```go
// RejectReason diz por que o loop recusou uma chamada antes de executá-la.
type RejectReason string
const RejectArgsTooLarge RejectReason = "args_too_large"

// ToolRejecter pode ser implementada por uma Tool devolvida por Resolve. O loop a chama no
// lugar de Execute para uma chamada que ele mesmo recusa, para que quem governa as tools
// possa registrar a recusa. Quem não implementa recebe o resultado de erro genérico de hoje.
type ToolRejecter interface {
    Reject(ctx context.Context, call ToolCall, reason RejectReason) ToolResult
}
```

`runOne` passa a: (1) `Resolve(name)`; (2) se os argumentos passam de `MaxArgsBytes`, chama `Reject` quando a tool implementa `ToolRejecter` (senão devolve o erro genérico, como hoje); (3) só então segue para `Execute`. O limite continua global e único (8 KiB, `Limits.MaxArgsBytes`); nenhuma tool o repete. `Reject` **nunca executa** a tool nem constrói a `Factory`.

### 4.2 Implementação em `internal/aitools`

- `deniedTool` e `governedTool` implementam `ToolRejecter`.
  - `deniedTool.Reject` registra a **própria** razão de negação (`unknown_tool`, `not_enabled`, `global_off`, `confirmation_unavailable`): a política vem antes do tamanho.
  - `governedTool.Reject` registra `denied / args_too_large`. Em ambos o modelo ouve a **mesma resposta genérica** de qualquer negação.
- Nova constante `models.AIToolDenyArgsTooLarge = "args_too_large"` (coluna `denial_reason` já é texto; **sem migração**).
- Para `args_too_large` a linha guarda **só `args_bytes`**: nem HMAC nem `args_keys` (não se calcula nada sobre um documento grande, que pode ter até 4 MiB vindos do provedor).
- Resultado: passa a valer "uma linha em `ai_tool_calls` para cada `ToolCall` que chega ao loop e pede execução".

## 5. Leitura imposta tecnicamente

### 5.1 O que impede escrita

Toda read tool recebe apenas um **`ReadDB`**, nunca um `*gorm.DB`:

```go
// ReadDB só sabe abrir uma visão de leitura. Não expõe o *gorm.DB.
type ReadDB struct{ db *gorm.DB }

func (r ReadDB) View(ctx context.Context, timeout time.Duration, fn func(tx *gorm.DB) error) error
```

`View`: `tx := db.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true})`; `SET LOCAL statement_timeout = <timeout>`; roda `fn(tx)`; **sempre `Rollback`** (nunca `Commit`, nada a persistir). O `tx` é o único handle que a tool vê e só vive dentro de `fn`.

- **A fronteira de segurança é o PostgreSQL, não o encapsulamento em Go.** O `ReadDB` não expõe o handle de banco à `Factory` nem à tool; a tool só recebe um `*gorm.DB` temporário **dentro do callback `View`**, cuja transação é `READ ONLY` do PostgreSQL, com `statement_timeout` local e `Rollback` obrigatório. Qualquer `INSERT/UPDATE/DELETE`, DDL ou `nextval` nessa transação falha com `25006`, mesmo que uma tool tente. Não depende de a tool "se comportar".
- **Operações permitidas**: `SELECT` (consultas e joins), `SHOW`. Nada mais.
- **Tempo**: `timeout` por tool (padrão 3 s) vira `statement_timeout` local e também limita o contexto; estourar vira `failed/timeout` na auditoria.
- O `Deps` entregue à `Factory` é `Deps{Read ReadDB}`; assinatura nova: `Factory func(Scope, Deps) ai.Tool`. Uma tool de escrita futura (9D) receberá um tipo diferente de dependência, nunca o `ReadDB`.
- Mesmo padrão do projeto? O projeto não tem camada de repositório; usar `Begin(TxOptions{ReadOnly})` é o mínimo que dá a garantia, sem abstração nova além do `ReadDB` de ~30 linhas.

### 5.2 Como `Deps` chega à tool

`ResolverConfig` ganha `Deps`. `governedTools` (em `ai_runtime.go`) o preenche com `ReadDB{db: a.DB}`. A `Factory` continua sendo chamada **somente** em `GovernedTool.Execute`, depois de autorização e `requested` (regra da 9B).

## 6. Contratos das tools

Regras comuns:
- **Argumentos estritos**: helper `aitools.DecodeArgs(call, &dst)` com `DisallowUnknownFields`, tipos, intervalos e tamanhos. Campo desconhecido ⇒ erro "invalid arguments" devolvido ao modelo (a chamada fica auditada com o nome do campo em `args_keys`; tentar `contact_id` deixa esse rastro).
- **Escopo vem do servidor**: `Scope.OrganizationID` (+ `Scope.ContactID` quando a tool é do contato) é **sempre** imposto. O modelo nunca fornece `organization_id`, `contact_id`, `user_id` ou equivalente; tais campos não existem no schema e, se enviados, são rejeitados.
- **Schemas portáveis**: só palavras-chave dentro do subconjunto aceito pelo adaptador Gemini (`type`, `properties`, `required`, `enum`, `minimum`, `maximum`, `description`). A rigidez ("sem campos extras") é do `DecodeArgs`, não de `additionalProperties`.
- **Resultado é dado não confiável para o modelo**: JSON de campos fixos e whitelisted; texto livre escrito por terceiros é excluído ou limitado e saneado; a descrição de cada tool diz que os campos de texto são dados, não instruções. Tamanho do resultado muito abaixo de `MaxResultBytes`.
- Risco `read`; opt-in por organização como qualquer tool da 9B.

### 6.1 `get_business_hours`

- **Argumentos**: nenhum (`{"type":"object","properties":{}}`).
- **Fonte**: linha padrão de `ChatbotSettings` da organização do `Scope` (`whats_app_account = ''`).
- **Resultado**: `{"configured": bool, "open_now": bool|null, "server_time": "HH:MM", "days": [{"day":"sunday".."saturday","open":bool,"start":"HH:MM","end":"HH:MM"}]}`. Sem horário configurado ou desligado: `{"configured": false}`.
- **Sem texto livre**: não inclui `out_of_hours_message`.
- **`open_now` sem duplicar a regra**: extrair `isWithinBusinessHours` para um pacote pequeno e puro (`internal/businesshours`) usado por `handlers` e pela tool, com testes de comportamento idêntico ao atual. **Limitação registrada**: a hora é a do servidor, não há fuso por organização (assim como já é para o chatbot hoje); a tool devolve `server_time` para o modelo não inferir.
- Não depende de `ContactID`; é leitura organizacional.

### 6.2 `get_my_occurrences`

- **Argumentos** (todos opcionais): `protocol` (string, `^[A-Za-z0-9._/-]{1,20}$` após `trim`), `status` (`"open" | "closed" | "all"`, padrão `"all"`), `limit` (inteiro 1 a 5, padrão 5). Sem paginação.
- **Consulta (uma só, em `View`)**: `occurrences` com `organization_id = Scope.OrganizationID AND contact_id = Scope.ContactID AND deleted_at IS NULL`, `LEFT JOIN occurrence_stages` (nome do estágio mesmo que o estágio tenha sido removido depois), filtro opcional por `protocol_number` e por status, `ORDER BY opened_at DESC LIMIT n`. O `protocol` do modelo é só um filtro **adicional** dentro do escopo, nunca substitui o contato.
- **Contrato de segurança**:
  - Ocorrência de **outro contato** com o protocolo informado ⇒ resultado **idêntico** a "não existe": `{"occurrences": []}`, mesma forma, mesmo `Content` formato, sem mensagem distinta, sem erro, sem diferença de campos. Como o filtro de contato está na mesma consulta, a tool nunca sequer lê a ocorrência alheia.
  - O mesmo vale para outra organização.
- **Regra fixa de escopo**: `OrganizationID = Scope.OrganizationID` e `ContactID = Scope.ContactID`, impostos pelo servidor em toda consulta. `protocol` (e `status`, `limit`) são **apenas filtros adicionais** dentro desse escopo. Não existe modo administrativo, parâmetro de escape nem fallback em que o modelo possa consultar outra ocorrência; erro na leitura do escopo resulta em erro, nunca em consulta sem filtro.
  - Nada de diferença observável por mensagem; teste compara bytes das respostas "protocolo alheio" e "protocolo inexistente".
- **Resultado por item**: `protocol`, `title` (≤ 100 caracteres, caracteres de controle e quebras de linha removidos), `stage` (nome), `status` (`"open"` ou `"closed"`: fechado se `stage.is_closing` ou `closed_at` preenchido), `opened_at`, `closed_at` (datas ISO). **Excluídos**: descrição, notas e eventos, responsável e equipe, SLA, prioridade interna, categoria, motivo, documentos, dados de venda, IDs internos.
- **Ordem de checagem na tool**: argumentos ⇒ `View` ⇒ montar resultado; qualquer erro interno vira `failed` genérico na 9B, sem texto interno ao modelo.

### 6.3 Catálogo e ativação

A partir da 9C, `DefaultCatalog()` **deixa de ser vazio**: contém as duas specs acima. Continua sem efeito em produção por **quatro** condições, todas desligadas por padrão: `ai_tools.enabled`, opt-in da organização por tool, e (proposta adicional abaixo) a lista de provedores validados. O teste "catálogo de produção vazio" da 9B é substituído por "catálogo de produção = exatamente estas duas tools, ambas `read`".

## 7. Provedores validados como condição técnica (proposta adicional, a aprovar)

Para que "não validado" não seja só uma frase: `[ai_tools] providers = []` (lista, vazia por padrão, ex.: `["groq"]`). `governedTools` só usa tools se o provedor da organização estiver na lista. Provedor ausente ⇒ caminho normal, idêntico ao de hoje. A lista só ganha um provedor depois que o roteiro da seção 9 o aprovou. Custo: uma chave de config e uma checagem em `governedTools`.

## 8. API de consulta da auditoria

`GET /api/ai-tools/calls`, permissão `ai_tools:read`, organização sempre a da autenticação.

- **Filtros**: `tool`, `status` (`requested|denied|executed|failed`), `denial_reason`, `contact_id` (= `subject_contact_id`), `run_id`, `from`/`to`; `page`/`limit` (máx. 100); ordenação `requested_at DESC`.
- **Resposta** por struct explícita (não o modelo GORM, para que coluna nova nunca vaze por padrão): `id`, `run_id`, `step`, `tool_name`, `risk`, `actor_kind`, `actor_ref`, `subject_contact_id`, `session_id`, `whatsapp_account`, `status`, `denial_reason`, `args_keys`, `args_bytes`, `result_bytes`, `result_truncated`, `result_is_error`, `error_kind`, `requested_at`, `finished_at`, `duration_ms`.
- **Nunca expõe**: valores de argumentos, conteúdo de resultado, `Opaque`, o HMAC (`args_hmac_sha256` fica só no banco, para perícia), nem o `call_id` do provedor.
- Somente leitura: não há `POST/PUT/DELETE` sobre `ai_tool_calls`. Linha em `requested` sem desfecho continua significando "tentada, resultado desconhecido" (documentado no campo `status`).
- Sem frontend.

## 9. Validação com provedores reais (gate de ativação)

**Artefato**: teste com build tag `realprovider` (`go test -tags realprovider ./internal/ai -run Real -v`), **fora da suíte normal e do CI**. Chaves e modelos só por variável de ambiente (`WHATC_REAL_{GROQ,GOOGLE,OPENAI,ANTHROPIC}_KEY` e `_MODEL`), nunca em arquivo, log ou commit; sem a variável o provedor é pulado e marcado **não validado**. Custo limitado (`max_tokens` baixo, poucas chamadas por provedor).

**Ordem**: Groq, Gemini 3, OpenAI, Anthropic.

**Cenários por provedor** (cada um registra *validado / divergente / não validado*, com a resposta real observada, sem segredos):
1. Uma tool call simples e o resultado de volta, resposta final em texto.
2. Várias chamadas na mesma resposta (mesma tool duas vezes, e duas tools) e a ordem dos resultados.
3. Os **schemas reais das duas tools da 9C** aceitos (`enum`, `integer` com `minimum/maximum`, objeto vazio).
4. `ToolChoice none` com tools declaradas.
5. **Resposta interrompida durante a geração de uma `ToolCall`**: observar o comportamento real de cada provedor (corte por `max_tokens`, `finish_reason`/`stop_reason`/`finishReason`, argumentos incompletos) e verificar se o adaptador classifica o caso corretamente. Se houver uma `ToolCall` incompleta ou malformada, deve resultar em `KindInvalidToolCall`; se o provedor der outro sinal de truncamento, **registrar a divergência** e avaliar o mapeamento específico daquele adaptador. Isto não é um contrato artificial do `internal/ai`: o resultado observado é o dado.
6. **Groq**: formato do resultado (`name` aceito/exigido), formato real do erro `failed_generation` (objeto ou texto) e o mapeamento para `KindInvalidToolCall`.
7. **Gemini 3**: `thoughtSignature` (devolver com e sem a assinatura e observar a diferença), `functionResponse` sem `id`, `id` quando o provedor o envia, e se `additionalProperties`/`default` são mesmo rejeitados (para confirmar ou relaxar `googleAllowed`).

**Saída**: tabela provedor × cenário num documento de resultados (`docs/superpowers/specs/…-fase-9c-provider-validation.md`) com data, modelo usado e veredito. Divergência encontrada ⇒ correção do adaptador em commit próprio **antes** de incluir o provedor em `ai_tools.providers`. Provedor sem chave disponível ⇒ **não validado**, fora da lista.

## 10. Plano de commits (locais, sem push até revisão)

1. `feat(ai): refuse oversized arguments after Resolve and let governed tools record it` (`ToolRejecter`, nova ordem em `runOne`, testes)
2. `feat(aitools): args_too_large denial` (constante, `Reject` em `governedTool`/`deniedTool`, linha só com `args_bytes`, testes)
3. `feat(aitools): read-only dependencies for tools` (`ReadDB`, `Deps`, nova assinatura da `Factory`, `DecodeArgs`, testes de escrita rejeitada e timeout)
4. `refactor(businesshours): extract the in-hours rule` (comportamento idêntico, testes de equivalência) e `feat(aitools): get_business_hours`
5. `feat(aitools): get_my_occurrences` (+ catálogo de produção com as duas tools)
6. `feat(handlers): wire read-only deps and the provider allowlist` (`[ai_tools] providers`, `governedTools`)
7. `feat(handlers): GET /api/ai-tools/calls`
8. `test(ai): real-provider validation harness (build tag realprovider)` e, depois de executado, o documento de resultados

## 11. Migração e compatibilidade

- **Sem tabelas ou colunas novas.** Nova constante de motivo de negação (texto) e uma chave de config (`ai_tools.providers`, padrão vazio).
- Verificar na implementação se `occurrences(organization_id, contact_id)` é bem servido pelos índices atuais (há índices em `contact_id` e único em `(organization_id, protocol_number)`); criar índice só se a medição pedir.
- Comportamento do chatbot inalterado até que, nesta ordem: o provedor seja validado e entre em `ai_tools.providers`, `ai_tools.enabled` seja ligado e um administrador habilite a tool para a organização.
- Refatoração `internal/businesshours` não muda o comportamento do horário no chatbot (testes de equivalência).

## 12. Critérios de aceite e testes

**`args_too_large`**
- Argumentos acima de `MaxArgsBytes`: a tool real nunca é construída nem executada; uma linha `denied/args_too_large` com só `args_bytes` (sem HMAC, sem chaves); o modelo ouve a resposta genérica; para tool desconhecida ou não habilitada o motivo registrado é o da política.
- Tool que não implementa `ToolRejecter` mantém o comportamento atual (erro genérico).
- Todo `ToolCall` que chega ao loop e pede execução deixa exatamente uma linha em `ai_tool_calls` (teste enumerando: permitida, negada, desconhecida, escrita, grande demais, rodada acima do limite).

**Leitura imposta**
- Tentar `INSERT/UPDATE/DELETE`/DDL pelo `tx` do `ReadDB` falha com `25006`; o `ReadDB` não expõe `*gorm.DB`; `View` sempre dá `Rollback`; o `statement_timeout` local interrompe consulta lenta e a auditoria registra `failed/timeout`; o handle não vale fora de `fn`.

**`get_business_hours`**
- Sem configuração ou desligado ⇒ `configured:false`; `open_now` igual ao do chatbot para o mesmo horário (testes de equivalência com relógio fixo); sem `out_of_hours_message`; organização B não vê o horário da A; argumentos extras rejeitados.

**`get_my_occurrences`**
- Retorna só ocorrências de `Scope.OrganizationID + Scope.ContactID`; campos exatamente os listados; `title` truncado e sem controles; ordem e `limit` respeitados; filtros `status` e `protocol` funcionam **dentro** do escopo.
- Protocolo de **outro contato** da mesma organização, de **outra organização** e **inexistente** produzem respostas **byte a byte idênticas**.
- Campos `organization_id`, `contact_id`, `user_id` ou qualquer desconhecido nos argumentos ⇒ erro de argumentos, nada consultado, `args_keys` na auditoria mostra o nome do campo.
- Ocorrência removida (soft delete) não aparece; estágio removido ainda mostra o nome.
- Texto de ocorrência com instruções ("ignore as regras…") sai como dado, truncado e saneado.

**Governança (continua valendo)**
- Provedor fora de `ai_tools.providers` ⇒ caminho normal, sem tools; chave global desligada ⇒ idem; sem opt-in ⇒ idem. Nenhuma write tool no catálogo. `Factory` só roda após autorização + `requested`.

**API de auditoria**
- Permissões (`ai_tools:read`; sem a permissão, 403); isolamento entre organizações; filtros e paginação (limite máximo); a resposta **não contém** `args_hmac_sha256`, `call_id`, valores, resultado nem `Opaque` (teste de sentinela sobre o JSON inteiro); não existe rota de escrita.

**Provedores reais**
- Documento de resultados preenchido; nenhum provedor entra em `ai_tools.providers` sem veredito "validado"; ausência de chave ⇒ "não validado".

**Regressão**
- `go build`, `go vet`, suíte completa com apenas as 3 falhas conhecidas; chatbot sem tools idêntico (incluindo payload byte a byte da 9A e as linhas de `AIUsageLog`).

## 13. Fora da 9C

Escrita de qualquer tipo, transferência, confirmação humana, Knowledge como tool ou mudança no RAG, X2, oportunidades comerciais, tela de auditoria ou de habilitação, fuso horário por organização, retenção/limpeza da auditoria, paginação de ocorrências, tools por conta do WhatsApp.

## 14. Decisões aprovadas

1. **`ai_tools.providers`: APROVADO.** `providers = []` por padrão; o provedor só entra na lista depois de validação real bem-sucedida (seção 7).
2. **`title` da ocorrência: APROVADO.** Até 100 caracteres, caracteres de controle e quebras removidos; nenhum outro texto livre (sem descrição nem notas); tratado explicitamente como dado não confiável, nunca como instrução.
3. **Relógio do servidor: APROVADO.** Mantém o comportamento atual do chatbot; a limitação é documentada; sem fuso por organização nesta fase.
4. **`ToolRejecter`: APROVADO.** Interface genérica em `internal/ai`, que não importa `aitools`; o limite de 8 KiB continua centralizado em `internal/ai`; `MaxArgsBytes` não é duplicado nas tools.
