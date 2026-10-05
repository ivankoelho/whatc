package handlers

import (
	"context"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/zerodha/fastglue"
)

type xprocessCandidateResponse struct {
	NumPedido    string     `json:"num_pedido"`
	CodEmpresa   string     `json:"cod_empresa"`
	UnitName     string     `json:"unit_name,omitempty"` // the local unit linked to that X2 store, if any
	Status       string     `json:"status"`
	ValorVendido float64    `json:"valor_vendido"`
	DataVenda    *time.Time `json:"data_venda,omitempty"`
}

// ListSalesOpportunityXProcessCandidates looks live (no persisted table —
// this is an on-demand hint, not part of the daily reconciliation job) for
// X2 pedidos the customer placed after this opportunity was opened, that
// aren't already linked to any opportunity — the "cliente comprou
// recentemente com outro vendedor" scenario (spec item G). Never
// auto-attributes anything; the agent must explicitly "Vincular" — matches
// the design's own non-objective against fuzzy auto-matching, and the
// user's 2026-09-27 decision: identification automatic, linking always
// manual.
//
// Every failure path (no integration, cliente not found, X2 unreachable)
// degrades to an empty candidate list rather than an error — this is a
// "nice to have" hint on top of an already-working panel, not something
// that should ever break the page.
func (a *App) ListSalesOpportunityXProcessCandidates(r *fastglue.Request) error {
	orgID, userID, err := a.requireAuth(r, models.ResourceSalesOpportunities, models.ActionRead)
	if err != nil {
		return nil
	}
	opp, err := a.loadAuthorizedSalesOpportunity(r, orgID, userID)
	if err != nil {
		return nil
	}

	empty := func() error { return r.SendEnvelope(map[string]any{"candidates": []xprocessCandidateResponse{}}) }

	// Meaningful while the opportunity is open, and also once it is converted: an
	// agent may convert before the X2 order exists (X2 is D-1), and then link it by
	// hand when it shows up. Lost/cancelled ones have nothing to link.
	if opp.Status != models.SalesOpportunityStatusAberta && opp.Status != models.SalesOpportunityStatusConvertida {
		return empty()
	}
	// documento is usually empty for a sales-only conversation (design doc
	// §1: only filled from SAC or from a link this contact registered
	// before) — without it there is nothing to search X2 for.
	documento := oppDocumento(opp)
	if documento == "" {
		return empty()
	}

	var integ models.XProcessIntegration
	if err := a.DB.Where("organization_id = ? AND is_active = ?", orgID, true).First(&integ).Error; err != nil {
		return empty()
	}
	integ.DecryptSecrets(a.Config.App.EncryptionKey)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := xprocess.New(a.Log, integ.BaseURL)

	resumos, _, err := findXProcessOrders(ctx, client, integ.APIKey, documento, xprocessVendasLimit)
	if err != nil {
		a.Log.Warn("xprocess candidates: X2 lookup failed", "opportunity_id", opp.ID, "error", err)
		return empty()
	}

	// A pedido already tracked by some opportunity's own link (this one or
	// another) shouldn't be re-suggested as a "new" find.
	var trackedNumPedidos []string
	a.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("organization_id = ? AND documento = ?", orgID, documento).
		Pluck("num_pedido", &trackedNumPedidos)
	tracked := make(map[string]bool, len(trackedNumPedidos))
	for _, n := range trackedNumPedidos {
		tracked[n] = true
	}

	codes := make([]string, 0, len(resumos))
	for _, resumo := range resumos {
		codes = append(codes, resumo.CodEmpresa)
	}
	unitsByCode := a.unitsByXProcessCode(orgID, codes...)
	candidates := make([]xprocessCandidateResponse, 0)
	for _, resumo := range resumos {
		if tracked[resumo.NumPedido] {
			continue
		}
		dataVenda, ok := parseXProcessDataVenda(resumo.DataVenda)
		// Only a purchase on or after the DAY this opportunity was opened counts
		// (an earlier purchase isn't a result of this funnel entry). Compared by
		// date: X2's data_venda is always midnight, so a same-day sale must count.
		if !ok || !saleOnOrAfterOpening(dataVenda, opp.OpenedAt) {
			continue
		}
		candidates = append(candidates, xprocessCandidateResponse{
			NumPedido: resumo.NumPedido, CodEmpresa: resumo.CodEmpresa,
			Status: resumo.Status, ValorVendido: resumo.ValorTotal, DataVenda: &dataVenda,
			UnitName: unitsByCode[resumo.CodEmpresa].Name,
		})
	}

	return r.SendEnvelope(map[string]any{"candidates": candidates})
}

// parseXProcessDataVenda parses X2's data_venda format (confirmed live
// against the real API: "2022-05-23T00:00:00", no timezone, no fractional
// seconds). A nil/empty/unparseable value is common (e.g. a CANCELADO
// pedido may carry a null data_venda) — the caller treats that as "not a
// usable candidate", not an error.
func parseXProcessDataVenda(raw *string) (time.Time, bool) {
	if raw == nil || *raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02T15:04:05", *raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
