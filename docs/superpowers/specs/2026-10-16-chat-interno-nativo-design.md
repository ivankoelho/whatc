# Chat interno nativo (`team_chat`) — design do MVP

Status: especificação para revisão (2026-10-16). Nenhum código de produção.
Substitui a proposta com Mattermost (descartada; ver `docs/research/2026-10-14-opensource-ambiente-comunicacao.md`).

## Decisões
- Chat nativo no Whatc, sem serviço externo, sem segundo banco, sem identidade separada: o autor é `users`, a org é `organizations`, os times são `teams`.
- Escopo do MVP: canais de time, canais livres (privados), DMs, mensagens com edição e exclusão, não lidas, tempo real, presença reaproveitada, "digitando".
- Fora do MVP: threads, reações, anexos, busca, grupos, contexto de atendimento.
- Retenção: **sem expurgo automático**. Soft delete na mensagem; usuário desativado mantém o histórico. Política formal é decisão de produto, posterior.
- Redis: não necessário no MVP. O hub WebSocket é em memória: uma instância só.
- Permissão nova `team_chat`, separada de `chat` (que é o atendimento WhatsApp; a rota `/chat` também é dele).

## Permissões
Recurso `team_chat`, ações `read`, `write`, `manage`, `delete`.
- Admin e gestor: `read`, `write`, `manage`, `delete`.
- Agente: `read`, `write`.
- Papéis customizados **não recebem nada automaticamente**: o backfill (padrão de `internal/database/permissions_backfill.go`, com `grantRule`) só concede por equivalência a papéis de sistema. Quem precisar do chat ganha a permissão pela tela de papéis.
- Seed de papéis de sistema não alcança instalações existentes (`FixSystemRolePermissions` pula papéis com permissões), por isso o backfill é obrigatório e precisa de teste.

## Modelo de dados
Todas com `organization_id`; entram no `AutoMigrate` (`internal/database/postgres.go`). Só tabelas novas e aditivas; rollback = `DROP TABLE`.

| Tabela | Campos | Observação |
|---|---|---|
| `chat_channels` | `kind` (team, channel, dm), `team_id` (nulo), `name`, `created_by_id` | Único `(organization_id, team_id)` para `kind=team`. |
| `chat_channel_members` | `channel_id`, `user_id`, `role`, `joined_at` | Só `channel` e `dm`. Canal de time deriva de `team_members`. Único `(channel_id, user_id)`. |
| `chat_messages` | `channel_id`, `user_id`, `body`, `parent_id`, `context_type`, `context_id`, `edited_at`, `DeletedAt` | Índice `(channel_id, created_at DESC, id DESC)`. `parent_id` reservado p/ threads. |
| DM (em `chat_channels`) | `dm_user_low_id`, `dm_user_high_id` | Chave canônica da DM: os dois IDs ordenados (menor, maior). Único `(organization_id, dm_user_low_id, dm_user_high_id)`; `POST /dm` é idempotente (devolve a existente; corrida resolvida pela unicidade). |
| `chat_reads` | `channel_id`, `user_id`, `last_read_at`, `last_read_message_id` | Único `(channel_id, user_id)`. Necessária p/ canal de time. |

Marcador de leitura = `(created_at, id)` da última mensagem lida (mesmo padrão do cursor existente em `contacts.go`). Não lidas = mensagens após o marcador, exceto as do próprio usuário.

### Não lidas: casos de borda
- Sem linha em `chat_reads`, o marcador é o **momento do primeiro acesso** (`joined_at` para canal/DM; para canal de time, o maior entre a criação do canal e a entrada no time). Canal recém-criado ou novo membro não herda histórico como "não lido".
- Ao reentrar num time, o marcador vira `max(marcador antigo, momento da reentrada)`: o período fora não conta.
- Mensagens excluídas e as do próprio usuário não contam.
- O contador é sempre calculado no servidor a partir do marcador; o cliente nunca o envia.

## Autorização
Função única `canAccessChannel(userID, orgID, channel)`, usada em todas as operações:
- canal de time: usuário é `TeamMember` do time e o time pertence à org;
- `channel`/`dm`: existe linha em `chat_channel_members`.
A org vem sempre da sessão. Nenhum `channel_id`/`team_id` do cliente tem autoridade sem passar por ela.

| Operação | Exigência |
|---|---|
| Listar canais | `team_chat:read`, só os acessíveis |
| Ler / baixar anexo | `canAccessChannel` |
| Enviar | `team_chat:write` + acesso |
| Editar | autor + acesso |
| Excluir (autor) | autoria + `canAccessChannel` |
| Excluir (administrativo) | `team_chat:delete` + mesma org + `canAccessChannel` |
| Criar canal | `team_chat:manage` |
| Criar DM | `team_chat:write`; ambos na mesma org |

`team_chat:manage` e `team_chat:delete` **nunca** substituem a checagem de organização nem `canAccessChannel`: concedem a ação, não o acesso ao recurso.

Sair de um time remove o acesso imediato, inclusive ao histórico; reentrar o devolve.

## Tempo real
- Reusa o hub (`internal/websocket`): envelope `{type, payload}`, `BroadcastToUsers`. Sem segundo socket.
- WS **só notifica com IDs**; conteúdo vem por REST. Motivos: limite de 512 bytes do cliente→servidor e o hub descartar eventos com buffer cheio.
- O servidor calcula os destinatários autorizados em todo evento (criar, editar, excluir, ler, digitar).
- Tipos novos: `team_chat_message`, `team_chat_message_updated`, `team_chat_message_deleted`, `team_chat_read`, `team_chat_typing` (constantes em `messages.go` e `frontend/src/services/websocket.ts`).
- Ao reconectar, o cliente recarrega canais e não lidas; eventos perdidos são recuperados por sincronização REST (`GET /channels` e mensagens após o último cursor conhecido), sem depender de entrega garantida do hub.
- A autorização é **revalidada a cada requisição REST**, mesmo que o cliente tenha sabido da mensagem por evento WS (ex-membro que ainda tem o ID recebe 404).
- Presença: a existente (`agent_presence`).

## API (`/api/team-chat`)
`GET /channels` · `POST /channels` · `POST /dm` · `GET /channels/{id}/messages?before_id=&limit=` (padrão 50, máx. 100) · `POST /channels/{id}/messages` · `PUT|DELETE /messages/{id}` · `POST /channels/{id}/read`.
Rate limit nos envios com `UserAwareRateLimit`.

## Frontend
Rota `/team-chat` (permissão `team_chat`); `/chat` não muda. `stores/teamChat.ts`, `views/team-chat/` (sidebar, mensagens, compositor). Reuso: `useInfiniteScroll` (direção `top`), `components/ui`, `useAppToast`, `useTypingNotifier`, `wsService`. Texto escapado, sem `v-html`.

## Contexto de atendimento (fase seguinte)
`context_type`/`context_id` guardam só referência. O servidor valida com `CanViewConversationByID` (e a permissão de ocorrência) na criação **e a cada leitura do cartão**; sem acesso, o cartão sai sem dados.

## Critérios de aceite do MVP
1. Isolamento por org: org A não acessa canal, mensagem ou DM da org B (404).
2. Não membro não lê, não envia, não edita, não exclui e não recebe evento WS.
3. Canal de time: sair do time remove o acesso na hora; reentrar devolve.
4. Eventos WS só chegam aos autorizados (criar, editar, excluir, ler, digitar).
5. Não lidas corretas após enviar, ler e reconectar, e nos casos de borda acima (canal novo, novo membro, saída e retorno ao time).
   - DM: duas chamadas simultâneas a `POST /dm` entre o mesmo par resultam em uma só DM.
   - Exclusão: autor sem acesso ao canal não exclui; `team_chat:delete` de outra org ou sem acesso ao canal não exclui.
6. Testes existentes do atendimento (`/chat`, WebSocket, visibilidade) continuam passando.
7. Migração aditiva e removível; nenhuma tabela existente alterada.

## Entregas
1. Modelos, permissões, backfill e `canAccessChannel`, com testes (baixo/médio).
2. REST de canais e mensagens, cursor, não lidas (médio).
3. Eventos WS e store (médio).
4. UI do MVP (alto).
5. Pós-MVP: threads, reações, anexos, busca, contexto.

## Riscos
- Hub descarta eventos com buffer cheio → REST é a fonte da verdade.
- Instância única; mais de uma exige trocar o hub.
- Papel customizado sem `team_chat` não verá o chat (esperado; documentar).
- Criação de canal de time sob corrida → resolvida pela unicidade.
- `ILIKE` na busca pode ficar lento; reavaliar só com evidência.
