package authmask

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"gorm.io/gorm"
)

// Recipients of authentication campaigns whose one-time code may or may not have
// reached the customer: Meta was called and the outcome was never confirmed
// (timeout, an answer that proves nothing, a worker that stopped). The worker
// never resends them; a person reconciles.

// Uncertain states.
const (
	// StateUncertain: already declared delivery_uncertain (failed, claim kept).
	StateUncertain = "uncertain"
	// StateStuck: still pending but claimed longer ago than a live send can last. A
	// worker's housekeeping declares it uncertain on its own; it is listed so it can be
	// seen even if no worker is running.
	StateStuck = "stuck"
)

// Uncertain is one such recipient.
type Uncertain struct {
	RecipientID  uuid.UUID
	CampaignID   uuid.UUID
	CampaignName string
	PhoneLast4   string
	MessageID    uuid.UUID
	State        string
	Reason       string
	Since        time.Time
}

// ListUncertain returns them, oldest first, straight from PostgreSQL. stale is how old
// a pending claim must be to count as stuck (the worker's limit), measured with the
// database clock, like the worker does. Read-only.
func ListUncertain(db *gorm.DB, stale time.Duration) ([]Uncertain, error) {
	type row struct {
		RecipientID  uuid.UUID
		CampaignID   uuid.UUID
		CampaignName string
		Phone        string
		MessageID    uuid.UUID
		State        string
		Reason       string
		Since        time.Time
	}
	var rows []row
	err := db.Raw(`
		SELECT r.id AS recipient_id, c.id AS campaign_id, c.name AS campaign_name, r.phone_number AS phone,
		       r.message_id AS message_id,
		       CASE WHEN r.status = 'failed' THEN ? ELSE ? END AS state,
		       r.error_message AS reason, m.created_at AS since
		FROM bulk_message_recipients r
		JOIN messages m ON m.id = r.message_id
		JOIN bulk_message_campaigns c ON c.id = r.campaign_id
		JOIN templates t ON t.id = c.template_id
		WHERE r.deleted_at IS NULL AND r.message_id IS NOT NULL AND upper(t.category) = 'AUTHENTICATION'
		  AND (r.status = 'failed' OR (r.status = 'pending' AND m.status = 'pending'
		        AND m.created_at < clock_timestamp() - (? * interval '1 second')))
		ORDER BY m.created_at`, StateUncertain, StateStuck, stale.Seconds()).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]Uncertain, 0, len(rows))
	for _, r := range rows {
		last4 := r.Phone
		if len(last4) > 4 {
			last4 = last4[len(last4)-4:]
		}
		out = append(out, Uncertain{r.RecipientID, r.CampaignID, r.CampaignName, last4, r.MessageID, r.State, r.Reason, r.Since})
	}
	return out, nil
}

// Errors of ReleaseUncertain. None of them carries a code.
var (
	// ErrNotUncertain: the recipient is not (or no longer) in the uncertain state.
	ErrNotUncertain = errors.New("recipient is not in an uncertain-delivery state")
	// ErrOperatorNotAllowed: the operator is not an active user of the campaign's
	// organization with permission to edit campaigns.
	ErrOperatorNotAllowed = errors.New("operator is not an active administrator of the campaign's organization")
	// ErrInvalidReason: the reason is empty, too long or looks like it contains a code.
	ErrInvalidReason = errors.New("a reason is required (max 500 characters, and it must not contain a verification code)")
	// ErrNoCodeStored: there is no code left to send, so releasing would achieve nothing.
	ErrNoCodeStored = errors.New("no sealed code is stored for this recipient")
	// ErrCodeNotValid: the code is past its validity (or retention) and can no longer be sent.
	ErrCodeNotValid = errors.New("the code is no longer valid and can no longer be sent")
	// ErrLocalReleaseDisabled: releasing an uncertain delivery locally is switched off (the
	// default). It is checked before anything is read or written.
	ErrLocalReleaseDisabled = errors.New("local release of uncertain deliveries is disabled (otp.allow_local_release is not true in the config file)")
)

// ReleaseRequest is a deliberate, attributed reconciliation.
type ReleaseRequest struct {
	RecipientID uuid.UUID
	// OperatorEmail identifies a REAL user: active, of the campaign's organization, with
	// the campaigns:write permission (or a super admin).
	OperatorEmail string
	// Reason says why the code is known not to have been delivered. It is audited.
	Reason string
	// LocalReleaseEnabled must carry config OTP.AllowLocalRelease. The zero value refuses:
	// a caller that forgets it, or a config that does not enable it, can never release.
	LocalReleaseEnabled bool
}

var digitRun = regexp.MustCompile(`\d{4,}`)

func validReason(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && len(s) <= 500 && !digitRun.MatchString(s)
}

// ReleaseUncertain is the human step of reconciliation. Call it only after confirming
// (with Meta's logs or the customer) that the code was NOT delivered. It drops the
// claim, so the next retry-failed may send again; it queues nothing itself.
//
// Everything happens in ONE transaction: the recipient row is locked, the operator and
// the code's validity are checked, the compare-and-set runs (status failed and a claim
// held) and the audit entry is written. If any step fails nothing is changed. With
// dryRun every check runs and nothing is written.
func ReleaseUncertain(db *gorm.DB, req ReleaseRequest, policy templateutil.OTPPolicy, now time.Time, dryRun bool) error {
	// First of all, before the database is touched, dry runs included: the release is off
	// unless the configuration file turned it on.
	if !req.LocalReleaseEnabled {
		return ErrLocalReleaseDisabled
	}
	reason, ok := validReason(req.Reason)
	if !ok {
		return ErrInvalidReason
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var row struct {
			CampaignID   uuid.UUID
			OrgID        uuid.UUID
			CampaignName string
			Status       string
			MessageID    *uuid.UUID
			Params       []byte
		}
		res := tx.Raw(`SELECT c.id AS campaign_id, c.organization_id AS org_id, c.name AS campaign_name,
				r.status AS status, r.message_id AS message_id, r.template_params AS params
			FROM bulk_message_recipients r JOIN bulk_message_campaigns c ON c.id = r.campaign_id
			WHERE r.id = ? AND r.deleted_at IS NULL FOR UPDATE OF r`, req.RecipientID).Scan(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 || row.Status != string(models.MessageStatusFailed) || row.MessageID == nil {
			return ErrNotUncertain
		}

		operator, err := lookupOperator(tx, req.OperatorEmail, row.OrgID)
		if err != nil {
			return err
		}

		var params models.JSONB
		_ = json.Unmarshal(row.Params, &params)
		if !templateutil.IsSealed(params) {
			return ErrNoCodeStored
		}
		if policy.Check(params, now) != nil || policy.Expired(params, now) {
			return ErrCodeNotValid
		}
		if dryRun {
			return nil
		}

		cas := tx.Model(&models.BulkMessageRecipient{}).
			Where("id = ? AND status = ? AND message_id = ?", req.RecipientID, models.MessageStatusFailed, *row.MessageID).
			Updates(map[string]any{
				"message_id":    nil,
				"error_message": gorm.Expr("'released_after_review by ' || ? || ': ' || COALESCE(error_message, '')", operator.Email),
			})
		if cas.Error != nil {
			return cas.Error
		}
		if cas.RowsAffected != 1 {
			return ErrNotUncertain
		}

		return tx.Create(&models.AuditLog{
			OrganizationID: row.OrgID,
			ResourceType:   "campaign",
			ResourceID:     row.CampaignID,
			UserID:         operator.ID,
			UserName:       operator.FullName,
			Action:         models.AuditActionUpdated,
			Changes: models.JSONBArray{
				map[string]any{"field": "recipient_delivery", "old_value": "delivery_uncertain", "new_value": "released_for_retry"},
				map[string]any{"field": "recipient_id", "old_value": nil, "new_value": req.RecipientID.String()},
				map[string]any{"field": "campaign", "old_value": nil, "new_value": row.CampaignName},
				map[string]any{"field": "reason", "old_value": nil, "new_value": reason},
				map[string]any{"field": "released_via", "old_value": nil, "new_value": "whatomate otp-uncertain"},
			},
		}).Error
	})
}

// lookupOperator validates that email names a real, active user of the campaign's
// organization who may edit campaigns. Same rules as the application: membership row
// first, the user's home role only for the home organization, super admins always.
func lookupOperator(tx *gorm.DB, email string, orgID uuid.UUID) (*models.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrOperatorNotAllowed
	}
	var user models.User
	if err := tx.Where("lower(email) = lower(?)", email).First(&user).Error; err != nil || !user.IsActive {
		return nil, ErrOperatorNotAllowed
	}
	if user.IsSuperAdmin {
		return &user, nil
	}
	var roleID *uuid.UUID
	var member models.UserOrganization
	if err := tx.Where("user_id = ? AND organization_id = ?", user.ID, orgID).First(&member).Error; err == nil {
		roleID = member.RoleID
	}
	if roleID == nil && user.OrganizationID == orgID {
		roleID = user.RoleID
	}
	if roleID == nil {
		return nil, ErrOperatorNotAllowed
	}
	var n int64
	err := tx.Table("role_permissions rp").
		Joins("JOIN permissions p ON p.id = rp.permission_id").
		Where("rp.custom_role_id = ? AND p.resource = ? AND p.action = ?", *roleID, models.ResourceCampaigns, models.ActionWrite).
		Count(&n).Error
	if err != nil {
		return nil, fmt.Errorf("checking operator permissions: %w", err)
	}
	if n == 0 {
		return nil, ErrOperatorNotAllowed
	}
	return &user, nil
}
