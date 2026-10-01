package assignment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/logf"
	"gorm.io/gorm"
)

// LoadCounter counts the number of active items per agent. Callers provide a
// domain-specific implementation: chat transfers count AgentTransfer rows while
// call transfers count CallTransfer rows.
type LoadCounter func(db *gorm.DB, orgID uuid.UUID, agentIDs []uuid.UUID) map[uuid.UUID]int64

// Assigner provides team-based agent assignment with caching.
type Assigner struct {
	db    *gorm.DB
	redis *redis.Client
	log   logf.Logger
	// presence is optional; see SetPresence.
	presence PresenceFunc
}

// New creates a new Assigner.
func New(db *gorm.DB, redis *redis.Client, log logf.Logger) *Assigner {
	return &Assigner{db: db, redis: redis, log: log}
}

// AssignToTeam dispatches to the appropriate strategy based on the team's
// AssignmentStrategy. excludeAgentIDs allows callers to skip agents that have
// already been tried (e.g., during call transfer rotation). Pass nil for normal
// use. loadCounter provides the domain-specific load counting function for the
// load_balanced strategy.
func (a *Assigner) AssignToTeam(teamID, orgID uuid.UUID, excludeAgentIDs []uuid.UUID, loadCounter LoadCounter) *uuid.UUID {
	cfg := a.GetTeamConfig(teamID)
	if cfg == nil {
		return nil
	}

	switch cfg.Strategy {
	case models.AssignmentStrategyRoundRobin:
		return a.assignRoundRobin(teamID, orgID, cfg.MemberIDs, excludeAgentIDs)
	case models.AssignmentStrategyLoadBalanced:
		return a.assignLoadBalanced(orgID, cfg.MemberIDs, excludeAgentIDs, loadCounter)
	case models.AssignmentStrategyManual:
		return nil
	default:
		return a.assignRoundRobin(teamID, orgID, cfg.MemberIDs, excludeAgentIDs)
	}
}

// GetAvailableAgents returns the user IDs of available agents in the team,
// excluding the given IDs. Used for broadcast fallback after rotation exhausts
// individual agents.
func (a *Assigner) GetAvailableAgents(teamID, orgID uuid.UUID, excludeAgentIDs []uuid.UUID) []uuid.UUID {
	cfg := a.GetTeamConfig(teamID)
	if cfg == nil {
		return nil
	}

	return a.filterAvailable(orgID, cfg.MemberIDs, excludeAgentIDs)
}

// assignRoundRobin selects the available agent with the oldest last_assigned_at
// from the cached member list and updates their timestamp.
//
// Selection and the timestamp update run in one transaction behind a per-team
// advisory lock. Without it two simultaneous assignments both read the same
// "oldest" member before either wrote last_assigned_at, and the same agent
// received both chats. The lock is transaction-scoped (released on commit or
// rollback) and only serializes assignments within one team.
func (a *Assigner) assignRoundRobin(teamID, orgID uuid.UUID, memberIDs []uuid.UUID, excludeAgentIDs []uuid.UUID) *uuid.UUID {
	var selected *uuid.UUID
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "team_rr:"+teamID.String()).Error; err != nil {
			return err
		}

		// Evaluated under the lock so the answer cannot go stale before use.
		available := a.filterAvailable(orgID, memberIDs, excludeAgentIDs)
		if len(available) == 0 {
			a.log.Debug("No available agents for round-robin", "team_id", teamID)
			return nil
		}

		// Among available agents, pick the one with oldest last_assigned_at
		var members []models.TeamMember
		if err := tx.
			Where("team_id = ? AND user_id IN ?", teamID, available).
			Order("last_assigned_at ASC NULLS FIRST, user_id ASC").
			Limit(1).
			Find(&members).Error; err != nil {
			return err
		}
		if len(members) == 0 {
			a.log.Debug("No team members found for round-robin", "team_id", teamID)
			return nil
		}

		if err := tx.Model(&members[0]).Update("last_assigned_at", time.Now()).Error; err != nil {
			return err
		}
		id := members[0].UserID
		selected = &id
		return nil
	})
	if err != nil {
		a.log.Error("Round-robin assignment failed", "error", err, "team_id", teamID)
		return nil
	}
	if selected != nil {
		a.log.Debug("Round-robin assigned to agent", "team_id", teamID, "user_id", *selected)
	}
	return selected
}

// assignLoadBalanced selects the available agent with the fewest active items
// as counted by the provided LoadCounter.
func (a *Assigner) assignLoadBalanced(orgID uuid.UUID, memberIDs []uuid.UUID, excludeAgentIDs []uuid.UUID, loadCounter LoadCounter) *uuid.UUID {
	available := a.filterAvailable(orgID, memberIDs, excludeAgentIDs)
	if len(available) == 0 {
		a.log.Debug("No available agents for load-balanced")
		return nil
	}

	loadMap := loadCounter(a.db, orgID, available)

	var lowestUserID *uuid.UUID
	var lowestCount int64 = -1
	for _, uid := range available {
		count := loadMap[uid]
		if lowestCount < 0 || count < lowestCount {
			lowestCount = count
			id := uid
			lowestUserID = &id
		}
	}

	if lowestUserID != nil {
		a.log.Debug("Load-balanced assigned to agent", "user_id", *lowestUserID, "current_load", lowestCount)
	}
	return lowestUserID
}

// filterAvailable returns user IDs from memberIDs that are eligible (see
// IsAgentEligible: active, available and connected) and not in the exclude list.
func (a *Assigner) filterAvailable(orgID uuid.UUID, memberIDs []uuid.UUID, excludeAgentIDs []uuid.UUID) []uuid.UUID {
	if len(memberIDs) == 0 {
		return nil
	}

	// Build exclude set
	excludeSet := make(map[uuid.UUID]bool, len(excludeAgentIDs))
	for _, id := range excludeAgentIDs {
		excludeSet[id] = true
	}

	// Filter out excluded agents before querying
	candidates := make([]uuid.UUID, 0, len(memberIDs))
	for _, id := range memberIDs {
		if !excludeSet[id] {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Query DB for availability (this changes frequently, not cached)
	var availableIDs []uuid.UUID
	a.db.Model(&models.User{}).
		Select("id").
		Where("id IN ? AND is_available = ? AND is_active = ?", candidates, true, true).
		Pluck("id", &availableIDs)

	if a.presence == nil {
		return availableIDs
	}
	connected := availableIDs[:0:0]
	for _, id := range availableIDs {
		if a.presence(orgID, id) {
			connected = append(connected, id)
		}
	}
	return connected
}

// ResolvePerAgentTimeout returns the per-agent timeout in seconds using the
// resolution order: team (if > 0) → global default.
func ResolvePerAgentTimeout(teamTimeout int, _ int, globalDefault int) int {
	if teamTimeout > 0 {
		return teamTimeout
	}
	if globalDefault > 0 {
		return globalDefault
	}
	return 15 // fallback default
}

// InvalidateTeamCache removes the cached team config for the given team ID.
func (a *Assigner) InvalidateTeamCache(teamID uuid.UUID) {
	ctx := context.Background()
	key := teamCachePrefix + teamID.String()
	a.redis.Del(ctx, key)
	a.log.Debug("Invalidated team assignment cache", "team_id", teamID)
}
