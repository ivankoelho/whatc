package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The agent's wait before answering ("espera inteligente").
//
// A customer who writes in several short messages should get ONE answer, produced after they
// paused. The wait lives in ai_pending_replies, a database table, so it survives a restart:
//
//   - a message that reaches the agent's turn does not call the model; it is added to the contact's
//     pending row and moves its due time to now + the agent's wait (never beyond aiWaitMaxHold after
//     the first grouped message, so a customer who never stops cannot silence the agent forever);
//   - a worker claims rows whose time came (FOR UPDATE SKIP LOCKED, so several instances never take
//     the same one), builds ONE prompt from all the grouped messages and calls the model ONCE;
//   - a message that arrives while the answer is being produced does not interrupt it (no tokens are
//     thrown away): it opens the next pending row, which is only claimed after the first finished;
//   - a row whose worker died is taken back after its lease, unless an answer already left (then it
//     is closed, never repeated).
//
// The human-takeover guarantees are untouched: before the model is called the worker looks for a
// takeover, and the send itself still goes through sendAIReplyIfNoHuman (the per-contact lock).

const (
	// aiWaitMaxHold: the longest a customer's messages are held before the agent answers anyway.
	aiWaitMaxHold = 2 * time.Minute
	// aiWaitMaxAttempts: how many times a row whose worker died is retried before it is closed.
	aiWaitMaxAttempts = 3
	// aiWaitWorkers: how many pending replies are answered at the same time by one instance.
	aiWaitWorkers = 4
)

var (
	// aiWaitPollEvery: how often the worker looks for rows that are due.
	aiWaitPollEvery = time.Second
	// aiWaitLease: how long a claimed row is considered alive. It must cover the longest answer: a
	// model call may take aiCallDeadline, twice when the safety filter regenerates, plus the handoff.
	aiWaitLease = 10 * time.Minute
)

// Outcomes recorded in AIPendingReply.Outcome.
const (
	aiOutcomeReplied       = "replied"        // the agent answered
	aiOutcomeHandoff       = "handoff"        // the agent handed the conversation to the team
	aiOutcomeNotSent       = "not_sent"       // an answer was produced but discarded (human took over meanwhile)
	aiOutcomeFallback      = "fallback"       // the model failed or was empty: the configured fallback message
	aiOutcomeNoAnswer      = "no_answer"      // nothing was sent (a single model failure, or AI not configured)
	aiOutcomeHumanActive   = "human_active"   // a human owned the conversation: the model was not called
	aiOutcomeFlowActive    = "flow_active"    // the customer entered a flow meanwhile
	aiOutcomeNoText        = "no_text"        // the grouped messages could not be read
	aiOutcomeGone          = "gone"           // the contact, account or session no longer exists
	aiOutcomeRecovered     = "recovered"      // a dead worker had already answered: closed, not repeated
	aiOutcomeAbandoned     = "abandoned"      // retries exhausted
	aiOutcomeAIOff         = "ai_off"         // the chatbot or the AI was switched off while waiting
	aiOutcomePanic         = "error"          // unexpected failure while answering
	aiOutcomeMerged        = "merged"         // taken back, but the customer had written again: its messages joined the newer pending reply
	aiOutcomeAmbiguousSend = "ambiguous_send" // the send may or may not have reached the customer: never resent
	aiOutcomeSendFailed    = "send_failed"    // the send provably did not reach the customer
	aiOutcomeLostLease     = "lost_lease"     // another worker owns the reply now; this one sent nothing
	aiOutcomeReclaimFailed = "reclaim_failed" // could not be taken back; closed so it cannot block the contact
)

// aiAgentWait reports the wait that applies to this number's agent, or 0 (answer at once). It
// resolves the agent exactly as the answer will (resolveAIAgent), so a number the agent is not
// allowed on never waits.
func (a *App) aiAgentWait(settings *models.ChatbotSettings, account *models.WhatsAppAccount) (*models.AIAgent, int) {
	agent := a.resolveAIAgent(settings, account)
	if agent == nil || agent.WaitSeconds <= 0 {
		return agent, 0
	}
	return agent, agent.WaitSeconds
}

// enqueueAIWait adds the customer's message to the contact's pending reply, creating it when there
// is none, and moves the due time. receivedAt is when the webhook handler first saw the message.
func (a *App) enqueueAIWait(agent *models.AIAgent, account *models.WhatsAppAccount, contact *models.Contact,
	session *models.ChatbotSession, wamid string, receivedAt time.Time, isNewSession bool) error {
	wait := time.Duration(agent.WaitSeconds) * time.Second
	for attempt := 0; attempt < 3; attempt++ {
		res := a.DB.Exec(`
			UPDATE ai_pending_replies SET
				due_at          = LEAST(?::timestamptz, first_message_at + make_interval(secs => ?)),
				last_message_at = ?,
				last_message_id = ?,
				wait_seconds    = ?,
				message_ids     = CASE WHEN message_ids @> jsonb_build_array(?::text)
				                       THEN message_ids ELSE message_ids || jsonb_build_array(?::text) END,
				updated_at      = ?
			WHERE contact_id = ? AND status = ? AND deleted_at IS NULL`,
			receivedAt.Add(wait), aiWaitMaxHold.Seconds(),
			receivedAt, wamid, agent.WaitSeconds, wamid, wamid, time.Now(),
			contact.ID, models.AIPendingStatusPending)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			return nil
		}
		row := models.AIPendingReply{
			BaseModel:       models.BaseModel{ID: uuid.New()},
			OrganizationID:  account.OrganizationID,
			ContactID:       contact.ID,
			SessionID:       session.ID,
			AgentID:         agent.ID,
			WhatsAppAccount: account.Name,
			IsNewSession:    isNewSession,
			Status:          models.AIPendingStatusPending,
			DueAt:           receivedAt.Add(wait),
			FirstMessageAt:  receivedAt,
			LastMessageAt:   receivedAt,
			MessageIDs:      models.StringArray{wamid},
			LastMessageID:   wamid,
			WaitSeconds:     agent.WaitSeconds,
		}
		err := a.DB.Create(&row).Error
		if err == nil {
			a.Log.Info("AI agent waiting before answering", "contact_id", contact.ID, "wait_seconds", agent.WaitSeconds)
			return nil
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" { // another message of this contact created it first: extend that one
			continue
		}
		return err
	}
	return errors.New("could not queue the AI wait")
}

// claimDueAIReplies atomically takes up to limit rows whose time came. A contact with an answer in
// progress is skipped (its next row waits), so a contact is never answered twice at once.
func (a *App) claimDueAIReplies(now time.Time, limit int) ([]models.AIPendingReply, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []models.AIPendingReply
	err := a.DB.Raw(`
		UPDATE ai_pending_replies SET status = ?, started_at = ?, locked_until = ?, attempts = attempts + 1, updated_at = ?
		WHERE id IN (
			SELECT p.id FROM ai_pending_replies p
			WHERE p.status = ? AND p.due_at <= ? AND p.deleted_at IS NULL
			  AND NOT EXISTS (SELECT 1 FROM ai_pending_replies q
			                  WHERE q.contact_id = p.contact_id AND q.status = ? AND q.deleted_at IS NULL)
			ORDER BY p.due_at
			LIMIT ?
			FOR UPDATE SKIP LOCKED)
		RETURNING *`,
		models.AIPendingStatusProcessing, now, now.Add(aiWaitLease), now,
		models.AIPendingStatusPending, now, models.AIPendingStatusProcessing, limit).Scan(&rows).Error
	return rows, err
}

// reclaimStaleAIReplies deals with rows whose worker died or stalled (lease expired). What it may do
// depends on one persisted fact, SendStartedAt, written before the answer is handed to WhatsApp:
//
//   - no marker: nothing reached the customer. The row runs again (up to aiWaitMaxAttempts), or, if the
//     customer wrote again meanwhile, its messages join that newer pending reply (aiOutcomeMerged);
//   - marker: the message may have left. If a message of that contact with a WhatsApp id was recorded
//     since, it did (recovered); otherwise nobody can know (ambiguous_send). Either way it is closed and
//     NEVER sent again: a missing answer is recoverable, a duplicate is not.
//
// Every update names the attempt it saw, so two instances (or a worker that comes back to life) cannot
// both act on the same row, and a failed update never leaves the row blocking its contact.
func (a *App) reclaimStaleAIReplies(now time.Time) {
	var stale []models.AIPendingReply
	if err := a.DB.Where("status = ? AND locked_until < ?", models.AIPendingStatusProcessing, now).Find(&stale).Error; err != nil {
		a.Log.Error("Failed to look for stale AI replies", "error", err)
		return
	}
	for i := range stale {
		row := &stale[i]
		switch {
		case row.SendStartedAt != nil:
			var delivered int64
			a.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ? AND created_at >= ? AND whats_app_message_id <> ''",
				row.ContactID, models.DirectionOutgoing, *row.SendStartedAt).Count(&delivered)
			outcome := aiOutcomeAmbiguousSend
			if delivered > 0 {
				outcome = aiOutcomeRecovered
			}
			a.closeStaleAIReply(row, outcome, now)
		case row.Attempts >= aiWaitMaxAttempts:
			a.closeStaleAIReply(row, aiOutcomeAbandoned, now)
			a.Log.Warn("AI reply abandoned after repeated failures", "contact_id", row.ContactID)
		default:
			if err := a.requeueStaleAIReply(row, now); err != nil {
				// It could not be put back (or merged). Leaving it in "processing" would block the contact's
				// next reply for good, so it is closed; the customer's messages are still in the conversation.
				a.Log.Error("Failed to take an AI reply back; closing it", "error", err, "contact_id", row.ContactID, "row_id", row.ID)
				a.closeStaleAIReply(row, aiOutcomeReclaimFailed, now)
			}
		}
	}
}

// closeStaleAIReply closes a row that is still the stale attempt that was seen.
func (a *App) closeStaleAIReply(row *models.AIPendingReply, outcome string, now time.Time) {
	res := a.DB.Model(&models.AIPendingReply{}).
		Where("id = ? AND status = ? AND attempts = ?", row.ID, models.AIPendingStatusProcessing, row.Attempts).
		Updates(map[string]any{"status": models.AIPendingStatusDone, "outcome": outcome, "finished_at": now, "locked_until": nil})
	if res.Error != nil {
		a.Log.Error("Failed to close a stale AI reply", "error", res.Error, "row_id", row.ID)
	}
}

// requeueStaleAIReply puts a row that never started sending back to pending, or merges it into the
// contact's newer pending row. Both happen in one transaction.
func (a *App) requeueStaleAIReply(row *models.AIPendingReply, now time.Time) error {
	return a.DB.Transaction(func(tx *gorm.DB) error {
		// Still the attempt we saw? (another instance may have handled it already)
		var current models.AIPendingReply
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND attempts = ?", row.ID, models.AIPendingStatusProcessing, row.Attempts).
			First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var newer models.AIPendingReply
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("contact_id = ? AND status = ? AND deleted_at IS NULL", row.ContactID, models.AIPendingStatusPending).
			First(&newer).Error
		switch {
		case err == nil:
			// The customer wrote again: one pending row per contact. The old messages came first, in order,
			// then the newer ones; the newer row keeps its own timer, and starts from the old row's first
			// message so the history sent to the model leaves out all of them.
			merged := append(models.StringArray{}, current.MessageIDs...)
			seen := map[string]bool{}
			for _, id := range merged {
				seen[id] = true
			}
			for _, id := range newer.MessageIDs {
				if !seen[id] {
					merged = append(merged, id)
				}
			}
			attempts := newer.Attempts
			if current.Attempts > attempts {
				attempts = current.Attempts
			}
			if err := tx.Model(&models.AIPendingReply{}).Where("id = ?", newer.ID).Updates(map[string]any{
				"message_ids": merged, "first_message_at": current.FirstMessageAt, "attempts": attempts, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			return tx.Model(&models.AIPendingReply{}).Where("id = ?", current.ID).Updates(map[string]any{
				"status": models.AIPendingStatusDone, "outcome": aiOutcomeMerged, "finished_at": now, "locked_until": nil,
			}).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Model(&models.AIPendingReply{}).Where("id = ?", current.ID).Updates(map[string]any{
				"status": models.AIPendingStatusPending, "locked_until": nil, "due_at": now, "send_started_at": nil,
			}).Error
		default:
			return err
		}
	})
}

// StartAIWaitWorker runs until ctx ends: it recovers rows left behind by a previous run, then keeps
// answering the rows that come due. Several instances may run it.
func (a *App) StartAIWaitWorker(ctx context.Context) {
	slots := make(chan struct{}, aiWaitWorkers)
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(aiWaitPollEvery)
	defer t.Stop()
	for {
		now := time.Now()
		a.reclaimStaleAIReplies(now)
		rows, err := a.claimDueAIReplies(now, cap(slots)-len(slots))
		if err != nil {
			a.Log.Error("Failed to claim AI replies", "error", err)
		}
		for i := range rows {
			slots <- struct{}{}
			wg.Add(1)
			go func(row models.AIPendingReply) {
				defer wg.Done()
				defer func() { <-slots }()
				a.processAIPendingReply(&row)
			}(rows[i])
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// drainAIWait claims and answers, one after another, everything due at now. For tests and tools: the
// production path is StartAIWaitWorker.
func (a *App) drainAIWait(now time.Time) int {
	n := 0
	for {
		rows, err := a.claimDueAIReplies(now, aiWaitWorkers)
		if err != nil || len(rows) == 0 {
			return n
		}
		var wg sync.WaitGroup
		for i := range rows {
			wg.Add(1)
			go func(row models.AIPendingReply) {
				defer wg.Done()
				a.processAIPendingReply(&row)
			}(rows[i])
		}
		wg.Wait()
		n += len(rows)
	}
}

// processAIPendingReply produces the one answer of a claimed row and closes it.
func (a *App) processAIPendingReply(row *models.AIPendingReply) {
	var typing aiTypingStats
	outcome := aiOutcomePanic
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("AI pending reply panicked", "contact_id", row.ContactID, "panic", r)
		}
		now := time.Now()
		// only if this attempt still owns the row: a worker that lost its claim (or whose row was closed by
		// someone else) must not overwrite what happened since
		a.DB.Model(&models.AIPendingReply{}).Where("id = ? AND status = ? AND attempts = ?", row.ID, models.AIPendingStatusProcessing, row.Attempts).Updates(map[string]any{
			"status": models.AIPendingStatusDone, "outcome": outcome, "finished_at": now, "locked_until": nil,
			"typing_starts": typing.Starts, "typing_renewals": typing.Renewals,
			"typing_failures": typing.Failures, "typing_last_error": typing.LastError,
		})
	}()
	outcome = a.runAIPendingReply(row, &typing)
}

func (a *App) runAIPendingReply(row *models.AIPendingReply, typing *aiTypingStats) string {
	var account models.WhatsAppAccount
	if err := a.DB.Where("organization_id = ? AND name = ?", row.OrganizationID, row.WhatsAppAccount).First(&account).Error; err != nil {
		return aiOutcomeGone
	}
	var contact models.Contact
	if err := a.DB.Where("id = ? AND organization_id = ?", row.ContactID, row.OrganizationID).First(&contact).Error; err != nil {
		return aiOutcomeGone
	}
	// A human who took over while the customer was still writing wins before any token is spent.
	if a.humanTookOver(row.OrganizationID, row.ContactID) {
		return aiOutcomeHumanActive
	}
	settings, err := a.getChatbotSettingsCached(row.OrganizationID, account.Name)
	if err != nil || settings == nil || !settings.IsEnabled || !settings.AI.Enabled {
		return aiOutcomeAIOff
	}
	var session models.ChatbotSession
	if err := a.DB.Where("id = ?", row.SessionID).First(&session).Error; err != nil {
		return aiOutcomeGone
	}
	if session.CurrentFlowID != nil { // the customer was taken into a flow meanwhile: the flow owns the conversation
		return aiOutcomeFlowActive
	}
	text, turnStart := a.turnCustomerText(row)
	if text == "" {
		return aiOutcomeNoText
	}

	turn := aiTurn{typingTo: row.LastMessageID, historyBefore: turnStart, typing: typing,
		fence: &aiSendFence{rowID: row.ID, attempt: row.Attempts}}
	return a.answerWithAI(&account, &contact, &session, settings, text, row.IsNewSession, turn)
}

// aiAnsweredOutcomes are the outcomes of a pending reply whose customer messages must not be answered
// again: the customer got an answer (or may have: ambiguous_send, never repeated).
var aiAnsweredOutcomes = []string{aiOutcomeReplied, aiOutcomeHandoff, aiOutcomeFallback, aiOutcomeRecovered, aiOutcomeAmbiguousSend}

// turnCustomerText rebuilds, from the messages table, what the customer said in the turn this row
// answers, in the order it was said. The database of messages is the source of truth: the row's own
// list of ids can be incomplete (a merge that failed, a row closed as reclaim_failed) and the
// session history may be switched off, so neither is relied on.
//
// The turn is the customer's messages in the window (from, to]:
//
//   - to is the row's latest message. Whatever arrived after it belongs to a later turn (it has, or
//     will have, its own pending reply): a message that comes while the answer is being generated is
//     never folded into the answer being generated;
//   - from is the latest of (a) the last message sent TO the contact before this turn began (the
//     agent, the chatbot or a person: what the customer says after that is what has not been answered;
//     a message that FAILED to send does not count: nothing reached the customer, so what they said
//     before it is still unanswered)
//     and (b) the last message of the most recent pending reply that was answered. (b) is what keeps
//     a message that arrived during the previous generation, before that answer went out, from being
//     answered twice, and it keeps A and B out of the next turn after they were answered.
//
// Only the customer's own messages are used: direction incoming, no sender user, with text.
// Messages sent by people or the system, and internal notes, are outgoing and never enter.
//
// It returns the text and the instant of the first message in it (the history sent to the model
// stops before it, so those messages are not repeated there).
func (a *App) turnCustomerText(row *models.AIPendingReply) (string, time.Time) {
	base := a.DB.Model(&models.Message{}).Where("organization_id = ? AND contact_id = ? AND whats_app_account = ?",
		row.OrganizationID, row.ContactID, row.WhatsAppAccount)

	// to: the latest of this row's own messages (falls back to the time it was last extended)
	to := row.LastMessageAt
	if len(row.MessageIDs) > 0 {
		var latest sql.NullTime
		base.Session(&gorm.Session{}).Where("direction = ? AND whats_app_message_id IN ?", models.DirectionIncoming, []string(row.MessageIDs)).
			Select("MAX(created_at)").Scan(&latest)
		if latest.Valid {
			to = latest.Time
		}
	}

	// from
	var from time.Time
	var out sql.NullTime
	base.Session(&gorm.Session{}).Where("direction = ? AND status <> ? AND created_at < ?", models.DirectionOutgoing, models.MessageStatusFailed, row.FirstMessageAt).
		Select("MAX(created_at)").Scan(&out)
	if out.Valid {
		from = out.Time
	}
	var answered sql.NullTime
	a.DB.Raw(`SELECT MAX(m.created_at)
		FROM ai_pending_replies r
		CROSS JOIN LATERAL jsonb_array_elements_text(r.message_ids) AS w(id)
		JOIN messages m ON m.whats_app_message_id = w.id AND m.contact_id = r.contact_id
		WHERE r.contact_id = ? AND r.id <> ? AND r.status = ? AND r.outcome IN ? AND r.deleted_at IS NULL`,
		row.ContactID, row.ID, models.AIPendingStatusDone, aiAnsweredOutcomes).Scan(&answered)
	if answered.Valid && answered.Time.After(from) {
		from = answered.Time
	}

	q := base.Session(&gorm.Session{}).Where("direction = ? AND sent_by_user_id IS NULL AND created_at <= ? AND content <> ''", models.DirectionIncoming, to)
	if !from.IsZero() {
		q = q.Where("created_at > ?", from)
	}
	var msgs []models.Message
	if err := q.Order("created_at ASC, id ASC").Find(&msgs).Error; err != nil {
		a.Log.Error("Failed to read the customer messages of an AI turn", "error", err, "contact_id", row.ContactID)
		return "", time.Time{}
	}

	parts := make([]string, 0, len(msgs))
	var start time.Time
	for _, m := range msgs {
		if t := strings.TrimSpace(m.Content); t != "" {
			if start.IsZero() {
				start = m.CreatedAt
			}
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n"), start
}
