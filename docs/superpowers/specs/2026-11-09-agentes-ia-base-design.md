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
Tabela `ai_agents` por organização, com: nome, objetivo, instruções/comportamento, dados da empresa, tom, regras e situações de transferência, ativo. O **agente padrão** é um campo de `ChatbotSettings` (`default_ai_agent_id`). O agente carrega a **lista de números de WhatsApp onde pode atuar** (`whatsapp_accounts`); fora dela ele não atua (§5.8). Modelo, temperatura e tokens vêm de `ChatbotSettings.AI`. Sem agente padrão ativo, o caminho atual (`SystemPrompt` livre) fica **idêntico**.

### 5.2 Roteamento comercial por unidade **[decidido: respeitar a unidade; sem CEP]**
Verificado: a distribuição existente (`createTransferToTeam`) leva o atendimento a uma **equipe**, que pertence a uma **unidade**, e escolhe o atendente da equipe. O roteamento "por posição do contato" só funciona para colaboradores (`resolveContactPlacementTeam`), não para clientes. Por isso, **[minha escolha]**:
- (Rev. 3: a habilitação do número vem da lista `whatsapp_accounts` do agente, §5.8; a regra abaixo é a segunda condição.) A unidade de um atendimento do agente é a da **equipe padrão do número** (`WhatsAppAccount.DefaultTeamID`, que já existe e já "escopa" o número a uma equipe), e essa equipe precisa ter `UnitID`.
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
- **Valor, unidade e contexto (rev. 3):** a citação precisa corresponder ao **campo certo**, não só conter o valor. Metragem exige número com unidade de área (m², m2, metros quadrados); quantidade exige número com unidade de contagem/embalagem (caixas, cx, peças, pacotes, unidades, metros lineares). O servidor extrai **todas** as menções do tipo nas mensagens do cliente e descarta as **negadas ou corrigidas** (precedidas de "não", "nem", "em vez de", "ao invés de", "menos" em até 3 palavras, ou na forma "X, não Y": Y é negado). Se sobrar **exatamente um** valor distinto e ele for o devolvido pelo modelo, o campo é aceito; se sobrar mais de um, ou o valor do modelo não for o que sobrou, é **ambíguo** e o campo fica **não informado**. Exemplo: "40 m², não 20 m²" aceita 40 e rejeita 20; "40 m² ou 50 m²" fica não informado. Campos de texto (produto, categoria, ambiente) exigem o termo na citação e não negado. Quando a unidade não bate com o campo (ex.: 40 "caixas" como metragem), o campo é rejeitado.
- `Interest` recebe duas seções: "Informado pelo cliente" e "Ainda não coletado". **O resumo escrito pelo modelo não é guardado** (revisão independente): é texto livre sem validação, poderia carregar número ou promessa que o cliente nunca disse e iria direto ao vendedor. `EstimatedQuantity`/`UnitOfMeasure` só são preenchidos com quantidade validada e unidade reconhecida.
- **Endurecimento após a revisão independente da Tarefa 5:** a unidade da quantidade vem do que o cliente escreveu, nunca do modelo; "metros" sozinho não vale (em piso costuma ser m²); descartam-se áreas que são da embalagem ("caixa de 2,5 m²", "por caixa"), dimensões ("40x50"), limites ("até", "no máximo"), números de outra pessoa ("meu vizinho"), somas ("mais") e correções sem unidade ("20 m² não, 40", "errei, é 40"), que deixam o campo como não informado; a negação de produto, categoria e ambiente é procurada na mensagem inteira do cliente, antes e depois do termo ("porcelanato não, laminado"), com palavra inteira; a citação de um número precisa conter o próprio número; a intenção exige, na citação, palavras de compra ou de preço.
- **Dono da oportunidade:** a oportunidade aberta pelo agente nasce **sem dono** (a carteira do contato pode ser de outra unidade) e passa a quem assume o atendimento (envio, fila, atribuição manual), só se ainda não tiver dono e só a que esse atendimento abriu. Oportunidade que já existia não é tocada.
- Criação via `createOrRetriggerSalesOpportunity` (sem duplicar), fonte `system`, com `SourceTransferID` da transferência quando houver.
- Falha ou resposta inválida da extração: a oportunidade é criada **só com a pendência "qualificação não extraída"** (quando foi o próprio agente que decidiu transferir); a transferência segue. Nunca bloqueia o atendimento. Se a transferência para a equipe da unidade falhar, tenta a fila geral marcada; a qualificação roda uma vez por conversa.
- **Mensagem de espera:** o texto longo ("Já passei a ele o que conversamos") só vale para a transferência que o próprio agente escolheu; depois de teto ou falha do provedor, nada foi qualificado e o texto é o curto ("Vou chamar um atendente para continuar com você.").

### 5.5 Humano assume → agente cala **[decidido: pior cenário do Ivan]**
"Humano assumiu" = `AgentTransfer` ativo. `Contact.AssignedUserID` é carteira e não conta. O agente não volta sozinho. Auditoria dos caminhos de envio do atendente para garantir que todo envio cria o atendimento (`agent_initiated`); só corrige lacunas.

**Sem corrida (rev. 3) [decidido: o atendente tem precedência] + [minha escolha: o mecanismo]:** checar logo antes de enviar não basta, porque o atendimento pode ser confirmado entre a checagem e o envio. A proteção é uma **trava consultiva do Postgres por contato** (`pg_advisory_xact_lock` sobre uma chave derivada de organização + contato):
- (a) o envio de **toda resposta gerada pela IA** (resposta direta e nó `ai_response`) roda numa transação que adquire a trava, **reconfere** `AgentTransfer` ativo e só então envia, segurando a trava até o envio terminar;
- (b) **toda criação de atendimento ativo** adquire a mesma trava antes do INSERT (ponto único: `createTransferRow`, usado por `saveAndFinalizeTransfer`, que cobre fila, equipe, palavra-chave, `agent_initiated` e o próprio handoff).
- Consequência: ou o atendimento é confirmado antes da checagem (o agente não envia), ou o envio do agente termina antes de o atendimento ser confirmado. **Nenhuma mensagem do agente sai depois da confirmação do atendimento.**
- Falha segura: o agente espera a trava por no máximo 3 s (`lock_timeout`); sem a trava, **descarta** a resposta. O atendente espera o fim do envio em curso (milissegundos a poucos segundos).
- A trava cobre só o envio. Nunca é mantida durante a geração, a extração nem a transferência do próprio handoff (evita deadlock); a mensagem de espera do handoff só sai depois de `TransferCreated` e não precisa dela.
- **Revisão independente da Tarefa 3 (incorporada):** (a) cada envio do agente segura uma conexão do banco enquanto o callback usa outras; por isso o número de envios do agente em paralelo é limitado por um semáforo (6, ou um quarto do pool) e quem não consegue vaga em 5 s descarta (`discarded_busy`); (b) `createTransferRow` espera a trava por no máximo 30 s e falha em vez de travar; (c) o nó `ai_response` que descarta a resposta **para o fluxo** (`halt`) sem salvar a sessão, para não reabrir a sessão que o atendimento encerrou nem deixar um nó seguinte falar por cima do humano; (d) a resposta padrão (fallback) enviada depois de um turno do agente também passa pela trava; (e) erro ao confirmar a transação depois de um envio bem-sucedido conta como enviado.
- **Limites conhecidos:** "enviado" quer dizer aceito pela API da Meta; a entrega ao aparelho é assíncrona. Mensagens de confirmação de ferramentas (`aitools`: `request_agent_transfer` e afins) ficam **fora do MVP**: o agente não usa `aitools`, que continua desligado por padrão, e esses caminhos não passam pela trava. Contatos duplicados do mesmo cliente (BSUID × telefone) teriam travas diferentes. Duas respostas do agente ao mesmo contato se serializam; a segunda é descartada se esperar mais de 5 s.

### 5.6 Falhas, mensagem de espera e duplicidade **[decidido]** + **[minha escolha]**
- A mensagem de espera ("Vou chamar um atendente…", texto fixo) só é enviada quando a transferência **resulta em `TransferCreated`**. `AlreadyActive` (índice único) não envia nada. Isso impede mensagem e transferência duplicadas, mesmo com mensagens simultâneas.
- Falha do provedor de IA (erro, limite, timeout): **nenhuma** mensagem de espera; vale o comportamento atual (resposta padrão do chatbot, se houver). Duas falhas seguidas na mesma sessão disparam uma transferência (uma vez, pelo mesmo caminho acima).
- Falha de IA nunca altera atendimento humano em curso: com transferência ativa o chatbot nem processa a mensagem.
- Fora do expediente: vale a regra existente (mensagem de fora de horário), uma vez por sessão (sinalizador na sessão), e a oportunidade ainda é registrada.

### 5.7 Teto mensal de tokens **[decidido: mantém no MVP]** + **[minha escolha]**
Campo `ai_monthly_token_limit` em `ChatbotSettings` (0 = sem teto), por organização, mês civil em `America/Sao_Paulo`.
- Consumo = soma de `TotalTokens` do `AIUsageLog` das features `chatbot_reply`, `chatbot_flow_node` e `agent_qualification`, **mais reservas em voo**.
- **Sem ultrapassagem silenciosa em chamadas simultâneas:** antes de cada chamada, uma transação com `pg_advisory_xact_lock` por organização soma o consumo, e, se `consumo + reserva ≤ teto`, insere uma linha de reserva no próprio `AIUsageLog` (feature `ai_reservation`; reserva = `MaxTokens` + estimativa da entrada).
- **Reserva de chamada viva nunca expira (rev. 3):** `completeAI` impõe um prazo máximo por chamada de agente (`aiCallDeadline` = 2 min, cobrindo o laço de ferramentas), e uma reserva só é considerada abandonada após 10 min. Como o prazo é muito menor que a validade, **uma chamada em andamento nunca perde a reserva**; só uma reserva de processo que caiu expira. Chamada que estoura o prazo é cancelada e segue a regra de término abaixo.
- **Registro antes da liberação, sem janela zerada (rev. 3):** ao fim da chamada (sucesso, erro ou cancelamento) as linhas reais de uso são gravadas **primeiro**; só depois a reserva é fechada (`ai_reservation_closed`, 0 tokens). Entre as duas gravações o consumo conta duas vezes (conservador, intencional); nunca zero. Se a gravação do uso real falhar, a reserva **vira consumo** (`ai_reservation_unreconciled`, valor reservado, continua somando): superestima, nunca subestima.
- Teto atingido: o agente não chama o provedor; transfere pelo caminho 5.6 com nota "limite de IA atingido"; sem extração (não gasta mais); o admin vê o aviso no mecanismo existente. Nada de erro técnico ao cliente.

### 5.8 Isolamento do piloto (rev. 3)
**Verificado no código:** `ChatbotSettings` é, na prática, **uma linha por organização**. `GET/PUT /api/chatbot/settings` só leem e gravam a linha padrão (`whats_app_account = ''`). O leitor `getChatbotSettingsCached` preferiria uma linha por número, mas **nada cria essa linha**. Portanto `default_ai_agent_id` vale para a organização inteira e **não** limita um piloto.

**[minha escolha] Lista explícita e fail-closed:** o agente tem `whatsapp_accounts` (nomes dos números permitidos). `resolveAIAgent` só devolve o agente quando o número **está na lista** e a equipe padrão dele está ativa e ligada a uma unidade (§5.2). Lista vazia = o agente não atua em número nenhum. Piloto = um agente com **um único número** na lista. Demais números da organização, mesmo com equipe e unidade, seguem como hoje, nos dois caminhos (resposta direta e nó `ai_response`). Nada muda no comportamento do chatbot atual (`SystemPrompt` livre) em nenhum número.

**Pré-condição do piloto (descoberta na construção):** a chave "chatbot ativado" (`ChatbotSettings.IsEnabled`) também é **por organização**. Com ela desligada, toda mensagem de todos os números vai direto para a fila de atendentes (`TransferSourceChatbotDisabled`). O agente só responde com o chatbot ligado, e ligar o chatbot passa a valer para os **demais números** da organização (saudação, palavras-chave, resposta padrão, IA com o prompt livre). Se a organização do piloto tem o chatbot desligado em produção hoje, ligá-lo para o piloto muda o comportamento dos outros números. Isso precisa ser decidido antes do piloto (alternativas fora do MVP: chave de chatbot por número).

## 6. Interface mínima **[decidido]**
Uma tela dentro de Chatbot → IA: lista de agentes com formulário em diálogo (nome, objetivo, instruções, empresa, tom, regras de transferência, **números permitidos**, ativo), seletor de agente padrão e campo do teto mensal, mais o aviso, por número permitido, de equipe de unidade ausente. PT-BR e EN. Permissão: a de configurações do chatbot.

## 7. Restrições
Isolamento por organização em tudo; chave de IA nunca em log, API ou cache em claro; migração aditiva e idempotente; sem mudança de comportamento para quem não usa agente; todo uso de IA via `completeAI`; sem push, merge ou deploy sem o Ivan.

## 8. Critérios de liberação (os 10 do Ivan) e prova
1. Cliente conversa com o agente pelo WhatsApp. 2. Responde dentro do contexto configurado. 3. Qualifica intenção de compra. 4. Registra oportunidade com os dados. 5. Transfere ao vendedor da unidade. 6. Não responde após o humano assumir. 7. Não expõe dados de outro cliente/organização. 8. Não envia marcador técnico nem mensagem indevida. 9. Controle de consumo e tratamento de falhas. 10. Validado em homologação antes da produção.

**Prova obrigatória:** um teste de ponta a ponta automatizado (§plano, Tarefa 6) e o mesmo roteiro executado à mão em homologação com um número de WhatsApp de teste, que serve de demonstração à diretoria.

## 9. Deixado para quem constrói
Nomes de rotas e colunas; formato exato do JSON de extração além dos campos de §5.4; layout das telas; estimativa de tokens de entrada; texto das regras fixas da plataforma (a partir de §5.3).
