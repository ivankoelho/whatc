package handlers_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

var caseSupervisorPerms = []string{"occurrences:read", "occurrences:write", "occurrences:view_all", "team_chat:read", "team_chat:write"}

// occurrences:view_all is organization-wide supervision of CASES: it never crosses
// the organization, in the API nor in the real-time authorization.
func TestOccurrenceViewAll_StopsAtTheOrganization(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	mine := chatUser(t, w.app, w.org.ID, caseSupervisorPerms)
	other := newIsoWorld(t)
	foreign := chatUser(t, w.app, other.org.ID, caseSupervisorPerms)

	code, _ := w.call(t, w.app.GetOccurrence, mine.ID, w.occID, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	code, _ = chatDo(t, w.app.GetOccurrence, other.org.ID, foreign.ID, nil, map[string]string{"id": w.occID.String()}, nil)
	assert.NotEqual(t, fasthttp.StatusOK, code, "another organization, even supervising all of its own cases")
	code, _ = chatDo(t, w.app.GetOccurrenceWhatsApp, other.org.ID, foreign.ID, nil, map[string]string{"id": w.occID.String()}, nil)
	assert.NotEqual(t, fasthttp.StatusOK, code)

	// The WebSocket authorization follows the same rule.
	assert.True(t, w.app.CanViewOccurrenceByID(mine.ID, w.org.ID, w.occID))
	assert.False(t, w.app.CanViewOccurrenceByID(foreign.ID, other.org.ID, w.occID))
	assert.False(t, w.app.CanViewOccurrenceByID(foreign.ID, w.org.ID, w.occID), "a foreign user claiming this organization")
}

// Case supervision and conversation access are independent permissions.
func TestOccurrencePermissions_AreIndependent(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	chat := []string{"chat:read", "chat:write"}
	casesOnly := chatUser(t, w.app, w.org.ID, caseSupervisorPerms)
	convOnly := chatUser(t, w.app, w.org.ID, append([]string{"occurrences:read", "occurrences:write", "conversations:view_all"}, chat...))
	both := chatUser(t, w.app, w.org.ID, append(append([]string{}, caseSupervisorPerms...), append(chat, "conversations:view_all")...))

	restricted := func(u *models.User) bool {
		_, body := w.call(t, w.app.GetOccurrenceWhatsApp, u.ID, w.occID, nil, nil)
		return strings.Contains(string(body), `"restricted":true`)
	}
	assert.True(t, restricted(casesOnly), "supervising cases does not open the conversation")
	assert.False(t, restricted(convOnly), "explicit conversations:view_all does")
	assert.False(t, restricted(both))

	// The conversation holder still sees the case (it implied supervision before and keeps doing so).
	code, _ := w.call(t, w.app.GetOccurrence, convOnly.ID, w.occID, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	// Reading and sending are authorized separately.
	readOnly := chatUser(t, w.app, w.org.ID, []string{"occurrences:read", "occurrences:write", "conversations:view_all", "chat:read"})
	assert.False(t, restricted(readOnly))
	code, _ = w.call(t, w.app.SendOccurrenceWhatsApp, readOnly.ID, w.occID, map[string]any{"content": "oi"}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
}

// The account's default team is an explicit authorization; another store's team,
// or another organization, is not.
func TestCaseWhatsApp_AccountTeamAuthorizationDoesNotCrossStoresOrOrganizations(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	perms := append([]string{"chat:read", "chat:write", "conversations:view_team"}, caseSupervisorPerms...)
	mkTeam := func(orgID uuid.UUID, name string) *models.Team {
		team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: orgID, Name: name, IsActive: true}
		require.NoError(t, w.app.DB.Create(team).Error)
		return team
	}
	join := func(team *models.Team, u *models.User) {
		require.NoError(t, w.app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: u.ID, Role: "agent"}).Error)
	}
	owner := mkTeam(w.org.ID, "Dona da conta")
	require.NoError(t, w.app.DB.Model(w.account).Update("default_team_id", owner.ID).Error)
	elsewhere := mkTeam(w.org.ID, "Outra loja")

	member := chatUser(t, w.app, w.org.ID, perms)
	join(owner, member)
	otherStore := chatUser(t, w.app, w.org.ID, perms)
	join(elsewhere, otherStore)
	foreignOrg := newIsoWorld(t)
	foreign := chatUser(t, w.app, foreignOrg.org.ID, perms)

	restricted := func(u *models.User) bool {
		_, body := w.call(t, w.app.GetOccurrenceWhatsApp, u.ID, w.occID, nil, nil)
		return strings.Contains(string(body), `"restricted":true`)
	}
	assert.False(t, restricted(member), "member of the account's team")
	assert.True(t, restricted(otherStore), "a team of another store")
	code, _ := w.call(t, w.app.SendOccurrenceWhatsApp, otherStore.ID, w.occID, map[string]any{"content": "oi"}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = w.call(t, w.app.GetOccurrenceWhatsApp, w.logOutra.ID, w.occID, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "logistics of another store")
	code, _ = chatDo(t, w.app.GetOccurrenceWhatsApp, foreignOrg.org.ID, foreign.ID, nil, map[string]string{"id": w.occID.String()}, nil)
	assert.NotEqual(t, fasthttp.StatusOK, code, "another organization")
}

// Changing a role takes effect at once: the cached permissions of its users are dropped.
func TestPermissionCache_IsInvalidatedWhenARoleChanges(t *testing.T) {
	w := newChatWorld(t)
	w.run()
	u := chatUser(t, w.app, w.org.ID, []string{"occurrences:read", "occurrences:write", "chat:read", "chat:write"})
	require.NotNil(t, u.RoleID)
	admin := chatUser(t, w.app, w.org.ID, []string{"roles:read", "roles:write"})

	// readable: the customer conversation comes back to this user.
	readable := func() bool {
		_, body := w.call(t, w.app.GetOccurrenceWhatsApp, u.ID, w.occID, nil, nil)
		return strings.Contains(string(body), `"restricted":false`) && strings.Contains(string(body), `"messages"`)
	}
	code, _ := w.call(t, w.app.GetOccurrence, u.ID, w.occID, nil, nil)
	require.NotEqual(t, fasthttp.StatusOK, code, "no case access yet")
	require.False(t, readable())

	update := func(perms []string) {
		code, body := chatDo(t, w.app.UpdateRole, w.org.ID, admin.ID, map[string]any{"permissions": perms}, map[string]string{"id": u.RoleID.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, code, string(body))
	}
	update([]string{"occurrences:read", "occurrences:write", "chat:read", "chat:write", "conversations:view_all"})
	assert.True(t, readable(), "granted: effective immediately")
	update([]string{"occurrences:read", "occurrences:write", "chat:read", "chat:write"})
	assert.False(t, readable(), "revoked: effective immediately")
}

