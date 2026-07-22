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

// 6. reaction/status-style events: broadcastReactionUpdate and the
// TypeStatusUpdate sites in handlers now route through
// BroadcastToAuthorizedViewers (an alias for BroadcastNewMessageToAuthorized)
// instead of BroadcastToOrg, so they're gated by the same authorizer as
// new_message. Confirms a denied user gets nothing for these event types.
//
// The conversation-lifecycle events also now use this same gated path:
// TypeAgentTransfer / TypeAgentTransferResume / TypeAgentTransferAssign
// (internal/handlers/agent_transfers.go), TypeTransferEscalation and the SLA
// expiry update (internal/handlers/sla_processor.go), and
// TypeContactStatusChanged (internal/handlers/contact_status.go). They were
// previously BroadcastToOrg and are now BroadcastToAuthorizedViewers, so the
// TypeAgentTransfer assertion below stands in for all of them — a denied user
// receives none of these conversation-naming events.
func TestHub_BroadcastToAuthorizedViewers_ReactionAndStatusGated(t *testing.T) {
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

	reactionMsg := websocket.WSMessage{Type: websocket.TypeReactionUpdate, Payload: "reaction"}
	hub.BroadcastToAuthorizedViewers(orgID, contactID, reactionMsg)

	assertReceivesMessage(t, cA, websocket.TypeReactionUpdate)
	assertNoMessage(t, cB)

	statusMsg := websocket.WSMessage{Type: websocket.TypeStatusUpdate, Payload: "status"}
	hub.BroadcastToAuthorizedViewers(orgID, contactID, statusMsg)

	assertReceivesMessage(t, cA, websocket.TypeStatusUpdate)
	assertNoMessage(t, cB)

	// Conversation-lifecycle events (agent transfer created/resumed/assigned,
	// SLA escalation/expiry, contact-status change) route through the same
	// gated method. A transfer event stands in for the whole family.
	transferMsg := websocket.WSMessage{Type: websocket.TypeAgentTransfer, Payload: "transfer"}
	hub.BroadcastToAuthorizedViewers(orgID, contactID, transferMsg)

	assertReceivesMessage(t, cA, websocket.TypeAgentTransfer)
	assertNoMessage(t, cB)
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
