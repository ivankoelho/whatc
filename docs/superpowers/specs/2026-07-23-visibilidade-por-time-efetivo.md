# Visibilidade por Time Efetivo — Ciclo 3

**Data:** 2026-07-23
**Branch:** `feature/team-scoped-visibility`
**Escopo:** Ciclo 3. Estende a função central de visibilidade do Ciclo 2 (`internal/handlers/conversation_visibility.go`), já em produção. Depende do modo estrito (`strict_conversation_visibility`) ligado.

## Objetivo

Com o modo estrito ligado, um agente deve enxergar uma conversa **apenas quando ela é direcionada a ele ou à equipe dele** — inclusive **durante a fase de triagem do chatbot**, antes de qualquer transferência para um agente específico. Conversas ainda sem destinatário não devem aparecer para agente nenhum, apenas para supervisores.

## Diagnóstico — por que vaza hoje

O modo estrito **está ligado** (confirmado: log de atividade "Strict Conversation Visibility: No → Yes", e dois usuários — admin e agente de Logística — enxergando a mesma conversa `tiagoalcantara921` em triagem).

A conversa observada está **no meio do fluxo do chatbot** coletando dados de orçamento; a transferência para a equipe de vendas só ocorre no **último nó** (`orc_transferir_vendas`). Ou seja, ela **não tem transferência ativa** para nenhum agente ou time.

A função central do Ciclo 2 trata esse estado como **"pool geral" → visível a todos os agentes autorizados** (`conversation_visibility.go:65-66` e `:160-167`; teste "strict: general queue (no team) visible to authorized agents"). Isso é por design no Ciclo 2. Como no fluxo real a maior parte da vida de uma conversa acontece dentro do bot (antes de transferir), quase toda conversa passa horas nesse pool compartilhado.

**Conclusão:** não há bug no SQL de visibilidade — os testes provam que conversas com transferência ativa a outro time/agente são corretamente ocultadas. O vazamento é a **regra do pool geral**. O ajuste é dar à conversa um **time** já na triagem e escopar o pool geral por esse time.

## Cenário da operação (modelo que guia o desenho)

- **Número Central** — funil: o bot descobre se é SAC, Vendas ou qual **Loja** e só então transfere. Cada **loja é um time próprio** (os nós `transf_ba_*` têm `team_id` distintos); não existe um time "Logística" pai. Setar um time amplo cedo exporia todos os agentes-de-loja; por isso o time é gravado **na granularidade conhecida em cada passo** (Vendas no menu principal; Loja X na seleção de loja).
- **Número Financeiro** — sempre Financeiro. **Número Compras** — sempre Compras. A conversa deveria nascer pertencendo ao time correto, sem depender do fluxo.

## Decisões

| Questão | Decisão |
|---|---|
| Como escopar a triagem | Introduzir o **time efetivo** da conversa, resolvido por precedência (abaixo). O pool geral deixa de ser "todos os agentes" |
| Pool geral (sem time algum) | Passa a ser **`view_all` apenas** (Admin/Supervisor). Elimina o vazamento e o problema dos N agentes-de-loja |
| Onde o time da triagem é gravado (Central) | `Contact.team_id` (novo, anulável), gravado pelo **fluxo** via `team_id` opcional **por botão** no nó de menu. É gravação de campo leve, **não** uma transferência (o bot continua) |
| Números dedicados (Financeiro/Compras) | `WhatsAppAccount.default_team_id` (novo, anulável). Configura-se **uma vez por conta**; toda conversa daquele número nasce escopada, **sem editar o fluxo** |
| Reset | Ao encerrar/liberar a conversa (`releaseContact`, Ciclo 1), limpa `Contact.team_id` — a próxima mensagem recomeça o funil |
| "Assumir da fila" | Passa a ser **por fila de time**: não há mais fila geral para agentes puxarem; o pool geral vira área de supervisor |

## Precedência (fonte única, em `authorizeConversation`)

Aplica-se **só com `strict_conversation_visibility` ligado**. Avaliação ordenada; a primeira que casar decide o conjunto de quem vê. Usuários com `conversations:view_all` (supervisor/manager/admin) **sempre** veem.

```
1. Há AgentTransfer ativa?
   a. com agent_id                         → { esse agente } + view_all
   b. sem agente, com team_id              → membros do time + view_all
   c. sem agente, sem team_id (fila geral) → time padrão da conta, se houver; senão view_all apenas
2. Sem transferência ativa:
   a. carteira (Contact.assigned_user_id)  → { esse usuário } + view_all
   b. time efetivo do fluxo (Contact.team_id) → membros do time + view_all
   c. time padrão da conta (WhatsAppAccount.default_team_id) → membros do time + view_all
   d. nada                                 → view_all apenas
```

Mapeamento para a lista de precedência acordada com o produto:

| Lista do produto | Regra acima |
|---|---|
| 1. Agente atribuído | 1a (atendimento ativo) **e** 2a (carteira, sem atendimento ativo) — no código são campos distintos, e a transferência ativa vence a carteira (invariante do Ciclo 2) |
| 2. Fila/transferência ativa | 1b |
| 3. Time efetivo do fluxo | 2b |
| 4. Time padrão da conta | 2c |
| 5. Sem nada → Admin/Supervisor | 1c (fallback) e 2d |

**Mudanças vs. Ciclo 2:** as duas folhas que hoje devolvem "todos os agentes autorizados" (fila geral explícita, e sem-transferência-sem-carteira) passam a resolver por **time padrão da conta → senão `view_all` apenas**. Todo o resto do Ciclo 2 é preservado, incluindo a precedência transferência-ativa > carteira.

## Arquitetura

### Time efetivo — resolução

Uma função interna resolve o "time efetivo" e o "agente servente" a partir do contato, e `authorizeConversation` aplica a árvore acima. O time padrão da conta é obtido do `WhatsAppAccount` do contato (`Contact.whats_app_account` → `whatsapp_accounts.name` → `default_team_id`), com cache (o mesmo já usado para settings) para não bater no banco a cada checagem.

`canViewConversation`, `canInteractWithConversation` e `CanViewConversationByID` permanecem como projeções — nenhuma segunda fonte de verdade.

### Armazenamento (aditivo)

```go
// models: Contact
TeamID *uuid.UUID `gorm:"type:uuid;index" json:"team_id,omitempty"` // time efetivo da conversa (triagem), setado pelo fluxo

// models: WhatsAppAccount
DefaultTeamID *uuid.UUID `gorm:"type:uuid;index" json:"default_team_id,omitempty"` // time padrão do número
```

`AutoMigrate` cria as colunas anuláveis; sem backfill. `Contact.TeamID` **não** é a carteira (`AssignedUserID`, um usuário) nem a transferência ativa (`AgentTransfer.TeamID`); é o setor lógico da conversa durante a triagem.

### Gravar o time no fluxo (Número Central)

O nó de botões (`buttons`) ganha um `team_id` **opcional por botão**. Ao consumir um botão que o carrega, `execChatButtons` grava `Contact.TeamID` (upsert do campo) — **sem** criar transferência, então o chatbot continua coletando dados normalmente. Isso cobre tanto o menu principal (escolher "Realizar pedido" → `team = Vendas`) quanto o menu de loja (escolher "Salvador" → `team = Loja Salvador`), permitindo o *narrowing progressivo*: o time só é gravado quando a granularidade útil aparece.

Config do botão (aditivo, retrocompatível):
```jsonc
{ "id": "realizar_pedido", "title": "🛒 Realizar pedido", "team_id": "<uuid Vendas>" }
```
Botões sem `team_id` não alteram o time efetivo (ex.: "Voltar ao menu").

> Alternativa considerada e descartada por ora: um nó dedicado "Definir setor". O `team_id` por botão cobre os dois menus do fluxo real sem nó extra e sem novo tipo de nó.

### Reset no encerramento

`releaseContact` (Ciclo 1) passa a limpar `Contact.TeamID` junto com `AssignedUserID`. Uma conversa encerrada volta a ser triada do zero no próximo contato — o setor não vaza entre atendimentos distintos.

### `scopeVisibleConversations` — tradução SQL (com o teste-oráculo)

Continua sendo **exclusivamente** a tradução SQL de `authorizeConversation.canView`, guardada por `TestVisibilityScopeMatchesFunction`. O `WHERE` ganha:
- subconsulta ao `Contact.team_id` cruzando `team_members` do usuário;
- join `contacts.whats_app_account = whatsapp_accounts.name` (mesma org) para o `default_team_id`, cruzando `team_members`;
- as folhas "fila geral" e "sem nada" deixam de incluir o contato para agentes (só `view_all`), respeitando o fallback de time padrão da conta.

O teste-oráculo é **estendido** para cobrir os novos ramos: contato com `Contact.team_id` do time do viewer vs. de outro time; conta com `default_team_id` do time do viewer vs. de outro; fila geral explícita com e sem time padrão de conta. O invariante permanece: `scopeVisibleConversations(...).Find(...)` == conjunto onde `canViewConversation` é `true`.

### Frontend

- **Construtor de fluxo** (`ChatNodeProperties.vue`, nó `buttons`): seletor de time opcional por botão.
- **Configurações da conta** (`AccountDetailView.vue`): seletor de "Time padrão" para o número.
- **Lista de conversas**: já vem filtrada da API; nenhuma mudança de lógica — só passa a refletir o novo escopo. i18n em ambos os locales para os rótulos novos.

## Migração de dados

Aditiva apenas: colunas `contacts.team_id` e `whatsapp_accounts.default_team_id` (anuláveis) via `AutoMigrate`. Sem backfill. Empresas com o modo estrito **desligado** não mudam de comportamento. Empresas com ele **ligado** deixam de expor a fase de triagem a todos os agentes — é exatamente a correção desejada, mas é uma **mudança de comportamento visível** e deve constar nas notas de versão.

## Riscos de compatibilidade

| Risco | Mitigação |
|---|---|
| Conversa some para agentes durante a triagem e ninguém a "puxa" | Por design: supervisor vê; o fluxo/campo default direciona ao time assim que possível. Documentar que "assumir da fila" passa a ser por time |
| Número sem `default_team_id` e fluxo que nunca grava time | Conversa fica só com `view_all`. Mitigação: a UI destaca contas sem time padrão; o funil do Central grava o time cedo |
| SQL do scope divergir da função (mais ramos agora) | `TestVisibilityScopeMatchesFunction` estendido é o guarda anti-bifurcação |
| Carteira vs. time efetivo ambíguo | Precedência explícita: carteira (usuário específico) vence o time efetivo |
| Transferência entre equipes | Continua funcionando: transferência ativa (1a/1b) tem prioridade sobre `Contact.team_id` |

## Arquivos alterados

```
internal/models/models.go                 Contact.TeamID; WhatsAppAccount.DefaultTeamID
internal/handlers/conversation_visibility.go  árvore atualizada; resolução de time efetivo + default da conta; SQL do scope
internal/handlers/chatbot_graph_runner.go  execChatButtons grava Contact.TeamID a partir do team_id do botão
internal/handlers/contacts.go              releaseContact limpa TeamID (junto com AssignedUserID)
internal/handlers/accounts.go              ler/escrever default_team_id na API da conta
frontend/src/components/chatbot/ChatNodeProperties.vue  team_id por botão no nó buttons
frontend/src/views/settings/AccountDetailView.vue       seletor de time padrão da conta
frontend/src/services/api.ts               default_team_id no tipo da conta
frontend/src/i18n/locales/{en,pt-BR}.json  strings
+ testes (Go: árvore + oráculo estendidos; e2e: triagem escopada)
```

## Testes

**Go — árvore estendida (listagem E ação), modo estrito ligado:**
- Conversa em triagem, `Contact.team_id = Vendas` (sem transferência): agente de Vendas vê; agente de Logística **não** vê; `view_all` vê.
- `Contact.team_id = Loja Salvador`: agente da Loja Salvador vê; agente da Loja Alagoinhas **não** vê. (Cobre o cenário dos N agentes-de-loja.)
- Número com `default_team_id = Financeiro`, conversa sem transferência e sem `Contact.team_id`: agente do Financeiro vê; vendedor **não** vê.
- Pool geral real (sem transferência, sem carteira, sem `Contact.team_id`, sem default da conta): **nenhum agente** vê; só `view_all`.
- Fila geral explícita (transferência ativa sem agente e sem time): time padrão da conta se houver; senão só `view_all`.
- **Precedência:** transferência ativa para o agente A vence `Contact.team_id` de outro time; carteira vence `Contact.team_id`.
- **Reset:** encerrar a conversa limpa `Contact.team_id` (próximo contato recomeça sem time).
- **Flag desligada:** comportamento idêntico ao Ciclo 2/atual (regressão).

**Oráculo (anti-bifurcação):** `TestVisibilityScopeMatchesFunction` estendido cobre os ramos `Contact.team_id` e `default_team_id`; o conjunto do SQL == conjunto da função.

**Fluxo:** consumir um botão com `team_id` grava `Contact.TeamID` e **não** cria transferência (o bot segue); botão sem `team_id` não altera o time.

**Frontend (Playwright):** conta com time padrão; agente de outro time não vê a conversa em triagem; agente do time vê. No Central, escolher a opção que grava o time faz a conversa aparecer só para aquele time.

## Fora de escopo

- Hierarquia de times (time "Logística" pai dos times-loja, para um supervisor de Logística ver todas as lojas sem `view_all`). Hoje isso é `conversations:view_all` ou pertencer a cada time.
- Roteamento automático número→time por regra além do `default_team_id` único.
