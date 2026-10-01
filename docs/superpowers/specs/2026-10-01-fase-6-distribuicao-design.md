# Fase 6: distribuicao de atendimentos

## Problema

Relato de uma agente: chegavam clientes sem ela estar online, e duas agentes
respondiam ao mesmo cliente. Causas confirmadas no codigo:

1. A elegibilidade de distribuicao olhava so `users.is_available` (flag manual, default `true`).
   Logout ou queda do WebSocket nunca a limpavam.
2. Responder nao assumia a conversa, e qualquer usuario com `contacts:read` respondia a qualquer conversa.
3. `AssignAgentTransfer` fazia ler-e-salvar (ultima escrita vence).
4. Nao havia garantia de um unico transfer ativo por contato.
5. Round-robin lia o membro e atualizava `last_assigned_at` sem transacao.

## Regras

- Elegibilidade (`assignment.Assigner.IsAgentEligible`): usuario ativo E `is_available` E com WebSocket vivo.
  Uma unica regra para chat e ligacoes. Presenca vem de `Hub.IsUserOnline`
  (ping/pong derruba conexoes mortas). Disponibilidade (toggle "ausente") e presenca sao conceitos separados.
- Envio por agente: o backend assume (claim atomico) o atendimento sem dono, ou abre um, ANTES de enviar.
  Atendimento de outro agente: `409`, salvo `transfers:write` ou `conversations:view_all`.
  Respostas de protocolo (ocorrencia) nao assumem nem sao bloqueadas.
- Atribuicao: `UPDATE ... WHERE agent_id IS NOT DISTINCT FROM <lido>`; zero linhas = `409`.
- Um transfer ativo por contato: indice unico parcial `idx_agent_transfers_one_active_per_contact`.

## Indice unico: duplicados existentes

A migration NAO altera dados. Se existirem contatos com mais de um transfer ativo, o indice nao e criado,
os registros sao listados no console/log e a migration segue. Depois de resolver manualmente, rode a migration de novo.

Consulta de diagnostico (somente leitura):

```sql
SELECT organization_id,
       contact_id,
       COUNT(*)                                     AS active_count,
       STRING_AGG(id::text, ',' ORDER BY transferred_at) AS transfer_ids,
       MIN(transferred_at)                          AS oldest_at,
       MAX(transferred_at)                          AS newest_at
FROM agent_transfers
WHERE status = 'active' AND deleted_at IS NULL
GROUP BY organization_id, contact_id
HAVING COUNT(*) > 1
ORDER BY active_count DESC, oldest_at;
```

## Desconexao e reconexao

- O agente deixa de ser elegivel para NOVA distribuicao no instante em que perde a conexao.
- Os atendimentos ja atribuidos a ele ficam por 60 s (`DefaultPresenceGrace`). Reconectar nesse prazo nao muda nada.
- `PresenceReaper` varre a cada 10 s; passados 60 s chama `ReturnAgentTransfersToQueue`
  (compare-and-set por transfer: idempotente, nunca devolve nem notifica duas vezes).
  Cobre tambem reinicio do servidor: o primeiro sweep inicia o relogio de todo agente atribuido.

## API e eventos

| Item | Mudanca |
|---|---|
| `PUT /api/chatbot/transfers/{id}/assign`, `.../unassign` | `409` com `{transfer_id, status, agent_id}` quando outra pessoa mudou a atribuicao |
| `POST /api/chatbot/transfers` | `409` quando o contato ja tem atendimento ativo (antes: possivel duplicata) |
| Envio de mensagem por agente (`SendMessage`, midia, template) | `409` em conversa de outro agente; claim atomico no envio quando sem dono |
| `GET /api/users` | cada usuario traz `is_online` (presenca), separado de `is_available` |
| WS `agent_presence` | `{user_id, online}`: primeira conexao / ultima desconexao |
| WS `agent_availability` | `{user_id, is_available}` |
| WS `agent_transfer`, `agent_transfer_assign` | inalterados; agora emitidos tambem nos claims e na devolucao por desconexao |

Auditoria (`audit_logs`, resource `transfers`): `claimed`, `assigned`, `released`, `auto_assigned`, `queued`,
`returned_away`, `returned_offline`, `conflict`. Acoes do sistema aparecem como "System".
Mudanca de disponibilidade: resource `users`, campo `is_available`.

## Limites conhecidos

- Presenca vem do hub em memoria: valido para o deploy atual de um unico processo. Com varias instancias seria
  preciso um estado compartilhado (nao introduzido nesta fase).
- `load_balanced` continua sendo uma aproximacao: duas atribuicoes simultaneas podem ver a mesma carga.
  Nunca gera dupla atribuicao (o indice unico e o claim atomico garantem), apenas desequilibrio momentaneo.
- Respostas de protocolo (ocorrencia) nao sao bloqueadas por outro dono, para nao quebrar o fluxo de SAC.
