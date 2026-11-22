package handlers

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm/clause"
)

// Origin of a conversation that began with a click on a Meta ad ("click to WhatsApp").
//
// Meta puts a "referral" object on the first message. It is saved on its own table (never in the
// contact's free-form metadata) so the CRM can say which ad brought the customer, and so the click id
// is there if conversions are ever reported back to Meta. Recording is idempotent (Meta retries
// webhooks) and never blocks or fails the message it came with.

// AdOriginResponse is the origin the CRM shows for a contact: the first ad that brought it.
type AdOriginResponse struct {
	SourceID   string `json:"source_id,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	SourceURL  string `json:"source_url,omitempty"`
	Headline   string `json:"headline,omitempty"`
	Body       string `json:"body,omitempty"`
	// Clicks is how many times the customer came from an ad (1 for a single click).
	Clicks int `json:"clicks"`
}

// recordAdReferral saves the referral of an incoming message, if it carries one.
func (a *App) recordAdReferral(orgID, contactID uuid.UUID, msg IncomingTextMessage) {
	ref := msg.Referral
	if ref == nil || msg.ID == "" {
		return
	}
	// A referral with nothing that identifies the ad says nothing worth keeping.
	if strings.TrimSpace(ref.SourceID) == "" && strings.TrimSpace(ref.CtwaClid) == "" && strings.TrimSpace(ref.SourceURL) == "" {
		return
	}
	row := models.ContactAdReferral{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID, ContactID: contactID, MessageID: msg.ID,
		SourceID: clipRunes(ref.SourceID, 100), SourceType: clipRunes(ref.SourceType, 20), SourceURL: clipRunes(ref.SourceURL, 1000),
		Headline: clipRunes(ref.Headline, 500), Body: ref.Body, MediaType: clipRunes(ref.MediaType, 20), CtwaClid: clipRunes(ref.CtwaClid, 500),
	}
	err := a.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	if err != nil {
		a.Log.Error("Failed to record the ad origin of a conversation", "error", err, "contact_id", contactID)
	}
}

// adOriginOf is the first-touch origin of a contact, or nil when no conversation of it came from an ad.
func (a *App) adOriginOf(orgID, contactID uuid.UUID) *AdOriginResponse {
	var rows []models.ContactAdReferral
	if err := a.DB.Where("organization_id = ? AND contact_id = ?", orgID, contactID).
		Order("created_at ASC").Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil
	}
	first := rows[0]
	return &AdOriginResponse{
		SourceID: first.SourceID, SourceType: first.SourceType, SourceURL: first.SourceURL,
		Headline: first.Headline, Body: first.Body, Clicks: len(rows),
	}
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// adOriginsFor is adOriginOf for a page of contacts in one query. A contact with no ad conversation has no entry.
func (a *App) adOriginsFor(orgID uuid.UUID, contacts []models.Contact) map[uuid.UUID]*AdOriginResponse {
	out := map[uuid.UUID]*AdOriginResponse{}
	if len(contacts) == 0 {
		return out
	}
	ids := make([]uuid.UUID, len(contacts))
	for i, c := range contacts {
		ids[i] = c.ID
	}
	var rows []models.ContactAdReferral
	if err := a.DB.Where("organization_id = ? AND contact_id IN ?", orgID, ids).Order("created_at ASC").Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		if o, ok := out[r.ContactID]; ok {
			o.Clicks++
			continue
		}
		out[r.ContactID] = &AdOriginResponse{SourceID: r.SourceID, SourceType: r.SourceType, SourceURL: r.SourceURL,
			Headline: r.Headline, Body: r.Body, Clicks: 1}
	}
	return out
}
