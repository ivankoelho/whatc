# Agent Typing & Message Authorship Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mostrar em tempo real quando outro agente está digitando numa conversa, e exibir na bolha qual agente enviou cada mensagem outgoing.

**Architecture:** Um método novo no hub (`BroadcastToContactViewers`) entrega o evento `agent_typing` apenas a clientes que selecionaram aquele contato. Um endpoint `POST` sem escrita em banco publica o evento; o frontend chama com throttle de 2,5s e mantém um `Record<contactId, TypingAgent>` reativo com expiração de 3s. O nome do agente vem de `Message.SentByUser`, que já existe — nenhuma migration.

**Tech Stack:** Go + GORM + fastglue + gorilla/fasthttp websocket; Vue 3 + Pinia + Tailwind; Playwright para e2e.

**Spec:** `docs/superpowers/specs/2026-07-20-agent-typing-design.md`

## Global Constraints

- **Branch:** `feature/agent-typing`, PR com base em `development`. Nunca commitar direto em `main`.
- **Broadcast WebSocket:** sempre `websocket.WSMessage{Type: ..., Payload: ...}` com payload de struct tipada. **Nunca** `map[string]any` como mensagem.
- **Sem mudança de schema.** `Message.SentByUserID` e a relação `Message.SentByUser` já existem (`internal/models/models.go:405` e `:412`). Nenhuma migration nesta feature.
- **Não alterar `BroadcastToContact`.** As notas de conversa dependem do comportamento atual dele. O método novo é aditivo.
- **Nada do nome do agente vai para o texto enviado ao cliente.** A feature é exclusivamente de UI interna.
- **i18n:** toda string visível entra em `frontend/src/i18n/locales/en.json` **e** `pt-BR.json`.
- **Throttle:** no máximo uma chamada a `/typing` a cada **2500ms** por agente.
- **Expiração do indicador:** **3000ms** sem novo evento.
- **Testes Go:** exigem `TEST_DATABASE_URL` e `TEST_REDIS_URL`. Com os containers do dev local:
  `export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/whatomate_test?sslmode=disable"` e `export TEST_REDIS_URL="redis://localhost:6379"`
- **Duas falhas de teste pré-existentes** no repo (`TestApp_ServeMedia_RejectsSymlink` no Windows e `TestUpdateContactChatbotMessage_SetsTimestampAndResetsReminder`). Não são regressões desta feature; ignore-as.

---

### Task 1: Entrega estrita no hub

**Files:**
- Modify: `internal/websocket/messages.go` (struct `BroadcastMessage`)
- Modify: `internal/websocket/hub.go:154-157` (condição do loop), e novo método após `BroadcastToContact` (`:189`)
- Modify: `internal/websocket/export_test.go` (helper de teste)
- Test: `internal/websocket/websocket_test.go`

**Interfaces:**
- Consumes: nada
- Produces: `websocket.BroadcastMessage.RequireContactMatch bool`; `func (h *Hub) BroadcastToContactViewers(orgID, contactID uuid.UUID, msg WSMessage)`; `websocket.ClientSetCurrentContact(c *Client, id *uuid.UUID)` para testes

- [ ] **Step 1: Adicionar o helper de teste**

Em `internal/websocket/export_test.go`, no fim do arquivo:

```go
// ClientSetCurrentContact sets the client's selected contact for testing.
func ClientSetCurrentContact(c *Client, id *uuid.UUID) {
	c.currentContact = id
}
```

- [ ] **Step 2: Escrever os testes (falhando)**

Em `internal/websocket/websocket_test.go`, no fim do arquivo:

```go
// --- BroadcastToContactViewers ---

func TestHub_BroadcastToContactViewers_OnlyReachesViewers(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactID := uuid.New()
	otherContact := uuid.New()

	viewing := newTestClient(hub, uuid.New(), orgID)
	viewingOther := newTestClient(hub, uuid.New(), orgID)
	noContact := newTestClient(hub, uuid.New(), orgID)

	websocket.ClientSetCurrentContact(viewing, &contactID)
	websocket.ClientSetCurrentContact(viewingOther, &otherContact)
	websocket.ClientSetCurrentContact(noContact, nil)

	hub.Register(viewing)
	hub.Register(viewingOther)
	hub.Register(noContact)
	waitForClientCount(t, hub, 3)

	msg := websocket.WSMessage{Type: websocket.TypeAgentTyping, Payload: "typing"}
	hub.BroadcastToContactViewers(orgID, contactID, msg)

	assertReceivesMessage(t, viewing, websocket.TypeAgentTyping)
	assertNoMessage(t, viewingOther)
	assertNoMessage(t, noContact)
}

func TestHub_BroadcastToContact_StillReachesClientsWithNoContact(t *testing.T) {
	// Regression guard: conversation notes rely on this behaviour.
	hub := newTestHub(t)
	orgID := uuid.New()
	contactID := uuid.New()

	noContact := newTestClient(hub, uuid.New(), orgID)
	websocket.ClientSetCurrentContact(noContact, nil)

	hub.Register(noContact)
	waitForClientCount(t, hub, 1)

	msg := websocket.WSMessage{Type: websocket.TypeNewMessage, Payload: "note"}
	hub.BroadcastToContact(orgID, contactID, msg)

	assertReceivesMessage(t, noContact, websocket.TypeNewMessage)
}
```

- [ ] **Step 3: Rodar e confirmar que falha**

Run: `go test ./internal/websocket/ -run "BroadcastToContactViewers|StillReachesClients" 2>&1 | head -5`
Expected: FAIL — `undefined: websocket.TypeAgentTyping` e `hub.BroadcastToContactViewers undefined`

- [ ] **Step 4: Adicionar o tipo e o payload do evento**

Em `internal/websocket/messages.go`, na lista de constantes (após `TypeContactStatusChanged`):

```go
	// TypeAgentTyping carries AgentTypingPayload
	TypeAgentTyping = "agent_typing"
```

E após `ContactStatusChangedPayload`:

```go
// AgentTypingPayload is the payload for agent_typing events.
type AgentTypingPayload struct {
	ContactID uuid.UUID `json:"contact_id"`
	UserID    uuid.UUID `json:"user_id"`
	UserName  string    `json:"user_name"`
	At        time.Time `json:"at"`
}
```

- [ ] **Step 5: Adicionar a flag ao BroadcastMessage**

Em `internal/websocket/messages.go`, na struct `BroadcastMessage`:

```go
	// RequireContactMatch restricts delivery to clients that have explicitly
	// selected ContactID. Without it, clients with no contact selected also
	// receive the message — the historical behaviour BroadcastToContact relies on.
	RequireContactMatch bool
```

- [ ] **Step 6: Ajustar a condição do loop**

Em `internal/websocket/hub.go`, substituir o bloco das linhas 154-157:

```go
			// If ContactID is specified, only send to clients viewing that contact
			if msg.ContactID != uuid.Nil {
				if client.currentContact == nil {
					// No contact selected: strict senders skip, legacy senders deliver
					if msg.RequireContactMatch {
						continue
					}
				} else if *client.currentContact != msg.ContactID {
					continue
				}
			}
```

- [ ] **Step 7: Adicionar o método novo**

Em `internal/websocket/hub.go`, imediatamente após `BroadcastToContact` (linha ~195):

```go
// BroadcastToContactViewers sends a message only to clients that have
// explicitly selected this contact. Unlike BroadcastToContact, a client with
// no contact selected receives nothing.
func (h *Hub) BroadcastToContactViewers(orgID, contactID uuid.UUID, msg WSMessage) {
	h.Broadcast(BroadcastMessage{
		OrgID:               orgID,
		ContactID:           contactID,
		RequireContactMatch: true,
		Message:             msg,
	})
}
```

- [ ] **Step 8: Rodar os testes**

Run: `go test ./internal/websocket/ -v 2>&1 | grep -E "^(--- |ok|FAIL)"`
Expected: PASS nos dois testes novos e em todos os já existentes do pacote

- [ ] **Step 9: Commit**

```bash
git add internal/websocket/
git commit -m "feat(ws): add agent_typing event and strict contact-viewer broadcast"
```

---

### Task 2: Endpoint de digitação

**Files:**
- Create: `internal/handlers/typing.go`
- Modify: `cmd/whatomate/main.go` (registro da rota, junto às demais de contato ~linha 632)
- Test: `internal/handlers/typing_test.go`

**Interfaces:**
- Consumes: `websocket.TypeAgentTyping`, `websocket.AgentTypingPayload`, `Hub.BroadcastToContactViewers` (Task 1)
- Produces: `func (a *App) NotifyTyping(r *fastglue.Request) error`, rota `POST /api/contacts/{id}/typing`

- [ ] **Step 1: Escrever os testes (falhando)**

Criar `internal/handlers/typing_test.go`:

```go
package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestApp_NotifyTyping(t *testing.T) {
	t.Parallel()

	t.Run("accepts a typing notification", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusNoContent, testutil.GetResponseStatusCode(req))
	})

	t.Run("does not reach a contact from another org", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		otherOrg := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		foreign := testutil.CreateTestContact(t, app.DB, otherOrg.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", foreign.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
	})

	t.Run("rejects an unauthenticated request", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusUnauthorized, testutil.GetResponseStatusCode(req))
	})

	t.Run("succeeds without a websocket hub", func(t *testing.T) {
		// newTestApp leaves WSHub nil; the handler must not panic.
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		req := testutil.NewJSONRequest(t, nil)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.NotifyTyping(req))
		assert.Equal(t, fasthttp.StatusNoContent, testutil.GetResponseStatusCode(req))
	})
}
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestApp_NotifyTyping 2>&1 | head -5`
Expected: FAIL — `app.NotifyTyping undefined`

- [ ] **Step 3: Implementar o handler**

Criar `internal/handlers/typing.go`:

```go
package handlers

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// NotifyTyping tells agents viewing this contact that the caller is typing.
//
// Nothing is persisted: the event is ephemeral and the frontend expires it
// after a few seconds. The contact lookup is not decoration — without it any
// authenticated user could fake typing on any contact in the instance.
func (a *App) NotifyTyping(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	// Same visibility rules as GetContact: users without contacts:read only
	// reach contacts assigned to them.
	var contact models.Contact
	query := a.DB.Where("id = ? AND organization_id = ?", contactID, orgID)
	query = a.scopeAssignedContact(query, userID, orgID)
	if err := query.First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	if a.WSHub != nil {
		a.WSHub.BroadcastToContactViewers(orgID, contact.ID, websocket.WSMessage{
			Type: websocket.TypeAgentTyping,
			Payload: websocket.AgentTypingPayload{
				ContactID: contact.ID,
				UserID:    userID,
				UserName:  audit.GetUserName(a.DB, userID),
				At:        time.Now(),
			},
		})
	}

	r.RequestCtx.SetStatusCode(fasthttp.StatusNoContent)
	return nil
}
```

- [ ] **Step 4: Registrar a rota**

Em `cmd/whatomate/main.go`, junto às demais rotas de contato (logo após a linha `g.PUT("/api/contacts/{id}/status", app.UpdateContactStatus)`):

```go
	g.POST("/api/contacts/{id}/typing", app.NotifyTyping)
```

- [ ] **Step 5: Rodar os testes**

Run: `go test ./internal/handlers/ -run TestApp_NotifyTyping -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS nos quatro subtestes

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/typing.go internal/handlers/typing_test.go cmd/whatomate/main.go
git commit -m "feat(api): add POST /contacts/{id}/typing endpoint"
```

---

### Task 3: `sent_by_user_name` na resposta de mensagem

**Files:**
- Modify: `internal/handlers/contacts.go:51-71` (`MessageResponse`), `:331` e o ramo padrão de `GetMessages`, e os pontos de montagem da resposta
- Modify: `internal/handlers/messages.go:481-538` (`broadcastNewMessage`)
- Test: `internal/handlers/typing_test.go`

**Interfaces:**
- Consumes: nada das tasks anteriores
- Produces: campos `MessageResponse.SentByUserID *uuid.UUID` e `MessageResponse.SentByUserName string`; chave `sent_by_user_name` no payload de `new_message`

- [ ] **Step 1: Escrever o teste (falhando)**

Anexar a `internal/handlers/typing_test.go`:

```go
func TestApp_GetMessages_IncludesSenderName(t *testing.T) {
	t.Parallel()

	t.Run("outgoing message carries the agent name", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID,
			testutil.WithRoleID(&adminRole.ID), testutil.WithFullName("Ana Ribeiro"))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		require.NoError(t, app.DB.Create(&models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			WhatsAppAccount: "acct",
			ContactID:       contact.ID,
			Direction:       models.DirectionOutgoing,
			MessageType:     models.MessageTypeText,
			Content:         "resposta do agente",
			SentByUserID:    &user.ID,
		}).Error)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.GetMessages(req))
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

		var resp struct {
			Data struct {
				Messages []handlers.MessageResponse `json:"messages"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		require.Len(t, resp.Data.Messages, 1)
		assert.Equal(t, "Ana Ribeiro", resp.Data.Messages[0].SentByUserName)
	})

	t.Run("message without an agent carries an empty name", func(t *testing.T) {
		app := newTestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
		user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		require.NoError(t, app.DB.Create(&models.Message{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  org.ID,
			WhatsAppAccount: "acct",
			ContactID:       contact.ID,
			Direction:       models.DirectionOutgoing,
			MessageType:     models.MessageTypeText,
			Content:         "resposta do chatbot",
		}).Error)

		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", contact.ID.String())

		require.NoError(t, app.GetMessages(req))

		var resp struct {
			Data struct {
				Messages []handlers.MessageResponse `json:"messages"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		require.Len(t, resp.Data.Messages, 1)
		assert.Empty(t, resp.Data.Messages[0].SentByUserName)
	})
}
```

Adicionar ao bloco de imports de `typing_test.go`:

```go
	"encoding/json"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
```

- [ ] **Step 2: Rodar e confirmar que falha**

Run: `go test ./internal/handlers/ -run TestApp_GetMessages_IncludesSenderName 2>&1 | head -5`
Expected: FAIL — `resp.Data.Messages[0].SentByUserName undefined`

- [ ] **Step 3: Adicionar os campos à MessageResponse**

Em `internal/handlers/contacts.go`, na struct `MessageResponse`, após o campo `WhatsAppAccount`:

```go
	SentByUserID     *uuid.UUID           `json:"sent_by_user_id,omitempty"`
	SentByUserName   string               `json:"sent_by_user_name,omitempty"`
```

- [ ] **Step 4: Adicionar o Preload nos dois ramos de GetMessages**

São exatamente duas ocorrências. Esquecer uma faz o nome sumir ao rolar o histórico — o tipo de bug que passa despercebido em revisão.

`internal/handlers/contacts.go:333` (ramo de paginação por cursor), trocar:

```go
		if err := msgQuery.Preload("ReplyToMessage").Order("created_at DESC").Limit(limit).Find(&messages).Error; err != nil {
```

por:

```go
		if err := msgQuery.Preload("ReplyToMessage").Preload("SentByUser").Order("created_at DESC").Limit(limit).Find(&messages).Error; err != nil {
```

`internal/handlers/contacts.go:369` (ramo padrão), trocar:

```go
	if err := msgQuery.Preload("ReplyToMessage").Order("created_at ASC").Offset(offset).Limit(queryLimit).Find(&messages).Error; err != nil {
```

por:

```go
	if err := msgQuery.Preload("ReplyToMessage").Preload("SentByUser").Order("created_at ASC").Offset(offset).Limit(queryLimit).Find(&messages).Error; err != nil {
```

- [ ] **Step 5: Preencher os campos na montagem da resposta**

Em `internal/handlers/contacts.go:398`, no literal `msgResp := MessageResponse{...}`, adicionar após a linha `WhatsAppAccount: m.WhatsAppAccount,`:

```go
			SentByUserID:    m.SentByUserID,
			SentByUserName:  senderName(&m),
```

Os outros dois `MessageResponse{}` do arquivo (linhas 692 e 876) são respostas de **envio** — o cliente que acabou de enviar já sabe quem enviou, e a bolha é atualizada pelo broadcast do Step 6. Não precisam do campo.

E adicionar o helper no fim de `internal/handlers/contacts.go`:

```go
// senderName returns the display name of the agent who sent an outgoing
// message, or "" for messages with no agent (chatbot, campaign, API).
func senderName(m *models.Message) string {
	if m.SentByUser == nil {
		return ""
	}
	return m.SentByUser.FullName
}
```

- [ ] **Step 6: Incluir o nome no broadcast de nova mensagem**

Em `internal/handlers/messages.go`, dentro de `broadcastNewMessage`, no literal `payload := map[string]any{...}` (linha ~495), adicionar:

```go
		"sent_by_user_name": a.senderNameForBroadcast(msg),
```

E adicionar o helper no fim de `internal/handlers/messages.go`:

```go
// senderNameForBroadcast resolves the agent name for a websocket payload.
// The message being broadcast was just created and has no preloaded relation,
// so the name is fetched directly.
func (a *App) senderNameForBroadcast(msg *models.Message) string {
	if msg.SentByUserID == nil {
		return ""
	}
	return audit.GetUserName(a.DB, *msg.SentByUserID)
}
```

Adicionar `"github.com/shridarpatil/whatomate/internal/audit"` aos imports de `messages.go` se ainda não estiver presente.

- [ ] **Step 7: Rodar os testes**

Run: `go test ./internal/handlers/ -run "TestApp_GetMessages_IncludesSenderName|TestApp_NotifyTyping" -v 2>&1 | grep -E "^(    --- |--- |ok|FAIL)"`
Expected: PASS em todos

- [ ] **Step 8: Verificar build e suíte**

Run: `go build ./... && go test -p 1 ./... 2>&1 | grep -E "^(FAIL|--- FAIL)"`
Expected: apenas as duas falhas pré-existentes listadas nas Global Constraints

- [ ] **Step 9: Commit**

```bash
git add internal/handlers/contacts.go internal/handlers/messages.go internal/handlers/typing_test.go
git commit -m "feat(api): expose sent_by_user_name on message responses and broadcasts"
```

---

### Task 4: Estado de digitação no frontend

**Files:**
- Modify: `frontend/src/stores/contacts.ts`
- Modify: `frontend/src/services/api.ts` (`contactsService`)
- Modify: `frontend/src/services/websocket.ts`

**Interfaces:**
- Consumes: evento `agent_typing` (Tasks 1 e 2)
- Produces: `contactsService.notifyTyping(id)`; no store: `typingByContact` (ref), `applyAgentTyping(payload)`, `clearTyping(contactId)`; tipo `TypingAgent`

- [ ] **Step 1: Adicionar a chamada de API**

Em `frontend/src/services/api.ts`, dentro de `contactsService`:

```ts
  notifyTyping: (id: string) => api.post(`/contacts/${id}/typing`),
```

- [ ] **Step 2: Adicionar o tipo e os campos de mensagem**

Em `frontend/src/stores/contacts.ts`, junto às demais interfaces exportadas:

```ts
export interface TypingAgent {
  user_id: string
  user_name: string
  at: number
}
```

E na interface `Message`, após `whatsapp_account`:

```ts
  sent_by_user_id?: string
  sent_by_user_name?: string
```

- [ ] **Step 3: Adicionar o estado e as ações**

Em `frontend/src/stores/contacts.ts`, junto aos demais refs de estado:

```ts
  // Record, not Map: Map is not reactive in Vue 3.
  const typingByContact = ref<Record<string, TypingAgent>>({})

  // Timer handles live outside the ref on purpose — they are scheduling
  // detail, not UI state, and must not trigger re-renders.
  const typingTimers: Record<string, ReturnType<typeof setTimeout>> = {}

  const TYPING_TTL_MS = 3000
```

E as ações, antes do `return` do store:

```ts
  function clearTyping(contactId: string) {
    if (typingTimers[contactId]) {
      clearTimeout(typingTimers[contactId])
      delete typingTimers[contactId]
    }
    if (typingByContact.value[contactId]) {
      delete typingByContact.value[contactId]
    }
  }

  // applyAgentTyping records that an agent is typing on a contact and schedules
  // its expiry. Each new event restarts the countdown, so no explicit
  // "stopped typing" signal is needed — and a closed tab or dropped connection
  // cannot leave the indicator stuck.
  function applyAgentTyping(payload: {
    contact_id: string
    user_id: string
    user_name: string
  }) {
    // Never show the current user their own typing
    if (payload.user_id === authStore.user?.id) return

    typingByContact.value[payload.contact_id] = {
      user_id: payload.user_id,
      user_name: payload.user_name,
      at: Date.now()
    }

    if (typingTimers[payload.contact_id]) {
      clearTimeout(typingTimers[payload.contact_id])
    }
    typingTimers[payload.contact_id] = setTimeout(() => {
      delete typingByContact.value[payload.contact_id]
      delete typingTimers[payload.contact_id]
    }, TYPING_TTL_MS)
  }
```

Exportar no `return`: `typingByContact`, `applyAgentTyping`, `clearTyping`.

O store precisa do usuário atual. Se `authStore` ainda não estiver em escopo no arquivo, adicionar no topo do `defineStore`:

```ts
  const authStore = useAuthStore()
```

e o import `import { useAuthStore } from '@/stores/auth'`.

- [ ] **Step 4: Ligar o evento no serviço de WebSocket**

Em `frontend/src/services/websocket.ts`, junto às demais constantes (após `WS_TYPE_CONTACT_STATUS_CHANGED`):

```ts
const WS_TYPE_AGENT_TYPING = 'agent_typing'
```

E no `switch` de `handleMessage`, após o `case WS_TYPE_CONTACT_STATUS_CHANGED`:

```ts
        case WS_TYPE_AGENT_TYPING:
          store.applyAgentTyping(message.payload)
          break
```

- [ ] **Step 5: Verificar tipos**

Run: `cd frontend && npx vue-tsc --noEmit 2>&1 | grep -v AccountDetailView`
Expected: sem saída (o erro pré-existente em `AccountDetailView.vue` é filtrado)

- [ ] **Step 6: Commit**

```bash
git add frontend/src/stores/contacts.ts frontend/src/services/api.ts frontend/src/services/websocket.ts
git commit -m "feat(frontend): track agent typing state with auto-expiry"
```

---

### Task 5: Notificação com throttle no compositor

**Files:**
- Create: `frontend/src/composables/useTypingNotifier.ts`
- Modify: `frontend/src/composables/index.ts`
- Modify: `frontend/src/views/chat/ChatView.vue:2497` (handler do `@input`)

**Interfaces:**
- Consumes: `contactsService.notifyTyping` (Task 4)
- Produces: `useTypingNotifier(): { notifyTyping(contactId: string): void }`

- [ ] **Step 1: Criar o composable**

Criar `frontend/src/composables/useTypingNotifier.ts`:

```ts
import { onUnmounted } from 'vue'
import { contactsService } from '@/services/api'

const THROTTLE_MS = 2500

/**
 * Notifies other agents that the current user is typing, at most once every
 * 2.5s per contact. Errors are swallowed on purpose: a presence indicator must
 * never block the composer.
 */
export function useTypingNotifier() {
  let lastSentAt = 0
  let lastContactId = ''

  function notifyTyping(contactId: string) {
    if (!contactId) return

    const now = Date.now()
    // Switching contacts resets the throttle so the first keystroke in a new
    // conversation is announced immediately.
    if (contactId !== lastContactId) {
      lastContactId = contactId
      lastSentAt = 0
    }
    if (now - lastSentAt < THROTTLE_MS) return

    lastSentAt = now
    contactsService.notifyTyping(contactId).catch(() => {})
  }

  onUnmounted(() => {
    lastSentAt = 0
    lastContactId = ''
  })

  return { notifyTyping }
}
```

- [ ] **Step 2: Exportar o composable**

Em `frontend/src/composables/index.ts`, adicionar na lista de exports:

```ts
export { useTypingNotifier } from './useTypingNotifier'
```

(Se o arquivo usar outro formato de export, seguir o formato já presente nas linhas vizinhas.)

- [ ] **Step 3: Ligar no compositor**

Em `frontend/src/views/chat/ChatView.vue`, no `<script setup>`, junto aos demais composables:

```ts
import { useTypingNotifier } from '@/composables/useTypingNotifier'

const { notifyTyping } = useTypingNotifier()

function handleComposerInput() {
  autoResizeTextarea()
  const contactId = contactsStore.currentContact?.id
  if (contactId) notifyTyping(contactId)
}
```

E no template (linha ~2497), trocar:

```vue
              @input="autoResizeTextarea"
```

por:

```vue
              @input="handleComposerInput"
```

- [ ] **Step 4: Verificar tipos e build**

Run: `cd frontend && npx vue-tsc --noEmit 2>&1 | grep -v AccountDetailView; npm run build 2>&1 | grep -E "error|built in"`
Expected: sem erros de tipo novos; build conclui

- [ ] **Step 5: Commit**

```bash
git add frontend/src/composables/useTypingNotifier.ts frontend/src/composables/index.ts frontend/src/views/chat/ChatView.vue
git commit -m "feat(chat): notify other agents while typing, throttled to 2.5s"
```

---

### Task 6: Indicador de digitação e nome na bolha

**Files:**
- Modify: `frontend/src/views/chat/ChatView.vue` (bolha ~2070, indicador após a lista de mensagens, limpeza na troca de contato)
- Modify: `frontend/src/i18n/locales/en.json`, `frontend/src/i18n/locales/pt-BR.json`

**Interfaces:**
- Consumes: `typingByContact`, `clearTyping` (Task 4); `message.sent_by_user_name` (Task 3)
- Produces: nada de novo

- [ ] **Step 1: Adicionar as strings de i18n**

Em `frontend/src/i18n/locales/en.json`, dentro do objeto `chat` (junto de `"filterAll"`):

```json
    "agentTyping": "{name} is typing…",
```

Em `frontend/src/i18n/locales/pt-BR.json`, mesma chave:

```json
    "agentTyping": "{name} está digitando…",
```

- [ ] **Step 2: Adicionar o computed do agente digitando**

Em `frontend/src/views/chat/ChatView.vue`, no `<script setup>`:

```ts
const typingAgent = computed(() => {
  const contactId = contactsStore.currentContact?.id
  if (!contactId) return null
  return contactsStore.typingByContact[contactId] || null
})
```

- [ ] **Step 3: Adicionar o helper de agrupamento por remetente**

Ainda no `<script setup>`:

```ts
// Show the agent name only when the sender changes from the previous message.
// Repeating the same name on consecutive bubbles is noise.
function shouldShowSenderName(message: Message, index: number): boolean {
  if (message.direction !== 'outgoing' || !message.sent_by_user_name) return false
  if (index === 0) return true
  const previous = contactsStore.messages[index - 1]
  return previous.direction !== 'outgoing' || previous.sent_by_user_name !== message.sent_by_user_name
}
```

Garantir que `Message` esteja importado do store no arquivo (verificar com `grep -n "import type { .*Message" frontend/src/views/chat/ChatView.vue`; se não estiver, adicionar ao import existente de `@/stores/contacts`).

- [ ] **Step 4: Exibir o nome na bolha**

Em `frontend/src/views/chat/ChatView.vue`, logo após a abertura da `div` com `class="chat-bubble ..."` (linha ~2069) e **antes** do bloco de reply preview:

```vue
                <!-- Which agent sent this, shown once per run of messages -->
                <div
                  v-if="shouldShowSenderName(message, index)"
                  class="text-[11px] font-medium text-white/50 light:text-gray-500 mb-0.5"
                >
                  {{ message.sent_by_user_name }}
                </div>
```

- [ ] **Step 5: Exibir o indicador de digitação**

Localizar o fim do laço de mensagens (o fechamento do `v-for` que começa na linha ~2031) e, logo após ele, ainda dentro do container rolável de mensagens, inserir:

```vue
            <!-- Another agent is composing a reply in this conversation -->
            <div
              v-if="typingAgent"
              class="flex items-center gap-1.5 px-3 py-1.5 text-xs text-white/50 light:text-gray-500"
              aria-live="polite"
            >
              <span class="flex gap-0.5">
                <span class="w-1 h-1 rounded-full bg-white/40 light:bg-gray-400 animate-bounce [animation-delay:0ms]" />
                <span class="w-1 h-1 rounded-full bg-white/40 light:bg-gray-400 animate-bounce [animation-delay:150ms]" />
                <span class="w-1 h-1 rounded-full bg-white/40 light:bg-gray-400 animate-bounce [animation-delay:300ms]" />
              </span>
              {{ $t('chat.agentTyping', { name: typingAgent.user_name }) }}
            </div>
```

- [ ] **Step 6: Limpar ao trocar de contato**

Localizar `handleContactClick` em `ChatView.vue` e, no início da função, antes de trocar de contato:

```ts
  const previousId = contactsStore.currentContact?.id
  if (previousId) contactsStore.clearTyping(previousId)
```

- [ ] **Step 7: Verificar tipos e build**

Run: `cd frontend && npx vue-tsc --noEmit 2>&1 | grep -v AccountDetailView; npm run build 2>&1 | grep -E "error|built in"`
Expected: sem erros novos; build conclui

- [ ] **Step 8: Commit**

```bash
git add frontend/src/views/chat/ChatView.vue frontend/src/i18n/locales/en.json frontend/src/i18n/locales/pt-BR.json
git commit -m "feat(chat): show typing indicator and sender name on outgoing bubbles"
```

---

### Task 7: Cobertura e2e

**Files:**
- Create: `frontend/e2e/tests/chat/agent-typing.spec.ts`

**Interfaces:**
- Consumes: tudo das Tasks 1-6
- Produces: nada

- [ ] **Step 1: Escrever o teste e2e**

Criar `frontend/e2e/tests/chat/agent-typing.spec.ts`:

```ts
import { test, expect, request as playwrightRequest } from '@playwright/test'
import { loginAsAdmin, ApiHelper } from '../../helpers'
import { ChatPage } from '../../pages'
import { createTestScope } from '../../framework'

const scope = createTestScope('agent-typing')

/**
 * Agent Typing E2E Tests
 *
 * Two browser contexts share one contact: one agent types, the other must see
 * the indicator appear and then expire on its own after ~3s.
 */
test.describe('Agent Typing', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(90000)

  let contactId: string

  test.beforeAll(async () => {
    const reqContext = await playwrightRequest.newContext()
    const api = new ApiHelper(reqContext)
    await api.loginAsAdmin()
    const contact = await api.createContact(scope.phone(), scope.name('contact'))
    contactId = contact.id
    await reqContext.dispose()
  })

  test('shows and expires the typing indicator for another agent', async ({ browser }) => {
    const watcherCtx = await browser.newContext()
    const typistCtx = await browser.newContext()

    try {
      const watcher = await watcherCtx.newPage()
      const typist = await typistCtx.newPage()

      await loginAsAdmin(watcher)
      await loginAsAdmin(typist)

      await new ChatPage(watcher).goto(contactId)
      await new ChatPage(typist).goto(contactId)

      // Give both sockets time to authenticate and send set_contact
      await watcher.waitForTimeout(1500)

      await typist.locator('textarea').first().fill('escrevendo uma resposta')

      const indicator = watcher.getByText(/está digitando|is typing/)
      await expect(indicator).toBeVisible({ timeout: 5000 })

      // No further events: it must disappear on its own
      await expect(indicator).toBeHidden({ timeout: 8000 })
    } finally {
      await watcherCtx.close()
      await typistCtx.close()
    }
  })

  test('shows the agent name on an outgoing bubble', async ({ page }) => {
    await loginAsAdmin(page)
    const chatPage = new ChatPage(page)
    await chatPage.goto(contactId)

    await page.locator('textarea').first().fill('mensagem do agente')
    await page.keyboard.press('Enter')

    // The bubble carries the sending agent's display name
    await expect(page.getByText('Test Admin').first()).toBeVisible({ timeout: 10000 })
  })
})
```

Nota: o segundo teste envia uma mensagem real. Se o ambiente e2e não tiver uma conta WhatsApp configurada, o envio falha no provedor mas a mensagem ainda é persistida com `SentByUserID` e renderizada — que é o que o teste verifica. Se o envio for bloqueado antes da persistência, substituir por criação da mensagem via SQL, seguindo o padrão de `frontend/e2e/tests/chat/service-window.spec.ts`.

- [ ] **Step 2: Subir o app e rodar o e2e**

```bash
# Build do frontend, embed e binário
cd frontend && npm run build && cd ..
rm -rf internal/frontend/dist/* && cp -r frontend/dist/* internal/frontend/dist/
go build -o whatomate.exe ./cmd/whatomate

# config.toml apontando para um banco e2e limpo (o arquivo está no .gitignore)
WHATOMATE_CONFIG=config.toml ./whatomate.exe server -migrate &
curl -f http://localhost:8080/health

cd frontend
BASE_URL=http://localhost:8080 \
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/whatomate_e2e" \
CI=true npx playwright test tests/chat/agent-typing.spec.ts --reporter=list
```

Expected: 2 passed

- [ ] **Step 3: Encerrar o app e limpar**

```bash
taskkill //F //IM whatomate.exe
rm -f whatomate.exe
```

- [ ] **Step 4: Commit**

```bash
git add frontend/e2e/tests/chat/agent-typing.spec.ts
git commit -m "test(e2e): cover agent typing indicator and bubble sender name"
```

---

## Verificação final antes do PR

- [ ] `go build ./...` limpo
- [ ] `go vet ./...` limpo
- [ ] `go test -p 1 ./...` — apenas as duas falhas pré-existentes
- [ ] `cd frontend && npx vue-tsc --noEmit` — apenas o erro pré-existente em `AccountDetailView.vue`
- [ ] `cd frontend && npm run build` limpo
- [ ] Nenhum broadcast usando `map[string]any` como `WSMessage`
- [ ] `BroadcastToContact` mantém o comportamento antigo (teste de regressão da Task 1 passando)
- [ ] Nenhuma alteração no texto enviado ao cliente no WhatsApp
- [ ] Chaves de i18n presentes em `en.json` **e** `pt-BR.json`
- [ ] PR aberto com base em `development`
