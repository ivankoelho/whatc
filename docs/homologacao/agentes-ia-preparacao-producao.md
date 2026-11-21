# Agente de IA: preparação para homologação real e produção

Branch `feature/agente-ia-producao` (sobre `feature/agente-ia-base` / PR #154, base atualizada com `main` em
`ccd26e43`). Esta etapa corrige os bloqueios técnicos e de segurança; **não** é homologação real nem autoriza
merge ou deploy. Testes aqui usam simuladores de WhatsApp e de IA, nunca Meta nem provedor reais.

## O que mudou

| Item | Problema | Solução | Onde |
|---|---|---|---|
| **P1-01** handoff durável | A despedida chegava ao cliente e o processo podia cair antes de a transferência existir: ninguém assumia. | A promessa é gravada em `ai_handoff_intents` **antes** de a despedida sair. `recoverAIHandoffs` (mesmo laço do worker, qualquer instância) conclui o que ficou pendente depois que a posse (lease) de 3 min vence. Idempotente (um intent pendente por contato; um atendimento ativo por contato), limitado (3 tentativas, depois `abandoned` com log de erro) e à prova de falha (se o intent não puder ser gravado, a despedida **não** é enviada: transfere primeiro). | `ai_handoff_intent.go`, `ai_handoff.go`, `ai_turn.go`, `ai_wait.go` |
| **SEC-1** cliente HTTP | A URL da requisição (com `client_secret` e `code` na troca OAuth, e o id do número em todas) ia para o texto dos erros; o token da Meta podia ir a um host de download não confiável e redirecionamentos eram seguidos. | Erros de transporte sem URL e com segredos mascarados (a classificação "não enviado" é mantida). Downloads com credencial só para hosts da Meta, verificados antes e a cada redirecionamento; chamadas à API nunca seguem redirecionamento. | `pkg/whatsapp/safe_http.go` |
| **B3** `ambiguous_send` | Resultado desconhecido aparecia como "falhou" e com a URL da Graph API. | Sem prova de recusa, o atendente lê: "Não foi possível confirmar o envio: a mensagem pode ter chegado ao cliente. Confira a conversa antes de reenviar." Recusa comprovada mostra o motivo público da Meta. A causa técnica fica só no log; nada é reenviado sozinho. | `messages.go` |
| **B4** isolamento | Ligar o chatbot valia para todos os números da organização. | `ai_agent_only` (padrão desligado): o chatbot roda só onde há agente autorizado; os demais números se comportam como com o chatbot desligado (fila humana). | `chatbot.go`, `chatbot_processor.go` |
| **B5** teto | Era possível ativar o agente sem teto, sem querer. | Ativar o agente exige um teto mensal **ou** o aceite explícito de ficar sem (`ai_unlimited_ack`, padrão falso). Tela mostra o consumo do mês. | `ai_token_cap.go`, `ai_agents.go`, tela de agentes |
| Lint | 3 `errcheck` em `recordAIUsage` | chamadas explícitas | `ai_runtime.go`, `ai_models.go` |

## P1-01: o que cada queda deixa para trás

| # | Momento | O cliente leu a despedida? | Estado ao cair | O que a recuperação faz |
|---|---|---|---|---|
| 0 | antes de gravar o intent | não | nada | nada (nada foi prometido) |
| 1 | intent gravado, despedida não enviada | não | intent pendente | transfere e avisa o cliente uma vez, depois que o atendimento existe |
| 2 | despedida enviada, transferência não iniciada (o P1-01) | sim | intent pendente | cria o atendimento |
| 3 | qualificação e oportunidade feitas, sem transferência | sim | intent pendente, 1 oportunidade | cria o atendimento; sem segunda extração nem segunda oportunidade |
| 4 | atendimento criado, aviso de espera não enviado | sim | atendimento ativo | encontra o atendimento e não duplica nada |
| 5 | tudo feito, intent não fechado | sim | atendimento ativo | fecha o intent |

Concorrência coberta: 4 recuperações simultâneas, com e sem um atendente humano abrindo a conversa ao mesmo tempo
(um atendimento ativo, no máximo uma mensagem de espera); atendente que assume antes da recuperação (a recuperação
termina em silêncio); intent com posse viva não é tomado.

**Limite honesto:** as quedas são simuladas dentro do processo (panic em pontos nomeados, sem nenhuma limpeza que
uma queda real também não pularia: a recuperação só lê o banco). Não é prova de `Process.Kill()`. O teste
multiprocesso de caracterização (`test/ai-crash-characterization`, Etapa 1/PR 1) continua suspenso.

## B4: matriz de isolamento

| Cenário | Resultado esperado | Teste |
|---|---|---|
| número do piloto, `agent_only` desligado ou ligado | o agente responde, assinado | `TestAgentOnly_Matrix_*` |
| outro número da mesma organização, `agent_only` desligado | comportamento anterior (IA livre da organização), **não** é o agente | idem |
| outro número, `agent_only` ligado | nenhuma chamada de IA, nada dito ao cliente, vai para a fila humana | idem |
| outra organização aponta para o agente desta | nunca resolve; número de outra organização também não | `TestAgentOnly_AnotherOrganizationNeverUsesThisOnesAgent`, `TestResolveAIAgent` |
| API: ler/alterar/apagar agente de outra organização | 404 | `TestAIAgents_OrgIsolation` |
| API: agente apontado para número de outra organização | 400 | `TestAIAgents_RejectsForeignAccountName` |
| API: agente padrão de outra organização | 400 | `TestAICap_DefaultAgentOfAnotherOrganizationIsRefused` |
| padrões | `agent_only=false`, `unlimited_ack=false` | `TestAgentOnly_DefaultsOff` |

## B5: o teto (decisão sua, nenhum valor foi escolhido)

O consumo é contado pelo banco, com trava consultiva por organização e reservas em voo, então vale entre várias
instâncias (testes de concorrência em `ai_token_cap_test.go`). Ao atingir o teto o agente não chama o provedor e
transfere com a nota "limite de IA atingido".

Opções para o valor e a política (a escolher):

1. **Teto obrigatório** (o aceite deixa de existir): mais seguro; a organização sempre decide um número.
2. **Teto ou aceite explícito** (como está): quem quiser ilimitado tem de dizer isso conscientemente.
3. **Valor inicial sugerido pelo sistema** (ex.: um piso por organização): evita o primeiro passo em branco, mas é um valor comercial.

Quanto ao valor em tokens: depende do preço do provedor escolhido e do volume do piloto (o roteiro de homologação
usa 200.000 como exemplo de teste, não como recomendação).

## Itens que continuam abertos

- **Homologação real** (WhatsApp e IA reais, roteiro de 10 critérios): depende dos insumos listados em
  `agentes-ia-preparacao-real.md`.
- **Janela de 24 h e cobrança do indicador de digitação na Meta:** sem prova.
- **Retenção de `ai_pending_replies` e de `ai_handoff_intents`:** sem política (não implementada de propósito).
- **`-race`:** sem `gcc` nesta máquina; roda na CI Linux.
- **E2E (Playwright):** não executado aqui.
- **Etapa 1 de endurecimento** (`integration/ai-agents-etapa1`, PRs 1–7): suspensa.
- **Lista de hosts de download da Meta** (`facebook.com`, `fbsbx.com`, `fbcdn.net`, `whatsapp.net`,
  `whatsapp.com`): se a Meta passar a servir mídia de outro domínio, o download falha fechado (erro claro) até a
  lista ser atualizada; confirmar na homologação real com mídia de verdade.
