package handlers_test

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// metricsPhoneSeq makes every contact of the metrics tests unique.
var metricsPhoneSeq atomic.Int64

// metricsEnv is an organization with a viewer allowed to see every agent.
type metricsEnv struct {
	app    *handlers.App
	org    *models.Organization
	viewer *models.User
}

func newMetricsEnv(t *testing.T) metricsEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleExact(t, app.DB, org.ID, "Analytics Viewer", false, false, getAnalyticsPermissions(t, app))
	viewer := testutil.CreateTestUser(t, app.DB, org.ID,
		testutil.WithEmail(testutil.UniqueEmail("viewer")),
		testutil.WithPassword("password"),
		testutil.WithRoleID(&role.ID),
	)
	return metricsEnv{app: app, org: org, viewer: viewer}
}

// agent creates a user who is an agent of a team (so the all-agents listing sees them).
func (e metricsEnv) agent(t *testing.T) *models.User {
	t.Helper()
	u := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithEmail(testutil.UniqueEmail("agent")))
	createTestTeamWithAgent(t, e.app, e.org.ID, u.ID)
	return u
}

// analytics calls the endpoint over an explicit, wide period (yesterday..tomorrow).
func (e metricsEnv) analytics(t *testing.T) handlers.AgentAnalyticsResponse {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.viewer.ID)
	testutil.SetQueryParam(req, "from", time.Now().AddDate(0, 0, -2).Format("2006-01-02"))
	testutil.SetQueryParam(req, "to", time.Now().AddDate(0, 0, 1).Format("2006-01-02"))
	require.NoError(t, e.app.GetAgentAnalytics(req))
	var resp struct {
		Data handlers.AgentAnalyticsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func (e metricsEnv) statsOf(t *testing.T, agentID uuid.UUID) handlers.AgentPerformanceStats {
	t.Helper()
	for _, s := range e.analytics(t).AgentStats {
		if s.AgentID == agentID.String() {
			return s
		}
	}
	t.Fatalf("agent %s not in the listing", agentID)
	return handlers.AgentPerformanceStats{}
}

// message creates a message and, for a person's reply, stamps the sender.
func (e metricsEnv) message(t *testing.T, contactID uuid.UUID, dir models.Direction, at time.Time, sentBy *uuid.UUID) {
	t.Helper()
	m := createTestMessage(t, e.app, e.org.ID, contactID, dir, at)
	if sentBy != nil {
		require.NoError(t, e.app.DB.Model(&models.Message{}).Where("id = ?", m.ID).Update("sent_by_user_id", *sentBy).Error)
	}
}

// transfer creates a transfer for a fresh contact.
func (e metricsEnv) transfer(t *testing.T, agentID *uuid.UUID, status models.TransferStatus, transferredAt time.Time, resumedAt *time.Time) (*models.AgentTransfer, *models.Contact) {
	t.Helper()
	// CreateTestContact draws 4 hex digits for the phone: with a few hundred
	// contacts in one organization it collides, so the number is sequential here.
	contact := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID,
		testutil.WithPhoneNumber(fmt.Sprintf("+5511%09d", metricsPhoneSeq.Add(1))))
	return createTestAgentTransfer(t, e.app, e.org.ID, contact.ID, agentID, status, models.TransferSourceManual, transferredAt, resumedAt), contact
}

// The queue time is first pickup minus queue entry. updated_at moves for any
// edit and must not leak into the metric; attendances never picked up are out.
func TestAgentAnalytics_QueueTimeUsesFirstPickup(t *testing.T) {
	e := newMetricsEnv(t)
	agent := e.agent(t)
	now := time.Now()

	tr, _ := e.transfer(t, &agent.ID, models.TransferStatusActive, now.Add(-60*time.Minute), nil)
	pickedUp := now.Add(-50 * time.Minute) // waited 10 min
	require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).
		Updates(map[string]any{"picked_up_at": pickedUp, "updated_at": now}).Error) // updated_at: 60 min later
	e.transfer(t, nil, models.TransferStatusActive, now.Add(-30*time.Minute), nil) // still in the queue

	got := e.analytics(t).Summary.AvgQueueTimeMins
	assert.InDelta(t, 10, got, 0.1, "queue time = picked_up_at - transferred_at, ignoring updated_at and waiting items")
}

// Messages are attributed to who sent them, not to anyone who ever had a
// transfer with that contact.
func TestAgentAnalytics_MessagesSentAreAttributedToTheSender(t *testing.T) {
	e := newMetricsEnv(t)
	ana, bia := e.agent(t), e.agent(t)
	now := time.Now()

	_, contact := e.transfer(t, &ana.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
	e.message(t, contact.ID, models.DirectionOutgoing, now.Add(-50*time.Minute), &ana.ID)
	e.message(t, contact.ID, models.DirectionOutgoing, now.Add(-40*time.Minute), &ana.ID)
	e.message(t, contact.ID, models.DirectionOutgoing, now.Add(-30*time.Minute), &bia.ID) // someone else, same contact
	e.message(t, contact.ID, models.DirectionOutgoing, now.Add(-20*time.Minute), nil)     // bot
	e.message(t, contact.ID, models.DirectionIncoming, now.Add(-10*time.Minute), nil)     // customer

	assert.EqualValues(t, 2, e.statsOf(t, ana.ID).MessagesSent)
	assert.EqualValues(t, 1, e.statsOf(t, bia.ID).MessagesSent)
}

// The resolution is credited to whoever owned the attendance when it was
// closed. The closer (resumed_by) can be a supervisor and is only a fallback
// for an attendance closed with no owner; an auto-closed (expired) attendance
// is nobody's resolution.
func TestAgentAnalytics_ResolvedIsCreditedToTheOwnerAtClose(t *testing.T) {
	e := newMetricsEnv(t)
	ana, bia := e.agent(t), e.agent(t)
	now := time.Now()
	resumed := now.Add(-10 * time.Minute)
	closedBy := func(tr *models.AgentTransfer, closer uuid.UUID) {
		t.Helper()
		require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).Update("resumed_by", closer).Error)
	}

	// Bia owns it; a supervisor (not an agent of any team) closes it.
	tr, _ := e.transfer(t, &bia.ID, models.TransferStatusResumed, now.Add(-2*time.Hour), &resumed)
	closedBy(tr, e.viewer.ID)
	// Ana owns it and closes it herself.
	tr, _ = e.transfer(t, &ana.ID, models.TransferStatusResumed, now.Add(-2*time.Hour), &resumed)
	closedBy(tr, ana.ID)
	// Reassigned to Bia, who owns it at the closing and closes it.
	tr, _ = e.transfer(t, &bia.ID, models.TransferStatusResumed, now.Add(-2*time.Hour), &resumed)
	closedBy(tr, bia.ID)
	// Closed from the queue, with no owner: the closer is the fallback.
	tr, _ = e.transfer(t, nil, models.TransferStatusResumed, now.Add(-2*time.Hour), &resumed)
	closedBy(tr, ana.ID)
	// Closed by the SLA: nobody resolved it, whoever owned it.
	e.transfer(t, &ana.ID, models.TransferStatusExpired, now.Add(-2*time.Hour), &resumed)

	assert.EqualValues(t, 2, e.statsOf(t, ana.ID).TransfersHandled, "own close + fallback for the unowned one")
	assert.EqualValues(t, 2, e.statsOf(t, bia.ID).TransfersHandled, "the supervisor's close and the reassigned one stay with the owner")
}

// Active attendances are split by who they wait for; "active" alone says
// nothing about whether the agent or the customer owes a reply.
func TestAgentAnalytics_ActiveAttendancesAreSplitByWhoIsWaiting(t *testing.T) {
	e := newMetricsEnv(t)
	agent := e.agent(t)
	now := time.Now()

	// Customer wrote last: waiting for the agent.
	_, c1 := e.transfer(t, &agent.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
	e.message(t, c1.ID, models.DirectionOutgoing, now.Add(-50*time.Minute), &agent.ID)
	e.message(t, c1.ID, models.DirectionIncoming, now.Add(-5*time.Minute), nil)
	// Agent wrote last: waiting for the customer.
	_, c2 := e.transfer(t, &agent.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
	e.message(t, c2.ID, models.DirectionIncoming, now.Add(-50*time.Minute), nil)
	e.message(t, c2.ID, models.DirectionOutgoing, now.Add(-5*time.Minute), &agent.ID)
	// Only a bot message since the attendance started: waiting for nobody yet.
	_, c3 := e.transfer(t, &agent.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
	e.message(t, c3.ID, models.DirectionOutgoing, now.Add(-5*time.Minute), nil)
	// The customer wrote BEFORE the attendance existed (that is the message that asked
	// for a human) and nobody has replied: they are waiting, so a message sent before
	// the transfer must count.
	_, c4 := e.transfer(t, &agent.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
	e.message(t, c4.ID, models.DirectionIncoming, now.Add(-3*time.Hour), nil)

	s := e.statsOf(t, agent.ID)
	assert.EqualValues(t, 4, s.ActiveTransfers)
	assert.EqualValues(t, 2, s.AwaitingAgent, "c1 (customer wrote last) and c4 (asked for a human, never answered)")
	assert.EqualValues(t, 1, s.AwaitingCustomer, "c2: the agent replied last; c3 has only a bot message, so nobody waits")
}

// SLA at risk means: escalated, not breached AND the customer is waiting now.
// The escalation level is never reset when the agent answers, so it is not
// enough by itself; the breach is a historical record and survives the reply.
func TestAgentAnalytics_SLARiskAndBreach(t *testing.T) {
	run := func(t *testing.T, level int, breached, customerWaiting bool) handlers.AgentPerformanceStats {
		t.Helper()
		e := newMetricsEnv(t)
		agent := e.agent(t)
		now := time.Now()
		tr, c := e.transfer(t, &agent.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
		require.NoError(t, e.app.DB.Model(&models.AgentTransfer{}).Where("id = ?", tr.ID).
			Updates(map[string]any{"escalation_level": level, "sla_breached": breached}).Error)
		if customerWaiting {
			e.message(t, c.ID, models.DirectionOutgoing, now.Add(-50*time.Minute), &agent.ID)
			e.message(t, c.ID, models.DirectionIncoming, now.Add(-5*time.Minute), nil)
		} else {
			e.message(t, c.ID, models.DirectionIncoming, now.Add(-50*time.Minute), nil)
			e.message(t, c.ID, models.DirectionOutgoing, now.Add(-5*time.Minute), &agent.ID)
		}
		return e.statsOf(t, agent.ID)
	}

	t.Run("escalated, customer waiting: at risk", func(t *testing.T) {
		s := run(t, 1, false, true)
		assert.EqualValues(t, 1, s.SLAAtRisk)
		assert.EqualValues(t, 0, s.SLABreached)
	})
	t.Run("escalated, agent already answered: not at risk", func(t *testing.T) {
		s := run(t, 1, false, false)
		assert.EqualValues(t, 0, s.SLAAtRisk)
		assert.EqualValues(t, 0, s.SLABreached)
	})
	t.Run("not escalated, customer waiting: not at risk", func(t *testing.T) {
		s := run(t, 0, false, true)
		assert.EqualValues(t, 0, s.SLAAtRisk)
	})
	t.Run("breached stays recorded after the agent answers and is not also at risk", func(t *testing.T) {
		waiting := run(t, 2, true, true)
		assert.EqualValues(t, 1, waiting.SLABreached)
		assert.EqualValues(t, 0, waiting.SLAAtRisk, "a breached attendance is counted as breached, not as at risk")

		answered := run(t, 2, true, false)
		assert.EqualValues(t, 1, answered.SLABreached, "the breach is historical")
		assert.EqualValues(t, 0, answered.SLAAtRisk)
	})
}

// The number of queries must not grow with the number of agents.
func TestAgentAnalytics_QueryCountDoesNotGrowWithAgents(t *testing.T) {
	e := newMetricsEnv(t)
	now := time.Now()

	var queries atomic.Int64
	count := func(*gorm.DB) { queries.Add(1) }
	require.NoError(t, e.app.DB.Callback().Query().Before("gorm:query").Register("count_queries_q", count))
	require.NoError(t, e.app.DB.Callback().Row().Before("gorm:row").Register("count_queries_r", count))
	t.Cleanup(func() {
		_ = e.app.DB.Callback().Query().Remove("count_queries_q")
		_ = e.app.DB.Callback().Row().Remove("count_queries_r")
	})

	addAgent := func() {
		a := e.agent(t)
		_, c := e.transfer(t, &a.ID, models.TransferStatusActive, now.Add(-time.Hour), nil)
		e.message(t, c.ID, models.DirectionIncoming, now.Add(-time.Minute), nil)
		e.message(t, c.ID, models.DirectionOutgoing, now.Add(-30*time.Minute), &a.ID)
	}
	measure := func() int64 {
		queries.Store(0)
		e.analytics(t)
		return queries.Load()
	}

	// The whole endpoint is measured (auth, summary, trend and per-agent stats).
	// Agents are added cumulatively and the count taken at 5, 50 and 200.
	sizes := []int{5, 50, 200}
	have := 0
	var counts []int64
	for _, size := range sizes {
		for ; have < size; have++ {
			addAgent()
		}
		e.analytics(t) // warm the permission cache so only the metric queries are compared
		counts = append(counts, measure())
		t.Logf("%3d agents -> %d queries", size, counts[len(counts)-1])
	}

	for i := 1; i < len(counts); i++ {
		assert.Equal(t, counts[0], counts[i],
			"queries must not depend on the number of agents (%d agents: %d, %d agents: %d)",
			sizes[0], counts[0], sizes[i], counts[i])
	}
}
