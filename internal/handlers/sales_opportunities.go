package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// createOrRetriggerSalesOpportunity is the single entry point for opening a
// funnel entry from the chatbot (spec §5, §5.2, §5.3). It is idempotent: if
// the contact already has an open opportunity, it is returned unchanged
// (only a "retriggered" event is appended) instead of creating a second one.
//
// Concurrency: two simultaneous calls for the same contact race on the
// partial unique index idx_sales_opp_org_contact_open. The loser's insert
// fails with a unique_violation, which this function treats identically to
// "found an existing open one" — reload and append retriggered — so the
// caller never sees a race error, only the idempotent result (spec §5.3).
func (a *App) createOrRetriggerSalesOpportunity(contact *models.Contact, sourceTransferID *uuid.UUID) (*models.SalesOpportunity, error) {
	var existing models.SalesOpportunity
	err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
		contact.OrganizationID, contact.ID, models.SalesOpportunityStatusAberta).
		First(&existing).Error
	if err == nil {
		if err := a.DB.Create(&models.SalesOpportunityEvent{
			OrganizationID:     contact.OrganizationID,
			SalesOpportunityID: existing.ID,
			Type:               models.SalesOpportunityEventRetriggered,
			Source:             models.SalesOpportunityEventSourceSystem,
		}).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	opp := models.SalesOpportunity{
		OrganizationID:   contact.OrganizationID,
		ContactID:        contact.ID,
		SourceTransferID: sourceTransferID,
		AssignedUserID:   contact.AssignedUserID,
		Stage:            models.SalesOpportunityStagePotencial,
		Status:           models.SalesOpportunityStatusAberta,
		StageChangedAt:   time.Now(),
	}

	txErr := a.DB.Transaction(func(tx *gorm.DB) error {
		number, err := a.nextOpportunityNumber(tx, contact.OrganizationID, time.Now())
		if err != nil {
			return err
		}
		opp.OpportunityNumber = number
		if err := tx.Create(&opp).Error; err != nil {
			return err
		}
		return tx.Create(&models.SalesOpportunityEvent{
			OrganizationID:     contact.OrganizationID,
			SalesOpportunityID: opp.ID,
			Type:               models.SalesOpportunityEventOpened,
			Source:             models.SalesOpportunityEventSourceSystem,
		}).Error
	})

	if txErr != nil {
		// Lost the race to a concurrent insert: the partial unique index
		// rejected us. Fall back to the retrigger path against the winner.
		if isUniqueViolation(txErr) {
			var winner models.SalesOpportunity
			if err := a.DB.Where("organization_id = ? AND contact_id = ? AND status = ?",
				contact.OrganizationID, contact.ID, models.SalesOpportunityStatusAberta).
				First(&winner).Error; err != nil {
				return nil, err
			}
			if err := a.DB.Create(&models.SalesOpportunityEvent{
				OrganizationID:     contact.OrganizationID,
				SalesOpportunityID: winner.ID,
				Type:               models.SalesOpportunityEventRetriggered,
				Source:             models.SalesOpportunityEventSourceSystem,
			}).Error; err != nil {
				return nil, err
			}
			return &winner, nil
		}
		return nil, txErr
	}

	return &opp, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — same detection as isUniqueNameViolation in
// occurrence_stages.go, kept separate here since it isn't specific to one
// named index (this function has to recognize the loss against
// idx_sales_opp_org_contact_open, not a single fixed constraint).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
