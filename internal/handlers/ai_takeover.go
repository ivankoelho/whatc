package handlers

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// A human taking over a conversation must win over any AI reply not yet sent, with no window
// between "nobody has taken over" and "the AI message goes out". Checking just before sending is
// not enough: the attendance can be confirmed between the check and the send. So both sides
// serialize on one Postgres advisory lock per contact:
//
//   - the AI send runs inside a transaction that takes the lock, re-checks for an active
//     AgentTransfer, and only then sends, holding the lock until the send is over;
//   - every creation of an active AgentTransfer (createTransferRow, the single insert point)
//     takes the same lock before the INSERT.
//
// Either the attendance is confirmed before the AI's check (the AI sends nothing) or the AI's
// send finishes before the attendance is confirmed. No AI message leaves after the attendance
// exists. The lock covers only the send: never the generation, the extraction or the handoff's
// own transfer, which would deadlock.

// aiSendLockTimeout is how long the AI waits for the contact lock. Without it the reply is
// discarded (fail safe): silence is better than talking over a human. A variable only so a test
// can shorten it.
var aiSendLockTimeout = 5 * time.Second

// Each in-flight AI send holds one database connection (the transaction that owns the lock) while
// the send itself needs more connections to save the message. If every connection of the pool were
// held by sends waiting for another, the whole application would freeze, and Postgres cannot see
// that deadlock. So the number of concurrent AI sends is capped well below the pool size; a send
// that cannot get a slot within aiSendLockTimeout is discarded like any other guard failure.
const aiSendMaxConcurrent = 6

var (
	aiSendSlotsOnce sync.Once
	aiSendSlots     chan struct{}
)

// aiSendSlotsFor returns the semaphore, sized once: aiSendMaxConcurrent, or a quarter of the
// connection pool when the pool is small.
func aiSendSlotsFor(db *gorm.DB) chan struct{} {
	aiSendSlotsOnce.Do(func() {
		n := aiSendMaxConcurrent
		if sqlDB, err := db.DB(); err == nil {
			if max := sqlDB.Stats().MaxOpenConnections; max > 0 && max/4 < n {
				n = max / 4
			}
		}
		if n < 1 {
			n = 1
		}
		aiSendSlots = make(chan struct{}, n)
	})
	return aiSendSlots
}

// createTransferLockTimeout bounds how long an attendant's action may wait for an AI send that is
// in flight (milliseconds). It is far above aiSendMaxHold; reaching it means something is stuck,
// and the action then fails instead of hanging forever.
const createTransferLockTimeout = 30 * time.Second

const (
	// aiSendMaxHold bounds how long a send may keep the lock, so a stuck WhatsApp call cannot
	// freeze the attendant's takeover.
	aiSendMaxHold = 15 * time.Second

	// Feature of the AIUsageLog row that records a reply that was generated but not sent.
	aiFeatureReplyDiscarded = "ai_reply_discarded"
	aiDiscardHumanTookOver  = "discarded_human"
	aiDiscardLockTimeout    = "discarded_lock_timeout"
	aiDiscardBusy           = "discarded_busy"
)

// contactLockKey is the advisory-lock key of one contact of one organization.
func contactLockKey(orgID, contactID uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("ai-human-takeover:"))
	_, _ = h.Write(orgID[:])
	_, _ = h.Write(contactID[:])
	return int64(h.Sum64())
}

// lockContact takes the contact's transaction-scoped advisory lock inside tx. It blocks until
// the lock is free, or, if the transaction set a lock_timeout, until that expires.
func lockContact(tx *gorm.DB, orgID, contactID uuid.UUID) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", contactLockKey(orgID, contactID)).Error
}

// isLockTimeout reports whether err is Postgres' lock_timeout (SQLSTATE 55P03).
func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// humanTookOver reports whether a human owns the conversation: an active AgentTransfer exists.
// Contact.AssignedUserID is the account owner (carteira), not a takeover, and does not count.
func (a *App) humanTookOver(orgID, contactID uuid.UUID) bool {
	return a.hasActiveAgentTransfer(orgID, contactID)
}

// sendAIReply sends an AI reply. A reply written by an AI agent goes through the contact lock;
// any other AI reply is sent exactly as before. It returns sent=false, err=nil when the reply was
// discarded because a human took over (or the lock could not be had in time).
func (a *App) sendAIReply(ctx context.Context, byAgent bool, orgID, contactID uuid.UUID, send func(context.Context) error) (bool, error) {
	if !byAgent {
		err := send(ctx)
		return err == nil, err
	}
	return a.sendAIReplyIfNoHuman(ctx, orgID, contactID, send)
}

// sendAIReplyIfNoHuman runs send under the contact lock after re-checking that no human has taken
// over. See the comment at the top of this file.
func (a *App) sendAIReplyIfNoHuman(ctx context.Context, orgID, contactID uuid.UUID, send func(context.Context) error) (bool, error) {
	slots := aiSendSlotsFor(a.DB)
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-time.After(aiSendLockTimeout):
		a.recordAIDiscard(orgID, contactID, aiDiscardBusy)
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}

	sent := false
	var sendErr error
	err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SET takes no bind parameters; the value is a constant of this package.
		if err := tx.Exec(fmt.Sprintf("SET LOCAL lock_timeout = %d", aiSendLockTimeout/time.Millisecond)).Error; err != nil {
			return err
		}
		if err := lockContact(tx, orgID, contactID); err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&models.AgentTransfer{}).
			Where("organization_id = ? AND contact_id = ? AND status = ?", orgID, contactID, models.TransferStatusActive).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return errAIHumanTookOver
		}
		sendCtx, cancel := context.WithTimeout(ctx, aiSendMaxHold)
		defer cancel()
		if sendErr = send(sendCtx); sendErr == nil {
			sent = true
		}
		return nil // a failed send is reported, but there is nothing to roll back
	})
	switch {
	case errors.Is(err, errAIHumanTookOver):
		a.recordAIDiscard(orgID, contactID, aiDiscardHumanTookOver)
		return false, nil
	case isLockTimeout(err):
		a.recordAIDiscard(orgID, contactID, aiDiscardLockTimeout)
		return false, nil
	case err != nil && sent:
		// The message already went out and only the (empty) transaction failed to commit: it was
		// sent, and saying otherwise would make the caller report a failure that did not happen.
		a.Log.Error("AI reply sent, but the guard transaction did not commit", "error", err, "contact_id", contactID)
		return true, sendErr
	case err != nil:
		return false, err
	}
	return sent, sendErr
}

var errAIHumanTookOver = errors.New("a human took over the conversation")

// recordAIDiscard leaves a trace that an AI reply was generated and not sent. It carries zero
// tokens and its own feature, so it never counts as consumption.
func (a *App) recordAIDiscard(orgID, contactID uuid.UUID, kind string) {
	entry := models.AIUsageLog{
		OrganizationID: orgID,
		ContactID:      &contactID,
		Feature:        aiFeatureReplyDiscarded,
		Success:        false,
		ErrorKind:      kind,
	}
	if err := a.DB.Create(&entry).Error; err != nil {
		a.Log.Error("Failed to record a discarded AI reply", "error", err, "kind", kind)
	}
	a.Log.Info("AI reply discarded", "reason", kind, "contact_id", contactID)
}
