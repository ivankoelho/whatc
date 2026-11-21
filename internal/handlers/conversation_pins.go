package handlers

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPinLimit = errors.New("pin limit reached")

// visiblePinCount counts the user's pins on conversations they can still
// see. A pin on a conversation that was transferred away would otherwise
// keep eating a slot the user cannot even see, or release.
func (a *App) visiblePinCount(db *gorm.DB, userID, orgID uuid.UUID) (int64, error) {
	q := db.Model(&models.Contact{}).Where("contacts.organization_id = ?", orgID)
	q = a.scopeVisibleConversations(q, userID, orgID)
	var n int64
	err := q.Where("contacts.id IN (?)",
		a.DB.Model(&models.ConversationPin{}).Select("contact_id").Where("user_id = ?", userID)).
		Count(&n).Error
	return n, err
}

// PinConversation pins a conversation to the top of the caller's own list
// (max models.MaxPinnedConversations). Personal and idempotent: pinning an
// already-pinned conversation succeeds without using another slot. Requires
// only that the caller can see the conversation.
func (a *App) PinConversation(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}

	var contact models.Contact
	q := a.scopeVisibleConversations(a.DB.Where("id = ? AND organization_id = ?", contactID, orgID), userID, orgID)
	if err := q.First(&contact).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Contact not found", nil, "")
	}

	err = a.DB.Transaction(func(tx *gorm.DB) error {
		// Serialize this user's pin changes so two quick clicks cannot both
		// pass the limit check.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").First(&models.User{}, "id = ?", userID).Error; err != nil {
			return err
		}
		var existing int64
		if err := tx.Model(&models.ConversationPin{}).
			Where("user_id = ? AND contact_id = ?", userID, contactID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		n, err := a.visiblePinCount(tx, userID, orgID)
		if err != nil {
			return err
		}
		if n >= models.MaxPinnedConversations {
			return errPinLimit
		}
		return tx.Create(&models.ConversationPin{UserID: userID, ContactID: contactID, OrganizationID: orgID}).Error
	})
	if errors.Is(err, errPinLimit) {
		return r.SendErrorEnvelope(fasthttp.StatusConflict,
			fmt.Sprintf("You can pin up to %d conversations", models.MaxPinnedConversations), nil, "")
	}
	if err != nil {
		a.Log.Error("Failed to pin conversation", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to pin conversation", nil, "")
	}
	return r.SendEnvelope(map[string]any{"contact_id": contactID, "is_pinned": true})
}

// UnpinConversation removes the caller's own pin. Idempotent. Deliberately
// does not require visibility: someone who lost access must still be able to
// clear the leftover mark.
func (a *App) UnpinConversation(r *fastglue.Request) error {
	orgID, userID, err := a.getOrgAndUserID(r)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	contactID, err := parsePathUUID(r, "id", "contact")
	if err != nil {
		return nil
	}
	if err := a.DB.Where("user_id = ? AND contact_id = ? AND organization_id = ?", userID, contactID, orgID).
		Delete(&models.ConversationPin{}).Error; err != nil {
		a.Log.Error("Failed to unpin conversation", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to unpin conversation", nil, "")
	}
	return r.SendEnvelope(map[string]any{"contact_id": contactID, "is_pinned": false})
}

func pinnedAtPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
