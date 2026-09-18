package handlers

import (
	"time"

	"github.com/google/uuid"
)

func (a *App) NextOpportunityNumberForTest(orgID uuid.UUID, day time.Time) (string, error) {
	return a.nextOpportunityNumber(a.DB, orgID, day)
}
