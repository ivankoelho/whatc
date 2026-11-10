package handlers

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"gorm.io/gorm"
)

// What an AI answer's send did, as far as it can be known, and the fence that keeps a worker that lost
// its claim from sending.
//
// Two facts about the outside world shape this file:
//
//   - SendOutgoingMessage does not return a provider failure: it stores it on the message row and
//     returns the message. The only way to learn the outcome is its OnResult callback. So an answer is
//     "sent" only when OnResult says so; a failed or unknown send is never recorded as an answer.
//   - A database transaction cannot span the call to Meta. A crash after Meta accepted a message and
//     before the database heard of it is not detectable afterwards. So the intent to send is persisted
//     BEFORE the call (aiPendingReply.SendStartedAt, set under the contact lock, only while this worker
//     still owns the row). Whoever finds a row with that marker after a crash knows the message may
//     have left, and never runs it again.

// errAIFenceLost: the row was taken from this worker (its lease ended and another claimed it, or it was
// closed). Nothing is sent.
var errAIFenceLost = errors.New("the AI reply no longer belongs to this worker")

// aiSendFence ties a send to one claim of a pending reply: the row id and the attempt that claim got.
type aiSendFence struct {
	rowID   uuid.UUID
	attempt int
}

// aiSendTrack watches the sends of one AI turn.
type aiSendTrack struct {
	app   *App
	fence *aiSendFence // nil for an answer given at once (no pending row)

	mu        sync.Mutex
	delivered bool  // the provider accepted at least one message
	err       error // the first provider (or fence) error
	fenced    bool
}

type aiSendTrackKey struct{}

func withAISendTrack(ctx context.Context, t *aiSendTrack) context.Context {
	return context.WithValue(ctx, aiSendTrackKey{}, t)
}

// chatbotSendOptions is ChatbotSendOptions plus, when the context carries a track, the callbacks that
// report the outcome and enforce the fence. Without a track it is exactly ChatbotSendOptions.
func chatbotSendOptions(ctx context.Context) MessageSendOptions {
	opts := ChatbotSendOptions()
	if t, _ := ctx.Value(aiSendTrackKey{}).(*aiSendTrack); t != nil {
		opts.OnResult = t.record
		if t.fence != nil {
			opts.BeforeSend = t.beforeSend
		}
	}
	return opts
}

func (t *aiSendTrack) record(_ string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		t.delivered = true
	} else if t.err == nil {
		t.err = err
	}
}

// beforeSend is the point of no return of the answer's send. It runs inside the contact lock, after the
// message row exists and immediately before WhatsApp is called: it persists "a send started" and, in the
// same statement, proves this worker still owns the row. If it does not, nothing is sent.
func (t *aiSendTrack) beforeSend(ctx context.Context) (context.Context, context.CancelFunc, error) {
	res := t.app.DB.Model(&models.AIPendingReply{}).
		Where("id = ? AND status = ? AND attempts = ?", t.fence.rowID, models.AIPendingStatusProcessing, t.fence.attempt).
		Update("send_started_at", gorm.Expr("COALESCE(send_started_at, ?)", time.Now()))
	if res.Error != nil {
		return nil, nil, res.Error // ownership could not be proved: do not send
	}
	if res.RowsAffected == 0 {
		t.mu.Lock()
		t.fenced = true
		t.mu.Unlock()
		t.app.Log.Warn("AI reply not sent: the claim was lost", "row_id", t.fence.rowID, "attempt", t.fence.attempt)
		return nil, nil, errAIFenceLost
	}
	return ctx, func() {}, nil
}

// failure says how the sends of the turn ended when they did not deliver, or "" when they did (or when
// nothing was sent). A provider refusal that proves the message was not accepted is a plain failure; a
// network failure, a timeout or any answer that proves nothing is an unknown outcome: the message may
// have reached the customer, so the audit says so and nothing is ever resent.
func (t *aiSendTrack) failure() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.fenced:
		return aiOutcomeLostLease
	case t.delivered || t.err == nil:
		return ""
	case whatsapp.NotSent(t.err):
		return aiOutcomeSendFailed
	default:
		return aiOutcomeAmbiguousSend
	}
}
