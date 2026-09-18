package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
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

// visibleSalesOpportunities scopes a query to what userID may see: everything
// with view_all, otherwise only opportunities assigned to them (spec §7).
// Task 7/8 reuse this for the board and export endpoints.
func (a *App) visibleSalesOpportunities(query *gorm.DB, userID, orgID uuid.UUID) *gorm.DB {
	if a.HasPermission(userID, models.ResourceSalesOpportunities, models.ActionViewAll, orgID) {
		return query
	}
	return query.Where("sales_opportunities.assigned_user_id = ?", userID)
}

// loadAuthorizedSalesOpportunity resolves {id} from the route and applies the
// same own-vs-view_all gate as visibleSalesOpportunities, mirroring
// loadAuthorizedOccurrence's status codes: 400 for a malformed id, 404 when
// it doesn't exist in this org, 403 when it exists but isn't the caller's
// and the caller lacks view_all (spec §7). Task 7/8 reuse this too.
func (a *App) loadAuthorizedSalesOpportunity(r *fastglue.Request, orgID, userID uuid.UUID) (*models.SalesOpportunity, error) {
	id, err := parsePathUUID(r, "id", "sales opportunity")
	if err != nil {
		return nil, errEnvelopeSent
	}
	opp, err := findByIDAndOrg[models.SalesOpportunity](a.DB, r, id, orgID, "Sales opportunity")
	if err != nil {
		return nil, errEnvelopeSent
	}
	if !a.HasPermission(userID, models.ResourceSalesOpportunities, models.ActionViewAll, orgID) &&
		(opp.AssignedUserID == nil || *opp.AssignedUserID != userID) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Insufficient permissions", nil, "")
		return nil, errEnvelopeSent
	}
	return opp, nil
}

// ListSalesOpportunities lists opportunities visible to the caller, optionally
// filtered by stage, status or assignee.
func (a *App) ListSalesOpportunities(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}

	pg := parsePaginationWithDefaults(r, 30, 100)
	query := a.DB.Model(&models.SalesOpportunity{}).Where("sales_opportunities.organization_id = ?", orgID)
	query = a.visibleSalesOpportunities(query, userID, orgID)

	if stage := string(r.RequestCtx.QueryArgs().Peek("stage")); stage != "" {
		query = query.Where("sales_opportunities.stage = ?", stage)
	}
	if status := string(r.RequestCtx.QueryArgs().Peek("status")); status != "" {
		query = query.Where("sales_opportunities.status = ?", status)
	}
	if assignedUserID := string(r.RequestCtx.QueryArgs().Peek("assigned_user_id")); assignedUserID != "" {
		query = query.Where("sales_opportunities.assigned_user_id = ?", assignedUserID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to count sales opportunities", nil, "")
	}

	var opportunities []models.SalesOpportunity
	if err := query.Order("opened_at DESC").Offset(pg.Offset).Limit(pg.Limit).Find(&opportunities).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list sales opportunities", nil, "")
	}

	return r.SendEnvelope(map[string]any{
		"opportunities": opportunities,
		"total":         total,
		"has_more":      int64(pg.Offset+len(opportunities)) < total,
	})
}

// GetSalesOpportunity returns one opportunity's detail.
func (a *App) GetSalesOpportunity(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	return r.SendEnvelope(opp)
}

// ListSalesOpportunityEvents returns an opportunity's event timeline, oldest first.
func (a *App) ListSalesOpportunityEvents(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	var events []models.SalesOpportunityEvent
	if err := a.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at ASC").Find(&events).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to list events", nil, "")
	}
	return r.SendEnvelope(map[string]any{"events": events})
}
