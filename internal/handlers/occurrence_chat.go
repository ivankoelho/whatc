package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The internal discussion of a case reuses the internal chat (messages,
// attachments, reactions, read markers, WebSocket events) through a channel of
// kind "occurrence". This file only creates that channel and reports its state;
// reading and writing go through the regular chat routes, which now ask the
// case who may do what (see occurrenceChatAccess).
//
// Internal vs external is structural, not a label: chat_messages and the
// WhatsApp messages table are different tables, the chat handlers never touch a
// WhatsApp client, and sending to the customer is a separate endpoint
// (ReplyToOccurrence) with its own authorization. Nothing here can produce a
// customer-facing message.

// ensureOccurrenceChannel returns the case's channel, creating it on first use.
// Idempotent under concurrency: the unique index makes the loser's insert a no-op.
func (a *App) ensureOccurrenceChannel(db *gorm.DB, occ *models.Occurrence, createdBy uuid.UUID) (*models.ChatChannel, error) {
	var ch models.ChatChannel
	find := func() error {
		return db.Where("organization_id = ? AND occurrence_id = ? AND kind = ?", occ.OrganizationID, occ.ID, models.ChatChannelKindOccurrence).First(&ch).Error
	}
	if find() == nil {
		return &ch, nil
	}
	n := models.ChatChannel{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: occ.OrganizationID,
		Kind: models.ChatChannelKindOccurrence, OccurrenceID: &occ.ID, Name: occ.ProtocolNumber, CreatedByID: &createdBy,
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&n).Error; err != nil {
		return nil, err
	}
	if err := find(); err != nil {
		return nil, err
	}
	return &ch, nil
}

type occurrenceChatState struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Unread    int64     `json:"unread"`
	CanPost   bool      `json:"can_post"`
	Messages  int64     `json:"messages"`
}

// GetOccurrenceChat returns the case's discussion channel (created on first use),
// how many messages the caller has not read and whether they may write.
func (a *App) GetOccurrenceChat(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceOccurrences, models.ActionRead)
	if err != nil {
		return nil
	}
	occ, acc, err := a.loadOccurrenceWithAccess(r, orgID, userID, false)
	if err != nil {
		return nil
	}
	ch, err := a.ensureOccurrenceChannel(a.DB, occ, userID)
	if err != nil {
		a.Log.Error("Failed to open the case discussion", "error", err, "occurrence_id", occ.ID)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to open the discussion", nil, "")
	}
	out := occurrenceChatState{ChannelID: ch.ID}
	out.CanPost = acc.can(models.OccurrenceCapComment) && a.HasPermission(userID, models.ResourceOccurrences, models.ActionWrite, orgID)

	msgs := a.DB.Model(&models.ChatMessage{}).Where("channel_id = ? AND organization_id = ? AND kind = ?", ch.ID, orgID, models.ChatMessageKindUser)
	msgs.Count(&out.Messages)
	unread := a.DB.Model(&models.ChatMessage{}).Where("channel_id = ? AND organization_id = ? AND kind = ? AND user_id <> ?",
		ch.ID, orgID, models.ChatMessageKindUser, userID)
	var read models.ChatRead
	if a.DB.Where("channel_id = ? AND user_id = ?", ch.ID, userID).First(&read).Error == nil {
		unread = unread.Where("created_at > ?", read.LastReadAt)
	}
	unread.Count(&out.Unread)
	return r.SendEnvelope(out)
}
