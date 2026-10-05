# Fase 9C — Validação de tool calling com provedores reais

Documento de resultados do gate de ativação. **Nenhum provedor foi validado ainda**: nesta sessão não havia chaves reais, então nada foi executado. Um provedor sem veredito **validado** abaixo **não** pode constar em `[ai_tools] providers` (e, sem ele, as tools nunca rodam).

## Como executar

Teste manual, fora da suíte e do CI (build tag `realprovider`). Chaves e modelos só por variável de ambiente; nunca em arquivo, log ou commit. Cada execução custa poucos centavos.

```bash
WHATC_REAL_GROQ_KEY=... WHATC_REAL_GROQ_MODEL=... \
WHATC_REAL_GOOGLE_KEY=... WHATC_REAL_GOOGLE_MODEL=... \
WHATC_REAL_OPENAI_KEY=... WHATC_REAL_OPENAI_MODEL=... \
WHATC_REAL_ANTHROPIC_KEY=... WHATC_REAL_ANTHROPIC_MODEL=... \
WHATC_REAL_REPORT=resultado.md \
go test -tags realprovider ./internal/ai -run TestRealProviders -v -count=1
```

Um provedor sem as duas variáveis é pulado e registrado como **não validado**. A ordem é a de prioridade: Groq, Gemini 3, OpenAI, Anthropic. Use o identificador de um modelo que suporte tool calling (para o Gemini, um modelo da família 3).

## Cenários

| # | Cenário | O que confirma |
|---|---|---|
| 1 | Uma tool call e o resultado de volta, resposta final em texto | protocolo básico de ida e volta pelo adaptador |
| 2 | Várias chamadas (mesma resposta ou em sequência) | ordem e correlação dos resultados |
| 3 | Os schemas reais das duas tools da 9C na mesma requisição | `enum`, `integer` com `minimum`/`maximum` e objeto vazio aceitos pela API |
| 4 | `ToolChoice none` com tools declaradas | o modelo responde em texto, sem chamar |
| 5 | Resposta interrompida durante a geração de uma tool call | **observação**: como o provedor sinaliza (e se o adaptador classifica como `KindInvalidToolCall`); divergência é registrada, não imposta |
| 6 (Groq) | Resultado de tool com `name`; formato real de `failed_generation` | `name` aceito no resultado, nenhum `message.name`; corpo real do erro quando ocorrer |
| 8 (Fase 9D) | As três tools de produção declaradas; o cliente pede uma pessoa; resultado `transfer_performed:false` volta | o schema de `request_agent_transfer` (string opcional) é aceito, o modelo chama a tool com argumentos válidos e responde após o resultado; o texto final e a escolha de chamar a tool ficam como **observação** (o servidor descarta o texto; `ToolChoice` não força tool). Se o modelo não chamar mesmo com pedido explícito, o cenário fica **não validado** (ida e volta não exercitada), nunca divergente |
| 9 (robustez do loop) | `MaxSteps: 1` com um pedido que induz a tool; a segunda requisição é a rodada final | a rodada final não declara tools nem `tool_choice`, o histórico de tools vai achatado como dados, o provedor responde só texto sem `ToolCall` e o loop encerra sem erro; o decorador registra as requisições como evidência. Não é critério do gate de capacidade do provedor (1–4 e 8): mede o nosso loop |
| 7 (Gemini 3) | `thoughtSignature` (com e sem), `functionResponse` sem `id`, `id` quando o provedor envia, e as palavras-chave de schema `additionalProperties` e `default` perguntadas direto à API | confirma ou relaxa `googleAllowed` e o tratamento do `Opaque` com evidência |

Vereditos: **validado** (o cenário passou), **divergente** (o provedor se comportou diferente do que o adaptador assume), **observado** (informativo, sem asserção) e **não validado** (não executado).

## Regras

- Divergência encontrada ⇒ **parar e apresentar** antes de mudar o contrato ou o adaptador; a correção vai em commit próprio e o cenário é repetido.
- Só entra em `[ai_tools] providers` o provedor cujos cenários 1 a 4 e 8 estão **validados** e cujas divergências foram resolvidas.
- Resultado em um modelo não vale para outro: registrar o modelo usado.

## Resultados

Data da última execução: **nenhuma**.

| Provedor | Modelo | Data | Cenários 1–4 e 8 | 5 (observação) | 6/7 (específicos) | Veredito | Em `providers`? |
|---|---|---|---|---|---|---|---|
| Groq | — | — | não executado | — | — | **NÃO VALIDADO** | não |
| Google (Gemini 3) | — | — | não executado | — | — | **NÃO VALIDADO** | não |
| OpenAI | — | — | não executado | — | — | **NÃO VALIDADO** | não |
| Anthropic | — | — | não executado | — | — | **NÃO VALIDADO** | não |

Quando houver execuções, colar aqui a tabela gerada em `WHATC_REAL_REPORT` (uma linha por cenário) e atualizar a tabela acima.
