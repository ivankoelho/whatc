package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAIAgent_EndToEnd is the MVP's acceptance scenario, with the real processor, the real
// database and only the two outside services faked (the AI provider and the WhatsApp API):
//
//	a customer writes on WhatsApp, the agent answers and asks, the customer gives what they want,
//	the agent qualifies and hands over, the opportunity appears in the CRM with the facts the
//	customer stated, the attendance lands in the team of the number's unit, the agent falls silent,
//	a seller takes over by sending a message, and nothing leaks to another number or organization.
//
// docs/homologacao/agentes-ia-roteiro.md runs the same scenario by hand on a real test number.
func TestAIAgent_EndToEnd(t *testing.T) {
	w := newWiring(t,
		"Olá! Qual produto você procura?",     // 1st AI call: answer to message 1
		"Para qual ambiente seria?",           // 2nd: answer to message 2
		"[[TRANSFERIR]]",                      // 3rd: the customer is ready
		qualHappy,                             // 4th: the extraction call
		"resposta do chatbot antigo (legado)", // later calls, on numbers without the agent
	)
	const from = "5511988880001"

	// the organization does NOT use "assign to the same agent": the seller must still get the opportunity
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).Update("assign_to_same_agent", false).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)

	// The organization has a second number with its own unit team, NOT allowed for the agent.
	otherAccount := testutil.CreateTestWhatsAppAccountWith(t, w.app.DB, w.f.org.ID, testutil.WithAccountName("loja-norte-"+uuid.NewString()[:6]))
	require.NoError(t, w.app.DB.Model(otherAccount).Update("default_team_id", w.f.team.ID).Error)

	// 1. the customer starts the conversation; the agent answers and asks
	w.inbound(t, from, "Oi, quero ver porcelanato")
	require.Equal(t, 1, w.wa.count())
	assert.Contains(t, w.wa.all()[0], "Qual produto você procura?")
	assert.Contains(t, w.ai.systemPrompt(0), "REGRAS DA PLATAFORMA", "the agent's layered prompt is in use")
	assert.Contains(t, w.ai.systemPrompt(0), "Qualificar compras de pisos", "and the agent's own objective")

	// 2. the agent keeps the context and keeps asking (history reaches the model)
	w.inbound(t, from, "É para a sala")
	require.Equal(t, 2, w.wa.count())
	assert.Contains(t, w.wa.all()[1], "Para qual ambiente")
	assert.Contains(t, w.ai.bodies[1]["messages"].([]any)[1].(map[string]any)["content"], "porcelanato",
		"the first message of the conversation is part of the next prompt")

	// 3. the customer states the quantity: the agent qualifies and hands over
	w.inbound(t, from, "Quero porcelanato para a sala, uns 40 m²")
	contact := w.contactFor(t, from)

	sent := w.wa.all()
	require.Len(t, sent, 3, "two answers and the waiting message, nothing else")
	assert.Contains(t, sent[2], "atendente")
	for _, body := range sent {
		assert.NotContains(t, body, "[[", "no technical marker reaches the customer")
		assert.NotContains(t, strings.ToLower(body), "transferir")
	}

	// the opportunity in the CRM, with the facts the customer stated and no more
	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1)
	assert.Contains(t, opps[0].Interest, "Produto: porcelanato")
	assert.Contains(t, opps[0].Interest, "Metragem: 40 m²")
	assert.Contains(t, opps[0].Interest, "Ainda não coletado")
	assert.NotContains(t, strings.Split(opps[0].Interest, "Ainda não coletado")[0], "Quantidade",
		"a quantity the customer never gave is not among the informed fields")
	require.NotNil(t, opps[0].EstimatedQuantity)
	assert.Equal(t, 40.0, *opps[0].EstimatedQuantity)
	assert.Equal(t, models.SalesUnitMetro2, *opps[0].UnitOfMeasure)

	// the attendance, in the team of the number's unit, with the history preserved in its notes
	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1)
	require.NotNil(t, ts[0].TeamID)
	assert.Equal(t, w.f.team.ID, *ts[0].TeamID)
	assert.Equal(t, models.TransferSourceAIAgent, ts[0].Source)
	assert.Contains(t, ts[0].Notes, "Produto: porcelanato")
	var unit models.Unit
	require.NoError(t, w.app.DB.First(&unit, "id = ?", *w.f.team.UnitID).Error, "the team belongs to a unit")

	// consumption and history of the agent's actions are recorded
	var usage []models.AIUsageLog
	require.NoError(t, w.app.DB.Where("organization_id = ?", w.f.org.ID).Find(&usage).Error)
	features := map[string]int{}
	for _, u := range usage {
		features[u.Feature]++
	}
	assert.Equal(t, 3, features[aiFeatureChatbotReply], "three agent turns")
	assert.Equal(t, 1, features[aiFeatureQualification], "one qualification call")

	// 4. the customer writes again: the attendance is active, the agent says nothing and is not even called
	calls := len(w.ai.bodies)
	w.inbound(t, from, "e o prazo de entrega?")
	assert.Equal(t, calls, len(w.ai.bodies))
	assert.Equal(t, 3, w.wa.count())

	// 5. a seller takes over by sending a message (the real send path)
	seller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	opts := DefaultSendOptions()
	opts.Async = false
	opts.SentByUserID = &seller.ID
	_, err := w.app.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{
		Account: w.f.account, Contact: contact, Type: models.MessageTypeText, Content: "Oi, aqui é o Carlos, da loja Centro.",
	}, opts)
	require.NoError(t, err)
	require.Equal(t, 4, w.wa.count())
	ts = w.transfers(t, contact.ID)
	require.Len(t, ts, 1)
	require.NotNil(t, ts[0].AgentID)
	assert.Equal(t, seller.ID, *ts[0].AgentID, "the seller owns the attendance now")
	opps = w.opportunities(t, contact.ID)
	require.NotNil(t, opps[0].AssignedUserID, "and the opportunity is theirs, so the sales funnel shows it to them")
	assert.Equal(t, seller.ID, *opps[0].AssignedUserID)

	// the sales funnel shows it to that seller and to nobody else without a wider permission
	visibleTo := func(userID uuid.UUID) int {
		var visible []models.SalesOpportunity
		require.NoError(t, w.app.visibleSalesOpportunities(w.app.DB.Model(&models.SalesOpportunity{}).
			Where("organization_id = ?", w.f.org.ID), userID, w.f.org.ID).Find(&visible).Error)
		return len(visible)
	}
	otherSeller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID) // another unit's seller, no extra permission
	assert.Equal(t, 1, visibleTo(seller.ID), "the seller who took over sees the opportunity")
	assert.Equal(t, 0, visibleTo(otherSeller.ID), "a seller of another unit does not")

	// 6. the agent stays off for good
	w.inbound(t, from, "pode ser amanhã?")
	assert.Equal(t, calls, len(w.ai.bodies), "the model is never called again")
	assert.Equal(t, 4, w.wa.count())

	// 7. another number of the same organization is not served by the agent
	var msg IncomingTextMessage
	require.NoError(t, json.Unmarshal([]byte(`{"from":"5511977770002","id":"wamid.other","type":"text","text":{"body":"Oi, quero porcelanato"}}`), &msg))
	w.app.processIncomingMessageFull(otherAccount.PhoneID, msg, "Outro cliente")
	require.Greater(t, len(w.ai.bodies), calls, "the chatbot's own AI path still answers on that number, as before")
	assert.NotContains(t, w.ai.systemPrompt(len(w.ai.bodies)-1), "REGRAS DA PLATAFORMA", "but not with the agent")
	var other models.Contact
	require.NoError(t, w.app.DB.Where("organization_id = ? AND phone_number = ?", w.f.org.ID, "5511977770002").First(&other).Error)
	assert.Empty(t, w.opportunities(t, other.ID), "no opportunity from the agent")
	assert.Empty(t, w.transfers(t, other.ID), "no handoff from the agent")

	// 8. another organization sees nothing of this
	otherOrg := testutil.CreateTestOrganization(t, w.app.DB)
	var n int64
	for _, m := range []any{&models.SalesOpportunity{}, &models.AgentTransfer{}, &models.AIUsageLog{}} {
		w.app.DB.Model(m).Where("organization_id = ?", otherOrg.ID).Count(&n)
		assert.Zero(t, n, "%T leaked into another organization", m)
	}
}
