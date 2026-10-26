package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The conversation on the case's channel belongs to those who work the case.
func TestCaseWhatsApp_ConversationIsForThoseWhoWorkTheCase(t *testing.T) {
	w := newChatWorld(t)
	w.run() // the receipt template goes out on the logistics account
	require.Equal(t, 1, w.meta.count())

	get := func(user uuid.UUID) (int, string) {
		c, b := w.call(t, w.app.GetOccurrenceWhatsApp, user, w.occID, nil, nil)
		return c, string(b)
	}
	// Those who work it see the exchange on the logistics account, with its state.
	for _, u := range []*models.User{w.logPorto, w.supPorto, w.supSAC} {
		code, body := get(u.ID)
		require.Equal(t, fasthttp.StatusOK, code, u.FullName)
		assert.Contains(t, body, `"restricted":false`)
		assert.Contains(t, body, w.account.Name, u.FullName)
		assert.Contains(t, body, `"status":"sent"`, "the message is shown with its delivery state")
	}
	// The SAC that forwarded it only gets the fact that it is restricted.
	code, body := get(w.sac.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, body, `"restricted":true`)
	assert.NotContains(t, body, "messages")
	assert.NotContains(t, body, "Olá")
	// What the customer exchanged on this channel BEFORE the case existed is not part of it.
	old := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID,
		WhatsAppAccount: w.account.Name, ContactID: w.contactOf(t), Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText,
		Content: "ASSUNTO-ANTIGO-OUTRO-CHAMADO", Status: models.MessageStatusRead}
	require.NoError(t, w.app.DB.Create(old).Error)
	require.NoError(t, w.app.DB.Model(old).Update("created_at", time.Now().Add(-48*time.Hour)).Error)
	_, oldBody := get(w.logPorto.ID)
	assert.NotContains(t, oldBody, "ASSUNTO-ANTIGO-OUTRO-CHAMADO")
	// Another store cannot even ask.
	code, _ = get(w.logOutra.ID)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	// Conversations on any other account stay out: an inbound on the SAC's own account is not shown.
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID,
		WhatsAppAccount: "SAC-9600", ContactID: w.contactOf(t), Direction: models.DirectionIncoming, MessageType: models.MessageTypeText,
		Content: "SEGREDO-DO-CHAT-DO-SAC", Status: models.MessageStatusDelivered}).Error)
	_, body = get(w.logPorto.ID)
	assert.NotContains(t, body, "SEGREDO-DO-CHAT-DO-SAC")
}

func (w *chatWorld) contactOf(t *testing.T) uuid.UUID {
	var occ models.Occurrence
	require.NoError(t, w.app.DB.First(&occ, w.occID).Error)
	return occ.ContactID
}

func TestCaseWhatsApp_ReplyNeedsTheWindowAndGoesOutOnTheLogisticsAccount(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	baseline := w.meta.count()
	send := func(user uuid.UUID, body map[string]any) int {
		c, _ := w.call(t, w.app.SendOccurrenceWhatsApp, user, w.occID, body, nil)
		return c
	}

	assert.Equal(t, fasthttp.StatusForbidden, send(w.sac.ID, map[string]any{"content": "oi"}), "following is not writing to the customer")
	assert.Equal(t, fasthttp.StatusForbidden, send(w.logOutra.ID, map[string]any{"content": "oi"}))
	assert.Equal(t, fasthttp.StatusBadRequest, send(w.logPorto.ID, map[string]any{}))
	assert.Equal(t, fasthttp.StatusConflict, send(w.logPorto.ID, map[string]any{"content": "oi"}), "the customer has not written on the logistics number: only templates")
	assert.Equal(t, baseline, w.meta.count())

	// The customer answers on the logistics number: the window opens.
	require.NoError(t, w.app.DB.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID,
		WhatsAppAccount: w.account.Name, ContactID: w.contactOf(t), Direction: models.DirectionIncoming, MessageType: models.MessageTypeText,
		Content: "pode ligar?", Status: models.MessageStatusDelivered}).Error)
	require.Equal(t, fasthttp.StatusOK, send(w.logPorto.ID, map[string]any{"content": "Claro, ligamos já."}))
	assert.Equal(t, baseline+1, w.meta.count())
	assert.Contains(t, w.meta.last().Path, w.account.PhoneID, "the logistics number, not the SAC's")

	// The conversation's owner did not change.
	var c models.Contact
	require.NoError(t, w.app.DB.First(&c, w.contactOf(t)).Error)
	assert.Equal(t, w.account.Name == c.WhatsAppAccount, false)

	// Old window: closed again.
	require.NoError(t, w.app.DB.Model(&models.Message{}).Where("whats_app_account = ? AND direction = ?", w.account.Name, models.DirectionIncoming).
		Update("created_at", time.Now().Add(-30*time.Hour)).Error)
	assert.Equal(t, fasthttp.StatusConflict, send(w.logPorto.ID, map[string]any{"content": "e então?"}))
}

func TestCaseWhatsApp_ManualTemplatesFollowTheSameRules(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	w.template(t, w.account.Name, "pedido_a_caminho", "pt_BR", "UTILITY", "APPROVED", "Seu pedido {{1}} está a caminho.")
	w.template(t, w.account.Name, "oferta_geral", "pt_BR", "MARKETING", "APPROVED", "Oferta {{1}}")
	n0 := w.meta.count()
	tpl := func(name string, params ...string) int {
		c, _ := w.call(t, w.app.SendOccurrenceWhatsApp, w.logPorto.ID, w.occID,
			map[string]any{"template": name, "language": "pt_BR", "params": params}, nil)
		return c
	}
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, tpl("oferta_geral", "x"), "marketing is not sent by hand from a case")
	assert.Equal(t, fasthttp.StatusNotFound, tpl("nao_existe"))
	assert.Equal(t, fasthttp.StatusBadRequest, tpl("pedido_a_caminho"), "parameters are required")
	assert.Equal(t, fasthttp.StatusBadRequest, tpl("pedido_a_caminho", " "))
	assert.Equal(t, n0, w.meta.count())
	assert.Equal(t, fasthttp.StatusOK, tpl("pedido_a_caminho", "45872"))
	assert.Equal(t, "pedido_a_caminho", w.meta.last().Template)
	assert.Equal(t, []string{"45872"}, w.meta.last().Params)

	// Listing shows the usable templates, not the marketing one.
	_, body := w.call(t, w.app.GetOccurrenceWhatsApp, w.logPorto.ID, w.occID, nil, nil)
	assert.Contains(t, string(body), "pedido_a_caminho")
	assert.NotContains(t, string(body), "oferta_geral")
}

func TestCaseWhatsApp_ProviderFailureIsReportedNotHidden(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	w.template(t, w.account.Name, "aviso", "pt_BR", "UTILITY", "APPROVED", "Aviso {{1}}")
	w.meta.setMode("400")
	c, _ := w.call(t, w.app.SendOccurrenceWhatsApp, w.logPorto.ID, w.occID,
		map[string]any{"template": "aviso", "language": "pt_BR", "params": []string{"x"}}, nil)
	assert.Equal(t, fasthttp.StatusBadGateway, c)
	_, body := w.call(t, w.app.GetOccurrenceWhatsApp, w.logPorto.ID, w.occID, nil, nil)
	assert.Contains(t, string(body), `"status":"failed"`, "the failed attempt shows in the conversation")
}

// Supervising a case never opens the customer's conversation by itself: the
// explicit chat permission is required on top of it.
func TestCaseWhatsApp_NeedsExplicitChatPermission(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	perms := []string{"occurrences:read", "occurrences:write", "conversations:view_all"}
	noChat := chatUser(t, w.app, w.org.ID, perms)
	withChat := chatUser(t, w.app, w.org.ID, append([]string{"chat:read", "chat:write"}, perms...))

	_, body := w.call(t, w.app.GetOccurrenceWhatsApp, noChat.ID, w.occID, nil, nil)
	assert.Contains(t, string(body), `"restricted":true`)
	assert.NotContains(t, string(body), `"messages"`)
	code, _ := w.call(t, w.app.SendOccurrenceWhatsApp, noChat.ID, w.occID, map[string]any{"content": "oi"}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)

	_, body = w.call(t, w.app.GetOccurrenceWhatsApp, withChat.ID, w.occID, nil, nil)
	assert.Contains(t, string(body), `"restricted":false`)
}

// Supervising every case (occurrences:view_all) shows the case, its internal
// discussion and indicators, but not the customer's conversation on a channel the
// supervisor was not authorized for. Authorization is by team (the case's team or
// the account's default team) or by the explicit conversations:view_all.
func TestCaseWhatsApp_CaseSupervisionDoesNotOpenOtherTeamsChannel(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	sup := chatUser(t, w.app, w.org.ID, []string{"occurrences:read", "occurrences:write", "occurrences:view_all",
		"chat:read", "chat:write", "conversations:view_team", "team_chat:read", "team_chat:write"})

	// The case itself and the internal discussion stay supervised.
	code, _ := w.call(t, w.app.GetOccurrence, sup.ID, w.occID, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, fasthttp.StatusOK, w.state2(t, sup.ID))

	// The Logistics conversation is not theirs.
	_, body := w.call(t, w.app.GetOccurrenceWhatsApp, sup.ID, w.occID, nil, nil)
	assert.Contains(t, string(body), `"restricted":true`)
	assert.NotContains(t, string(body), `"messages"`)
	code, _ = w.call(t, w.app.SendOccurrenceWhatsApp, sup.ID, w.occID, map[string]any{"content": "oi"}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)

	// Joining the team that owns the account is the explicit authorization.
	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, Name: "Dona da conta", IsActive: true}
	require.NoError(t, w.app.DB.Create(team).Error)
	require.NoError(t, w.app.DB.Model(w.account).Update("default_team_id", team.ID).Error)
	require.NoError(t, w.app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: sup.ID, Role: "agent"}).Error)
	_, body = w.call(t, w.app.GetOccurrenceWhatsApp, sup.ID, w.occID, nil, nil)
	assert.Contains(t, string(body), `"restricted":false`)
}

func (w *chatWorld) state2(t *testing.T, user uuid.UUID) int {
	code, _ := w.call(t, w.app.GetOccurrenceChat, user, w.occID, nil, nil)
	return code
}
