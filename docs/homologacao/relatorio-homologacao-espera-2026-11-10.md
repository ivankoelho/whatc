# Relatório da homologação manual da espera inteligente — 10/11/2026

**Ambiente:** organização isolada de desenvolvimento (banco `whatomate_dev_agia`), navegador do Claude, interface
em `127.0.0.1:3010`, binário dos cinco commits funcionais (HEAD `2a294af7`) e simuladores locais de WhatsApp
(Graph) e de IA. Nenhum cliente real, nenhuma credencial real, nada em produção. Horários em UTC, medidos no
simulador e no banco. Histórico da IA **desligado** (exceto onde dito), espera padrão de 5 s.

Os ganchos que deixam o binário falar com os simuladores existem só nesse binário local (worktree
descartável, removido). Não estão na branch.

## Resultado por cenário

| Pri. | Cenário | Evidência | Decisão |
|---|---|---|---|
| P0 | Espera de 5 s | Mensagem recebida 05:06:33.8; linha com `due_at` 05:06:38; indicador e chamada à IA 05:06:39.28; resposta 05:06:39.33 (assinada `*Atacadão dos Pisos - Assistente Virtual:*`). | **Aprovado** |
| P0 | Mensagens consecutivas | 3 mensagens (05:06:51, :53, :55); nada enviado 3 s depois da última; **uma** chamada à IA com `"Oi, boa tarde\nqueria ver pisos para minha casa\nde preferencia porcelanato"`; uma resposta às 05:07:01. | **Aprovado** |
| P0 | Histórico desligado | `ai_include_history = false`. O texto da chamada acima veio da tabela de mensagens. Cenário forçado: linha A+B com worker morto, junção recusada pelo banco (`reclaim_failed`), linha nova só com C → a IA recebeu `"A: quero porcelanato\nB: para a sala\nC: uns 30 m2"` e a extração de qualificação viu as três. | **Aprovado** |
| P0 | Reinício durante a espera | Espera de 20 s, 2 mensagens, serviço derrubado 05:07:20 e religado depois do prazo (05:07:34); resposta única 05:07:37, com as duas mensagens, `attempts = 1`. | **Aprovado** |
| P0 | Takeover humano | (a) Durante a espera (30 s): atendente enviou 05:08:14 (assinado `*Admin:*`); às 05:08:30 a linha fechou `human_active`, **0** chamadas à IA, 0 respostas. (b) Durante a geração (25 s): indicador iniciado 05:14:26.9 e renovado 05:14:36.9; atendente enviou 05:14:38.0; sem mais renovações; a IA terminou 05:14:51.9 e a resposta foi descartada (`not_sent`, 1 registro `ai_reply_discarded`, nenhuma mensagem da IA). (c) Cliente escreve depois: 0 chamadas, 0 envios, nenhuma linha nova. | **Aprovado** |
| P0 | Concorrência | Duas instâncias do serviço no mesmo banco, 8 clientes em paralelo (2 mensagens cada): 8 linhas `replied`, todas com `attempts = 1`; 8 chamadas à IA; exatamente 1 resposta por cliente; as instâncias dividiram o trabalho (7 e 1). | **Aprovado** |
| P1 | Mensagem durante a geração | `[lento]` (IA leva 25 s). D e E chegaram aos 12 s e 14 s da geração. A primeira chamada recebeu só `"Oi [lento]"`; a segunda, `"D: queria saber o prazo\nE: tem em 60x60?"`, logo que a primeira terminou (05:09:22.9). Uma resposta cada, nada repetido. | **Aprovado** |
| P1 | Falha de envio | (a) Recusa da Meta (`#131009`): outcome `send_failed`, mensagem `failed`, sem registro `ai_response` na sessão, 1 tentativa de envio, sem retry. (b) Conexão que cai após o pedido: outcome `ambiguous_send`, mesma coisa. A interface mostra os dois como falha em vermelho, sem tiques de entrega. | **Aprovado com ressalva** (ver defeitos 1 e 2) |
| P1 | Indicador | Início no momento da reivindicação, renovações a cada ~10–12 s enquanto a IA gera (05:08:56.9, 05:09:06.9, 05:09:18.9), só `type: text`, para a mensagem mais recente do cliente, parada em 05:09:21.98 quando a geração acabou; nenhum envio depois. Contadores gravados na linha (`typing_starts`, `typing_renewals`, 0 falhas). | **Aprovado** |
| P1 | Configuração por agente | Segundo agente criado pela tela com espera de 12 s e nome "Assistente B". Com ele como padrão: sem resposta aos 8 s, resposta aos 12,7 s assinada `*Assistente B:*`; o agente original manteve 5 s. Valores persistidos no banco; agente padrão trocado e restaurado pela tela. | **Aprovado** |

Resumo das linhas de espera da homologação: 16 `replied`, 2 `send_failed`, 1 `ambiguous_send`, 1 `human_active`,
1 `not_sent`; `attempts` máximo 1; 0 falhas do indicador; nenhuma linha ficou em `processing`.

## Defeitos e observações

1. **Mensagem não respondida por falha de envio não volta ao próximo turno (defeito, P1).** O limite inferior do
   texto do turno usa qualquer mensagem de saída, inclusive a que falhou. Medido: o cliente escreveu "Oi, boa
   noite" (resposta recusada, `send_failed`) e depois "quero ver porcelanato"; a IA recebeu **só** a segunda
   (`AI-IN "quero ver porcelanato"`). Eu havia afirmado o contrário em relatório anterior; a medição desmente.
   Com o histórico desligado, o que o cliente disse antes da falha deixa de ser considerado. Correção provável:
   ignorar no limite as saídas com `status = failed` (as de resultado desconhecido continuam excluídas pelo
   limite das linhas respondidas). Não corrigido: aguarda decisão.
2. **Falha e resultado desconhecido aparecem iguais (já registrado).** A interface mostra "falhou" também para
   `ambiguous_send`, e o texto do erro inclui a URL do pedido (`Post "http://…/v24.0/<phone_id>/messages": EOF`).
   Em produção seria o endereço da Graph API com o id do número. É comportamento anterior à espera, a ajustar
   antes de produção.
3. **Tela de agente (observação de usabilidade).** A lista do "Agente padrão" abre por cima do botão Salvar, e
   escolher uma opção não salva: é preciso clicar em Salvar. Nada a corrigir agora.
4. **Cache de configuração entre instâncias.** O cache de configurações do chatbot é por processo; uma mudança
   feita por uma instância pode demorar a valer na outra. Anterior à espera, sem efeito observado.

## Pendências para produção (inalteradas)
Transferência interrompida após a resposta; apresentação de `ambiguous_send`; resposta um turno atrasada;
política de retenção de `ai_pending_replies`; janela de 24 h e cobrança do indicador na Meta; `-race` com CGO.
