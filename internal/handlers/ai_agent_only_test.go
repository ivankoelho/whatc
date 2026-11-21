package handlers

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B4 isolation matrix (runtime). Numbers: PILOT (the agent's) and OTHER (same organization, not listed).
// The organization's chatbot is on, and so is its AI: before AgentOnly, that was true for every number.
//
//	                        PILOT (agent listed)         OTHER (not listed)
//	AgentOnly = false       the agent answers            behavior as before agents existed (free-text AI)
//	AgentOnly = true        the agent answers            no chatbot, no AI call: human queue, as with the chatbot off
//
// plus: another organization never sees or uses this organization's agent.

func (w *wiring) otherNumber(t *testing.T) *models.WhatsAppAccount {
	t.Helper()
	acc := testutil.CreateTestWhatsAppAccountWith(t, w.app.DB, w.f.org.ID, testutil.WithAccountName("other-"+uuid.NewString()[:8]))
	require.NoError(t, w.app.DB.Model(acc).Update("default_team_id", w.f.team.ID).Error)
	return acc
}

func (w *wiring) inboundOn(t *testing.T, acc *models.WhatsAppAccount, from, text string) {
	t.Helper()
	var msg IncomingTextMessage
	raw := `{"from":"` + from + `","id":"wamid.` + uuid.NewString()[:8] + `","type":"text","text":{"body":` + mustJSON(text) + `}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &msg))
	w.app.processIncomingMessageFull(acc.PhoneID, msg, "Cliente")
}

func (w *wiring) setAgentOnly(t *testing.T, on bool) {
	t.Helper()
	require.NoError(t, w.app.DB.Model(&models.ChatbotSettings{}).Where("organization_id = ?", w.f.org.ID).Update("ai_agent_only", on).Error)
	w.app.InvalidateChatbotSettingsCache(w.f.org.ID)
}

func (w *wiring) transferSourcesFor(t *testing.T, contact *models.Contact) []models.TransferSource {
	t.Helper()
	var out []models.TransferSource
	for _, tr := range w.transfers(t, contact.ID) {
		out = append(out, tr.Source)
	}
	return out
}

func TestAgentOnly_Matrix_PilotNumberAnswersOtherNumberIsLeftAlone(t *testing.T) {
	const customer = "oi, queria ver pisos"
	for _, agentOnly := range []bool{false, true} {
		name := map[bool]string{false: "AgentOnly=false", true: "AgentOnly=true"}[agentOnly]
		t.Run(name, func(t *testing.T) {
			w := newWiring(t, "Olá! Em que posso ajudar?", "Olá! (resposta livre)")
			w.setAgentOnly(t, agentOnly)
			other := w.otherNumber(t)

			// PILOT: the agent answers in both modes
			w.inbound(t, "5511977000001", customer)
			require.Len(t, w.ai.bodies, 1, "the agent called the model")
			sent := w.textSends()
			require.Len(t, sent, 1)
			assert.Contains(t, sent[0], "Assistente Virtual", "signed as the agent")

			// OTHER: same organization, number not listed
			before := len(w.ai.bodies)
			w.inboundOn(t, other, "5511977000002", customer)
			c := w.contactFor(t, "5511977000002")
			if agentOnly {
				assert.Len(t, w.ai.bodies, before, "no AI call at all on a number without an agent")
				assert.Len(t, w.textSends(), 1, "nothing is said to that customer")
				assert.Contains(t, w.transferSourcesFor(t, c), models.TransferSourceChatbotDisabled, "it goes to the human queue, as with the chatbot off")
			} else {
				// legacy behavior preserved: the organization-wide chatbot/AI still answers there, and it is
				// NOT the agent (no signature, no agent prompt)
				assert.Len(t, w.ai.bodies, before+1)
				all := w.textSends()
				require.Len(t, all, 2)
				assert.NotContains(t, all[1], "Assistente Virtual")
				assert.Empty(t, w.aiTransfers(t, c.ID))
			}
		})
	}
}

// Another organization can point its default agent at this organization's agent id: it must resolve to nothing.
func TestAgentOnly_AnotherOrganizationNeverUsesThisOnesAgent(t *testing.T) {
	w := newWiring(t, "x")
	otherOrg := newAIAgentFixture(t, w.app)
	// the other organization's settings reference OUR agent (id guessed/copied)
	otherOrg.settings.AI.DefaultAgentID = &w.f.agent.ID
	otherOrg.settings.AI.AgentOnly = true
	assert.Nil(t, w.app.resolveAIAgent(otherOrg.settings, otherOrg.account), "an agent of another organization never resolves")
	// and OUR agent still resolves on OUR number only
	assert.NotNil(t, w.app.resolveAIAgent(w.f.settings, w.f.account))
	assert.Nil(t, w.app.resolveAIAgent(w.f.settings, otherOrg.account), "a number of another organization is not ours")
}

// AgentOnly and UnlimitedAck default to false: an organization that never touched them behaves as before
// (the fixture of the other tests accepts no-limit explicitly, so this reads a row created with the defaults).
func TestAgentOnly_DefaultsOff(t *testing.T) {
	w := newWiring(t, "x")
	org := testutil.CreateTestOrganization(t, w.app.DB)
	require.NoError(t, w.app.DB.Create(&models.ChatbotSettings{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID}).Error)
	var s models.ChatbotSettings
	require.NoError(t, w.app.DB.Where("organization_id = ?", org.ID).First(&s).Error)
	assert.False(t, s.AI.AgentOnly)
	assert.False(t, s.AI.UnlimitedAck)
}
