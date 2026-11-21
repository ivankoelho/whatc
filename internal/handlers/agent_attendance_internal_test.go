package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestSessionsFrom_ManualExpiredActiveAndMultiDevice(t *testing.T) {
	now := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	start, end := now.Add(-24*time.Hour), now
	life := 7 * 24 * time.Hour
	h := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
	ev := func(sid, e string, at time.Time) models.UserSessionEvent {
		return models.UserSessionEvent{SessionID: sid, Event: e, CreatedAt: at}
	}
	evs := []models.UserSessionEvent{
		ev("A", "login", h(10)), ev("A", "logout", h(8)), // device A: manual
		ev("B", "login", h(9)), ev("B", "refresh", h(1)), // device B: still alive, outlives A's logout
		ev("A", "logout", h(7)),                     // duplicate logout is ignored
		ev("C", "login", now.Add(-20*24*time.Hour)), // opened long before the window: not a login of this period
	}
	got, lastLogout, active := sessionsFrom(evs, start, end, now, life)
	assert.Len(t, got, 2, "C belongs to another period")
	assert.Equal(t, "manual", got[0].Ended)
	assert.Equal(t, 120, got[0].Minutes)
	assert.Equal(t, "active", got[1].Ended, "logout on one device must not end the other session")
	assert.Equal(t, 1, active)
	assert.Equal(t, h(8), *lastLogout, "last logout is the first (deduplicated) one")

	// a session nobody refreshed for longer than the token life has expired
	exp, _, active := sessionsFrom([]models.UserSessionEvent{ev("D", "login", h(5))}, start, end, now, 2*time.Hour)
	assert.Equal(t, "expired", exp[0].Ended)
	assert.Equal(t, h(5).Add(2*time.Hour), *exp[0].EndAt)
	assert.Zero(t, active)
}

func TestPresenceSummary_AwayConnectedDisconnectedAndOpenAway(t *testing.T) {
	now := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	start := now.Add(-10 * time.Hour)
	at := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
	p := func(s models.AgentState, from time.Time, to *time.Time) models.AgentPresenceLog {
		return models.AgentPresenceLog{State: s, StartedAt: from, EndedAt: to}
	}
	e := func(t time.Time) *time.Time { return &t }
	logs := []models.AgentPresenceLog{
		p(models.AgentStateOffline, at(12), e(at(9))), // before the window start: clipped, drop is outside
		p(models.AgentStateOnline, at(9), e(at(6))),   // 3h online
		p(models.AgentStateAway, at(6), e(at(5))),     // 1h away, returned
		p(models.AgentStateOnline, at(5), e(at(2))),   // 3h
		p(models.AgentStateOffline, at(2), e(at(1))),  // a disconnection inside the window
		p(models.AgentStateAway, at(1), nil),          // away with no return yet
	}
	conn, awayMin, awayN, disc, open, away := presenceSummary(logs, start, now, now)
	assert.Equal(t, 3*60+60+3*60+60, conn, "online + away; clipped to the window")
	assert.Equal(t, 120, awayMin)
	assert.Equal(t, 2, awayN)
	assert.Equal(t, 1, disc)
	assert.True(t, open, "away without a recorded return is flagged")
	assert.Len(t, away, 2)
}
