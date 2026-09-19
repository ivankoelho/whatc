package handlers_test

import (
	"errors"
	"sync"
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

func TestOccurrenceProcesses_SeedsFiveRealProcessesOnFirstRead(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var processes []models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).
		Order("position ASC").Find(&processes).Error)
	require.Len(t, processes, 5)

	names := make([]string, len(processes))
	for i, p := range processes {
		names[i] = p.Name
	}
	assert.Equal(t, []string{
		"Devolução após 24h",
		"Material já separado ou em romaneio",
		"Divergência no ato do recebimento",
		"Avaria — comunicação e abertura",
		"Desistência sem avaria",
	}, names)

	// The avaria process must carry its real restrictions/guidance text, not a
	// placeholder — proof the transcription from the validated HTML landed.
	var avaria models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").
		First(&avaria).Error)
	assert.Contains(t, avaria.Restrictions, "Vamos trocar seu produto.")
	assert.NotNil(t, avaria.ResponseMinutes)
	require.NotNil(t, avaria.WhatHappenedID)
	require.NotNil(t, avaria.CategoryID)
	assert.True(t, avaria.IsActive)

	var whatHappened models.OccurrenceWhatHappened
	require.NoError(t, app.DB.First(&whatHappened, "id = ?", *avaria.WhatHappenedID).Error)
	assert.Equal(t, "Produto com Avaria", whatHappened.Name, "must reuse the existing seeded reason, not create a duplicate")

	for _, name := range []string{"Devolução Após 24h", "Alteração de Pedido em Separação", "Desistência do Pedido"} {
		var n int64
		app.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ? AND name = ?", org.ID, name).Count(&n)
		assert.EqualValues(t, 1, n, "new reason %q must be created", name)
	}

	var orphans int64
	app.DB.Model(&models.OccurrenceProcessMessage{}).Where("process_id = ?", uuid.Nil).Count(&orphans)
	assert.Zero(t, orphans, "no message may reference the nil process")
}

func TestOccurrenceProcesses_SeedIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var count int64
	app.DB.Model(&models.OccurrenceProcess{}).Where("organization_id = ?", org.ID).Count(&count)
	assert.EqualValues(t, 5, count)

	countRows := func(model any) int64 {
		var n int64
		app.DB.Model(model).Where("organization_id = ?", org.ID).Count(&n)
		return n
	}
	assert.EqualValues(t, 6, countRows(&models.OccurrenceProcessMessage{}), "5 registration + 1 documents (Avaria)")

	cats, reasons := countRows(&models.OccurrenceCategory{}), countRows(&models.OccurrenceWhatHappened{})
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))
	assert.Equal(t, cats, countRows(&models.OccurrenceCategory{}), "re-run must not create duplicate categories")
	assert.Equal(t, reasons, countRows(&models.OccurrenceWhatHappened{}), "re-run must not create duplicate reasons")
}

// Concurrent first reads must seed exactly once: no orphan messages pointing
// at the nil process, no partial data.
func TestOccurrenceProcesses_ConcurrentSeedIsAtomic(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = app.EnsureDefaultOccurrenceProcessesForTest(org.ID)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "seed %d failed", i)
	}

	var procs, msgs, orphans int64
	app.DB.Model(&models.OccurrenceProcess{}).Where("organization_id = ?", org.ID).Count(&procs)
	app.DB.Model(&models.OccurrenceProcessMessage{}).Where("organization_id = ?", org.ID).Count(&msgs)
	app.DB.Model(&models.OccurrenceProcessMessage{}).Where("process_id = ?", uuid.Nil).Count(&orphans)
	assert.EqualValues(t, 5, procs)
	assert.EqualValues(t, 6, msgs)
	assert.Zero(t, orphans, "no message may reference the nil process")
}

func TestFindOrCreateWhatHappened_ReusesExistingByName(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID)) // seeds the six defaults, including "Produto com Avaria"

	found, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	var count int64
	app.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ? AND name = ?", org.ID, "Produto com Avaria").Count(&count)
	assert.EqualValues(t, 1, count, "must not create a duplicate row for a name that already exists")
	assert.Equal(t, "Produto com Avaria", found.Name)

	created, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Motivo Novo")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, created.ID)
	app.DB.Model(&models.OccurrenceWhatHappened{}).Where("organization_id = ? AND name = ?", org.ID, "Motivo Novo").Count(&count)
	assert.EqualValues(t, 1, count, "a name that is not present must be created")
}

// GORM replaces a zero-value field carrying a `default:` tag with that default,
// so an explicit IsActive:false must survive Create.
func TestOccurrenceProcess_InactiveIsStoredAsInactive(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Rascunho", IsActive: false}
	require.NoError(t, app.DB.Create(&p).Error)

	var reloaded models.OccurrenceProcess
	require.NoError(t, app.DB.First(&reloaded, "id = ?", p.ID).Error)
	assert.False(t, reloaded.IsActive)

	m := models.OccurrenceProcessMessage{
		OrganizationID: org.ID, ProcessID: p.ID,
		Stage: models.OccurrenceProcessMessageClosing, Content: "x", IsActive: false,
	}
	require.NoError(t, app.DB.Create(&m).Error)
	var reloadedMsg models.OccurrenceProcessMessage
	require.NoError(t, app.DB.First(&reloadedMsg, "id = ?", m.ID).Error)
	assert.False(t, reloadedMsg.IsActive)
}

// DB half of the "one active process per reason" invariant: the partial unique
// index must be created by AutoMigrate and really enforced.
func TestOccurrenceProcess_PartialUniqueIndexOneActivePerReason(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	wh := models.OccurrenceWhatHappened{OrganizationID: org.ID, Name: "Motivo", IsActive: true}
	require.NoError(t, app.DB.Create(&wh).Error)
	reason := wh.ID

	first := models.OccurrenceProcess{OrganizationID: org.ID, Name: "A", WhatHappenedID: &reason, IsActive: true}
	require.NoError(t, app.DB.Create(&first).Error)

	dupActive := models.OccurrenceProcess{OrganizationID: org.ID, Name: "B", WhatHappenedID: &reason, IsActive: true}
	assert.ErrorContains(t, app.DB.Create(&dupActive).Error, "idx_occ_process_what_happened", "second ACTIVE process for the same reason must be rejected")

	inactive := models.OccurrenceProcess{OrganizationID: org.ID, Name: "C", WhatHappenedID: &reason, IsActive: false}
	assert.NoError(t, app.DB.Create(&inactive).Error, "an INACTIVE process for the same reason is allowed")
}

// --- CRUD handlers ---

func processAdmin(t *testing.T, app *handlers.App) (*models.Organization, *models.User) {
	t.Helper()
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	return org, testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
}

func createProcessReq(t *testing.T, orgID, userID uuid.UUID, body map[string]any) *fastglue.Request {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	return req
}

func auditCount(app *handlers.App, orgID uuid.UUID, action models.AuditAction) int64 {
	var n int64
	app.DB.Model(&models.AuditLog{}).
		Where("organization_id = ? AND resource_type = ? AND action = ?", orgID, models.ResourceOccurrenceProcesses, action).
		Count(&n)
	return n
}

func TestCreateOccurrenceProcess_RequiresWritePermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Novo Processo"})
	require.NoError(t, app.CreateOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestUpdateAndDeleteOccurrenceProcess_RequirePermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "P", IsActive: true}
	require.NoError(t, app.DB.Create(&p).Error)

	upd := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "X"})
	testutil.SetPathParam(upd, "id", p.ID.String())
	require.NoError(t, app.UpdateOccurrenceProcess(upd))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(upd))

	del := testutil.NewGETRequest(t)
	testutil.SetAuthContext(del, org.ID, user.ID)
	testutil.SetPathParam(del, "id", p.ID.String())
	require.NoError(t, app.DeleteOccurrenceProcess(del))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(del))
}

func TestCreateOccurrenceProcess_WritesAuditLog(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)

	req := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Processo Manual", "is_active": true})
	require.NoError(t, app.CreateOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	require.Eventually(t, func() bool { return auditCount(app, org.ID, models.AuditActionCreated) == 1 },
		time.Second, 10*time.Millisecond)
}

func TestCreateOccurrenceProcess_RejectsSecondActiveProcessForSameWhatHappened(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	reason, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	first := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Processo A", "what_happened_id": reason.ID.String(), "is_active": true})
	require.NoError(t, app.CreateOccurrenceProcess(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	second := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Processo B", "what_happened_id": reason.ID.String(), "is_active": true})
	require.NoError(t, app.CreateOccurrenceProcess(second))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(second),
		"only one ACTIVE process may exist per reason — ResolveOccurrenceProcess's .First() depends on this")
}

func TestCreateOccurrenceProcess_AllowsSecondInactiveProcessForSameWhatHappened(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	reason, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)

	first := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Processo A", "what_happened_id": reason.ID.String(), "is_active": true})
	require.NoError(t, app.CreateOccurrenceProcess(first))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(first))

	second := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Processo B (rascunho)", "what_happened_id": reason.ID.String(), "is_active": false})
	require.NoError(t, app.CreateOccurrenceProcess(second))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(second), "an inactive draft must not collide with the active one")

	var stored models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Processo B (rascunho)").First(&stored).Error)
	assert.False(t, stored.IsActive)
}

func TestCreateOccurrenceProcess_RejectsForeignOrgReason(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	foreign, err := app.FindOrCreateWhatHappenedForTest(otherOrg.ID, "Produto com Avaria")
	require.NoError(t, err)

	req := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "X", "what_happened_id": foreign.ID.String()})
	require.NoError(t, app.CreateOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestUpdateOccurrenceProcess_UpdatesAndAudits(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Antigo", IsActive: true, Guidance: "g"}
	require.NoError(t, app.DB.Create(&p).Error)

	req := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "Novo", "is_active": false, "guidance": ""})
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.UpdateOccurrenceProcess(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp models.OccurrenceProcess
	testutil.ParseEnvelopeResponse(t, req, &resp)
	assert.Equal(t, "Novo", resp.Name, "response must show the new values")
	assert.False(t, resp.IsActive)

	var stored models.OccurrenceProcess
	require.NoError(t, app.DB.First(&stored, "id = ?", p.ID).Error)
	assert.Equal(t, "Novo", stored.Name)
	assert.False(t, stored.IsActive, "false must be written, not skipped")
	assert.Empty(t, stored.Guidance)

	require.Eventually(t, func() bool { return auditCount(app, org.ID, models.AuditActionUpdated) == 1 },
		time.Second, 10*time.Millisecond)
	var entry models.AuditLog
	require.NoError(t, app.DB.Where("organization_id = ? AND action = ?", org.ID, models.AuditActionUpdated).First(&entry).Error)
	assert.NotEmpty(t, entry.Changes, "audit diff must show before/after")
}

func TestUpdateOccurrenceProcess_RejectsActivatingSecondProcessForReason(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	reason, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)
	active := models.OccurrenceProcess{OrganizationID: org.ID, Name: "A", WhatHappenedID: &reason.ID, IsActive: true}
	draft := models.OccurrenceProcess{OrganizationID: org.ID, Name: "B", WhatHappenedID: &reason.ID, IsActive: false}
	require.NoError(t, app.DB.Create(&active).Error)
	require.NoError(t, app.DB.Create(&draft).Error)

	req := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "B", "what_happened_id": reason.ID.String(), "is_active": true})
	testutil.SetPathParam(req, "id", draft.ID.String())
	require.NoError(t, app.UpdateOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))

	// A process may keep its own reason: the active one updates itself fine.
	self := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "A renomeado", "what_happened_id": reason.ID.String(), "is_active": true})
	testutil.SetPathParam(self, "id", active.ID.String())
	require.NoError(t, app.UpdateOccurrenceProcess(self))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(self))
}

func TestListOccurrenceProcesses_SeedsOnFirstRead(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListOccurrenceProcesses(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var resp struct {
		Processes []models.OccurrenceProcess `json:"processes"`
	}
	testutil.ParseEnvelopeResponse(t, req, &resp)
	assert.Len(t, resp.Processes, 5)
}

func TestListOccurrenceProcesses_RequiresReadPermission(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.ListOccurrenceProcesses(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
}

func TestDeleteOccurrenceProcess_SoftDeletesAndAudits(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Descartável", IsActive: true}
	require.NoError(t, app.DB.Create(&p).Error)
	msg := models.OccurrenceProcessMessage{
		OrganizationID: org.ID, ProcessID: p.ID, Stage: models.OccurrenceProcessMessageRegistration, Content: "x", IsActive: true,
	}
	require.NoError(t, app.DB.Create(&msg).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.DeleteOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var visible int64
	app.DB.Model(&models.OccurrenceProcess{}).Where("id = ?", p.ID).Count(&visible)
	assert.Zero(t, visible)
	var raw int64
	app.DB.Unscoped().Model(&models.OccurrenceProcess{}).Where("id = ? AND deleted_at IS NOT NULL", p.ID).Count(&raw)
	assert.EqualValues(t, 1, raw, "row must be soft-deleted, not removed")

	var liveMsgs, deletedMsgs int64
	app.DB.Model(&models.OccurrenceProcessMessage{}).Where("process_id = ?", p.ID).Count(&liveMsgs)
	app.DB.Unscoped().Model(&models.OccurrenceProcessMessage{}).Where("process_id = ? AND deleted_at IS NOT NULL", p.ID).Count(&deletedMsgs)
	assert.Zero(t, liveMsgs, "process messages must not be orphaned live")
	assert.EqualValues(t, 1, deletedMsgs, "process messages must be soft-deleted with the process")

	require.Eventually(t, func() bool { return auditCount(app, org.ID, models.AuditActionDeleted) == 1 },
		time.Second, 10*time.Millisecond)
}

func TestDeleteOccurrenceProcess_RefusesWhenOccurrenceUsesIt(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	process := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Em Uso", IsActive: true}
	require.NoError(t, app.DB.Create(&process).Error)
	stage, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{
		OrganizationID: org.ID, ContactID: contact.ID, Title: "x", StageID: stage.ID,
		OpenedByUserID: user.ID, ProcessID: &process.ID,
	}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", process.ID.String())
	require.NoError(t, app.DeleteOccurrenceProcess(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req))
}

// The pre-check can lose a race; the partial unique index is the backstop and
// must surface as the same 409, never a 500.
func TestIsActiveProcessConflict_MapsIndexViolationOnly(t *testing.T) {
	app := newTestApp(t)
	org, _ := processAdmin(t, app)
	reason, err := app.FindOrCreateWhatHappenedForTest(org.ID, "Produto com Avaria")
	require.NoError(t, err)
	require.NoError(t, app.DB.Create(&models.OccurrenceProcess{OrganizationID: org.ID, Name: "A", WhatHappenedID: &reason.ID, IsActive: true}).Error)

	dupErr := app.DB.Create(&models.OccurrenceProcess{OrganizationID: org.ID, Name: "B", WhatHappenedID: &reason.ID, IsActive: true}).Error
	require.Error(t, dupErr)
	assert.True(t, handlers.IsActiveProcessConflictForTest(dupErr))
	assert.False(t, handlers.IsActiveProcessConflictForTest(errors.New("boom")))
	assert.False(t, handlers.IsActiveProcessConflictForTest(nil))
}

// The audit diff must see edits to JSONB list fields even when the list length
// is unchanged and nothing else changed.
func TestUpdateOccurrenceProcess_AuditsRequiredFieldsOnlyChange(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	p := models.OccurrenceProcess{
		OrganizationID: org.ID, Name: "P", IsActive: true,
		RequiredFields:    models.JSONBArray{"invoice_number"},
		EvidenceChecklist: models.JSONBArray{"Nota fiscal"},
	}
	require.NoError(t, app.DB.Create(&p).Error)

	req := createProcessReq(t, org.ID, user.ID, map[string]any{
		"name": "P", "is_active": true,
		"required_fields":    []string{"product_description"},
		"evidence_checklist": []string{"Nota fiscal"},
	})
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.UpdateOccurrenceProcess(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	require.Eventually(t, func() bool { return auditCount(app, org.ID, models.AuditActionUpdated) == 1 },
		time.Second, 10*time.Millisecond, "a required_fields-only edit must still be audited")
	var entry models.AuditLog
	require.NoError(t, app.DB.Where("organization_id = ? AND action = ?", org.ID, models.AuditActionUpdated).First(&entry).Error)
	var found bool
	for _, c := range entry.Changes {
		m, ok := c.(map[string]any)
		if ok && m["field"] == "required_fields" {
			found = true
			assert.NotEqual(t, m["old_value"], m["new_value"])
		}
	}
	assert.True(t, found, "changes must include a required_fields entry, got %v", entry.Changes)
}

func TestUpdateOccurrenceProcess_ExplicitNullClearsOptionalFields(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	cat := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Cat", IsActive: true}
	require.NoError(t, app.DB.Create(&cat).Error)
	dept := models.Department{OrganizationID: org.ID, Name: "Logística", Active: true}
	require.NoError(t, app.DB.Create(&dept).Error)
	resp, res := 60, 120
	p := models.OccurrenceProcess{
		OrganizationID: org.ID, Name: "P", IsActive: true,
		CategoryID: &cat.ID, DepartmentID: &dept.ID, ResponseMinutes: &resp, ResolutionMinutes: &res,
	}
	require.NoError(t, app.DB.Create(&p).Error)

	req := createProcessReq(t, org.ID, user.ID, map[string]any{
		"name": "P", "category_id": nil, "department_id": nil, "response_minutes": nil, "resolution_minutes": nil,
	})
	testutil.SetPathParam(req, "id", p.ID.String())
	require.NoError(t, app.UpdateOccurrenceProcess(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.OccurrenceProcess
	require.NoError(t, app.DB.First(&stored, "id = ?", p.ID).Error)
	assert.Nil(t, stored.CategoryID)
	assert.Nil(t, stored.DepartmentID)
	assert.Nil(t, stored.ResponseMinutes)
	assert.Nil(t, stored.ResolutionMinutes)
}

func TestOccurrenceProcess_RejectsForeignOrgAndSoftDeletedRefs(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	other := testutil.CreateTestOrganization(t, app.DB)

	foreignCat := models.OccurrenceCategory{OrganizationID: other.ID, Name: "Cat", IsActive: true}
	require.NoError(t, app.DB.Create(&foreignCat).Error)
	foreignReason, err := app.FindOrCreateWhatHappenedForTest(other.ID, "Produto com Avaria")
	require.NoError(t, err)
	foreignDept := models.Department{OrganizationID: other.ID, Name: "Outro", Active: true}
	require.NoError(t, app.DB.Create(&foreignDept).Error)

	deletedCat := models.OccurrenceCategory{OrganizationID: org.ID, Name: "Removida", IsActive: true}
	require.NoError(t, app.DB.Create(&deletedCat).Error)
	require.NoError(t, app.DB.Delete(&deletedCat).Error)

	existing := models.OccurrenceProcess{OrganizationID: org.ID, Name: "E", IsActive: true}
	require.NoError(t, app.DB.Create(&existing).Error)

	for field, id := range map[string]string{
		"category_id":      foreignCat.ID.String(),
		"what_happened_id": foreignReason.ID.String(),
		"department_id":    foreignDept.ID.String(),
	} {
		create := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "X", field: id})
		require.NoError(t, app.CreateOccurrenceProcess(create))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(create), "create must reject foreign %s", field)

		update := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "X", field: id})
		testutil.SetPathParam(update, "id", existing.ID.String())
		require.NoError(t, app.UpdateOccurrenceProcess(update))
		assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(update), "update must reject foreign %s", field)
	}

	create := createProcessReq(t, org.ID, user.ID, map[string]any{"name": "X", "category_id": deletedCat.ID.String()})
	require.NoError(t, app.CreateOccurrenceProcess(create))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(create), "soft-deleted category must be rejected")
}

// PR2's frontend calls .includes/.length on these, so null would crash it.
func TestOccurrenceProcess_OmittedJSONBArraysAreEmptyNotNull(t *testing.T) {
	app := newTestApp(t)
	org, user := processAdmin(t, app)
	require.NoError(t, app.EnsureDefaultWhatHappenedForTest(org.ID))
	var reason models.OccurrenceWhatHappened
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Duvida sobre o Produto").First(&reason).Error)

	const wantEmpty1, wantEmpty2 = `"required_fields":[]`, `"evidence_checklist":[]`
	assertEmpty := func(name, body string) {
		t.Helper()
		assert.Contains(t, body, wantEmpty1, name)
		assert.Contains(t, body, wantEmpty2, name)
	}

	// Create via the handler with both arrays omitted.
	req := createProcessReq(t, org.ID, user.ID, map[string]any{
		"name": "Sem listas", "is_active": true, "what_happened_id": reason.ID.String(),
	})
	require.NoError(t, app.CreateOccurrenceProcess(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assertEmpty("create response", string(testutil.GetResponseBody(req)))

	// DB row: jsonb '[]', not SQL NULL.
	var stored struct{ Evidence, Required string }
	require.NoError(t, app.DB.Raw(`SELECT evidence_checklist::text AS evidence, required_fields::text AS required
		FROM occurrence_processes WHERE organization_id = ? AND name = ?`, org.ID, "Sem listas").Scan(&stored).Error)
	assert.Equal(t, "[]", stored.Evidence)
	assert.Equal(t, "[]", stored.Required)

	// A struct created with nil slices (the GORM default path) is also '[]'.
	nilProc := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Nil direto", IsActive: false}
	require.NoError(t, app.DB.Create(&nilProc).Error)
	require.NoError(t, app.DB.Raw(`SELECT evidence_checklist::text AS evidence, required_fields::text AS required
		FROM occurrence_processes WHERE id = ?`, nilProc.ID).Scan(&stored).Error)
	assert.Equal(t, "[]", stored.Evidence)
	assert.Equal(t, "[]", stored.Required)

	// The column itself refuses an explicit NULL.
	err := app.DB.Exec(`UPDATE occurrence_processes SET required_fields = NULL WHERE id = ?`, nilProc.ID).Error
	assert.Error(t, err, "required_fields must be NOT NULL")
	err = app.DB.Exec(`UPDATE occurrence_processes SET evidence_checklist = NULL WHERE id = ?`, nilProc.ID).Error
	assert.Error(t, err, "evidence_checklist must be NOT NULL")

	// List response.
	list := testutil.NewGETRequest(t)
	testutil.SetAuthContext(list, org.ID, user.ID)
	require.NoError(t, app.ListOccurrenceProcesses(list))
	assertEmpty("list response", string(testutil.GetResponseBody(list)))

	// Resolve response.
	res := testutil.NewGETRequest(t)
	testutil.SetAuthContext(res, org.ID, user.ID)
	testutil.SetQueryParam(res, "what_happened_id", reason.ID.String())
	require.NoError(t, app.ResolveOccurrenceProcess(res))
	assert.Contains(t, string(testutil.GetResponseBody(res)), "Sem listas")
	assertEmpty("resolve response", string(testutil.GetResponseBody(res)))
}
