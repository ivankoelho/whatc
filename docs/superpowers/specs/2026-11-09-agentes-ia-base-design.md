# Agentes de IA — Projeto 1: agente base (design)

Data: 2026-11-09. Base: `origin/development` @ `217f48b3`. Branch: `feature/agente-ia-base`.

> Este é o **projeto 1 de 4** da iniciativa "levar o melhor do DeskcommCRM (MIT, `melgarafael/DeskcommCRM`) para o WhATC". Os outros três têm desenho próprio e dependem deste:
> 2. Conhecimento e ferramentas (base de conhecimento, extensões HTTP do catálogo, consultas ao X2).
> 3. Qualificação e região (endereço nas Unidades, CEP/localização, mover a oportunidade no funil de vendas).
> 4. Copiloto do atendente (resumo na transferência, sugestão de resposta).
>
> Marcação: **[decidido]** = dito pelo Ivan; **[minha escolha]** = decisão tomada por mim a pedido dele ("a partir da expertise em sistemas dessa natureza"), sujeita a revisão.

## 1. Intenção

O WhATC já responde com IA dentro do chatbot, mas como um prompt livre único por organização/conta. Falta o que o Deskcomm tem de melhor: **agentes** com instruções estruturadas, que o modelo é obrigado a seguir, vários por organização, que **param no instante em que um humano assume**, com **teto de uso** por organização.

O objetivo comercial (Atacadão dos Pisos) é um agente que atende no WhatsApp, entende a intenção de compra e, nos projetos seguintes, qualifica e entrega ao vendedor da loja certa. Este projeto entrega o alicerce: o agente existe, é configurável, é seguro e é desligável.

## 2. O que já existe (não refazer)

Verificado em `origin/development`:

- `internal/ai`: interface `Provider` com OpenAI, Anthropic, Google e Groq; laço de ferramentas limitado (`toolloop`).
- `ChatbotSettings.AI` (`models.AIConfig`): provedor, modelo, chave **por organização** (criptografada, só `resolveAIAPIKey` decifra), temperatura, `SystemPrompt`, histórico. A linha de `chatbot_settings` pode ser da organização (`whats_app_account = ''`) ou de uma conta de WhatsApp.
- `completeAI` (`internal/handlers/ai_runtime.go`): **única porta** de toda chamada de IA; grava `AIUsageLog` (tokens, latência, erro, fontes de conhecimento).
- `generateAIReply` (`chatbot_processor.go`): monta `SystemPrompt` + `AIContext` + bloco de conhecimento + histórico.
- `AIContext` (texto fixo ou chamada de API), `internal/knowledge` (busca textual Postgres), `internal/aitools` (ferramentas governadas, confirmação do cliente, auditoria; inclui `request_agent_transfer`).
- Nó `ai_response` nos fluxos do chatbot (`chatbot_graph_runner.go`).
- `hasActiveAgentTransfer`: com transferência ativa o chatbot não processa a mensagem; existe `TransferSourceAgentInitiated` (humano que escreve primeiro cria transferência).

## 3. Objetivos

1. **Vários agentes de IA por organização**, dentro do chatbot de IA atual, sem módulo novo. **[decidido]**
2. Cada agente tem **campos de instrução estruturados**, seguidos à risca. **[decidido]**
3. **Humano assume → agente desliga imediatamente** naquela conversa e não volta sozinho. **[decidido]**
4. O agente **nunca promete** preço, prazo, estoque ou reserva. **[decidido]**
5. **Teto de uso por organização**, com parada segura ao estourar. **[minha escolha]**
6. Compatibilidade total: quem não criar agente continua com o `SystemPrompt` de hoje, sem mudança de comportamento.

## 4. Não-objetivos (neste projeto)

- RAG com vetores, extensões HTTP novas, consultas ao X2, endereço nas Unidades, mover oportunidade no funil, sugestão de resposta ao atendente: projetos 2–4.
- Histórico/versionamento de instruções com "voltar versão" (o Deskcomm tem; fica para depois, **[minha escolha]**).
- Módulo, menu ou tela de "Agentes de IA" separada do chatbot **[decidido]**.
- Custo em reais por chamada (exige tabela de preços por modelo; ver §6.5).

## 5. Anti-objetivos (seria fracasso mesmo que "funcione")

- O agente enviar **qualquer** mensagem depois de um humano ter assumido a conversa, inclusive uma resposta que já estava sendo gerada. **[decidido: este é o pior cenário para o Ivan]**
- O agente afirmar preço, prazo, estoque ou reserva como compromisso.
- Uma organização consumir IA sem teto e sem aviso.
- A chave de IA de uma organização ser usada, vista ou logada por outra.

## 6. Desenho

### 6.1 Modelo de dados — tabela `ai_agents` **[minha escolha]**

Por organização (`organization_id`, com isolamento igual ao das demais tabelas), campos:

| Campo | Uso |
|---|---|
| `name`, `is_enabled` | identificação e liga/desliga |
| `role_objective` | papel e objetivo do agente |
| `tone` | tom de voz |
| `always_rules` (lista) | "sempre faça" |
| `never_rules` (lista) | "nunca faça" (default já traz as proibições de promessa) |
| `handoff_rules` (lista) | quando transferir para humano |
| `company_info` | informações fixas da empresa |
| `model`, `temperature`, `max_tokens` | opcionais; vazio herda de `ChatbotSettings.AI` |

Provedor e **chave continuam únicos por organização** em `ChatbotSettings.AI`; o agente não guarda segredo.

### 6.2 Escolha do agente **[minha escolha, confirmada pelo Ivan como "sim"]**

- `ChatbotSettings` ganha `default_ai_agent_id` (por organização e, opcionalmente, por conta de WhatsApp, herdando do padrão da organização).
- O nó `ai_response` do fluxo ganha `agent_id` opcional; se vazio, usa o padrão da conta/organização.
- Sem agente resolvido: caminho atual, com `SystemPrompt` livre.

### 6.3 Montagem do prompt

Ordem fixa em um único ponto (`generateAIReply`):
1. **Camada da plataforma** (não editável por organização): regras invioláveis, incluindo "nunca prometa preço, prazo, estoque ou reserva; confirmar é papel do vendedor", "não invente informação", "pedido de humano = transferir".
2. Campos do agente, em seções rotuladas (papel, tom, sempre, nunca, quando transferir, empresa).
3. `AIContext` e bloco de conhecimento (como hoje).

As regras de "nunca" vêm **depois** das de "sempre" e em bloco próprio, para pesar mais.

### 6.4 Humano assume → agente desliga **[decidido]**

- Reaproveitar `hasActiveAgentTransfer`: transferência ativa impede o agente de processar a mensagem recebida.
- **Fechar a janela em andamento:** logo antes de enviar a resposta do agente, reconsultar se há transferência ativa ou atendente atribuído; se houver, **descartar** a resposta (registrar em `AIUsageLog` como descartada).
- "Humano assumiu" = **existe `AgentTransfer` ativo** para o contato. `Contact.AssignedUserID` é carteira/dono, não assunção, e **não** liga nem desliga o agente.
- Cobrir também: atendente que **envia mensagem** em conversa sem transferência. Hoje a transferência `agent_initiated` nasce em um único ponto (`agent_transfers.go`); a tarefa é **auditar os caminhos de envio do atendente** (inbox, API, fila) e garantir que todos passem por ele, corrigindo só as lacunas.
- O agente **não reassume sozinho**. Só volta se uma pessoa devolver a conversa ao bot (ação já existente de encerrar a transferência; não criar nova).

### 6.5 Teto de uso **[minha escolha]**

- Limite mensal de **tokens** por organização (soma do `AIUsageLog` do mês; já existe o dado). Campo em `ChatbotSettings`. Vazio = sem teto.
- Ao atingir: o agente para de responder, a conversa é transferida a um humano e o admin é avisado. Nunca responde com erro técnico ao cliente.
- Custo em reais fica para depois, por exigir tabela de preço por modelo e provedor.

### 6.6 Verificação antes do envio **[minha escolha]**

Filtro determinístico no texto de saída contra uma lista de expressões de compromisso configurável por organização (padrão: "garanto", "prometo", "reservei", "está garantido", etc.). Se casar, a resposta é bloqueada e a conversa vai para um humano. Teto conhecido: não pega todas as formas de prometer; checagem por um segundo modelo é a evolução, se a prática mostrar necessidade.

### 6.7 Interface

Dentro de Chatbot → IA: lista de agentes, formulário com os campos de §6.1, escolha do agente padrão, teto de uso e a lista de expressões bloqueadas. Textos em PT-BR e EN (padrão do projeto). Permissões: reaproveitar a de configurações do chatbot.

## 7. Restrições

- Isolamento por organização em tudo; chave nunca em log, resposta de API ou cache em claro.
- Migração aditiva e idempotente; nenhuma coluna existente muda. Organizações atuais não mudam de comportamento.
- Branch a partir de `development`; PR com a linha `Roadmap:` conforme o fluxo de roadmap automático; sem deploy sem confirmação do Ivan.
- Testes de Go exigem `TEST_DATABASE_URL` e `TEST_REDIS_URL`.

## 8. Deixado para quem construir

- Nomes de rotas, estrutura exata de migração e layout das telas.
- Se `request_agent_transfer` (aitools, hoje atrás de `ai_tools.enabled` e provedor validado) é o caminho do "quando transferir" ou se basta um marcador no texto da resposta; avaliar o custo de habilitar a camada de ferramentas só para isso.
- Formato de armazenamento das listas (JSONB) e limites de tamanho dos campos.
- Como registrar respostas descartadas e bloqueadas para auditoria.

## 9. Decisões de detalhe (todas **[minha escolha]**, vindas da revisão do desenho)

- **Pontos de envio do agente:** são dois, `chatbot_processor.go` (resposta direta) e o nó `ai_response` (`chatbot_graph_runner.go`). Criar um helper único `humanTookOver(org, contato)` e chamá-lo imediatamente antes de cada envio. A corrida residual entre o recheck e a chamada à API do WhatsApp (milissegundos) é aceita; sem lock nem transação.
- **Quando o agente bloqueia** (teto, filtro de promessa, regra de transferência): o cliente recebe uma mensagem fixa de espera, configurável por organização (padrão: "Vou chamar um atendente para continuar com você."), a conversa vai para a fila padrão pelo mecanismo existente (`createTransferToQueue`) e o admin é avisado pelo mecanismo de aviso que já exista; não criar canal novo.
- **Teto de uso:** conta os tokens de `chatbot_reply` e `chatbot_flow_node` da organização no mês civil em `America/Sao_Paulo`. A chamada que cruza o limite é permitida; o corte vale a partir da seguinte. Sem teto configurado, nada muda (a checagem só roda quando há teto).
- **Filtro de promessa:** vale só quando há agente. Casa por palavra inteira, sem diferenciar caixa nem acento, e ignora ocorrência precedida de "não"/"nao" nas três palavras anteriores (evita bloquear "não garanto estoque"). A lista padrão vive no código; a organização grava só as adições e remoções dela, para que melhorias do padrão cheguem a todas.
- **Agente padrão por conta de WhatsApp:** disponível já nesta entrega (a estrutura por conta de `chatbot_settings` existe). Agente desativado ou apagado cai no `SystemPrompt` livre de hoje, nunca em silêncio.
- **Quando transferir:** marcador no texto da resposta (por exemplo `[[TRANSFERIR]]`), que o servidor lê, remove e converte em transferência. Funciona com qualquer provedor e não exige ligar `ai_tools`. A ferramenta `request_agent_transfer` fica como evolução.
- **Migração e permissões:** seguir o padrão de `internal/database` para tabelas novas; reaproveitar a permissão de configurações do chatbot, sem permissão nova.
