package handlers_test

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
	"github.com/valyala/fasthttp"
)

// Forwarding to another team: the new owner hears occurrence_changed first and
// the occurrence_alert second, and forwarding again to the same place is no news.
func TestOccurrenceAlerts_ForwardArrivesAfterChangedAndIsIdempotent(t *testing.T) {
	w := newXDWorld(t)
	sac, log, vend := w.u["SAC"], w.u["Log"], w.u["Vendas"]
	occ := w.open(t, sac, w.team["Log"], "Encaminhar com alerta")
	w.on(occ)
	cl, _ := w.move(t, w.app.ClaimOccurrence, log, occ, map[string]any{})
	require.Equal(t, fasthttp.StatusOK, cl)

	hub := websocket.NewHub(testutil.NopLogger())
	go hub.Run()
	hub.SetOccurrenceAuthorizer(func(_, _, _ uuid.UUID) bool { return true })
	hub.SetConversationAuthorizer(func(_, _, _ uuid.UUID) bool { return true })
	client := websocket.NewClient(hub, nil, vend.ID, w.org.ID)
	hub.Register(client)
	for d := time.Now().Add(2 * time.Second); time.Now().Before(d) && hub.GetClientCount() != 1; {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 1, hub.GetClientCount())
	w.app.WSHub = hub

	types := func() (out []string) {
		for {
			select {
			case data := <-client.SendChan():
				var m websocket.WSMessage
				require.NoError(t, json.Unmarshal(data, &m))
				if m.Type == websocket.TypeOccurrenceChanged || m.Type == websocket.TypeOccurrenceAlert {
					out = append(out, m.Type)
				}
			case <-time.After(300 * time.Millisecond):
				return
			}
		}
	}
	types() // drain what the setup produced

	body := map[string]any{"team_id": w.team["Vendas"].ID.String(), "assigned_user_id": vend.ID.String(), "reason": "é de venda"}
	code, raw := w.move(t, w.app.ForwardOccurrence, log, occ, body)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	got := types()
	require.NotEmpty(t, got)
	assert.Equal(t, websocket.TypeOccurrenceChanged, got[0], "changed first")
	assert.Equal(t, websocket.TypeOccurrenceAlert, got[len(got)-1], "alert last")

	pending := func() (n int64) {
		w.app.DB.Model(&models.OccurrenceAlert{}).Where("occurrence_id = ? AND user_id = ? AND dismissed_at IS NULL", occ, vend.ID).Count(&n)
		return
	}
	assert.EqualValues(t, 1, pending())
}
