package handlers_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Concluding an attendance is part of attending: chat:write is enough, contacts:write is not required
// (the agent role has contacts:read only on purpose). WHO may act on WHICH conversation is still the
// visibility rule's job. These tests pin both halves.

type concludeEnv struct {
	app   *handlers.App
	org   *models.Organization
	team  *models.Team
	acct  *models.WhatsAppAccount // its default team is the team: where a freed conversation goes back to
	agent *models.User            // owns the attendance
	other *models.User            // another agent of the organization, in no team with the owner
}

func newConcludeEnv(t *testing.T) *concludeEnv {
	t.Helper()
	app := usageApp(t) // with usage recording on, so the status events are written
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	team := &models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Atendimento " + uuid.NewString()[:6], IsActive: true}
	require.NoError(t, app.DB.Create(team).Error)
	require.NoError(t, app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: agent.ID, Role: models.TeamRoleAgent}).Error)
	acct := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(acct).Update("default_team_id", team.ID).Error)
	return &concludeEnv{app: app, org: org, team: team, acct: acct, agent: agent, other: other}
}

// attendance opens an active attendance of a new contact, owned by owner.
func (e *concludeEnv) attendance(t *testing.T, owner *models.User) (*models.Contact, *models.AgentTransfer) {
	t.Helper()
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": owner.ID, "contact_status": models.ContactStatusInProgress,
		"whats_app_account": e.acct.Name, // closing frees the conversation and clears its team: it falls back to the account default team
	}).Error)
	tr := &models.AgentTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: e.org.ID, ContactID: contact.ID, PhoneNumber: contact.PhoneNumber,
		Status: models.TransferStatusActive, Source: models.TransferSourceManual, AgentID: &owner.ID, TeamID: &e.team.ID, TransferredAt: time.Now(),
	}
	require.NoError(t, e.app.DB.Create(tr).Error)
	return contact, tr
}

func (e *concludeEnv) put(t *testing.T, user *models.User, contact *models.Contact, status string) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"contact_status": status})
	testutil.SetAuthContext(req, e.org.ID, user.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, e.app.UpdateContactStatus(req))
	return testutil.GetResponseStatusCode(req)
}

func (e *concludeEnv) state(t *testing.T, contact *models.Contact, tr *models.AgentTransfer) (models.Contact, models.AgentTransfer) {
	t.Helper()
	var c models.Contact
	var x models.AgentTransfer
	require.NoError(t, e.app.DB.First(&c, "id = ?", contact.ID).Error)
	require.NoError(t, e.app.DB.First(&x, "id = ?", tr.ID).Error)
	return c, x
}

// 1. The attendant who owns the attendance concludes it with the agent role as it ships (no contacts:write).
func TestConclude_OwnerAgentWithoutContactsWriteConcludes(t *testing.T) {
	t.Parallel()
	e := newConcludeEnv(t)
	roleKeys := e.app.HasPermission(e.agent.ID, models.ResourceContacts, models.ActionWrite, e.org.ID)
	require.False(t, roleKeys, "precondition: the agent role does not have contacts:write")
	contact, tr := e.attendance(t, e.agent)

	require.Equal(t, fasthttp.StatusOK, e.put(t, e.agent, contact, "resolved"))

	c, x := e.state(t, contact, tr)
	assert.Equal(t, models.ContactStatusResolved, c.ContactStatus)
	assert.Nil(t, c.AssignedUserID, "the contact is freed")
	assert.Equal(t, models.TransferStatusResumed, x.Status, "the human attendance is closed (back to the automatic flow)")
	require.NotNil(t, x.ResumedBy)
	assert.Equal(t, e.agent.ID, *x.ResumedBy, "and it records who closed it")
}

// 2. An agent with no access to the conversation cannot conclude it, and nothing changes.
func TestConclude_AgentWithoutAccessToTheConversationIsRefused(t *testing.T) {
	t.Parallel()
	e := newConcludeEnv(t)
	contact, tr := e.attendance(t, e.agent)

	assert.Equal(t, fasthttp.StatusForbidden, e.put(t, e.other, contact, "resolved"))

	c, x := e.state(t, contact, tr)
	assert.Equal(t, models.ContactStatusInProgress, c.ContactStatus)
	assert.Equal(t, models.TransferStatusActive, x.Status)
	require.NotNil(t, c.AssignedUserID)
}

// 3 + 4. Roles that cannot attend (no chat:write) and the contacts:read-only roles keep being refused,
// through the API, whether or not they have a role at all.
func TestConclude_RolesWithoutChatWriteAreRefused(t *testing.T) {
	t.Parallel()
	e := newConcludeEnv(t)
	readOnly := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "somente-leitura", []string{"chat:read", "contacts:read", "occurrences:read"})
	reader := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&readOnly.ID))
	noRole := testutil.CreateTestUser(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: e.team.ID, UserID: reader.ID, Role: models.TeamRoleAgent}).Error)
	contact, tr := e.attendance(t, e.agent)

	for name, u := range map[string]*models.User{"role with chat:read and contacts:read only": reader, "user without a role": noRole} {
		for _, status := range []string{"resolved", "in_progress", "new"} {
			assert.Equal(t, fasthttp.StatusForbidden, e.put(t, u, contact, status), "%s -> %s", name, status)
		}
	}
	c, x := e.state(t, contact, tr)
	assert.Equal(t, models.ContactStatusInProgress, c.ContactStatus)
	assert.Equal(t, models.TransferStatusActive, x.Status)
}

// chat:write may conclude or reopen, but sending a conversation back to "new" stays with contacts:write.
func TestConclude_ChatWriteCannotSetNewButCanReopen(t *testing.T) {
	t.Parallel()
	e := newConcludeEnv(t)
	contact, _ := e.attendance(t, e.agent)

	assert.Equal(t, fasthttp.StatusForbidden, e.put(t, e.agent, contact, "new"), "an arbitrary reset is a contact-record decision")

	require.Equal(t, fasthttp.StatusOK, e.put(t, e.agent, contact, "resolved"))
	require.Equal(t, fasthttp.StatusOK, e.put(t, e.agent, contact, "in_progress"), "reopening mirrors concluding")
	var c models.Contact
	require.NoError(t, e.app.DB.First(&c, "id = ?", contact.ID).Error)
	assert.Equal(t, models.ContactStatusInProgress, c.ContactStatus)
}

// 5. A supervisor keeps exactly what the RBAC gives them: view_team widens WHICH conversations (a teammate's),
// not the permission to attend; contacts:write holders keep every transition.
func TestConclude_SupervisorKeepsTheRBACBehaviour(t *testing.T) {
	t.Parallel()
	e := newConcludeEnv(t)
	contact, tr := e.attendance(t, e.agent)

	// view_team + chat:write, in the owner's team: may conclude a teammate's attendance
	teamSup := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "supervisor-equipe", []string{"chat:read", "chat:write", "contacts:read", "conversations:view_team"})
	sup := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&teamSup.ID))
	require.NoError(t, e.app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: e.team.ID, UserID: sup.ID, Role: models.TeamRoleManager}).Error)
	// the same role OUTSIDE the owner's team: refused by the visibility rule, not by the new permission
	outsider := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&teamSup.ID))
	// contacts:write holder without chat:write keeps the old behaviour for conversations he can reach
	admin := testutil.CreateAdminRole(t, e.app.DB, e.org.ID)
	boss := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&admin.ID))

	assert.Equal(t, fasthttp.StatusForbidden, e.put(t, outsider, contact, "resolved"), "view_team without sharing a team with the owner")
	require.Equal(t, fasthttp.StatusOK, e.put(t, sup, contact, "resolved"))
	_, x := e.state(t, contact, tr)
	assert.Equal(t, models.TransferStatusResumed, x.Status)

	// an admin can still set "new" (contacts:write), which no chat:write-only role can
	assert.Equal(t, fasthttp.StatusOK, e.put(t, boss, contact, "new"))
}

// 6 + 7. Repeated clicks do not repeat the effects: one close, one status event, same stored state.
func TestConclude_RepeatedClicksHaveNoDuplicateEffects(t *testing.T) {
	t.Parallel()
	e := newConcludeEnv(t)
	contact, tr := e.attendance(t, e.agent)

	require.Equal(t, fasthttp.StatusOK, e.put(t, e.agent, contact, "resolved"))
	_, first := e.state(t, contact, tr)
	require.NotNil(t, first.ResumedAt)

	// the second click, and a third from a stale page: already concluded, so nothing changes again
	assert.Equal(t, fasthttp.StatusOK, e.put(t, e.agent, contact, "resolved"))
	assert.Equal(t, fasthttp.StatusOK, e.put(t, e.agent, contact, "resolved"))

	c, second := e.state(t, contact, tr)
	assert.Equal(t, models.ContactStatusResolved, c.ContactStatus, "still concluded (what a refresh shows)")
	assert.Equal(t, models.TransferStatusResumed, second.Status)
	assert.True(t, first.ResumedAt.Equal(*second.ResumedAt), "the close time never moves")
	resolved := 0
	for _, ev := range statusEvents(t, e.app, contact.ID) {
		if ev.ToStatus == string(models.ContactStatusResolved) {
			resolved++
		}
	}
	assert.Equal(t, 1, resolved, "exactly one resolved event")
}
