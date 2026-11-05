package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Idempotency of the protocol send.
//
// One deliberate send = one Idempotency-Key = at most one message to the provider, whatever the
// number of requests, retries, tabs or servers. The guarantee is the UNIQUE index on
// (organization_id, idempotency_key): the request that INSERTs the row owns the send; every
// other request with that key only ever reads the row and answers from it.
//
//	processing  the owner is still working        -> 202 "in progress", nothing is sent
//	sent        the provider accepted it          -> the first answer again (200)
//	failed      proven not sent                   -> the first answer again; a NEW intention uses a NEW key
//	uncertain   the provider never confirmed it   -> 202 pending confirmation, nothing is sent
//
// The same key with anything else (another text, contact, account, case or person) is a 409:
// it is never read as "the same send".
//
// A claim whose owner vanished (process killed, database lost after the provider accepted) is
// reconciled by evidence after sendIntentStaleAfter: the audit trail and the message row. When
// the evidence is not enough it becomes "uncertain"; it is NEVER released for a new send and the
// message is never sent again by recovery: no local record proves the provider did not get it.

const (
	headerIdempotencyKey   = "Idempotency-Key"
	headerIdempotentReplay = "Idempotent-Replay"

	// sendIntentStaleAfter is how long a claim may stay "processing": well past the provider's
	// 30 s client timeout, so a live owner has long finished.
	sendIntentStaleAfter = 2 * time.Minute
	// SendIntentRetention is how long a finished (sent or failed) intent is kept. After it the key
	// is forgotten and would be read as a new intention: keys are single-use by contract and
	// the interface never reuses one. processing and unreconciled uncertain rows are never purged.
	SendIntentRetention = 90 * 24 * time.Hour

	// sendIntentUncertainRecheckWindow bounds only HOW LONG the background loop keeps re-reading an unsettled
	// uncertain send (the provider's own confirmation arrives within hours). It never bounds the protection:
	// the send stays open, the case stays blocked, the record is kept, and a person can still settle it.
	sendIntentUncertainRecheckWindow = 72 * time.Hour
	sendIntentBatch                  = 200
	sendIntentMaxBatches             = 5

	sendIntentReconcileEvery = 30 * time.Second
	sendIntentPurgeEvery     = time.Hour

	errTypeIdempotencyKeyRequired = "idempotency_key_required"
	errTypeIdempotencyKeyInvalid  = "idempotency_key_invalid"
	errTypeIdempotencyKeyReused   = "idempotency_key_reused"
	errTypeSendNotFound           = "send_not_found"
	errTypeSendPending            = "send_pending"
	errTypeResolutionInvalid      = "resolution_invalid"
	errTypeSendNotUncertain       = "send_not_uncertain"

	protocolResultProcessing = models.SendIntentProcessing
)

// Fencing. The owner of a send may call the provider only AFTER it moved its row, atomically in the database,
// from "never dispatched" to "dispatched" (dispatchSendIntent), and only until the deadline stored with that move,
// which is enforced as the deadline of the provider call itself. So anybody who finds the row stale can tell, from
// the database alone, whether the owner can still produce the effect on the customer:
//   - never dispatched and stale: the owner cannot send any more, whatever it does when it wakes up (its own
//     compare-and-set will fail), so the send is proven not sent and the row is fenced to failed;
//   - dispatched: the owner may still be calling the provider until its deadline (plus a margin for clock skew
//     between servers); only after that can it no longer START a send, and the row is settled from the evidence.
// What no software can revoke is a request the owner already handed to the network before the deadline: the
// provider may still process it, which is why a send that ends without an answer stays "uncertain" and why the
// margin exists. See the contract ("Fencing").
var (
	// sendIntentDispatchWindow is how long after the dispatch the owner may still call the provider. Longer than
	// the 30 s client timeout, so a normal send is never cut by it.
	sendIntentDispatchWindow = 40 * time.Second
	// sendIntentSkewMargin covers the clock difference between application servers (the deadline is computed by
	// the database and enforced against each server's own clock) and the provider's own queueing.
	sendIntentSkewMargin = 30 * time.Second
)

// sendHooks lets a test stop the owner at the two instants that matter. Always nil outside tests.
var sendHooks struct{ beforeDispatch, afterDispatch func() }

// errSendFenced: somebody fenced the row before the owner reached the point of no return. Nothing was sent.
var errSendFenced = errors.New("the send was fenced before it reached the provider")

// dispatchSendIntent is the owner's point of no return. It returns the deadline until which the provider may be
// called, ok=false when the row is not "processing and never dispatched" any more (fenced by reconciliation),
// and an error when the database could not say (nothing may be sent then either).
func (a *App) dispatchSendIntent(intent *models.OccurrenceSendIntent) (deadline, started time.Time, ok bool, err error) {
	if h := sendHooks.beforeDispatch; h != nil {
		h()
	}
	// The process's own monotonic clock starts BEFORE the statement is sent, so the local expiry (started + window)
	// can never be later than the database's (its NOW() is taken after this instant): the owner's own timer is
	// conservative, and needs no agreement between this server's wall clock and the database's.
	started = time.Now()
	res := a.DB.Raw(`UPDATE occurrence_send_intents
		SET dispatched_at = NOW(), dispatch_deadline = NOW() + make_interval(secs => ?), updated_at = NOW()
		WHERE id = ? AND state = ? AND dispatched_at IS NULL
		RETURNING dispatch_deadline`, sendIntentDispatchWindow.Seconds(), intent.ID, models.SendIntentProcessing).Scan(&deadline)
	if res.Error != nil {
		return time.Time{}, time.Time{}, false, res.Error
	}
	if res.RowsAffected != 1 {
		return time.Time{}, time.Time{}, false, nil
	}
	if h := sendHooks.afterDispatch; h != nil {
		h()
	}
	return deadline, started, true, nil
}

// preflightRefusal: the last check before the request refused it. No request was made.
type preflightRefusal struct{ cause error }

func (e *preflightRefusal) Error() string { return e.cause.Error() }
func (e *preflightRefusal) Unwrap() error { return e.cause }

// errPastDispatchDeadline: the owner's time to call the provider is over (or the row is not "processing and
// dispatched" any more).
var errPastDispatchDeadline = errors.New("the owner's deadline to call the provider has passed or the send was fenced")

// preflightSendIntent runs immediately before the provider request is handed to the HTTP client. It asks the
// DATABASE, with the database's own clock, whether this row is still processing, dispatched and inside its
// deadline, and checks the process's monotonic timer too. A refusal means the request is not made, which is
// provably not sent. It narrows, and does not remove, the gap between this check and the write on the network
// (see "Fencing" in the contract): a process frozen right after it returns can still write later.
func (a *App) preflightSendIntent(ctx context.Context, id uuid.UUID, started time.Time) error {
	if time.Since(started) >= sendIntentDispatchWindow {
		return &preflightRefusal{cause: errPastDispatchDeadline}
	}
	var n int64
	err := a.DB.WithContext(ctx).Model(&models.OccurrenceSendIntent{}).
		Where("id = ? AND state = ? AND dispatched_at IS NOT NULL AND dispatch_deadline > NOW()", id, models.SendIntentProcessing).
		Count(&n).Error
	if err != nil {
		return &preflightRefusal{cause: fmt.Errorf("could not confirm that the send is still allowed: %w", err)}
	}
	if n != 1 {
		return &preflightRefusal{cause: errPastDispatchDeadline}
	}
	return nil
}

// dbNow is the DATABASE's clock. Everything that decides whether a dispatched owner can still send (the deadline
// and the margin after it) is judged on this one clock, so no agreement between the wall clocks of several
// application servers is needed. There is NO fallback to the local clock: when the database cannot answer, the
// caller does not judge (see reconcileByDBClock): a decision that frees or settles a send must never be taken on a
// clock the deadline was not measured on.
func (a *App) dbNow() (time.Time, error) {
	if a.dbClockForTest != nil {
		return a.dbClockForTest()
	}
	var t time.Time
	if err := a.DB.Raw("SELECT NOW()").Scan(&t).Error; err != nil {
		return time.Time{}, fmt.Errorf("could not read the database clock: %w", err)
	}
	if t.IsZero() {
		return time.Time{}, errors.New("could not read the database clock: empty answer")
	}
	return t, nil
}

// reconcileByDBClock reconciles one row on the database's clock. When the clock cannot be read it does NOTHING (the
// row stays as it is, which is the safe side: still open) and reports false.
func (a *App) reconcileByDBClock(intent *models.OccurrenceSendIntent) bool {
	now, err := a.dbNow()
	if err != nil {
		a.Log.Warn("Send intent not reconciled: the database clock could not be read", "error", err, "send_id", intent.ID)
		return false
	}
	return a.reconcileSendIntent(intent, now)
}

// errTypeSendGuardUnavailable: the database protection that keeps a case at one open send is missing or invalid.
const errTypeSendGuardUnavailable = "send_guard_unavailable"

// guardUnavailableError: the protection could not be confirmed INSIDE the claim, so nothing was claimed.
type guardUnavailableError struct{ cause error }

func (e *guardUnavailableError) Error() string { return e.cause.Error() }
func (e *guardUnavailableError) Unwrap() error { return e.cause }

// checkOpenSendGuard confirms, on the connection of the claim transaction, that the open-send index is really in
// the database. There is NO cache: every new claim is checked, inside the same transaction as its INSERT.
func (a *App) checkOpenSendGuard(tx *gorm.DB) error {
	if a.verifyOpenSendGuard != nil {
		return a.verifyOpenSendGuard(tx)
	}
	return database.VerifyOpenSendIntentGuard(tx)
}

// requireIdempotencyKey reads and validates the header. It writes the 400 itself.
func requireIdempotencyKey(r *fastglue.Request) (string, bool) {
	raw := strings.TrimSpace(string(r.RequestCtx.Request.Header.Peek(headerIdempotencyKey)))
	if raw == "" {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The Idempotency-Key header is required", nil, errTypeIdempotencyKeyRequired)
		return "", false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		_ = r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The Idempotency-Key must be a UUID", nil, errTypeIdempotencyKeyInvalid)
		return "", false
	}
	return id.String(), true
}

// protocolRequestIdentity is everything that decides WHAT is sent and TO WHOM. The same key may
// only ever mean this exact thing.
type protocolRequestIdentity struct {
	Kind           string `json:"kind"`
	OccurrenceID   string `json:"occurrence_id"`
	ProtocolNumber string `json:"protocol_number"`
	ContactID      string `json:"contact_id"`
	Phone          string `json:"phone"`
	BSUID          string `json:"bsuid"`
	AccountID      string `json:"account_id"`
	AccountName    string `json:"account_name"`
	Content        string `json:"content"`
}

func hashProtocolRequest(id protocolRequestIdentity) string {
	b, _ := json.Marshal(id) // a fixed struct: the field order, and so the hash, is stable
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// claimSendIntent tries to take the key AND the case. claimed is true for exactly one caller per key and
// one send per case at a time; everyone else gets the row that is in the way:
//   - the row with the same key (a repeat of the same request), or
//   - the case's open row (processing, or uncertain until a person settles it) under ANOTHER key: nobody
//     may send to this customer from this case while a send may be in flight or delivered.
//
// The INSERT ... ON CONFLICT DO NOTHING is the atomic step. It carries no conflict target on purpose: the
// unique key index (organization, key) and the partial unique index uq_send_intent_open (organization, case,
// kind where open) are both enforced by the database, so two simultaneous requests cannot both insert,
// whatever their keys, users, devices or servers. A stale "processing" row that is in the way is reconciled
// from the evidence first (it may have ended): that never sends and never frees an uncertain one.
func (a *App) claimSendIntent(orgID, userID uuid.UUID, occ *models.Occurrence, key, hash string) (*models.OccurrenceSendIntent, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		now := time.Now()
		intent := &models.OccurrenceSendIntent{
			ID: uuid.New(), OrganizationID: orgID, IdempotencyKey: key, OccurrenceID: occ.ID, UserID: userID,
			Kind: models.SendIntentKindProtocol, RequestHash: hash, State: models.SendIntentProcessing,
			MessageID: uuid.New(), ClaimedAt: now,
		}
		claimed := false
		// The INSERT and the check of the protection are ONE transaction. Once the INSERT has run this transaction
		// holds a lock on the table that DROP INDEX must wait for, so an index that is there when it is checked
		// (after the INSERT) stays there until the claim is committed: a claim can only be committed under the
		// protection, never in the gap between a check and the INSERT. If the protection is missing the INSERT is
		// rolled back and nothing was claimed. A repeat of an existing key inserts nothing and needs no check.
		err := a.DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(intent)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 {
				if gerr := a.checkOpenSendGuard(tx); gerr != nil {
					return &guardUnavailableError{cause: gerr}
				}
				claimed = true
			}
			return nil
		})
		if err != nil {
			return nil, false, err
		}
		if claimed {
			return intent, true, nil
		}
		var existing models.OccurrenceSendIntent
		err = a.DB.Where("organization_id = ? AND idempotency_key = ?", orgID, key).First(&existing).Error
		if err == nil {
			return &existing, false, nil // the same key: the caller answers from it
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
		// Not the key: the case's open send is in the way.
		err = a.DB.Where("organization_id = ? AND occurrence_id = ? AND kind = ? AND state IN ?",
			orgID, occ.ID, models.SendIntentKindProtocol, []string{models.SendIntentProcessing, models.SendIntentUncertain}).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue // it closed between the two statements: try to take the case again
		}
		if err != nil {
			return nil, false, err
		}
		if a.reconcileByDBClock(&existing) && existing.State != models.SendIntentProcessing && existing.State != models.SendIntentUncertain {
			continue // reconciliation settled it: the case may be free now
		}
		return &existing, false, nil
	}
	return nil, false, fmt.Errorf("could not take the case for a send after several attempts")
}

// intentOutcome is the data of an answer: the same fields whatever the result.
func intentOutcome(intent *models.OccurrenceSendIntent, protocolNumber, result string) map[string]any {
	return map[string]any{
		"result":          result,
		"sent":            result == models.SendIntentSent,
		"confirmed":       result == models.SendIntentSent || result == models.SendIntentFailed,
		"send_id":         intent.ID.String(),
		"message_id":      intent.MessageID.String(),
		"protocol_number": protocolNumber,
	}
}

// intentAnswer builds the envelope and status the first request answers (and every repeat repeats).
func intentAnswer(intent *models.OccurrenceSendIntent, protocolNumber, result string, failStatus int, failMessage, failType string) (int, models.JSONB) {
	switch result {
	case models.SendIntentSent:
		return fasthttp.StatusOK, models.JSONB{"status": "success", "data": intentOutcome(intent, protocolNumber, result)}
	case models.SendIntentUncertain:
		return fasthttp.StatusAccepted, models.JSONB{
			"status":  "success",
			"message": "Result pending confirmation: the provider did not confirm the message and it may have been delivered. Do not send it again; check the conversation",
			"data":    intentOutcome(intent, protocolNumber, result),
		}
	default: // failed
		env := models.JSONB{"status": "error", "message": failMessage, "data": intentOutcome(intent, protocolNumber, models.SendIntentFailed)}
		if failType != "" {
			env["error_type"] = failType
		}
		return failStatus, env
	}
}

// finishSendIntent closes the claim with the answer. It is a compare-and-set on "processing": it
// can only ever happen once, and never over what recovery decided.
func (a *App) finishSendIntent(intent *models.OccurrenceSendIntent, state string, status int, envelope models.JSONB) {
	now := time.Now()
	res := a.DB.Model(&models.OccurrenceSendIntent{}).
		Where("id = ? AND state = ?", intent.ID, models.SendIntentProcessing).
		Updates(map[string]any{"state": state, "http_status": status, "response": envelope, "finished_at": now})
	if res.Error != nil {
		// The message may already be out. The claim stays "processing" and reconciliation
		// settles it from the trail and the message row; nothing sends again meanwhile.
		a.Log.Error("Failed to close the send intent", "error", res.Error, "send_id", intent.ID, "state", state)
		return
	}
	if res.RowsAffected == 0 {
		a.Log.Warn("The send intent was already closed by someone else", "send_id", intent.ID, "state", state)
		return
	}
	intent.State, intent.HTTPStatus, intent.Response, intent.FinishedAt = state, status, envelope, &now
}

// sendStoredAnswer writes a stored answer (the first one, or a repeat of it).
func sendStoredAnswer(r *fastglue.Request, status int, envelope models.JSONB, replay bool) error {
	if replay {
		r.RequestCtx.Response.Header.Set(headerIdempotentReplay, "true")
	}
	return r.SendJSON(status, map[string]any(envelope))
}

// answerExistingIntent answers a request whose key is already taken. It never sends anything.
func (a *App) answerExistingIntent(r *fastglue.Request, intent *models.OccurrenceSendIntent, occ *models.Occurrence, userID uuid.UUID, key, hash string) error {
	if intent.IdempotencyKey != key {
		return a.answerSendPending(r, intent, occ, userID)
	}
	if intent.OccurrenceID != occ.ID || intent.UserID != userID || intent.RequestHash != hash || intent.Kind != models.SendIntentKindProtocol {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			"This Idempotency-Key was already used for a different request; use a new key for a new send", nil, errTypeIdempotencyKeyReused)
	}
	a.reconcileByDBClock(intent)
	switch intent.State {
	case models.SendIntentProcessing:
		r.RequestCtx.Response.Header.Set(headerIdempotentReplay, "true")
		return r.SendJSON(fasthttp.StatusAccepted, map[string]any{
			"status":  "success",
			"message": "A send with this Idempotency-Key is already in progress; nothing was sent again. Ask for its state with the send_id",
			"data":    intentOutcome(intent, occ.ProtocolNumber, protocolResultProcessing),
		})
	default:
		return sendStoredAnswer(r, intent.HTTPStatus, intent.Response, true)
	}
}

// answerSendPending refuses a new send because the case already has an open one under another key. It says
// what the page needs to show the state and to ask about it (send_id, state) and whether the caller started
// it; it never reveals who did, what was sent or anything of another organization (the row is the case's own,
// found by organization and case). Nothing is sent.
func (a *App) answerSendPending(r *fastglue.Request, open *models.OccurrenceSendIntent, occ *models.Occurrence, userID uuid.UUID) error {
	return r.SendJSON(fasthttp.StatusConflict, map[string]any{
		"status":     "error",
		"message":    "This case already has a customer send that is not settled yet; nothing was sent. Check the conversation and wait for its result",
		"error_type": errTypeSendPending,
		"data": map[string]any{
			"result":     "pending",
			"sent":       false,
			"confirmed":  false,
			"send_id":    open.ID.String(),
			"state":      open.State,
			"own":        open.UserID == userID,
			"claimed_at": open.ClaimedAt,
		},
	})
}

// --- reconciliation -----------------------------------------------------------------------

// sentEvidence is what the local records say about one send.
type sentEvidence struct {
	trailResult   string // sent | failed | uncertain, from the result row of the audit trail, or ""
	messageStatus string // status of the message row, or "" when there is none
	messageWamid  string
}

func (a *App) sendEvidence(intent *models.OccurrenceSendIntent) sentEvidence {
	var ev sentEvidence
	probe, _ := json.Marshal([]map[string]any{{"field": "send_id", "new_value": intent.ID.String()}})
	var rows []models.AuditLog
	a.DB.Where("organization_id = ? AND resource_type = ? AND resource_id = ?", intent.OrganizationID, protocolSendAuditType, intent.OccurrenceID).
		Where("changes @> ?::jsonb", string(probe)).Find(&rows)
	for _, row := range rows {
		for _, c := range row.Changes {
			m, ok := c.(map[string]any)
			if !ok || m["field"] != "result" {
				continue
			}
			if v, _ := m["new_value"].(string); v == models.SendIntentSent || v == models.SendIntentFailed || v == models.SendIntentUncertain {
				ev.trailResult = v
			}
		}
	}
	var msg models.Message
	if err := a.DB.Select("id", "status", "whats_app_message_id").Where("id = ?", intent.MessageID).First(&msg).Error; err == nil {
		ev.messageStatus, ev.messageWamid = string(msg.Status), msg.WhatsAppMessageID
	}
	return ev
}

func providerAccepted(ev sentEvidence) bool {
	switch models.MessageStatus(ev.messageStatus) {
	case models.MessageStatusSent, models.MessageStatusDelivered, models.MessageStatusRead:
		return true
	}
	return false
}

// reconcileSendIntent settles an intent from the evidence in the local records. It only ever moves
// a claim FORWARD (processing -> sent | failed | uncertain, or uncertain -> sent when the provider
// itself later confirmed it), never frees a key and never sends. A claim whose owner never dispatched is
// FENCED to failed (proven not sent); one that was dispatched is touched only after the owner's deadline plus
// the skew margin. Returns true when it changed it.
func (a *App) reconcileSendIntent(intent *models.OccurrenceSendIntent, now time.Time) bool {
	fence := false
	switch {
	case intent.State == models.SendIntentProcessing && intent.DispatchedAt == nil:
		// The owner never reached the point of no return. After the wait it is fenced: whatever it does when it
		// wakes up, its own dispatch will find the row moved and send nothing.
		if now.Sub(intent.ClaimedAt) < sendIntentStaleAfter {
			return false
		}
		fence = true
	case intent.State == models.SendIntentProcessing:
		// Dispatched: the owner may still be calling the provider until its deadline (plus the skew margin).
		deadline := intent.DispatchedAt.Add(sendIntentDispatchWindow)
		if intent.DispatchDeadline != nil {
			deadline = *intent.DispatchDeadline
		}
		if now.Before(deadline.Add(sendIntentSkewMargin)) {
			return false
		}
	case intent.State == models.SendIntentUncertain && intent.ReconciledAt == nil:
	default:
		return false
	}
	ev := a.sendEvidence(intent)
	var occ models.Occurrence
	_ = a.DB.Select("id", "protocol_number").Where("id = ?", intent.OccurrenceID).First(&occ).Error

	if fence {
		status, env := intentAnswer(intent, occ.ProtocolNumber, models.SendIntentFailed, fasthttp.StatusBadGateway,
			"The send was abandoned before it reached the provider; nothing was sent", "")
		res := a.DB.Model(&models.OccurrenceSendIntent{}).
			Where("id = ? AND state = ? AND dispatched_at IS NULL", intent.ID, models.SendIntentProcessing).
			Updates(map[string]any{"state": models.SendIntentFailed, "http_status": status, "response": env, "finished_at": now, "reconciled_at": now})
		if res.Error != nil || res.RowsAffected != 1 {
			a.DB.Where("id = ?", intent.ID).First(intent) // the owner dispatched meanwhile: it is not abandoned
			return false
		}
		a.Log.Info("Send intent fenced: the owner never reached the provider", "send_id", intent.ID)
		a.DB.Where("id = ?", intent.ID).First(intent)
		return true
	}

	target := ""
	switch {
	case intent.State == models.SendIntentUncertain:
		// Only the provider's own confirmation upgrades it (a message id was returned and the
		// message advanced): a trail line or a missing row proves nothing about the provider.
		if ev.messageWamid != "" && providerAccepted(ev) {
			target = models.SendIntentSent
		}
	case ev.trailResult != "": // the handler itself wrote the result: it is the best evidence
		target = ev.trailResult
	case providerAccepted(ev): // no result row, but the message row says the provider took it
		target = models.SendIntentSent
	default:
		// Not enough evidence. A missing local row does NOT prove the provider did not receive the
		// request, so this is uncertain, never failed, and the key stays taken.
		target = models.SendIntentUncertain
	}
	if target == "" || target == intent.State {
		return false
	}
	status, env := intentAnswer(intent, occ.ProtocolNumber, target, fasthttp.StatusBadGateway, "The provider did not accept the message", "")
	updates := map[string]any{"state": target, "http_status": status, "response": env, "finished_at": now}
	if target != models.SendIntentUncertain {
		updates["reconciled_at"] = now
	}
	res := a.DB.Model(&models.OccurrenceSendIntent{}).
		Where("id = ? AND state = ?", intent.ID, intent.State).Updates(updates)
	if res.Error != nil || res.RowsAffected != 1 {
		return false
	}
	a.Log.Info("Send intent reconciled", "send_id", intent.ID, "from", intent.State, "to", target)
	a.DB.Where("id = ?", intent.ID).First(intent)
	return true
}

// ReconcileSendIntents settles the claims whose owner vanished and re-checks the recent unsettled uncertain
// ones. The two are separate queries on purpose: an old uncertain row can never keep a newer stale "processing"
// claim waiting, whatever their number. Safe in several processes: every change is a conditional UPDATE.
func (a *App) ReconcileSendIntents(now time.Time) int {
	n := 0
	// 1. Stale processing claims, oldest first (partial index idx_send_intent_pending).
	for i := 0; i < sendIntentMaxBatches; i++ {
		var stale []models.OccurrenceSendIntent
		// Never dispatched: after the wait. Dispatched: after the owner deadline plus the skew margin.
		a.DB.Where("state = ? AND ((dispatched_at IS NULL AND claimed_at <= ?) OR (dispatched_at IS NOT NULL AND dispatch_deadline <= ?))",
			models.SendIntentProcessing, now.Add(-sendIntentStaleAfter), now.Add(-sendIntentSkewMargin)).
			Order("claimed_at").Limit(sendIntentBatch).Find(&stale)
		changed := 0
		for j := range stale {
			if a.reconcileSendIntent(&stale[j], now) {
				changed++
			}
		}
		n += changed
		if len(stale) < sendIntentBatch || changed == 0 {
			break
		}
	}
	// 2. Unsettled uncertain ones, only for the recheck window, newest first (a provider confirmation is most
	// likely for the most recent). Older ones are left alone, still open and still blocking, until a person settles them.
	var uncertain []models.OccurrenceSendIntent
	a.DB.Where("state = ? AND reconciled_at IS NULL AND finished_at > ?", models.SendIntentUncertain, now.Add(-sendIntentUncertainRecheckWindow)).
		Order("finished_at DESC").Limit(sendIntentBatch).Find(&uncertain)
	for i := range uncertain {
		if a.reconcileSendIntent(&uncertain[i], now) {
			n++
		}
	}
	return n
}

// PurgeSendIntents applies the retention rule and returns how many rows it removed: only settled intents
// (sent, failed), 90 days after they were settled (by the provider's answer, by reconciliation or by a
// person). processing and uncertain are never removed: an uncertain row is the record that keeps the case
// protected until somebody settles it.
func (a *App) PurgeSendIntents(now time.Time) int64 {
	cutoff := now.Add(-SendIntentRetention)
	res := a.DB.Where("state IN ? AND COALESCE(resolved_at, finished_at) < ?",
		[]string{models.SendIntentSent, models.SendIntentFailed}, cutoff,
	).Delete(&models.OccurrenceSendIntent{})
	if res.Error != nil {
		a.Log.Error("Failed to purge send intents", "error", res.Error)
		return 0
	}
	return res.RowsAffected
}

// reconcileCycle is one pass of the background loop. If the database clock cannot be read the whole pass is skipped
// (and says so): reconciling or purging on the local clock could free or settle a send whose owner can still call
// the provider. It reports whether it ran.
func (a *App) reconcileCycle(lastPurge *time.Time) bool {
	now, err := a.dbNow()
	if err != nil {
		a.Log.Warn("Send intent reconciliation skipped: the database clock could not be read", "error", err)
		return false
	}
	a.ReconcileSendIntents(now)
	if now.Sub(*lastPurge) >= sendIntentPurgeEvery {
		a.PurgeSendIntents(now)
		*lastPurge = now
	}
	return true
}

// StartSendIntentReconciler runs the reconciliation and the retention until ctx ends.
func (a *App) StartSendIntentReconciler(ctx context.Context) {
	t := time.NewTicker(sendIntentReconcileEvery)
	defer t.Stop()
	var lastPurge time.Time
	for {
		a.reconcileCycle(&lastPurge)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- state query --------------------------------------------------------------------------

// GetOccurrenceProtocolSend answers "what happened to that send?" after a timeout, a page
// refresh or a lost answer. Same rules as sending (chat:write, the case and its channel), and a
// send of another case or organization is simply not found.
func (a *App) GetOccurrenceProtocolSend(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, _, ok := a.authorizeCustomerMessageFromCase(r, orgID, userID)
	if !ok {
		return nil
	}
	sendID, err := parsePathUUID(r, "send_id", "send")
	if err != nil {
		return nil
	}
	var intent models.OccurrenceSendIntent
	if err := a.DB.Where("id = ? AND organization_id = ? AND occurrence_id = ?", sendID, orgID, occ.ID).First(&intent).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Send not found", nil, errTypeSendNotFound)
	}
	return a.answerSendState(r, &intent, occ, userID)
}

// GetOccurrenceProtocolSendByKey asks the same question with the Idempotency-Key, for a caller that
// sent the request but never saw the answer (so it does not know the send_id). Read-only: it never
// claims the key and never sends. 404 means the server holds no claim for the key (the request never
// arrived, or is still on its way); the caller must not read that as "it was not sent" until enough
// time has passed for a request in flight to have ended.
func (a *App) GetOccurrenceProtocolSendByKey(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionWrite)
	if err != nil {
		return nil
	}
	occ, _, ok := a.authorizeCustomerMessageFromCase(r, orgID, userID)
	if !ok {
		return nil
	}
	if string(r.RequestCtx.QueryArgs().Peek("open")) == "true" {
		// The case's open send, whoever started it, on any device: so the screen can say there is one
		// without trying to send. Same authorization as sending; nothing of another case or organization.
		var open models.OccurrenceSendIntent
		if err := a.DB.Where("organization_id = ? AND occurrence_id = ? AND kind = ? AND state IN ?",
			orgID, occ.ID, models.SendIntentKindProtocol, []string{models.SendIntentProcessing, models.SendIntentUncertain}).
			Order("claimed_at DESC").First(&open).Error; err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Send not found", nil, errTypeSendNotFound)
		}
		return a.answerSendState(r, &open, occ, userID)
	}
	key, err := uuid.Parse(strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("idempotency_key"))))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The idempotency_key must be a UUID", nil, errTypeIdempotencyKeyInvalid)
	}
	var intent models.OccurrenceSendIntent
	if err := a.DB.Where("organization_id = ? AND idempotency_key = ? AND occurrence_id = ?", orgID, key.String(), occ.ID).First(&intent).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Send not found", nil, errTypeSendNotFound)
	}
	return a.answerSendState(r, &intent, occ, userID)
}

// answerSendState reconciles (read-only evidence, never a send) and describes one send.
//
// "confirmed" means the OUTCOME OF THE OPERATION is known, not that the customer received the
// message: sent -> sent=true, confirmed=true; failed (proven not sent) -> sent=false, confirmed=true;
// uncertain or processing -> sent=false, confirmed=false.
func (a *App) answerSendState(r *fastglue.Request, intentPtr *models.OccurrenceSendIntent, occ *models.Occurrence, userID uuid.UUID) error {
	intent := *intentPtr
	a.reconcileByDBClock(&intent)

	var msgStatus string
	a.DB.Model(&models.Message{}).Select("status").Where("id = ?", intent.MessageID).Scan(&msgStatus)
	data := intentOutcome(&intent, occ.ProtocolNumber, intent.State)
	data["state"] = intent.State
	data["message_status"] = msgStatus
	data["claimed_at"] = intent.ClaimedAt
	data["finished_at"] = intent.FinishedAt
	data["reconciled_at"] = intent.ReconciledAt
	data["own"] = intent.UserID == userID
	if intent.ResolvedAt != nil { // settled by a person: say so, never who
		data["resolved_by_person"] = true
		data["resolved_at"] = intent.ResolvedAt
		data["resolution_outcome"] = intent.ResolutionOutcome
	}
	if intent.State == models.SendIntentProcessing {
		data["detail"] = fmt.Sprintf("in progress; the owner has until its deadline (%s after it dispatched) plus a margin, and after that it is reconciled from the records", sendIntentDispatchWindow)
	}
	return r.SendEnvelope(data)
}

// --- settling an uncertain send -----------------------------------------------------------

// ResolveSendRequest is what a person states after checking the conversation with the customer.
type ResolveSendRequest struct {
	Outcome             string `json:"outcome"` // delivered | not_delivered
	Note                string `json:"note"`    // why: what the conversation showed
	ConversationChecked bool   `json:"conversation_checked"`
}

const (
	resolveNoteMin = 10
	resolveNoteMax = 1000
)

var errSendAlreadySettled = errors.New("send already settled")

// ResolveOccurrenceProtocolSend settles an uncertain send by a person's decision, after they checked the
// conversation. It sends NOTHING: it records who decided what and why, moves the send to sent (delivered) or
// failed (not_delivered) and so frees the case. The decision, the new state and the audit entry are one
// transaction, so it cannot be half done. It is the only way out of uncertain without the provider's own
// confirmation, it is not undone through the interface, and a later correction is a new audited action.
//
// It needs its own permission (occurrences:resolve_send), independent of occurrences:write and
// chat:write, AND the scope to see the case: a supervisor settles only what is under their management.
func (a *App) ResolveOccurrenceProtocolSend(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionResolveSend)
	if err != nil {
		return nil
	}
	occ, _, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	sendID, err := parsePathUUID(r, "send_id", "send")
	if err != nil {
		return nil
	}
	var req ResolveSendRequest
	if err := r.Decode(&req, "json"); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid request body", nil, "")
	}
	note := strings.TrimSpace(req.Note)
	if req.Outcome != models.SendResolutionDelivered && req.Outcome != models.SendResolutionNotDelivered {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "outcome must be delivered or not_delivered", nil, errTypeResolutionInvalid)
	}
	if !req.ConversationChecked {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Confirm that you checked the conversation with the customer", nil, errTypeResolutionInvalid)
	}
	if n := len([]rune(note)); n < resolveNoteMin || n > resolveNoteMax {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest,
			fmt.Sprintf("Say what the conversation showed (%d to %d characters)", resolveNoteMin, resolveNoteMax), nil, errTypeResolutionInvalid)
	}

	var intent models.OccurrenceSendIntent
	if err := a.DB.Where("id = ? AND organization_id = ? AND occurrence_id = ?", sendID, orgID, occ.ID).First(&intent).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Send not found", nil, errTypeSendNotFound)
	}
	// What the provider's own evidence says wins over a person's reading: settle it from the records first.
	now0, cerr := a.dbNow()
	if cerr != nil {
		a.Log.Error("Cannot settle the send: the database clock could not be read", "error", cerr, "send_id", intent.ID)
		return r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "The database could not be reached just now; nothing was changed. Try again", nil, "")
	}
	a.reconcileSendIntent(&intent, now0)
	if intent.State != models.SendIntentUncertain {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This send is not waiting to be settled (state: "+intent.State+")", nil, errTypeSendNotUncertain)
	}

	target := models.SendIntentSent
	if req.Outcome == models.SendResolutionNotDelivered {
		target = models.SendIntentFailed
	}
	now := time.Now()
	status, env := intentAnswer(&intent, occ.ProtocolNumber, target, fasthttp.StatusBadGateway, "Settled by a person: the message was not delivered", "")
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.OccurrenceSendIntent{}).
			Where("id = ? AND state = ?", intent.ID, models.SendIntentUncertain).
			Updates(map[string]any{
				"state": target, "http_status": status, "response": env, "finished_at": now, "reconciled_at": now,
				"resolved_by_id": userID, "resolved_at": now, "resolution_outcome": req.Outcome, "resolution_note": note,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errSendAlreadySettled
		}
		field := func(name string, value any) map[string]any {
			return map[string]any{"field": name, "old_value": nil, "new_value": value}
		}
		return audit.LogAuditTx(tx, orgID, userID, audit.GetUserName(tx, userID), protocolSendAuditType, occ.ID, models.AuditActionUpdated,
			field("kind", "protocol"), field("send_id", intent.ID.String()), field("protocol_number", occ.ProtocolNumber),
			field("result", "resolved_"+req.Outcome), field("outcome", req.Outcome), field("note", note),
			field("conversation_checked", true), field("previous_state", models.SendIntentUncertain), field("message_id", intent.MessageID.String()))
	})
	if errors.Is(err, errSendAlreadySettled) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "This send was settled by someone else just now", nil, errTypeSendNotUncertain)
	}
	if err != nil {
		a.Log.Error("Failed to settle the send", "error", err, "send_id", intent.ID, "occurrence", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record the decision; nothing was changed", nil, "")
	}
	a.Log.Info("Uncertain send settled by a person", "send_id", intent.ID, "occurrence", occ.ID, "user_id", userID, "outcome", req.Outcome)
	a.DB.Where("id = ?", intent.ID).First(&intent)
	return a.answerSendState(r, &intent, occ, userID)
}
