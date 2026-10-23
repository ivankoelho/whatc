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

// PhoneIdentities returns every digits-only string that identifies the same
// subscriber as phone, with the canonical form (the caller's own, normalized)
// first.
//
// Brazil is the reason this exists. Mobile numbers there gained a leading "9"
// after the DDD, but accounts created before the change are still reachable —
// and still reported by WhatsApp — in the legacy 8-digit form. So the same
// person is "+55 71 99123-4567" to an agent typing them into the CRM and
// "5571 9123-4567" on the inbound webhook. Matching on one form alone splits
// them into two contacts, and the second one has no owner.
//
// Scoped deliberately to country code 55 and to the mobile range: applying the
// rule globally, or to Brazilian landlines, would fuse genuinely different
// subscribers.
func PhoneIdentities(phone string) []string {
	normalized := CanonicalPhone(phone)
	ids := []string{normalized}
	if alt := brazilianMobileAlternate(normalized); alt != "" {
		ids = append(ids, alt)
	}
	// Rows saved before CanonicalPhone existed may lack the country code
	// ("71996002928"); keep matching them so those duplicates collapse onto
	// the oldest row instead of staying invisible to the lookup.
	for _, id := range ids[:len(ids):len(ids)] {
		if strings.HasPrefix(id, "55") && (len(id) == 12 || len(id) == 13) {
			ids = append(ids, id[2:])
		}
	}
	return ids
}

// CanonicalPhone is NormalizePhone plus the default country code: a bare
// Brazilian number (DDD + 8 or 9 digits, 10 or 11 digits) gets "55" in front.
// Country-coded numbers are 12+ digits, so the length alone tells them apart.
// The DDD must be a real Brazilian area code and an 11-digit number must be a
// mobile (9 after the DDD), which keeps e.g. US numbers ("12125551234") intact.
// ponytail: assumes a Brazilian deployment; make the default country
// Numbers starting with 55 are left alone: that is the country code far more
// often than DDD 55 (RS), and prefixing would corrupt a short country-coded one.
// configurable if a foreign number without its country code ever collides.
func CanonicalPhone(phone string) string {
	n := NormalizePhone(phone)
	if (len(n) == 10 || len(n) == 11 && n[2] == '9') && !strings.HasPrefix(n, "55") && strings.Contains(brazilianDDDs, " "+n[:2]+" ") {
		return "55" + n
	}
	return n
}

const brazilianDDDs = " 11 12 13 14 15 16 17 18 19 21 22 24 27 28 31 32 33 34 35 37 38 41 42 43 44 45 46 47 48 49 51 53 54 55 61 62 63 64 65 66 67 68 69 71 73 74 75 77 79 81 82 83 84 85 86 87 88 89 91 92 93 94 95 96 97 98 99 "

// brazilianMobileAlternate returns the other valid form of a Brazilian mobile
// number, or "" when n is not one. Layout is 55 + DD + subscriber, so the
// subscriber's first digit sits at index 4.
func brazilianMobileAlternate(n string) string {
	if !strings.HasPrefix(n, "55") {
		return ""
	}
	switch len(n) {
	case 13:
		// 55 + DD + 9 + 8 digits — drop the added ninth digit.
		if n[4] == '9' {
			return n[:4] + n[5:]
		}
	case 12:
		// 55 + DD + 8 digits — add it back, but only for the mobile range.
		// Landlines start at 2-5 and must never grow a ninth digit.
		if n[4] >= '6' && n[4] <= '9' {
			return n[:4] + "9" + n[4:]
		}
	}
	return ""
}

// phoneMatchSet expands PhoneIdentities into every stored spelling: each
// identity bare and with the legacy "+" prefix.
func phoneMatchSet(phone string) []string {
	identities := PhoneIdentities(phone)
	set := make([]string, 0, len(identities)*2)
	for _, id := range identities {
		set = append(set, id, "+"+id)
	}
	return set
}

// contactByPhone resolves phone to a single contact row, including
// soft-deleted ones.
//
// Duplicates created before this fix mean more than one row can match, so the
// winner is pinned to the oldest. That is deliberately not "whichever form was
// asked for": the older row is the one an agent created and owns, while the
// newer one is the ownerless copy the webhook split off. Preferring the exact
// match would hand the conversation back to the copy.
func contactByPhone(db *gorm.DB, orgID uuid.UUID, phone string) (*models.Contact, error) {
	var contact models.Contact
	if err := db.Unscoped().
		Where("organization_id = ? AND phone_number IN ?", orgID, phoneMatchSet(phone)).
		Order("created_at ASC").
		First(&contact).Error; err != nil {
		return nil, err
	}
	return &contact, nil
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
	normalizedPhone := CanonicalPhone(phoneNumber)

	// Any stored spelling of the same subscriber counts as a hit, including
	// soft-deleted rows and Brazil's legacy 8-digit mobile form.
	if contact, err := contactByPhone(db, orgID, phoneNumber); err == nil {
		// Restore if soft-deleted
		if contact.DeletedAt.Valid {
			db.Unscoped().Model(contact).Update("deleted_at", nil)
			contact.DeletedAt.Valid = false
		}
		// Update profile name if changed
		if profileName != "" && contact.ProfileName != profileName {
			db.Model(contact).Update("profile_name", profileName)
		}
		return contact, false, nil
	}

	// Create new contact
	contact := models.Contact{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		PhoneNumber:    normalizedPhone,
		ProfileName:    profileName,
	}
	if err := db.Create(&contact).Error; err != nil {
		// Race condition: another goroutine may have created the contact
		if raced, err2 := contactByPhone(db, orgID, phoneNumber); err2 == nil {
			// Restore if soft-deleted
			if raced.DeletedAt.Valid {
				db.Unscoped().Model(raced).Update("deleted_at", nil)
				raced.DeletedAt.Valid = false
			}
			return raced, false, nil
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
	// A real DB error is returned as-is (NOT collapsed to ErrRecordNotFound):
	// callers gate authorization on "does this contact already exist?", so a
	// transient error must never read as "brand new" and skip the gate.
	return contactByPhone(db, orgID, phoneNumber)
}

// FindContact finds a contact for the given phone number, matching every
// spelling of the same subscriber identity. Unlike FindContactUnscoped it
// excludes soft-deleted rows.
func FindContact(db *gorm.DB, orgID uuid.UUID, phoneNumber string) (*models.Contact, error) {
	var contact models.Contact
	if err := db.
		Where("organization_id = ? AND phone_number IN ?", orgID, phoneMatchSet(phoneNumber)).
		Order("created_at ASC").
		First(&contact).Error; err != nil {
		return nil, gorm.ErrRecordNotFound
	}
	return &contact, nil
}
