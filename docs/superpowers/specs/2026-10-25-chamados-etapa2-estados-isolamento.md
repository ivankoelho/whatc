# Chamados — Etapa 2: estados gerais, transições e isolamento

Complementa o [diagnóstico](2026-10-25-central-chamados-diagnostico.md).

## Estados gerais

`new`, `in_progress`, `waiting_customer`, `resolved` (rótulos: Novo, Em andamento, Aguardando cliente, Concluído).

- Cada `OccurrenceStage` tem `general_state` (obrigatório na prática: vazio é derivado das flags). `is_closing` sempre espelha `resolved`; etapa inicial exige `new`.
- `Occurrence.general_state` é a cópia denormalizada da etapa atual. Mantida em: criação, mudança de etapa e reclassificação da etapa (propaga aos chamados dela).
- Etapas podem ser da organização (`team_id` nulo) ou de uma equipe. O pipeline de um chamado = etapas da organização + etapas da equipe dele. `team_id` da etapa não muda depois de criada.
- `allowed_from` (opcional) restringe de quais etapas se pode chegar naquela.
- Nenhuma etapa de Logística está no código: tudo é configuração (tela *Configurações → CRM → Etapas*, com estado geral e equipe).
- **Fora desta etapa:** `ContactStatus` (barra lateral do chat) continua com 3 valores e o CHECK do banco. Acrescentar "Aguardando cliente" no chat é uma mudança separada, com migração do constraint.

## Transições (backend)

`PUT /api/occurrences/{id}/stage` (mesmo contrato; clientes antigos não contornam):

| Regra | Resposta |
|---|---|
| sem acesso de interação ao chamado | 403 |
| chamado sem responsável e usuário não é supervisor | 409 "assuma antes" |
| usuário não é o responsável nem supervisor do escopo | 403 |
| etapa de destino de outra equipe | 422 |
| `allowed_from` não inclui a etapa atual | 422 |
| outra mudança aconteceu no meio (compare-and-set em `stage_id`) | 409 |

Concluir carimba `closed_at`; sair de `resolved` reabre (`closed_at` nulo, `reopened_count + 1`, evento com `reopened: true`). Reabrir segue a mesma regra de quem pode mover (responsável ou supervisor).

`POST /api/occurrences/{id}/claim` assume um chamado sem responsável (atômico, 409 se já tem dono). Trocar responsável (`PUT`) exige ser o responsável atual ou supervisor; mudar `unit_id` de um chamado de equipe exige supervisor.

## Isolamento

O acesso é decidido pelo próprio chamado, nunca pela visibilidade da conversa.

| Quem | Vê | Age |
|---|---|---|
| `conversations:view_all` | tudo da organização | sim, como supervisor |
| responsável | sim | sim |
| membro da equipe do chamado | sim | sim |
| `conversations:view_team` + equipe da mesma unidade, ou responsável é colega de equipe | sim | sim, como supervisor |
| quem abriu | sim | só se o chamado não tem equipe |
| participante (`occurrence_participants`) | sim | não |

- Mesma regra em lista, detalhe, mutações, eventos, contadores (`GET /api/occurrences/summary`), filtro do Kanban e eventos WebSocket (o hub falha fechado sem autorizador).
- Outra organização: 404. Outra loja: 403 / ausente das listas.
- `team_id` novo na criação (`POST /api/occurrences`): chamado encaminhado a uma equipe da qual o criador não faz parte nasce **sem responsável**; a unidade e o departamento vêm da equipe, a etapa inicial da equipe (ou da organização).

## Migração e histórico

Aditiva: colunas novas anuláveis/com default, tabela `occurrence_participants`, dois índices únicos parciais por nome de etapa (organização e equipe) no lugar do antigo.

Backfill idempotente (só preenche vazio): `general_state` das etapas (flags; nome "Aguardando cliente" → `waiting_customer`) e dos chamados; `team_id` do chamado = equipe da atendimento de origem (`source_transfer_id`), um fato registrado, não um palpite.

**Chamados históricos sem equipe nem unidade ficam restritos:** só responsável, quem abriu, participantes e `view_all` os enxergam. Antes, quem tinha acesso à conversa os via. Se algum perfil precisar enxergar esses chamados, atribuir equipe/unidade manualmente (supervisor) ou conceder `conversations:view_all`.

## Pendências conhecidas

- Dashboard/widgets (`widgets.go`, fonte `occurrences`) continuam agregando a organização inteira para quem tem `analytics:read`; falta aplicar o escopo por usuário.
- Endpoint para adicionar participantes (a tabela e a leitura existem).
- Estado "Aguardando cliente" no chat (`ContactStatus`).
- Voltar de "Aguardando cliente" para "Em andamento" quando o cliente responde (automação).
