# Fase 9A — Contrato de Tools / Function Calling (nota de design)

Status: **revisão 2 (incorpora os 6 ajustes da revisão do usuário). Nada foi implementado; implementação só após aprovação desta versão.** Branch: `feature/fase-9-ia-actions` (a partir de `development` em `0c9760a`).

### Mudanças da revisão 2
1. `Opaque` permanece, definido como **estado de transporte** (§3).
2. Google: preserva `id` quando o provedor o devolve; sintetiza só quando ausente (§5.3).
3. `ToolChoice` só `auto`/`none` por **escolha deliberada de escopo**, não por falta de portabilidade (§3).
4. Groq: sem `sendToolName`; o serializador distingue `message.name` de `tool-result.name` (§5.4). `ToolCalling=true` no Groq se os testes do contrato passarem.
5. **`completeAIWithTools` removido.** A 9A entrega `RunToolLoop`; integração com `completeAI` fica para 9B/9C (§7).
6. **Sem retry automático** de tool call malformada no `RunToolLoop` (§6).
Aprovados sem mudança: defaults do loop, execução serial, sem `tool_steps`, regressão byte a byte.

## 1. Pergunta que a 9A responde

> Como o Whatc representa, solicita, executa e recebe resultados de Tools de forma independente do provedor?

A 9A é **infraestrutura**. Nenhuma ferramenta de negócio entra, nenhum dado é alterado, nenhum comportamento do chatbot muda.

### Decisões já fechadas (revisão do #82)
| # | Decisão |
|---|---|
| 1 | Divisão 9A contrato · 9B segurança/autorização/auditoria · 9C ferramentas read-only · 9D primeira escrita + confirmação |
| 2 | Primeira escrita (9D): transferência de conversa para atendente/equipe, extraindo uma camada de serviço que preserva testes e comportamento de `createTransferToQueue`/`createTransferToTeam` |
| 3 | Confirmação humana obrigatória, explícita e vinculada à ação/argumentos mostrados, para toda escrita (9D) |
| 4 | Somente o chatbot aciona. Sem Agent Assistant, sem IA em nome de usuário, sem ferramentas administrativas |
| 5 | Criar/alterar fluxos por IA fica fora da Fase 9 |
| 6 | Os três provedores (OpenAI, Anthropic, Google) na 9A, mais verificação do Groq |

### Fora da 9A (explícito)
Transferência, ocorrência, oportunidade, X2, Knowledge como tool, catálogo de ferramentas por organização, permissões de ferramentas, auditoria de execução de tools, confirmação humana, execução de ações reais, Agent Assistant, criação/alteração de fluxos.

## 2. Estado atual (auditado)

- `internal/ai`: `Provider{Name, Capabilities, Complete, ListModels}`. `Request{Model, System, Messages, MaxTokens, Temperature}`, `Message{Role, Content string}`, `Response{Text, Model, Usage}`. `Complete` devolve **só texto**. `Capabilities.ToolCalling=true` nos adaptadores é apenas declaração futura, nada de tools é enviado ou lido.
- Adaptadores: `openAICompat` (OpenAI **e** Groq, só mudam nome/base URL), `anthropic.go`, `google.go`, com `doJSON` comum (4 MiB de limite, chave redigida, `Error`/`ErrorKind`).
- `completeAI` (`internal/handlers/ai_runtime.go`) é a única porta: resolve a chave, chama `Provider.Complete`, grava `AIUsageLog`. Único chamador: `generateAIResponse` (`chatbot_processor.go`), que serve `chatbot_reply` e `chatbot_flow_node`.
- Hoje há **uma** chamada por resposta e uma linha de uso por chamada.

## 3. Contrato neutro (pacote `internal/ai`)

Tudo novo é **aditivo**. Com `Request.Tools` vazio, o payload enviado a cada provedor tem de ser **idêntico ao de hoje** (teste de regressão por comparação de corpo).

```go
// Definição declarada pelo Whatc. Parameters é um JSON Schema (objeto) em json.RawMessage.
type ToolDefinition struct {
    Name        string          // ^[a-zA-Z0-9_-]{1,64}$ (interseção das regras dos 4 provedores)
    Description string
    Parameters  json.RawMessage // {"type":"object",...}; validado na construção
}

// Pedido estruturado do modelo. Arguments é o JSON cru já parseado como objeto.
type ToolCall struct {
    ID        string          // do provedor; sintético no Google (ver §5.3)
    Name      string
    Arguments json.RawMessage // sempre um objeto JSON válido (adaptador garante ou falha)
    Opaque    json.RawMessage // estado de transporte do adaptador (ver regra abaixo)
}

// Resultado devolvido ao modelo. Texto apenas; erro de tool é dado, não exceção.
type ToolResult struct {
    CallID  string
    Name    string
    Content string
    IsError bool
}

type Message struct {
    Role        Role         // RoleUser | RoleAssistant | RoleTool (novo)
    Content     string
    ToolCalls   []ToolCall   // só em RoleAssistant
    ToolResults []ToolResult // só em RoleTool
}

type ToolChoice string // "" (= auto) | ToolChoiceAuto | ToolChoiceNone

type Request struct { /* campos atuais */ Tools []ToolDefinition; ToolChoice ToolChoice }

type FinishReason string // stop | tool_calls | length | other

type Response struct { /* campos atuais */ ToolCalls []ToolCall; Finish FinishReason }
```

Regras do contrato:
- **Associação call↔result** por `CallID` obrigatório; uma `RoleTool` message carrega **todos** os resultados da rodada do assistente anterior (cada `ToolCall` do assistente tem exatamente um `ToolResult`). O adaptador mapeia: OpenAI/Groq → N mensagens `role:"tool"`; Anthropic → uma mensagem `user` com N blocos `tool_result`; Google → um `content` com N `functionResponse`.
- **`ToolCall.Opaque` é propriedade de transporte/adaptação.** Existe só para devolver ao provedor, na parte correspondente, estado que ele exige de volta (ex.: `thoughtSignature` do Gemini no `functionCall`). **Nunca é persistido, exibido, auditado nem interpretado pelo domínio**: o orquestrador apenas copia a `ToolCall` de volta no replay. Fica fora de logs, de `AIUsageLog` e de qualquer auditoria de 9B. Só o adaptador que o produziu o lê. Não há outro campo opaco no contrato (`Message` não tem).
- **`ToolChoice` só `auto`/`none`, por decisão deliberada de escopo.** A 9A não expõe seleção forçada de ferramenta (`required`/ferramenta específica) no contrato neutro porque nada no escopo atual precisa dela; será reavaliada quando uma ferramenta real exigir esse comportamento. (Os provedores variam: o Groq documenta `required` e função específica; a Anthropic rejeita `any`/`tool` com 400 em alguns modelos recentes. Isso é contexto para a reavaliação futura, não a justificativa.)
- Sem streaming, sem `strict`, sem tools nativas/hospedadas do provedor (ex.: built-in/MCP do Groq, server tools da Anthropic): só *client tools* locais.
- `Capabilities.ToolCalling` passa a ser **verdadeiro apenas quando a 9A o implementa** para aquele adaptador; é o gate do orquestrador (provedor sem suporte → resposta de texto normal, sem tools).

## 4. Separação orquestrador / registry / executor

```
Provider  →  ToolCall estruturada  →  Orquestrador  →  Resolução (Registry)  →  Executor
```

O ponto central pedido: **o loop não presume que toda `ToolCall` é executável.** O orquestrador só *pede* a execução por uma porta; quem decide se pode, e como, é a resolução.

```go
// Resolução: nome -> definição + executor, ou "desconhecida".
type ToolResolver interface {
    Definitions() []ToolDefinition
    Resolve(name string) (Tool, bool)
}

type Tool interface {
    Execute(ctx context.Context, call ToolCall) (ToolResult, error)
}
```

Na 9A existe só: interface + `testResolver` (double de teste) e um `emptyResolver` (zero ferramentas, usado quando nada está registrado). **Não há registry de produção** e `generateAIResponse` continua sem passar tools. A 9B encaixa **antes de `Execute`** (autorização, catálogo por organização, auditoria, confirmação) sem desmontar o loop, porque `Resolve` e `Execute` já são pontos separados, e o orquestrador recebe o resolver por injeção.

Onde fica: `internal/ai` ganha só o **contrato de tipos + adaptadores**. O **loop** (`RunToolLoop`) fica em `internal/ai` também, sem dependência de handlers/GORM (recebe `Provider`, `ToolResolver`, limites e um *hook* de observação). A integração com `completeAI` é descrita em §7.

## 5. Adaptadores

Os três são implementados no mesmo PR, cada um com testes contra servidor simulado (`httptest`) que reproduz o formato do provedor.

### 5.1 OpenAI (e família Chat Completions)
- Envio: `tools:[{type:"function",function:{name,description,parameters}}]`; `tool_choice:"auto"|"none"`.
- Resposta: `choices[0].message.tool_calls[]{id, function{name, arguments (string JSON)}}`, `finish_reason:"tool_calls"`. `arguments` é **string** com JSON: o adaptador faz o parse; inválido → ver §6.
- Retorno: `{"role":"tool","tool_call_id":...,"content":...}`; o assistente anterior é reenviado com `tool_calls`.
- Paralelismo: aceito (várias `tool_calls`); a 9A não envia `parallel_tool_calls` (padrão do provedor).

### 5.2 Anthropic
- Envio: `tools:[{name,description,input_schema}]`; `tool_choice:{"type":"auto"|"none"}`.
- Resposta: blocos `content[]` de `type:"tool_use" {id,name,input(objeto)}` (podem vir junto com `text`); `stop_reason:"tool_use"`; `max_tokens` pode truncar um `tool_use` → tratado como `length`/chamada inválida.
- Retorno: mensagem `user` com blocos `tool_result {tool_use_id, content, is_error}`, **todos os resultados numa única mensagem, imediatamente após** o `assistant` com os `tool_use`.
- A Anthropic exige devolver blocos de *thinking* quando o raciocínio estendido está ligado; a 9A **não liga** thinking, e `Opaque` cobre o caso futuro.

### 5.3 Google (Gemini, `generateContent`)
- Envio: `tools:[{functionDeclarations:[{name,description,parameters}]}]`; `toolConfig.functionCallingConfig.mode: AUTO|NONE`.
- Resposta: `parts[].functionCall {name, args(objeto), id?}`. O `id` é opcional na API. **Regra de correlação:** se o Google devolveu `id`, o adaptador o **preserva** em `ToolCall.ID` e o devolve em `functionResponse.id`; se não devolveu, gera um ID interno **determinístico** (`call_<índice>` na ordem das partes) apenas para a associação local, e o `functionResponse` é casado por `name` + posição (sem enviar `id` que o provedor nunca emitiu). O adaptador mantém também qualquer mecanismo de correlação específico que a referência oficial exigir (confirmar no gate). Retorno: `parts[].functionResponse {name, response, id?}`.
- Modelos com *thinking* podem exigir devolver `thoughtSignature` exatamente na parte do `functionCall`: guardado em `ToolCall.Opaque` (estado de transporte, §3) e reenviado sem interpretação.
- **Subconjunto de schema:** o Gemini aceita só parte do JSON Schema em `parameters`. O adaptador valida/rejeita no momento do *build* da request (erro claro, nunca falha silenciosa no provedor).
- **Ressalva de verificação:** as páginas de documentação que consegui ler nesta rodada não detalham o REST de `generateContent` (cobriram o formato novo do SDK). Os formatos acima (campos de `functionCall`/`functionResponse`, `thoughtSignature`, `finishReason` de chamada malformada, subconjunto de schema) são **pré-requisito de confirmação contra a referência oficial antes de escrever o adaptador**, e ficam travados nos testes com servidor simulado.

### 5.4 Groq — verificação de compatibilidade (pedida)
Fonte: documentação oficial do Groq (Tool Use / Local Tool Calling / OpenAI compatibility), lida nesta rodada.

| Ponto | Resultado |
|---|---|
| Formato de `tools` | `type:"function"` + `function{name,description,parameters}`: **igual ao OpenAI** |
| `tool_calls` na resposta | `id`, `type:"function"`, `function{name, arguments (string JSON)}`, `finish_reason:"tool_calls"`: **igual** |
| Mensagem de resultado | `role:"tool"`, `tool_call_id`, **`name`**, `content`. O Groq documenta o campo `name` no resultado; o OpenAI usa só `tool_call_id` |
| `tool_choice` | `auto`, `none`, `required`, ou função específica: superconjunto do que a 9A usa |
| `parallel_tool_calls` | existe; suporte por modelo (a documentação cita "alguns modelos") |
| Campos **não suportados** (400) | `logprobs`, `logit_bias`, `top_logprobs`, `messages[].name`, `n != 1` |
| Erro de geração de tool | **HTTP 400** com `error.failed_generation` (`reason`, `tool_call_id`, `attempted_arguments`) quando o modelo não gera uma tool call válida; a doc recomenda retry com temperatura menor |
| Suporte por modelo | "todos os modelos hospedados suportam tool use" segundo a doc, mas a confiabilidade varia por modelo (relatos públicos de `tool_use_failed` frequente em alguns) |
| `temperature` 0 | convertido para 1e-8 |

Conclusões para o design:
1. **O formato de tools/tool_calls/resultado é compatível** com o adaptador OpenAI. Compatível não é equivalente: três diferenças reais.
2. **`message.name` ≠ `tool-result.name`.** São dois conceitos distintos: o Groq rejeita `messages[].name` em geral (400), mas documenta `name` no resultado de tool (`role:"tool"`). O serializador do adaptador trata os dois **explicitamente, sem flag booleano**:
   - mensagens `user`/`assistant`/`system`: nunca levam `name`;
   - `assistant` com tool call: `tool_calls[]{id, type:"function", function{name, arguments}}` (o `function.name` é parte da chamada, não `message.name`);
   - resultado de tool: `{role:"tool", tool_call_id, name, content}` para Groq. Para OpenAI, o formato **exatamente aceito pelo endpoint usado** (`role:"tool"`, `tool_call_id`, `content`; `name` não é necessário). A diferença fica como dado do provedor no serializador (uma função de resultado por dialeto), coberta por teste de corpo para cada um.
3. Falha de geração de tool vira **HTTP 400**, não 200 com texto. O adaptador mapeia `error.failed_generation` para um erro **tipado** novo `KindInvalidToolCall`, **não** `KindInvalidRequest`, para que o chamador distinga "modelo errou a chamada" de "request ruim" (ver §6; **sem retry automático** na 9A).
4. **`Capabilities.ToolCalling=true` para Groq**, desde que os testes do contrato (servidor simulado) passem. A documentação confirma tool calling local (a aplicação define/executa as funções e mantém o loop) e o formato `tool_calls` compatível, que é o modelo do Whatc. A confiabilidade por modelo varia; **a validação real com chave Groq é recomendada antes de habilitar ferramentas reais em produção** (9C/9D), não é bloqueio da 9A.
5. **Limite da verificação:** feita pela documentação e relatos públicos; sem chave Groq nesta sessão não há chamada real. A 9A fixa o comportamento com servidor simulado que reproduz exatamente os formatos acima.

Estrutura: `openAICompat` continua um adaptador só para OpenAI e Groq; as diferenças de dialeto (§2) ficam no serializador por provedor, sem flag genérico. Nada de provedor novo.

## 6. Loop limitado e casos de falha

`RunToolLoop(ctx, p Provider, req Request, r ToolResolver, lim Limits) (*LoopResult, error)`

```go
type Limits struct {
    MaxSteps       int           // chamadas ao provedor no loop (padrão 4)
    MaxToolCalls   int           // total de ToolCalls executadas (padrão 8)
    MaxCallsStep   int           // por rodada (padrão 4)
    Timeout        time.Duration // relógio total do loop (padrão 20s)
    MaxArgsBytes   int           // por ToolCall (padrão 8 KiB)
    MaxResultBytes int           // por ToolResult devolvido ao modelo (padrão 8 KiB, truncado com marca)
}
```

Fluxo: chama o provedor; se `Finish != tool_calls` → devolve o texto. Senão, para cada `ToolCall`: valida limites e argumentos → `Resolve` → `Execute` → `ToolResult`; monta `RoleAssistant`(com `ToolCalls`) + `RoleTool`(resultados); repete. Ao estourar `MaxSteps`, faz **uma última chamada com `ToolChoice=none`** para obter texto; se ainda assim vier tool call, falha com `ErrToolLoopLimit` (o chamador cai no comportamento de falha de IA que já existe).

**Quatro categorias de falha, tratadas separadamente (nenhum retry genérico no orquestrador):**

| Categoria | Exemplos | Tratamento |
|---|---|---|
| Erro do provedor/transporte | 5xx, rede, 429, timeout | propagado como `*ai.Error`; **o `RunToolLoop` não faz retry**. Retry só no adaptador e somente se o erro for seguramente recuperável (na 9A: nenhum retry novo; comportamento atual de `Complete` preservado) |
| Tool call malformada **pelo provedor** | Groq 400 `failed_generation`, `MALFORMED_FUNCTION_CALL` do Google, `max_tokens` cortando `tool_use`, `arguments` ilegível | `KindInvalidToolCall`; o loop **aborta** com esse erro tipado. **Sem retry automático**; pode ser revisitado com evidência real dos provedores |
| Tool call semanticamente inválida / erro da ferramenta | tool inexistente, `arguments` parseável mas não-objeto ou acima de `MaxArgsBytes`, `Execute` com erro | **não derruba o loop**: vira `ToolResult{IsError:true}` curto e sanitizado devolvido ao modelo, que decide o próximo passo dentro dos limites normais (sem contador especial de "correção") |
| Limite do loop | `MaxSteps`, `MaxToolCalls`, `MaxCallsStep`, `Timeout` | erro tipado do loop (`ErrToolLoopLimit`), exceto o último passo com `ToolChoice=none` descrito acima |

| Situação | Comportamento |
|---|---|
| Tool inexistente | `ToolResult{IsError:true, Content:"unknown tool"}`; conta nos limites. Nunca panic |
| `Execute` retorna erro | vira `ToolResult{IsError:true}` com mensagem **sanitizada e curta**; erro interno nunca vaza ao modelo nem ao cliente |
| `Execute` panic | `recover` → `IsError`; nunca derruba o processo |
| Resultado grande | truncado em `MaxResultBytes` com marca explícita |
| `ctx` cancelado / timeout total | aborta, sem novas chamadas, retorna `ctx.Err()`/`KindTimeout`; o `ctx` é propagado ao `Execute` |
| Provedor sem `ToolCalling` | `RunToolLoop` não envia tools e se comporta como `Complete` simples |
| Chamadas paralelas | executadas **em série**, na ordem do provedor (determinismo; a 9B pode reavaliar) |

Os limites são constantes/`Limits` com padrões, **sem** configuração por organização nem coluna nova (YAGNI até a 9B/9C mostrarem a necessidade).

## 7. Relação com `completeAI`

- **A 9A não cria uma segunda porta de IA.** `completeAI` e `generateAIResponse` ficam **intocados**: mesma assinatura, mesmo comportamento, uma linha de `AIUsageLog` por chamada. Nada em `internal/handlers` muda.
- A 9A entrega apenas `RunToolLoop` em `internal/ai`, que recebe um `Provider` e um `ToolResolver` por injeção. O contrato de integração fica **preparado, não implementado**: o `completeAI` atual já resolve chave e constrói o provedor, então a integração futura será **estender a porta única** (não duplicá-la), num único ponto, quando 9B/9C estiverem prontas. O desenho dessa integração (inclusive como cada passo do loop vira linha de uso) é assunto da nota da 9B.
- Sem `tool_steps` em `ai_usage_logs` e sem usar `AIUsageLog` para representar execução de agente; a auditoria de execução de tools nasce na 9B.

## 8. Testes (servidores simulados, sem rede real)

- **Regressão:** sem `Tools`, corpo enviado igual byte a byte ao atual, nos 4 provedores; testes existentes de `internal/ai` e do chatbot intactos.
- **Por adaptador (OpenAI, Anthropic, Google, Groq):** serialização de `tools`; parse de 0/1/N `tool_calls`; `arguments` string (OpenAI/Groq) vs objeto (Anthropic/Google); IDs sintéticos do Google; replay do assistente + resultados no formato certo; `ToolChoice none`; erro de provedor tipado; chave nunca aparece em erro.
- **Groq:** servidor simulado com 400 `failed_generation` → `KindInvalidToolCall`; resultado de tool com `name`, **nenhuma** mensagem user/assistant com `message.name`; OpenAI sem `name` no resultado; `ToolCalling=true` para ambos.
- **Google:** `id` presente é preservado e devolvido; `id` ausente gera `call_<índice>` determinístico e o `functionResponse` casa por nome/posição; `thoughtSignature` volta intacto na parte correta; `Opaque` não aparece em nenhuma saída de log/erro.
- **Loop:** fluxo feliz (1 e 2 rodadas); tool desconhecida; args não-objeto/grandes viram `IsError` sem execução; **tool call malformada do provedor aborta sem retry (uma só chamada ao provedor)**; `MaxSteps` com chamada final `none`; `MaxToolCalls`/`MaxCallsStep`; resultado truncado; timeout; cancelamento no meio; panic no executor; executor com erro; provedor sem tool calling; execução em série na ordem.
- **Separação:** teste prova que `RunToolLoop` não executa nada sem passar por `Resolve`, e que um resolver que recusa não chega ao `Execute`.
- **Chatbot intacto:** `generateAIResponse`/`completeAI` sem alteração (diff vazio em `internal/handlers`) e testes existentes do chatbot verdes.
- `go build`, `go vet`, suíte completa com apenas as 3 falhas conhecidas.

## 9. Compatibilidade, schema e risco

- **Sem mudança de schema, migração, API HTTP, frontend, config ou `internal/handlers`.** Tudo desligado por construção: nada em produção chama o loop.
- **Regressão obrigatória e literal:** `Request.Tools` nulo/vazio ⇒ mesmo payload (byte a byte, por provedor) ⇒ mesma resposta ⇒ mesmo `generateAIResponse` ⇒ mesmo comportamento.
- Risco principal: formatos dos provedores. Mitigado por servidores simulados baseados na documentação e pelo gate `Capabilities.ToolCalling`.
- Segurança: argumentos e resultados são dados não confiáveis (vêm do modelo/do cliente). Limites de tamanho, parse estrito, sem execução na 9A, mensagens de erro sanitizadas.


## 10. Plano de commits (locais, sem push até revisão)

1. `feat(ai): neutral tool-calling types and request/response fields` (+ regressão de payload byte a byte)
2. `feat(ai): OpenAI and Groq tool calling` (serializadores por dialeto)
3. `feat(ai): Anthropic tool calling`
4. `feat(ai): Google tool calling` (**antes dele**, confirmar a referência oficial de `generateContent`, §5.3)
5. `feat(ai): bounded tool loop with resolver/executor ports`

Testes junto de cada commit. Nenhum commit toca `internal/handlers`.

## 11. Decisões fechadas

- `tool_steps` em `ai_usage_logs`: não.
- Groq: implementar; `ToolCalling=true` se os testes do contrato passarem; validação real recomendada antes de ferramentas reais (9C/9D).
- Limites padrão (4 passos, 8 chamadas, 4 por rodada, 20 s, 8 KiB args/resultado): aprovados como defaults internos.
- Execução serial das chamadas paralelas: aprovada.
- Gate do Gemini (confirmar a referência oficial antes de codar o adaptador): mantido.
- Sem `completeAIWithTools`; sem retry genérico no `RunToolLoop`; `Opaque` só como estado de transporte.
