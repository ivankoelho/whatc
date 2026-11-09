# Agentes de IA — MVP (design, rev. 2)

Data: 2026-11-09. Base: `origin/development` @ `217f48b3`. Branch local: `feature/agente-ia-base` (sem push).

> Rev. 2 reduz o escopo da rev. 1 a um **MVP demonstrável à diretoria e liberável em piloto**: um agente de IA que atende no WhatsApp, qualifica a intenção de compra, registra a oportunidade no CRM, transfere para a equipe da unidade e cala quando um humano assume. Tudo que não serve a esse caminho foi para a Fase 2 (§4).
>
> Marcação: **[decidido]** = dito pelo Ivan; **[minha escolha]** = decisão minha, sujeita a revisão. Contexto: iniciativa "levar o melhor do DeskcommCRM (MIT, `melgarafael/DeskcommCRM`) para o WhATC".

## 1. Intenção

O chatbot do WhATC já responde com IA, mas com um prompt livre. O MVP dá a ele a forma de **agente**: instruções estruturadas, qualificação de venda, registro no CRM, transferência à unidade certa, desligamento imediato quando um humano assume e controle de consumo. A estrutura existente é a prioridade: nada de módulo paralelo. **[decidido]**

## 2. O que o agente faz (Fase 1) **[decidido]**

Recebe mensagens do WhatsApp; responde com IA mantendo o contexto; identifica a necessidade; pergunta para qualificar (produto, categoria, quantidade, metragem, ambiente e intenção de compra, conforme aplicável); registra o que coletou numa oportunidade do CRM; transfere ao vendedor/equipe da unidade com o histórico; para de responder quando um humano assume; registra consumo de IA e histórico das ações.

## 3. Reaproveitado (verificado em `origin/development`)

- Chatbot, nó `ai_response`, `generateAIReply` (`chatbot_processor.go`), `completeAI` (única porta de IA) e `AIUsageLog` (`ai_usage.go`).
- Provedores OpenAI, Anthropic, Google e Groq; chave **por organização**, criptografada (`resolveAIAPIKey`).
- `AIContext` (contextos de IA), `internal/knowledge` (busca textual).
- Oportunidades: `createOrRetriggerSalesOpportunity` (`sales_opportunities.go:38`) cria ou reabre sem duplicar; campos `Interest` (texto), `EstimatedQuantity` e `UnitOfMeasure` (UN, M, M2, KG, PCT, CX). Produto, categoria, ambiente e intenção cabem em `Interest`; **sem migração no CRM**.
- Transferência: `transferToQueue` (devolve `TransferResult` com `Outcome`), índice único `idx_agent_transfers_one_active_per_contact` (um atendimento ativo por contato), `hasActiveAgentTransfer`, `createTransferToTeam`, `Assigner.AssignToTeam`, `TransferSourceAgentInitiated`.
- Equipes (`Team.UnitID`), unidades (`Unit`), `WhatsAppAccount.DefaultTeamID`, permissões do chatbot, isolamento por organização.

## 4. Fora do MVP (Fase 2) **[decidido]**

Integração avançada com o Deskcomm; agentes por departamento; agente por nó de fluxo ou por conta; consulta de produtos e estoque (X2, catálogo); pedidos; multiagente; memória de longo prazo; autonomia avançada; analytics de agentes; construtor visual; roteamento por CEP ou região; copiloto do atendente. A arquitetura só **não impede** esses itens: a tabela de agentes aceita vários, e a montagem do prompt é um ponto único onde ferramentas entrarão.

## 5. Decisões

### 5.1 Agente **[decidido]** + **[minha escolha]**
Tabela `ai_agents` por organização, com: nome, objetivo, instruções/comportamento, dados da empresa, tom, regras e situações de transferência, ativo. O **agente padrão** é um campo de `ChatbotSettings` (`default_ai_agent_id`) e vale para a conta daquela linha. Modelo, temperatura e tokens vêm de `ChatbotSettings.AI`. Sem agente padrão ativo, o caminho atual (`SystemPrompt` livre) fica **idêntico**.

### 5.2 Roteamento comercial por unidade **[decidido: respeitar a unidade; sem CEP]**
Verificado: a distribuição existente (`createTransferToTeam`) leva o atendimento a uma **equipe**, que pertence a uma **unidade**, e escolhe o atendente da equipe. O roteamento "por posição do contato" só funciona para colaboradores (`resolveContactPlacementTeam`), não para clientes. Por isso, **[minha escolha]**:
- A unidade de um atendimento do agente é a da **equipe padrão do número** (`WhatsAppAccount.DefaultTeamID`, que já existe e já "escopa" o número a uma equipe), e essa equipe precisa ter `UnitID`.
- **O agente só atua em número cuja equipe padrão esteja ativa e vinculada a uma unidade.** Sem isso, `resolveAIAgent` devolve nada e o número se comporta como hoje. A tela avisa quais números ficaram de fora. Assim, oportunidade do agente nunca nasce sem unidade responsável.
- Se a equipe for desativada no meio do caminho, a transferência cai na fila geral, **marcada** com o motivo de roteamento `ai_no_unit_team` e registrada em log. O cliente nunca fica sem atendimento.
- Cada unidade com número próprio é o cenário coberto. Um único número atendendo várias unidades exigiria saber a unidade do cliente (CEP/região): Fase 2.
- Pontos a **verificar na construção**: se a oportunidade chega ao vendedor certo (hoje `assignOpenSalesOpportunityToAgent` depende de `AssignToSameAgent`) e que transferir fora do expediente produza o aviso de fora de horário existente, sem duplicar.

### 5.3 Segurança comercial **[decidido]**
Sem fonte confiável de preço e estoque, o agente **não informa valor, desconto, disponibilidade nem prazo como confirmados**; qualifica e encaminha essas dúvidas ao vendedor.
- Regras fixas da plataforma no prompt (não editáveis pela organização).
- Filtro de saída **[minha escolha]**: bloqueia compromissos ("garanto", "prometo", "reservei"…, exceto negados como "não garanto"), valor em reais que não conste do texto configurado do agente, percentual de desconto, afirmação de disponibilidade/estoque e prazo em dias. Resposta bloqueada **não é enviada**: uma nova tentativa com instrução corretiva ("reescreva sem informar preço, estoque ou prazo; diga que o vendedor confirma"); se bloquear de novo, transfere. Teto conhecido: heurística; falso positivo custa uma regeneração.

### 5.4 Qualificação e oportunidade **[decidido: chamada extra aprovada]**
Quando o agente decide transferir (intenção de compra qualificada, pedido de humano ou regra de transferência), o servidor faz **uma chamada extra de IA** sobre o histórico, pedindo JSON com os campos produto, categoria, quantidade, unidade, metragem, ambiente, intenção e resumo. **[minha escolha]** para não inventar:
- Cada campo é `{valor, evidencia}` ou nulo. `evidencia` é uma **citação literal** de mensagem **do cliente**; o servidor confere que a citação existe nas mensagens do cliente. Sem citação válida (ou valor numérico ausente da citação), o campo é tratado como **não informado**.
- `Interest` recebe duas seções: "Informado pelo cliente" e "Ainda não coletado", mais o resumo. `EstimatedQuantity`/`UnitOfMeasure` só são preenchidos com quantidade validada e unidade reconhecida.
- Criação via `createOrRetriggerSalesOpportunity` (sem duplicar), fonte `system`, com `SourceTransferID` da transferência quando houver.
- Falha ou resposta inválida da extração: a oportunidade é criada **só com o resumo e a pendência "qualificação não extraída"**; a transferência segue. Nunca bloqueia o atendimento.

### 5.5 Humano assume → agente cala **[decidido: pior cenário do Ivan]**
"Humano assumiu" = `AgentTransfer` ativo. `Contact.AssignedUserID` é carteira e não conta. Um helper `humanTookOver` é chamado **imediatamente antes de cada envio do agente**, nos dois caminhos (resposta direta e nó `ai_response`), e a resposta é descartada com registro. Auditoria dos caminhos de envio do atendente para garantir que todo envio cria o atendimento (`agent_initiated`); só corrige lacunas. Corrida residual de milissegundos antes da chamada ao WhatsApp é aceita. O agente não volta sozinho.

### 5.6 Falhas, mensagem de espera e duplicidade **[decidido]** + **[minha escolha]**
- A mensagem de espera ("Vou chamar um atendente…", texto fixo) só é enviada quando a transferência **resulta em `TransferCreated`**. `AlreadyActive` (índice único) não envia nada. Isso impede mensagem e transferência duplicadas, mesmo com mensagens simultâneas.
- Falha do provedor de IA (erro, limite, timeout): **nenhuma** mensagem de espera; vale o comportamento atual (resposta padrão do chatbot, se houver). Duas falhas seguidas na mesma sessão disparam uma transferência (uma vez, pelo mesmo caminho acima).
- Falha de IA nunca altera atendimento humano em curso: com transferência ativa o chatbot nem processa a mensagem.
- Fora do expediente: vale a regra existente (mensagem de fora de horário), uma vez por sessão (sinalizador na sessão), e a oportunidade ainda é registrada.

### 5.7 Teto mensal de tokens **[decidido: mantém no MVP]** + **[minha escolha]**
Campo `ai_monthly_token_limit` em `ChatbotSettings` (0 = sem teto), por organização, mês civil em `America/Sao_Paulo`.
- Consumo = soma de `TotalTokens` do `AIUsageLog` das features `chatbot_reply`, `chatbot_flow_node` e `agent_qualification`, **mais reservas em voo**.
- **Sem ultrapassagem silenciosa em chamadas simultâneas:** antes de cada chamada, uma transação com `pg_advisory_xact_lock` por organização soma o consumo, e, se `consumo + reserva ≤ teto`, insere uma linha de reserva no próprio `AIUsageLog` (feature `ai_reservation`; reserva = `MaxTokens` + estimativa da entrada). Ao terminar a chamada, a reserva é removida e vale a linha real. Reservas com mais de 10 minutos são ignoradas (autocorreção se o processo cair).
- Teto atingido: o agente não chama o provedor; transfere pelo caminho 5.6 com nota "limite de IA atingido"; sem extração (não gasta mais); o admin vê o aviso no mecanismo existente. Nada de erro técnico ao cliente.

## 6. Interface mínima **[decidido]**
Uma tela dentro de Chatbot → IA: lista de agentes com formulário em diálogo (nome, objetivo, instruções, empresa, tom, regras de transferência, ativo), seletor de agente padrão e campo do teto mensal, mais o aviso dos números sem equipe de unidade. PT-BR e EN. Permissão: a de configurações do chatbot.

## 7. Restrições
Isolamento por organização em tudo; chave de IA nunca em log, API ou cache em claro; migração aditiva e idempotente; sem mudança de comportamento para quem não usa agente; todo uso de IA via `completeAI`; sem push, merge ou deploy sem o Ivan.

## 8. Critérios de liberação (os 10 do Ivan) e prova
1. Cliente conversa com o agente pelo WhatsApp. 2. Responde dentro do contexto configurado. 3. Qualifica intenção de compra. 4. Registra oportunidade com os dados. 5. Transfere ao vendedor da unidade. 6. Não responde após o humano assumir. 7. Não expõe dados de outro cliente/organização. 8. Não envia marcador técnico nem mensagem indevida. 9. Controle de consumo e tratamento de falhas. 10. Validado em homologação antes da produção.

**Prova obrigatória:** um teste de ponta a ponta automatizado (§plano, Tarefa 6) e o mesmo roteiro executado à mão em homologação com um número de WhatsApp de teste, que serve de demonstração à diretoria.

## 9. Deixado para quem constrói
Nomes de rotas e colunas; formato exato do JSON de extração além dos campos de §5.4; layout das telas; estimativa de tokens de entrada; texto das regras fixas da plataforma (a partir de §5.3).
