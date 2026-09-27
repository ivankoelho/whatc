package handlers

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type upsertSalesOpportunityXProcessLinkRequest struct {
	NumPedido string `json:"num_pedido"`
	Documento string `json:"documento"`
}

// UpsertSalesOpportunityXProcessLink registers (or edits, while still
// unresolved) the X2 pedido number + documento for an open opportunity
// (design §6). Never calls the X2 API — the daily reconciliation job does
// that (design's core D-1 constraint, §1).
func (a *App) UpsertSalesOpportunityXProcessLink(r *fastglue.Request) error {
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

	var req upsertSalesOpportunityXProcessLinkRequest
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.NumPedido == "" {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "num_pedido is required", nil, "")
	}
	documento, err := contactutil.NormalizeDocumento(req.Documento)
	if err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, err.Error(), nil, "")
	}

	// Find this opportunity's open (unresolved) link, if any — design §4/§6:
	// while unresolved, editing updates the same row; once resolved, a new
	// registration must create a new row instead (never overwrite).
	var openLink models.SalesOpportunityXProcessLink
	hasOpenLink := a.DB.Where("sales_opportunity_id = ? AND resolved_at IS NULL", opp.ID).
		First(&openLink).Error == nil

	createLink := func() error {
		newLink := models.SalesOpportunityXProcessLink{
			OrganizationID:     orgID,
			SalesOpportunityID: opp.ID,
			NumPedido:          req.NumPedido,
			Documento:          documento,
		}
		return a.DB.Create(&newLink).Error
	}

	if hasOpenLink {
		// Finding 8 (TOCTOU): re-check resolved_at IS NULL atomically in the
		// UPDATE's own WHERE clause, same reasoning as ConvertSalesOpportunity/
		// LoseSalesOpportunity above. Without it, a concurrent reconciliation
		// run (a later task) resolving this exact link between our read above
		// and this write would let us silently overwrite num_pedido/documento
		// on a row that's supposed to be immutable once resolved. If we lose
		// that race (RowsAffected == 0), fall back to creating a new row —
		// same as the "no open link" branch — so the agent's registration
		// isn't dropped just because a reconciliation run landed at the same
		// moment.
		result := a.DB.Model(&models.SalesOpportunityXProcessLink{}).
			Where("id = ? AND resolved_at IS NULL", openLink.ID).
			Updates(map[string]any{
				"num_pedido":            req.NumPedido,
				"documento":             documento,
				"consecutive_not_found": 0,
				"last_checked_at":       nil,
			})
		if result.Error != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update link", nil, "")
		}
		if result.RowsAffected == 0 {
			if err := createLink(); err != nil {
				return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create link", nil, "")
			}
		}
	} else {
		if err := createLink(); err != nil {
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to create link", nil, "")
		}
	}

	if err := a.DB.Model(&models.SalesOpportunity{}).Where("id = ?", opp.ID).Updates(map[string]any{
		"xprocess_num_pedido": req.NumPedido,
		"xprocess_documento":  documento,
	}).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Failed to update opportunity", nil, "")
	}

	// Fill Contact.CPFCNPJ only if empty — never clobber a value it already
	// had (design §3: "Preenchido a partir do documento informado, se
	// estiver vazio"). Column is physically "cpfcnpj" (no underscore): GORM's
	// default snake_case for the all-caps Go field CPFCNPJ, with no explicit
	// `column:` override on the model. This already broke contact saves once
	// in this exact codebase (see commit 00e573c, "fix(contacts): use the
	// real cpfcnpj column name in map-based updates") — do not "fix" this
	// back to cpf_cnpj, it is deliberately the physical column name.
	a.DB.Model(&models.Contact{}).
		Where("id = ? AND (cpfcnpj IS NULL OR cpfcnpj = '')", opp.ContactID).
		Update("cpfcnpj", documento)

	a.DB.First(opp, "id = ?", opp.ID)
	return r.SendEnvelope(opp)
}

type salesOpportunityXProcessLinkResponse struct {
	NumPedido      string     `json:"num_pedido"`
	Documento      string     `json:"documento"`
	StatusXProcess *string    `json:"status_xprocess,omitempty"`
	ValorVendido   *float64   `json:"valor_vendido,omitempty"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	// PendingReview mirrors the design's §5/§8 rule: 5+ consecutive 404
	// rounds, still unresolved. Computed here, not stored — the wording
	// shown to the agent must say "not located", never "invalid" (design's
	// global constraints).
	PendingReview bool `json:"pending_review"`
}

// xprocessNotFoundReviewThreshold is the number of consecutive job rounds
// with a 404 before a link surfaces for agent review (design §3, §5, §8).
const xprocessNotFoundReviewThreshold = 5

// GetSalesOpportunityXProcessLink returns the opportunity's current link —
// the open one if there is one, otherwise the most recently resolved one.
func (a *App) GetSalesOpportunityXProcessLink(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}

	var link models.SalesOpportunityXProcessLink
	if err := a.DB.Where("sales_opportunity_id = ?", opp.ID).
		Order("resolved_at IS NULL DESC, created_at DESC").
		First(&link).Error; err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "No X2 link registered for this opportunity", nil, "")
	}

	return r.SendEnvelope(salesOpportunityXProcessLinkResponse{
		NumPedido: link.NumPedido, Documento: link.Documento,
		StatusXProcess: link.StatusXProcess, ValorVendido: link.ValorVendido,
		LastCheckedAt: link.LastCheckedAt, ResolvedAt: link.ResolvedAt,
		PendingReview: link.ResolvedAt == nil && link.ConsecutiveNotFound >= xprocessNotFoundReviewThreshold,
	})
}
