package handlers_test

import (
	"time"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestInternalTicket_CreateRoutesAndStaysInScope(t *testing.T) {
	w := newIsoWorld(t)

	open := func(body map[string]any) (int, models.Occurrence) {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
		require.NoError(t, w.app.CreateInternalTicket(req))
		code := testutil.GetResponseStatusCode(req)
		var occ models.Occurrence
		if code == fasthttp.StatusOK {
			var out struct {
				Data struct {
					ID uuid.UUID `json:"id"`
				} `json:"data"`
			}
			testutil.ParseJSONResponse(t, req, &out)
			require.NoError(t, w.app.DB.First(&occ, "id = ?", out.Data.ID).Error)
		}
		return code, occ
	}
	team := w.teamPorto.ID.String()

	// Impact x urgency decide the priority; no customer is attached.
	code, occ := open(map[string]any{
		"title": "Impressora parada", "description": "Não imprime", "ticket_type": "incident",
		"impact": "high", "urgency": "high", "team_id": team,
	})
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Equal(t, models.OccurrenceKindInternal, occ.Kind)
	assert.Nil(t, occ.ContactID, "an internal ticket has no customer")
	assert.Equal(t, models.OccurrencePriorityUrgent, occ.Priority)
	assert.Equal(t, w.teamPorto.ID, *occ.TeamID)
	assert.NotNil(t, occ.SLA.ResolutionDeadline)

	// The rest of the matrix.
	for _, c := range []struct {
		impact, urgency string
		want            models.OccurrencePriority
	}{
		{"high", "medium", models.OccurrencePriorityHigh},
		{"medium", "medium", models.OccurrencePriorityNormal},
		{"low", "medium", models.OccurrencePriorityLow},
	} {
		code, o := open(map[string]any{"title": "m", "description": "d", "team_id": team, "impact": c.impact, "urgency": c.urgency})
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Equal(t, c.want, o.Priority, "%s x %s", c.impact, c.urgency)
	}

	// Destination, subject, description and levels are validated.
	code, _ = open(map[string]any{"title": "x", "description": "y"})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "no team or department")
	code, _ = open(map[string]any{"title": "x", "team_id": team})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "no description")
	code, _ = open(map[string]any{"title": "x", "description": "y", "team_id": team, "impact": "huge"})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "bad impact")

	// The assignee must belong to the destination team.
	code, _ = open(map[string]any{"title": "x", "description": "y", "team_id": team, "assigned_user_id": w.logOutra.ID.String()})
	assert.Equal(t, fasthttp.StatusBadRequest, code, "assignee outside the team")

	// Observers become comment-only participants. The requester is on the case
	// through the forwarding trail, exactly once, even when listed as an observer.
	code, withObs := open(map[string]any{"title": "o", "description": "d", "team_id": team,
		"observer_user_ids": []string{w.logOutra.ID.String(), w.sac.ID.String()}})
	require.Equal(t, fasthttp.StatusOK, code)
	count := func(userID uuid.UUID) (n int64) {
		w.app.DB.Model(&models.OccurrenceParticipant{}).Where("occurrence_id = ? AND user_id = ?", withObs.ID, userID).Count(&n)
		return
	}
	assert.EqualValues(t, 1, count(w.logOutra.ID), "observer added")
	assert.EqualValues(t, 1, count(w.sac.ID), "requester not duplicated")

	// Case scope: the receiving team sees it, another store's team does not, and
	// nothing customer-facing is reachable (it has no customer).
	get := func(userID uuid.UUID) int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, w.org.ID, userID)
		testutil.SetPathParam(req, "id", occ.ID.String())
		require.NoError(t, w.app.GetOccurrence(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusOK, get(w.logPorto.ID))
	assert.Equal(t, fasthttp.StatusForbidden, get(w.logOutra.ID))

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, w.org.ID, w.logPorto.ID)
	testutil.SetPathParam(req, "id", occ.ID.String())
	require.NoError(t, w.app.SendOccurrenceProtocol(req))
	assert.NotEqual(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), "an internal ticket cannot message a customer")
}

// An internal ticket has a resolution deadline and no first-response deadline: that
// one measures the first public reply to a customer, which it cannot have.
func TestInternalTicket_SLAHasNoFirstResponseDeadline(t *testing.T) {
	w := newIsoWorld(t)
	team := w.teamPorto.ID.String()

	req := testutil.NewJSONRequest(t, map[string]any{
		"title": "t", "description": "d", "team_id": team, "impact": "low", "urgency": "low",
	})
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	require.NoError(t, w.app.CreateInternalTicket(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var occ models.Occurrence
	require.NoError(t, w.app.DB.Where("organization_id = ? AND kind = ?", w.org.ID, models.OccurrenceKindInternal).First(&occ).Error)
	assert.Nil(t, occ.SLA.ResponseDeadline, "no first-response deadline")
	require.NotNil(t, occ.SLA.ResolutionDeadline)
	lowResolution := *occ.SLA.ResolutionDeadline
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), lowResolution, 10*time.Second) // default low policy

	// Raising the priority recalculates the resolution deadline but never creates the response one.
	upd := testutil.NewJSONRequest(t, map[string]any{"title": occ.Title, "priority": "urgent"})
	testutil.SetAuthContext(upd, w.org.ID, w.logPorto.ID)
	testutil.SetPathParam(upd, "id", occ.ID.String())
	require.NoError(t, w.app.UpdateOccurrence(upd))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(upd))

	require.NoError(t, w.app.DB.First(&occ, "id = ?", occ.ID).Error)
	assert.Equal(t, models.OccurrencePriorityUrgent, occ.Priority)
	assert.Nil(t, occ.SLA.ResponseDeadline, "a priority change must not create a first-response deadline")
	require.NotNil(t, occ.SLA.ResolutionDeadline)
	assert.True(t, occ.SLA.ResolutionDeadline.Before(lowResolution), "urgent resolves sooner than low")
	assert.WithinDuration(t, time.Now().Add(4*time.Hour), *occ.SLA.ResolutionDeadline, 10*time.Second)
}
