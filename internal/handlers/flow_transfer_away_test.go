package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/assignment"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bot-driven flow handing off to a team must never land on an agent who is
// away or disconnected, under every team strategy, and the agent must start
// receiving again as soon as they are back.
func TestFlowTransfer_AwayOrOfflineAgentNeverReceives(t *testing.T) {
	for _, strategy := range []models.AssignmentStrategy{
		models.AssignmentStrategyRoundRobin,
		models.AssignmentStrategyLoadBalanced,
		models.AssignmentStrategyManual,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			app, org, account, _, _ := newGraphTestFixtures(t)
			offline := map[uuid.UUID]bool{}
			app.Assigner = assignment.New(app.DB, app.Redis, app.Log)
			app.Assigner.SetPresence(func(_, id uuid.UUID) bool { return !offline[id] })

			team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
				Name: "Time " + uuid.NewString()[:8], IsActive: true, AssignmentStrategy: strategy}
			require.NoError(t, app.DB.Create(team).Error)
			away, disconnected, online := testutil.CreateTestUser(t, app.DB, org.ID),
				testutil.CreateTestUser(t, app.DB, org.ID), testutil.CreateTestUser(t, app.DB, org.ID)
			for _, u := range []*models.User{away, disconnected, online} {
				require.NoError(t, app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()},
					TeamID: team.ID, UserID: u.ID, Role: models.TeamRoleAgent}).Error)
			}
			require.NoError(t, app.DB.Model(away).Update("is_available", false).Error)
			offline[disconnected.ID] = true

			handoff := func() *uuid.UUID {
				contact := testutil.CreateTestContact(t, app.DB, org.ID)
				session := &models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
					ContactID: contact.ID, WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber,
					Status: models.SessionStatusActive, SessionData: models.JSONB{}, StartedAt: time.Now(), LastActivityAt: time.Now()}
				require.NoError(t, app.DB.Create(session).Error)
				flow := newTransferFlow(t, app, org, account, map[string]any{"team_id": team.ID.String()})
				require.NoError(t, app.runChatGraph(account, contact, session, flow, "quero falar com alguém", "", nil))
				var tr models.AgentTransfer
				require.NoError(t, app.DB.Where("contact_id = ? AND status = ?", contact.ID, models.TransferStatusActive).First(&tr).Error)
				require.Equal(t, team.ID, *tr.TeamID)
				return tr.AgentID
			}

			// Phase 1: only `online` is eligible. Several handoffs, so round robin
			// would have rotated onto the others if it could.
			for range 4 {
				got := handoff()
				if strategy == models.AssignmentStrategyManual {
					assert.Nil(t, got, "manual queue never auto-assigns")
					continue
				}
				require.NotNil(t, got)
				assert.Equal(t, online.ID, *got, "away/offline agents must not receive")
			}

			// Phase 2: the away agent comes back and the other one reconnects;
			// both are eligible again and must receive the next handoffs.
			require.NoError(t, app.DB.Model(away).Update("is_available", true).Error)
			delete(offline, disconnected.ID)
			seen := map[uuid.UUID]bool{}
			for range 6 {
				if got := handoff(); got != nil {
					seen[*got] = true
				}
			}
			if strategy == models.AssignmentStrategyManual {
				assert.Empty(t, seen)
				return
			}
			assert.True(t, seen[away.ID], "back from away: receives again")
			assert.True(t, seen[disconnected.ID], "reconnected: receives again")
		})
	}
}
