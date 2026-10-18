package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// carteiraEnv is an organization where agent A holds the carteira (relationship)
// of some contacts and agent B owns some attendances. Both A and B have a role
// WITHOUT contacts:read, which is what makes the legacy "assigned contacts" scope
// the thing that decides what they can see.
type carteiraEnv struct {
	app      *handlers.App
	org      *models.Organization
	a, b     *models.User
	contacts map[string]*models.Contact
}

func newCarteiraEnv(t *testing.T, tolerance bool) *carteiraEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	all := testutil.GetOrCreateTestPermissions(t, app.DB)
	var perms []models.Permission
	for _, p := range all {
		if (p.Resource == "transfers" && (p.Action == "read" || p.Action == "pickup")) || p.Resource == "chat" {
			perms = append(perms, p)
		}
	}
	require.NotEmpty(t, perms)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Restricted agent", false, false, perms)
	a := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithEmail(testutil.UniqueEmail("cart-a")))
	b := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID), testutil.WithEmail(testutil.UniqueEmail("cart-b")))

	upsertChatbotSettings(t, app, org.ID, true)
	setAwayTolerance(t, app, org.ID, tolerance, 10)

	e := &carteiraEnv{app: app, org: org, a: a, b: b, contacts: map[string]*models.Contact{}}
	mk := func(name string, carteira *models.User, transferTo *models.User, queued bool, status models.TransferStatus) {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		if carteira != nil {
			require.NoError(t, app.DB.Model(c).Update("assigned_user_id", carteira.ID).Error)
		}
		if transferTo != nil || queued {
			var owner *uuid.UUID
			if transferTo != nil {
				owner = &transferTo.ID
			}
			require.NoError(t, app.DB.Create(&models.AgentTransfer{
				BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: c.ID,
				PhoneNumber: "+550000", Status: status, Source: models.TransferSourceManual, AgentID: owner,
			}).Error)
		}
		e.contacts[name] = c
	}
	active := models.TransferStatusActive
	mk("carteira A, B owns it", a, b, false, active)                                           // the case of the plan
	mk("carteira A, no attendance", a, nil, false, active)                                     //
	mk("carteira A, waiting in the queue", a, nil, true, active)                               // nobody owns it yet
	mk("carteira A, A owns it", a, a, false, active)                                           //
	mk("carteira A, B's attendance already closed", a, b, false, models.TransferStatusResumed) //
	mk("no carteira, B owns it", nil, b, false, active)                                        //
	mk("carteira B, A owns it", b, a, false, active)                                           //
	return e
}

func (e *carteiraEnv) canView(t *testing.T, user *models.User, name string) bool {
	t.Helper()
	var fresh models.Contact
	require.NoError(t, e.app.DB.First(&fresh, "id = ?", e.contacts[name].ID).Error)
	return e.app.CanViewConversationForTest(user.ID, e.org.ID, &fresh)
}

func (e *carteiraEnv) scopeIDs(t *testing.T, user *models.User) map[uuid.UUID]bool {
	t.Helper()
	var visible []models.Contact
	q := e.app.ScopeVisibleConversationsForTest(e.app.DB.Where("organization_id = ?", e.org.ID), user.ID, e.org.ID)
	require.NoError(t, q.Find(&visible).Error)
	out := map[uuid.UUID]bool{}
	for _, c := range visible {
		out[c.ID] = true
	}
	return out
}

// With the away tolerance on, the carteira alone does not open a conversation that
// an active attendance gave to ANOTHER agent. Everything else is as before.
func TestVisibility_CarteiraDoesNotOpenAnotherAgentsConversation_WhenOn(t *testing.T) {
	e := newCarteiraEnv(t, true)

	assert.False(t, e.canView(t, e.a, "carteira A, B owns it"), "the former agent keeps the relationship, not the access")
	assert.True(t, e.canView(t, e.b, "carteira A, B owns it"), "the owner sees it")

	assert.True(t, e.canView(t, e.a, "carteira A, no attendance"), "no attendance: the carteira rules")
	assert.True(t, e.canView(t, e.a, "carteira A, waiting in the queue"), "nobody owns it yet")
	assert.True(t, e.canView(t, e.a, "carteira A, A owns it"))
	assert.True(t, e.canView(t, e.a, "carteira A, B's attendance already closed"), "a closed attendance does not own anything")
	assert.False(t, e.canView(t, e.a, "no carteira, B owns it"))
	assert.True(t, e.canView(t, e.a, "carteira B, A owns it"), "owning the attendance is enough")
}

// Without the opt-in, the original rule stands exactly (organizations that did not
// ask for the change keep what they had).
func TestVisibility_CarteiraRuleIsUnchanged_WhenOff(t *testing.T) {
	e := newCarteiraEnv(t, false)

	assert.True(t, e.canView(t, e.a, "carteira A, B owns it"), "original behaviour: the carteira still opens it")
	assert.True(t, e.canView(t, e.b, "carteira A, B owns it"))
	assert.True(t, e.canView(t, e.a, "carteira A, no attendance"))
	assert.False(t, e.canView(t, e.a, "no carteira, B owns it"))
	assert.True(t, e.canView(t, e.a, "carteira B, A owns it"))
}

// The function and its SQL twin agree for both agents, with the option on and off.
func TestVisibility_ScopeMatchesFunction_CarteiraRule(t *testing.T) {
	for name, tolerance := range map[string]bool{"on": true, "off": false} {
		t.Run(name, func(t *testing.T) {
			e := newCarteiraEnv(t, tolerance)
			for _, user := range []*models.User{e.a, e.b} {
				expected := map[uuid.UUID]bool{}
				for n, c := range e.contacts {
					if e.canView(t, user, n) {
						expected[c.ID] = true
					}
				}
				assert.Equal(t, expected, e.scopeIDs(t, user),
					"scopeVisibleConversations must return exactly what canViewConversation allows (tolerance %s)", name)
			}
		})
	}
}

// The backend enforces it where it acts, not only where it lists: a former agent
// cannot take over, release or interact with a conversation another agent owns.
func TestVisibility_CarteiraCannotBeUsedToActOnAnotherAgentsConversation(t *testing.T) {
	e := newCarteiraEnv(t, true)
	var transfer models.AgentTransfer
	require.NoError(t, e.app.DB.Where("contact_id = ?", e.contacts["carteira A, B owns it"].ID).First(&transfer).Error)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, e.org.ID, e.a.ID)
	testutil.SetPathParam(req, "id", transfer.ID.String())
	require.NoError(t, e.app.AssignAgentTransfer(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req),
		"A holds only the carteira: the conversation belongs to B")

	var stored models.AgentTransfer
	require.NoError(t, e.app.DB.First(&stored, "id = ?", transfer.ID).Error)
	require.NotNil(t, stored.AgentID)
	assert.Equal(t, e.b.ID, *stored.AgentID, "B's ownership is untouched")

	// B, the owner, passes the same gate (the answer is not a 403).
	reqB := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(reqB, e.org.ID, e.b.ID)
	testutil.SetPathParam(reqB, "id", transfer.ID.String())
	require.NoError(t, e.app.AssignAgentTransfer(reqB))
	assert.NotEqual(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(reqB))
}
