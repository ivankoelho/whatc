package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// AgentAnalyticsSummary represents overall agent analytics
type AgentAnalyticsSummary struct {
	TotalTransfersHandled int64            `json:"total_transfers_handled"`
	ActiveTransfers       int64            `json:"active_transfers"`
	AvgQueueTimeMins      float64          `json:"avg_queue_time_mins"`
	AvgFirstResponseMins  float64          `json:"avg_first_response_mins"`
	AvgResolutionMins     float64          `json:"avg_resolution_mins"`
	TransfersBySource     map[string]int64 `json:"transfers_by_source"`
	TotalBreakTimeMins    float64          `json:"total_break_time_mins"`
	BreakCount            int64            `json:"break_count"`
}

// AgentPerformanceStats represents performance metrics for an agent
type AgentPerformanceStats struct {
	AgentID              string  `json:"agent_id"`
	AgentName            string  `json:"agent_name"`
	AvgFirstResponseMins float64 `json:"avg_first_response_mins"`
	AvgResolutionMins    float64 `json:"avg_resolution_mins"`
	TransfersHandled     int64   `json:"transfers_handled"`
	ActiveTransfers      int64   `json:"active_transfers"`
	// ActiveTransfers split by who the attendance is waiting for (see
	// calculateStatsFor). Both can be below ActiveTransfers: an attendance with
	// no message since it started is in neither.
	AwaitingAgent    int64 `json:"awaiting_agent"`
	AwaitingCustomer int64 `json:"awaiting_customer"`
	// SLAAtRisk: escalated, not breached and the customer is waiting right now.
	// SLABreached: breached at some point (historical, "SLA violado").
	SLAAtRisk   int64 `json:"sla_at_risk"`
	SLABreached int64 `json:"sla_breached"`
	// Ownership moves in the period (see calculateStatsFor).
	ReassignedAway     int64   `json:"reassigned_away"`
	ReassignedIn       int64   `json:"reassigned_in"`
	ReturnedToQueue    int64   `json:"returned_to_queue"`
	MessagesSent       int64   `json:"messages_sent"`
	TotalBreakTimeMins float64 `json:"total_break_time_mins"`
	BreakCount         int64   `json:"break_count"`
	IsAvailable        bool    `json:"is_available"`
	CurrentBreakStart  *string `json:"current_break_start,omitempty"`
}

// TrendPoint represents a data point for time-series charts
type TrendPoint struct {
	Date             string  `json:"date"`
	TransfersHandled int64   `json:"transfers_handled"`
	AvgResponseMins  float64 `json:"avg_response_mins"`
}

// AgentAnalyticsResponse is the full API response
type AgentAnalyticsResponse struct {
	Summary    AgentAnalyticsSummary   `json:"summary"`
	AgentStats []AgentPerformanceStats `json:"agent_stats,omitempty"`
	TrendData  []TrendPoint            `json:"trend_data"`
	MyStats    *AgentPerformanceStats  `json:"my_stats,omitempty"`
}

// GetAgentAnalytics returns agent analytics for the organization
// Agents see only their own stats; Admin/Manager see all agents
func (a *App) GetAgentAnalytics(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	// Parse date range
	fromStr := string(r.RequestCtx.QueryArgs().Peek("from"))
	toStr := string(r.RequestCtx.QueryArgs().Peek("to"))
	groupBy := string(r.RequestCtx.QueryArgs().Peek("group_by"))
	agentIDStr := string(r.RequestCtx.QueryArgs().Peek("agent_id"))
	if groupBy == "" {
		groupBy = "day"
	}

	now := time.Now()
	var periodStart, periodEnd time.Time

	if fromStr != "" && toStr != "" {
		var errMsg string
		periodStart, periodEnd, errMsg = parseDateRange(fromStr, toStr, a.orgLocation(orgID))
		if errMsg != "" {
			return r.SendErrorEnvelope(fasthttp.StatusBadRequest, errMsg, nil, "")
		}
	} else {
		// Default to current month
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = now
	}

	response := AgentAnalyticsResponse{
		Summary: AgentAnalyticsSummary{
			TransfersBySource: make(map[string]int64),
		},
		TrendData: []TrendPoint{},
	}

	// Check if filtering by specific agent (requires analytics permission)
	var filterAgentID *uuid.UUID
	if a.HasPermission(userID, models.ResourceAnalytics, models.ActionRead, orgID) && agentIDStr != "" {
		parsedID, err := uuid.Parse(agentIDStr)
		if err == nil {
			filterAgentID = &parsedID
		}
	}

	if filterAgentID != nil {
		// User with analytics permission viewing specific agent
		agentStats := a.calculateAgentStats(orgID, *filterAgentID, periodStart, periodEnd)
		response.MyStats = &agentStats
		response.TrendData = a.calculateTrendData(orgID, periodStart, periodEnd, groupBy, filterAgentID)
		// Calculate summary for this specific agent
		a.calculateAgentSummaryStats(orgID, *filterAgentID, periodStart, periodEnd, &response.Summary)
	} else if !a.HasPermission(userID, models.ResourceAnalytics, models.ActionRead, orgID) {
		// Users without analytics permission only see their own stats
		myStats := a.calculateAgentStats(orgID, userID, periodStart, periodEnd)
		response.MyStats = &myStats
		response.TrendData = a.calculateTrendData(orgID, periodStart, periodEnd, groupBy, &userID)
		a.calculateAgentSummaryStats(orgID, userID, periodStart, periodEnd, &response.Summary)
	} else {
		// Users with analytics permission see all agents
		a.calculateSummaryStats(orgID, periodStart, periodEnd, &response.Summary)
		response.TrendData = a.calculateTrendData(orgID, periodStart, periodEnd, groupBy, nil)
		response.AgentStats = a.calculateAllAgentStats(orgID, periodStart, periodEnd)
		// Also include current user's stats (for their own break time tracking)
		myStats := a.calculateAgentStats(orgID, userID, periodStart, periodEnd)
		response.MyStats = &myStats
	}

	return r.SendEnvelope(response)
}

// GetAgentDetails returns detailed analytics for a specific agent
func (a *App) GetAgentDetails(r *fastglue.Request) error {
	orgID, _, err := a.requireAuth(r, models.ResourceAnalytics, models.ActionRead)
	if err != nil {
		return nil
	}

	agentID, err := parsePathUUID(r, "id", "agent")
	if err != nil {
		return nil
	}

	// Parse date range
	fromStr := string(r.RequestCtx.QueryArgs().Peek("from"))
	toStr := string(r.RequestCtx.QueryArgs().Peek("to"))
	groupBy := string(r.RequestCtx.QueryArgs().Peek("group_by"))
	if groupBy == "" {
		groupBy = "day"
	}

	now := time.Now()
	var periodStart, periodEnd time.Time

	if fromStr != "" && toStr != "" {
		var errMsg string
		periodStart, periodEnd, errMsg = parseDateRange(fromStr, toStr, a.orgLocation(orgID))
		if errMsg != "" {
			// Fall back to current month on parse error
			periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			periodEnd = now
		}
	} else {
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = now
	}

	// Verify agent exists
	_, err = findByIDAndOrg[models.User](a.DB, r, agentID, orgID, "Agent")
	if err != nil {
		return nil
	}

	stats := a.calculateAgentStats(orgID, agentID, periodStart, periodEnd)
	trendData := a.calculateTrendData(orgID, periodStart, periodEnd, groupBy, &agentID)

	return r.SendEnvelope(map[string]any{
		"agent":      stats,
		"trend_data": trendData,
	})
}

// GetAgentComparison returns comparison data for multiple agents
func (a *App) GetAgentComparison(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}

	if !a.HasPermission(userID, models.ResourceAnalytics, models.ActionRead, orgID) {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Access denied", nil, "")
	}

	// Parse date range
	fromStr := string(r.RequestCtx.QueryArgs().Peek("from"))
	toStr := string(r.RequestCtx.QueryArgs().Peek("to"))

	now := time.Now()
	var periodStart, periodEnd time.Time

	if fromStr != "" && toStr != "" {
		var errMsg string
		periodStart, periodEnd, errMsg = parseDateRange(fromStr, toStr, a.orgLocation(orgID))
		if errMsg != "" {
			// Fall back to current month on parse error
			periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			periodEnd = now
		}
	} else {
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		periodEnd = now
	}

	agentStats := a.calculateAllAgentStats(orgID, periodStart, periodEnd)

	return r.SendEnvelope(map[string]any{
		"agents": agentStats,
	})
}

// Helper functions

func (a *App) calculateSummaryStats(orgID uuid.UUID, start, end time.Time, summary *AgentAnalyticsSummary) {
	// Total transfers handled (resumed)
	a.DB.Model(&models.AgentTransfer{}).
		Where("organization_id = ? AND status = ? AND transferred_at >= ? AND transferred_at <= ?",
			orgID, models.TransferStatusResumed, start, end).
		Count(&summary.TotalTransfersHandled)

	// Active transfers
	a.DB.Model(&models.AgentTransfer{}).
		Where("organization_id = ? AND status = ?", orgID, models.TransferStatusActive).
		Count(&summary.ActiveTransfers)

	// Average queue time: from entering the queue to the FIRST pickup
	// (picked_up_at, never rewritten). Attendances still waiting have no
	// picked_up_at and are not part of the average. updated_at is not used: it
	// moves for any edit, so it measured nothing.
	type AvgResult struct {
		Avg float64
	}
	var queueTimeResult AvgResult
	a.DB.Model(&models.AgentTransfer{}).
		Select("COALESCE(AVG(EXTRACT(EPOCH FROM (picked_up_at - transferred_at))/60), 0) as avg").
		Where("organization_id = ? AND picked_up_at IS NOT NULL AND transferred_at >= ? AND transferred_at <= ?",
			orgID, start, end).
		Scan(&queueTimeResult)
	summary.AvgQueueTimeMins = queueTimeResult.Avg

	// Average first response: entering the queue to the agent's first reply.
	var firstResponseResult AvgResult
	a.DB.Model(&models.AgentTransfer{}).
		Select("COALESCE(AVG(EXTRACT(EPOCH FROM (first_response_at - transferred_at))/60), 0) as avg").
		Where("organization_id = ? AND first_response_at IS NOT NULL AND transferred_at >= ? AND transferred_at <= ?",
			orgID, start, end).
		Scan(&firstResponseResult)
	summary.AvgFirstResponseMins = firstResponseResult.Avg

	// Average resolution time (time from transfer to resume)
	var resolutionTimeResult AvgResult
	a.DB.Model(&models.AgentTransfer{}).
		Select("AVG(EXTRACT(EPOCH FROM (resumed_at - transferred_at))/60) as avg").
		Where("organization_id = ? AND status = ? AND resumed_at IS NOT NULL AND transferred_at >= ? AND transferred_at <= ?",
			orgID, models.TransferStatusResumed, start, end).
		Scan(&resolutionTimeResult)
	summary.AvgResolutionMins = resolutionTimeResult.Avg

	// Transfers by source
	type SourceCount struct {
		Source string
		Count  int64
	}
	var sourceCounts []SourceCount
	a.DB.Model(&models.AgentTransfer{}).
		Select("source, COUNT(*) as count").
		Where("organization_id = ? AND transferred_at >= ? AND transferred_at <= ?", orgID, start, end).
		Group("source").
		Scan(&sourceCounts)

	for _, sc := range sourceCounts {
		summary.TransfersBySource[sc.Source] = sc.Count
	}
}

func (a *App) calculateTrendData(orgID uuid.UUID, start, end time.Time, groupBy string, agentID *uuid.UUID) []TrendPoint {
	var dateFormat string
	var dateTrunc string

	switch groupBy {
	case "week":
		dateFormat = "2006-01-02"
		dateTrunc = "week"
	default: // day
		dateFormat = "2006-01-02"
		dateTrunc = "day"
	}

	type TrendResult struct {
		Date  time.Time
		Count int64
	}

	query := a.DB.Model(&models.AgentTransfer{}).
		Select("DATE_TRUNC('"+dateTrunc+"', transferred_at) as date, COUNT(*) as count").
		Where("organization_id = ? AND status = ? AND transferred_at >= ? AND transferred_at <= ?",
			orgID, models.TransferStatusResumed, start, end)

	if agentID != nil {
		query = query.Where("agent_id = ?", *agentID)
	}

	var results []TrendResult
	query.Group("DATE_TRUNC('" + dateTrunc + "', transferred_at)").
		Order("date ASC").
		Scan(&results)

	trendData := make([]TrendPoint, len(results))
	for i, r := range results {
		trendData[i] = TrendPoint{
			Date:             r.Date.Format(dateFormat),
			TransfersHandled: r.Count,
		}
	}

	return trendData
}
