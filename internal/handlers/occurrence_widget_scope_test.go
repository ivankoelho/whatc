package handlers_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Dashboard indicators must show each user exactly the cases their list shows.
func TestOccurrenceWidgets_FollowTheViewersScope(t *testing.T) {
	w := newIsoWorld(t)
	// A second, unrelated case of another store, so scopes differ.
	contact2 := w.occ.ContactID
	stage, err := w.app.InitialStageForTest(w.org.ID)
	require.NoError(t, err)
	other := models.Occurrence{OrganizationID: w.org.ID, ContactID: contact2, Title: "Outra loja",
		StageID: stage.ID, OpenedByUserID: w.logOutra.ID, TeamID: &w.teamOutra.ID, UnitID: &w.unitOutra.ID}
	require.NoError(t, w.app.CreateOccurrenceForTest(&other))

	count := func(viewer uuid.UUID) float64 {
		wd := models.Widget{OrganizationID: w.org.ID, DataSource: "occurrences", Metric: "count", Field: "open", DisplayType: "number"}
		resp, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, viewer, wd, "", "")
		require.NoError(t, err)
		return resp.Value
	}
	assert.EqualValues(t, 1, count(w.logPorto.ID), "Porto sees only its case")
	assert.EqualValues(t, 1, count(w.logOutra.ID), "the other store sees only its own")
	assert.EqualValues(t, 1, count(w.supPorto.ID))
	assert.EqualValues(t, 2, count(w.supSAC.ID), "view_all consolidates")
	assert.EqualValues(t, 1, count(w.sac.ID), "SAC counts what it opened")
	assert.EqualValues(t, 0, count(uuid.Nil), "no viewer, no data")

	// Table, grouped and chart paths use the same scope.
	for _, display := range []string{"table", "chart"} {
		wd := models.Widget{OrganizationID: w.org.ID, DataSource: "occurrences", Metric: "count", DisplayType: display, ChartType: "bar", GroupByField: "priority"}
		pResp, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.logPorto.ID, wd, "", "")
		require.NoError(t, err)
		sResp, err := w.app.ExecuteWidgetQueryForTest(w.org.ID, w.supSAC.ID, wd, "", "")
		require.NoError(t, err)
		assert.Equal(t, 1, len(pResp.TableRows)+sumPoints(pResp.DataPoints), display)
		assert.Equal(t, 2, len(sResp.TableRows)+sumPoints(sResp.DataPoints), display)
	}

	// Other organization: nothing leaks.
	org2 := newIsoWorld(t)
	assert.EqualValues(t, 1, func() float64 {
		wd := models.Widget{OrganizationID: org2.org.ID, DataSource: "occurrences", Metric: "count", Field: "open"}
		r, _ := org2.app.ExecuteWidgetQueryForTest(org2.org.ID, org2.supSAC.ID, wd, "", "")
		return r.Value
	}(), "an org's view_all sees only that org")
}

func sumPoints(points []handlers.DataPoint) int {
	n := 0
	for _, p := range points {
		n += int(p.Value)
	}
	return n
}
