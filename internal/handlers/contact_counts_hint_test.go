package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hubClient(t *testing.T, hub *websocket.Hub, orgID uuid.UUID, want int) *websocket.Client {
	t.Helper()
	c := websocket.NewClient(hub, nil, uuid.New(), orgID)
	hub.Register(c)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && hub.GetClientCount() != want {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, want, hub.GetClientCount())
	return c
}

// A transfer can take a conversation OUT of someone's scope; that user no
// longer passes the per-contact authorizer, so the only way to make their
// counter recount is an id-less org-wide hint. It must reach clients of the
// org only, and must not carry any contact data.
func TestTransferBroadcasts_SendCountsHintToOrgOnly(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	// The hub in newSLATestApp has no authorizer: per-contact events are
	// dropped/allowed by its own rules, which is irrelevant to the hint.
	inOrg := hubClient(t, app.WSHub, org.ID, 1)
	outsider := hubClient(t, app.WSHub, other.ID, 2)

	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	transfer := &models.AgentTransfer{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID, ContactID: contact.ID,
		PhoneNumber: contact.PhoneNumber, Status: models.TransferStatusActive,
	}

	cases := map[string]func(){
		"created": func() { app.broadcastTransferCreated(transfer, contact) },
		"assign":  func() { app.broadcastTransferAssigned(transfer) },
		"resume":  func() { app.broadcastTransferResumed(transfer) },
	}
	for name, fire := range cases {
		t.Run(name, func(t *testing.T) {
			fire()
			var hint *websocket.WSMessage
			deadline := time.After(2 * time.Second)
		loop:
			for {
				select {
				case data := <-inOrg.SendChan():
					var m websocket.WSMessage
					require.NoError(t, json.Unmarshal(data, &m))
					if m.Type == websocket.TypeContactCountsChanged {
						hint = &m
						break loop
					}
				case <-deadline:
					break loop
				}
			}
			require.NotNil(t, hint, "no contact_counts_changed reached the org")
			raw, _ := json.Marshal(hint.Payload)
			assert.JSONEq(t, `{}`, string(raw), "the hint must not carry contact data")
			assert.NotContains(t, string(raw), contact.ID.String())

			select {
			case data := <-outsider.SendChan():
				var m websocket.WSMessage
				_ = json.Unmarshal(data, &m)
				assert.NotEqual(t, websocket.TypeContactCountsChanged, m.Type, "hint leaked to another org")
			case <-time.After(150 * time.Millisecond):
			}
		})
	}
}
