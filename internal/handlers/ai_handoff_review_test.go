package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Follow-ups from the independent review of the handoff.

// After a cap or a provider failure nothing was qualified: the waiting message must not claim that
// the conversation was passed on.
func TestHandoffFromAI_ShortMessageWhenNothingWasQualified(t *testing.T) {
	for _, reason := range []string{aiHandoffCap, aiHandoffFailure} {
		w := newWiring(t, "nunca deve ser chamado")
		contact, session := w.handoffSession(t, "quero porcelanato")
		require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, reason))
		sent := w.wa.all()
		require.Len(t, sent, 1)
		assert.Contains(t, sent[0], "atendente")
		assert.NotContains(t, sent[0], "Já passei", reason)
	}
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))
	assert.Contains(t, w.wa.all()[0], "Já passei", "when the agent did qualify, the long message is true")
}

// An out-of-hours handoff for a failure must not swallow the qualification of a later, in-hours
// handoff on the same conversation.
func TestHandoffFromAI_EarlierOutOfHoursFailureDoesNotBlockLaterQualification(t *testing.T) {
	w := newWiring(t, qualHappy)
	closed := models.JSONBArray{}
	for d := 0; d < 7; d++ {
		closed = append(closed, map[string]any{"day": float64(d), "enabled": false})
	}
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).Updates(map[string]any{
		"business_hours_enabled": true, "business_hours": closed, "out_of_hours_message": "Voltamos amanhã",
	}).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")

	assert.False(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffFailure))
	assert.Empty(t, w.ai.bodies, "a failure handoff does not qualify")
	assert.True(t, sessionFlag(session, aiSessionHandedOff))

	// the store opens; the customer writes again and the agent asks for a human
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).
		Update("business_hours_enabled", false).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	assert.Len(t, w.ai.bodies, 1, "the qualification ran now")
	assert.Len(t, w.opportunities(t, contact.ID), 1)
	assert.Len(t, w.transfers(t, contact.ID), 1)
	assert.False(t, sessionFlag(session, aiSessionHandedOff), "cleared once the attendance exists")
}

// The contact's account owner (carteira) may belong to another unit: the opportunity the agent
// opens is nobody's until somebody takes the attendance, and then it is theirs.
func TestHandoffFromAI_OpportunityIsNotOwnedByTheContactsAccountOwner(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	otherUnitSeller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	require.NoError(t, w.app.DB.Model(contact).Update("assigned_user_id", otherUnitSeller.ID).Error)
	contact.AssignedUserID = &otherUnitSeller.ID

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1)
	assert.Nil(t, opps[0].AssignedUserID, "not the other unit's seller")

	// the seller of this unit takes the attendance: now it is theirs
	seller := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	ts := w.transfers(t, contact.ID)
	require.Len(t, ts, 1)
	claimed, err := w.app.claimTransfer(w.app.DB, &ts[0], seller.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	opps = w.opportunities(t, contact.ID)
	require.NotNil(t, opps[0].AssignedUserID)
	assert.Equal(t, seller.ID, *opps[0].AssignedUserID)
}

// assignAIOpportunity touches only the opportunity opened by that attendance, and only while it has
// no owner: an opportunity somebody already works on, or one from another attendance, is left alone.
func TestAssignAIOpportunity_OnlyTheAttendancesOwnUnownedOpportunity(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	seller := testutil.CreateTestUser(t, app.DB, org.ID)
	incumbent := testutil.CreateTestUser(t, app.DB, org.ID)

	transferID, otherTransferID := uuid.New(), uuid.New()
	mk := func(tid uuid.UUID, status models.SalesOpportunityStatus, number string) *models.SalesOpportunity {
		o := &models.SalesOpportunity{OrganizationID: org.ID, ContactID: contact.ID, OpportunityNumber: number,
			SourceTransferID: &tid, Stage: models.SalesOpportunityStagePotencial, Status: status}
		require.NoError(t, app.DB.Create(o).Error)
		return o
	}
	mine := mk(transferID, models.SalesOpportunityStatusPerdida, "OP-1") // closed: untouched
	t1 := &models.AgentTransfer{BaseModel: models.BaseModel{ID: transferID}, OrganizationID: org.ID, ContactID: contact.ID, Source: models.TransferSourceAIAgent}

	app.assignAIOpportunity(app.DB, t1, seller.ID)
	require.NoError(t, app.DB.First(mine, "id = ?", mine.ID).Error)
	assert.Nil(t, mine.AssignedUserID, "a closed opportunity is not assigned")

	require.NoError(t, app.DB.Model(mine).Update("status", models.SalesOpportunityStatusAberta).Error)
	app.assignAIOpportunity(app.DB, t1, seller.ID)
	require.NoError(t, app.DB.First(mine, "id = ?", mine.ID).Error)
	require.NotNil(t, mine.AssignedUserID)
	assert.Equal(t, seller.ID, *mine.AssignedUserID)

	app.assignAIOpportunity(app.DB, t1, incumbent.ID) // already owned
	require.NoError(t, app.DB.First(mine, "id = ?", mine.ID).Error)
	assert.Equal(t, seller.ID, *mine.AssignedUserID, "an owned opportunity is never moved")

	// an attendance that is not an AI handoff never uses this rule
	// (a different contact: the funnel allows one open opportunity per contact)
	contact2 := testutil.CreateTestContact(t, app.DB, org.ID)
	other := &models.SalesOpportunity{OrganizationID: org.ID, ContactID: contact2.ID, OpportunityNumber: "OP-2",
		SourceTransferID: &otherTransferID, Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta}
	require.NoError(t, app.DB.Create(other).Error)
	notAI := &models.AgentTransfer{BaseModel: models.BaseModel{ID: otherTransferID}, OrganizationID: org.ID, ContactID: contact2.ID, Source: models.TransferSourceManual}
	app.assignAIOpportunity(app.DB, notAI, seller.ID)
	var again models.SalesOpportunity
	require.NoError(t, app.DB.First(&again, "id = ?", other.ID).Error)
	assert.Nil(t, again.AssignedUserID)
}

// An opportunity that already existed before the handoff is neither re-linked nor re-assigned.
func TestHandoffFromAI_PreexistingOpportunityIsLeftAlone(t *testing.T) {
	w := newWiring(t, qualHappy)
	contact, session := w.handoffSession(t, "Oi, quero porcelanato", "para a sala, uns 40 m²")
	owner := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID)
	existing := &models.SalesOpportunity{OrganizationID: w.f.org.ID, ContactID: contact.ID, OpportunityNumber: "OP-OLD",
		AssignedUserID: &owner.ID, Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta, Interest: "antiga"}
	require.NoError(t, w.app.DB.Create(existing).Error)

	require.True(t, w.app.handoffFromAI(w.f.account, contact, session, w.f.settings, aiHandoffMarker))

	opps := w.opportunities(t, contact.ID)
	require.Len(t, opps, 1, "no duplicate")
	assert.Equal(t, "antiga", opps[0].Interest)
	assert.Nil(t, opps[0].SourceTransferID, "not linked to an attendance that did not create it")
	require.NotNil(t, opps[0].AssignedUserID)
	assert.Equal(t, owner.ID, *opps[0].AssignedUserID)
}

// --- the ai_response node of a flow --------------------------------------------------------------

func (w *wiring) nodeFlow(t *testing.T) (*models.ChatbotFlow, *models.Contact, *models.ChatbotSession) {
	t.Helper()
	contact, session := w.handoffSession(t, "Quero porcelanato para a sala, uns 40 m²")
	flow := &models.ChatbotFlow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID,
		WhatsAppAccount: w.f.account.Name, Name: "ai-then-message", IsEnabled: true,
		Graph: models.JSONB{"version": 2, "entry_node": "ai",
			"nodes": []any{
				map[string]any{"id": "ai", "type": "ai_response", "label": "ask", "config": map[string]any{}},
				map[string]any{"id": "after", "type": "message", "label": "after", "config": map[string]any{"text": "MENSAGEM POSTERIOR DO FLUXO"}},
				map[string]any{"id": "end", "type": "end"},
			},
			"edges": []any{
				map[string]any{"from": "ai", "to": "after", "condition": "default"},
				map[string]any{"from": "after", "to": "end", "condition": "default"},
			}}}
	require.NoError(t, w.app.DB.Create(flow).Error)
	return flow, contact, session
}

func TestGraphNode_MarkerHandsOverAndStopsTheFlow(t *testing.T) {
	w := newWiring(t, "[[TRANSFERIR]]", qualHappy)
	flow, contact, session := w.nodeFlow(t)

	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "Quero porcelanato para a sala, uns 40 m²", "", nil))

	sent := w.wa.all()
	require.Len(t, sent, 1, "only the waiting message; the flow's next node must not speak")
	assert.Contains(t, sent[0], "atendente")
	assert.Equal(t, 0, countContaining(sent, "MENSAGEM POSTERIOR"))
	assert.Len(t, w.opportunities(t, contact.ID), 1)
	assert.Len(t, w.transfers(t, contact.ID), 1)
}

func TestGraphNode_TextThenMarker(t *testing.T) {
	w := newWiring(t, "Perfeito, anotei! [[TRANSFERIR]]", qualHappy)
	flow, contact, session := w.nodeFlow(t)
	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "Quero porcelanato para a sala, uns 40 m²", "", nil))

	sent := w.wa.all()
	require.Len(t, sent, 2)
	assert.Contains(t, sent[0], "Perfeito, anotei!")
	assert.NotContains(t, sent[0], "[[")
	assert.Contains(t, sent[1], "atendente")
}

func TestGraphNode_CapReachedHandsOver(t *testing.T) {
	w := newWiring(t, "nunca deve ser chamado")
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("id = ?", w.f.settings.ID).
		Update("ai_monthly_token_limit", 10).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
	w.f.settings.AI.MonthlyTokenLimit = 10
	flow, contact, session := w.nodeFlow(t)

	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "quero porcelanato", "", nil))

	assert.Empty(t, w.ai.bodies)
	assert.Len(t, w.transfers(t, contact.ID), 1)
	require.Equal(t, 1, w.wa.count())
	assert.Equal(t, 0, countContaining(w.wa.all(), "MENSAGEM POSTERIOR"))
}

func TestGraphNode_FirstProviderFailureOnlyCounts(t *testing.T) {
	w := newWiring(t)
	w.ai.failStatus = 500
	flow, contact, session := w.nodeFlow(t)

	require.NoError(t, w.app.runChatGraph(w.f.account, contact, session, flow, "quero porcelanato", "", nil))
	assert.Empty(t, w.transfers(t, contact.ID), "one failure never hands over")
	assert.Equal(t, 1, sessionInt(session, aiSessionFailures))
}
