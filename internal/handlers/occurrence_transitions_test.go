package handlers_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func (w *isoWorld) stageOf(t *testing.T, state string) models.OccurrenceStage {
	t.Helper()
	var st models.OccurrenceStage
	require.NoError(t, w.app.DB.Where("organization_id = ? AND team_id IS NULL AND general_state = ?", w.org.ID, state).
		Order("position").First(&st).Error, state)
	return st
}

func (w *isoWorld) teamStage(t *testing.T, team *models.Team, name, state string, allowedFrom ...string) models.OccurrenceStage {
	t.Helper()
	st := models.OccurrenceStage{OrganizationID: w.org.ID, Name: name, TeamID: &team.ID,
		GeneralState: state, IsClosing: state == models.GeneralStateResolved, AllowedFrom: allowedFrom}
	require.NoError(t, w.app.DB.Create(&st).Error)
	return st
}

func (w *isoWorld) move(t *testing.T, userID uuid.UUID, stageID uuid.UUID) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"stage_id": stageID.String()})
	testutil.SetAuthContext(req, w.org.ID, userID)
	testutil.SetPathParam(req, "id", w.occ.ID.String())
	require.NoError(t, w.app.ChangeOccurrenceStage(req))
	return testutil.GetResponseStatusCode(req)
}

func (w *isoWorld) reload(t *testing.T) models.Occurrence {
	t.Helper()
	var o models.Occurrence
	require.NoError(t, w.app.DB.First(&o, w.occ.ID).Error)
	return o
}

func TestTransitions_UnassignedCaseMustBeClaimedBeforeItMoves(t *testing.T) {
	w := newIsoWorld(t)
	em := w.stageOf(t, models.GeneralStateInProgress)
	assert.Equal(t, fasthttp.StatusConflict, w.move(t, w.logPorto.ID, em.ID), "claim first")
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	assert.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, em.ID))
	assert.Equal(t, models.GeneralStateInProgress, w.reload(t).GeneralState)
}

func TestTransitions_OnlyAssigneeOrSupervisorOfTheScopeMoves(t *testing.T) {
	w := newIsoWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	em := w.stageOf(t, models.GeneralStateInProgress)

	mate := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(w.logPorto.RoleID))
	require.NoError(t, w.app.DB.Create(&models.TeamMember{TeamID: w.teamPorto.ID, UserID: mate.ID}).Error)

	assert.Equal(t, fasthttp.StatusForbidden, w.move(t, mate.ID, em.ID), "teammate who is not the assignee")
	assert.Equal(t, fasthttp.StatusForbidden, w.move(t, w.sac.ID, em.ID), "SAC follows, does not work the case")
	assert.Equal(t, fasthttp.StatusForbidden, w.move(t, w.logOutra.ID, em.ID), "another store")
	assert.Equal(t, fasthttp.StatusOK, w.move(t, w.supPorto.ID, em.ID), "store supervisor intervenes")
}

func TestTransitions_StageMustBelongToTheCasePipeline(t *testing.T) {
	w := newIsoWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	mine := w.teamStage(t, w.teamPorto, "Consultando pedido", models.GeneralStateInProgress)
	foreign := w.teamStage(t, w.teamOutra, "Etapa da outra loja", models.GeneralStateInProgress)

	assert.Equal(t, fasthttp.StatusUnprocessableEntity, w.move(t, w.logPorto.ID, foreign.ID), "ID of another team's stage")
	assert.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, mine.ID))
}

func TestTransitions_AllowedFromIsEnforced(t *testing.T) {
	w := newIsoWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	aberto := w.stageOf(t, models.GeneralStateNew)
	wait := w.stageOf(t, models.GeneralStateWaitingCustomer)
	em := w.stageOf(t, models.GeneralStateInProgress)
	confirm := w.teamStage(t, w.teamPorto, "Confirmar com cliente", models.GeneralStateWaitingCustomer, em.ID.String())

	assert.Equal(t, fasthttp.StatusUnprocessableEntity, w.move(t, w.logPorto.ID, confirm.ID), "not reachable from Aberto")
	require.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, em.ID))
	assert.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, confirm.ID), "reachable from Em análise")
	assert.Equal(t, models.GeneralStateWaitingCustomer, w.reload(t).GeneralState)
	_ = aberto
	_ = wait
}

func TestTransitions_ResolveAndReopen(t *testing.T) {
	w := newIsoWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	done := w.stageOf(t, models.GeneralStateResolved)
	em := w.stageOf(t, models.GeneralStateInProgress)

	require.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, done.ID))
	o := w.reload(t)
	assert.Equal(t, models.GeneralStateResolved, o.GeneralState)
	assert.NotNil(t, o.ClosedAt)
	assert.Equal(t, 0, o.ReopenedCount)

	assert.Equal(t, fasthttp.StatusForbidden, w.move(t, w.sac.ID, em.ID), "the opener cannot reopen a team's case")
	assert.Equal(t, fasthttp.StatusForbidden, w.move(t, w.logOutra.ID, em.ID))
	require.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, em.ID), "assignee reopens")
	o = w.reload(t)
	assert.Nil(t, o.ClosedAt)
	assert.Equal(t, 1, o.ReopenedCount)
	assert.Equal(t, models.GeneralStateInProgress, o.GeneralState)

	var ev models.OccurrenceEvent
	require.NoError(t, w.app.DB.Where("occurrence_id = ? AND type = ?", w.occ.ID, models.OccurrenceEventStageChange).
		Order("created_at DESC").First(&ev).Error)
	assert.Equal(t, true, ev.Metadata["reopened"])
}

// Concurrent moves from the same starting stage never both win from it, the
// timeline records exactly the moves that happened, and the denormalized
// general_state always matches the final stage.
func TestTransitions_ConcurrentMovesStayConsistent(t *testing.T) {
	w := newIsoWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	targets := []models.OccurrenceStage{
		w.stageOf(t, models.GeneralStateInProgress),
		w.stageOf(t, models.GeneralStateWaitingCustomer),
		w.stageOf(t, models.GeneralStateResolved),
	}

	const n = 9
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = w.move(t, w.logPorto.ID, targets[i%len(targets)].ID)
		}(i)
	}
	wg.Wait()

	ok := 0
	for _, c := range codes {
		require.Contains(t, []int{fasthttp.StatusOK, fasthttp.StatusConflict}, c, codes)
		if c == fasthttp.StatusOK {
			ok++
		}
	}
	require.GreaterOrEqual(t, ok, 1)

	var events int64
	w.app.DB.Model(&models.OccurrenceEvent{}).
		Where("occurrence_id = ? AND type = ?", w.occ.ID, models.OccurrenceEventStageChange).Count(&events)
	// A same-stage resend is a no-op 200 without an event, so events <= ok.
	assert.LessOrEqual(t, int(events), ok)
	assert.GreaterOrEqual(t, int(events), 1)

	o := w.reload(t)
	var st models.OccurrenceStage
	require.NoError(t, w.app.DB.First(&st, o.StageID).Error)
	assert.Equal(t, st.EffectiveState(), o.GeneralState)
	assert.Equal(t, o.GeneralState == models.GeneralStateResolved, o.ClosedAt != nil)
}

func TestStages_OldClientsStillWorkAndStayConsistent(t *testing.T) {
	w := newIsoWorld(t)
	role := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "stage-admin-"+uuid.NewString()[:6],
		[]string{"occurrences:read", "occurrences.stages:write", "occurrences.stages:read"})
	admin := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&role.ID))

	call := func(h func(*fastglue.Request) error, body map[string]any, id *uuid.UUID) (int, models.OccurrenceStage) {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.org.ID, admin.ID)
		if id != nil {
			testutil.SetPathParam(req, "id", id.String())
		}
		require.NoError(t, h(req))
		var env struct {
			Data models.OccurrenceStage `json:"data"`
		}
		_ = json.Unmarshal(testutil.GetResponseBody(req), &env)
		return testutil.GetResponseStatusCode(req), env.Data
	}

	// Old payload: flags only.
	code, mid := call(w.app.CreateOccurrenceStage, map[string]any{"name": "Triagem " + uuid.NewString()[:4], "position": 9}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.GeneralStateInProgress, mid.GeneralState)
	code, closing := call(w.app.CreateOccurrenceStage, map[string]any{"name": "Fechado " + uuid.NewString()[:4], "is_closing": true}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.GeneralStateResolved, closing.GeneralState)

	// New payload validation.
	code, _ = call(w.app.CreateOccurrenceStage, map[string]any{"name": "Ruim", "general_state": "inventado"}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code)
	code, _ = call(w.app.CreateOccurrenceStage, map[string]any{"name": "Ruim2", "general_state": "in_progress", "is_initial": true}, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code, "initial must be state new")
	code, _ = call(w.app.CreateOccurrenceStage, map[string]any{"name": "Fora", "team_id": uuid.NewString()}, nil)
	assert.Equal(t, fasthttp.StatusNotFound, code, "team of nowhere")

	// Same name allowed in different scopes, rejected within one.
	code, _ = call(w.app.CreateOccurrenceStage, map[string]any{"name": "Triagem", "team_id": w.teamPorto.ID.String(), "general_state": "new"}, nil)
	assert.Equal(t, fasthttp.StatusOK, code)
	code, _ = call(w.app.CreateOccurrenceStage, map[string]any{"name": "Triagem", "team_id": w.teamPorto.ID.String(), "general_state": "new"}, nil)
	assert.Equal(t, fasthttp.StatusConflict, code)

	// Reclassifying a stage carries its cases along.
	require.NoError(t, w.app.DB.Model(&models.Occurrence{}).Where("id = ?", w.occ.ID).Update("stage_id", mid.ID).Error)
	code, _ = call(w.app.UpdateOccurrenceStage, map[string]any{"name": mid.Name, "general_state": "waiting_customer", "position": 9}, &mid.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.GeneralStateWaitingCustomer, w.reload(t).GeneralState)

	// An old client resending flags only does not reset the state.
	code, kept := call(w.app.UpdateOccurrenceStage, map[string]any{"name": mid.Name, "position": 9}, &mid.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.GeneralStateWaitingCustomer, kept.GeneralState)
}

func TestCreate_ForwardedCaseIsBornUnassignedInTheTargetTeam(t *testing.T) {
	w := newIsoWorld(t)
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id": contact.ID.String(), "title": "Pedido 45872 atrasado", "team_id": w.teamPorto.ID.String(),
	})
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	require.NoError(t, w.app.CreateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))

	var env struct {
		Data struct {
			ID             uuid.UUID  `json:"id"`
			ProtocolNumber string     `json:"protocol_number"`
			GeneralState   string     `json:"general_state"`
			TeamID         *uuid.UUID `json:"team_id"`
			UnitID         *uuid.UUID `json:"unit_id"`
			Assigned       *uuid.UUID `json:"assigned_user_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &env))
	assert.Equal(t, models.GeneralStateNew, env.Data.GeneralState)
	assert.Nil(t, env.Data.Assigned, "the receiving team claims it")
	require.NotNil(t, env.Data.TeamID)
	assert.Equal(t, w.teamPorto.ID, *env.Data.TeamID)
	require.NotNil(t, env.Data.UnitID)
	assert.Equal(t, w.unitPorto.ID, *env.Data.UnitID)

	assert.True(t, w.listContains(t, w.logPorto.ID, env.Data.ProtocolNumber))
	assert.False(t, w.listContains(t, w.logOutra.ID, env.Data.ProtocolNumber))
	assert.True(t, w.listContains(t, w.sac.ID, env.Data.ProtocolNumber), "SAC keeps following")
}

func TestSummary_CountersMatchListAndScope(t *testing.T) {
	w := newIsoWorld(t)
	require.Equal(t, fasthttp.StatusOK, w.claim(t, w.logPorto.ID))
	require.Equal(t, fasthttp.StatusOK, w.move(t, w.logPorto.ID, w.stageOf(t, models.GeneralStateWaitingCustomer).ID))

	type summary struct {
		Data struct {
			Counts map[string]int64 `json:"counts"`
			Total  int64            `json:"total"`
		} `json:"data"`
	}
	sum := func(userID uuid.UUID) summary {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, w.org.ID, userID)
		require.NoError(t, w.app.GetOccurrencesSummary(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var s summary
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &s))
		return s
	}
	listTotal := func(userID uuid.UUID, state string) int64 {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, w.org.ID, userID)
		if state != "" {
			testutil.SetQueryParam(req, "general_state", state)
		}
		require.NoError(t, w.app.ListOccurrences(req))
		var env struct {
			Data struct {
				Total int64 `json:"total"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &env))
		return env.Data.Total
	}

	for _, u := range []*models.User{w.logPorto, w.sac, w.supPorto, w.supSAC, w.logOutra} {
		s := sum(u.ID)
		assert.Equal(t, listTotal(u.ID, ""), s.Data.Total, u.FullName)
		for state, n := range s.Data.Counts {
			if state == "unclassified" {
				continue
			}
			assert.Equal(t, listTotal(u.ID, state), n, "%s/%s", u.FullName, state)
		}
	}
	assert.EqualValues(t, 0, sum(w.logOutra.ID).Data.Total, "another store counts nothing of this case")
	assert.EqualValues(t, 1, sum(w.logPorto.ID).Data.Counts[models.GeneralStateWaitingCustomer])
}
