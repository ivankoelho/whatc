package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm/clause"
)

// sweepBatchSize is the page size of the integrity job. A technical limit of the
// implementation, not an operational parameter: the job pages by key in batches,
// so it never loads the whole window in memory. Changing it changes nothing but
// the number of round trips.
const sweepBatchSize = 500

// SweepResult counts what one integrity pass did.
type SweepResult struct {
	Inferred    int // messages that had no ledger row and got one (origin inferred)
	Unlinked    int // wamids with Meta events but no message, recorded as `unlinked`
	Resettled   int // pending rows re-settled (unconfirmed after the deadline, or events missed)
	Errors      int
	LastErrText string
}

// Sweep is the integrity job: it makes the ledger complete and settled. It is
// idempotent, resumable (keyset pages) and safe to run concurrently with itself,
// with the send path and with the webhook: every write goes through the unique
// keys (ON CONFLICT DO NOTHING) or the row-locking Settle. All its windows and
// deadlines come from the configuration.
func (r *Recorder) Sweep(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	if !r.Enabled() {
		return res, nil
	}
	// Measurement starts when the first ledger row was written. Nothing before that
	// instant is reconstructed: with an empty ledger there is no baseline yet and the
	// job does nothing at all.
	base, err := r.baseline(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Errors++
		res.LastErrText = err.Error()
		return res, nil
	}
	if base == nil {
		return res, nil
	}
	steps := []func(context.Context, time.Time, time.Time, *SweepResult) error{r.sweepMissingRows, r.sweepUnlinked, r.sweepPending}
	for _, step := range steps {
		if err := step(ctx, now, *base, &res); err != nil {
			res.Errors++
			res.LastErrText = err.Error()
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
		}
	}
	return res, nil
}

// sweepMissingRows gives a ledger row to every message of the window that has none
// (recording failed, or the process died between creating the message and
// recording it). Messages younger than one interval are left alone so the
// normal path can record them with the real origin.
func (r *Recorder) sweepMissingRows(ctx context.Context, now, base time.Time, res *SweepResult) error {
	from := now.Add(-r.Cfg.IntegrityWindow())
	if from.Before(base) {
		from = base // never before the start of the measurement
	}
	to := now.Add(-r.Cfg.IntegrityInterval())
	curAt, curID := from, uuid.Nil
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var msgs []models.Message
		if err := r.DB.WithContext(ctx).
			Joins("LEFT JOIN message_usage u ON u.message_id = messages.id").
			Where("u.id IS NULL AND messages.created_at <= ?", to).
			Where("(messages.created_at, messages.id) > (?, ?)", curAt, curID).
			Order("messages.created_at, messages.id").Limit(sweepBatchSize).
			Find(&msgs).Error; err != nil {
			return err
		}
		for i := range msgs {
			m := &msgs[i]
			if err := r.recordInferred(ctx, m); err != nil {
				res.Errors++
				res.LastErrText = err.Error()
				continue
			}
			res.Inferred++
		}
		if len(msgs) < sweepBatchSize {
			return nil
		}
		last := msgs[len(msgs)-1]
		curAt, curID = last.CreatedAt, last.ID
	}
}

// recordInferred creates the row of a message found without one, with an origin
// guessed from what the message itself says, and settles it.
func (r *Recorder) recordInferred(ctx context.Context, m *models.Message) error {
	o := Origin{ActorType: ActorSystem, Detail: "unclassified", Inferred: true}
	switch {
	case m.Direction == models.DirectionIncoming:
		o = Origin{ActorType: ActorContact, Inferred: true}
	case m.SentByUserID != nil:
		o = Origin{ActorType: ActorAgent, ActorUserID: m.SentByUserID, Inferred: true}
	default:
		if s, _ := m.Metadata["campaign_id"].(string); s != "" {
			if id, err := uuid.Parse(s); err == nil {
				o = Origin{ActorType: ActorCampaign, CampaignID: &id, Detail: "campaign", Inferred: true}
			}
		}
	}
	if err := r.Record(ctx, nil, m, o); err != nil {
		return err
	}
	switch {
	case m.Direction == models.DirectionOutgoing && m.WhatsAppMessageID == "" && m.Status == models.MessageStatusFailed:
		return r.MarkSendFailed(ctx, nil, m.ID)
	case m.WhatsAppMessageID != "":
		return r.Settle(ctx, m.OrganizationID, m.WhatsAppAccount, m.WhatsAppMessageID)
	}
	return nil
}

// unlinkedCandidate is a wamid Meta reported on that has no ledger row.
type unlinkedCandidate struct {
	OrganizationID   uuid.UUID
	WhatsAppAccount  string `gorm:"column:whatsapp_account"`
	Wamid            string
	FirstEventAt     time.Time
	RecipientCountry string
}

// sweepUnlinked records, as `unlinked`, the wamids Meta reported on for which no
// message was found after the reconciliation wait. It keeps the known cost
// reachable by wamid; it is not treated as a loss, and a later send/record links it.
func (r *Recorder) sweepUnlinked(ctx context.Context, now, base time.Time, res *SweepResult) error {
	from := now.Add(-r.Cfg.IntegrityWindow())
	to := now.Add(-r.Cfg.UnlinkedAfter())
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var cands []unlinkedCandidate
		if err := r.DB.WithContext(ctx).Raw(`
			SELECT e.organization_id, e.whatsapp_account, e.wamid,
			       MIN(e.event_at) AS first_event_at, MAX(e.recipient_country) AS recipient_country
			FROM message_pricing_events e
			WHERE e.received_at >= ? AND e.wamid > ?
			  AND NOT EXISTS (SELECT 1 FROM message_usage u
			                  WHERE u.organization_id = e.organization_id AND u.whatsapp_account = e.whatsapp_account AND u.wamid = e.wamid)
			  AND NOT EXISTS (SELECT 1 FROM messages m
			                  WHERE m.organization_id = e.organization_id AND m.whats_app_message_id = e.wamid AND m.created_at < ?)
			GROUP BY e.organization_id, e.whatsapp_account, e.wamid
			HAVING MIN(e.received_at) <= ?
			ORDER BY e.wamid LIMIT ?`, from, cursor, base, to, sweepBatchSize).Scan(&cands).Error; err != nil {
			return err
		}
		for _, c := range cands {
			row := models.MessageUsage{
				OrganizationID: c.OrganizationID, WhatsAppAccount: c.WhatsAppAccount, Wamid: c.Wamid, Direction: "outgoing",
				RecipientCountry: c.RecipientCountry, ActorType: string(ActorSystem), OriginDetail: "unlinked", OriginInferred: true,
				UnitSource: string(UnitNone), Quantity: 1, BillingState: string(StatePending), LinkState: string(LinkUnlinked), SentAt: c.FirstEventAt,
			}
			cr := r.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if cr.Error != nil {
				res.Errors++
				res.LastErrText = cr.Error.Error()
				continue
			}
			if cr.RowsAffected > 0 {
				res.Unlinked++
				if err := r.Settle(ctx, c.OrganizationID, c.WhatsAppAccount, c.Wamid); err != nil {
					res.Errors++
					res.LastErrText = err.Error()
				}
			}
		}
		if len(cands) < sweepBatchSize {
			return nil
		}
		cursor = cands[len(cands)-1].Wamid
	}
}

// sweepPending re-settles `pending` rows that either passed the undelivered
// deadline (they become `unconfirmed`) or have a delivery/failure event the
// webhook failed to settle.
func (r *Recorder) sweepPending(ctx context.Context, now, _ time.Time, res *SweepResult) error {
	deadline := now.Add(-r.Cfg.UndeliveredAfter())
	cursor := uuid.Nil
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []models.MessageUsage
		if err := r.DB.WithContext(ctx).
			Where("billing_state = ? AND wamid <> '' AND id > ?", string(StatePending), cursor).
			Where(`sent_at <= ? OR EXISTS (SELECT 1 FROM message_pricing_events e
			        WHERE e.organization_id = message_usage.organization_id AND e.whatsapp_account = message_usage.whatsapp_account
			          AND e.wamid = message_usage.wamid AND e.status IN ('delivered','read','failed'))`, deadline).
			Order("id").Limit(sweepBatchSize).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := r.Settle(ctx, row.OrganizationID, row.WhatsAppAccount, row.Wamid); err != nil {
				res.Errors++
				res.LastErrText = err.Error()
				continue
			}
			res.Resettled++
		}
		if len(rows) < sweepBatchSize {
			return nil
		}
		cursor = rows[len(rows)-1].ID
	}
}

// Logf is the part of the application logger the job needs.
type Logf interface {
	Info(msg string, fields ...any)
	Error(msg string, fields ...any)
}

// RunIntegrity runs Sweep every IntegrityInterval until ctx is cancelled. A pass
// that fails or panics is logged and the loop carries on: it is instrumentation.
func (r *Recorder) RunIntegrity(ctx context.Context, log Logf) {
	if !r.Enabled() {
		log.Info("Usage integrity job is off (usage.record_enabled = false)")
		return
	}
	every := r.Cfg.IntegrityInterval()
	log.Info("Usage integrity job started", "interval", every.String(), "window", r.Cfg.IntegrityWindow().String())
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweepOnce(ctx, log)
		}
	}
}

func (r *Recorder) sweepOnce(ctx context.Context, log Logf) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("Usage integrity pass panicked", "panic", fmt.Sprint(p))
		}
	}()
	res, err := r.Sweep(ctx, time.Now())
	if err != nil || res.Errors > 0 {
		log.Error("Usage integrity pass had errors", "error", err, "errors", res.Errors, "last", res.LastErrText)
	}
	if res.Inferred+res.Unlinked+res.Resettled > 0 {
		log.Info("Usage integrity pass", "inferred", res.Inferred, "unlinked", res.Unlinked, "resettled", res.Resettled)
	}
}

// baseline is the instant the measurement started: the creation time of the oldest
// ledger row, or nil while the ledger is empty. It is read, never stored, so it
// cannot drift forward: rows are only ever added after it.
func (r *Recorder) baseline(ctx context.Context) (*time.Time, error) {
	var first *time.Time
	if err := r.DB.WithContext(ctx).Model(&models.MessageUsage{}).Select("MIN(created_at)").Row().Scan(&first); err != nil {
		return nil, err
	}
	return first, nil
}
