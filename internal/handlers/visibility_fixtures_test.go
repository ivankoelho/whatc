package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
)

// Conversation visibility is strict, so a fixture that wants a user to work a contact has to say why they may:
// ownContact puts the contact in the user's carteira, shareContact puts it in a team all the users belong to.
func ownContact(t *testing.T, app *handlers.App, contact *models.Contact, user *models.User) {
	t.Helper()
	require.NoError(t, app.DB.Model(contact).Update("assigned_user_id", user.ID).Error)
}

func shareContact(t *testing.T, app *handlers.App, orgID uuid.UUID, contact *models.Contact, users ...*models.User) {
	t.Helper()
	team := createTeamWithMember(t, app, orgID, users[0].ID)
	for _, u := range users[1:] {
		require.NoError(t, app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: u.ID, Role: models.TeamRoleAgent}).Error)
	}
	require.NoError(t, app.DB.Model(contact).Update("team_id", team.ID).Error)
}
