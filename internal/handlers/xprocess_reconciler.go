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

	// One X2 order must never feed two opportunities. If an OLDER link of another
	// opportunity already tracks this exact order, this (newer) link changes nothing
	// and is left for a person; the older one keeps being reconciled.
	if a.xprocessOrderHeldByOlderLink(link, resumo.CodEmpresa) {
		a.Log.Warn("xprocess reconciliation: order already tracked by an older link of another opportunity; leaving this one alone",
			"link_id", link.ID, "opportunity_id", link.SalesOpportunityID, "cod_empresa", resumo.CodEmpresa, "num_pedido", link.NumPedido)
		a.DB.Model(link).Update("last_checked_at", now)
		return
	}

	updates := map[string]any{
		"last_checked_at":       now,
		"consecutive_not_found": 0,
		"cod_empresa":           resumo.CodEmpresa,
		"cod_vendedor":          resumo.CodVendedor,
		"status_xprocess":       resumo.Status,
		"valor_vendido":         resumo.ValorTotal,
		// Freight is recorded apart and never added to valor_vendido / realized_value.
		"valor_frete": resumo.ValorFrete,
		"itens":       itensJSON,
	}

	switch resumo.Status {
	case "SEPARACAO", "SEPARADO":
		if opp.Status == models.SalesOpportunityStatusAberta {
			a.convertXProcessOpportunity(&opp, link)
		}
		a.setRealizedValueFromXProcess(&opp, resumo.ValorTotal)
		// resolved_at stays nil — keep checking until FECHADO or CANCELADO.

	case "FECHADO":
		if opp.Status == models.SalesOpportunityStatusAberta {
			a.convertXProcessOpportunity(&opp, link)
		}
		a.setRealizedValueFromXProcess(&opp, resumo.ValorTotal)
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

	err = a.DB.Model(link).Updates(updates).Error
	if err != nil && isUniqueViolation(err) {
		// The unique index on open (cod_empresa, num_pedido) refused the company code:
		// another link holds the order. Keep reconciling this link without it.
		delete(updates, "cod_empresa")
		err = a.DB.Model(link).Updates(updates).Error
	}
	if err != nil {
		a.Log.Error("xprocess reconciliation: failed to update link", "link_id", link.ID, "error", err)
	}
}

// setRealizedValueFromXProcess records X2's order total as the opportunity's
// realized_value. X2 is the official source of what was actually sold, so on a
// valid reconciliation (order found, not cancelled, positive total) it replaces
// a manually entered realized_value. Only a converted opportunity is touched,
// and never estimated_value or realized_quantity (X2 does not return quantities
// yet). A manual edit made while the link is still being re-checked is
// overwritten on the next round, by design.
func (a *App) setRealizedValueFromXProcess(opp *models.SalesOpportunity, total float64) {
	if total <= 0 {
		return
	}
	if err := a.DB.Model(&models.SalesOpportunity{}).
		Where("id = ? AND organization_id = ? AND status = ?", opp.ID, opp.OrganizationID, models.SalesOpportunityStatusConvertida).
		Where("realized_value IS DISTINCT FROM ?", total).
		Update("realized_value", total).Error; err != nil {
		a.Log.Error("xprocess reconciliation: failed to record realized value", "opportunity_id", opp.ID, "error", err)
	}
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

// RunXProcessReconciliation is one full sweep: every active
// XProcessIntegration, every one of that organization's unresolved links.
// Called once daily by XProcessReconciler.Start, and directly by the
// backfill/manual-trigger path if one is ever added.
func (a *App) RunXProcessReconciliation() {
	var integrations []models.XProcessIntegration
	if err := a.DB.Where("is_active = ?", true).Find(&integrations).Error; err != nil {
		a.Log.Error("xprocess reconciliation: failed to load integrations", "error", err)
		return
	}
	for i := range integrations {
		integ := integrations[i]
		integ.DecryptSecrets(a.Config.App.EncryptionKey)

		var links []models.SalesOpportunityXProcessLink
		if err := a.DB.Where("organization_id = ? AND resolved_at IS NULL", integ.OrganizationID).Find(&links).Error; err != nil {
			a.Log.Error("xprocess reconciliation: failed to load pending links", "org_id", integ.OrganizationID, "error", err)
			continue
		}
		client := xprocess.New(a.Log, integ.BaseURL)
		for j := range links {
			a.reconcileXProcessLink(client, integ.APIKey, &links[j])
		}
		if len(links) > 0 {
			a.Log.Info("xprocess reconciliation: organization done", "org_id", integ.OrganizationID, "links_checked", len(links))
		}

		// Discovery runs after the sweep, and also when there were no pending links:
		// a converted opportunity with no link at all is exactly what it looks for.
		a.discoverXProcessOrders(client, integ.APIKey, integ.OrganizationID)
	}
}

// XProcessReconciler runs RunXProcessReconciliation once per calendar day,
// starting at triggerHour. Mirrors SLAProcessor's ticker+goroutine shape
// (internal/handlers/sla_processor.go) rather than adding a cron
// dependency — the interval just needs to be short enough that the daily
// run starts promptly after triggerHour, nothing fancier.
//
// lastRunDate is in-memory only: a server restart between triggerHour and
// midnight will run the sweep again that day. Harmless — reconciliation is
// naturally idempotent per link (an already-resolved link is never
// re-selected, and re-checking an unresolved one just re-fetches the same
// D-1 snapshot) — not worth a persisted "did we run today" ledger.
// ponytail: in-memory day tracking, move to a persisted marker if this ever
// runs across multiple server instances (it doesn't, as of this entrega).
type XProcessReconciler struct {
	app         *App
	interval    time.Duration
	triggerHour int
	runFunc     func()
	lastRunDate string
	stopCh      chan struct{}
}

// NewXProcessReconciler creates a reconciler that calls
// app.RunXProcessReconciliation once per day, on the first tick at or after
// triggerHour. The tick time is converted to appLocation (America/Bahia)
// before comparing against triggerHour, not compared as server local time.
func NewXProcessReconciler(app *App, interval time.Duration, triggerHour int) *XProcessReconciler {
	r := &XProcessReconciler{app: app, interval: interval, triggerHour: triggerHour, stopCh: make(chan struct{})}
	r.runFunc = app.RunXProcessReconciliation
	return r
}

func (p *XProcessReconciler) Start(ctx context.Context) {
	p.app.Log.Info("XProcess reconciler started", "interval", p.interval, "trigger_hour", p.triggerHour)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.maybeRun(time.Now())
		}
	}
}

func (p *XProcessReconciler) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

// maybeRun compares against the deployment's real local time (appLocation,
// America/Bahia by default), not the server's bare wall clock. The
// deployment environment runs with TZ=UTC, so a naive time.Now().Hour()
// read as "2" is actually ~23:00 in the operation's real local time — well
// before X2's own ~2h local reload (helpers.go's appLocation doc, design §3).
func (p *XProcessReconciler) maybeRun(now time.Time) {
	now = now.In(appLocation)
	today := now.Format("2006-01-02")
	if now.Hour() < p.triggerHour || p.lastRunDate == today {
		return
	}
	p.lastRunDate = today
	p.runFunc()
}

// NewXProcessReconcilerForTest and MaybeRunForTest let
// TestXProcessReconciler_RunsOnceAtTriggerHourNotBefore drive maybeRun with
// fixed timestamps instead of waiting on a real ticker, and substitute a
// counting stub for the real (DB-hitting) RunXProcessReconciliation.
func NewXProcessReconcilerForTest(app *App, runFunc func()) *XProcessReconciler {
	r := NewXProcessReconciler(app, time.Minute, 2)
	r.runFunc = runFunc
	return r
}

func (p *XProcessReconciler) MaybeRunForTest(now time.Time) { p.maybeRun(now) }
