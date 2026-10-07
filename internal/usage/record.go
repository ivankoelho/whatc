package usage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Recorder writes and settles the ledger. A nil Recorder, or one whose
// configuration turns recording off, does nothing and returns no error, so
// callers never need a guard of their own.
type Recorder struct {
	DB  *gorm.DB
	Cfg config.UsageConfig
}

// New builds a Recorder.
func New(db *gorm.DB, cfg config.UsageConfig) *Recorder { return &Recorder{DB: db, Cfg: cfg} }

// Enabled reports whether recording is on.
func (r *Recorder) Enabled() bool { return r != nil && r.DB != nil && r.Cfg.Enabled() }

func (r *Recorder) conn(ctx context.Context, tx *gorm.DB) *gorm.DB {
	if tx != nil {
		return tx.WithContext(ctx)
	}
	return r.DB.WithContext(ctx)
}

// Record creates the ledger row of msg, right after the message itself.
// It is idempotent (INSERT … ON CONFLICT DO NOTHING on both unique keys): a
// second call for the same message changes nothing. When an `unlinked` row for
// the same wamid already exists (created by the integrity job before the message
// was found) it is linked to msg instead of being duplicated.
//
// The caller must log a returned error and carry on: recording never blocks a send.
func (r *Recorder) Record(ctx context.Context, tx *gorm.DB, msg *models.Message, o Origin) error {
	if !r.Enabled() || msg == nil {
		return nil
	}
	db := r.conn(ctx, tx)

	var contact models.Contact
	_ = db.Select("id", "phone_number", "team_id").Take(&contact, "id = ?", msg.ContactID).Error

	if o.ActorType == "" {
		o.ActorType, o.Detail, o.Inferred = ActorSystem, "unclassified", true
	}
	unitID, unitSrc := r.resolveUnit(db, o, contact.TeamID)

	row := models.MessageUsage{
		OrganizationID: msg.OrganizationID, WhatsAppAccount: msg.WhatsAppAccount,
		MessageID: &msg.ID, Wamid: msg.WhatsAppMessageID, Direction: string(msg.Direction),
		ContactID: &msg.ContactID, ConversationID: msg.ConversationID, MessageType: string(msg.MessageType),
		RecipientCountry: CountryOf(contact.PhoneNumber), TemplateName: msg.TemplateName,
		ActorType: string(o.ActorType), ActorUserID: o.ActorUserID, TeamID: o.TeamID, UnitID: unitID, UnitSource: string(unitSrc),
		FlowID: o.FlowID, FlowNodeID: o.FlowNodeID, CampaignID: o.CampaignID, OccurrenceID: o.OccurrenceID,
		AIUsageLogID: o.AIUsageLogID, OriginDetail: o.Detail, OriginInferred: o.Inferred,
		Quantity: 1, BillingState: string(StatePending), LinkState: string(LinkLinked), SentAt: msg.CreatedAt,
	}
	if row.SentAt.IsZero() {
		row.SentAt = time.Now()
	}
	if row.TeamID == nil {
		row.TeamID = contact.TeamID
	}
	if msg.TemplateName != "" {
		var tpl models.Template
		if err := db.Select("meta_template_id", "category").
			Where("organization_id = ? AND whats_app_account = ? AND name = ?", msg.OrganizationID, msg.WhatsAppAccount, msg.TemplateName).
			Take(&tpl).Error; err == nil {
			row.TemplateID, row.DeclaredCategory = tpl.MetaTemplateID, tpl.Category
		}
	}
	if msg.Direction == models.DirectionIncoming {
		row.BillingState, row.Billable, row.EstimatedCost = string(StateNotBillable), boolPtr(false), floatPtr(0)
	}

	res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 && row.Wamid != "" {
		// An `unlinked` row for this wamid may exist: complete it with what we know.
		return db.Model(&models.MessageUsage{}).
			Where("organization_id = ? AND whatsapp_account = ? AND wamid = ? AND message_id IS NULL",
				row.OrganizationID, row.WhatsAppAccount, row.Wamid).
			Updates(map[string]any{
				"message_id": msg.ID, "link_state": string(LinkLinked), "contact_id": msg.ContactID,
				"message_type": row.MessageType, "recipient_country": row.RecipientCountry,
				"template_name": row.TemplateName, "template_id": row.TemplateID, "declared_category": row.DeclaredCategory,
				"actor_type": row.ActorType, "actor_user_id": row.ActorUserID, "team_id": row.TeamID,
				"unit_id": row.UnitID, "unit_source": row.UnitSource, "flow_id": row.FlowID, "flow_node_id": row.FlowNodeID,
				"campaign_id": row.CampaignID, "occurrence_id": row.OccurrenceID, "ai_usage_log_id": row.AIUsageLogID,
				"origin_detail": row.OriginDetail, "origin_inferred": row.OriginInferred,
				"direction": row.Direction, "sent_at": row.SentAt,
			}).Error
	}
	return nil
}

// resolveUnit attributes the cost to a unit: the agent's, else the team's, else none.
func (r *Recorder) resolveUnit(db *gorm.DB, o Origin, contactTeam *uuid.UUID) (*uuid.UUID, UnitSource) {
	if o.ActorUserID != nil {
		var u models.User
		if err := db.Select("id", "unit_id").Take(&u, "id = ?", *o.ActorUserID).Error; err == nil && u.UnitID != nil {
			return u.UnitID, UnitFromAgent
		}
	}
	team := o.TeamID
	if team == nil {
		team = contactTeam
	}
	if team != nil {
		var t models.Team
		if err := db.Select("id", "unit_id").Take(&t, "id = ?", *team).Error; err == nil && t.UnitID != nil {
			return t.UnitID, UnitFromTeam
		}
	}
	return nil, UnitNone
}

// MarkSendFailed records that the API refused the send before accepting it:
// no wamid, not billable, cost zero.
func (r *Recorder) MarkSendFailed(ctx context.Context, tx *gorm.DB, messageID uuid.UUID) error {
	if !r.Enabled() {
		return nil
	}
	return r.conn(ctx, tx).Model(&models.MessageUsage{}).
		Where("message_id = ? AND wamid = ''", messageID).
		Updates(map[string]any{
			"billing_state": string(StateSendFailed), "billable": false, "estimated_cost": 0,
			"settled_at": time.Now(),
		}).Error
}

// AttachWamid is called once the API accepted the send and the message got its
// wamid. It stores the wamid on the message's row and settles it, which picks up
// any status event that arrived before the wamid was known. When the integrity
// job had already created an `unlinked` row for that wamid, that row is merged
// (it holds nothing the events cannot rebuild).
func (r *Recorder) AttachWamid(ctx context.Context, orgID, messageID uuid.UUID, account, wamid string) error {
	if !r.Enabled() || wamid == "" {
		return nil
	}
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		orphan := tx.Model(&models.MessageUsage{}).
			Where("organization_id = ? AND whatsapp_account = ? AND wamid = ? AND message_id IS NULL", orgID, account, wamid)

		var own models.MessageUsage
		err := tx.Where("message_id = ?", messageID).Take(&own).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// No row of its own (recording failed earlier): adopt the unlinked one, if any.
			return orphan.Updates(map[string]any{"message_id": messageID, "link_state": string(LinkLinked)}).Error
		case err != nil:
			return err
		case own.Wamid == "":
			if err := orphan.Delete(&models.MessageUsage{}).Error; err != nil {
				return err
			}
			return tx.Model(&models.MessageUsage{}).Where("id = ?", own.ID).Update("wamid", wamid).Error
		}
		return nil
	})
	if err != nil {
		return err
	}
	return r.Settle(ctx, orgID, account, wamid)
}
