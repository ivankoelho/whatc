# Visibilidade de Conversas — Ciclo 2

**Data:** 2026-07-21
**Branch:** `feature/conversation-visibility`
**Escopo:** Ciclo 2 de 2. O Ciclo 1 (liberação/encerramento) já está em `development`.

## Objetivo

Quando uma conversa está atribuída a um agente, **somente esse agente** (mais supervisores/managers/admins conforme RBAC) pode visualizá-la e interagir com ela. A restrição vale na API **e** na interface, e é **configurável por empresa**.

## Diagnóstico da implementação atual

### Como as conversas são listadas e autorizadas hoje

Não existe entidade "Conversa": uma conversa é um `Contact` mais sua `AgentTransfer` ativa. A visibilidade é decidida por uma única função, `scopeAssignedContact` (`internal/handlers/contacts.go:223`), aplicada em **11 pontos** (listagem, obter, mensagens, enviar, mídia, status, digitação):

```go
func (a *App) scopeAssignedContact(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
    if a.HasPermission(userID, models.ResourceContacts, models.ActionRead, orgID) {
        return query // vê TUDO
    }
    return query.Where("assigned_user_id = ? OR id IN (transferências ativas do usuário)")
}
```

### A inconsistência central

O papel **agent padrão tem `contacts:read`** (`internal/models/roles.go:298`). Como `scopeAssignedContact` libera geral para quem tem `contacts:read`, **o filtro nunca restringe agentes** — todos veem e podem responder qualquer conversa, inclusive as atribuídas a outro agente. O envio (`SendMessage`, `contacts.go:583`) usa o mesmo filtro, então também não bloqueia.

Confirmações às perguntas do produto:
1. Outros agentes **veem** a conversa do João? Sim.
2. Conseguem **enviar** mensagem nela? Sim.
3. A restrição é só de UI ou também backend? Backend, mas frouxa — a infraestrutura existe (11 pontos), só a condição de bypass (`contacts:read`) é ampla demais.
4. A regra está concluída? Não.

### O que já existe e o que falta

| Peça | Estado |
|---|---|
| Ponto único de autorização de visibilidade | ✅ existe (`scopeAssignedContact`), aplicado em 11 lugares |
| Enforcement no backend (não só UI) | ✅ existe, mas com bypass amplo |
| Permissão para "ver tudo" separada de "ler contato" | ❌ não existe |
| Escopo de equipe na visibilidade | ❌ não existe (dados `TeamMember` e `AgentTransfer.TeamID` existem, mas nenhuma consulta de visibilidade os usa) |
| Configuração por empresa | ❌ não existe |

## Decisões

| Questão | Decisão |
|---|---|
| Quem vê tudo | Nova permissão **`conversations:view_all`**, seedada em `manager`/`admin`. Não sobrecarrega `contacts:read`, que continua sendo acesso a dados do contato |
| Configurável por empresa | Flag **`strict_conversation_visibility`** em `AgentAssignmentConfig`, **default `false`** — nenhuma empresa muda de comportamento no deploy |
| Fila geral (`TeamID` NULL) | Estado operacional legítimo (chatbot desabilitado, fluxo sem equipe, conversa iniciada por agente, empresa sem equipes). Visível a todos os agentes autorizados |
| Fila de equipe (`TeamID` != NULL) | Visível só aos membros da equipe (via `TeamMember`) + `view_all` |
| Fila geral como equipe padrão | Recomendação de produto aceita, mas fora de escopo — **Ciclo 3** (exige equipe-padrão por org + backfill) |

## Precedência entre `AgentTransfer.AgentID` e `Contact.AssignedUserID`

**Esta é a seção que governa toda a autorização. Há uma única fonte de verdade primária, e a secundária tem escopo estritamente delimitado.**

Os dois campos representam conceitos diferentes e **não são equivalentes**:
- `AgentTransfer.AgentID` — **quem está atendendo agora** (atendimento ativo).
- `Contact.AssignedUserID` — **carteira / vínculo comercial** do contato.

**Regra de precedência (invariante):**

> `Contact.AssignedUserID` só é consultado quando **não existe `AgentTransfer` ativa** para o contato.

Consequências, mapeadas aos riscos levantados:

| Estado | Governado por | Risco evitado |
|---|---|---|
| Atendimento ativo com agente | `AgentTransfer.AgentID` | — |
| **Fila** (transferência ativa, `AgentID` NULL) | `AgentTransfer.TeamID` | A conversa **não** fica invisível para a equipe: há transferência ativa, então a carteira nunca é consultada |
| **Encerrada** | pool geral | **Não** fica presa ao agente: o `releaseContact` do Ciclo 1 limpa `AssignedUserID` no encerramento |
| **Transferida** | a nova transferência ativa | A carteira antiga **não** bloqueia: a nova transferência governa |
| Sem transferência ativa **e** com `AssignedUserID` (carteira pura) | `Contact.AssignedUserID` | Único caso em que a carteira governa visibilidade |

O último caso é o cenário de carteira individual: um contato atribuído manualmente via `PUT /contacts/{id}/assign`, sem atendimento ativo. Só então a carteira restringe a visibilidade — a esse agente mais `view_all`.

Como fila e encerramento **sempre** têm, respectivamente, transferência ativa ou `AssignedUserID` já limpo, a carteira **nunca** interfere no fluxo de filas, transferências ou novos atendimentos.

### Comportamento após uma transferência

Este comportamento é consequência direta da precedência acima, e fica documentado explicitamente porque é o ponto que o produto mais precisa garantir.

Quando uma conversa atendida pelo **agente de origem A** é transferida para o **agente de destino B** (ou para a fila de uma equipe):

- A transferência ativa passa a ter `AgentID = B` (ou `AgentID = NULL` com o `TeamID` da equipe de destino). Como a autorização é governada pela transferência ativa, **o agente A perde acesso imediatamente** — na próxima requisição, `canView`/`canInteract` retornam `false` e a listagem já não traz a conversa. Não há janela em que A ainda responda a uma conversa que não é mais dele.
- **Exceção — permissões superiores:** um usuário com `conversations:view_all` (supervisor/manager/admin) continua vendo e interagindo, antes e depois da transferência, porque `view_all` é avaliado antes da árvore de atribuição. Se o próprio A tiver `view_all`, ele mantém acesso — mas então não é "o agente de origem" e sim um supervisor, e o acesso é por RBAC, não por ter atendido antes.
- **Perda imediata, não no encerramento:** o acesso de A cai no instante da transferência, não quando a conversa é encerrada. A referência é sempre o estado atual da transferência ativa, nunca o histórico de quem atendeu.

Uma tentativa de A enviar mensagem à conversa **após** a transferência retorna **403** — isto é testado (ver "Testes").

## Arquitetura

### A função central — um único ponto

Arquivo novo `internal/handlers/conversation_visibility.go`.

**Visualizar e interagir são conceitos separados desde já**, ainda que no Ciclo 2 as duas usem a mesma regra. A separação é estrutural (assinaturas distintas), não de implementação — hoje `canInteract` delega a `canView`; amanhã, se surgir um caso "vê mas não responde" (ex.: um papel de auditoria read-only), o ponto de divergência já existe e não exige retrofit dos call sites.

```go
// conversationAccess é a decisão de autorização central, calculada uma vez a
// partir do estado do contato. Todas as formas abaixo derivam dela — não há
// segunda fonte de verdade.
type conversationAccess struct {
    canView     bool
    canInteract bool
}

// authorizeConversation é o ÚNICO lugar onde a regra vive. Recebe o contato
// carregado e devolve a decisão.
func (a *App) authorizeConversation(userID, orgID uuid.UUID, contact *models.Contact) conversationAccess

// canViewConversation e canInteractWithConversation são finas projeções da
// decisão acima — usadas nos caminhos de AÇÃO (false → 403).
func (a *App) canViewConversation(userID, orgID uuid.UUID, contact *models.Contact) bool
func (a *App) canInteractWithConversation(userID, orgID uuid.UUID, contact *models.Contact) bool
```

**`scopeVisibleConversations` é *exclusivamente* a tradução SQL de `authorizeConversation.canView`** — não uma segunda regra. Ela existe só porque a listagem não pode chamar a função linha a linha sem puxar tudo do banco. Para blindar contra bifurcação futura:

- As duas devem produzir o mesmo veredito de *visualização* para qualquer contato. Isso é **verificado por teste**: um teste-oráculo cria contatos em todos os ramos da árvore e afirma que `scopeVisibleConversations(...).Find(...)` retorna exatamente o conjunto para o qual `canViewConversation` é `true`. Se as duas divergirem, o teste quebra.
- O comentário no código de ambas aponta uma para a outra e para este parágrafo do spec.

```go
// scopeVisibleConversations é a tradução SQL de authorizeConversation.canView
// (ver spec §"A função central"). Deve retornar exatamente os contatos para os
// quais canViewConversation é true — o teste TestVisibilityScopeMatchesFunction
// garante isso. Substitui scopeAssignedContact nos 11 pontos de listagem.
func (a *App) scopeVisibleConversations(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB
```

Ambas consultam a mesma decisão interna. A árvore:

```
strict_conversation_visibility da empresa == false (default)?
    → comportamento ATUAL na íntegra (lógica de scopeAssignedContact de hoje:
      tem contacts:read → vê tudo; senão → só o atribuído/transferido).
      Nada muda para empresas existentes. FIM.

Ligada:
    HasPermission(user, conversations:view_all)? → PERMITE (supervisor/manager/admin)

    Contato tem AgentTransfer ativa?
        Sim, com AgentID:      user == AgentID?                → PERMITE / senão NEGA
        Sim, sem agente, TeamID != NULL:  user é membro da equipe? → PERMITE / senão NEGA
        Sim, sem agente, TeamID == NULL (fila geral):            → PERMITE (autorizados)
        Não (sem transferência ativa):
            Contact.AssignedUserID != NULL?  user == AssignedUserID? → PERMITE / senão NEGA
            AssignedUserID == NULL:                                  → PERMITE (autorizados)
```

`scopeVisibleConversations` traduz isso num `WHERE` com subconsultas a `agent_transfers` (ativa) e `team_members`. **Com a flag desligada, ela reproduz exatamente `scopeAssignedContact` de hoje** — não é "todos veem tudo": a restrição fraca atual (para quem não tem `contacts:read`) continua valendo. A flag ligada é que troca o critério para a árvore estrita.

### A permissão `conversations:view_all`

Novo recurso `conversations` com ação `view_all` no RBAC existente. Seedada em `manager` e `admin` via `SystemRolePermissions`. `agent` **não** recebe. Papéis customizados podem receber pela tela de papéis, que já existe. Nenhum sistema paralelo de permissões.

### A flag `strict_conversation_visibility`

Campo em `AgentAssignmentConfig` (`internal/models/chatbot.go`), ao lado da `close_inactive_attendances` do Ciclo 1:

```go
StrictConversationVisibility bool `gorm:"column:strict_conversation_visibility;default:false" json:"strict_conversation_visibility"`
```

Aditivo, `AutoMigrate` cria a coluna, sem backfill. Exposto na tela de configurações do chatbot (leitura + escrita + toggle na UI, com i18n em ambos os locales). Consultado **só** dentro de `canViewConversation`.

### Enforcement

- **Listagem/leitura (11 pontos):** trocar `scopeAssignedContact` por `scopeVisibleConversations`.
- **Ação (enviar, mídia, status, digitação):** após carregar o contato, `if !canViewConversation(...) → 403`. Hoje esses caminhos dependem do scope no `First()`; passam a ter a checagem explícita, porque a mensagem de erro correta é 403 (não 404), e porque a regra estrita precisa negar mesmo quando o contato existe.

### Frontend

A lista já vem filtrada da API — conversas não autorizadas simplesmente não chegam. O compositor de mensagem e a área de chat só renderizam quando a conversa está visível. **A UI é conveniência; o backend é a verdade** — o 403 protege mesmo que a UI falhe. Mudança de frontend é pequena: consumir o que a API retorna e não renderizar ação onde não há permissão.

## Migração de dados

Aditiva apenas:
1. Coluna `strict_conversation_visibility` (default false) via `AutoMigrate`.
2. Seed da permissão `conversations:view_all` e sua atribuição a `manager`/`admin` — idempotente, no mesmo mecanismo de `SeedPermissionsAndRoles`.

Sem backfill destrutivo. Sem liberação retroativa. Empresas existentes ficam com a flag desligada → comportamento idêntico ao de hoje.

## Riscos de compatibilidade

| Risco | Mitigação |
|---|---|
| Empresa existente perde visibilidade no deploy | Flag default `false` — comportamento inalterado até a empresa optar |
| Papel customizado sem `view_all` perde acesso amplo | Só afeta empresas que ligarem a flag; a permissão é atribuível pela tela de papéis |
| Carteira prender conversa | Precedência documentada: carteira só governa sem transferência ativa; encerramento limpa o campo |
| Regra divergir entre listagem e ação | Uma decisão, duas formas, no mesmo arquivo — testadas contra a mesma matriz |

## Arquivos alterados

```
internal/handlers/conversation_visibility.go   NOVO — canViewConversation + scopeVisibleConversations
internal/handlers/contacts.go                  11 pontos: scopeAssignedContact → scopeVisibleConversations; 403 nas ações
internal/handlers/contact_status.go            scope + 403
internal/handlers/media.go                     scope
internal/handlers/typing.go                    scope
internal/models/roles.go                       recurso conversations, ação view_all, seed em manager/admin
internal/models/chatbot.go                     flag strict_conversation_visibility
internal/handlers/chatbot.go                   ler/escrever a flag na API de settings
frontend/src/views/settings/ChatbotSettingsView.vue   toggle da flag
frontend/src/i18n/locales/{en,pt-BR}.json      strings
+ testes
```

`scopeAssignedContact` é removida após todos os pontos migrarem para `scopeVisibleConversations` — não deixar as duas coexistindo.

## Testes

**Go — a matriz da árvore, em listagem E em envio:**
- Modo estrito ligado: agente atribuído vê e envia; outro agente da mesma equipe recebe **403** na ação e **não** vê na listagem.
- `view_all` (manager) vê e envia qualquer conversa.
- Fila de equipe: membro da equipe vê; não-membro sem `view_all` não vê.
- Fila geral (`TeamID` NULL): qualquer agente autorizado vê.
- Carteira pura (sem transferência ativa, `AssignedUserID` definido): só o dono + `view_all`.
- **Precedência:** contato com transferência ativa para o agente A e `AssignedUserID` = agente B → governa A (a transferência vence).
- **Flag desligada:** comportamento idêntico ao atual (teste de regressão).

**Comportamento após transferência (Ajuste 2):**
- Conversa de A é transferida para B → na sequência, A recebe **403** ao tentar enviar mensagem, e a conversa some da listagem de A. B vê e envia.
- Transferência para fila de equipe → A perde acesso; membros da equipe de destino ganham.
- Supervisor com `view_all` mantém acesso antes e depois da transferência.

**Isolamento multiconta (Ajuste 3):**
- Agente da organização X **nunca** vê nem interage com conversa da organização Y, independentemente da flag, de `view_all`, de atribuição ou de pertencer a equipe de mesmo nome. `view_all` é escopado à organização do usuário — testar que um manager de X com `view_all` não alcança contato de Y (**403**/404, nunca 200).
- A flag `strict_conversation_visibility` de uma organização não afeta a visibilidade em outra.

**Coerência scope × função (Ajuste 4 — anti-bifurcação):**
- `TestVisibilityScopeMatchesFunction`: cria contatos cobrindo todos os ramos da árvore e afirma que o conjunto retornado por `scopeVisibleConversations(...).Find(...)` é **exatamente** aquele para o qual `canViewConversation` é `true`. Guarda contra a listagem e a ação divergirem.

**Frontend (Playwright):** dois agentes; um atribuído vê e responde, o outro não vê a conversa na lista nem consegue abrir. Após transferir do primeiro para um terceiro, o primeiro perde a conversa da lista.

## Fora de escopo (Ciclo 3)

- Fila geral como equipe-padrão por organização (elimina `TeamID` NULL).
- Visibilidade granular além de equipe (ex.: sub-times, hierarquia de supervisão).
