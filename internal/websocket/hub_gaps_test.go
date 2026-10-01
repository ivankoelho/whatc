package websocket_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/stretchr/testify/assert"
)

// --- IsUserOnline ---

func TestHub_IsUserOnline_ReturnsTrueForRegisteredUser(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	userID := uuid.New()

	hub.Register(newTestClient(hub, userID, orgID))
	waitForClientCount(t, hub, 1)

	assert.True(t, hub.IsUserOnline(orgID, userID))
}

func TestHub_IsUserOnline_FalseForUnknownUser(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()

	hub.Register(newTestClient(hub, uuid.New(), orgID))
	waitForClientCount(t, hub, 1)

	assert.False(t, hub.IsUserOnline(orgID, uuid.New()))
}

func TestHub_IsUserOnline_FalseForUnknownOrg(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	userID := uuid.New()

	hub.Register(newTestClient(hub, userID, orgID))
	waitForClientCount(t, hub, 1)

	// Same user but different org → false.
	assert.False(t, hub.IsUserOnline(uuid.New(), userID))
}

func TestHub_IsUserOnline_FalseAfterAllClientsUnregister(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	userID := uuid.New()

	c := newTestClient(hub, userID, orgID)
	hub.Register(c)
	waitForClientCount(t, hub, 1)

	hub.Unregister(c)
	waitForClientCount(t, hub, 0)

	assert.False(t, hub.IsUserOnline(orgID, userID))
}

// --- FilterOnlineUsers ---

func TestHub_FilterOnlineUsers_ReturnsOnlyOnlineSubset(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	online1 := uuid.New()
	online2 := uuid.New()
	offline := uuid.New()

	hub.Register(newTestClient(hub, online1, orgID))
	hub.Register(newTestClient(hub, online2, orgID))
	waitForClientCount(t, hub, 2)

	got := hub.FilterOnlineUsers(orgID, []uuid.UUID{online1, offline, online2})
	assert.ElementsMatch(t, []uuid.UUID{online1, online2}, got)
}

func TestHub_FilterOnlineUsers_NilForUnknownOrg(t *testing.T) {
	hub := newTestHub(t)
	got := hub.FilterOnlineUsers(uuid.New(), []uuid.UUID{uuid.New()})
	assert.Nil(t, got)
}

func TestHub_FilterOnlineUsers_EmptyInputReturnsEmpty(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	hub.Register(newTestClient(hub, uuid.New(), orgID))
	waitForClientCount(t, hub, 1)

	got := hub.FilterOnlineUsers(orgID, nil)
	assert.Empty(t, got)
}

// --- BroadcastToUsers ---

// drainCount returns how many messages are pending on the client's send channel.
func drainCount(c *websocket.Client) int {
	got := 0
	ch := c.SendChan()
	for {
		select {
		case <-ch:
			got++
		default:
			return got
		}
	}
}

func TestHub_BroadcastToUsers_DeliversToEachListedUser(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	user1 := uuid.New()
	user2 := uuid.New()
	user3 := uuid.New() // not in the broadcast list

	c1 := newTestClient(hub, user1, orgID)
	c2 := newTestClient(hub, user2, orgID)
	c3 := newTestClient(hub, user3, orgID)
	hub.Register(c1)
	hub.Register(c2)
	hub.Register(c3)
	waitForClientCount(t, hub, 3)

	hub.BroadcastToUsers(orgID, []uuid.UUID{user1, user2}, websocket.WSMessage{Type: "x"})

	// Give the hub goroutine a moment to dispatch.
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, drainCount(c1))
	assert.Equal(t, 1, drainCount(c2))
	assert.Equal(t, 0, drainCount(c3), "user not in the list must not receive the message")
}

func TestHub_BroadcastToUsers_NilListIsNoop(t *testing.T) {
	hub := newTestHub(t)
	orgID := uuid.New()
	c := newTestClient(hub, uuid.New(), orgID)
	hub.Register(c)
	waitForClientCount(t, hub, 1)

	hub.BroadcastToUsers(orgID, nil, websocket.WSMessage{Type: "noop"})
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 0, drainCount(c))
}

// --- Presence listener ---

type presenceEvent struct {
	org, user uuid.UUID
	online    bool
}

func collectPresence(hub *websocket.Hub) <-chan presenceEvent {
	ch := make(chan presenceEvent, 16)
	hub.SetPresenceListener(func(o, u uuid.UUID, online bool) { ch <- presenceEvent{o, u, online} })
	return ch
}

func expectPresence(t *testing.T, ch <-chan presenceEvent, want presenceEvent) {
	t.Helper()
	select {
	case got := <-ch:
		assert.Equal(t, want, got)
	case <-time.After(2 * time.Second):
		t.Fatalf("no presence event, wanted %+v", want)
	}
}

func expectNoPresence(t *testing.T, ch <-chan presenceEvent) {
	t.Helper()
	select {
	case got := <-ch:
		t.Fatalf("unexpected presence event %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestHub_PresenceListener_FiresOnFirstConnectAndLastDisconnectOnly(t *testing.T) {
	hub := newTestHub(t)
	events := collectPresence(hub)
	orgID, userID := uuid.New(), uuid.New()

	tab1 := newTestClient(hub, userID, orgID)
	hub.Register(tab1)
	expectPresence(t, events, presenceEvent{orgID, userID, true})

	// A second tab is not a presence change.
	tab2 := newTestClient(hub, userID, orgID)
	hub.Register(tab2)
	waitForClientCount(t, hub, 2)
	expectNoPresence(t, events)

	// Closing one tab leaves the user online.
	hub.Unregister(tab1)
	waitForClientCount(t, hub, 1)
	expectNoPresence(t, events)
	assert.True(t, hub.IsUserOnline(orgID, userID))

	// Closing the last one takes them offline.
	hub.Unregister(tab2)
	expectPresence(t, events, presenceEvent{orgID, userID, false})
	assert.False(t, hub.IsUserOnline(orgID, userID))
}

func TestHub_PresenceListener_ReconnectAfterDropFiresOnlineAgain(t *testing.T) {
	hub := newTestHub(t)
	events := collectPresence(hub)
	orgID, userID := uuid.New(), uuid.New()

	c := newTestClient(hub, userID, orgID)
	hub.Register(c)
	expectPresence(t, events, presenceEvent{orgID, userID, true})
	hub.Unregister(c)
	expectPresence(t, events, presenceEvent{orgID, userID, false})

	hub.Register(newTestClient(hub, userID, orgID))
	expectPresence(t, events, presenceEvent{orgID, userID, true})
}
