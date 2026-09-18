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
	opp, err := findByIDAndOrg[models.SalesOpportunity](a.DB.Preload("Contact"), r, id, orgID, "Sales opportunity")
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
	query := a.DB.Model(&models.SalesOpportunity{}).Preload("Contact").Where("sales_opportunities.organization_id = ?", orgID)
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

type changeSalesOpportunityStageRequest struct {
	Stage string `json:"stage"`
}

// ChangeSalesOpportunityStage advances potencial -> abrir_orcamento ->
// direcionada. Entering "direcionada" requires direcionamento to already be
// set (spec §5) and stamps stage_changed_at, the SLA clock's base (spec §6).
// Leaving direcionada clears sla_breached back to false since the SLA clock
// only runs while the opportunity sits in direcionada (Task 9 depends on
// this reset).
func (a *App) ChangeSalesOpportunityStage(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can change stage", nil, "")
	}

	var req changeSalesOpportunityStageRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	newStage := models.SalesOpportunityStage(req.Stage)
	if newStage != models.SalesOpportunityStagePotencial &&
		newStage != models.SalesOpportunityStageAbrirOrcamento &&
		newStage != models.SalesOpportunityStageDirecionada {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid stage", nil, "")
	}
	if newStage == models.SalesOpportunityStageDirecionada && opp.Direcionamento == nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "direcionamento is required before entering direcionada", nil, "")
	}

	// Resending the stage the opportunity is already in is a no-op, not a
	// transition: without this guard it would restamp stage_changed_at with
	// now() (the SLA clock's base, spec §6) even though the opportunity never
	// left the stage, and log a spurious "X → X" stage_changed event. Mirrors
	// ChangeOccurrenceStage's identical guard in occurrences.go.
	if newStage == opp.Stage {
		return r.SendEnvelope(opp)
	}

	fromStage := opp.Stage
	now := time.Now()
	updates := map[string]any{"stage": newStage, "stage_changed_at": now}
	if newStage != models.SalesOpportunityStageDirecionada {
		updates["sla_breached"] = false
	}
	// Finding 8 (TOCTOU): re-check status=aberta atomically in the WHERE
	// clause instead of trusting the Go-level read above — a concurrent
	// convert/lose landing between that read and this write must not let a
	// closed opportunity's stage silently change.
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(updates)
	if result.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to change stage", nil, "")
	}
	if result.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can change stage", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventStageChanged,
		FromStage: &fromStage, ToStage: &newStage, Source: models.SalesOpportunityEventSourceManual,
		CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type changeSalesOpportunityDirecionamentoRequest struct {
	Direcionamento string `json:"direcionamento"`
}

// ChangeSalesOpportunityDirecionamento edits direcionamento as a plain form
// field, not a stage transition — allowed in any stage while status=aberta
// (spec §5).
func (a *App) ChangeSalesOpportunityDirecionamento(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can change direcionamento", nil, "")
	}

	var req changeSalesOpportunityDirecionamentoRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	direcionamento := models.SalesDirecionamento(req.Direcionamento)
	if direcionamento != models.SalesDirecionamentoVisita && direcionamento != models.SalesDirecionamentoWhatsApp {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Invalid direcionamento", nil, "")
	}

	// Resending the direcionamento it already has is a no-op: without this
	// guard it would write another direcionamento_changed event every time,
	// same class of issue as ChangeSalesOpportunityStage's stage guard above.
	if opp.Direcionamento != nil && *opp.Direcionamento == direcionamento {
		return r.SendEnvelope(opp)
	}

	// Finding 8 (TOCTOU): re-check status=aberta atomically, same reasoning
	// as ChangeSalesOpportunityStage above.
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Update("direcionamento", direcionamento)
	if result.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to change direcionamento", nil, "")
	}
	if result.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can change direcionamento", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventDirecionamentoChanged,
		Source: models.SalesOpportunityEventSourceManual, CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type updateSalesOpportunityDetailsRequest struct {
	Interest          *string  `json:"interest"`
	EstimatedValue    *float64 `json:"estimated_value"`
	EstimatedQuantity *int     `json:"estimated_quantity"`
}

// UpdateSalesOpportunityDetails edits interest/estimated_value/estimated_quantity
// — plain fields filled in by the agent (spec §4), not a funnel transition.
// Unlike stage/direcionamento/convert/lose, this does NOT write a
// SalesOpportunityEvent: the spec only requires an event for transitions and
// direcionamento changes, not for editing these fields.
func (a *App) UpdateSalesOpportunityDetails(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be edited", nil, "")
	}

	var req updateSalesOpportunityDetailsRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}

	updates := map[string]any{}
	if req.Interest != nil {
		updates["interest"] = *req.Interest
	}
	if req.EstimatedValue != nil {
		updates["estimated_value"] = *req.EstimatedValue
	}
	if req.EstimatedQuantity != nil {
		updates["estimated_quantity"] = *req.EstimatedQuantity
	}
	if len(updates) == 0 {
		return r.SendEnvelope(opp)
	}

	// Same TOCTOU guard as the other mutating handlers (finding 8): re-check
	// status=aberta atomically in the WHERE clause instead of trusting the Go
	// read above, so a concurrent convert/lose landing between that read and
	// this write can't silently let a closed opportunity's fields change.
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(updates)
	if result.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update opportunity", nil, "")
	}
	if result.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be edited", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

// ConvertSalesOpportunity marks the opportunity converted. Manual only in
// this delivery — conversion_source is always "manual" (spec §2, §9).
// Validates the state machine (spec §5.1): only aberta -> convertida; stage
// is left untouched, it freezes at whatever value it had on conversion.
func (a *App) ConvertSalesOpportunity(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be converted", nil, "")
	}

	now := time.Now()
	source := models.SalesConversionSourceManual
	// Finding 8 (TOCTOU): re-check status=aberta atomically — this is the
	// pair to LoseSalesOpportunity's own guard below; without it, two
	// concurrent convert+lose (or convert+convert) requests on the same
	// opportunity could both pass the Go-level check and both write.
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(map[string]any{
			"status":            models.SalesOpportunityStatusConvertida,
			"conversion_source": source, "converted_at": now,
			"sla_breached": false,
		})
	if result.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to convert opportunity", nil, "")
	}
	if result.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be converted", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventConverted,
		Source: models.SalesOpportunityEventSourceManual, CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type loseSalesOpportunityRequest struct {
	LossReason string `json:"loss_reason"`
	LossNotes  string `json:"loss_notes"`
}

// LoseSalesOpportunity marks the opportunity lost. loss_reason is required
// and must be one of the closed list (spec §4, §5.1). Validates the state
// machine (spec §5.1): only aberta -> perdida; stage is left untouched.
func (a *App) LoseSalesOpportunity(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionWrite)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}
	if opp.Status != models.SalesOpportunityStatusAberta {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be marked lost", nil, "")
	}

	var req loseSalesOpportunityRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	reason := models.SalesLossReason(req.LossReason)
	if !models.ValidSalesLossReasons[reason] {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "loss_reason is required and must be a valid reason", nil, "")
	}

	now := time.Now()
	// Finding 8 (TOCTOU): re-check status=aberta atomically, same reasoning
	// as ConvertSalesOpportunity above.
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(map[string]any{
			"status":      models.SalesOpportunityStatusPerdida,
			"loss_reason": reason, "loss_notes": req.LossNotes, "lost_at": now,
			"sla_breached": false,
		})
	if result.Error != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to mark opportunity lost", nil, "")
	}
	if result.RowsAffected == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Only open opportunities can be marked lost", nil, "")
	}
	if err := a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: orgID, SalesOpportunityID: opp.ID, Type: models.SalesOpportunityEventLost,
		Source: models.SalesOpportunityEventSourceManual, CreatedByID: &userID,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to record event", nil, "")
	}

	a.DB.First(opp, "id = ?", opp.ID)
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
