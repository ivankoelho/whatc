package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCountOf(t *testing.T, app *App, orgID, userID uuid.UUID) int64 {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.GetContactStatusCounts(req))
	var resp struct {
		Data struct {
			New int64 `json:"new"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data.New
}

// The server is the source of truth for the "Novo" counter, so the numbers the
// frontend resyncs to must obey scope: a transfer to another team takes the
// conversation out of the agent's counter but not the admin's, and a
// conversation whose last inbound is older than 24h stops counting for all.
func TestNewCounter_ScopeTransferAndWindow(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	otherAgent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	teamA := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "A", IsActive: true}
	teamB := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "B", IsActive: true}
	require.NoError(t, app.DB.Create(teamA).Error)
	require.NoError(t, app.DB.Create(teamB).Error)
	require.NoError(t, app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: teamA.ID, UserID: agent.ID, Role: models.TeamRoleAgent}).Error)
	require.NoError(t, app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: teamB.ID, UserID: otherAgent.ID, Role: models.TeamRoleAgent}).Error)

	queue := func(teamID uuid.UUID) *models.Contact {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		now := time.Now()
		require.NoError(t, app.DB.Model(c).Update("last_inbound_at", now).Error)
		require.NoError(t, app.DB.Create(&models.AgentTransfer{
			BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: c.ID,
			PhoneNumber: c.PhoneNumber, Status: models.TransferStatusActive, TeamID: &teamID, TransferredAt: now,
		}).Error)
		return c
	}
	c1 := queue(teamA.ID)
	queue(teamA.ID)
	queue(teamB.ID)

	assert.Equal(t, int64(2), newCountOf(t, app, org.ID, agent.ID), "agent: own team only")
	assert.Equal(t, int64(1), newCountOf(t, app, org.ID, otherAgent.ID), "other agent: own team only")
	assert.Equal(t, int64(3), newCountOf(t, app, org.ID, admin.ID), "admin: everything")

	// Transfer c1 to team B: leaves agent's scope, enters otherAgent's.
	require.NoError(t, app.DB.Model(&models.AgentTransfer{}).
		Where("contact_id = ?", c1.ID).Update("team_id", teamB.ID).Error)
	assert.Equal(t, int64(1), newCountOf(t, app, org.ID, agent.ID))
	assert.Equal(t, int64(2), newCountOf(t, app, org.ID, otherAgent.ID))
	assert.Equal(t, int64(3), newCountOf(t, app, org.ID, admin.ID))

	// 24h window: a conversation with no active transfer and an old last
	// inbound no longer counts; the same one inside the window did.
	free := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(free).Update("last_inbound_at", time.Now()).Error)
	assert.Equal(t, int64(4), newCountOf(t, app, org.ID, admin.ID))
	require.NoError(t, app.DB.Model(free).Update("last_inbound_at", time.Now().Add(-25*time.Hour)).Error)
	assert.Equal(t, int64(3), newCountOf(t, app, org.ID, admin.ID))
}
