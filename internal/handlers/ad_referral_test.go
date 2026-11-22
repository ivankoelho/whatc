package handlers

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboundRaw delivers a customer message exactly as Meta's JSON spells it, so the JSON tags are exercised.
func (w *wiring) inboundRaw(t *testing.T, raw string) {
	t.Helper()
	var msg IncomingTextMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))
	w.app.processIncomingMessageFull(w.f.account.PhoneID, msg, "Cliente")
}

func (w *wiring) referralsOf(t *testing.T, contactID any) []models.ContactAdReferral {
	t.Helper()
	var rows []models.ContactAdReferral
	require.NoError(t, w.app.DB.Where("contact_id = ?", contactID).Order("created_at").Find(&rows).Error)
	return rows
}

const adMsg = `{"from":"5511990001111","id":"%s","type":"text","text":{"body":"Oi, vi o anúncio"},"referral":{` +
	`"source_url":"https://fb.me/abc","source_id":"%s","source_type":"ad","headline":"Consulta jurídica","body":"Fale com a gente",` +
	`"media_type":"image","ctwa_clid":"AffAbc123"}}`

func TestAdReferral_IsRecordedFromTheFirstMessageOfAnAdConversation(t *testing.T) {
	w := newWiring(t, "Olá!")
	w.inboundRaw(t, fmt.Sprintf(adMsg, "wamid.ad1", "120001"))

	c := w.contactFor(t, "5511990001111")
	rows := w.referralsOf(t, c.ID)
	require.Len(t, rows, 1)
	assert.Equal(t, "120001", rows[0].SourceID)
	assert.Equal(t, "ad", rows[0].SourceType)
	assert.Equal(t, "Consulta jurídica", rows[0].Headline)
	assert.Equal(t, "AffAbc123", rows[0].CtwaClid)
	assert.Equal(t, w.f.org.ID, rows[0].OrganizationID)

	origin := w.app.adOriginOf(w.f.org.ID, c.ID)
	require.NotNil(t, origin)
	assert.Equal(t, "120001", origin.SourceID)
	assert.Equal(t, 1, origin.Clicks)
}

func TestAdReferral_ARetriedWebhookIsNotRecordedTwice(t *testing.T) {
	w := newWiring(t, "Olá!")
	raw := fmt.Sprintf(adMsg, "wamid.retry", "120001")
	w.inboundRaw(t, raw)
	w.inboundRaw(t, raw) // Meta redelivers the same message
	c := w.contactFor(t, "5511990001111")
	assert.Len(t, w.referralsOf(t, c.ID), 1)
}

func TestAdReferral_FirstTouchIsTheOriginAndLaterClicksAreCounted(t *testing.T) {
	w := newWiring(t, "Olá!")
	w.inboundRaw(t, fmt.Sprintf(adMsg, "wamid.first", "AD-FIRST"))
	w.inboundRaw(t, fmt.Sprintf(adMsg, "wamid.second", "AD-SECOND"))
	c := w.contactFor(t, "5511990001111")

	origin := w.app.adOriginOf(w.f.org.ID, c.ID)
	require.NotNil(t, origin)
	assert.Equal(t, "AD-FIRST", origin.SourceID, "the first ad is the origin")
	assert.Equal(t, 2, origin.Clicks)
}

func TestAdReferral_PlainMessagesAndEmptyReferralsRecordNothing(t *testing.T) {
	w := newWiring(t, "Olá!")
	w.inbound(t, "5511990002222", "Oi, tudo bem?")
	w.inboundRaw(t, `{"from":"5511990003333","id":"wamid.empty","type":"text","text":{"body":"Oi"},"referral":{"headline":"só um título"}}`)

	for _, from := range []string{"5511990002222", "5511990003333"} {
		c := w.contactFor(t, from)
		assert.Empty(t, w.referralsOf(t, c.ID), from)
		assert.Nil(t, w.app.adOriginOf(w.f.org.ID, c.ID), from)
	}
}

func TestAdReferral_TheContactResponseCarriesTheOrigin(t *testing.T) {
	w := newWiring(t, "Olá!")
	w.inboundRaw(t, fmt.Sprintf(adMsg, "wamid.resp", "120009"))
	c := w.contactFor(t, "5511990001111")

	resp := w.app.buildContactResponse(c, w.f.org.ID)
	require.NotNil(t, resp.AdOrigin)
	assert.Equal(t, "120009", resp.AdOrigin.SourceID)
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"ad_origin"`)
	assert.NotContains(t, string(raw), "AffAbc123", "the click id is for reporting, not for the screen")
}

func TestAdReferral_APageOfContactsGetsItsOriginsInOneLookup(t *testing.T) {
	w := newWiring(t, "Olá!")
	w.inboundRaw(t, fmt.Sprintf(adMsg, "wamid.p1", "AD-A"))
	w.inbound(t, "5511990002222", "Oi")
	w.inboundRaw(t, fmt.Sprintf(`{"from":"5511990003333","id":"%s","type":"text","text":{"body":"Oi"},"referral":{"source_id":"AD-B","source_type":"ad","headline":"B"}}`, "wamid.p2"))
	w.inboundRaw(t, fmt.Sprintf(`{"from":"5511990003333","id":"%s","type":"text","text":{"body":"Oi de novo"},"referral":{"source_id":"AD-C","source_type":"ad"}}`, "wamid.p3"))

	a := w.contactFor(t, "5511990001111")
	plain := w.contactFor(t, "5511990002222")
	b := w.contactFor(t, "5511990003333")
	got := w.app.adOriginsFor(w.f.org.ID, []models.Contact{*a, *plain, *b})

	require.Contains(t, got, a.ID)
	assert.Equal(t, "AD-A", got[a.ID].SourceID)
	assert.NotContains(t, got, plain.ID, "no ad, no entry")
	assert.Equal(t, "AD-B", got[b.ID].SourceID, "the first ad is the origin")
	assert.Equal(t, 2, got[b.ID].Clicks)
	assert.Empty(t, w.app.adOriginsFor(w.f.org.ID, nil))
}
