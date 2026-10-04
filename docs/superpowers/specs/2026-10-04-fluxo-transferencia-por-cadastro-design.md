# Transferência do fluxo pelo cadastro do contato (unidade + departamento)

Nota de design. Estado: **aguardando aprovação**; nada implementado.

## 1. Objetivo

Hoje o nó de transferência de um fluxo aponta para **um time fixo**. A proposta é um segundo modo em que o destino sai do **cadastro do contato**: a unidade e o departamento do contato identificam o time que o atende, e a distribuição existente escolhe o agente. O fluxo deixa de depender da identidade de um time: se os times forem reorganizados, o fluxo continua apontando para "loja X, setor Y".

```
Contato colaborador → unit_id + department_id → time(s) marcado(s) com o par → distribuição existente → agente
                                   sem par ou sem time → fallback do nó + motivo registrado
```

## 2. Estado atual (verificado no código)

- **Nó de transferência** (`execChatTransfer`, `chatbot_graph_runner.go`): configuração `{body, team_id, notes, tags}`. `team_id` com UUID chama `createTransferToTeam`; vazio ou `_general` chama `createTransferToQueue`. Um `team_id` inválido cai na fila, com aviso no log.
- **`createTransferToTeam`**: pula se o contato já tem transferência ativa, respeita o horário comercial (fora dele, manda a mensagem de fora do horário) e pede o agente a `Assigner.AssignToTeam(teamID, ...)`. A distribuição (round-robin, balanceada por carga ou manual) **atua dentro de um time**. Não existe distribuição entre times.
- **`Team`** já tem `unit_id` e `department_id`, descritos hoje como "metadado aditivo: roteamento e atribuição não são afetados". São lidos só pela ocorrência (`unitDepartmentFromTransfer`). Marcar o time é opcional. Esta fase muda esse contrato: os dois campos passam a influenciar o roteamento do fluxo.
- **`Contact.unit_id` / `department_id`** só existem para `colaborador` (regra do servidor, PR #89), o que evita interpretar o cadastro de um cliente como destino de atendimento.
- **Editor do fluxo:** `ChatNodeProperties.vue` (seletor de time do nó, linha ~574) e a simulação (`useFlowGraphSimulation.ts`, `FlowPreviewPanel`) espelham o nó de transferência.
- O `team_id` dos **botões** (`buttons`, que grava o time no contato) é outro mecanismo e fica fora do escopo.

## 3. Modos do nó

Nova chave `destination` na configuração do nó:

| `destination` | Comportamento |
|---|---|
| ausente ou `"team"` | **igual a hoje**: `team_id` fixo ou fila. Fluxos existentes não mudam. |
| `"contact_placement"` | **novo**: destino pelo cadastro do contato (seção 4). Usa `fallback_team_id` (time, ou `_general`/vazio = fila) quando não houver destino. |

Fora desta fase: unidade e departamento **fixos** escolhidos no próprio nó (terceiro modo, evolução posterior).

## 4. Resolução (determinística e no servidor)

A unidade e o departamento vêm **do contato no banco**, nunca de texto da conversa nem de variável do fluxo.

1. O contato precisa ser `colaborador` e ter **os dois** campos. Se não, motivo `no_contact_placement` → fallback.
2. Candidatos: times da mesma organização, **ativos** (`is_active`), não excluídos, com `unit_id` e `department_id` iguais aos do contato, ordenados por `created_at, id` (ordem fixa).
3. Nenhum candidato → motivo `no_team_for_contact_placement` → fallback.
4. Um candidato → `createTransferToTeam(time)`, sem mudança nenhuma dali em diante.
5. Vários candidatos: a distribuição existente não atravessa times, então a escolha entre times é **determinística e explícita**: o primeiro candidato, na ordem fixa, que tenha agente disponível (`Assigner.GetAvailableAgents`); se nenhum tiver, o primeiro da ordem (a fila daquele time). Quem escolhe o agente dentro do time é a distribuição de sempre.
   - Limite conhecido, anotado no código: não há balanceamento entre times que compartilham o par. O caminho para melhorar é escolher por carga do time.
   - A tela de times deve avisar quando dois times ativos compartilham o par (item da implementação).

Tudo o que `createTransferToTeam` e `createTransferToQueue` já fazem continua valendo: transferência ativa existente, horário comercial, `TransferSourceFlow`, índice único de transferência ativa.

## 5. Fallback e auditoria do motivo

- Fallback: `fallback_team_id` do nó (time) ou, se ausente/`_general`, a fila geral. A transferência **nunca falha em silêncio** e o contato sempre é atendido pelo destino de fallback.
- Motivos: `team_fixed` (modo atual), `contact_placement` (resolveu pelo cadastro), `no_contact_placement`, `no_team_for_contact_placement`.
- O motivo é registrado no log estruturado (ids, sem conteúdo de mensagem) **e** persistido em `agent_transfers.routing_reason` (coluna nova, nullable, aditiva) para dar para auditar depois por que uma transferência foi parar numa fila. Ver decisão 1.

## 6. Interface

- `ChatNodeProperties.vue`: seletor "Destino" (Time fixo / Cadastro do contato). No segundo, o seletor "Se não houver time" (fallback: um time ou a fila geral). O seletor de time atual continua para o modo fixo.
- Simulação e pré-visualização do fluxo: mostram "cadastro do contato (unidade/departamento)" e o fallback configurado, sem executar nada.
- Tela de times: aviso quando dois times ativos têm o mesmo par unidade + departamento.
- Textos em `pt-BR` e `en`. O comentário do modelo `Team` é atualizado para dizer que unidade e departamento agora também são lidos pelo fluxo.

## 7. Segurança e compatibilidade

- Nenhum endpoint novo. Leitura sempre escopada à organização do contato; times de outra organização nunca entram na consulta.
- Fluxos existentes: sem `destination`, o comportamento é idêntico (testado). Exportação e importação de fluxos carregam o JSON do nó; no modo novo só viaja o `fallback_team_id`.
- Não altera a Fase 9D (a transferência proposta pela IA continua indo para a fila), o RAG nem as campanhas.

## 8. Testes

**Backend (`internal/handlers`)**
- Resolução: um time; nenhum; sem unidade; sem departamento; contato não colaborador; time inativo e excluído ignorados; times de outra organização ignorados.
- Vários times: o primeiro com agente disponível; nenhum com agente → o primeiro da ordem; ordem estável entre execuções.
- Fallback: time de fallback e fila geral, com o motivo certo registrado e persistido.
- Compatibilidade: nó sem `destination`, com `team_id` e com fila, idêntico ao atual (casos existentes passam sem alteração).
- Transferência ativa, horário comercial e idempotência continuam valendo no modo novo.

**Frontend**
- Helper de rótulo/validação do nó (vitest); verificação no navegador do editor e da simulação.

## 9. Fora de escopo

Unidade e departamento fixos no nó; `team_id` dos botões; transferência por palavra-chave e fluxo legado; balanceamento entre times; restrição de unicidade do par entre times (só o aviso); Fase 9D.

## 10. Plano (depois da aprovação)

1. Backend: coluna `routing_reason`, resolução e fallback no nó + testes.
2. Frontend: editor, simulação, aviso na tela de times, i18n.
3. Verificação no navegador e suíte completa.

Branch `feature/flow-transfer-by-contact-placement`, a partir de `development` (`03f39fc`). Sem push antes da sua revisão local.

## 11. Pontos para decidir

1. **Persistir o motivo** em `agent_transfers.routing_reason` (recomendado, coluna aditiva nullable) ou só no log?
2. **Vários times com o mesmo par:** a regra da seção 4 (primeiro com agente disponível, senão o primeiro da ordem) serve? A alternativa é recusar a ambiguidade e usar sempre o fallback quando houver mais de um.
3. **Exigir unidade e departamento juntos** (recomendado) ou aceitar só um dos dois? Aceitar só um tornaria o destino ambíguo.
4. **Só contatos `colaborador`** usam o modo novo (recomendado, coerente com a regra do cadastro).
5. **Fallback padrão:** a fila geral quando o nó não define `fallback_team_id`.

## 12. Garantias que não mudam

`ai_tools.providers = []` e `write_enabled = false`. A única mudança de schema é a coluna nullable do item 1. Merge só com aprovação explícita, sem squash nem rebase.
