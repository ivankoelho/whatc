package handlers_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// WhatsApp simulation (no real message leaves the process: the provider is a local fake). A customer talks to
// SAC or Vendas, the case is forwarded to Logística, internal updates never reach the customer by accident, and the
// external reply only goes out through someone who may write to that conversation.
func TestHomologation_WhatsAppSimulation(t *testing.T) {
	for _, origin := range []string{"SAC", "Vendas"} {
		t.Run("iniciado por "+origin, func(t *testing.T) {
			w := newXDWorld(t)
			w.run() // park intents left by other tests
			from, log := w.u[origin], w.u["Log"]
			base := w.meta.count()

			// the customer wrote to the origin's number; the case is opened and forwarded to Logística
			contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
			w.customerWrote(t, contact.ID)
			code, raw := chatDo(t, w.app.CreateOccurrence, from.OrganizationID, from.ID, map[string]any{
				"contact_id": contact.ID.String(), "title": "Pedido 45872 - " + origin, "team_id": w.team["Log"].ID.String(),
				"order_ref": "45872", "priority": "high"}, nil, nil)
			require.Equal(t, fasthttp.StatusOK, code, string(raw))
			var occ models.Occurrence
			require.NoError(t, w.app.DB.Where("organization_id = ? AND title = ?", w.org.ID, "Pedido 45872 - "+origin).First(&occ).Error)
			w.on(occ.ID)

			t.Run("protocolo vinculado ao caso", func(t *testing.T) {
				assert.NotEmpty(t, occ.ProtocolNumber)
				var n models.OccurrenceNotification
				require.NoError(t, w.app.DB.Where("occurrence_id = ?", occ.ID).First(&n).Error, "the customer notice is an intent persisted with the case")
				assert.Equal(t, base, w.meta.count(), "creating the case never calls the provider")
			})

			t.Run("o aviso ao cliente sai uma vez, pelo número da Logística", func(t *testing.T) {
				assert.Equal(t, 1, w.run())
				assert.Equal(t, base+1, w.meta.count())
				assert.Contains(t, w.meta.last().Path, w.account.PhoneID)
				assert.Equal(t, 0, w.run())
				assert.Equal(t, base+1, w.meta.count(), "no duplicate")
			})

			t.Run("atualização interna nunca vai ao cliente", func(t *testing.T) {
				sent := w.meta.count()
				require.Equal(t, fasthttp.StatusOK, w.note(t, log, occ.ID, "nota interna: cliente irritado, priorizar"))
				cc, rr := chatDo(t, w.app.GetOccurrenceChat, log.OrganizationID, log.ID, nil, map[string]string{"id": occ.ID.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, cc)
				ch := decodeChat[occChatState](t, rr).ChannelID
				cc, _ = chatDo(t, w.app.SendChatMessage, log.OrganizationID, log.ID, map[string]any{"body": "discussão: ligar para a transportadora"}, map[string]string{"id": ch.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, cc)
				c2, _ := chatDo(t, w.app.ClaimOccurrence, log.OrganizationID, log.ID, map[string]any{}, map[string]string{"id": occ.ID.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, c2)
				c2, _ = chatDo(t, w.app.ChangeOccurrenceStage, log.OrganizationID, log.ID, map[string]any{"stage_id": w.stage(t, "in_progress").String()}, map[string]string{"id": occ.ID.String()}, nil)
				require.Equal(t, fasthttp.StatusOK, c2)
				w.run()
				assert.Equal(t, sent, w.meta.count(), "notes, discussion, claim and stage change send nothing to the customer")
			})

			t.Run("resposta externa só por quem pode", func(t *testing.T) {
				// the customer answers on the logistics number: the 24h window opens
				require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID,
					WhatsAppAccount: w.account.Name, ContactID: occ.ContactID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText,
					Content: "pode ligar?", Status: models.MessageStatusDelivered}).Error)
				sent := w.meta.count()
				send := func(u *models.User) int {
					c, _ := chatDo(t, w.app.SendOccurrenceWhatsApp, u.OrganizationID, u.ID, map[string]any{"content": "Claro, ligamos já."}, map[string]string{"id": occ.ID.String()}, nil)
					return c
				}
				assert.Equal(t, fasthttp.StatusForbidden, send(from), "the requester only follows: following is not writing to the customer")
				assert.Equal(t, fasthttp.StatusForbidden, send(w.vendasOutra), "another store")
				assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusNotFound}, send(w.foreign), "another organization")
				assert.Equal(t, sent, w.meta.count(), "refused attempts sent nothing")
				assert.Equal(t, fasthttp.StatusOK, send(log), "the person who works the case, with chat:write")
				assert.Equal(t, sent+1, w.meta.count())
				assert.Contains(t, w.meta.last().Path, w.account.PhoneID)
			})

			t.Run("histórico preservado", func(t *testing.T) {
				code, body, _ := w.export(t, log, occ.ID, []string{"full"}, "csv")
				_ = body
				// "full" cannot be CSV: ask for the sections one by one
				code, body, _ = w.export(t, log, occ.ID, []string{"whatsapp"}, "csv")
				require.Equal(t, fasthttp.StatusOK, code)
				text := string(body)
				assert.Contains(t, text, "pode ligar?")
				assert.Contains(t, text, "Claro, ligamos já.")
				code, body, _ = w.export(t, log, occ.ID, []string{"history"}, "csv")
				require.Equal(t, fasthttp.StatusOK, code)
				assert.True(t, strings.Contains(string(body), "nota interna") || strings.Contains(string(body), "Nota interna"))
				// the requester's report carries the case history but never the conversation
				code, _, _ = w.export(t, from, occ.ID, []string{"whatsapp"}, "csv")
				assert.Equal(t, fasthttp.StatusForbidden, code)
			})
		})
	}
}
