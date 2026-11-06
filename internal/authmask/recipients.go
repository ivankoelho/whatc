package authmask

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"gorm.io/gorm"
)

// RecipientsReport summarises a run over bulk_message_recipients.template_params
// of AUTHENTICATION campaigns that still hold clear-text codes.
type RecipientsReport struct {
	Scrub         int // codes no longer needed: already sent, cancelled/deleted campaign or recipient
	SealPending   int // still needed (pending or retryable failures): sealed only with -seal-pending
	AlreadySealed int // skipped
}

type recipientRow struct {
	ID             uuid.UUID
	TemplateParams []byte
	RStatus        string
	CStatus        string
	Gone           bool
}

// Recipients handles codes already stored in clear in campaign recipients.
//
//   - Scrub (always safe): the recipient was sent/delivered/read, the campaign was
//     cancelled, or the campaign/recipient was deleted. A retry never reads these.
//   - Pending (draft/scheduled/processing/paused pending recipients, and failed
//     recipients of campaigns that can still be retried): untouched unless
//     sealPending is set, which encrypts them in place (the worker accepts both
//     forms), so the campaign keeps working. key is required for that.
//
// apply=false only counts.
func Recipients(db *gorm.DB, key string, apply, sealPending bool) (*RecipientsReport, error) {
	rep := &RecipientsReport{}
	var rows []recipientRow
	err := db.Raw(`
		SELECT r.id, r.template_params, r.status AS r_status, c.status AS c_status,
		       (r.deleted_at IS NOT NULL OR c.deleted_at IS NOT NULL) AS gone
		FROM bulk_message_recipients r
		JOIN bulk_message_campaigns c ON c.id = r.campaign_id
		JOIN templates t ON t.id = c.template_id
		WHERE upper(t.category) = 'AUTHENTICATION'
		  AND r.template_params IS NOT NULL AND r.template_params <> '{}'::jsonb`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, row := range rows {
		var params models.JSONB
		_ = json.Unmarshal(row.TemplateParams, &params)
		if templateutil.IsSealed(params) {
			rep.AlreadySealed++
			continue
		}
		done := row.RStatus == string(models.MessageStatusSent) ||
			row.RStatus == string(models.MessageStatusDelivered) ||
			row.RStatus == string(models.MessageStatusRead)
		if done || row.Gone || row.CStatus == string(models.CampaignStatusCancelled) {
			rep.Scrub++
			if apply {
				if err := db.Unscoped().Model(&models.BulkMessageRecipient{}).Where("id = ?", row.ID).
					UpdateColumn("template_params", models.JSONB{}).Error; err != nil {
					return nil, err
				}
			}
			continue
		}
		rep.SealPending++
		if apply && sealPending {
			sealed, err := templateutil.SealParams(params, key, now)
			if err != nil {
				return nil, err
			}
			if err := db.Model(&models.BulkMessageRecipient{}).Where("id = ?", row.ID).
				UpdateColumn("template_params", models.JSONB(sealed)).Error; err != nil {
				return nil, err
			}
		}
	}
	return rep, nil
}
