package handlers

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// LiveAgent is one row of the live supervision panel: who is where right now.
type LiveAgent struct {
	AgentPerformanceStats
	State models.AgentState `json:"state"`
	// StateSince is when State really began; nil when the log cannot tell.
	StateSince *time.Time `json:"state_since,omitempty"`
}

// LiveAgentsResponse is the live panel snapshot. Scope tells the UI which view
// it got: "all" (analytics:read, whole organization) or "team" (view_team).
type LiveAgentsResponse struct {
	Scope  string      `json:"scope"`
	Agents []LiveAgent `json:"agents"`
	// Queue is attendances waiting in a queue (no agent yet) inside the scope.
	Queue int64 `json:"queue"`
}

// liveAgentsScope resolves how much of the organization the user may supervise.
// analytics:read keeps its organization-wide meaning; analytics.agents:view_team
// narrows to the users who share a team with the caller (same rule as
// conversations:view_team, see salesTeamMates). Neither: no access.
func (a *App) liveAgentsScope(userID, orgID uuid.UUID) string {
	switch {
	case a.HasPermission(userID, models.ResourceAnalytics, models.ActionRead, orgID):
		return "all"
	case a.HasPermission(userID, models.ResourceAnalyticsAgents, models.ActionViewTeam, orgID):
		return "team"
	default:
		return ""
	}
}

// GetLiveAgents returns the current state of the agents the caller supervises:
// presence (with since), the attendances each one owns split by who is waited
// for, SLA risk, and the size of the queue. Stats are for the current day in
// the organization's timezone; the counts that matter live (active, waiting)
// do not depend on the period.
func (a *App) GetLiveAgents(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	scope := a.liveAgentsScope(userID, orgID)
	if scope == "" {
		return r.SendErrorEnvelope(fasthttp.StatusForbidden, "Access denied", nil, "")
	}

	// Agents (and queue) in scope. Team scope: the caller's own teams only.
	agentQ := a.DB.Model(&models.User{}).
		Joins("JOIN team_members ON team_members.user_id = users.id").
		Where("users.organization_id = ? AND team_members.role = ?", orgID, models.TeamRoleAgent)
	queueQ := a.DB.Model(&models.AgentTransfer{}).
		Where("organization_id = ? AND status = ? AND agent_id IS NULL", orgID, models.TransferStatusActive)
	if scope == "team" {
		myTeams := a.DB.Model(&models.TeamMember{}).Select("team_id").Where("user_id = ?", userID)
		agentQ = agentQ.Where("team_members.team_id IN (?)", myTeams)
		queueQ = queueQ.Where("team_id IN (?)", myTeams)
	}

	var ids []uuid.UUID
	if err := agentQ.Distinct().Pluck("users.id", &ids).Error; err != nil {
		a.Log.Error("Failed to list agents for the live panel", "error", err, "org_id", orgID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to load agents", nil, "")
	}
	var queue int64
	if err := queueQ.Count(&queue).Error; err != nil {
		a.Log.Error("Failed to count the queue for the live panel", "error", err, "org_id", orgID)
	}

	now := time.Now()
	loc := a.orgLocation(orgID)
	y, m, d := now.In(loc).Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, loc)
	stats := a.calculateStatsFor(orgID, ids, dayStart, now)

	states := make(map[uuid.UUID]models.AgentState, len(ids))
	for _, id := range ids {
		if st, ok := a.liveAgentState(orgID, id); ok {
			states[id] = st
		}
	}
	since := a.presenceSince(orgID, states)

	agents := make([]LiveAgent, 0, len(ids))
	for _, id := range ids {
		agents = append(agents, LiveAgent{
			AgentPerformanceStats: *stats[id],
			State:                 states[id],
			StateSince:            since[id],
		})
	}
	// Most pressing first: customers waiting on the agent, then name.
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].AwaitingAgent != agents[j].AwaitingAgent {
			return agents[i].AwaitingAgent > agents[j].AwaitingAgent
		}
		return agents[i].AgentName < agents[j].AgentName
	})

	return r.SendEnvelope(LiveAgentsResponse{Scope: scope, Agents: agents, Queue: queue})
}
