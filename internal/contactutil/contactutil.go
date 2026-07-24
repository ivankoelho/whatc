package contactutil

import (
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// NormalizePhone reduces a phone number to its canonical digits-only identity:
// it strips "+", spaces, dashes, parentheses, and every other non-digit. So
// "+55 11 99999-9999", "55 (11) 99999-9999" and "5511999999999" all resolve to
// "5511999999999". Use it for BOTH storing and looking up a contact's phone so
// the same subscriber is never split across formats — a raw string match would
// treat a differently-formatted number as a new contact.
func NormalizePhone(phone string) string {
	var b strings.Builder
	b.Grow(len(phone))
	for i := 0; i < len(phone); i++ {
		if c := phone[i]; c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// GetOrCreateContact finds or creates a contact for the given phone number.
// Merges behaviors from both handler and worker implementations:
//   - Normalizes phone (strips leading "+")
//   - Tries both normalized and +prefix forms
//   - Updates profile name if changed
//   - Handles race conditions on create by re-fetching
//   - Restores soft-deleted contacts if found
//
// Returns the contact, whether it was newly created, and any error.
func GetOrCreateContact(db *gorm.DB, orgID uuid.UUID, phoneNumber, profileName string) (*models.Contact, bool, error) {
	// Canonical digits-only identity (see NormalizePhone).
	normalizedPhone := NormalizePhone(phoneNumber)

	// Try to find existing contact with normalized phone (including soft-deleted)
	var contact models.Contact
	if err := db.Unscoped().Where("organization_id = ? AND phone_number = ?", orgID, normalizedPhone).First(&contact).Error; err == nil {
		// Restore if soft-deleted
		if contact.DeletedAt.Valid {
			db.Unscoped().Model(&contact).Update("deleted_at", nil)
			contact.DeletedAt.Valid = false
		}
		// Update profile name if changed
		if profileName != "" && contact.ProfileName != profileName {
			db.Model(&contact).Update("profile_name", profileName)
		}
		return &contact, false, nil
	}

	// Also try with + prefix (contacts may have been stored with it)
	if err := db.Unscoped().Where("organization_id = ? AND phone_number = ?", orgID, "+"+normalizedPhone).First(&contact).Error; err == nil {
		// Restore if soft-deleted
		if contact.DeletedAt.Valid {
			db.Unscoped().Model(&contact).Update("deleted_at", nil)
			contact.DeletedAt.Valid = false
		}
		if profileName != "" && contact.ProfileName != profileName {
			db.Model(&contact).Update("profile_name", profileName)
		}
		return &contact, false, nil
	}

	// Create new contact
	contact = models.Contact{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		PhoneNumber:    normalizedPhone,
		ProfileName:    profileName,
	}
	if err := db.Create(&contact).Error; err != nil {
		// Race condition: another goroutine may have created the contact
		if err2 := db.Unscoped().Where("organization_id = ? AND phone_number = ?", orgID, normalizedPhone).First(&contact).Error; err2 == nil {
			// Restore if soft-deleted
			if contact.DeletedAt.Valid {
				db.Unscoped().Model(&contact).Update("deleted_at", nil)
				contact.DeletedAt.Valid = false
			}
			return &contact, false, nil
		}
		return nil, false, err
	}
	return &contact, true, nil
}

// FindContactUnscoped finds a contact for the given phone number, trying both
// normalized and +prefix forms, INCLUDING soft-deleted contacts. This is the
// same identity resolution GetOrCreateContact uses, but read-only: it never
// restores a soft-delete or updates the profile name.
//
// Use this (not a raw scoped query) anywhere an "does this contact already
// exist?" check gates authorization — a raw exact-match, non-Unscoped query
// can miss a contact stored in the other phone-number format, or one that is
// soft-deleted, and wrongly treat it as brand-new.
func FindContactUnscoped(db *gorm.DB, orgID uuid.UUID, phoneNumber string) (*models.Contact, error) {
	normalizedPhone := NormalizePhone(phoneNumber)

	// Match the digits-only form and the legacy "+"-prefixed form in one query.
	// A real DB error is returned as-is (NOT collapsed to ErrRecordNotFound):
	// callers gate authorization on "does this contact already exist?", so a
	// transient error must never read as "brand new" and skip the gate.
	var contact models.Contact
	if err := db.Unscoped().
		Where("organization_id = ? AND phone_number IN (?, ?)", orgID, normalizedPhone, "+"+normalizedPhone).
		First(&contact).Error; err != nil {
		return nil, err
	}
	return &contact, nil
}

// FindContact finds a contact for the given phone number with both forms (normalized and +prefix).
func FindContact(db *gorm.DB, orgID uuid.UUID, phoneNumber string) (*models.Contact, error) {
	normalizedPhone := NormalizePhone(phoneNumber)

	var contact models.Contact
	if err := db.Where("organization_id = ? AND phone_number = ?", orgID, normalizedPhone).First(&contact).Error; err == nil {
		return &contact, nil
	}

	if err := db.Where("organization_id = ? AND phone_number = ?", orgID, "+"+normalizedPhone).First(&contact).Error; err == nil {
		return &contact, nil
	}

	return nil, gorm.ErrRecordNotFound
}
