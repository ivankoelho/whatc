# Chamados — Etapa 4: fluxo SAC → Logística

Complementa a [etapa 2](2026-10-25-chamados-etapa2-estados-isolamento.md).

## O fluxo

1. **SAC cria** `POST /api/occurrences` com `contact_id`, `team_id` (equipe de destino), `order_ref`, `description`, `priority`, `category_id` e, se tiver acesso à conversa, `source_transfer_id`. Seleção de equipe: `GET /api/occurrences/forward-targets` (nomes de equipe e loja, sem membros).
2. O chamado nasce **sem responsável**, com equipe e unidade da equipe de destino, estado `new`, na etapa inicial da equipe (ou da organização).
3. Na mesma transação são gravados: evento `forwarded`, o solicitante como **participante** (`view` + `comment`) e a notificação ao cliente **preparada** (não enviada).
4. **Logística** da loja vê o card em "Novo" e **assume** (`POST /claim`, o mesmo endpoint atômico) — só então move etapas.
5. **SAC** acompanha (vê, comenta nas anotações internas) mas não altera etapa, não assume, não responde ao cliente.
6. **Supervisor de loja** intervém: move etapas, redistribui, convoca participantes e concede capacidades.
7. Logística conclui; o SAC vê `resolved`. Reabertura conta em `reopened_count` e gera evento `reopened`.

## Criação separada da conversa (A3)

Quem **trabalha a conversa** e quem **solicita algo a uma equipe** são direitos distintos. Sem acesso à conversa (modo estrito), o SAC ainda pode abrir um chamado **encaminhado** a uma equipe se tiver `occurrences:write` e `contacts:read`. Isso:

- não dá acesso ao histórico, nem altera a atribuição da conversa;
- não permite citar como origem uma conversa que o usuário não vê (`source_transfer_id` → 403);
- valida organização do contato, equipe ativa da mesma organização, unidade coerente com a equipe e que a conversa citada seja do mesmo contato.

Limite conhecido: a **busca de contato** da tela ainda respeita a visibilidade estrita de conversas; para um contato cuja conversa é de outro agente, o SAC precisa chegar ao contato por outro caminho (ex.: o painel da conversa que ele atende). O endpoint já aceita o `contact_id`.

## Participantes e capacidades (B3)

`occurrence_participants.capabilities` ⊆ {`comment`, `change_status`, `claim`, `reassign`, `resolve`}; ver é implícito.

| Ação | Quem pode |
|---|---|
| Comentar (anotação interna) | equipe/responsável/supervisor, ou participante com `comment` |
| Assumir | equipe/supervisor, ou participante com `claim` (só se sem responsável) |
| Mudar etapa | responsável ou supervisor; participante com `change_status` |
| Concluir / reabrir | responsável ou supervisor; participante com `resolve` |
| Redistribuir (`POST /reassign`) | supervisor, responsável atual, ou participante com `reassign`; o novo responsável precisa ser da equipe (supervisor pode qualquer um da organização) |
| Convidar participante | quem trabalha o caso; só `comment` — qualquer outra capacidade é do supervisor |
| Responder ao cliente / editar | só quem trabalha o caso (nunca participante) |

A tela de detalhe recebe `can{}` do servidor e esconde o que o usuário não pode usar (o servidor continua sendo a autoridade).

A discussão interna reutiliza as **anotações internas** do chamado (eventos `note`, em tempo real só para quem tem acesso). **Pendente:** vincular o chat interno (`team_chat`) ao chamado.

## Histórico (B4)

Eventos: `opened`, `forwarded`, `assignment` (assumir e redistribuir, com de/para), `stage_change`, `participant_added`, `participant_removed`, `note`, `closed`, `reopened`, `scope_regularized`. A conversa de origem fica como `source_transfer_id` (referência, nenhuma mensagem é copiada).

## Conta de WhatsApp da Logística (B5)

- `occurrence_channel_configs`: conta por **unidade** ou padrão da **organização** (`GET/PUT /api/occurrence-channel-config`, permissões de contas). Sem número fixo no código.
- `occurrence_notifications`: um registro por chamado e tipo (`occ:<id>:created`, único), com conta resolvida, `status` (`prepared` | `skipped`), `reason` e `eligible`. **Nada é enviado nesta fase** (`eligible=false`, `dispatch_not_enabled`).
- Próxima fase: um despachante lê `prepared`, verifica template aprovado e consentimento, envia de forma assíncrona e atualiza o registro. A chave de idempotência já impede duplicidade.

## Auditoria de chamados históricos (A2)

`GET /api/occurrences/scope-audit` (somente `conversations:view_all`): resumo (total, sem equipe, sem unidade, restritos, com responsável, ligados a conversa, determináveis) e fila de regularização com **sugestão apenas quando há evidência**: unidade da equipe, equipe do atendimento de origem, ou par unidade+departamento com **uma única** equipe. `POST /scope-audit/apply` aplica as sugestões; `PUT /occurrences/{id}/scope` define manualmente (motivo obrigatório). Tudo gera evento `scope_regularized` e log de auditoria, e nunca sobrescreve um escopo já definido. Sem evidência, o chamado permanece restrito.

## Dashboard (A1)

Contadores, gráficos, agrupamentos e tabela dos widgets da fonte `occurrences` usam a mesma regra de visibilidade da lista e do Kanban (`visibleOccurrences`); sem usuário, não retornam nada. Não existe exportação nem drill-down de ocorrências além disso.

## Fora desta entrega

Envio automático de template, integração SULTS/GLPI, `ContactStatus` "Aguardando cliente" no chat, vínculo do chat interno ao chamado, tela de fila de regularização (a API existe), busca de contato entre conversas de outros agentes.
