package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

func (a *App) NextOpportunityNumberForTest(orgID uuid.UUID, day time.Time) (string, error) {
	return a.nextOpportunityNumber(a.DB, orgID, day)
}

func (a *App) CreateOrRetriggerSalesOpportunityForTest(contact *models.Contact, sourceTransferID *uuid.UUID) (*models.SalesOpportunity, error) {
	return a.createOrRetriggerSalesOpportunity(contact, sourceTransferID)
}
