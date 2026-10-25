package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"gorm.io/gorm"
)

// One-time codes are sent under a claim, because sending the same code twice is
// not the same as sending an ordinary message twice and Meta offers no idempotency.
//
// The claim lives in PostgreSQL, in one transaction that (a) creates the masked
// message as pending and (b) sets bulk_message_recipients.message_id with a
// compare-and-set on `status = 'pending' AND message_id IS NULL`. Only the worker
// whose update affects a row may call Meta; the transaction is closed before the
// call, and a lost claim rolls the message back.
//
// What the database can say afterwards:
//
//	message_id NULL, status pending   nobody tried yet (or the last attempt provably did not send)
//	message_id set,  status pending   claimed: a worker is sending, or died doing it
//	status sent                       Meta confirmed (wamid recorded)
//	message_id NULL, status failed    provably NOT sent (refusal, invalid code, expired): RetryFailed may retry
//	message_id set,  status failed    delivery_uncertain: the outcome is unknown; never retried automatically
//
// Recipients of ordinary campaigns never touch any of this.

const (
	// ReasonDeliveryUncertain prefixes the error of a recipient whose outcome is unknown.
	ReasonDeliveryUncertain = "delivery_uncertain"

	// defaultMetaCall is the longest a request to Meta may last: it is the HTTP
	// client's timeout (30s) and the worker also enforces it as the call's deadline.
	defaultMetaCall = 30 * time.Second
	// claimWriteBudget is what a live worker may need around that call: the claim
	// transaction before it and the result writes after it.
	claimWriteBudget = 30 * time.Second
)

var (
	errClaimLost         = errors.New("recipient already claimed")
	errClaimNoLongerHeld = errors.New("claim no longer held")
)

// metaCallTimeout is the longest one Meta call may take (and is made to take).
func (w *Worker) metaCallTimeout() time.Duration {
	if w.WhatsApp != nil && w.WhatsApp.HTTPClient != nil && w.WhatsApp.HTTPClient.Timeout > 0 &&
		w.WhatsApp.HTTPClient.Timeout < defaultMetaCall {
		return w.WhatsApp.HTTPClient.Timeout
	}
	return defaultMetaCall
}

// claimStale is how old a claim must be before it can be called abandoned. It is
// derived, not picked: a live worker holds a claim for at most the Meta call plus
// the writes around it, and the limit is twice that, so a worker that is merely
// slow (a GC pause, a loaded database) is not mistaken for a dead one. A worker
// that is restarted loses its claim immediately, so it never needs more. Ageing is
// measured with the DATABASE clock on both ends (the claim instant is stamped with
// clock_timestamp() in the claim transaction and compared with clock_timestamp() when
// recovering), so workers whose own clocks are off cannot make a live send look stuck
// or a dead one look alive. Even if this limit is ever wrong, a late result cannot
// overwrite a recovered claim (see finishSent): the recipient stays blocked and uncertain.
func (w *Worker) claimStale() time.Duration {
	if w.ClaimStale > 0 {
		return w.ClaimStale
	}
	return 2 * (w.metaCallTimeout() + claimWriteBudget)
}

type recipientState struct {
	Status    models.MessageStatus
	MessageID *uuid.UUID
}

func (w *Worker) recipientState(id uuid.UUID) (recipientState, bool) {
	var st recipientState
	if err := w.DB.Model(&models.BulkMessageRecipient{}).Select("status", "message_id").Where("id = ?", id).Take(&st).Error; err != nil {
		return st, false
	}
	return st, true
}

// sendSecretRecipient sends the code of one recipient of an authentication campaign.
func (w *Worker) sendSecretRecipient(ctx context.Context, job *queue.RecipientJob, campaign *models.BulkMessageCampaign, account *models.WhatsAppAccount, contact *models.Contact) error {
	st, found := w.recipientState(job.RecipientID)
	if !found {
		return nil // deleted meanwhile
	}
	switch {
	case st.Status == models.MessageStatusSent || st.Status == models.MessageStatusDelivered || st.Status == models.MessageStatusRead:
		return nil // a redelivered job after a confirmed send
	case st.MessageID != nil:
		w.resolveExistingClaim(ctx, job, st) // in flight, or an attempt whose result was never recorded
		return nil
	case st.Status != models.MessageStatusPending:
		return nil // failed: only RetryFailed (which makes it pending again) starts a new attempt
	}

	var key string
	if w.Config != nil {
		key = w.Config.App.EncryptionKey
	}
	sealedParams := job.TemplateParams
	opened, oerr := templateutil.OpenParams(sealedParams, key, w.now())
	if oerr != nil || len(opened) == 0 {
		// A code that expired or is simply gone is final: the secret is dropped. A key
		// that is missing or wrong, or an envelope that does not open, is a configuration
		// or data problem: the ciphertext is KEPT so it recovers once the key is back.
		w.Log.Error("Verification code unavailable for recipient", "recipient_id", job.RecipientID, "campaign_id", job.CampaignID, "reason", otpOpenFailure(oerr))
		switch {
		case errors.Is(oerr, templateutil.ErrSealExpired), oerr == nil:
			w.failBeforeClaim(ctx, job, "otp_expired: the verification code is no longer available", true)
		default:
			w.failBeforeClaim(ctx, job, "otp_unavailable: "+otpOpenFailure(oerr)+" (the stored code was kept; fix it and retry the failed recipients)", false)
		}
		return nil
	}
	if cerr := w.otpPolicy().Check(sealedParams, w.now()); cerr != nil {
		w.Log.Warn("Verification code not sent", "reason", cerr.Error(), "recipient_id", job.RecipientID, "campaign_id", job.CampaignID)
		w.failBeforeClaim(ctx, job, cerr.Error()+": the verification code is not valid for sending", true)
		return nil
	}

	// A worker that is shutting down starts nothing new: no claim is made, the job is
	// not acknowledged and is delivered again later.
	if err := w.shutdownErr(ctx); err != nil {
		return err
	}

	// Claim: one short transaction, closed before Meta is called.
	msg := w.newSecretMessage(job, campaign, contact)
	claimErr := w.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(msg).Error; err != nil {
			return err
		}
		// The claim instant is the DATABASE's, not this worker's clock.
		if err := tx.Exec("UPDATE messages SET created_at = clock_timestamp() WHERE id = ?", msg.ID).Error; err != nil {
			return err
		}
		res := tx.Model(&models.BulkMessageRecipient{}).
			Where("id = ? AND status = ? AND message_id IS NULL", job.RecipientID, models.MessageStatusPending).
			UpdateColumn("message_id", msg.ID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errClaimLost // rolls the message back
		}
		return nil
	})
	if errors.Is(claimErr, errClaimLost) {
		if now, ok := w.recipientState(job.RecipientID); ok && now.MessageID != nil {
			w.resolveExistingClaim(ctx, job, now)
		}
		return nil
	}
	if claimErr != nil {
		// Nothing was sent. The job is not acknowledged and will be delivered again.
		return fmt.Errorf("failed to claim recipient: %w", claimErr)
	}

	if w.afterClaim != nil {
		w.afterClaim()
	}

	// The claim took time: check the validity again right before Meta is called.
	if cerr := w.otpPolicy().Check(sealedParams, w.now()); cerr != nil {
		w.releaseClaim(ctx, job, msg, cerr.Error()+": the verification code is not valid for sending", true)
		return nil
	}

	// No new Meta call while the worker is shutting down. Nothing has left yet, so the
	// claim is undone (the recipient goes back to pending) and the job is redelivered.
	if err := w.shutdownErr(ctx); err != nil {
		w.unclaim(job, msg, "worker_shutdown: the worker was stopping before the request was made; the code was not sent")
		return err
	}

	recipient := &models.BulkMessageRecipient{
		PhoneNumber:    job.PhoneNumber,
		RecipientName:  job.RecipientName,
		TemplateParams: opened,
		HeaderParams:   job.HeaderParams,
	}
	// The call is bounded by a deadline the claim-recovery limit is derived from, and it
	// does NOT depend on the shutdown signal: once a claim is held the request is allowed
	// to finish within its own timeout, so a clean stop does not turn a send into an
	// uncertain one (Close waits for it, for a bounded time).
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.metaCallTimeout())
	defer cancel()
	wamid, sendErr := w.sendTemplateMessage(callCtx, account, campaign.Template, recipient, campaign.HeaderMediaID, campaign.HeaderMediaFilename)
	if sendErr != nil {
		// The error text may echo request details: never let a code reach logs or rows.
		sendErr = whatsapp.WithRedactedText(sendErr, templateutil.RedactValues(sendErr.Error(), opened))
	}
	switch {
	case sendErr == nil:
		w.finishSent(ctx, job, msg, wamid)
	case whatsapp.Classify(sendErr) == whatsapp.OutcomeRefused:
		// Meta refused it (or it never left): provably not sent, retrying cannot
		// succeed until something is fixed. The claim is released, nothing is queued.
		w.Log.Error("Meta refused the message", "error", sendErr, "recipient", job.RecipientID)
		w.releaseClaim(ctx, job, msg, "meta_refused: "+sendErr.Error(), false)
	case whatsapp.Classify(sendErr) == whatsapp.OutcomeTransient:
		// Throttled: provably not sent, and a later retry may work.
		w.Log.Warn("Meta throttled the message", "error", sendErr, "recipient", job.RecipientID)
		w.releaseClaim(ctx, job, msg, "meta_transient: "+sendErr.Error(), false)
	default:
		// Nothing proves it was not accepted. It is never resent on a guess.
		w.Log.Error("Meta result unknown", "error", sendErr, "recipient", job.RecipientID)
		w.markUncertain(ctx, job, msg, ReasonDeliveryUncertain+": the request to Meta ended without a confirmed result ("+sendErr.Error()+"); the code is NOT resent automatically")
	}
	return nil
}

// newSecretMessage is the masked, pending message created inside the claim.
func (w *Worker) newSecretMessage(job *queue.RecipientJob, campaign *models.BulkMessageCampaign, contact *models.Contact) *models.Message {
	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  job.OrganizationID,
		WhatsAppAccount: campaign.WhatsAppAccount,
		ContactID:       contact.ID,
		Direction:       models.DirectionOutgoing,
		MessageType:     models.MessageTypeTemplate,
		Status:          models.MessageStatusPending,
		Metadata: models.JSONB{
			"campaign_id":      job.CampaignID.String(),
			"recipient_name":   job.RecipientName,
			"auth_code_masked": true,
		},
	}
	if campaign.Template != nil {
		msg.TemplateName = campaign.Template.Name
		msg.Content = templateutil.MaskAllParams(campaign.Template.BodyContent) // never the code
		if campaign.HeaderMediaLocalPath != "" {
			msg.MediaURL = campaign.HeaderMediaLocalPath
			msg.MediaMimeType = campaign.HeaderMediaMimeType
			msg.MediaFilename = campaign.HeaderMediaFilename
		}
	}
	return msg
}

// resolveExistingClaim is what a job does when it finds the recipient already
// claimed: it may only ask the database to resolve a claim that is provably too
// old to be alive. It never sends.
func (w *Worker) resolveExistingClaim(ctx context.Context, job *queue.RecipientJob, st recipientState) {
	if st.Status != models.MessageStatusPending || st.MessageID == nil {
		return // already failed (uncertain or not), or sent: nothing to resolve
	}
	if n, err := w.resolveStaleClaims(ctx, &job.RecipientID); err != nil {
		w.Log.Error("Failed to resolve a stale claim", "error", err, "recipient_id", job.RecipientID)
	} else if n == 0 {
		w.Log.Info("Recipient is being sent by another worker, skipping", "recipient_id", job.RecipientID)
	}
}

// ResolveStaleClaims finds, directly in PostgreSQL and without any job having to
// come back from Redis, the recipients that were claimed longer ago than a live
// send can last, and declares their delivery uncertain. They stay blocked: the
// claim (message_id) is kept, nothing is queued and retry-failed skips them.
// Returns how many were resolved.
func (w *Worker) ResolveStaleClaims(ctx context.Context) (int, error) {
	return w.resolveStaleClaims(ctx, nil)
}

type staleClaim struct {
	RecipientID    uuid.UUID
	CampaignID     uuid.UUID
	MessageID      uuid.UUID
	OrganizationID uuid.UUID
}

// resolveStaleClaims does the transition atomically: the age test, the state test
// and the update are ONE statement, so a worker that is alive and finishing at the
// same moment either wins (its update runs first and the recipient is no longer
// pending) or loses (this runs first and its late result is refused, see finishSent).
func (w *Worker) resolveStaleClaims(ctx context.Context, only *uuid.UUID) (int, error) {
	reason := ReasonDeliveryUncertain + ": a claim older than the longest a live send can last was never resolved (the worker stopped or was cut off); the code is NOT resent automatically"
	var claims []staleClaim
	err := w.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := `UPDATE bulk_message_recipients r SET status = 'failed', error_message = @reason
			FROM messages m
			WHERE m.id = r.message_id AND r.status = 'pending' AND r.deleted_at IS NULL
			  AND m.status = 'pending' AND m.created_at < clock_timestamp() - (@stale * interval '1 second')`
		args := []any{sql.Named("reason", reason), sql.Named("stale", w.claimStale().Seconds())}
		if only != nil {
			q += " AND r.id = @only"
			args = append(args, sql.Named("only", *only))
		}
		q += " RETURNING r.id AS recipient_id, r.campaign_id AS campaign_id, r.message_id AS message_id, m.organization_id AS organization_id"
		if err := tx.Raw(q, args...).Scan(&claims).Error; err != nil {
			return err
		}
		if len(claims) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(claims))
		for i, c := range claims {
			ids[i] = c.MessageID
		}
		return tx.Model(&models.Message{}).Where("id IN ? AND status = ?", ids, models.MessageStatusPending).
			Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason}).Error
	})
	if err != nil {
		return 0, err
	}
	for _, c := range claims {
		w.Log.Warn("Stale claim resolved as delivery_uncertain", "recipient_id", c.RecipientID, "campaign_id", c.CampaignID)
		w.incrementCampaignCount(c.CampaignID, "failed_count")
		var msg models.Message
		if err := w.DB.Where("id = ?", c.MessageID).First(&msg).Error; err == nil {
			w.recordCampaignUsageUnsettled(ctx, &msg, c.CampaignID)
		}
		w.checkCampaignCompletion(ctx, c.CampaignID, c.OrganizationID)
	}
	return len(claims), nil
}

// finishSent records the result Meta confirmed, on the message created by the claim.
// Both updates require the claim to still be pending and still ours: if the claim was
// meanwhile declared uncertain (or released by a person), this late result is refused
// and rolled back, so it can never overwrite a reconciled state. The wamid is logged,
// so whoever reconciles has it.
func (w *Worker) finishSent(ctx context.Context, job *queue.RecipientJob, msg *models.Message, wamid string) {
	now := w.now()
	err := w.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.BulkMessageRecipient{}).
			Where("id = ? AND status = ? AND message_id = ?", job.RecipientID, models.MessageStatusPending, msg.ID).
			Updates(map[string]any{"status": models.MessageStatusSent, "whats_app_message_id": wamid, "sent_at": now, "error_message": ""})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errClaimNoLongerHeld
		}
		return tx.Model(&models.Message{}).Where("id = ?", msg.ID).
			Updates(map[string]any{"status": models.MessageStatusSent, "whats_app_message_id": wamid}).Error
	})
	if err != nil {
		// Meta accepted the message but the result could not be written (or the claim had
		// already been reconciled). The recipient stays as it is: pending, or uncertain.
		w.Log.Error("Meta accepted the message but its result was not recorded", "error", err, "recipient_id", job.RecipientID, "message_id", msg.ID, "wamid", wamid)
		return
	}
	w.Log.Info("Message sent", "recipient", job.PhoneNumber, "message_id", wamid)
	w.scrubRecipientSecret(job.RecipientID) // sent: the code is no longer needed
	w.incrementCampaignCount(job.CampaignID, "sent_count")
	msg.Status, msg.WhatsAppMessageID = models.MessageStatusSent, wamid
	w.recordCampaignUsage(ctx, msg, job.CampaignID, nil)
	w.checkCampaignCompletion(ctx, job.CampaignID, job.OrganizationID)
}

// releaseClaim ends an attempt that PROVABLY did not send: the message is failed
// and the recipient goes back to "failed, no claim", so RetryFailed can try again.
// dropSecret deletes the stored code (it can never be sent); otherwise it is kept.
// Like finishSent it only acts while the claim is still pending and still ours.
func (w *Worker) releaseClaim(ctx context.Context, job *queue.RecipientJob, msg *models.Message, reason string, dropSecret bool) {
	err := w.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.BulkMessageRecipient{}).
			Where("id = ? AND status = ? AND message_id = ?", job.RecipientID, models.MessageStatusPending, msg.ID).
			Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason, "message_id": nil})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errClaimNoLongerHeld
		}
		return tx.Model(&models.Message{}).Where("id = ?", msg.ID).
			Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason}).Error
	})
	if err != nil {
		w.Log.Error("Failed to release the claim", "error", err, "recipient_id", job.RecipientID)
		return
	}
	if dropSecret {
		w.scrubRecipientSecret(job.RecipientID)
	}
	w.incrementCampaignCount(job.CampaignID, "failed_count")
	msg.Status, msg.ErrorMessage = models.MessageStatusFailed, reason
	w.recordCampaignUsage(ctx, msg, job.CampaignID, errors.New(reason))
	w.checkCampaignCompletion(ctx, job.CampaignID, job.OrganizationID)
}

// markUncertain records that the outcome of THIS worker's own attempt is unknown.
// The claim (message_id) stays, which is exactly what keeps RetryFailed away from it.
// The sealed code is kept too, so a person can reconcile and release it deliberately.
func (w *Worker) markUncertain(ctx context.Context, job *queue.RecipientJob, msg *models.Message, reason string) {
	// Recipient and message change together or not at all: the compare-and-set on the
	// recipient and the message update are one transaction, and the counters, usage and
	// completion run only after it committed.
	won := false
	err := w.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.BulkMessageRecipient{}).
			Where("id = ? AND status = ? AND message_id = ?", job.RecipientID, models.MessageStatusPending, msg.ID).
			Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return nil // somebody else resolved it first: nothing to undo, nothing to count
		}
		if err := tx.Model(&models.Message{}).Where("id = ?", msg.ID).
			Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason}).Error; err != nil {
			return err // rolls the recipient update back as well
		}
		won = true
		return nil
	})
	if err != nil {
		// Both writes were rolled back: the recipient is still claimed (pending), and
		// housekeeping resolves it as delivery_uncertain once the claim is old enough.
		w.Log.Error("Failed to record an uncertain delivery; the claim stays for housekeeping", "error", err, "recipient_id", job.RecipientID)
		return
	}
	if !won {
		return
	}
	w.incrementCampaignCount(job.CampaignID, "failed_count")
	msg.Status, msg.ErrorMessage = models.MessageStatusFailed, reason
	w.recordCampaignUsageUnsettled(ctx, msg, job.CampaignID)
	w.checkCampaignCompletion(ctx, job.CampaignID, job.OrganizationID)
}

// failBeforeClaim fails a recipient that could not even be claimed (no code to send).
// It is a compare-and-set, so duplicate jobs count the failure once.
func (w *Worker) failBeforeClaim(ctx context.Context, job *queue.RecipientJob, reason string, dropSecret bool) {
	res := w.DB.Model(&models.BulkMessageRecipient{}).
		Where("id = ? AND status = ? AND message_id IS NULL", job.RecipientID, models.MessageStatusPending).
		Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason})
	if res.Error != nil || res.RowsAffected != 1 {
		return
	}
	if dropSecret {
		w.scrubRecipientSecret(job.RecipientID)
	}
	w.incrementCampaignCount(job.CampaignID, "failed_count")
	w.checkCampaignCompletion(ctx, job.CampaignID, job.OrganizationID)
}

// recordCampaignUsageUnsettled writes the ledger row without settling it: Meta may
// or may not have accepted (and billed) a message whose outcome is unknown.
func (w *Worker) recordCampaignUsageUnsettled(ctx context.Context, msg *models.Message, campaignID uuid.UUID) {
	if !w.Usage.Enabled() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			w.Log.Error("Usage recording panicked", "panic", fmt.Sprint(r))
		}
	}()
	origin := usage.Origin{ActorType: usage.ActorCampaign, CampaignID: &campaignID, Detail: "campaign"}
	if err := w.Usage.Record(ctx, nil, msg, origin); err != nil {
		w.Log.Error("Usage recording failed", "op", "record", "error", err, "message_id", msg.ID)
	}
}

const (
	// housekeepingEvery is how often abandoned claims are looked for.
	housekeepingEvery = 5 * time.Minute
	// retentionEveryNthTick makes retention run hourly (12 ticks of 5 minutes).
	retentionEveryNthTick = 12
)

// DefaultClaimStale is the claim-recovery limit with the default Meta timeout, for
// tools that run outside a worker.
func DefaultClaimStale() time.Duration { return 2 * (defaultMetaCall + claimWriteBudget) }

// ---- graceful shutdown ----

var errShuttingDown = errors.New("worker is shutting down")

// shutdownErr is non-nil when the worker must start nothing new.
func (w *Worker) shutdownErr(ctx context.Context) error {
	if w.closing.Load() {
		return errShuttingDown
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", errShuttingDown, err)
	}
	return nil
}

// BeginShutdown makes the worker refuse to start new one-time-code sends.
func (w *Worker) BeginShutdown() { w.closing.Store(true) }

// drainTimeout is how long shutdown waits for jobs already started: a Meta call plus
// half the write budget around it.
func (w *Worker) drainTimeout() time.Duration { return w.metaCallTimeout() + claimWriteBudget/2 }

// Drain waits up to timeout for the jobs already started to finish. It reports
// whether they all did.
func (w *Worker) Drain(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for w.inflight.count() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
	return true
}

// inflightJobs counts the jobs being handled.
type inflightJobs struct {
	mu sync.Mutex
	n  int
}

func (i *inflightJobs) begin() { i.mu.Lock(); i.n++; i.mu.Unlock() }
func (i *inflightJobs) end()   { i.mu.Lock(); i.n--; i.mu.Unlock() }
func (i *inflightJobs) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.n
}

// unclaim undoes a claim for an attempt that never made a request: the message is
// failed (kept for the record) and the recipient goes back to pending with no claim,
// so the redelivered job starts over. Compare-and-set on the claim.
func (w *Worker) unclaim(job *queue.RecipientJob, msg *models.Message, reason string) {
	err := w.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.BulkMessageRecipient{}).
			Where("id = ? AND status = ? AND message_id = ?", job.RecipientID, models.MessageStatusPending, msg.ID).
			UpdateColumn("message_id", nil)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errClaimNoLongerHeld
		}
		return tx.Model(&models.Message{}).Where("id = ?", msg.ID).
			Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": reason}).Error
	})
	if err != nil {
		w.Log.Error("Failed to undo the claim", "error", err, "recipient_id", job.RecipientID)
	}
}
