package handlers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"gorm.io/gorm"
)

// Discovery of X2 orders for opportunities that were converted BEFORE the order
// existed. X2 is D-1, so an agent can convert on the day of the sale and the
// order only reaches the API the next morning; the agent never typed its number.
// The job finds it by the customer's documento:
//
//	documento -> /api/clientes -> cod_cliente -> /api/vendas -> orders
//
// and links it ONLY when there is no doubt. Rules (all of them must hold):
//   - only an opportunity that is `convertida`, has NO link at all, and was
//     converted at most xprocessDiscoveryWindow ago is linked automatically;
//   - an order is a candidate only if it is SEPARACAO, SEPARADO or FECHADO (a
//     CANCELADO order never opens a new link), is not tracked by any link yet,
//     and its sale DATE is on or after the opportunity's opening DATE;
//   - the seller only vetoes: when both the opportunity's owner has an X2 seller
//     code and the order has a cod_vendedor, they must be equal;
//   - exactly ONE candidate order and exactly ONE eligible opportunity (among
//     every open or converted, still unlinked opportunity of that documento,
//     whatever the contact). Zero or more than one on either side -> nothing is
//     linked; the agent still gets the order as a manual candidate;
//   - a /api/vendas list with exactly the requested limit may be truncated (the
//     API has no pagination and no guaranteed order) -> never linked automatically;
//   - discovery never replaces, undoes or edits an existing link.
const (
	// xprocessDiscoveryWindow is how long after conversion the job looks for the
	// order. It bounds the work; the opportunity stays converted afterwards and a
	// person can still link the order by hand.
	xprocessDiscoveryWindow = 7 * 24 * time.Hour

	// xprocessVendasLimit is the most /api/vendas returns per call.
	xprocessVendasLimit = 1000
)

// xprocessDiscoverableStatus are the order states that can open a new link.
func xprocessDiscoverableStatus(status string) bool {
	switch status {
	case "SEPARACAO", "SEPARADO", "FECHADO":
		return true
	}
	return false
}

// saleOnOrAfterOpening compares DATES, not instants: X2's data_venda is always a
// bare date at 00:00:00, so a sale made the same day the opportunity was opened
// must count even though midnight is earlier than the opening time.
func saleOnOrAfterOpening(dataVenda, openedAt time.Time) bool {
	sale := time.Date(dataVenda.Year(), dataVenda.Month(), dataVenda.Day(), 0, 0, 0, 0, time.UTC)
	opened := openedAt.In(appLocation)
	open := time.Date(opened.Year(), opened.Month(), opened.Day(), 0, 0, 0, 0, time.UTC)
	return !sale.Before(open)
}

// sellerCompatible: the seller only vetoes. Either side missing -> compatible.
func sellerCompatible(oppSeller *string, orderSeller string) bool {
	opp := ""
	if oppSeller != nil {
		opp = strings.TrimSpace(*oppSeller)
	}
	order := strings.TrimSpace(orderSeller)
	if opp == "" || order == "" {
		return true
	}
	return opp == order
}

func oppSellerCode(o *models.SalesOpportunity) *string {
	if o.AssignedUser == nil {
		return nil
	}
	return o.AssignedUser.XProcessSellerCode
}

// xprocessMatch is a safe automatic association.
type xprocessMatch struct {
	opp    *models.SalesOpportunity
	order  xprocess.PedidoResumo
	reason string
}

// decideXProcessMatch is the whole association rule, free of I/O so it can be
// tested exhaustively. orders are the customer's grouped X2 orders, opps every
// open or converted, unlinked opportunity of the same documento, claimed says
// whether an order is already tracked by some link. It returns nil whenever the
// answer is not exactly one safe pair.
func decideXProcessMatch(orders []xprocess.PedidoResumo, opps []models.SalesOpportunity, claimed func(xprocess.PedidoResumo) bool, now time.Time) *xprocessMatch {
	type candidate struct {
		order xprocess.PedidoResumo
		sale  time.Time
	}
	var candidates []candidate
	for _, o := range orders {
		if !xprocessDiscoverableStatus(o.Status) || claimed(o) {
			continue
		}
		sale, ok := parseXProcessDataVenda(o.DataVenda)
		if !ok {
			continue
		}
		fits := false
		for i := range opps {
			if saleOnOrAfterOpening(sale, opps[i].OpenedAt) {
				fits = true
				break
			}
		}
		if fits {
			candidates = append(candidates, candidate{o, sale})
		}
	}
	if len(candidates) != 1 {
		return nil
	}
	c := candidates[0]

	var eligible []*models.SalesOpportunity
	for i := range opps {
		o := &opps[i]
		if saleOnOrAfterOpening(c.sale, o.OpenedAt) && sellerCompatible(oppSellerCode(o), c.order.CodVendedor) {
			eligible = append(eligible, o)
		}
	}
	if len(eligible) != 1 {
		return nil
	}
	o := eligible[0]
	if o.Status != models.SalesOpportunityStatusConvertida || o.ConvertedAt == nil ||
		o.ConvertedAt.Before(now.Add(-xprocessDiscoveryWindow)) {
		return nil
	}

	seller := "vendedor não verificado (sem código em um dos lados)"
	if code := oppSellerCode(o); code != nil && strings.TrimSpace(*code) != "" && strings.TrimSpace(c.order.CodVendedor) != "" {
		seller = "vendedor " + strings.TrimSpace(c.order.CodVendedor) + " compatível"
	}
	return &xprocessMatch{
		opp:   o,
		order: c.order,
		reason: fmt.Sprintf("único pedido candidato e única oportunidade elegível do documento; venda de %s em ou após a abertura; %s",
			c.sale.Format("2006-01-02"), seller),
	}
}

// findXProcessOrders resolves a documento to the customer's orders. truncated is
// true when X2 returned exactly `limit` lines: the API has no pagination and no
// promised order, so the list may be missing orders.
func findXProcessOrders(ctx context.Context, client *xprocess.Client, apiKey, documento string, limit int) (orders []xprocess.PedidoResumo, truncated bool, err error) {
	codCliente, err := client.ConsultarClientePorDocumento(ctx, apiKey, documento)
	if err != nil {
		return nil, false, err
	}
	items, err := client.ListarVendasPorCliente(ctx, apiKey, codCliente, limit)
	if err != nil {
		return nil, false, err
	}
	orders, err = xprocess.GroupPedidos(items)
	if err != nil {
		return nil, false, err
	}
	return orders, len(items) >= limit, nil
}

// xprocessOrderHeldByOlderLink is the check reconciliation makes before acting on
// a link: does ANOTHER opportunity's OLDER link already track this same order?
// "Same order" = same number and the same company, or an older link X2 has not
// answered yet (no cod_empresa) for the same documento.
//
// The OLDEST link owns the order and keeps being reconciled; only the newer one is
// held back. Comparing ages (not mere existence) matters for duplicates that
// already exist in the data: if each saw the other as "elsewhere", both would be
// frozen forever, including cancellations.
func (a *App) xprocessOrderHeldByOlderLink(link *models.SalesOpportunityXProcessLink, codEmpresa string) bool {
	var n int64
	a.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("organization_id = ? AND num_pedido = ? AND sales_opportunity_id <> ?",
			link.OrganizationID, link.NumPedido, link.SalesOpportunityID).
		Where("((cod_empresa = ?) OR (cod_empresa IS NULL AND documento = ?))", codEmpresa, link.Documento).
		Where("(created_at < ? OR (created_at = ? AND id < ?))", link.CreatedAt, link.CreatedAt, link.ID).
		Count(&n)
	return n > 0
}

// xprocessOrderClaimed is the conservative check used before linking: is this
// order tracked by ANY link, including one typed by an agent that X2 has not
// answered yet (no cod_empresa) — same number and same documento counts.
func (a *App) xprocessOrderClaimed(orgID uuid.UUID, codEmpresa, numPedido, documento string) bool {
	var n int64
	a.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("organization_id = ? AND num_pedido = ? AND ((cod_empresa = ?) OR (documento = ?))",
			orgID, numPedido, codEmpresa, documento).
		Count(&n)
	return n > 0
}

// oppDocumento is the documento used to search X2 for an opportunity: the one an
// agent registered, else the contact's, both validated like every other document.
func oppDocumento(o *models.SalesOpportunity) string {
	candidates := []string{}
	if o.XProcessDocumento != nil {
		candidates = append(candidates, *o.XProcessDocumento)
	}
	if o.Contact != nil {
		candidates = append(candidates, o.Contact.CPFCNPJ)
	}
	for _, raw := range candidates {
		if doc, err := contactutil.NormalizeDocumento(raw); err == nil && doc != "" {
			return doc
		}
	}
	return ""
}

const noLinkYet = "NOT EXISTS (SELECT 1 FROM sales_opportunity_xprocess_links l " +
	"WHERE l.sales_opportunity_id = sales_opportunities.id AND l.deleted_at IS NULL)"

// discoverXProcessOrders is one organization's discovery pass; see the rules at
// the top of this file. Best-effort: every failure is logged and skipped.
func (a *App) discoverXProcessOrders(client *xprocess.Client, apiKey string, orgID uuid.UUID) {
	now := time.Now()

	var seeds []models.SalesOpportunity
	if err := a.DB.Preload("Contact").
		Where("sales_opportunities.organization_id = ? AND sales_opportunities.status = ? AND sales_opportunities.converted_at >= ?",
			orgID, models.SalesOpportunityStatusConvertida, now.Add(-xprocessDiscoveryWindow)).
		Where(noLinkYet).
		Find(&seeds).Error; err != nil {
		a.Log.Error("xprocess discovery: failed to load converted opportunities", "org_id", orgID, "error", err)
		return
	}
	docSet := map[string]bool{}
	for i := range seeds {
		if doc := oppDocumento(&seeds[i]); doc != "" {
			docSet[doc] = true
		}
	}
	documentos := make([]string, 0, len(docSet))
	for d := range docSet {
		documentos = append(documentos, d)
	}
	sort.Strings(documentos) // deterministic order
	if len(documentos) == 0 {
		return
	}

	linked := 0
	for _, documento := range documentos {
		if a.discoverForDocumento(client, apiKey, orgID, documento, now) {
			linked++
		}
	}
	a.Log.Info("xprocess discovery: organization done", "org_id", orgID, "documentos", len(documentos), "linked", linked)
}

// discoverForDocumento returns true when it linked an order.
func (a *App) discoverForDocumento(client *xprocess.Client, apiKey string, orgID uuid.UUID, documento string, now time.Time) bool {
	// Every open or converted opportunity of this documento that has no link,
	// whatever its contact: they all compete for the same orders.
	var opps []models.SalesOpportunity
	if err := a.DB.Preload("AssignedUser").
		Select("sales_opportunities.*").
		Joins("JOIN contacts ON contacts.id = sales_opportunities.contact_id AND contacts.deleted_at IS NULL").
		Where("sales_opportunities.organization_id = ? AND sales_opportunities.status IN ?", orgID,
			[]models.SalesOpportunityStatus{models.SalesOpportunityStatusAberta, models.SalesOpportunityStatusConvertida}).
		Where("COALESCE(NULLIF(sales_opportunities.xprocess_documento, ''), contacts.cpfcnpj) = ?", documento).
		Where(noLinkYet).
		Find(&opps).Error; err != nil {
		a.Log.Error("xprocess discovery: failed to load opportunities", "org_id", orgID, "error", err)
		return false
	}
	// A converted opportunity past the discovery window is no longer a target, so it
	// does not compete either: otherwise any returning customer with an old, never
	// linked sale would block every future automatic match. Open ones always count.
	inPool := opps[:0]
	for _, o := range opps {
		if o.Status == models.SalesOpportunityStatusAberta ||
			(o.ConvertedAt != nil && !o.ConvertedAt.Before(now.Add(-xprocessDiscoveryWindow))) {
			inPool = append(inPool, o)
		}
	}
	opps = inPool
	if len(opps) == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	orders, truncated, err := findXProcessOrders(ctx, client, apiKey, documento, xprocessVendasLimit)
	if errors.Is(err, xprocess.ErrClienteNaoEncontrado) {
		return false // not in X2 yet (D-1) or never bought: try again tomorrow, inside the window
	}
	if err != nil {
		a.Log.Warn("xprocess discovery: lookup failed", "org_id", orgID, "error", err)
		return false
	}
	if truncated {
		a.Log.Info("xprocess discovery: vendas list may be truncated; not linking automatically", "org_id", orgID, "lines", xprocessVendasLimit)
		return false
	}

	match := decideXProcessMatch(orders, opps, func(o xprocess.PedidoResumo) bool {
		return a.xprocessOrderClaimed(orgID, o.CodEmpresa, o.NumPedido, documento)
	}, now)
	if match == nil {
		return false
	}

	link, ok := a.createDiscoveredXProcessLink(orgID, match, documento)
	if !ok {
		return false
	}
	// Fill status, values and freight right away through the normal path.
	a.reconcileXProcessLink(client, apiKey, link)
	return true
}

// createDiscoveredXProcessLink writes the link, the opportunity's pointer and the
// history event in one transaction. A unique violation (another run or an agent
// linked the order first) just means "someone else got there": nothing is changed.
func (a *App) createDiscoveredXProcessLink(orgID uuid.UUID, m *xprocessMatch, documento string) (*models.SalesOpportunityXProcessLink, bool) {
	codEmpresa, codVendedor := m.order.CodEmpresa, m.order.CodVendedor
	link := models.SalesOpportunityXProcessLink{
		OrganizationID:     orgID,
		SalesOpportunityID: m.opp.ID,
		NumPedido:          m.order.NumPedido,
		Documento:          documento,
		CodEmpresa:         &codEmpresa,
		CodVendedor:        &codVendedor,
		LinkSource:         "auto",
		MatchReason:        m.reason,
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&link).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.SalesOpportunity{}).Where("id = ?", m.opp.ID).Updates(map[string]any{
			"xprocess_num_pedido": m.order.NumPedido,
			"xprocess_documento":  documento,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&models.SalesOpportunityEvent{
			OrganizationID: orgID, SalesOpportunityID: m.opp.ID,
			Type: models.SalesOpportunityEventXProcessLinked, Source: models.SalesOpportunityEventSourceXProcess,
		}).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, false
		}
		a.Log.Error("xprocess discovery: failed to create link", "opportunity_id", m.opp.ID, "error", err)
		return nil, false
	}
	a.Log.Info("xprocess discovery: linked order to opportunity",
		"opportunity_id", m.opp.ID, "cod_empresa", codEmpresa, "num_pedido", m.order.NumPedido)
	return &link, true
}
