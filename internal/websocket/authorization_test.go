package websocket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/stretchr/testify/assert"
)

// allowOnly returns an authorizer that only permits the given user for any contact.
func allowOnly(allowed uuid.UUID) websocket.ConversationAuthorizer {
	return func(userID, orgID, contactID uuid.UUID) bool {
		return userID == allowed
	}
}

// 1. new_message reaches only authorized clients.
func TestHub_BroadcastNewMessageToAuthorized_OnlyAuthorizedReceives(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	hub.SetConversationAuthorizer(allowOnly(userA))

	cA := newTestClient(hub, userA, orgID)
	cB := newTestClient(hub, userB, orgID)
	hub.Register(cA)
	hub.Register(cB)
	waitForClientCount(t, hub, 2)

	msg := websocket.WSMessage{Type: websocket.TypeNewMessage, Payload: "hi"}
	hub.BroadcastNewMessageToAuthorized(orgID, contactID, msg)

	assertReceivesMessage(t, cA, websocket.TypeNewMessage)
	assertNoMessage(t, cB)
}

// 2. A contact-targeted (notes-style) broadcast is gated by authorization even
// when the unauthorized client has explicitly selected the contact.
func TestHub_BroadcastToContact_GatedByAuthorization(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactID := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	hub.SetConversationAuthorizer(allowOnly(userA))

	cA := newTestClient(hub, userA, orgID)
	cB := newTestClient(hub, userB, orgID)
	// Both are viewing the contact; only A is authorized.
	websocket.ClientSetCurrentContact(cA, &contactID)
	websocket.ClientSetCurrentContact(cB, &contactID)
	hub.Register(cA)
	hub.Register(cB)
	waitForClientCount(t, hub, 2)

	msg := websocket.WSMessage{Type: websocket.TypeConversationNoteCreated, Payload: "note"}
	hub.BroadcastToContact(orgID, contactID, msg)

	assertReceivesMessage(t, cA, websocket.TypeConversationNoteCreated)
	assertNoMessage(t, cB)
}

// 3. IgnoreContactFilter: an authorized client receives new_message even while
// viewing a DIFFERENT contact.
func TestHub_BroadcastNewMessageToAuthorized_IgnoresCurrentContact(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactID := uuid.New()
	otherContact := uuid.New()
	userA := uuid.New()

	hub.SetConversationAuthorizer(allowOnly(userA))

	cA := newTestClient(hub, userA, orgID)
	websocket.ClientSetCurrentContact(cA, &otherContact)
	hub.Register(cA)
	waitForClientCount(t, hub, 1)

	msg := websocket.WSMessage{Type: websocket.TypeNewMessage, Payload: "sidebar"}
	hub.BroadcastNewMessageToAuthorized(orgID, contactID, msg)

	assertReceivesMessage(t, cA, websocket.TypeNewMessage)
}

// 4. handleSetContact refuses to subscribe an unauthorized client.
func TestClient_HandleSetContact_DeniedLeavesCurrentContactNil(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactX := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	// Authorizer allows A, denies B.
	hub.SetConversationAuthorizer(allowOnly(userA))

	cB := newTestClient(hub, userB, orgID)

	websocket.ClientHandleSetContactForTest(cB, map[string]any{
		"contact_id": contactX.String(),
	})

	assert.Nil(t, websocket.ClientCurrentContact(cB),
		"unauthorized client must not become subscribed to the contact")
}

// 4b. handleSetContact allows an authorized client to subscribe.
func TestClient_HandleSetContact_AllowedSetsCurrentContact(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactX := uuid.New()
	userA := uuid.New()

	hub.SetConversationAuthorizer(allowOnly(userA))

	cA := newTestClient(hub, userA, orgID)

	websocket.ClientHandleSetContactForTest(cA, map[string]any{
		"contact_id": contactX.String(),
	})

	got := websocket.ClientCurrentContact(cA)
	if assert.NotNil(t, got, "authorized client should be subscribed") {
		assert.Equal(t, contactX, *got)
	}
}

// 5. Nil-authorizer regression: with no authorizer, BroadcastToContact behaves
// exactly as before — a client with no contact selected still receives it.
func TestHub_NilAuthorizer_BroadcastToContactUnchanged(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	contactID := uuid.New()

	noContact := newTestClient(hub, uuid.New(), orgID)
	websocket.ClientSetCurrentContact(noContact, nil)
	hub.Register(noContact)
	waitForClientCount(t, hub, 1)

	msg := websocket.WSMessage{Type: websocket.TypeNewMessage, Payload: "legacy"}
	hub.BroadcastToContact(orgID, contactID, msg)

	assertReceivesMessage(t, noContact, websocket.TypeNewMessage)
}
