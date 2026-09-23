package handlers_test

import (
	"encoding/json"
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

func minutesPtr(i int) *int { return &i }

// createOccurrenceWith posts CreateOccurrence with the given extra body
// fields and returns the request (for status/body assertions).
func createOccurrenceWith(t *testing.T, app *handlers.App, orgID, userID, contactID uuid.UUID, extra map[string]any) *fastglue.Request {
	t.Helper()
	body := map[string]any{"contact_id": contactID.String(), "description": "x"}
	for k, v := range extra {
		body[k] = v
	}
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.CreateOccurrence(req))
	return req
}

func TestCreateOccurrence_WithProcessID_OverridesSLAAndIsReturnedInResponse(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.EnsureDefaultOccurrenceProcessesForTest(org.ID))

	var process models.OccurrenceProcess
	require.NoError(t, app.DB.Where("organization_id = ? AND name = ?", org.ID, "Avaria — comunicação e abertura").First(&process).Error)
	require.NotNil(t, process.ResponseMinutes)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"process_id": process.ID.String()})
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("organization_id = ? AND contact_id = ?", org.ID, contact.ID).First(&occ).Error)
	require.NotNil(t, occ.ProcessID)
	assert.Equal(t, process.ID, *occ.ProcessID)
	require.NotNil(t, occ.SLA.ResponseDeadline)

	expected := time.Now().Add(time.Duration(*process.ResponseMinutes) * time.Minute)
	assert.WithinDuration(t, expected, *occ.SLA.ResponseDeadline, time.Minute,
		"the process's own ResponseMinutes must win over the priority-based default policy")

	var resp struct {
		Data handlers.OccurrenceResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.NotNil(t, resp.Data.ProcessID, "process_id must reach the API response, not just the DB row")
	assert.Equal(t, process.ID, *resp.Data.ProcessID)
	assert.Equal(t, process.Name, resp.Data.ProcessName)
}

func TestCreateOccurrence_ProcessFromOtherOrg_Is404AndCreatesNothing(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	other := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	foreign := models.OccurrenceProcess{OrganizationID: other.ID, Name: "Alheio", IsActive: true}
	require.NoError(t, app.DB.Create(&foreign).Error)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"process_id": foreign.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))

	var n int64
	app.DB.Model(&models.Occurrence{}).Where("contact_id = ?", contact.ID).Count(&n)
	assert.Zero(t, n)
}

func TestCreateOccurrence_InvalidProcessID_Is400(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"process_id": "not-a-uuid"})
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestCreateOccurrence_InactiveProcess_Is400AndCreatesNothing(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Inativo", IsActive: false}
	require.NoError(t, app.DB.Create(&p).Error)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"process_id": p.ID.String()})
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	var n int64
	app.DB.Model(&models.Occurrence{}).Where("contact_id = ?", contact.ID).Count(&n)
	assert.Zero(t, n)
}

func TestCreateOccurrence_ProcessWithoutMinutes_FallsBackToPriorityPolicy(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Sem SLA", IsActive: true}
	require.NoError(t, app.DB.Create(&p).Error)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"process_id": p.ID.String(), "priority": "urgent"})
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	require.NotNil(t, occ.ProcessID)
	require.NotNil(t, occ.SLA.ResponseDeadline)
	// Default urgent policy: 30min response, 4h resolution.
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), *occ.SLA.ResponseDeadline, 5*time.Second)
	assert.WithinDuration(t, time.Now().Add(4*time.Hour), *occ.SLA.ResolutionDeadline, 5*time.Second)
}

func TestCreateOccurrence_WithoutProcessID_UsesPriorityPolicy(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	req := createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"priority": "urgent"})
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	assert.Nil(t, occ.ProcessID)
	require.NotNil(t, occ.SLA.ResponseDeadline)
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), *occ.SLA.ResponseDeadline, 5*time.Second)
	assert.WithinDuration(t, time.Now().Add(4*time.Hour), *occ.SLA.ResolutionDeadline, 5*time.Second)

	var resp struct {
		Data handlers.OccurrenceResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Nil(t, resp.Data.ProcessID)
	assert.Empty(t, resp.Data.ProcessName)
}

func TestUpdateOccurrence_PriorityChange_KeepsProcessSLA(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	withContact := testutil.CreateTestContact(t, app.DB, org.ID)
	withoutContact := testutil.CreateTestContact(t, app.DB, org.ID)
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Com SLA", IsActive: true,
		ResponseMinutes: minutesPtr(7), ResolutionMinutes: minutesPtr(77)}
	require.NoError(t, app.DB.Create(&p).Error)

	createReq := createOccurrenceWith(t, app, org.ID, user.ID, withContact.ID, map[string]any{"process_id": p.ID.String(), "priority": "low"})
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(createReq))
	createOccurrenceWith(t, app, org.ID, user.ID, withoutContact.ID, map[string]any{"priority": "low"})

	update := func(contactID uuid.UUID) models.Occurrence {
		var occ models.Occurrence
		require.NoError(t, app.DB.Where("contact_id = ?", contactID).First(&occ).Error)
		req := testutil.NewJSONRequest(t, map[string]any{"title": occ.Title, "priority": "urgent"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", occ.ID.String())
		require.NoError(t, app.UpdateOccurrence(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		require.NoError(t, app.DB.First(&occ, "id = ?", occ.ID).Error)
		return occ
	}

	with := update(withContact.ID)
	require.NotNil(t, with.SLA.ResponseDeadline)
	assert.WithinDuration(t, time.Now().Add(7*time.Minute), *with.SLA.ResponseDeadline, 5*time.Second)
	assert.WithinDuration(t, time.Now().Add(77*time.Minute), *with.SLA.ResolutionDeadline, 5*time.Second)

	without := update(withoutContact.ID)
	require.NotNil(t, without.SLA.ResponseDeadline)
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), *without.SLA.ResponseDeadline, 5*time.Second)
}

func TestGetOccurrence_ReturnsProcessIDAndName(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	p := models.OccurrenceProcess{OrganizationID: org.ID, Name: "Processo X", IsActive: true}
	require.NoError(t, app.DB.Create(&p).Error)
	createOccurrenceWith(t, app, org.ID, user.ID, contact.ID, map[string]any{"process_id": p.ID.String()})

	var occ models.Occurrence
	require.NoError(t, app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)

	getReq := testutil.NewGETRequest(t)
	testutil.SetAuthContext(getReq, org.ID, user.ID)
	testutil.SetPathParam(getReq, "id", occ.ID.String())
	require.NoError(t, app.GetOccurrence(getReq))

	var resp struct {
		Data handlers.OccurrenceResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(getReq), &resp))
	require.NotNil(t, resp.Data.ProcessID)
	assert.Equal(t, p.ID, *resp.Data.ProcessID)
	assert.Equal(t, "Processo X", resp.Data.ProcessName)
}
