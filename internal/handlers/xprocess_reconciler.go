package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
)

// xprocessClosedGracePeriod is how long the job keeps checking a link after
// the first time it sees FECHADO, in case the pedido gets cancelled after
// invoicing (design §3, §5).
const xprocessClosedGracePeriod = 7 * 24 * time.Hour

// reconcileXProcessLink checks one pending link against X2 and applies the
// design's §5 outcome rules. Never called directly by an HTTP handler —
// only by RunXProcessReconciliation (Task 11) and by tests via the small
// exported wrapper below.
func (a *App) reconcileXProcessLink(client *xprocess.Client, apiKey string, link *models.SalesOpportunityXProcessLink) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now()

	items, err := client.ConsultarPedido(ctx, apiKey, link.NumPedido, link.Documento)
	if errors.Is(err, xprocess.ErrPedidoNaoEncontrado) {
		a.DB.Model(link).Updates(map[string]any{
			"last_checked_at":       now,
			"consecutive_not_found": link.ConsecutiveNotFound + 1,
		})
		return
	}
	if err != nil {
		a.Log.Error("xprocess reconciliation: request failed", "link_id", link.ID, "num_pedido", link.NumPedido, "error", err)
		a.DB.Model(link).Update("last_checked_at", now)
		return
	}

	resumo, err := xprocess.SummarizePedido(items)
	if err != nil {
		a.Log.Error("xprocess reconciliation: failed to summarize pedido", "link_id", link.ID, "error", err)
		a.DB.Model(link).Update("last_checked_at", now)
		return
	}

	var opp models.SalesOpportunity
	if err := a.DB.First(&opp, "id = ?", link.SalesOpportunityID).Error; err != nil {
		a.Log.Error("xprocess reconciliation: opportunity not found", "link_id", link.ID, "opportunity_id", link.SalesOpportunityID, "error", err)
		return
	}

	itensJSON, err := xprocessItemsToJSONB(resumo.Itens)
	if err != nil {
		a.Log.Error("xprocess reconciliation: failed to encode itens", "link_id", link.ID, "error", err)
	}

	updates := map[string]any{
		"last_checked_at":       now,
		"consecutive_not_found": 0,
		"cod_empresa":           resumo.CodEmpresa,
		"cod_vendedor":          resumo.CodVendedor,
		"status_xprocess":       resumo.Status,
		"valor_vendido":         resumo.ValorTotal,
		"itens":                 itensJSON,
	}

	switch resumo.Status {
	case "SEPARACAO", "SEPARADO":
		if opp.Status == models.SalesOpportunityStatusAberta {
			a.convertXProcessOpportunity(&opp, link)
		}
		// resolved_at stays nil — keep checking until FECHADO or CANCELADO.

	case "FECHADO":
		if opp.Status == models.SalesOpportunityStatusAberta {
			a.convertXProcessOpportunity(&opp, link)
		}
		firstClosedAt := link.FirstClosedAt
		if firstClosedAt == nil {
			updates["first_closed_at"] = now
			firstClosedAt = &now
		}
		if now.After(firstClosedAt.Add(xprocessClosedGracePeriod)) {
			updates["resolved_at"] = now
		}

	case "CANCELADO":
		if opp.Status == models.SalesOpportunityStatusConvertida {
			a.cancelXProcessOpportunity(&opp, link)
		} else if opp.Status == models.SalesOpportunityStatusAberta {
			a.loseXProcessOpportunity(&opp, link)
		}
		updates["resolved_at"] = now
	}

	a.DB.Model(link).Updates(updates)
}

// convertXProcessOpportunity mirrors ConvertSalesOpportunity's own DB
// writes exactly, but with source=xprocess and no HTTP actor — this is a
// background job, not a user request.
func (a *App) convertXProcessOpportunity(opp *models.SalesOpportunity, link *models.SalesOpportunityXProcessLink) {
	now := time.Now()
	source := models.SalesConversionSourceXProcess
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(map[string]any{
			"status":            models.SalesOpportunityStatusConvertida,
			"conversion_source": source, "converted_at": now,
			"sla_breached": false,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return // already converted (manually or by a concurrent run) — no event, no duplicate
	}
	a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: link.OrganizationID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventConverted, Source: models.SalesOpportunityEventSourceXProcess,
	})
	opp.Status = models.SalesOpportunityStatusConvertida
}

// loseXProcessOpportunity marks an opportunity that never converted as lost
// because X2 shows its pedido cancelled (design §5).
func (a *App) loseXProcessOpportunity(opp *models.SalesOpportunity, link *models.SalesOpportunityXProcessLink) {
	now := time.Now()
	reason := models.SalesLossReasonOutro
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusAberta).
		Updates(map[string]any{
			"status":      models.SalesOpportunityStatusPerdida,
			"loss_reason": reason, "loss_notes": "Cancelado no X2 (pedido " + link.NumPedido + ")",
			"lost_at": now, "sla_breached": false,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		return
	}
	a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: link.OrganizationID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventLost, Source: models.SalesOpportunityEventSourceXProcess,
	})
}

// cancelXProcessOpportunity marks an already-converted opportunity as
// cancelled without touching the original converted event (design §3, §5 —
// history must show "converted on X, cancelled on Y", never erase the sale
// existed).
func (a *App) cancelXProcessOpportunity(opp *models.SalesOpportunity, link *models.SalesOpportunityXProcessLink) {
	now := time.Now()
	result := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND status = ?", opp.ID, models.SalesOpportunityStatusConvertida).
		Updates(map[string]any{"status": models.SalesOpportunityStatusCancelada, "cancelled_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		return
	}
	a.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: link.OrganizationID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventCancelled, Source: models.SalesOpportunityEventSourceXProcess,
	})
}

// xprocessItemsToJSONB round-trips a typed slice through JSON into
// models.JSONBArray (which is itself []any) — simplest correct way to store
// xprocess.PedidoItem values in a jsonb column without hand-rolling a second
// marshaler. Named distinctly from occurrence_processes.go's toJSONBArray
// ([]string) — same package, different signature, so it can't share the name.
func xprocessItemsToJSONB(items []xprocess.PedidoItem) (models.JSONBArray, error) {
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return models.JSONBArray(out), nil
}

// ReconcileXProcessLinkForTest exposes reconcileXProcessLink to tests in
// this package (handlers_test) without widening the real, unexported API
// surface used by production code (RunXProcessReconciliation, Task 11).
func (a *App) ReconcileXProcessLinkForTest(client *xprocess.Client, apiKey string, link *models.SalesOpportunityXProcessLink) {
	a.reconcileXProcessLink(client, apiKey, link)
}
