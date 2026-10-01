package handlers_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePresence is a mutable connectivity source standing in for the hub.
// Agents are connected unless a test marks them offline, so leftovers from
// other tests sharing the database are never swept by accident.
type fakePresence struct {
	mu      sync.Mutex
	offline map[uuid.UUID]bool
}

func newFakePresence() *fakePresence { return &fakePresence{offline: map[uuid.UUID]bool{}} }
func (f *fakePresence) set(id uuid.UUID, online bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offline[id] = !online
}
func (f *fakePresence) is(_, id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.offline[id]
}

func assignedTo(t *testing.T, app *handlers.App, id uuid.UUID) *uuid.UUID {
	t.Helper()
	var tr models.AgentTransfer
	require.NoError(t, app.DB.First(&tr, "id = ?", id).Error)
	return tr.AgentID
}

func TestPresenceReaper_ReleasesOnlyAfterGracePeriod(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	tr := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)

	presence := newFakePresence()
	presence.set(agent.ID, false) // not connected
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)

	t0 := time.Now()
	assert.Zero(t, reaper.Sweep(t0), "first sighting only starts the clock")
	assert.NotNil(t, assignedTo(t, app, tr.ID))

	assert.Zero(t, reaper.Sweep(t0.Add(59*time.Second)), "still inside the grace period")
	assert.NotNil(t, assignedTo(t, app, tr.ID))

	assert.Equal(t, 1, reaper.Sweep(t0.Add(61*time.Second)), "grace expired: released")
	assert.Nil(t, assignedTo(t, app, tr.ID))
}

func TestPresenceReaper_ReconnectWithinGraceKeepsAssignmentsAndRestartsClock(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	tr := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)

	presence := newFakePresence()
	presence.set(agent.ID, false)
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)

	t0 := time.Now()
	reaper.Sweep(t0) // disconnected, clock starts

	presence.set(agent.ID, true) // refresh finished
	assert.Zero(t, reaper.Sweep(t0.Add(30*time.Second)))
	assert.NotNil(t, assignedTo(t, app, tr.ID), "reconnect inside the window preserves the assignment")

	// Drops again later: the clock must restart, not inherit the old one.
	presence.set(agent.ID, false)
	assert.Zero(t, reaper.Sweep(t0.Add(70*time.Second)), "old disconnect time must not carry over")
	assert.Zero(t, reaper.Sweep(t0.Add(100*time.Second)))
	assert.NotNil(t, assignedTo(t, app, tr.ID))
	assert.Equal(t, 1, reaper.Sweep(t0.Add(131*time.Second)))
	assert.Nil(t, assignedTo(t, app, tr.ID))
}

func TestPresenceReaper_NeverTouchesConnectedAgentsOrOtherAgentsTransfers(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	gone, here := createTestAgent(t, app, org.ID), createTestAgent(t, app, org.ID)
	goneTr := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &gone.ID)
	hereTr := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &here.ID)

	presence := newFakePresence()
	presence.set(gone.ID, false)
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)

	t0 := time.Now()
	reaper.Sweep(t0)
	assert.Equal(t, 1, reaper.Sweep(t0.Add(2*time.Minute)))

	assert.Nil(t, assignedTo(t, app, goneTr.ID))
	got := assignedTo(t, app, hereTr.ID)
	require.NotNil(t, got)
	assert.Equal(t, here.ID, *got)
}

func TestPresenceReaper_SecondSweepDoesNotReturnTheSameTransferAgain(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)

	presence := newFakePresence()
	presence.set(agent.ID, false)
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)

	t0 := time.Now()
	reaper.Sweep(t0)
	assert.Equal(t, 1, reaper.Sweep(t0.Add(61*time.Second)))
	assert.Zero(t, reaper.Sweep(t0.Add(120*time.Second)), "already released: nothing left to return")
}

func TestReturnAgentTransfersToQueue_IsIdempotentUnderConcurrency(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	const transfers = 5
	for range transfers {
		createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)
	}

	var total atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 6 { // overlapping runs: sweep + toggle + another instance
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			total.Add(int32(app.ReturnAgentTransfersToQueue(agent.ID, org.ID)))
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, transfers, total.Load(), "each transfer is returned exactly once across all runs")
	var still int64
	app.DB.Model(&models.AgentTransfer{}).Where("agent_id = ?", agent.ID).Count(&still)
	assert.Zero(t, still)
}

func TestPresenceReaper_OnPresenceChangeTracksClock(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	agent := createTestAgent(t, app, org.ID)
	tr := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)

	presence := newFakePresence()
	presence.set(agent.ID, false)
	reaper := handlers.NewPresenceReaper(app, 60*time.Second, time.Second)
	reaper.SetConnectedForTest(presence.is)

	reaper.OnPresenceChange(org.ID, agent.ID, false) // hub: last tab closed
	// A sweep long after the disconnect releases without needing a prior sighting.
	assert.Equal(t, 1, reaper.Sweep(time.Now().Add(61*time.Second)))
	assert.Nil(t, assignedTo(t, app, tr.ID))

	// Reconnect event clears the clock for the next assignment.
	tr2 := createTestTransfer(t, app, org.ID, testutil.CreateTestContact(t, app.DB, org.ID).ID, account.Name, models.TransferStatusActive, &agent.ID)
	presence.set(agent.ID, false)
	reaper.OnPresenceChange(org.ID, agent.ID, false)
	presence.set(agent.ID, true)
	reaper.OnPresenceChange(org.ID, agent.ID, true)
	assert.Zero(t, reaper.Sweep(time.Now().Add(5*time.Minute)))
	assert.NotNil(t, assignedTo(t, app, tr2.ID))
}
