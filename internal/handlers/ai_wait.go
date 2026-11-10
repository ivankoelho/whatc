package handlers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
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
	aiOutcomeReplied     = "replied"      // the agent answered
	aiOutcomeHandoff     = "handoff"      // the agent handed the conversation to the team
	aiOutcomeNotSent     = "not_sent"     // an answer was produced but discarded (human took over meanwhile)
	aiOutcomeFallback    = "fallback"     // the model failed or was empty: the configured fallback message
	aiOutcomeNoAnswer    = "no_answer"    // nothing was sent (a single model failure, or AI not configured)
	aiOutcomeHumanActive = "human_active" // a human owned the conversation: the model was not called
	aiOutcomeFlowActive  = "flow_active"  // the customer entered a flow meanwhile
	aiOutcomeNoText      = "no_text"      // the grouped messages could not be read
	aiOutcomeGone        = "gone"         // the contact, account or session no longer exists
	aiOutcomeRecovered   = "recovered"    // a dead worker had already answered: closed, not repeated
	aiOutcomeAbandoned   = "abandoned"    // retries exhausted
	aiOutcomeAIOff       = "ai_off"       // the chatbot or the AI was switched off while waiting
	aiOutcomePanic       = "error"        // unexpected failure while answering
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

// reclaimStaleAIReplies deals with rows whose worker died (lease expired). A row whose answer
// already left is closed, never repeated; any other is put back to run again, up to
// aiWaitMaxAttempts times. Safe to run on several instances at once (each update re-checks the row).
func (a *App) reclaimStaleAIReplies(now time.Time) {
	var stale []models.AIPendingReply
	if err := a.DB.Where("status = ? AND locked_until < ?", models.AIPendingStatusProcessing, now).Find(&stale).Error; err != nil {
		a.Log.Error("Failed to look for stale AI replies", "error", err)
		return
	}
	for i := range stale {
		row := &stale[i]
		guard := a.DB.Model(&models.AIPendingReply{}).Where("id = ? AND status = ? AND locked_until < ?", row.ID, models.AIPendingStatusProcessing, now)
		since := row.CreatedAt
		if row.StartedAt != nil {
			since = *row.StartedAt
		}
		var answered int64
		a.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ? AND created_at >= ?", row.ContactID, models.DirectionOutgoing, since).Count(&answered)
		switch {
		case answered > 0:
			guard.Updates(map[string]any{"status": models.AIPendingStatusDone, "outcome": aiOutcomeRecovered, "finished_at": now})
		case row.Attempts >= aiWaitMaxAttempts:
			guard.Updates(map[string]any{"status": models.AIPendingStatusDone, "outcome": aiOutcomeAbandoned, "finished_at": now})
			a.Log.Warn("AI reply abandoned after repeated failures", "contact_id", row.ContactID)
		default:
			guard.Updates(map[string]any{"status": models.AIPendingStatusPending, "locked_until": nil, "due_at": now})
		}
	}
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
		a.DB.Model(&models.AIPendingReply{}).Where("id = ?", row.ID).Updates(map[string]any{
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
	text := a.groupedCustomerText(row)
	if text == "" {
		return aiOutcomeNoText
	}

	turn := aiTurn{typingTo: row.LastMessageID, historyBefore: row.FirstMessageAt, typing: typing}
	return a.answerWithAI(&account, &contact, &session, settings, text, row.IsNewSession, turn)
}

// groupedCustomerText joins, in the order received, the text of the messages the row grouped. They
// are read from the messages table (the row keeps only their WhatsApp ids).
func (a *App) groupedCustomerText(row *models.AIPendingReply) string {
	if len(row.MessageIDs) == 0 {
		return ""
	}
	var msgs []models.Message
	a.DB.Where("organization_id = ? AND contact_id = ? AND direction = ? AND whats_app_message_id IN ?",
		row.OrganizationID, row.ContactID, models.DirectionIncoming, []string(row.MessageIDs)).
		Order("created_at ASC").Find(&msgs)
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if t := strings.TrimSpace(m.Content); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}
