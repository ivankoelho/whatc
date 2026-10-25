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

## Testes intermitentes — investigação (A4)

**Causa confirmada para a maior parte: duas execuções de teste na mesma base.** `TEST_DATABASE_URL` aponta por padrão para `whatomate_test` e `TEST_REDIS_URL` para o Redis `/0`. Outra sessão de trabalho roda `go test ./... -p 1` e `go test -race ./internal/handlers/...` em um contêiner (`golang:1.26`, `run.sh`) contra **a mesma base e o mesmo Redis** (`host.docker.internal:5432/whatomate_test`, `redis://…/0`).

Evidências:
- Com minha suíte parada, `pg_stat_activity` mostrava conexões **ativas** (`commit`) em `whatomate_test` vindas de outro processo, e `docker exec … ps` mostrava o `handlers.test` do outro contêiner.
- Suíte completa em `whatomate_test` durante a execução concorrente: **283 falhas**, com `deadlock detected (SQLSTATE 40P01)` e violações de FK `fk_contacts_messages` (linhas apagadas por outro processo no meio do teste) e estouro do timeout de 10 min.
- A mesma suíte em base e Redis privados (`whatomate_test_profiles`, `redis://…/5`): **0 deadlocks e só 4 falhas** (abaixo). O pacote `internal/usage` passou (45 s). `Sweep` varre mensagens de **todas** as organizações, então linhas de outra execução entram na conta; `TestSweep_ConcurrentPassesNeverDuplicate` e `TestRecord_UnitAttribution…` não reproduziram em 15 execuções isoladas nem com 24 laços de CPU: coincidem com execuções concorrentes na base compartilhada (correlação forte, sem reprodução controlada do defeito).

**Falha que persiste sob carga, por desenho do teste:** `TestPhaseA_Concurrency_NoDeadlockUnderMixedWrites` exige mais de 100 operações numa janela fixa ("the storm actually ran"); com o contêiner a 100 % de CPU houve 92. É um limite de vazão dependente da máquina, não um deadlock (nenhum foi detectado). Passa isolada e com a máquina livre. Recomendação: trocar o piso fixo por um mínimo pequeno ou por contagem de operações, em outra mudança.

**Falhas identificadas separadamente, sem relação com esta entrega:**
- `TestServeLoginBackground_RejectsSymlink` e `TestApp_ServeMedia_RejectsSymlink`: criação de symlink exige privilégio no Windows (falham também no commit base `8ea43948`).
- `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`: falha em `development` limpo neste ambiente (registrada antes).

**Como evitar:** cada sessão usa a sua base e o seu Redis, por exemplo
`TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/whatomate_test_<sessao>?sslmode=disable` e `TEST_REDIS_URL=redis://localhost:6379/<n>` (o contêiner `whatomate_test_pg` aceita `CREATE DATABASE`). O servidor de desenvolvimento usa o Redis `/0`: não compartilhe com os testes.
