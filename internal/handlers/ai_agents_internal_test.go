package handlers

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aiAgentFixture is an organization with an enabled agent allowed on the number "pilot", whose
// default team is active and tied to a unit.
type aiAgentFixture struct {
	org      *models.Organization
	settings *models.ChatbotSettings
	agent    *models.AIAgent
	account  *models.WhatsAppAccount
	team     *models.Team
}

func newAIAgentFixture(t *testing.T, app *App) aiAgentFixture {
	t.Helper()
	org := testutil.CreateTestOrganization(t, app.DB)
	unit := models.Unit{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Loja " + uuid.NewString()[:6], Active: true}
	require.NoError(t, app.DB.Create(&unit).Error)
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Comercial", IsActive: true, UnitID: &unit.ID}
	require.NoError(t, app.DB.Create(&team).Error)
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("pilot-"+uuid.NewString()[:8]))
	require.NoError(t, app.DB.Model(account).Update("default_team_id", team.ID).Error)
	account.DefaultTeamID = &team.ID

	agent := models.AIAgent{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Agente",
		IsEnabled: true, WhatsAppAccounts: models.StringArray{account.Name},
	}
	require.NoError(t, app.DB.Create(&agent).Error)
	settings := &models.ChatbotSettings{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		AI: models.AIConfig{DefaultAgentID: &agent.ID},
	}
	return aiAgentFixture{org: org, settings: settings, agent: &agent, account: account, team: &team}
}

func TestResolveAIAgent(t *testing.T) {
	app := newSLATestApp(t)

	cases := []struct {
		name   string
		mutate func(t *testing.T, f *aiAgentFixture)
		want   bool
	}{
		{"all conditions hold", func(*testing.T, *aiAgentFixture) {}, true},
		{"no default agent", func(_ *testing.T, f *aiAgentFixture) { f.settings.AI.DefaultAgentID = nil }, false},
		{"agent disabled", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Model(f.agent).Update("is_enabled", false).Error)
		}, false},
		{"agent deleted", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Delete(f.agent).Error)
		}, false},
		{"agent of another organization", func(t *testing.T, f *aiAgentFixture) {
			other := testutil.CreateTestOrganization(t, app.DB)
			require.NoError(t, app.DB.Model(f.agent).Update("organization_id", other.ID).Error)
		}, false},
		{"allowed list empty", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Model(f.agent).Update("whatsapp_accounts", models.StringArray{}).Error)
		}, false},
		{"allowed list without this number", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Model(f.agent).Update("whatsapp_accounts", models.StringArray{"some-other-number"}).Error)
		}, false},
		{"number without default team", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Model(f.account).Update("default_team_id", nil).Error)
			f.account.DefaultTeamID = nil
		}, false},
		{"team inactive", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Model(f.team).Update("is_active", false).Error)
		}, false},
		{"team without unit", func(t *testing.T, f *aiAgentFixture) {
			require.NoError(t, app.DB.Model(f.team).Update("unit_id", nil).Error)
		}, false},
		{"team of another organization", func(t *testing.T, f *aiAgentFixture) {
			other := testutil.CreateTestOrganization(t, app.DB)
			require.NoError(t, app.DB.Model(f.team).Update("organization_id", other.ID).Error)
		}, false},
		{"settings of another organization", func(t *testing.T, f *aiAgentFixture) {
			f.settings.OrganizationID = uuid.New()
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAIAgentFixture(t, app)
			tc.mutate(t, &f)
			got := app.resolveAIAgent(f.settings, f.account)
			if tc.want {
				require.NotNil(t, got)
				assert.Equal(t, f.agent.ID, got.ID)
			} else {
				assert.Nil(t, got)
			}
		})
	}
	t.Run("nil inputs", func(t *testing.T) {
		assert.Nil(t, app.resolveAIAgent(nil, nil))
	})
}

// TestResolveAIAgent_PilotIsolation proves that listing one number activates the agent on that
// number only: the other numbers of the organization, even with a team and a unit, get none.
func TestResolveAIAgent_PilotIsolation(t *testing.T) {
	app := newSLATestApp(t)
	f := newAIAgentFixture(t, app)

	mk := func() *models.WhatsAppAccount {
		acc := testutil.CreateTestWhatsAppAccountWith(t, app.DB, f.org.ID, testutil.WithAccountName("other-"+uuid.NewString()[:8]))
		require.NoError(t, app.DB.Model(acc).Update("default_team_id", f.team.ID).Error)
		acc.DefaultTeamID = &f.team.ID
		return acc
	}
	b, c := mk(), mk()

	assert.NotNil(t, app.resolveAIAgent(f.settings, f.account), "the listed number gets the agent")
	assert.Nil(t, app.resolveAIAgent(f.settings, b), "an unlisted number must not")
	assert.Nil(t, app.resolveAIAgent(f.settings, c), "an unlisted number must not")
}

func TestAIAgentAccountIssues(t *testing.T) {
	app := newSLATestApp(t)
	f := newAIAgentFixture(t, app)
	assert.Empty(t, app.aiAgentAccountIssues(f.agent))

	require.NoError(t, app.DB.Model(f.team).Update("unit_id", nil).Error)
	issues := app.aiAgentAccountIssues(f.agent)
	require.Len(t, issues, 1)
	assert.Equal(t, f.account.Name, issues[0].Account)
	assert.Equal(t, aiIssueNoUnit, issues[0].Problem)

	f.agent.WhatsAppAccounts = models.StringArray{f.account.Name, "ghost"}
	issues = app.aiAgentAccountIssues(f.agent)
	require.Len(t, issues, 2)
	assert.Equal(t, "account_not_found", issues[1].Problem)
}
