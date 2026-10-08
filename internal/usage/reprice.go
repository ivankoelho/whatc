package usage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RepriceResult counts what a Reprice pass looked at and changed.
type RepriceResult struct {
	Processed int `json:"processed"`
	Changed   int `json:"changed"`
}

// Reprice settles again the rows of an organization that are waiting on a price or
// a Meta event (`no_rate`, `awaiting_pricing`, `unconfirmed`), so a price registered
// afterwards reaches them. It pages by key, locks each row while it recomputes it
// and never touches a row in any other state. estimated_cost is only ever derived
// from the internal table; nothing from Meta overwrites it.
func (r *Recorder) Reprice(ctx context.Context, orgID uuid.UUID) (RepriceResult, error) {
	var res RepriceResult
	if !r.Enabled() {
		return res, nil
	}
	cursor := uuid.Nil
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var ids []uuid.UUID
		if err := r.DB.WithContext(ctx).Model(&models.MessageUsage{}).
			Where("organization_id = ? AND wamid <> '' AND billing_state IN ? AND id > ?", orgID,
				[]string{string(StateNoRate), string(StateAwaitingPricing), string(StateUnconfirmed)}, cursor).
			Order("id").Limit(sweepBatchSize).Pluck("id", &ids).Error; err != nil {
			return res, err
		}
		for _, id := range ids {
			changed, err := r.repriceOne(ctx, orgID, id)
			if err != nil {
				return res, err
			}
			res.Processed++
			if changed {
				res.Changed++
			}
		}
		if len(ids) < sweepBatchSize {
			return res, nil
		}
		cursor = ids[len(ids)-1]
	}
}

func (r *Recorder) repriceOne(ctx context.Context, orgID, id uuid.UUID) (changed bool, err error) {
	err = r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.MessageUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND organization_id = ?", id, orgID).Take(&row).Error; err != nil {
			return err
		}
		changed, err = r.settleRow(tx, row, time.Now())
		return err
	})
	return changed, err
}
