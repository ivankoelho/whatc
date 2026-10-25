package handlers

import (
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// GetOccurrencesSummary returns how many cases the caller can see in each
// general state. It runs through the same visibleOccurrences scope as the list
// and the board, so counters, list and Kanban cannot disagree. Optional
// team_id / unit_id narrow it the same way the list filters do.
func (a *App) GetOccurrencesSummary(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	q := a.DB.Model(&models.Occurrence{}).Where("occurrences.organization_id = ?", orgID)
	q = a.visibleOccurrences(q, userID, orgID)
	if v := string(r.RequestCtx.QueryArgs().Peek("team_id")); v != "" {
		q = q.Where("occurrences.team_id = ?", v)
	}
	if v := string(r.RequestCtx.QueryArgs().Peek("unit_id")); v != "" {
		q = q.Where("occurrences.unit_id = ?", v)
	}

	var rows []struct {
		State string
		N     int64
	}
	if err := q.Select("occurrences.general_state AS state, COUNT(*) AS n").
		Group("occurrences.general_state").Scan(&rows).Error; err != nil {
		a.Log.Error("Failed to summarize occurrences", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to summarize occurrences", nil, "")
	}
	counts := map[string]int64{
		models.GeneralStateNew: 0, models.GeneralStateInProgress: 0,
		models.GeneralStateWaitingCustomer: 0, models.GeneralStateResolved: 0,
	}
	var total int64
	for _, row := range rows {
		total += row.N
		if _, ok := counts[row.State]; ok {
			counts[row.State] += row.N
		} else {
			counts["unclassified"] += row.N
		}
	}
	return r.SendEnvelope(map[string]any{"counts": counts, "total": total})
}
