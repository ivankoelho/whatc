package handlers

import (
	"errors"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// Monthly token cap of an organization's AI agent.
//
// Consumption is read from AIUsageLog (what the organization's chatbot AI really used this
// month) plus RESERVATIONS: before each agent call, tokens for that call are reserved as a row of
// the same table, under a per-organization advisory lock, only if consumption + reservations +
// this call still fit the cap. Concurrent calls therefore cannot all pass the check and then all
// spend: the sum of what is spent and what is in flight never exceeds the cap, except for the
// error of estimating a call's input size (the estimate is deliberately pessimistic).
//
// A reservation is not allowed to disappear under a live call: every agent call runs under
// aiCallDeadline, which is much shorter than aiReservationStale, so a reservation only goes stale
// when its process died. When the call ends, the REAL usage rows are written first and only then
// is the reservation closed, so consumption is never momentarily zero; if the real rows could not
// be written, the reservation is kept as consumption (it over-counts, never under-counts).

// aiCallDeadline is the longest an agent call (including its tool loop) may take. A variable only
// so a test can shorten it.
var aiCallDeadline = 2 * time.Minute

const (
	// aiReservationStale is how long a reservation counts before it is presumed abandoned (the
	// process that made it died). It must stay far above aiCallDeadline.
	aiReservationStale = 10 * time.Minute

	aiFeatureQualification           = "agent_qualification"
	aiFeatureReservation             = "ai_reservation"
	aiFeatureReservationClosed       = "ai_reservation_closed"
	aiFeatureReservationUnreconciled = "ai_reservation_unreconciled"

	// aiDefaultReserveOutput is what is reserved for the answer when the request sets no limit.
	aiDefaultReserveOutput = 1000
)

// ErrAICapReached means the organization's monthly AI token cap would be exceeded by this call.
var ErrAICapReached = errors.New("monthly AI token limit reached")

// aiCapZone is the calendar the monthly cap follows.
var aiCapZone = func() *time.Location {
	if loc, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
		return loc
	}
	return time.FixedZone("BRT", -3*60*60) // Brazil has no DST since 2019
}()

// aiCapMonth returns the [start, end) of the calendar month that contains now.
func aiCapMonth(now time.Time) (time.Time, time.Time) {
	n := now.In(aiCapZone)
	start := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, aiCapZone)
	return start, start.AddDate(0, 1, 0)
}

func aiCapLockKey(orgID uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("ai-token-cap:"))
	_, _ = h.Write(orgID[:])
	return int64(h.Sum64())
}

// estimateAITokens is a pessimistic token estimate of one request: the answer limit plus the
// input counted at one token per two bytes (real text is nearer one per three or four).
func estimateAITokens(req ai.Request) int64 {
	out := int64(req.MaxTokens)
	if out <= 0 {
		out = aiDefaultReserveOutput
	}
	in := len(req.System)
	for _, m := range req.Messages {
		in += len(m.Content)
	}
	return out + int64(in)/2 + 1
}

// aiReservation is tokens set aside for one in-flight agent call.
type aiReservation struct {
	a           *App
	id          uuid.UUID
	tokens      int64
	usageFailed atomic.Bool
	once        sync.Once
}

// noteUsageFailure records that the call's real usage row could not be written.
func (r *aiReservation) noteUsageFailure() { r.usageFailed.Store(true) }

// Settle ends the reservation: it is closed (counts for nothing; the real usage rows now carry the
// consumption) or, if writing those rows failed, kept as consumption. Safe to call more than once.
func (r *aiReservation) Settle() {
	r.once.Do(func() {
		feature, tokens := aiFeatureReservationClosed, 0
		if r.usageFailed.Load() {
			feature, tokens = aiFeatureReservationUnreconciled, int(r.tokens)
		}
		if err := r.a.DB.Model(&models.AIUsageLog{}).Where("id = ?", r.id).
			Updates(map[string]any{"feature": feature, "total_tokens": tokens}).Error; err != nil {
			// The reservation stays as it is and keeps counting until it goes stale: conservative.
			r.a.Log.Error("Failed to settle an AI token reservation", "error", err, "reservation_id", r.id)
		}
	})
}

// aiTokensUsed is the organization's consumption in the month of now: real usage of the chatbot
// AI plus unfinished reservations and reservations kept as consumption.
func aiTokensUsed(tx *gorm.DB, orgID uuid.UUID, now time.Time) (int64, error) {
	start, end := aiCapMonth(now)
	var used int64
	err := tx.Raw(`
		SELECT COALESCE(SUM(total_tokens), 0) FROM ai_usage_logs
		WHERE organization_id = ? AND created_at >= ? AND created_at < ?
		  AND (feature IN (?, ?, ?, ?) OR (feature = ? AND created_at > ?))`,
		orgID, start, end,
		aiFeatureChatbotReply, aiFeatureChatbotNode, aiFeatureQualification, aiFeatureReservationUnreconciled,
		aiFeatureReservation, now.Add(-aiReservationStale),
	).Scan(&used).Error
	return used, err
}

// reserveAITokens reserves tokens for one call, or returns ErrAICapReached if consumption plus
// reservations plus tokens would pass limit. A failure of the database is returned as is: the call
// does not go ahead (fail closed).
func (a *App) reserveAITokens(orgID uuid.UUID, limit, tokens int64, now time.Time) (*aiReservation, error) {
	res := &aiReservation{a: a, id: uuid.New(), tokens: tokens}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", aiCapLockKey(orgID)).Error; err != nil {
			return err
		}
		used, err := aiTokensUsed(tx, orgID, now)
		if err != nil {
			return err
		}
		if used+tokens > limit {
			return ErrAICapReached
		}
		return tx.Create(&models.AIUsageLog{
			ID: res.id, OrganizationID: orgID, Feature: aiFeatureReservation,
			TotalTokens: int(tokens), Success: true, CreatedAt: now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
