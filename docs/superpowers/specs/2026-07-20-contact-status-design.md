# Contact Status — Design

**Data:** 2026-07-20
**Branch:** `feature/contact-status`

## Objetivo

Dar a cada conversa um estado de atendimento explícito (`new` / `in_progress` / `resolved`), com transições automáticas nos momentos certos, alteração manual pelo agente, filtro na lista de conversas e atualização em tempo real via WebSocket.

Hoje a lista de contatos não distingue "ninguém atendeu ainda" de "já foi resolvido". O agente usa o badge de não-lidas como proxy de fila, o que quebra assim que ele abre a conversa sem responder.

## Decisões tomadas

| Questão | Decisão | Motivo |
|---|---|---|
| Semântica de `new` | `new` = aguardando 1º atendimento | Se todo INCOMING tirasse o contato de `new`, a aba "Novo" ficaria permanentemente vazia e seu contador seria inútil |
| Saída de `new` | Somente quando um **agente envia** uma mensagem | Sinal inequívoco de atendimento iniciado; abrir a conversa para espiar não esvazia a fila |
| Campo na API | Novo `contact_status`; `status` legado intacto | `ContactResponse.status` já existe hardcoded como `"active"`; alterá-lo quebraria integrações |
| Backfill | Derivado da atividade | Evita que a base inteira apareça em "Novo" (ou desapareça em "Concluído") no primeiro deploy |
| Contador | Só na aba "Novo", no escopo de visibilidade do usuário | Um número que o agente não consegue abrir é ruído, e vaza volume que ele não deveria ver |
| Permissão | Permissão de escrita de contatos existente + audit log | Sem nova permissão para propagar em roles customizados; mantém rastro de quem concluiu/reabriu |
| Lógica de transição | Helper central `transitionContactStatus()` | Uma regra num lugar só; alternativas (GORM hook, updates inline) espalham a regra ou disparam sem saber o ator |

## Backend

### Modelo

`internal/models/constants.go`:

```go
type ContactStatus string

const (
    ContactStatusNew        ContactStatus = "new"
    ContactStatusInProgress ContactStatus = "in_progress"
    ContactStatusResolved   ContactStatus = "resolved"
)
```

`internal/models/models.go`, em `Contact`:

```go
ContactStatus ContactStatus `gorm:"size:20;not null;default:'new'" json:"contact_status"`
```

### Migration

O projeto não tem migrations versionadas: é `AutoMigrate` + SQL cru em `getIndexes()` (`internal/database/postgres.go:233`) + funções de backfill. Seguimos esse padrão.

Ordem de execução em `RunMigrationWithProgress`, que **não** precisa ser alterada:

1. `AutoMigrate` cria a coluna `NOT NULL DEFAULT 'new'` — toda linha existente nasce com valor válido
2. Loop de `getIndexes()` aplica CHECK e índice composto (~linha 172)
3. `BackfillContactStatus` corrige os valores derivados (~linha 211, junto de `BackfillLastInboundAt`)

O CHECK rodar antes do backfill é seguro precisamente porque o default do `AutoMigrate` já satisfaz a constraint, e o backfill só escreve valores do enum. Nenhuma reordenação da função é necessária.

Em `getIndexes()`:

```sql
-- CHECK constraint (Postgres não tem ADD CONSTRAINT IF NOT EXISTS)
DO $$ BEGIN
  ALTER TABLE contacts ADD CONSTRAINT chk_contacts_contact_status
    CHECK (contact_status IN ('new','in_progress','resolved'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Índice composto na ordem exata do filtro + ordenação do ListContacts
CREATE INDEX IF NOT EXISTS idx_contacts_org_status_lastmsg
  ON contacts(organization_id, contact_status, last_message_at DESC NULLS LAST);
```

`BackfillContactStatus(db *gorm.DB) error`, ao lado de `BackfillLastInboundAt` (`postgres.go:392`). Guardado por `WHERE contact_status = 'new'` (o default), portanto idempotente e incapaz de sobrescrever estado real em execuções seguintes:

- sem nenhuma mensagem → `new` (já é o default; nenhum UPDATE necessário)
- `last_inbound_at` nas últimas 24h **ou** `is_read = false` → `in_progress`
- demais contatos com histórico de mensagens → `resolved`

### Helper de transição

Arquivo novo `internal/handlers/contact_status.go`:

```go
func (a *App) transitionContactStatus(
    contact *models.Contact,
    to models.ContactStatus,
    from []models.ContactStatus, // vazio = qualquer origem
    actorID *uuid.UUID,          // nil = transição automática
    reason string,
) (bool, error)
```

Comportamento:

1. UPDATE condicional: `WHERE id = ? AND contact_status IN (from...)` (sem cláusula quando `from` é vazio)
2. `RowsAffected == 0` → retorna `(false, nil)`: sem audit, sem broadcast
3. Grava `AuditLog` com contato, status anterior, novo status, ator e `reason`. O status anterior vem do `contact` carregado em memória, capturado antes do UPDATE
4. Publica o evento WebSocket
5. Atualiza `contact.ContactStatus` em memória

O UPDATE condicional é o que resolve a corrida entre um INCOMING e um envio simultâneo do agente, e o que garante exatamente um broadcast por mudança real.

### Gatilhos

| Origem | De → Para | Local |
|---|---|---|
| `PUT /contacts/{id}/status` | qualquer → qualquer | handler novo |
| Mensagem INCOMING | `resolved` → `in_progress` | `internal/handlers/chatbot_processor.go:1603` |
| Agente envia mensagem | `new` → `in_progress` | `internal/handlers/messages.go:261` |

`SendOutgoingMessage` é o caminho unificado de envio, usado por agentes e pelo chatbot. O gatilho só dispara quando `opts.SentByUserID != nil`, então resposta automática do bot **não** tira o contato da fila "Novo".

`chatbot_processor.go:1603` é o único ponto de INCOMING. O update de contato em `webhook.go:711` é eco de mensagem *outgoing* vinda do app do celular e não dispara transição.

### API

**`PUT /api/contacts/{id}/status`**

Registrar em `cmd/whatomate/main.go` junto às demais rotas de contato (~linha 632).

Body: `{"contact_status": "resolved"}`

- Valida contra o enum → 400 em valor inválido
- Reusa a permissão de escrita de contatos + `scopeAssignedContact`
- Chama `transitionContactStatus` com `from` vazio e `actorID` = usuário
- Retorna o `ContactResponse` atualizado

**`GET /api/contacts?status=new`**

Em `ListContacts` (`internal/handlers/contacts.go:87`), ao lado dos filtros `search` e `tags`. Valor inválido → 400 (não filtro silencioso vazio). Aplicado antes do `Count`, para que `total` reflita o filtro.

**`GET /api/contacts/counts`**

Retorna `{"new": N}`, respeitando `ScopeToOrg` + `scopeAssignedContact`. Endpoint separado para não pagar um count extra em toda listagem.

**`ContactResponse`** ganha `ContactStatus models.ContactStatus \`json:"contact_status"\``. O campo `status` continua retornando `"active"`, inalterado.

### WebSocket

Em `internal/websocket/messages.go`:

```go
TypeContactStatusChanged = "contact_status_changed"

type ContactStatusChangedPayload struct {
    ContactID       uuid.UUID  `json:"contact_id"`
    OldStatus       string     `json:"old_status"`
    NewStatus       string     `json:"new_status"`
    ChangedByUserID *uuid.UUID `json:"changed_by_user_id,omitempty"`
    ChangedAt       time.Time  `json:"changed_at"`
}
```

Broadcast via `a.WSHub.BroadcastToOrg(orgID, websocket.WSMessage{Type: websocket.TypeContactStatusChanged, Payload: payload})` — struct tipada `WSMessage{}`, nunca map genérico. Org-wide, porque a sidebar de todo agente precisa reagir, não só quem tem o contato aberto.

## Frontend

### Direção visual

Nenhuma paleta ou tipografia nova. O brief é adicionar a um inbox existente que o agente encara o dia inteiro; qualquer coisa que destoe do `#0a0a0b` + shadcn-vue estabelecido vira defeito percebido, não identidade. Todas as escolhas derivam de tokens já presentes em `ChatView.vue`.

Semântica de cor:

| Status | Token | Motivo |
|---|---|---|
| Novo | `emerald-500` | Mesmo tom do badge de não-lidas — mesma classe de sinal |
| Em andamento | `sky-400` | Distinto sem competir; nenhum outro elemento do chat usa sky |
| Concluído | `white/30` | Ausência de sinal é a informação |

### Abas de filtro

Chips roláveis horizontalmente, numa linha abaixo da busca (`ChatView.vue:1660`). **Não** segmented control de 4 colunas: "Todos / Novo / Em andamento / Concluído" não cabe nos ~304px úteis da sidebar `w-80` sem truncar ou abreviar. Chips têm largura natural por rótulo, preservam "Em andamento" por extenso e sobrevivem a traduções mais verbosas que o inglês — relevante num app com Crowdin.

Chip ativo segue o pattern das abas de conta existentes (`ChatView.vue:1963`): `bg-emerald-600 text-white`. Inativo: `bg-white/[0.08] text-white/70`, com variantes `light:`.

O chip "Novo" carrega o contador quando `> 0`, no mesmo tratamento do badge de não-lidas.

### Barra de status na linha

Barra de 2px na borda esquerda de cada item da lista, na cor do status. Visível apenas para `new` e `in_progress`; `resolved` não recebe barra.

A linha já carrega avatar, nome, horário, prévia e badge de não-lidas — um badge de status seria o quinto elemento em 304px e destruiria a leitura. A barra custa zero espaço horizontal e torna a lista escaneável na vertical.

Como cor sozinha não pode carregar informação: cada linha ganha `:title` com o status por extenso e a barra recebe `aria-label`.

### Prévia da última mensagem

A segunda linha do item passa a exibir `last_message_preview` no lugar de `contact.phone_number`. O campo já existe no modelo e já é populado nos três caminhos de escrita (incoming, outgoing, eco) — mudança puramente de frontend.

O telefone não se perde: quando o contato não tem nome, a linha 1 já é o telefone; nos demais casos ele aparece no cabeçalho do chat e no painel de contato.

Prefixo `Você:` para mensagens outgoing está **fora de escopo** (exigiria um campo adicional no preview vindo do backend).

### Botão no cabeçalho

À esquerda do grupo de ícones existente (`ChatView.vue:~1890`), com rótulo visível — é ação de consequência e merece nome, não só ícone:

- status ≠ `resolved` → "Concluir atendimento", ghost `text-emerald-400`, ícone `CheckCircle2`
- status = `resolved` → "Reabrir atendimento", ghost neutro, ícone `RotateCcw`

Rótulo e confirmação usam o mesmo verbo: "Concluir atendimento" → toast "Atendimento concluído". `disabled` durante o request. Erro informa o que falhou, sem mensagem genérica.

### Componentização

`ChatView.vue` tem 2774 linhas e a feature toca exatamente a região da lista. Extrair:

- `frontend/src/components/chat/ConversationStatusFilter.vue` — chips + contador
- `frontend/src/components/chat/ConversationListItem.vue` — linha, barra de status, prévia

`ChatView` passa props. Nenhuma outra refatoração do arquivo faz parte deste escopo.

### Estado e tempo real

`frontend/src/stores/contacts.ts` ganha `statusFilter` e `newCount`. Trocar de chip refaz o fetch com `?status=` (filtro server-side, para paginação e `total` corretos).

`frontend/src/services/websocket.ts` ganha o handler de `contact_status_changed`: atualiza o contato em memória, remove-o da lista se deixou de casar com o filtro ativo, e ajusta `newCount`. Sem reload.

### i18n

Todas as strings novas entram nos arquivos de locale (`chat.statusNew`, `chat.statusInProgress`, `chat.statusResolved`, `chat.filterAll`, `chat.resolveConversation`, `chat.reopenConversation`, e as mensagens de toast), seguindo o sweep de i18n já concluído no projeto.

## Testes

**Go**

- `transitionContactStatus`: cada par de/para permitido; o no-op quando `RowsAffected == 0` não grava audit nem faz broadcast; audit log recebe status anterior e ator corretos
- `BackfillContactStatus`: as três categorias de contato; idempotência em segunda execução
- `PUT /contacts/{id}/status`: sucesso, valor inválido → 400, sem permissão → 403, contato fora do escopo de visibilidade → 404
- `GET /contacts?status=`: filtro correto, `total` coerente, valor inválido → 400
- Gatilho INCOMING: `resolved` → `in_progress`; `new` permanece `new`
- Gatilho de envio: agente move `new` → `in_progress`; envio do chatbot (`SentByUserID == nil`) não move

**Frontend (Playwright)**

- Filtrar por chip mostra apenas contatos do status
- Concluir um atendimento remove a linha da aba "Em andamento"
- Contador de "Novo" decrementa ao receber `contact_status_changed` via WS

## Fora de escopo

- Prefixo `Você:` na prévia
- Contadores nas abas "Todos", "Em andamento" e "Concluído"
- Permissão dedicada `contacts:change_status`
- Auto-resolução por inatividade
- Relatórios/analytics por status
