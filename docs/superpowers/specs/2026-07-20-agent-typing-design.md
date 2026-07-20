# Agent Typing & Message Authorship — Design

**Data:** 2026-07-20
**Branch:** `feature/agent-typing`

## Objetivo

Dois problemas de coordenação entre agentes na mesma conversa:

1. Dois agentes podem redigir respostas para o mesmo contato ao mesmo tempo sem saber um do outro. Um indicador de digitação em tempo real resolve.
2. Olhando o histórico, não dá para saber qual agente enviou cada mensagem — todas as bolhas outgoing são anônimas. Exibir o nome de quem enviou resolve.

## Decisões tomadas

| Questão | Decisão | Motivo |
|---|---|---|
| Nome do agente visível ao cliente | **Não.** Apenas na UI interna | Mensagem enviada no WhatsApp não pode ser editada nem desfeita; prefixo não funciona em templates (conteúdo aprovado pela Meta é imutável), consome caracteres do limite e expõe nomes de funcionários a todos os clientes |
| Ciclo de vida do typing | Timer de 3s renovado a cada evento | Não precisa de evento explícito de parada, e sobrevive a aba fechada ou queda de rede — casos em que um sinal de "parou" nunca chegaria |
| Mensagem outgoing sem agente | Omitir o nome | Chatbot, campanha e API não têm `SentByUserID`; a ausência do nome já comunica que não foi um humano |
| Onde exibir o indicador | Só na área do chat | É onde o evento já chega; levá-lo à lista de conversas exigiria broadcast para toda a organização a cada tecla |
| Transporte | `POST` HTTP | Reaproveita middleware de auth e permissão; a alternativa (mensagem de entrada pelo WebSocket) pouparia um round-trip a cada 2-3s mas exigiria estender o parser de entrada e montar autorização à mão |
| Entrega do evento | Método novo `BroadcastToContactViewers` | O `BroadcastToContact` atual também entrega a clientes sem contato selecionado (ver abaixo); um método novo evita regressão nas notas de conversa |

**Consequência aceita:** o cliente no WhatsApp continua sem saber com qual agente falou. O pedido original mencionava isso; a decisão foi conscientemente não atender, pelos motivos da primeira linha da tabela. Se um dia for desejado, é uma feature separada — provavelmente configurável por organização e restrita a mensagens de texto livre.

## O que já existe

Nenhuma mudança de schema é necessária:

- `Message.SentByUserID` e a relação `Message.SentByUser` já existem (`internal/models/models.go:405` e `:412`)
- `GetMessages` já usa `Preload("ReplyToMessage")` (`internal/handlers/contacts.go:331`), então `Preload("SentByUser")` segue o padrão
- `SendOutgoingMessage` já grava `SentByUserID` a partir de `opts.SentByUserID`

## O bug de entrega no hub

`internal/websocket/hub.go:155`:

```go
if msg.ContactID != uuid.Nil && client.currentContact != nil && *client.currentContact != msg.ContactID {
    continue
}
```

Quando `client.currentContact == nil` — o agente está na lista de conversas sem nenhuma aberta — a condição é falsa e o cliente **recebe** a mensagem. Ou seja, `BroadcastToContact` entrega a todos que não selecionaram contato.

Para as notas de conversa (único consumidor atual) isso é inofensivo. Para digitação seria um frame a cada 2-3s por agente digitando, entregue a quem não está olhando aquela conversa, carregando nome do agente e id do contato.

**Decisão:** não alterar `BroadcastToContact`. Adicionar:

```go
// BroadcastToContactViewers sends a message only to clients that have
// explicitly selected this contact. Unlike BroadcastToContact, a client with
// no contact selected receives nothing.
func (h *Hub) BroadcastToContactViewers(orgID, contactID uuid.UUID, msg WSMessage)
```

Isso exige um campo novo em `BroadcastMessage` (ex.: `RequireContactMatch bool`) e um ajuste na condição do loop que preserva o comportamento atual quando a flag está desligada. Mudar `BroadcastToContact` diretamente alteraria a entrega das notas de conversa, o que está fora do escopo desta feature.

## Backend

### Evento WebSocket

`internal/websocket/messages.go`:

```go
TypeAgentTyping = "agent_typing"

// AgentTypingPayload is the payload for agent_typing events.
type AgentTypingPayload struct {
    ContactID uuid.UUID `json:"contact_id"`
    UserID    uuid.UUID `json:"user_id"`
    UserName  string    `json:"user_name"`
    At        time.Time `json:"at"`
}
```

Struct tipada, nunca map.

### Endpoint

`POST /api/contacts/{id}/typing`, registrado junto às demais rotas de contato em `cmd/whatomate/main.go`.

1. `getOrgAndUserID` → 401 se falhar
2. `parsePathUUID`
3. Carrega o contato seguindo o padrão do `GetContact` (`internal/handlers/contacts.go:222`) — `a.DB.Where("id = ? AND organization_id = ?", ...)` encadeado com `a.scopeAssignedContact(query, userID, orgID)` — e responde 404 se não encontrar. `findByIDAndOrg` **não** serve aqui: ele checa a organização mas não aplica o escopo de visibilidade do usuário. Sem essa checagem, qualquer usuário autenticado forja digitação em qualquer contato da instância.
4. Resolve o nome via `audit.GetUserName(a.DB, userID)` — já faz fallback quando `full_name` está vazio
5. `WSHub.BroadcastToContactViewers(...)`; no-op silencioso se `WSHub` for nil
6. Responde 204

Sem escrita em banco. O custo por chamada é um SELECT por id indexado e um SELECT de nome.

### `sent_by_user_name` na resposta

`MessageResponse` ganha:

```go
SentByUserID   *uuid.UUID `json:"sent_by_user_id,omitempty"`
SentByUserName string     `json:"sent_by_user_name,omitempty"`
```

Preenchidos a partir de `msg.SentByUser.FullName` quando a relação existe. `Preload("SentByUser")` entra nos **dois** ramos de `GetMessages` (o de paginação por cursor e o padrão) — esquecer um deles produz nomes que somem ao rolar o histórico.

`broadcastNewMessage` (`internal/handlers/messages.go:481`) inclui `sent_by_user_name` no payload; sem isso, a mensagem que chega ao vivo aparece sem nome até um refresh.

## Frontend

### Estado

Em `frontend/src/stores/contacts.ts`:

```ts
export interface TypingAgent {
  user_id: string
  user_name: string
  at: number
}

const typingByContact = ref<Record<string, TypingAgent>>({})
```

Objeto simples, não `Map` — `Map` não é reativo no Vue 3.

Os handles de `setTimeout` ficam num `Record<string, number>` **fora** do `ref`: são detalhe de agendamento, não estado de UI, e não devem disparar renderização.

`applyAgentTyping(payload)`:
- Ignora eventos cujo `user_id` é o do próprio usuário
- Grava/sobrescreve a entrada do contato
- Reinicia o timer de 3s; ao expirar, remove a entrada

Um agente por contato (o último vence). Dois agentes digitando ao mesmo tempo na mesma conversa é raro o bastante para não justificar uma lista.

Limpeza ao trocar de contato e no unmount da view.

### Notificação com throttle

Composable `frontend/src/composables/useTypingNotifier.ts`: expõe `notifyTyping()`, que dispara `POST /contacts/{id}/typing` no máximo uma vez a cada 2500ms. Chamado no `@input` do compositor.

Erros são engolidos — um indicador de digitação nunca pode travar a caixa de texto.

### Exibição

**Indicador:** abaixo da lista de mensagens, no contato aberto: `"{nome} está digitando…"`.

**Nome na bolha:** rótulo pequeno dentro da bolha outgoing, no mesmo tratamento visual do rótulo de reply que já existe. Exibido **apenas quando o remetente muda em relação à mensagem anterior** — repetir o mesmo nome em bolhas consecutivas é ruído; agrupar por remetente é o padrão de Slack e Teams.

### i18n

`chat.agentTyping` (`"{name} está digitando…"` / `"{name} is typing…"`) em `en.json` e `pt-BR.json`.

## Testes

**Go**
- `BroadcastToContactViewers`: entrega a cliente com o contato selecionado; **não** entrega a cliente sem contato selecionado; não entrega a cliente vendo outro contato
- `BroadcastToContact` mantém o comportamento atual (teste de regressão)
- `POST /contacts/{id}/typing`: 204 no sucesso; 404 para contato de outra org; 401 sem auth; no-op sem hub
- `GetMessages`: mensagem enviada por agente traz `sent_by_user_name`; mensagem sem `SentByUserID` traz o campo vazio

**Playwright**
- Dois contextos de browser: um agente digita, o outro vê o indicador aparecer e desaparecer após ~3s
- Nome do agente visível na bolha outgoing

## Fora de escopo

- Prefixo com o nome do agente no texto enviado ao cliente
- Indicador de digitação na lista de conversas
- Múltiplos agentes digitando simultaneamente no mesmo contato
- Indicador de digitação do cliente (o webhook da Meta não fornece esse sinal)
- Corrigir o comportamento de entrega do `BroadcastToContact` para as notas de conversa
