package handlers_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func orderedStageIDs(t *testing.T, orgID uuid.UUID, app *handlers.App) []string {
	t.Helper()
	var rows []models.OccurrenceStage
	require.NoError(t, app.DB.Where("organization_id = ?", orgID).Order("position ASC").Find(&rows).Error)
	out := make([]string, len(rows))
	for i, s := range rows {
		out[i] = s.ID.String()
	}
	return out
}

func TestOccurrenceStages_Reorder(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	rw := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "stages-w", []string{"occurrences.stages:write"})
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&rw.ID))
	ro := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "stages-r", []string{"occurrences:read"})
	reader := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&ro.ID))
	require.NoError(t, app.EnsureDefaultStagesForTest(org.ID))

	call := func(u *models.User, orgID uuid.UUID, body map[string]any) (int, []byte) {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, orgID, u.ID)
		require.NoError(t, app.ReorderOccurrenceStages(req))
		return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
	}

	before := orderedStageIDs(t, org.ID, app)
	require.GreaterOrEqual(t, len(before), 3)
	reversed := make([]string, len(before))
	for i, id := range before {
		reversed[len(before)-1-i] = id
	}

	// no permission to administer stages: nothing moves
	code, _ := call(reader, org.ID, map[string]any{"ids": reversed})
	assert.Equal(t, fasthttp.StatusForbidden, code)
	assert.Equal(t, before, orderedStageIDs(t, org.ID, app))

	// another organization's admin cannot touch this pipeline (the permutation would not match their own set)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	otherRole := testutil.CreateTestRoleWithKeys(t, app.DB, otherOrg.ID, "stages-w", []string{"occurrences.stages:write"})
	other := testutil.CreateTestUser(t, app.DB, otherOrg.ID, testutil.WithRoleID(&otherRole.ID))
	require.NoError(t, app.EnsureDefaultStagesForTest(otherOrg.ID))
	code, _ = call(other, otherOrg.ID, map[string]any{"ids": reversed})
	assert.Equal(t, fasthttp.StatusConflict, code, "stage ids of another organization are refused")
	assert.Equal(t, before, orderedStageIDs(t, org.ID, app))

	// not a permutation: missing, repeated, foreign ids
	for name, ids := range map[string][]string{
		"missing":  before[1:],
		"repeated": append([]string{before[0]}, before[:len(before)-1]...),
		"foreign":  append([]string{uuid.NewString()}, before[1:]...),
	} {
		code, _ = call(admin, org.ID, map[string]any{"ids": ids})
		assert.Equal(t, fasthttp.StatusConflict, code, name)
	}
	assert.Equal(t, before, orderedStageIDs(t, org.ID, app))

	// a valid reorder persists, and no case changes stage or status
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	initial, err := app.InitialStageForTest(org.ID)
	require.NoError(t, err)
	occ := models.Occurrence{OrganizationID: org.ID, ContactID: contact.ID, Title: "Parado", StageID: initial.ID, OpenedByUserID: admin.ID}
	require.NoError(t, app.CreateOccurrenceForTest(&occ))
	code, raw := call(admin, org.ID, map[string]any{"ids": reversed, "expected": before})
	require.Equal(t, fasthttp.StatusOK, code, string(raw))
	assert.Equal(t, reversed, orderedStageIDs(t, org.ID, app))
	var after models.Occurrence
	require.NoError(t, app.DB.First(&after, "id = ?", occ.ID).Error)
	assert.Equal(t, initial.ID, after.StageID, "reordering never moves a case")
	assert.Equal(t, occ.GeneralState, after.GeneralState)

	// stale view: the client still believes in the old order -> conflict carrying the current order
	code, raw = call(admin, org.ID, map[string]any{"ids": before, "expected": before})
	assert.Equal(t, fasthttp.StatusConflict, code)
	var body struct {
		Data struct {
			IDs []string `json:"ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, reversed, body.Data.IDs)
	assert.Equal(t, reversed, orderedStageIDs(t, org.ID, app), "the stale request changed nothing")
}

// Two admins reordering at the same time: both are serialized; exactly one wins per round and the final order is
// always one of the two requested ones, never a mix.
func TestOccurrenceStages_ReorderConcurrent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	rw := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "stages-w", []string{"occurrences.stages:write"})
	a := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&rw.ID))
	b := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&rw.ID))
	require.NoError(t, app.EnsureDefaultStagesForTest(org.ID))

	before := orderedStageIDs(t, org.ID, app)
	orderA := append([]string{}, before...)
	orderA[0], orderA[1] = orderA[1], orderA[0]
	orderB := make([]string, len(before))
	for i, id := range before {
		orderB[len(before)-1-i] = id
	}

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, c := range []struct {
		u   *models.User
		ids []string
	}{{a, orderA}, {b, orderB}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, map[string]any{"ids": c.ids, "expected": before})
			testutil.SetAuthContext(req, org.ID, c.u.ID)
			_ = app.ReorderOccurrenceStages(req)
			codes[i] = testutil.GetResponseStatusCode(req)
		}()
	}
	wg.Wait()

	assert.ElementsMatch(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, codes, "one wins, the other is told the order changed")
	final := orderedStageIDs(t, org.ID, app)
	assert.True(t, assert.ObjectsAreEqual(final, orderA) || assert.ObjectsAreEqual(final, orderB), "never a mix")
	// positions are a clean 0..n-1 sequence
	var rows []models.OccurrenceStage
	require.NoError(t, app.DB.Where("organization_id = ?", org.ID).Order("position").Find(&rows).Error)
	for i, s := range rows {
		assert.Equal(t, i, s.Position)
	}
}
