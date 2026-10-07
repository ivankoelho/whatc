package usage

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// LookupRate returns the price in force for the organization, destination
// country (the exact calling code, else the "*" fallback), billing category and
// instant. Validity is by calendar day (UTC), both ends inclusive. nil means no
// price is registered — the caller records `no_rate`, never a silent zero.
func LookupRate(db *gorm.DB, orgID uuid.UUID, country, category string, at time.Time) (*models.WhatsAppRate, error) {
	day := at.UTC().Format("2006-01-02")
	var rate models.WhatsAppRate
	err := db.Where("organization_id = ? AND category = ? AND country IN (?, '*') AND valid_from <= ? AND (valid_to IS NULL OR valid_to >= ?)",
		orgID, category, country, day, day).
		Order("CASE WHEN country = '*' THEN 1 ELSE 0 END, valid_from DESC").
		Take(&rate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rate, nil
}
