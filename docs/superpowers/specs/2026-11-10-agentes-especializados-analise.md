# Agentes de IA: espera inteligente, identificação, modelo híbrido e especialidades (análise técnica)

Data: 2026-11-10. Base: branch `feature/agente-ia-base` (MVP homologável, sem push). **Este documento é uma análise. Nada daqui foi implementado.**

Legenda: **[verificado]** = li no código desta branch ou em fonte oficial da Meta; **[terceiro]** = fonte não oficial; **[hipótese]** = inferência minha, a confirmar.

## 0. Resumo e recomendação

1. **O Chatbot continua sendo a porta de entrada e o orquestrador; o `AIAgent` evolui para especialidades configuráveis.** Não nasce módulo novo: a estrutura atual (agente, trava de atendimento humano, teto de tokens, extração validada, transferência, oportunidade, protocolo, razão de uso do WhatsApp) já cobre 80% da base.
2. **Cabe no MVP, com risco baixo, só a identificação do agente nas mensagens** (nome de exibição + marca "IA" no histórico). É pequena e não mexe no caminho de decisão.
3. **A espera inteligente e o indicador de digitação merecem um lançamento próprio logo após o piloto**, não dentro do MVP: mexem no caminho principal de mensagens (coalescer mensagens, relógio, reinício após queda), e o indicador de digitação precisa de uma prova real com número de teste.
4. **O modelo híbrido (Chatbot + IA + Meta Flows) é viável com peças que já existem** (nó `whatsapp_flow`, leitura de resposta de Flow, `SessionData`). O ganho de custo **não pode ser presumido**: desde 1º/10/2026 a Meta cobra respostas livres e interativas dentro da janela depois de 1.000 por número por mês. O que economiza é **enviar menos mensagens e gastar menos tokens**, e isso se mede no piloto.
5. **Um único "dono da conversa" por vez** (fluxo do Chatbot, um agente ou um humano) é a regra de arquitetura que impede dois agentes respondendo juntos. Ela reaproveita a trava por contato e o atendimento ativo que já existem.
6. **Ordem sugerida:** piloto do MVP → identificação + medição de custo → espera inteligente → contexto Chatbot↔agente → especialidades (SAC primeiro, por já ter protocolo) → Flows sugeridos pelo agente → Logística → Jurídico.

## 1. O que já existe e será reaproveitado

| Necessidade | Peça existente **[verificado]** | Uso |
|---|---|---|
| Porta de entrada, menus, formulários | Construtor de fluxos do Chatbot: nós `message`, `buttons`, `prompt`, `api_call`, `condition`, `timing`, `set_variable`, `ai_response`, `transfer`, `webhook`, `goto_flow`, `whatsapp_flow`, `end` | Continua sendo a orquestração estruturada |
| Meta Flows | Modelo `WhatsAppFlow`, tela de Flows, nó `whatsapp_flow` que envia o Flow, leitura da resposta (`nfm_reply`) para `Message.FlowResponse` e para as variáveis do fluxo | Base do "formulário estruturado" |
| IA | `completeAI` (porta única, registra `AIUsageLog`), provedores OpenAI/Anthropic/Google/Groq, chave por organização, `AIContext`, base de conhecimento, camadas de prompt do MVP | Todas as especialidades usam a mesma porta |
| Controle de consumo de IA | Teto mensal com reserva atômica (`ai_token_cap.go`) por organização | Estender para teto por agente |
| Controle de custo da Meta | **Razão de consumo do WhatsApp** (`internal/usage`, tabela `message_usage`): uma linha por mensagem, tipo de ator (`ai`, `agent`, `flow`, `campaign`...), vínculo com `AIUsageLogID`, eventos de preço da Meta (`message_pricing_events`), estado de cobrança e custo estimado | Custo real por conversa sem criar medição nova |
| Quem responde | `AgentTransfer` ativo = humano é dono; trava por contato (`ai_takeover.go`); `createTransferRow` como ponto único | Base do "dono único da conversa" |
| Transferência | `transferToTeam`, fila geral, `routing_reason`, atendimento com notas | Mecanismo único de encaminhamento |
| CRM | Oportunidades (`createOrRetriggerSalesOpportunity`), funil, dono só ao assumir | Agente Comercial (já feito no MVP) |
| Protocolos | `Occurrence` (protocolo, estágio, prioridade, equipe, unidade, departamento, categoria, "o que aconteceu", documentos, `SourceTransferID`), regra de que equipe **comercial** não é destino de protocolo | SAC, Logística, Jurídico |
| ERP | Integração X2/XProcess (consulta de pedido por número e documento, dados D-1, só leitura) | Agente de Logística |
| Estrutura organizacional | Unidades, departamentos, equipes, `WhatsAppAccount.DefaultTeamID`, permissões (`chatbot.ai` etc.) | Isolamento por organização, unidade e equipe |
| Identidade da mensagem | `Message.SentByUserID`, `Message.Metadata` (JSONB, livre), prefixo "*Nome:*" quando "assinar com o nome do atendente" está ligado | Identificação do agente de IA |
| Fila e Redis | `internal/queue` (jobs em Redis), Redis já usado em cache e limites | Espera inteligente |

## 2. Sugestão 1: espera inteligente e indicador de digitação

### Como funciona hoje **[verificado]**
Cada mensagem do cliente dispara, no mesmo instante, `processIncomingMessageFull` numa goroutine do webhook, que chama o agente. Não há agrupamento de mensagens. O "digitando" que existe no WhatC é **interno** (um atendente vê que outro atendente digita; evento efêmero de WebSocket): **não** é o indicador que o cliente vê no WhatsApp.

### Proposta
**Agrupar o turno do agente ("turn coalescing"), por contato:**
1. Cada mensagem recebida registra no Redis `chegou_em` e incrementa um contador de geração do contato (`ai:turn:{org}:{contato}`).
2. O turno do agente é agendado para `chegou_em + atraso` (atraso configurável por agente, em segundos; 0 = comportamento atual). Nova mensagem antes do disparo **reinicia** o relógio (nova geração).
3. No disparo: o agente adquire a trava do turno (mesmo padrão da trava por contato, chave diferente), confere que a geração não mudou (senão desiste: o relógio mais novo cuida), confere `humanTookOver`, e processa **todas as mensagens do cliente desde a última resposta do agente** como uma só entrada (hoje `generateAIReply` recebe uma mensagem só).
4. Mensagem que chega **durante** a geração: antes de enviar, a resposta é confrontada com a geração; se mudou, é descartada e o turno é refeito com o conjunto novo. Isso fecha respostas duplicadas e respostas "ao primeiro pedaço".
5. Sobrevivência a reinício: o agendamento fica no Redis (conjunto ordenado por horário) e um varredor leve retoma turnos vencidos. Sem isso, uma queda perde o turno.

**Indicador de digitação (o que o cliente vê no WhatsApp):**
- A Meta documenta o indicador junto do recibo de leitura, no mesmo endpoint de mensagens: `status: "read"` com o `message_id` da **mensagem recebida** mais um objeto `typing_indicator` de tipo texto. Ele some quando você envia a resposta ou depois de cerca de 25 s. A página oficial da Meta confirma a existência do recurso; **os nomes exatos dos campos vieram de provedores parceiros, [terceiro]**, e precisam de prova com o número de teste.
- O cliente `pkg/whatsapp` já tem `MarkMessageRead` (mesmo endpoint); acrescentar o campo é pequeno.
- **Regra de uso:** só acionar quando a chamada de IA **de fato começou** (nunca durante a simples espera, para não "digitar" e depois calar). Efeitos: marca como lidas as mensagens anteriores (aceitável, é o que um atendente faz) e vale no máximo ~25 s; se a IA demorar mais, não repete em laço.
- Custo do indicador: **não encontrei menção de cobrança** nas páginas lidas da Meta (não confirmado); verificar nos eventos de preço do piloto.

### Configuração e risco
- Campos novos no agente: `reply_delay_seconds` (padrão sugerido 6 a 8 s; faixa 0 a 30), `show_typing` (bool). Migração aditiva.
- Risco: **médio**. Mexe no caminho principal de mensagens. Por isso: atrás de chave, **desligado por padrão** (atraso 0 = código atual), com testes de corrida (duas mensagens em sequência, mensagem durante a geração, humano entra durante a espera, queda do processo).
- **Veredicto:** não entra no MVP congelado; é a **primeira evolução depois do piloto**.

## 3. Sugestão 2: identificar o agente de IA nas mensagens

### Hoje **[verificado]**
A mensagem enviada pela IA já fica registrada como ator `ai` na razão de consumo (relatórios de custo), mas o **histórico da conversa** só conhece `SentByUserID`. Mensagem de IA aparece sem usuário, como uma mensagem de bot qualquer. Para humanos existe a assinatura opcional "*Carlos:*".

### Proposta (três camadas, todas consistentes entre si)
1. **Para o cliente:** prefixo no estilo dos humanos, com o **nome de exibição do agente** ("*Assistente Virtual:*"), configurável no agente (`display_name`, com padrão "Assistente Virtual"). A natureza automatizada fica explícita no próprio nome. Não usa nome de pessoa.
2. **No histórico interno:** gravar em `Message.Metadata` `sender = {type: "ai_agent", agent_id, agent_name, ai_usage_log_id}` no envio. A tela de conversa mostra um selo "IA" e o nome do agente, distinto do atendente humano. **Sem migração** em `messages` (o campo JSONB já existe).
3. **Relatórios e auditoria:** já usam `actor_type = ai` na razão de consumo; passam a poder agrupar por agente via `ai_usage_log_id`. `AgentTransfer.Source = ai_agent` já identifica o encaminhamento.

**A IA nunca é gravada como funcionário:** não recebe usuário, não ocupa `SentByUserID`, não conta em carteira nem em métricas de atendente.

- Mudança de dados: 1 coluna aditiva (`ai_agents.display_name`).
- Risco: **baixo** (envio do agente: prefixo + metadado; a trava de atendimento não muda).
- **Veredicto: pode entrar como pequeno complemento do MVP** (estimativa: cerca de 1 dia, com testes), se você quiser que o piloto já mostre o agente identificado.

## 4. Sugestão 3: Chatbot + Agente de IA + Meta Flows (modelo híbrido)

### Regras de cobrança vigentes **[Meta oficial, salvo indicação]**
Li a página oficial de preços da Meta e uma explicação de parceiro (WATI), em 10/11/2026:
- **Desde 1º/10/2026**, mensagens de **serviço** (qualquer mensagem não-template dentro da janela de 24 h) voltam a ser **cobradas por mensagem entregue**, depois de **1.000 gratuitas por mês por número de telefone comercial** (a franquia zera todo mês). Antes eram gratuitas.
- **Templates de utilidade enviados dentro da janela** também voltam a ser cobrados desde 1º/10/2026 (eram gratuitos desde 1º/07/2025).
- Mensagens **do cliente para a empresa** não são cobradas.
- Janela gratuita de **anúncio Click-to-WhatsApp**: **até 7 dias** (as duas páginas dizem 7 dias, não 72 h).
- Meta só altera preços em 1º de janeiro, abril, julho ou outubro, com aviso.
- **Valor no Brasil:** a única cifra que consegui ler na página da Meta é a de utilidade e autenticação, **US$ 0,0068 por mensagem**. A equivalência "serviço = tarifa de utilidade" vem do WATI **[terceiro]**, não da Meta.
- **Não confirmado:** cobrança específica de **Flows** (o WATI classifica Flows, botões e listas como mensagens de serviço; a Meta define serviço como qualquer mensagem não-template e não cita Flows), cobrança do **indicador de digitação**, valores em **BRL** por categoria (estão nos arquivos de tarifas).

### O que isso significa para a decisão
- **Texto livre, botões, lista e Flow são, ao que tudo indica, o mesmo tipo de cobrança: uma mensagem de serviço enviada.** Não há vantagem de preço por "usar Flow" em si.
- **O que muda o custo é o número de mensagens que a empresa envia.** Um Flow que coleta seis campos é **uma** mensagem enviada; as mesmas perguntas em conversa são, em geral, de três a seis mensagens enviadas **e** de três a seis chamadas de IA. As respostas do cliente são gratuitas.
- Ordem de grandeza **[hipótese]**: a US$ 0,0068 por mensagem enviada, cada pergunta evitada vale cerca de US$ 0,007 mais os tokens daquela rodada. É pouco por conversa e relevante em volume; **o valor real precisa sair do piloto**, porque a razão de consumo já guarda o preço que a Meta informa por mensagem.
- Contra: Flow tem custo de construir e manter, taxa de abandono de formulário, e é pior que conversa quando o cliente precisa explicar algo aberto (um defeito, uma reclamação).
- A franquia de 1.000/mês por número é pequena para varejo: na prática, todas as mensagens contam.

### Como integrar sem duplicar
O princípio: **o Chatbot decide a estrutura; o agente decide o significado.**
1. **Chatbot → agente:** o nó `transfer` ganha um destino "agente X" (ou um novo nó `ai_agent`), que entrega a escolha do cliente e as variáveis já coletadas (`SessionData`) num bloco "Contexto já coletado" do prompt. O agente é instruído a **não perguntar de novo** o que está ali. Mudança pequena: montar o bloco; sem migração.
2. **Agente → Chatbot:** o agente pode pedir um fluxo existente com um marcador (por exemplo `[[FLUXO:acompanhar_pedido]]`). O servidor valida contra uma **lista de fluxos permitidos do agente**, inicia o fluxo e passa o dono da conversa de volta ao Chatbot. O agente nunca escreve nada que o servidor execute sem validar.
3. **Agente → Meta Flow:** igual, com `[[FORMULARIO:<id>]]`, restrito a Flows publicados e permitidos para aquele agente. O servidor reutiliza o envio do nó `whatsapp_flow`.
4. **Flow → agente:** a resposta do formulário (`nfm_reply`) já é lida e guardada. Ela passa a entrar no contexto do próximo turno ("dados recebidos do formulário"), e o turno do agente é disparado.
5. **Quando usar Flow em vez de conversa:** regra explícita por agente, não "intuição do modelo": por exemplo, "faltam três ou mais campos estruturados" ou "documento/dados cadastrais" → Flow; "dúvida aberta" → conversa. A regra fica no prompt e é auditada pelo registro de decisões (seção 7).
- **Veredicto:** exige alteração de arquitetura moderada (dono da conversa, marcadores de ação, lista de permitidos). Fase 3, depois das especialidades básicas e da medição de custo.

## 5. Sugestões 4 e 5: agentes especializados e direcionamento

### Estrutura (sem novo módulo)
`AIAgent` ganha **especialidade** e passa a descrever uma "função":
- `specialty`: `comercial`, `logistica`, `sac`, `juridico` ou `outro`.
- **Ações permitidas** (lista fechada no servidor, não livre): o agente só pode executar o que a especialidade permite.
- **Destino humano**: equipe/departamento (e a regra existente de que equipe comercial não recebe protocolo).
- **Limite de consumo** próprio, e `can_route_to` (para quais especialidades pode sugerir mudança).

| Especialidade | O que faz | Reaproveita | O que falta |
|---|---|---|---|
| **Comercial** | Qualifica, abre/atualiza oportunidade, encaminha à unidade | **Já feito no MVP** | Nada obrigatório |
| **SAC** | Reclamação, troca, devolução, garantia; coleta dados e documentos; abre protocolo | `Occurrence`, categorias, "o que aconteceu", documentos, regra de destino não comercial, extração validada do MVP | Função de **serviço** para abrir protocolo sem um usuário humano (hoje `CreateOccurrence` é uma rota autenticada e `opened_by_user_id` é obrigatório); lista de categorias como opções fechadas |
| **Logística** | Acompanha pedido e entrega, identifica atraso, coleta dados, abre protocolo para Logística | Integração X2 (consulta de pedido por número + documento, **dados D-1**), protocolo | Ferramenta de consulta ao pedido chamada pelo servidor; mensagem sempre dizendo "até o fechamento de ontem" |
| **Jurídico** | Classifica demanda, coleta informações e documentos, abre protocolo ao responsável; respostas **somente** de textos aprovados | `AIContext`/base de conhecimento (conteúdo aprovado), protocolo | Marcar conteúdo como "aprovado pelo jurídico"; regra dura de nunca opinar, prometer ou decidir; revisão jurídica do prompt antes de ativar |

**Princípio de segurança de todas:** o modelo **propõe** e o servidor **executa e valida** (listas fechadas, evidência literal do cliente, filtros de saída). Nada de ferramenta livre.

### Quem fala com o cliente: dono único da conversa
Estado explícito por conversa: **dono = {fluxo do Chatbot | agente X | humano}**. Exatamente um.
1. **Entrada pelo Chatbot:** o cliente escolhe "Atendimento", "Pedido" etc.; o nó correspondente define o dono como o agente da especialidade e entrega o contexto.
2. **Troca de especialidade pelo próprio agente (sua alternativa):** quando o texto do cliente não bate com a opção escolhida (escolheu "Atendimento" e disse que recebeu produto danificado), o agente em exercício emite `[[ESPECIALIDADE:sac]]`. O servidor valida: `can_route_to`, agente ativo, mesma organização e número, no máximo duas trocas por conversa. Se valer, troca o dono **atomicamente sob a trava do contato**, registra a decisão e o novo agente assume com um resumo estruturado. O cliente não reinicia nada.
   - **Alternativa avaliada e descartada para a primeira versão:** um "agente roteador" separado que lê toda mensagem. Custa uma chamada de IA extra por mensagem e cria um ponto único de falha. A detecção pelo agente em exercício custa zero chamada extra.
3. **Humano:** o atendimento ativo continua prevalecendo sobre qualquer agente (mecanismo atual, sem mudança).

### Como garantir que só um responda
- Toda resposta automática passa por uma **única função de envio** que, sob a trava do contato, confere duas coisas: **não há humano ativo** (já existe) e **quem está enviando é o dono atual** (novo). Quem não é o dono descarta a resposta.
- A troca de dono acontece sob a mesma trava; portanto não há instante em que dois sejam donos.
- Testes de corrida: duas trocas simultâneas, resposta do agente antigo depois da troca, humano durante a troca.

## 6. Isolamento, permissões e consumo

- **Isolamento:** o agente atua só nos números listados nele (regra do MVP) e só em equipe de unidade válida; especialidades herdam isso. Protocolo e oportunidade nascem com unidade e equipe.
- **Permissões:** cadastrar especialidades usa `chatbot.ai`; abrir protocolo pelo agente usa uma permissão de **serviço do agente**, não a do usuário que configurou. Jurídico ganha permissão própria de configuração (recomendado).
- **Limites de IA:** teto mensal da organização (existe) + **teto por agente** (mesma reserva atômica, chave por agente) + orçamento de tokens **por conversa**, para uma conversa longa não consumir o mês.
- **Custo da Meta:** a razão de consumo (`message_usage`) já guarda ator, vínculo com a chamada de IA e preço informado pela Meta. Falta um **relatório de custo por conversa e por agente** (Meta + IA) e **alertas** de limite. É daí que sairá a regra "Flow ou conversa".
- **Histórico e auditoria das decisões:** tabela nova `ai_agent_events` (troca de especialidade, fluxo sugerido, formulário enviado, protocolo aberto, descarte por humano), com motivo e identificadores. Hoje parte disso só existe como linhas de uso e notas.

## 7. Mudanças de banco de dados

| Mudança | Tipo | Fase |
|---|---|---|
| `ai_agents.display_name` | coluna aditiva | MVP+ |
| `ai_agents.reply_delay_seconds`, `show_typing` | colunas aditivas | Espera inteligente |
| `ai_agents.specialty`, `can_route_to`, `allowed_flow_ids`, `token_cap` | colunas aditivas | Especialidades |
| Dono da conversa (`chatbot_sessions.active_agent_id` ou chave em `SessionData`) | coluna aditiva (ou nenhuma, se usar `SessionData`) | Especialidades |
| `occurrences.opened_by_ai_agent_id` e `opened_by_user_id` **anulável** | alteração em tabela existente | SAC |
| `ai_agent_events` | tabela nova | Especialidades |
| `Message.Metadata.sender` | nenhuma (JSONB livre) | MVP+ |

Todas aditivas e idempotentes, no padrão do projeto. A única que **altera** algo existente é tornar `opened_by_user_id` anulável: precisa de revisão cuidadosa porque várias telas e relatórios de ocorrência assumem que existe um usuário. Alternativa sem migração: um usuário de serviço por organização. **Não recomendo**, porque grava a IA como se fosse um funcionário, o que você descartou.

## 8. Fases propostas

| Fase | Entrega | Depende de | Prioridade | Critérios de aceite |
|---|---|---|---|---|
| **0** | Homologação e piloto do MVP (um número, uma unidade) | Sua autorização | Alta | Roteiro de homologação aprovado; consumo de IA e de Meta medidos no piloto |
| **1** | **Identificação do agente** (nome de exibição, selo "IA", metadado) | Fase 0 (ou antes, se quiser no piloto) | Alta, baixo risco | Cliente vê "*Assistente Virtual:*"; histórico distingue IA de humano; relatórios agrupam por agente; IA nunca aparece como usuário |
| **2** | **Custo por conversa** (Meta + IA) e alertas | Fase 0 | Alta | Relatório mostra custo por conversa e por agente com dados reais; alerta de limite |
| **3** | **Espera inteligente** + digitação (spike da API com número de teste primeiro) | Fase 0 | Média | Duas mensagens em sequência geram uma resposta; humano que entra durante a espera cala o agente; reinício do servidor não perde turno; indicador só durante chamada real |
| **4** | **Contexto Chatbot→agente** (menu entrega escolha e dados) | Fase 0 | Média | Agente não repete pergunta já respondida no menu |
| **5** | **Dono único da conversa** + **SAC** (protocolo pelo agente, troca de especialidade validada) | Fases 1 e 4; migração de `occurrences` | Média | Um só responde; troca auditada; protocolo sem usuário humano; teste de corrida entre agentes |
| **6** | **Agente sugere fluxo do Chatbot e formulário Meta** | Fase 5 | Média | Só fluxos permitidos; resposta do formulário volta ao contexto; economia medida em mensagens e tokens |
| **7** | **Logística** (consulta de pedido X2) | Fase 5 | Média | Resposta sempre com "até o fechamento de ontem"; protocolo para a equipe de Logística |
| **8** | **Jurídico** | Fase 5; revisão do jurídico | Baixa, só após validação | Só textos aprovados; nunca opina; protocolo ao responsável |

## 9. Riscos e decisões que dependem de você

1. **Cobrança de Flows e do indicador de digitação não está confirmada.** Tratar como mensagem de serviço até o piloto provar o contrário, olhando o preço que a Meta devolve por mensagem.
2. **Termos da Meta para IA no WhatsApp:** conferir os termos vigentes antes de ampliar (atendimento de negócio com IA é o uso esperado, mas as regras para IA mudaram recentemente **[hipótese]**).
3. **Jurídico:** exige aprovação e revisão do texto por quem responde juridicamente pela empresa; sugiro só ativar depois de um piloto de SAC.
4. **LGPD:** documentos e dados coletados por IA (SAC, Jurídico) precisam de aviso de consentimento e retenção; o projeto já tem o conceito de retenção de protocolo.
5. **Uma decisão sua para a Fase 1:** quer a identificação do agente já no piloto do MVP? Recomendo que sim.
6. **Uma decisão sua para a Fase 3:** aprova um spike de meio dia, com o número de teste, para confirmar a chamada do indicador de digitação antes de qualquer código de produto?

Fontes consultadas em 10/11/2026: [preços oficiais da Meta](https://developers.facebook.com/docs/whatsapp/pricing/), [explicação do WATI sobre as mudanças de outubro de 2026](https://support.wati.io/en/articles/16954666-whatsapp-business-platform-api-pricing-changes-service-messages-and-click-to-message-ads), [documentação de indicador de digitação via parceiros](https://docs.360dialog.com/partner/messaging/messages-statuses) e [Meta: enviar mensagens](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/send-messages).
