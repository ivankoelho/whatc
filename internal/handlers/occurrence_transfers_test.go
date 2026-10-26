package handlers_test

import (
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// L1: forward and return a case that already has a team. Same protocol, nothing lost, access follows the new
// responsibility, and the four notions (forward, return, support, follow) stay distinct.

func (w *xdWorld) move(t *testing.T, h func(*fastglue.Request) error, u *models.User, occ uuid.UUID, body map[string]any) (int, []byte) {
	t.Helper()
	return chatDo(t, h, u.OrganizationID, u.ID, body, map[string]string{"id": occ.String()}, nil)
}

func TestTransfer_ForwardKeepsTheCaseAndMovesAccess(t *testing.T) {
	w := newXDWorld(t)
	sac, log, vend := w.u["SAC"], w.u["Log"], w.u["Vendas"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - encaminhar")
	w.on(occ)
	protocol := w.protocolOf(t, occ)
	f := w.upl2(t, log, occ, "laudo.pdf", pdfBytes())
	require.Equal(t, fasthttp.StatusOK, w.note(t, log, occ, "Em apuração"))
	cc, rr := chatDo(t, w.app.GetOccurrenceChat, log.OrganizationID, log.ID, nil, map[string]string{"id": occ.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, cc)
	ch := decodeChat[occChatState](t, rr).ChannelID
	_, _ = chatDo(t, w.app.SendChatMessage, log.OrganizationID, log.ID, map[string]any{"body": "discussão antes da transferência"}, map[string]string{"id": ch.String()}, nil)
	w.chID = ch
	cl, _ := w.move(t, w.app.ClaimOccurrence, log, occ, map[string]any{})
	require.Equal(t, fasthttp.StatusOK, cl)

	// Logística forwards to Vendas (a person of that team receives it)
	code, raw := w.move(t, w.app.ForwardOccurrence, log, occ, map[string]any{"team_id": w.team["Vendas"].ID.String(), "assigned_user_id": vend.ID.String(), "reason": "Pedido é de venda, não de entrega"})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, occ).Error)
	assert.Equal(t, protocol, o.ProtocolNumber, "same protocol, no new case")
	assert.Equal(t, w.team["Vendas"].ID, *o.TeamID)
	assert.Equal(t, vend.ID, *o.AssignedUserID)
	var n int64
	w.app.DB.Model(&models.Occurrence{}).Where("organization_id = ?", w.org.ID).Where("title = ?", "Pedido 45872 - encaminhar").Count(&n)
	assert.EqualValues(t, 1, n)

	// the new owner works it and still has everything
	code, _ = w.get(t, vend, occ)
	assert.Equal(t, fasthttp.StatusOK, code)
	dc, _, _ := w.fileCall(t, w.app.DownloadOccurrenceFile, vend, occ, f.ID.String(), "")
	assert.Equal(t, fasthttp.StatusOK, dc, "the new owner works the case, so the case files come with it")
	sc, _, _ := w.fileCall(t, w.app.DownloadOccurrenceFile, sac, occ, f.ID.String(), "")
	assert.Equal(t, fasthttp.StatusForbidden, sc, "while the requester, who only follows, still cannot read a restricted file")
	_, msgs := w.read(t, vend.ID)
	assert.Len(t, msgs, 1, "the internal discussion goes with the case")
	pg, page := w.history(t, vend, occ, "")
	require.Equal(t, fasthttp.StatusOK, pg)
	assert.GreaterOrEqual(t, len(page.Events), 3, "the history goes with the case")

	// the previous team no longer works it, but the person who handed it over keeps following and may comment
	_, st := w.get(t, log, occ)
	_ = st
	assert.Equal(t, fasthttp.StatusOK, w.note(t, log, occ, "Passei adiante, mas acompanho."), "the sender still follows (comment)")
	cs, _ := chatDo(t, w.app.ChangeOccurrenceStage, log.OrganizationID, log.ID, map[string]any{"stage_id": w.stage(t, "in_progress").String()}, map[string]string{"id": occ.String()}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, cs, "but cannot move the case any more")
	assert.Equal(t, fasthttp.StatusForbidden, w.whatsapp(t, log, occ), "following is not access to the customer's WhatsApp")
	cf, _ := w.move(t, w.app.ForwardOccurrence, log, occ, map[string]any{"team_id": w.team["Log"].ID.String(), "reason": "tentando puxar de volta"})
	assert.Equal(t, fasthttp.StatusForbidden, cf, "the sender cannot forward it again once it is no longer theirs")

	// a person of the old team who was not involved loses sight of it
	other := w.apoio["Log"]
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Log"].ID, UserID: other.ID}).Error)
	code, _ = w.get(t, other, occ)
	assert.NotEqual(t, fasthttp.StatusOK, code, "old team members lose access")

	// forwarding grants no view_all: the new owner still cannot open unrelated cases
	stranger := w.open(t, sac, w.team["Log"], "outro caso")
	code, _ = w.get(t, vend, stranger)
	assert.NotEqual(t, fasthttp.StatusOK, code)
}

func TestTransfer_ReturnNeedsReasonAndGoesBackWithoutPingPong(t *testing.T) {
	w := newXDWorld(t)
	sac, log := w.u["SAC"], w.u["Log"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - devolver")
	w.on(occ)

	// nothing to return before any forward beyond the opening? The opening IS a forward from the requester
	code, _ := w.move(t, w.app.ReturnOccurrence, log, occ, map[string]any{"reason": ""})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "reason is mandatory")
	code, _ = w.move(t, w.app.ReturnOccurrence, sac, occ, map[string]any{"reason": "eu mesmo devolvo"})
	assert.Equal(t, fasthttp.StatusForbidden, code, "the requester only follows")

	code, raw := w.move(t, w.app.ReturnOccurrence, log, occ, map[string]any{"reason": "Cliente pediu cancelamento"})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, occ).Error)
	assert.Nil(t, o.TeamID, "back with its requester")
	code, _ = w.get(t, sac, occ)
	assert.Equal(t, fasthttp.StatusOK, code)
	// the requester can work it again (a case without a team is theirs)
	assert.Equal(t, fasthttp.StatusOK, w.note(t, sac, occ, "Recebi de volta."))
	// the receiver cannot bounce it again: no cycles
	code, _ = w.move(t, w.app.ReturnOccurrence, log, occ, map[string]any{"reason": "devolvendo de novo"})
	assert.Contains(t, []int{fasthttp.StatusForbidden, fasthttp.StatusConflict}, code)
	code, _ = w.move(t, w.app.ReturnOccurrence, sac, occ, map[string]any{"reason": "devolvendo ao ninguém"})
	assert.Equal(t, fasthttp.StatusConflict, code, "nothing to return after a return")

	// the timeline says who did what and why
	var ev models.OccurrenceEvent
	require.NoError(t, w.app.DB.Where("occurrence_id = ? AND type = ?", occ, models.OccurrenceEventReturned).First(&ev).Error)
	assert.Equal(t, "Cliente pediu cancelamento", ev.Content)
	require.NotNil(t, ev.CreatedByID)
	assert.Equal(t, log.ID, *ev.CreatedByID)
}

func TestTransfer_ValidationAndSecurity(t *testing.T) {
	w := newXDWorld(t)
	sac, log := w.u["SAC"], w.u["Log"]
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - validação")
	w.on(occ)
	fwd := func(u *models.User, body map[string]any) int {
		c, _ := w.move(t, w.app.ForwardOccurrence, u, occ, body)
		return c
	}
	vendas := w.team["Vendas"].ID.String()

	assert.Equal(t, fasthttp.StatusBadRequest, fwd(log, map[string]any{"team_id": vendas}), "reason required")
	assert.Equal(t, fasthttp.StatusBadRequest, fwd(log, map[string]any{"team_id": "xx", "reason": "motivo válido"}))
	assert.Equal(t, fasthttp.StatusConflict, fwd(log, map[string]any{"team_id": w.team["Log"].ID.String(), "reason": "mesma equipe"}), "no transfer to where it already is")
	assert.Equal(t, fasthttp.StatusNotFound, fwd(log, map[string]any{"team_id": uuid.NewString(), "reason": "equipe inexistente"}))
	other := newIsoWorld(t) // a team of another organization
	assert.Equal(t, fasthttp.StatusNotFound, fwd(log, map[string]any{"team_id": other.teamSAC.ID.String(), "reason": "equipe de outra organização"}))

	require.NoError(t, w.app.DB.Model(w.team["Vendas"]).Update("is_active", false).Error)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, fwd(log, map[string]any{"team_id": vendas, "reason": "equipe inativa"}))
	require.NoError(t, w.app.DB.Model(w.team["Vendas"]).Update("is_active", true).Error)

	inactive := w.apoio["Vendas"]
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Vendas"].ID, UserID: inactive.ID}).Error)
	require.NoError(t, w.app.DB.Model(inactive).Update("is_active", false).Error)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, fwd(log, map[string]any{"team_id": vendas, "assigned_user_id": inactive.ID.String(), "reason": "usuário inativo"}))
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, fwd(log, map[string]any{"team_id": vendas, "assigned_user_id": w.u["SAC"].ID.String(), "reason": "fora da equipe"}), "the person must belong to the destination team")
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, fwd(log, map[string]any{"team_id": vendas, "assigned_user_id": w.foreign.ID.String(), "reason": "outra organização"}))

	// who may forward: those who work it; not an outsider, not a follower, not a support person
	assert.NotEqual(t, fasthttp.StatusOK, fwd(w.vendasOutra, map[string]any{"team_id": vendas, "reason": "outra loja"}))
	assert.NotEqual(t, fasthttp.StatusOK, fwd(w.foreign, map[string]any{"team_id": vendas, "reason": "outra org"}))
	assert.Equal(t, fasthttp.StatusForbidden, fwd(sac, map[string]any{"team_id": vendas, "reason": "só acompanho"}))
	ap := w.apoio["Log"]
	c, _ := chatDo(t, w.app.AddOccurrenceParticipant, log.OrganizationID, log.ID, map[string]any{"user_id": ap.ID.String(), "role": "support", "department_id": w.dept["Logística"].ID.String()}, map[string]string{"id": occ.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, c)
	assert.Equal(t, fasthttp.StatusForbidden, fwd(ap, map[string]any{"team_id": vendas, "reason": "sou apoio"}), "support does not transfer responsibility")
	// and a profile without occurrences:write
	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, occ).Error)
	assert.Equal(t, w.team["Log"].ID, *o.TeamID, "nothing moved by any refused attempt")

	// a closed case must be reopened first
	require.NoError(t, w.app.DB.Model(&models.Occurrence{}).Where("id = ?", occ).Update("general_state", models.GeneralStateResolved).Error)
	assert.Equal(t, fasthttp.StatusConflict, fwd(log, map[string]any{"team_id": vendas, "reason": "caso encerrado"}))
}

func TestTransfer_SimultaneousMovesOnlyOneWins(t *testing.T) {
	w := newXDWorld(t)
	sac, log := w.u["SAC"], w.u["Log"]
	colleague := w.apoio["Log"]
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.team["Log"].ID, UserID: colleague.ID}).Error)
	occ := w.open(t, sac, w.team["Log"], "Pedido 45872 - corrida")
	w.on(occ)
	targets := []*models.Team{w.team["Vendas"], w.team["SupLoja"]}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, u := range []*models.User{log, colleague} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = chatDo(t, w.app.ForwardOccurrence, u.OrganizationID, u.ID, map[string]any{"team_id": targets[i].ID.String(), "reason": "corrida de encaminhamento"}, map[string]string{"id": occ.String()}, nil)
		}()
	}
	wg.Wait()
	ok, other := 0, 0
	for _, c := range codes {
		switch c {
		case fasthttp.StatusOK:
			ok++
		default:
			other++
		}
	}
	assert.Equal(t, 1, ok, "exactly one move wins: %v", codes)
	assert.Equal(t, 1, other)
	var moves int64
	w.app.DB.Model(&models.OccurrenceTransfer{}).Where("occurrence_id = ? AND reason = ?", occ, "corrida de encaminhamento").Count(&moves)
	assert.EqualValues(t, 1, moves, "one record, no duplicate transfer")
}
