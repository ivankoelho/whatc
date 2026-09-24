package handlers

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// nextOpportunityNumber returns the next OPP-YYYYMMDD-NNNNNN for the
// organisation and day. MUST run inside the same transaction as the
// opportunity insert — see nextProtocolNumber's doc comment in
// occurrence_protocol.go for why COUNT(*)+1 is not safe here either.
func (a *App) nextOpportunityNumber(tx *gorm.DB, orgID uuid.UUID, day time.Time) (string, error) {
	dayKey := day.Format("20060102")

	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&models.SalesOpportunityCounter{
			OrganizationID: orgID,
			Day:            dayKey,
			LastSeq:        0,
		}).Error; err != nil {
		return "", err
	}

	var seq int
	row := tx.Raw(`UPDATE sales_opportunity_counters
		SET last_seq = last_seq + 1
		WHERE organization_id = ? AND day = ?
		RETURNING last_seq`, orgID, dayKey).Row()
	if err := row.Scan(&seq); err != nil {
		return "", err
	}

	return fmt.Sprintf("OPP-%s-%06d", dayKey, seq), nil
}
