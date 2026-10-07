package usage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordStatusEvent appends one Meta status event to message_pricing_events.
// inserted is false for a duplicate (same wamid, status and event time), which
// the caller must not settle again.
func (r *Recorder) RecordStatusEvent(ctx context.Context, ev StatusEvent) (inserted bool, err error) {
	if !r.Enabled() {
		return false, nil
	}
	row := models.MessagePricingEvent{
		OrganizationID: ev.OrganizationID, WhatsAppAccount: ev.WhatsAppAccount, Wamid: ev.Wamid,
		Status: ev.Status, EventAt: ev.EventAt, ReceivedAt: time.Now(),
		RecipientCountry: ev.RecipientCountry, ErrorCode: ev.ErrorCode, ErrorTitle: ev.ErrorTitle,
	}
	if ev.Pricing != nil {
		row.HasPricing = true
		row.Pricing = models.JSONB{"billable": ev.Pricing.Billable, "pricing_model": ev.Pricing.PricingModel, "category": ev.Pricing.Category}
	}
	if ev.ConversationID != "" || ev.ConversationOrigin != "" {
		row.Conversation = models.JSONB{"id": ev.ConversationID, "origin": ev.ConversationOrigin}
	}
	res := r.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	return res.RowsAffected > 0, res.Error
}

// Settle recomputes the row of a wamid from the whole set of its events. It is
// idempotent and safe to call from the webhook, the send path and the integrity
// job at the same time: the row is locked while it is recomputed. A wamid with
// no row yet is a no-op (the events wait; AttachWamid or the job settle later).
func (r *Recorder) Settle(ctx context.Context, orgID uuid.UUID, account, wamid string) error {
	if !r.Enabled() || wamid == "" {
		return nil
	}
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.MessageUsage
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("organization_id = ? AND whatsapp_account = ? AND wamid = ?", orgID, account, wamid).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return r.settleRow(tx, row, time.Now())
	})
}

// settleRow recomputes and stores one locked row.
func (r *Recorder) settleRow(tx *gorm.DB, row models.MessageUsage, now time.Time) error {
	var events []models.MessagePricingEvent
	if err := tx.Where("organization_id = ? AND whatsapp_account = ? AND wamid = ?", row.OrganizationID, row.WhatsAppAccount, row.Wamid).
		Find(&events).Error; err != nil {
		return err
	}
	var rateErr error
	s := Compute(row, events, func(country, category string, at time.Time) *models.WhatsAppRate {
		rate, err := LookupRate(tx, row.OrganizationID, country, category, at)
		if err != nil {
			rateErr = err
		}
		return rate
	}, now, r.Cfg)
	if rateErr != nil {
		return rateErr
	}
	if !s.DiffersFrom(row) {
		return nil
	}
	var settledAt *time.Time
	if s.BillingState != StatePending {
		settledAt = &now
	}
	return tx.Model(&models.MessageUsage{}).Where("id = ?", row.ID).Updates(map[string]any{
		"billing_state": string(s.BillingState), "billable": s.Billable, "billing_category": s.BillingCategory,
		"pricing_model": s.PricingModel, "conversation_id": s.ConversationID, "meta_pricing": s.MetaPricing,
		"meta_pricing_history": s.MetaPricingHist, "category_diverged": s.CategoryDiverged,
		"estimated_cost": s.EstimatedCost, "estimated_currency": s.EstimatedCurrency, "rate_id": s.RateID,
		"repriced_at": s.RepricedAt, "settled_at": settledAt,
	}).Error
}
