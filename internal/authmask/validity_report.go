package authmask

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"gorm.io/gorm"
)

// CampaignImpact is what turning on otp.require_valid_until would do to one
// authentication campaign.
type CampaignImpact struct {
	CampaignID   uuid.UUID
	Name         string
	Status       string
	ScheduledAt  *time.Time
	WithValidity int // recipients that carry the issuer's valid_until and are still sendable now
	Expired      int // carry valid_until but it already passed: will fail as otp_expired
	NoValidity   int // actionable recipients without valid_until: would fail as otp_validity_missing
	Plaintext    int // of NoValidity, still stored in clear (legacy, never sealed)
}

// ValidityReport is a read-only impact report for the transition to explicit
// OTP validity. Only actionable recipients count: pending ones and failed ones
// of campaigns that can still be retried; sent, cancelled and deleted ones
// need nothing.
func ValidityReport(db *gorm.DB, now time.Time, policy templateutil.OTPPolicy) ([]CampaignImpact, error) {
	type row struct {
		CID            uuid.UUID
		Name           string
		CStatus        string
		ScheduledAt    *time.Time
		RStatus        string
		TemplateParams []byte
	}
	var rows []row
	err := db.Raw(`
		SELECT c.id AS c_id, c.name, c.status AS c_status, c.scheduled_at, r.status AS r_status, r.template_params
		FROM bulk_message_recipients r
		JOIN bulk_message_campaigns c ON c.id = r.campaign_id AND c.deleted_at IS NULL
		JOIN templates t ON t.id = c.template_id
		WHERE r.deleted_at IS NULL AND upper(t.category) = 'AUTHENTICATION'
		  AND c.status <> 'cancelled' AND r.status IN ('pending','failed')`).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]*CampaignImpact{}
	var order []uuid.UUID
	for _, r := range rows {
		var p models.JSONB
		_ = json.Unmarshal(r.TemplateParams, &p)
		if len(p) == 0 {
			continue // nothing stored: nothing to send, nothing to protect
		}
		im := byID[r.CID]
		if im == nil {
			im = &CampaignImpact{CampaignID: r.CID, Name: r.Name, Status: r.CStatus, ScheduledAt: r.ScheduledAt}
			byID[r.CID] = im
			order = append(order, r.CID)
		}
		switch deadline, ok := policy.Deadline(p); {
		case !ok:
			im.NoValidity++
			if !templateutil.IsSealed(p) {
				im.Plaintext++
			}
		case !now.Before(deadline):
			im.Expired++
		default:
			im.WithValidity++
		}
	}
	out := make([]CampaignImpact, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}
