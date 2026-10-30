package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Regression tests for the findings of the functional homologation: configuration of the organization, the
// organizations list and creation, conversation visibility by default, handing a conversation over, and the
// lists that answered without the permission.

func settingsOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var env struct {
		Data struct {
			Settings map[string]any `json:"settings"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	return env.Data.Settings
}

// R4: being logged in does not authorise changing the organization's configuration.
func TestOrgSettings_NeedSettingsGeneralWrite(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := chatUser(t, app, org.ID, []string{"chat:read", "chat:write", "contacts:read"})
	admin := chatUser(t, app, org.ID, []string{"settings.general:read", "settings.general:write"})
	integ := chatUser(t, app, org.ID, []string{"settings.general:write", "accounts:write", "accounts:read"})

	code, _ := chatDo(t, app.UpdateOrganizationSettings, org.ID, agent.ID, map[string]any{"mask_phone_numbers": true, "timezone": "America/Manaus"}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "an agent cannot change the settings")
	code, _ = chatDo(t, app.UpdateOrganizationSettings, org.ID, agent.ID, map[string]any{}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "not even with an empty body: authorisation does not depend on the payload")
	code, raw := chatDo(t, app.GetOrganizationSettings, org.ID, agent.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, false, settingsOf(t, raw)["mask_phone_numbers"], "and nothing changed")
	assert.NotEqual(t, "America/Manaus", settingsOf(t, raw)["timezone"])

	code, _ = chatDo(t, app.UpdateOrganizationSettings, org.ID, admin.ID, map[string]any{"timezone": "America/Manaus"}, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code, "settings.general:write may")
	_, raw = chatDo(t, app.GetOrganizationSettings, org.ID, admin.ID, nil, nil, nil)
	assert.Equal(t, "America/Manaus", settingsOf(t, raw)["timezone"])

	// the Meta app credentials are a separate permission
	code, _ = chatDo(t, app.UpdateOrganizationSettings, org.ID, admin.ID, map[string]any{"meta_app_id": "APP-1"}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "general settings alone do not open the integration credentials")
	code, _ = chatDo(t, app.UpdateOrganizationSettings, org.ID, integ.ID, map[string]any{"meta_app_id": "APP-1"}, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	_, raw = chatDo(t, app.GetOrganizationSettings, org.ID, agent.ID, nil, nil, nil)
	assert.Empty(t, settingsOf(t, raw)["meta_app_id"], "the integration identifiers are not shown to everybody")
	_, raw = chatDo(t, app.GetOrganizationSettings, org.ID, integ.ID, nil, nil, nil)
	assert.Equal(t, "APP-1", settingsOf(t, raw)["meta_app_id"])

	// another organization's settings cannot be reached by naming it
	other := testutil.CreateTestOrganization(t, app.DB)
	code, _ = chatDo(t, app.UpdateOrganizationSettings, other.ID, admin.ID, map[string]any{"timezone": "UTC"}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "the caller holds no role in the other organization")
}

// R1-R3: an organization's administrator sees and creates nothing outside it.
func TestOrganizations_ListIsScopedAndCreationIsForSystemAdminsOnly(t *testing.T) {
	app := newTestApp(t)
	orgA := testutil.CreateTestOrganization(t, app.DB)
	orgB := testutil.CreateTestOrganization(t, app.DB)
	admin := chatUser(t, app, orgA.ID, []string{"organizations:read", "organizations:write", "organizations:assign"})
	manager := chatUser(t, app, orgA.ID, []string{"organizations:read"})
	role := testutil.CreateAdminRole(t, app.DB, orgA.ID)
	super := testutil.CreateTestUser(t, app.DB, orgA.ID, testutil.WithRoleID(&role.ID), testutil.WithSuperAdmin())

	names := func(raw []byte) []string {
		var env struct {
			Data struct {
				Organizations []struct {
					ID string `json:"id"`
				} `json:"organizations"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &env))
		var ids []string
		for _, o := range env.Data.Organizations {
			ids = append(ids, o.ID)
		}
		return ids
	}
	for _, u := range []*models.User{admin, manager} {
		code, raw := chatDo(t, app.ListOrganizations, orgA.ID, u.ID, nil, nil, nil)
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Equal(t, []string{orgA.ID.String()}, names(raw), "only the caller's own organization")
		assert.NotContains(t, string(raw), orgB.ID.String())
	}
	_, raw := chatDo(t, app.ListOrganizations, orgA.ID, super.ID, nil, nil, nil)
	assert.Contains(t, names(raw), orgA.ID.String())
	assert.Contains(t, names(raw), orgB.ID.String(), "a system administrator sees them all")

	var before int64
	app.DB.Model(&models.Organization{}).Count(&before)
	code, _ := chatDo(t, app.CreateOrganization, orgA.ID, admin.ID, map[string]any{"name": "Intruder Org"}, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code, "organizations:write alone does not make a tenant administrator a platform one")
	var after int64
	app.DB.Model(&models.Organization{}).Count(&after)
	assert.Equal(t, before, after, "nothing was created")
	code, _ = chatDo(t, app.CreateOrganization, orgA.ID, super.ID, map[string]any{"name": "Platform Org"}, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
}

// R7: with the default configuration a conversation is not open to whoever holds contacts:read.
func TestConversationVisibility_IsStrictWithoutAnyConfiguration(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAgentRole(t, app.DB, org.ID) // contacts:read, no view_all
	owner := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, contact.ID, &owner.ID, nil)

	for name, h := range map[string]func(*testing.T, uuid.UUID) int{
		"contact": func(t *testing.T, uid uuid.UUID) int {
			c, _ := chatDo(t, app.GetContact, org.ID, uid, nil, map[string]string{"id": contact.ID.String()}, nil)
			return c
		},
		"messages": func(t *testing.T, uid uuid.UUID) int {
			c, _ := chatDo(t, app.GetMessages, org.ID, uid, nil, map[string]string{"id": contact.ID.String()}, nil)
			return c
		},
	} {
		assert.Equal(t, fasthttp.StatusOK, h(t, owner.ID), name+": the owner")
		assert.Equal(t, fasthttp.StatusNotFound, h(t, other.ID), name+": somebody with contacts:read but no part in the conversation")
	}
	assert.False(t, app.CanViewConversationForTest(other.ID, org.ID, contact))
}

// V2: assigning a contact moves the live attendance with it, so the visibility really changes.
func TestAssignContact_MovesTheActiveAttendance(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	roleKeys := []string{"chat:read", "chat:write", "contacts:read", "transfers:read", "transfers:write"}
	sac := chatUser(t, app, org.ID, roleKeys)
	seller := chatUser(t, app, org.ID, roleKeys)
	bystander := chatUser(t, app, org.ID, roleKeys)
	manager := chatUser(t, app, org.ID, []string{"contacts:read", "contacts:write", "conversations:view_all"})
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, contact.ID, &sac.ID, nil)

	assert.False(t, app.CanViewConversationForTest(seller.ID, org.ID, contact))
	code, raw := chatDo(t, app.AssignContact, org.ID, manager.ID, map[string]any{"user_id": seller.ID.String()}, map[string]string{"id": contact.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	var tr models.AgentTransfer
	require.NoError(t, app.DB.Where("contact_id = ? AND status = ?", contact.ID, models.TransferStatusActive).First(&tr).Error)
	require.NotNil(t, tr.AgentID)
	assert.Equal(t, seller.ID, *tr.AgentID, "the attendance's responsible agent changed, not only the carteira pointer")
	assert.True(t, app.CanViewConversationForTest(seller.ID, org.ID, contact), "the seller now sees the conversation")
	assert.False(t, app.CanViewConversationForTest(bystander.ID, org.ID, contact))
	assert.False(t, app.CanViewConversationForTest(sac.ID, org.ID, contact), "and the previous agent no longer owns it")
	var n int64
	app.DB.Table("audit_logs").Where("resource_type = ?", "agent_transfer").Count(&n)
}

// SAC -> Vendas: handing the conversation to a team keeps who did it, lets the team see it, and lets the SAC follow it
// without being able to answer in the seller's place.
func TestTransferHandover_TeamSeesItAndTheSenderFollowsReadOnly(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	keys := []string{"chat:read", "chat:write", "contacts:read", "transfers:read", "transfers:write"}
	sac := chatUser(t, app, org.ID, keys)
	seller := chatUser(t, app, org.ID, keys)
	otherStore := chatUser(t, app, org.ID, keys)
	team := createTeamWithMember(t, app, org.ID, seller.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	activeTransfer(t, app, org.ID, contact.ID, &sac.ID, nil)
	var tr models.AgentTransfer
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&tr).Error)

	code, raw := chatDo(t, app.AssignAgentTransfer, org.ID, sac.ID, map[string]any{"agent_id": nil, "team_id": team.ID.String()}, map[string]string{"id": tr.ID.String()}, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(raw))

	require.NoError(t, app.DB.First(&tr, tr.ID).Error)
	require.NotNil(t, tr.TeamID)
	assert.Equal(t, team.ID, *tr.TeamID, "it reached the commercial team")
	require.NotNil(t, tr.TransferredByUserID)
	assert.Equal(t, sac.ID, *tr.TransferredByUserID, "and the origin is recorded")
	assert.True(t, app.CanViewConversationForTest(seller.ID, org.ID, contact), "a member of the team sees it")
	assert.True(t, app.CanInteractWithConversationForTest(seller.ID, org.ID, contact), "and may answer it")
	assert.False(t, app.CanViewConversationForTest(otherStore.ID, org.ID, contact), "somebody outside the team does not")
	assert.True(t, app.CanViewConversationForTest(sac.ID, org.ID, contact), "the SAC keeps following it")
	assert.False(t, app.CanInteractWithConversationForTest(sac.ID, org.ID, contact), "read only: it no longer answers in the seller's place")
}

// R5/R6 and U2: the lists answer only to the permission that governs them.
func TestLists_NeedTheirPermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := chatUser(t, app, org.ID, []string{"chat:read", "contacts:read"})
	hasRoles := chatUser(t, app, org.ID, []string{"roles:read"})
	canPick := chatUser(t, app, org.ID, []string{"users:write"})
	hasTemplates := chatUser(t, app, org.ID, []string{"templates:read"})
	handsOver := chatUser(t, app, org.ID, []string{"transfers:write"})

	code, _ := chatDo(t, app.ListRoles, org.ID, agent.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = chatDo(t, app.ListRoles, org.ID, hasRoles.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	code, _ = chatDo(t, app.ListRoles, org.ID, canPick.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code, "whoever creates users picks a role")

	code, _ = chatDo(t, app.ListTemplates, org.ID, agent.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, _ = chatDo(t, app.ListTemplates, org.ID, hasTemplates.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusOK, code)

	// a person who may hand conversations over, but may not browse the user directory, still gets the picker's list
	code, _ = chatDo(t, app.ListUsers, org.ID, handsOver.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	code, raw := chatDo(t, app.ListTransferAgents, org.ID, handsOver.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, string(raw), agent.ID.String())
	assert.NotContains(t, string(raw), "\"email\"", "only what the picker needs")
	code, _ = chatDo(t, app.ListTransferAgents, org.ID, agent.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
	other := testutil.CreateTestOrganization(t, app.DB)
	outsider := chatUser(t, app, other.ID, []string{"transfers:write"})
	_, raw = chatDo(t, app.ListTransferAgents, other.ID, outsider.ID, nil, nil, nil)
	assert.NotContains(t, string(raw), agent.ID.String(), "never another organization's people")
}

// The Vendas profile works in the CRM, not in the internal protocols.
func TestVendasProfile_HasNoProtocolPermissions(t *testing.T) {
	for _, p := range models.TeamProfiles() {
		has := false
		for _, k := range p.Permissions {
			if k == "occurrences:read" || k == "occurrences:write" {
				has = true
			}
		}
		switch p.Name {
		case "Vendas":
			assert.False(t, has, "sellers never reach the internal protocols")
		case "SAC", "Logística", "Supervisor de Loja", "Supervisor de SAC":
			assert.True(t, has, p.Name+" works with protocols")
		}
	}
}

// A SAC agent hands a customer to the sales team of a store: the picker lists the organization's active teams with
// transfers:write, even though the teams list only returns the caller's own teams. Without it: refused. Other
// organizations never appear.
func TestListTransferTeams_NeedsTransfersWriteAndStaysInTheOrganization(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	sac := chatUser(t, app, org.ID, []string{"chat:read", "chat:write", "transfers:write"})
	reader := chatUser(t, app, org.ID, []string{"chat:read", "transfers:read"})
	createTestTeam(t, app, org.ID, sac.ID)
	vendas := createTestTeam(t, app, org.ID, chatUser(t, app, org.ID, []string{"chat:read"}).ID)
	createTestTeam(t, app, other.ID, chatUser(t, app, other.ID, []string{"chat:read"}).ID)

	code, raw := chatDo(t, app.ListTransferTeams, org.ID, sac.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	d := decodeChat[struct {
		Teams []struct {
			ID uuid.UUID `json:"id"`
		} `json:"teams"`
	}](t, raw)
	assert.Len(t, d.Teams, 2, "own team and the sales team, not the other organization's")
	var ids []uuid.UUID
	for _, tm := range d.Teams {
		ids = append(ids, tm.ID)
	}
	assert.Contains(t, ids, vendas.ID)

	code, _ = chatDo(t, app.ListTransferTeams, org.ID, reader.ID, nil, nil, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)
}
