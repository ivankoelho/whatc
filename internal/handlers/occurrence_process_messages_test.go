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
	"github.com/zerodha/fastglue"
)

const avariaProcessName = "Avaria — comunicação e abertura"

// seededAdmin returns an org and an admin user, with the default processes seeded.
func seededAdmin(t *testing.T, app *handlers.App) (*models.Organization, *models.User) {
	t.Helper()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))
	return org, user
}

func processByName(t *testing.T, app *handlers.App, orgID uuid.UUID, name string) models.OccurrenceProcess {
	t.Helper()
	var p models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", orgID, name).First(&p).Error)
	return p
}

func occurrenceForProcess(t *testing.T, app *handlers.App, org *models.Organization, user *models.User, processID *uuid.UUID) models.Occurrence {
	t.Helper()
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: processID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))
	return occ
}

func previewReq(t *testing.T, orgID, userID uuid.UUID, processID any, stage string, occurrenceID any) *fastglue.Request {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", processID)
	testutil.SetPathParam(req, "stage", stage)
	if occurrenceID != nil {
		testutil.SetQueryParam(req, "occurrence_id", occurrenceID)
	}
	return req
}

func TestResolveOccurrenceProcess_FindsProcessByWhatHappened(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)

	var avaria models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Produto com Avaria").First(&avaria).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "what_happened_id", avaria.ID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), avariaProcessName)
}

func TestResolveOccurrenceProcess_NoMatchReturnsNullProcess(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID))

	var unrelated models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Duvida sobre o Produto").First(&unrelated).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "what_happened_id", unrelated.ID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "no process for this reason is not an error — the form falls back to the generic path")
	assert.Contains(t, string(testutil.GetResponseBody(req)), `"process":null`)
}

func TestResolveOccurrenceProcess_IgnoresInactiveProcess(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	avaria := processByName(t, app, org.ID, avariaProcessName)
	require.NoError(t, app.DB.Model(&avaria).Update("is_active", false).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "what_happened_id", avaria.WhatHappenedID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), `"process":null`)
}

func TestResolveOccurrenceProcess_RequiresValidWhatHappenedID(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)

	for name, val := range map[string]any{"missing": nil, "invalid": "not-a-uuid"} {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		if val != nil {
			testutil.SetQueryParam(req, "what_happened_id", val)
		}
		require.NoError(t, app.ResolveOccurrenceProcess(req), name)
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req), name)
	}
}

func TestResolveProcessMessageVariables_SubstitutesRealVariables(t *testing.T) {
	occ := &models.Occurrence{
		ProtocolNumber: "202600123",
		InvoiceNumber:  "NF-9988",
		Contact:        &models.Contact{ProfileName: "João"},
	}
	out := handlers.ResolveProcessMessageVariablesForTest("Olá [Nome], protocolo [Protocolo], NF [NF], atendente [Atendente].", occ, "Maria")
	assert.Equal(t, "Olá João, protocolo 202600123, NF NF-9988, atendente Maria.", out)
}

func TestResolveProcessMessageVariables_PrazoAndLoja(t *testing.T) {
	deadline := time.Now().Add(3*24*time.Hour + time.Hour)
	occ := &models.Occurrence{Unit: &models.Unit{Name: "Loja Centro"}}
	occ.SLA.ResponseDeadline = &deadline
	out := handlers.ResolveProcessMessageVariablesForTest("[Loja]: [Prazo]", occ, "")
	assert.Equal(t, "Loja Centro: 3 dias", out)
}

func TestFormatProcessDeadline(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "o mais breve possível"},
		{"negative", -5 * time.Hour, "o mais breve possível"},
		{"30min", 30 * time.Minute, "1 hora"},
		{"90min", 90 * time.Minute, "2 horas"},
		{"23h", 23 * time.Hour, "23 horas"},
		{"24h", 24 * time.Hour, "1 dia"},
		{"25h", 25 * time.Hour, "1 dia"},
		{"49h", 49 * time.Hour, "2 dias"},
		{"5d", 5 * 24 * time.Hour, "5 dias"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, handlers.FormatProcessDeadlineForTest(c.in), c.name)
	}
}

func TestResolveProcessMessageVariables_NilAndOverdueDeadline(t *testing.T) {
	assert.Equal(t, "prazo: .", handlers.ResolveProcessMessageVariablesForTest("prazo: [Prazo].", &models.Occurrence{}, ""))

	past := time.Now().Add(-2 * time.Hour)
	occ := &models.Occurrence{}
	occ.SLA.ResponseDeadline = &past
	assert.Equal(t, "em o mais breve possível", handlers.ResolveProcessMessageVariablesForTest("em [Prazo]", occ, ""))
}

func TestResolveProcessMessageVariables_DoesNotDoubleSubstitute(t *testing.T) {
	occ := &models.Occurrence{ProtocolNumber: "123", Contact: &models.Contact{ProfileName: "[Protocolo]"}}
	out := handlers.ResolveProcessMessageVariablesForTest("Olá [Nome], protocolo [Protocolo]", occ, "")
	assert.Equal(t, "Olá [Protocolo], protocolo 123", out)
}

func TestPreviewOccurrenceProcessMessage_SoftDeletedContactReturns404(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, user, &process.ID)
	require.NoError(t, app.DB.Delete(&models.Contact{}, "id = ?", occ.ContactID).Error)

	req := previewReq(t, org.ID, user.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestPreviewOccurrenceProcessMessage_CrossOrgOccurrenceReturns404(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	otherOrg, otherUser := seededAdmin(t, app)
	otherProcess := processByName(t, app, otherOrg.ID, avariaProcessName)
	otherOcc := occurrenceForProcess(t, app, otherOrg, otherUser, &otherProcess.ID)
	process := processByName(t, app, org.ID, avariaProcessName)

	req := previewReq(t, org.ID, user.ID, process.ID.String(), "registration", otherOcc.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestPreviewOccurrenceProcessMessage_InactiveTemplateFallsBack(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, user, &process.ID)
	require.NoError(t, app.DB.Model(&models.OccurrenceProcessMessage{}).
		Where("process_id = ?", process.ID).Update("is_active", false).Error)

	req := previewReq(t, org.ID, user.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, "Guarde este número para consultas futuras", "registration falls back to legacy text")
	assert.Contains(t, body, `"has_template":true`)

	req = previewReq(t, org.ID, user.ID, process.ID.String(), "documents", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	body = string(testutil.GetResponseBody(req))
	assert.Contains(t, body, `"has_template":false`)
	assert.Contains(t, body, `"content":""`)
}

func TestLogOccurrenceProcessMessageUse_ProcessNameComesFromOccurrenceNotClient(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)
	withProcess := occurrenceForProcess(t, app, org, user, &process.ID)
	without := occurrenceForProcess(t, app, org, user, nil)

	eventContent := func(occ models.Occurrence) string {
		req := testutil.NewJSONRequest(t, map[string]any{"stage": "registration", "process_name": "SPOOFED"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", occ.ID.String())
		require.NoError(t, app.LogOccurrenceProcessMessageUse(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var ev models.OccurrenceEvent
		require.NoError(t, app.DB.Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventProcessMessageUsed).First(&ev).Error)
		return ev.Content
	}

	got := eventContent(withProcess)
	assert.Contains(t, got, avariaProcessName)
	assert.NotContains(t, got, "SPOOFED")

	got = eventContent(without)
	assert.NotContains(t, got, "SPOOFED")
	assert.NotContains(t, got, "processo:")
}

func TestPreviewOccurrenceProcessMessage_UsesTheOccurrenceFromTheQueryParam_NotThePathParam(t *testing.T) {
	// Regression test: the route's {id} is the PROCESS id, never an occurrence id.
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, user, &process.ID)

	req := previewReq(t, org.ID, user.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req),
		"must resolve the occurrence from occurrence_id, never from the {id} path param")

	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, `"has_template":true`)
	assert.Contains(t, body, occ.ProtocolNumber, "the real seeded template must have been found and its variables substituted")
	assert.NotContains(t, body, "[Protocolo]")
}

func TestPreviewOccurrenceProcessMessage_RejectsOccurrenceLinkedToADifferentProcess(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	avaria := processByName(t, app, org.ID, avariaProcessName)
	divergencia := processByName(t, app, org.ID, "Divergência no ato do recebimento")
	occ := occurrenceForProcess(t, app, org, user, &divergencia.ID)

	req := previewReq(t, org.ID, user.ID, avaria.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestPreviewOccurrenceProcessMessage_RejectsOccurrenceWithNoProcess(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	avaria := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, user, nil)

	req := previewReq(t, org.ID, user.ID, avaria.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestPreviewOccurrenceProcessMessage_ForbiddenWhenUserCannotViewConversation(t *testing.T) {
	app := newTestApp(t)
	org, owner := seededAdmin(t, app)
	outsiderRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "preview-outsider",
		[]string{"chat:read", "chat:write", "occurrences:read", "occurrences:write"})
	outsider := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&outsiderRole.ID))
	enableStrictVisibility(t, app, org.ID)

	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, owner, &process.ID)
	require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", occ.ContactID).
		Update("assigned_user_id", owner.ID).Error)

	req := previewReq(t, org.ID, outsider.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestPreviewOccurrenceProcessMessage_InvalidStageAndOccurrenceID(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, user, &process.ID)

	req := previewReq(t, org.ID, user.ID, process.ID.String(), "bogus", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req), "invalid stage")

	req = previewReq(t, org.ID, user.ID, process.ID.String(), "registration", nil)
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req), "missing occurrence_id")
}

func TestPreviewOccurrenceProcessMessage_NoTemplateReturnsHasTemplateFalse_NotAGeneratedMessage(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, user, &process.ID)

	// "closing" has no seeded template for this process.
	req := previewReq(t, org.ID, user.ID, process.ID.String(), "closing", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, `"has_template":false`)
	assert.Contains(t, body, `"content":""`, "must not silently generate a customer-facing message nobody wrote")
}

func TestPreviewOccurrenceProcessMessage_RegistrationFallsBackToLegacyProtocolText(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	process := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Sem template", IsActive: true}
	require.NoError(t, app.DB.Create(&process).Error)
	occ := occurrenceForProcess(t, app, org, user, &process.ID)

	req := previewReq(t, org.ID, user.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, occ.ProtocolNumber, "registration must always fall back to the legacy protocol text, matching current production behaviour")
	assert.Contains(t, body, `"has_template":true`)
}

func TestLogOccurrenceProcessMessageUse_CreatesTimelineEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	occ := occurrenceForProcess(t, app, org, user, nil)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "registration"})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, app.LogOccurrenceProcessMessageUse(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var count int64
	app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ? AND type = ?", occ.ID, models.OccurrenceEventProcessMessageUsed).Count(&count)
	assert.EqualValues(t, 1, count, "must reuse the existing occurrence_events timeline, not a new table")

	bad := testutil.NewJSONRequest(t, map[string]any{"stage": "bogus"})
	testutil.SetAuthContext(bad, org.ID, user.ID)
	testutil.SetPathParam(bad, "id", occ.ID.String())
	require.NoError(t, app.LogOccurrenceProcessMessageUse(bad))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(bad))
}

func upsertReq(t *testing.T, orgID, userID, processID uuid.UUID, stage string, body map[string]any) *fastglue.Request {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", processID.String())
	testutil.SetPathParam(req, "stage", stage)
	return req
}

func TestUpsertOccurrenceProcessMessage_CreatesThenUpdatesAndAudits(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)

	req := upsertReq(t, org.ID, user.ID, process.ID, "closing", map[string]any{"content": "Encerrado, [Nome]."})
	require.NoError(t, app.UpsertOccurrenceProcessMessage(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	// Second call on the same stage must update (and be able to write false).
	req = upsertReq(t, org.ID, user.ID, process.ID, "closing", map[string]any{"content": "Novo texto", "is_active": false})
	require.NoError(t, app.UpsertOccurrenceProcessMessage(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var rows []models.OccurrenceProcessMessage
	require.NoError(t, app.DB.Where("process_id = ? AND stage = ?", process.ID, "closing").Find(&rows).Error)
	require.Len(t, rows, 1, "second upsert must update, not insert a duplicate")
	assert.Equal(t, "Novo texto", rows[0].Content)
	assert.False(t, rows[0].IsActive, "is_active=false must actually be written")

	require.Eventually(t, func() bool { return auditCount(app, org.ID, models.AuditActionCreated) == 1 },
		2*time.Second, 20*time.Millisecond, "created audit row")
	require.Eventually(t, func() bool { return auditCount(app, org.ID, models.AuditActionUpdated) == 1 },
		2*time.Second, 20*time.Millisecond, "updated audit row")
}

func TestUpsertOccurrenceProcessMessage_ValidationAndPermission(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)

	req := upsertReq(t, org.ID, user.ID, process.ID, "bogus", map[string]any{"content": "x"})
	require.NoError(t, app.UpsertOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req), "invalid stage")

	req = upsertReq(t, org.ID, user.ID, process.ID, "closing", map[string]any{"content": ""})
	require.NoError(t, app.UpsertOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req), "empty content")

	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	req = upsertReq(t, org.ID, agent.ID, process.ID, "closing", map[string]any{"content": "x"})
	require.NoError(t, app.UpsertOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req), "agent lacks occurrences.processes:write")
}

func TestListOccurrenceProcessMessages_RequiresReadAndReturnsSeededMessages(t *testing.T) {
	app := newTestApp(t)
	org, user := seededAdmin(t, app)
	process := processByName(t, app, org.ID, avariaProcessName)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", process.ID.String())
	require.NoError(t, app.ListOccurrenceProcessMessages(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Contains(t, string(testutil.GetResponseBody(req)), `"stage":"registration"`)

	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetPathParam(req, "id", process.ID.String())
	require.NoError(t, app.ListOccurrenceProcessMessages(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestResolveOccurrenceProcess_AgentRoleSeedsOnFirstCall(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID) // occurrences:read only, no occurrences.processes:*
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID))

	var avaria models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Produto com Avaria").First(&avaria).Error)

	// Fresh org: nothing has called ListOccurrenceProcesses (the agent can't).
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	testutil.SetQueryParam(req, "what_happened_id", avaria.ID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	body := string(testutil.GetResponseBody(req))
	assert.Contains(t, body, avariaProcessName, "an agent must resolve the seeded process on the very first call")
	assert.NotContains(t, body, `"process":null`)
}

func TestPreviewOccurrenceProcessMessage_AssigneeAllowedOutsideConversationScope(t *testing.T) {
	app := newTestApp(t)
	org, owner := seededAdmin(t, app)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "preview-assignee",
		[]string{"chat:read", "chat:write", "occurrences:read", "occurrences:write"})
	assignee := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	outsider := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	enableStrictVisibility(t, app, org.ID)

	process := processByName(t, app, org.ID, avariaProcessName)
	occ := occurrenceForProcess(t, app, org, owner, &process.ID)
	// The contact belongs to the owner, so neither user can view the conversation...
	require.NoError(t, app.DB.Model(&models.Contact{}).Where("id = ?", occ.ContactID).
		Update("assigned_user_id", owner.ID).Error)
	// ...but the occurrence itself is assigned to `assignee`.
	require.NoError(t, app.DB.Model(&models.Occurrence{}).Where("id = ?", occ.ID).
		Update("assigned_user_id", assignee.ID).Error)

	req := previewReq(t, org.ID, assignee.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "the occurrence's assignee keeps access, like loadAuthorizedOccurrence")

	req = previewReq(t, org.ID, outsider.ID, process.ID.String(), "registration", occ.ID.String())
	require.NoError(t, app.PreviewOccurrenceProcessMessage(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req), "a non-assignee outsider is still refused")
}
