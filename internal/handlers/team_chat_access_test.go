package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newChatChannel(t *testing.T, app *handlers.App, orgID uuid.UUID, kind models.ChatChannelKind, teamID *uuid.UUID) *models.ChatChannel {
	t.Helper()
	ch := &models.ChatChannel{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		Kind:           kind,
		TeamID:         teamID,
		Name:           "ch-" + uuid.New().String()[:6],
	}
	require.NoError(t, app.DB.Create(ch).Error)
	return ch
}

func addChannelMember(t *testing.T, app *handlers.App, ch *models.ChatChannel, userID uuid.UUID) {
	t.Helper()
	require.NoError(t, app.DB.Create(&models.ChatChannelMember{
		OrganizationID: ch.OrganizationID, ChannelID: ch.ID, UserID: userID, Role: "member",
	}).Error)
}

func TestCanAccessChatChannel(t *testing.T) {
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	member := testutil.CreateTestUser(t, app.DB, orgA.ID)
	outsider := testutil.CreateTestUser(t, app.DB, orgA.ID)
	otherOrgUser := testutil.CreateTestUser(t, app.DB, orgB.ID)

	t.Run("team channel follows team membership", func(t *testing.T) {
		team := createTeamWithMember(t, app, orgA.ID, member.ID)
		ch := newChatChannel(t, app, orgA.ID, models.ChatChannelKindTeam, &team.ID)

		assert.True(t, app.CanAccessChatChannelForTest(member.ID, orgA.ID, ch))
		assert.False(t, app.CanAccessChatChannelForTest(outsider.ID, orgA.ID, ch))

		// Leaving the team removes access at once; rejoining restores it.
		require.NoError(t, app.DB.Where("team_id = ? AND user_id = ?", team.ID, member.ID).
			Delete(&models.TeamMember{}).Error)
		assert.False(t, app.CanAccessChatChannelForTest(member.ID, orgA.ID, ch))

		require.NoError(t, app.DB.Create(&models.TeamMember{
			BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: member.ID, Role: models.TeamRoleAgent,
		}).Error)
		assert.True(t, app.CanAccessChatChannelForTest(member.ID, orgA.ID, ch))
	})

	t.Run("free channel and DM need a membership row", func(t *testing.T) {
		for _, kind := range []models.ChatChannelKind{models.ChatChannelKindChannel, models.ChatChannelKindDM} {
			ch := newChatChannel(t, app, orgA.ID, kind, nil)
			addChannelMember(t, app, ch, member.ID)
			assert.True(t, app.CanAccessChatChannelForTest(member.ID, orgA.ID, ch), string(kind))
			assert.False(t, app.CanAccessChatChannelForTest(outsider.ID, orgA.ID, ch), string(kind))
		}
	})

	t.Run("cross organization is denied even for a member", func(t *testing.T) {
		ch := newChatChannel(t, app, orgA.ID, models.ChatChannelKindChannel, nil)
		addChannelMember(t, app, ch, member.ID)
		assert.False(t, app.CanAccessChatChannelForTest(member.ID, orgB.ID, ch), "org from session differs")
		assert.False(t, app.CanAccessChatChannelForTest(otherOrgUser.ID, orgB.ID, ch))
	})

	t.Run("team from another organization grants nothing", func(t *testing.T) {
		teamB := createTeamWithMember(t, app, orgB.ID, otherOrgUser.ID)
		ch := newChatChannel(t, app, orgA.ID, models.ChatChannelKindTeam, &teamB.ID)
		assert.False(t, app.CanAccessChatChannelForTest(otherOrgUser.ID, orgA.ID, ch))
		assert.False(t, app.CanAccessChatChannelForTest(otherOrgUser.ID, orgB.ID, ch))
	})

	t.Run("loadChatChannel hides other tenants", func(t *testing.T) {
		ch := newChatChannel(t, app, orgA.ID, models.ChatChannelKindChannel, nil)
		_, ok := app.LoadChatChannelForTest(orgA.ID, ch.ID)
		assert.True(t, ok)
		_, ok = app.LoadChatChannelForTest(orgB.ID, ch.ID)
		assert.False(t, ok)
	})
}

func TestCanDeleteChatMessage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)

	modRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mod", []string{"team_chat:read", "team_chat:write", "team_chat:delete"})
	plainRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "plain", []string{"team_chat:read", "team_chat:write"})
	foreignRole := testutil.CreateTestRoleWithKeys(t, app.DB, orgB.ID, "mod", []string{"team_chat:read", "team_chat:write", "team_chat:delete"})
	author := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&plainRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&plainRole.ID))
	moderator := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&modRole.ID))
	foreignMod := testutil.CreateTestUser(t, app.DB, orgB.ID, testutil.WithRoleID(&foreignRole.ID))

	ch := newChatChannel(t, app, org.ID, models.ChatChannelKindChannel, nil)
	addChannelMember(t, app, ch, author.ID)
	addChannelMember(t, app, ch, other.ID)
	msg := &models.ChatMessage{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ChannelID: ch.ID, UserID: author.ID, Body: "hi",
	}
	require.NoError(t, app.DB.Create(msg).Error)

	assert.True(t, app.CanDeleteChatMessageForTest(author.ID, org.ID, msg, ch), "author with access")
	assert.False(t, app.CanDeleteChatMessageForTest(other.ID, org.ID, msg, ch), "member without delete permission")
	assert.False(t, app.CanDeleteChatMessageForTest(moderator.ID, org.ID, msg, ch), "delete permission without channel access")

	addChannelMember(t, app, ch, moderator.ID)
	assert.True(t, app.CanDeleteChatMessageForTest(moderator.ID, org.ID, msg, ch), "delete permission plus channel access")

	assert.False(t, app.CanDeleteChatMessageForTest(foreignMod.ID, orgB.ID, msg, ch), "delete permission from another organization")

	require.NoError(t, app.DB.Where("channel_id = ? AND user_id = ?", ch.ID, author.ID).
		Delete(&models.ChatChannelMember{}).Error)
	assert.False(t, app.CanDeleteChatMessageForTest(author.ID, org.ID, msg, ch), "author who lost channel access")
}

func TestChatChannelUniqueness(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	u1 := testutil.CreateTestUser(t, app.DB, org.ID)
	u2 := testutil.CreateTestUser(t, app.DB, org.ID)
	team := createTeamWithMember(t, app, org.ID, u1.ID)

	t.Run("one team channel per team", func(t *testing.T) {
		newChatChannel(t, app, org.ID, models.ChatChannelKindTeam, &team.ID)
		dup := &models.ChatChannel{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
			Kind: models.ChatChannelKindTeam, TeamID: &team.ID}
		assert.Error(t, app.DB.Create(dup).Error)
	})

	t.Run("one DM per canonical pair, whoever opens it", func(t *testing.T) {
		low, high := models.CanonicalDMPair(u1.ID, u2.ID)
		first := &models.ChatChannel{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
			Kind: models.ChatChannelKindDM, DMUserLowID: &low, DMUserHighID: &high}
		require.NoError(t, app.DB.Create(first).Error)

		low2, high2 := models.CanonicalDMPair(u2.ID, u1.ID)
		second := &models.ChatChannel{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
			Kind: models.ChatChannelKindDM, DMUserLowID: &low2, DMUserHighID: &high2}
		assert.Error(t, app.DB.Create(second).Error)
	})

	t.Run("a member is added to a channel once", func(t *testing.T) {
		ch := newChatChannel(t, app, org.ID, models.ChatChannelKindChannel, nil)
		addChannelMember(t, app, ch, u1.ID)
		assert.Error(t, app.DB.Create(&models.ChatChannelMember{
			OrganizationID: org.ID, ChannelID: ch.ID, UserID: u1.ID, Role: "member"}).Error)
	})
}
