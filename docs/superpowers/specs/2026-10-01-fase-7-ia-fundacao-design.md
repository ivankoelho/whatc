# Fase 7: fundacao da IA (credenciais, AIProvider, Groq, modelos, observabilidade)

Escopo desta etapa: seguranca da chave, interface comum de provedores, os tres provedores atuais migrados
sem mudanca de comportamento, Groq como quarto provedor, listagem de modelos e observabilidade basica.
Fora do escopo (fases seguintes): tools/function calling, RAG, assistente do agente, automacoes, acoes autonomas.

## Credenciais

- A chave de API e criptografada (AES-GCM, `app.encryption_key`) ao ser salva. Banco, cache Redis e structs em memoria
  guardam so o texto cifrado (`enc:...`).
- `resolveAIAPIKey` e o unico ponto que decifra, imediatamente antes da chamada ao provedor. Chave cifrada que nao
  decifra (chave de criptografia trocada ou ausente) e erro: nunca se envia o texto cifrado a um provedor.
- A API nunca devolve a chave, nem mascarada. `GET /api/chatbot/settings` devolve `ai_api_key_configured` (bool).
- A chave do Google passou da URL para o header `x-goog-api-key`.
- Alterar provedor, modelo ou chave exige `settings.chatbot:write` (antes, qualquer membro da organizacao podia
  substituir a chave). Provedor desconhecido retorna 400.

### Migracao das chaves existentes

`EncryptChatbotAIKeys` roda no `-migrate`: cifra as chaves em texto puro (inclusive linhas apagadas logicamente).
Idempotente; verifica a ida e volta antes de gravar; UPDATE com compare-and-set; nunca registra chave (nem em debug);
sem `app.encryption_key` nao altera nada e avisa. O cache de settings e esvaziado uma vez a cada inicializacao,
para nao sobreviver entrada antiga com chave em texto puro.

Reversao: a versao anterior nao le chave cifrada. Voltar a versao exige digitar as chaves de novo na tela
(a tela aceita uma nova chave a qualquer momento). Mantenha `app.encryption_key` estavel: trocar o valor torna as
chaves ja salvas indecifraveis e a IA falha com erro explicito ate que sejam digitadas de novo.

## AIProvider (`internal/ai`)

`Provider { Name, Capabilities, Complete, ListModels }` + `ai.New(nome, cfg)`. Adaptadores: openai e groq (mesmo
adaptador OpenAI-compativel), anthropic, google. Erros tipados (`auth`, `rate_limit`, `invalid_request`, `provider`,
`timeout`, `network`, `empty_response`, `bad_response`) com a chave redigida. Os payloads dos tres provedores atuais
sao os mesmos de antes (testes fixam role do historico, system prompt, `max_tokens` e "temperature so se > 0").

Groq: base `https://api.groq.com/openai/v1`, `Authorization: Bearer`, `POST /chat/completions`, `GET /models`.
Nao ha lista fixa de modelos Groq. **Nao foi possivel chamar a Groq de verdade neste ambiente** (rede bloqueada):
o adaptador e testado contra servidores simulados no formato documentado. A primeira chamada real com uma chave
valida e a validacao que falta.

## Modelos

`POST /api/chatbot/ai/models {provider, api_key?}` pergunta ao proprio provedor. Com `api_key` usa a chave digitada
(nao e salva); sem ela usa a salva, mas so para o provedor ao qual ela pertence. A tela tem "Carregar modelos";
OpenAI/Anthropic/Google mantem as sugestoes antigas como alternativa, Groq nao tem nenhuma.

## Observabilidade (`ai_usage_logs`)

Por chamada: provedor, modelo (o informado pelo provedor), feature (`chatbot_reply`, `chatbot_flow_node`,
`model_list`), latencia, tokens (entrada/saida/total), sucesso, tipo e status do erro, mensagem de erro sanitizada e
ids de contexto (organizacao, conta, contato, sessao, usuario). Nao guarda prompt, resposta nem segredo. Falha ao
registrar nunca derruba a resposta ao cliente. Custo estimado nao e calculado (nao ha tabela de precos).

## Limites desta etapa

- Um unico modelo por configuracao. Modelos por finalidade (chatbot, assistencia, classificacao, automacao) e timeout
  configuravel ficam para quando houver um segundo consumidor; `Request` e `ai_usage_logs.feature` ja comportam isso.
- `PUT /api/chatbot/settings` continua sem checagem de permissao para os campos que nao sao de IA (comportamento
  anterior, fora do escopo): vale revisar.
