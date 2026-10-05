package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stuckConfirmation proposes through the AI, then "crashes" right after the customer's tap was
// consumed: the row is confirmed, nothing was executed, and the confirmation is `age` old.
func (e *confirmEnv) stuckConfirmation(t *testing.T, age time.Duration) models.AIToolConfirmation {
	t.Helper()
	_, buttons, err := e.app.GenerateAIReplyForTest(e.settings, e.session, "quero uma pessoa")
	require.NoError(t, err)
	require.NotEmpty(t, buttonID(buttons, "aitc:"))
	conf := e.confirmations(t)[0]
	at := time.Now().Add(-age)
	require.NoError(t, e.app.DB.Model(&models.AIToolConfirmation{}).Where("id = ?", conf.ID).Updates(map[string]any{
		"status": models.AIConfirmationConfirmed, "confirmed_at": at, "confirmed_by_contact_id": e.contact.ID, "confirm_wamid": "wamid.first",
	}).Error)
	return conf
}

func TestAIToolReconciler_ClosesAConfirmationACrashLeftAndTellsTheCustomer(t *testing.T) {
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{"reason":"x"}`), textReply("ok"))
	e.app.Config.AITools.ReconcileMinAgeSeconds = intp(10)
	conf := e.stuckConfirmation(t, 30*time.Second)

	rec := handlers.NewAIToolReconciler(e.app)
	assert.Equal(t, 1, rec.Sweep(context.Background()))

	got := e.confirmations(t)[0]
	assert.Equal(t, conf.ID, got.ID)
	assert.Equal(t, models.AIConfirmationExecuted, got.Status)
	rows := e.transfers(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.TransferSourceAIConfirmed, rows[0].Source)
	assert.Contains(t, e.sentToCustomer(t), "Pronto! Você entrou na fila de atendimento. Um atendente vai responder em breve.")

	// a second sweep finds nothing and changes nothing
	assert.Zero(t, rec.Sweep(context.Background()))
	assert.Len(t, e.transfers(t), 1)
}

func TestAIToolReconciler_NeverTouchesWhatIsTooRecentOrTooOldOrNoLongerAuthorized(t *testing.T) {
	// too recent: the normal path may still be running
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	e.app.Config.AITools.ReconcileMinAgeSeconds = intp(10)
	e.stuckConfirmation(t, 2*time.Second)
	assert.Zero(t, handlers.NewAIToolReconciler(e.app).Sweep(context.Background()))
	assert.Empty(t, e.transfers(t))
	assert.Equal(t, models.AIConfirmationConfirmed, e.confirmations(t)[0].Status)

	// older than the confirmation validity: the customer's authorization is stale
	e2 := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	e2.app.Config.AITools.ReconcileMinAgeSeconds = intp(10)
	e2.stuckConfirmation(t, 11*time.Minute)
	assert.Equal(t, 1, handlers.NewAIToolReconciler(e2.app).Sweep(context.Background()))
	c := e2.confirmations(t)[0]
	assert.Equal(t, models.AIConfirmationFailed, c.Status)
	assert.Equal(t, "reconcile_stale", c.ErrorKind)
	assert.Empty(t, e2.transfers(t))

	// the write switch was turned off after the tap: the re-authorization refuses, even in recovery
	e3 := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	e3.app.Config.AITools.ReconcileMinAgeSeconds = intp(10)
	e3.stuckConfirmation(t, 30*time.Second)
	e3.app.Config.AITools.WriteEnabled = false
	assert.Equal(t, 1, handlers.NewAIToolReconciler(e3.app).Sweep(context.Background()), "it also runs with the switches off, to close what is open")
	c = e3.confirmations(t)[0]
	assert.Equal(t, models.AIConfirmationDenied, c.Status)
	assert.Equal(t, "write_disabled", c.DenialReason)
	assert.Empty(t, e3.transfers(t))
}

func TestAIToolReconciler_RecordsATransferTheCrashedRunAlreadyCreated(t *testing.T) {
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	e.app.Config.AITools.ReconcileMinAgeSeconds = intp(10)
	conf := e.stuckConfirmation(t, 30*time.Second)

	// the crashed run had created the transfer before dying
	res, err := e.app.TransferToQueue(context.Background(), e.account, e.contact, models.TransferSourceAIConfirmed, "earlier")
	require.NoError(t, err)
	require.Equal(t, handlers.TransferCreated, res.Outcome)

	assert.Equal(t, 1, handlers.NewAIToolReconciler(e.app).Sweep(context.Background()))
	assert.Len(t, e.transfers(t), 1, "no second transfer")
	got := e.confirmations(t)[0]
	assert.Equal(t, conf.ID, got.ID)
	assert.Equal(t, models.AIConfirmationExecuted, got.Status)
	require.NotNil(t, got.TransferID)
	assert.Equal(t, res.TransferID, *got.TransferID)
}

func TestAIToolReconciler_IntervalZeroIsOffAndWithoutTheSecretNothingRuns(t *testing.T) {
	e := newConfirmEnv(t, textReply("ok"))
	assert.Equal(t, 30*time.Second, handlers.NewAIToolReconciler(e.app).Interval(), "default")
	e.app.Config.AITools.ReconcileIntervalSeconds = intp(0)
	rec := handlers.NewAIToolReconciler(e.app)
	assert.Zero(t, rec.Interval())

	done := make(chan struct{})
	go func() { rec.Start(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start must return at once when the interval is 0")
	}

	e.app.Config.App.EncryptionKey = ""
	assert.Zero(t, handlers.NewAIToolReconciler(e.app).Sweep(context.Background()))
}

func TestAIToolReconciler_TheTickerClosesWhatIsStuckAndStopsOnStop(t *testing.T) {
	e := newConfirmEnv(t, callWith("call_1", "request_agent_transfer", `{}`), textReply("ok"))
	e.app.Config.AITools.ReconcileMinAgeSeconds = intp(10)
	e.app.Config.AITools.ReconcileIntervalSeconds = intp(1)
	e.stuckConfirmation(t, 30*time.Second)

	rec := handlers.NewAIToolReconciler(e.app)
	done := make(chan struct{})
	go func() { rec.Start(context.Background()); close(done) }()
	require.Eventually(t, func() bool {
		return e.confirmations(t)[0].Status == models.AIConfirmationExecuted
	}, 6*time.Second, 100*time.Millisecond)
	rec.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop must end the loop")
	}
	assert.Len(t, e.transfers(t), 1)
}

func intp(v int) *int { return &v }
