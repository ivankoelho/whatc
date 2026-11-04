package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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

	sendIntentReconcileEvery = 30 * time.Second
	sendIntentPurgeEvery     = time.Hour

	errTypeIdempotencyKeyRequired = "idempotency_key_required"
	errTypeIdempotencyKeyInvalid  = "idempotency_key_invalid"
	errTypeIdempotencyKeyReused   = "idempotency_key_reused"
	errTypeSendNotFound           = "send_not_found"

	protocolResultProcessing = models.SendIntentProcessing
)

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

// claimSendIntent tries to take the key. claimed is true for exactly one caller per key; everyone
// else gets the existing row. The INSERT ... ON CONFLICT DO NOTHING is the atomic step: two
// simultaneous requests cannot both insert.
func (a *App) claimSendIntent(orgID, userID uuid.UUID, occ *models.Occurrence, key, hash string) (*models.OccurrenceSendIntent, bool, error) {
	now := time.Now()
	intent := &models.OccurrenceSendIntent{
		ID: uuid.New(), OrganizationID: orgID, IdempotencyKey: key, OccurrenceID: occ.ID, UserID: userID,
		Kind: models.SendIntentKindProtocol, RequestHash: hash, State: models.SendIntentProcessing,
		MessageID: uuid.New(), ClaimedAt: now,
	}
	res := a.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "organization_id"}, {Name: "idempotency_key"}},
		DoNothing: true,
	}).Create(intent)
	if res.Error != nil {
		return nil, false, res.Error
	}
	if res.RowsAffected == 1 {
		return intent, true, nil
	}
	var existing models.OccurrenceSendIntent
	if err := a.DB.Where("organization_id = ? AND idempotency_key = ?", orgID, key).First(&existing).Error; err != nil {
		return nil, false, err
	}
	return &existing, false, nil
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
func (a *App) answerExistingIntent(r *fastglue.Request, intent *models.OccurrenceSendIntent, occ *models.Occurrence, userID uuid.UUID, hash string) error {
	if intent.OccurrenceID != occ.ID || intent.UserID != userID || intent.RequestHash != hash || intent.Kind != models.SendIntentKindProtocol {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			"This Idempotency-Key was already used for a different request; use a new key for a new send", nil, errTypeIdempotencyKeyReused)
	}
	a.reconcileSendIntent(intent, time.Now())
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
// itself later confirmed it), never frees a key and never sends. Returns true when it changed it.
func (a *App) reconcileSendIntent(intent *models.OccurrenceSendIntent, now time.Time) bool {
	switch {
	case intent.State == models.SendIntentProcessing && now.Sub(intent.ClaimedAt) >= sendIntentStaleAfter:
	case intent.State == models.SendIntentUncertain && intent.ReconciledAt == nil:
	default:
		return false
	}
	ev := a.sendEvidence(intent)
	var occ models.Occurrence
	_ = a.DB.Select("id", "protocol_number").Where("id = ?", intent.OccurrenceID).First(&occ).Error

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

// ReconcileSendIntents settles the claims whose owner vanished and re-checks the open uncertain
// ones. Safe in several processes: every change is a conditional UPDATE.
func (a *App) ReconcileSendIntents(now time.Time) int {
	var open []models.OccurrenceSendIntent
	a.DB.Where("(state = ? AND claimed_at <= ?) OR (state = ? AND reconciled_at IS NULL)",
		models.SendIntentProcessing, now.Add(-sendIntentStaleAfter), models.SendIntentUncertain).
		Order("claimed_at").Limit(200).Find(&open)
	n := 0
	for i := range open {
		if a.reconcileSendIntent(&open[i], now) {
			n++
		}
	}
	return n
}

// PurgeSendIntents applies the retention rule and returns how many rows it removed: only
// finished intents (sent, failed) older than SendIntentRetention, and uncertain ones that were
// reconciled that long ago. processing and unreconciled uncertain are never removed.
func (a *App) PurgeSendIntents(now time.Time) int64 {
	cutoff := now.Add(-SendIntentRetention)
	res := a.DB.Where(
		"(state IN ? AND finished_at < ?) OR (state = ? AND reconciled_at IS NOT NULL AND reconciled_at < ?)",
		[]string{models.SendIntentSent, models.SendIntentFailed}, cutoff, models.SendIntentUncertain, cutoff,
	).Delete(&models.OccurrenceSendIntent{})
	if res.Error != nil {
		a.Log.Error("Failed to purge send intents", "error", res.Error)
		return 0
	}
	return res.RowsAffected
}

// StartSendIntentReconciler runs the reconciliation and the retention until ctx ends.
func (a *App) StartSendIntentReconciler(ctx context.Context) {
	t := time.NewTicker(sendIntentReconcileEvery)
	defer t.Stop()
	var lastPurge time.Time
	for {
		now := time.Now()
		a.ReconcileSendIntents(now)
		if now.Sub(lastPurge) >= sendIntentPurgeEvery {
			a.PurgeSendIntents(now)
			lastPurge = now
		}
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
	return a.answerSendState(r, &intent, occ)
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
	key, err := uuid.Parse(strings.TrimSpace(string(r.RequestCtx.QueryArgs().Peek("idempotency_key"))))
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "The idempotency_key must be a UUID", nil, errTypeIdempotencyKeyInvalid)
	}
	var intent models.OccurrenceSendIntent
	if err := a.DB.Where("organization_id = ? AND idempotency_key = ? AND occurrence_id = ?", orgID, key.String(), occ.ID).First(&intent).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Send not found", nil, errTypeSendNotFound)
	}
	return a.answerSendState(r, &intent, occ)
}

// answerSendState reconciles (read-only evidence, never a send) and describes one send.
//
// "confirmed" means the OUTCOME OF THE OPERATION is known, not that the customer received the
// message: sent -> sent=true, confirmed=true; failed (proven not sent) -> sent=false, confirmed=true;
// uncertain or processing -> sent=false, confirmed=false.
func (a *App) answerSendState(r *fastglue.Request, intentPtr *models.OccurrenceSendIntent, occ *models.Occurrence) error {
	intent := *intentPtr
	a.reconcileSendIntent(&intent, time.Now())

	var msgStatus string
	a.DB.Model(&models.Message{}).Select("status").Where("id = ?", intent.MessageID).Scan(&msgStatus)
	data := intentOutcome(&intent, occ.ProtocolNumber, intent.State)
	data["state"] = intent.State
	data["message_status"] = msgStatus
	data["claimed_at"] = intent.ClaimedAt
	data["finished_at"] = intent.FinishedAt
	data["reconciled_at"] = intent.ReconciledAt
	if intent.State == models.SendIntentProcessing {
		data["detail"] = fmt.Sprintf("in progress; it is reconciled from the records after %s without an answer", sendIntentStaleAfter)
	}
	return r.SendEnvelope(data)
}
