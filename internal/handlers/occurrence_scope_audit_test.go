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

type auditEnv struct {
	Data struct {
		Summary     map[string]int64 `json:"summary"`
		Occurrences []struct {
			ID         uuid.UUID `json:"id"`
			Suggestion *struct {
				Reason string `json:"reason"`
			} `json:"suggestion"`
		} `json:"occurrences"`
	} `json:"data"`
}

func (w *isoWorld) legacy(t *testing.T, mutate func(*models.Occurrence)) models.Occurrence {
	t.Helper()
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	stage, err := w.app.InitialStageForTest(w.org.ID)
	require.NoError(t, err)
	o := models.Occurrence{OrganizationID: w.org.ID, ContactID: contact.ID, Title: "Antigo",
		StageID: stage.ID, OpenedByUserID: w.sac.ID}
	mutate(&o)
	require.NoError(t, w.app.CreateOccurrenceForTest(&o))
	return o
}

func (w *isoWorld) audit(t *testing.T, userID uuid.UUID) (int, auditEnv) {
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, userID)
	testutil.SetQueryParam(req, "only", "incomplete")
	require.NoError(t, w.app.GetOccurrenceScopeAudit(req))
	var env auditEnv
	_ = json.Unmarshal(testutil.GetResponseBody(req), &env)
	return testutil.GetResponseStatusCode(req), env
}

func TestScopeAudit_OnlyOrgWideSupervisorsAndOnlyReliableSuggestions(t *testing.T) {
	w := newIsoWorld(t)
	dept := models.Department{OrganizationID: w.org.ID, Name: "Logística " + uuid.NewString()[:6], Active: true}
	require.NoError(t, w.app.DB.Create(&dept).Error)
	// One team is unique for (Porto, dept); the other store has two for the same pair.
	require.NoError(t, w.app.DB.Model(w.teamPorto).Update("department_id", dept.ID).Error)
	dupA := models.Team{OrganizationID: w.org.ID, Name: "dupA" + uuid.NewString()[:5], IsActive: true, UnitID: &w.unitOutra.ID, DepartmentID: &dept.ID}
	dupB := models.Team{OrganizationID: w.org.ID, Name: "dupB" + uuid.NewString()[:5], IsActive: true, UnitID: &w.unitOutra.ID, DepartmentID: &dept.ID}
	require.NoError(t, w.app.DB.Create(&dupA).Error)
	require.NoError(t, w.app.DB.Create(&dupB).Error)

	teamOnly := w.legacy(t, func(o *models.Occurrence) { o.TeamID = &w.teamPorto.ID })                             // unit from team
	uniquePair := w.legacy(t, func(o *models.Occurrence) { o.UnitID, o.DepartmentID = &w.unitPorto.ID, &dept.ID }) // single team
	ambiguous := w.legacy(t, func(o *models.Occurrence) { o.UnitID, o.DepartmentID = &w.unitOutra.ID, &dept.ID })  // two teams
	bare := w.legacy(t, func(o *models.Occurrence) { o.AssignedUserID = &w.logPorto.ID })                          // nothing known
	tr := models.AgentTransfer{OrganizationID: w.org.ID, ContactID: w.occ.ContactID, WhatsAppAccount: "x", PhoneNumber: "5511900000000", TeamID: &w.teamPorto.ID}
	require.NoError(t, w.app.DB.Create(&tr).Error)
	viaTransfer := w.legacy(t, func(o *models.Occurrence) { o.SourceTransferID = &tr.ID })

	code, _ := w.audit(t, w.logPorto.ID)
	assert.Equal(t, fasthttp.StatusForbidden, code, "an agent cannot audit")
	code, _ = w.audit(t, w.supPorto.ID)
	assert.Equal(t, fasthttp.StatusForbidden, code, "a store supervisor is not org-wide")

	code, env := w.audit(t, w.supSAC.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	reasons := map[uuid.UUID]string{}
	for _, row := range env.Data.Occurrences {
		if row.Suggestion != nil {
			reasons[row.ID] = row.Suggestion.Reason
		}
	}
	assert.Equal(t, "team_unit", reasons[teamOnly.ID])
	assert.Equal(t, "unit_department_single_team", reasons[uniquePair.ID])
	assert.Equal(t, "source_transfer_team", reasons[viaTransfer.ID])
	assert.NotContains(t, reasons, ambiguous.ID, "two candidate teams: not guessed")
	assert.NotContains(t, reasons, bare.ID, "an assignee alone proves nothing")
	assert.GreaterOrEqual(t, env.Data.Summary["restricted_with_assignee"], int64(1))
	assert.GreaterOrEqual(t, env.Data.Summary["determinable"], int64(3))

	// Applying: reliable ones get regularized, the rest are skipped; events are written.
	req := testutil.NewJSONRequest(t, map[string]any{"occurrence_ids": []string{
		uniquePair.ID.String(), ambiguous.ID.String(), bare.ID.String(), viaTransfer.ID.String()}})
	testutil.SetAuthContext(req, w.org.ID, w.supSAC.ID)
	require.NoError(t, w.app.ApplyOccurrenceScopeSuggestions(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var applied struct {
		Data map[string]int `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &applied))
	assert.Equal(t, 2, applied.Data["applied"])
	assert.Equal(t, 2, applied.Data["skipped"])

	var got models.Occurrence
	require.NoError(t, w.app.DB.First(&got, uniquePair.ID).Error)
	require.NotNil(t, got.TeamID)
	assert.Equal(t, w.teamPorto.ID, *got.TeamID)
	var n int64
	w.app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", uniquePair.ID, models.OccurrenceEventScopeRegularized).Count(&n)
	assert.EqualValues(t, 1, n, "regularization is on the case timeline")
	assert.True(t, w.listContains(t, w.logPorto.ID, got.ProtocolNumber), "now in the Porto team's scope")

	// The ambiguous one stays restricted until a person decides, with a reason.
	assert.False(t, w.listContains(t, w.logPorto.ID, ambiguous.ProtocolNumber))
	manual := func(body map[string]any) int {
		r := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(r, w.org.ID, w.supSAC.ID)
		testutil.SetPathParam(r, "id", ambiguous.ID.String())
		require.NoError(t, w.app.SetOccurrenceScope(r))
		return testutil.GetResponseStatusCode(r)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, manual(map[string]any{"team_id": dupA.ID.String()}), "reason is mandatory")
	assert.Equal(t, fasthttp.StatusOK, manual(map[string]any{"team_id": dupA.ID.String(), "reason": "confirmado com a loja"}))
	assert.Equal(t, fasthttp.StatusConflict, manual(map[string]any{"team_id": dupB.ID.String(), "reason": "outra vez"}), "already scoped")
}
