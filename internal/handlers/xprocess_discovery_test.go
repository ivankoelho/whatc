package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const discoveryCPF = "52998224725"

// x2Line is one /api/vendas or /api/pedido line, in the shape the real API
// returns (all numbers are BR-decimal strings, data_venda is always midnight).
type x2Line struct {
	Empresa, Pedido, Vendedor, Status, DataVenda string
	Total, Frete                                 string
}

func (l x2Line) json() map[string]any {
	return map[string]any{
		"cod_empresa": l.Empresa, "num_pedido": l.Pedido, "cod_cliente": "9", "cod_vendedor": l.Vendedor,
		"status": l.Status, "data_venda": l.DataVenda, "total": l.Total, "vl_frete": l.Frete,
	}
}

type fakeX2 struct {
	srv      *httptest.Server
	lines    []x2Line
	hasCli   bool
	requests int64
	extra    int // extra filler lines appended to /api/vendas (to simulate a full 1000-line list)
}

func newFakeX2(t *testing.T, lines ...x2Line) *fakeX2 {
	f := &fakeX2{lines: lines, hasCli: true}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&f.requests, 1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/clientes":
			if !f.hasCli {
				_, _ = w.Write([]byte(`{"ok":true,"total":0,"clientes":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"total":1,"clientes":[{"cod_cliente":"9"}]}`))
		case "/api/vendas":
			out := []map[string]any{}
			for _, l := range f.lines {
				out = append(out, l.json())
			}
			for i := 0; i < f.extra; i++ {
				out = append(out, x2Line{Empresa: "1", Pedido: "1", Status: "FECHADO", DataVenda: "2020-01-01T00:00:00", Total: "1,0"}.json())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "vendas": out})
		case "/api/pedido":
			var body struct {
				Numero string `json:"numero_pedido"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			out := []map[string]any{}
			for _, l := range f.lines {
				if l.Pedido == body.Numero {
					out = append(out, l.json())
				}
			}
			if len(out) == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"Pedido nao encontrado"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pedido": out})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// X2 dates are local calendar dates; the app compares them in America/Bahia.
func bahia() *time.Location {
	if loc, err := time.LoadLocation("America/Bahia"); err == nil {
		return loc
	}
	return time.FixedZone("BRT", -3*3600)
}

func today() string { return time.Now().In(bahia()).Format("2006-01-02") + "T00:00:00" }
func daysAgo(n int) string {
	return time.Now().In(bahia()).AddDate(0, 0, -n).Format("2006-01-02") + "T00:00:00"
}

type discoveryEnv struct {
	app   *handlers.App
	org   *models.Organization
	agent *models.User
}

func newDiscoveryEnv(t *testing.T) discoveryEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	return discoveryEnv{app, org, agent}
}

// convertedOpp creates a contact with the given documento and a manually
// converted opportunity for it, converted `convertedAgo` ago and opened `openedAgo` ago.
func (e discoveryEnv) convertedOpp(t *testing.T, documento string, openedAgo, convertedAgo time.Duration) *models.SalesOpportunity {
	t.Helper()
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	if documento != "" {
		require.NoError(t, e.app.DB.Model(contact).Update("cpfcnpj", documento).Error)
	}
	opp := newOpenOpportunity(t, e.app, e.org.ID, e.agent.ID, contact.ID)
	src := models.SalesConversionSourceManual
	require.NoError(t, e.app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": src,
		"converted_at": time.Now().Add(-convertedAgo), "opened_at": time.Now().Add(-openedAgo),
		"estimated_value": 1000,
	}).Error)
	return opp
}

func (e discoveryEnv) run(t *testing.T, f *fakeX2) {
	t.Helper()
	e.app.DiscoverXProcessOrdersForTest(xprocess.New(e.app.Log, f.srv.URL), "key", e.org.ID)
}

func (e discoveryEnv) links(t *testing.T, opp *models.SalesOpportunity) []models.SalesOpportunityXProcessLink {
	t.Helper()
	var l []models.SalesOpportunityXProcessLink
	require.NoError(t, e.app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&l).Error)
	return l
}

func (e discoveryEnv) reload(t *testing.T, opp *models.SalesOpportunity) models.SalesOpportunity {
	t.Helper()
	var o models.SalesOpportunity
	require.NoError(t, e.app.DB.First(&o, "id = ?", opp.ID).Error)
	return o
}

func order(pedido, status, data string) x2Line {
	return x2Line{Empresa: "40", Pedido: pedido, Vendedor: "630877", Status: status, DataVenda: data, Total: "1000,0", Frete: "0,0"}
}

// --- the happy path ---

func TestDiscovery_LinksTheSingleOrderToTheSingleConvertedOpportunity(t *testing.T) {
	e := newDiscoveryEnv(t)
	opp := e.convertedOpp(t, discoveryCPF, 24*time.Hour, 2*time.Hour)
	f := newFakeX2(t,
		x2Line{Empresa: "40", Pedido: "4317", Vendedor: "630877", Status: "FECHADO", DataVenda: today(), Total: "2000,50", Frete: "10,25"},
		x2Line{Empresa: "40", Pedido: "4317", Vendedor: "630877", Status: "FECHADO", DataVenda: today(), Total: "1000,00", Frete: "89,75"},
	)
	e.run(t, f)

	links := e.links(t, opp)
	require.Len(t, links, 1)
	l := links[0]
	assert.Equal(t, "auto", l.LinkSource)
	assert.NotEmpty(t, l.MatchReason)
	assert.Equal(t, "4317", l.NumPedido)
	assert.Equal(t, discoveryCPF, l.Documento)
	require.NotNil(t, l.CodEmpresa)
	assert.Equal(t, "40", *l.CodEmpresa)
	require.NotNil(t, l.ValorFrete)
	assert.InDelta(t, 100.0, *l.ValorFrete, 1e-6, "freight is the sum of the lines, kept apart")
	require.NotNil(t, l.ValorVendido)
	assert.InDelta(t, 3000.50, *l.ValorVendido, 1e-6, "the sold value never includes freight")

	got := e.reload(t, opp)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, got.Status)
	require.NotNil(t, got.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceManual, *got.ConversionSource, "discovery does not rewrite how it was converted")
	require.NotNil(t, got.RealizedValue)
	assert.InDelta(t, 3000.50, *got.RealizedValue, 1e-6)
	assert.InDelta(t, 1000.0, *got.EstimatedValue, 1e-9, "estimated_value is never touched")
	require.NotNil(t, got.XProcessNumPedido)
	assert.Equal(t, "4317", *got.XProcessNumPedido)

	var events []models.SalesOpportunityEvent
	require.NoError(t, e.app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventXProcessLinked).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.SalesOpportunityEventSourceXProcess, events[0].Source)
}

func TestDiscovery_SameDaySaleCounts(t *testing.T) {
	// data_venda is a bare date at midnight: a sale made the very day the
	// opportunity opened must not be discarded because 00:00 < the opening time.
	e := newDiscoveryEnv(t)
	opp := e.convertedOpp(t, discoveryCPF, 10*time.Minute, time.Minute)
	e.run(t, newFakeX2(t, order("5001", "SEPARACAO", today())))
	assert.Len(t, e.links(t, opp), 1)
}

func TestDiscovery_IsIdempotentAndNeverTouchesAnExistingLink(t *testing.T) {
	e := newDiscoveryEnv(t)
	opp := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	f := newFakeX2(t, order("5002", "FECHADO", today()))
	e.run(t, f)
	e.run(t, f)
	assert.Len(t, e.links(t, opp), 1, "a second run creates nothing")

	// An opportunity that already has a link (agent's, even an unresolved one) is left alone.
	other := e.convertedOpp(t, "11144477735", 24*time.Hour, time.Hour)
	require.NoError(t, e.app.DB.Create(&models.SalesOpportunityXProcessLink{
		OrganizationID: e.org.ID, SalesOpportunityID: other.ID, NumPedido: "777", Documento: "11144477735",
	}).Error)
	f2 := newFakeX2(t, order("5003", "FECHADO", today()))
	e.run(t, f2)
	links := e.links(t, other)
	require.Len(t, links, 1)
	assert.Equal(t, "777", links[0].NumPedido)
	assert.Equal(t, "agent", links[0].LinkSource)
}

// --- the safety rules: every one of these must end with NO link ---

func TestDiscovery_DoesNotLink(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2)
	}{
		{"sale before the opening date", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			return e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour), newFakeX2(t, order("6001", "FECHADO", daysAgo(5)))
		}},
		{"cancelled order", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			return e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour), newFakeX2(t, order("6002", "CANCELADO", today()))
		}},
		{"two candidate orders", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			return e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour),
				newFakeX2(t, order("6003", "FECHADO", today()), order("6004", "SEPARACAO", today()))
		}},
		{"converted more than 7 days ago", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			return e.convertedOpp(t, discoveryCPF, 20*24*time.Hour, 8*24*time.Hour), newFakeX2(t, order("6005", "FECHADO", today()))
		}},
		{"customer not in X2 yet", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			f := newFakeX2(t, order("6006", "FECHADO", today()))
			f.hasCli = false
			return e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour), f
		}},
		{"list that may be truncated (exactly 1000 lines)", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			f := newFakeX2(t, order("6007", "FECHADO", today()))
			f.extra = 999
			return e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour), f
		}},
		{"seller of the order differs from the owner's X2 code", func(t *testing.T, e discoveryEnv) (*models.SalesOpportunity, *fakeX2) {
			code := "111"
			require.NoError(t, e.app.DB.Model(e.agent).Update("xprocess_seller_code", code).Error)
			return e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour), newFakeX2(t, order("6008", "FECHADO", today()))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newDiscoveryEnv(t)
			opp, f := c.setup(t, e)
			e.run(t, f)
			assert.Empty(t, e.links(t, opp))
			assert.Nil(t, e.reload(t, opp).RealizedValue)
		})
	}
}

func TestDiscovery_SellerOnlyVetoes(t *testing.T) {
	// Owner without an X2 code: the seller is not a criterion -> links.
	e := newDiscoveryEnv(t)
	opp := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	e.run(t, newFakeX2(t, order("6101", "FECHADO", today())))
	assert.Len(t, e.links(t, opp), 1)

	// Same code on both sides -> links.
	e2 := newDiscoveryEnv(t)
	require.NoError(t, e2.app.DB.Model(e2.agent).Update("xprocess_seller_code", "630877").Error)
	opp2 := e2.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	e2.run(t, newFakeX2(t, order("6102", "FECHADO", today())))
	assert.Len(t, e2.links(t, opp2), 1)
}

func TestDiscovery_OpenOpportunityIsNeverLinkedOrConverted(t *testing.T) {
	e := newDiscoveryEnv(t)
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact).Update("cpfcnpj", discoveryCPF).Error)
	open := newOpenOpportunity(t, e.app, e.org.ID, e.agent.ID, contact.ID)
	e.run(t, newFakeX2(t, order("6201", "FECHADO", today())))
	assert.Empty(t, e.links(t, open))
	assert.Equal(t, models.SalesOpportunityStatusAberta, e.reload(t, open).Status)
}

func TestDiscovery_AnotherOpportunityOfTheSameDocumentMakesItAmbiguous(t *testing.T) {
	// Phase 2 allows the same CPF on several contacts. A converted opportunity and
	// an open one (different contacts, same documento) both fit the order: two
	// eligible opportunities -> nothing is linked, whichever is newer.
	e := newDiscoveryEnv(t)
	converted := e.convertedOpp(t, discoveryCPF, 48*time.Hour, time.Hour)
	contact2 := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact2).Update("cpfcnpj", discoveryCPF).Error)
	open := newOpenOpportunity(t, e.app, e.org.ID, e.agent.ID, contact2.ID)
	require.NoError(t, e.app.DB.Model(open).Update("opened_at", time.Now().Add(-24*time.Hour)).Error)

	e.run(t, newFakeX2(t, order("6301", "FECHADO", today())))
	assert.Empty(t, e.links(t, converted))
	assert.Empty(t, e.links(t, open))
}

func TestDiscovery_DateEliminatesTheOtherOpportunity(t *testing.T) {
	// The second opportunity opened AFTER the sale cannot own it, so only the
	// first one is eligible -> linked.
	e := newDiscoveryEnv(t)
	first := e.convertedOpp(t, discoveryCPF, 5*24*time.Hour, time.Hour)
	later := e.convertedOpp(t, discoveryCPF, time.Hour, time.Minute) // opened today
	require.NoError(t, e.app.DB.Model(later).Update("opened_at", time.Now().AddDate(0, 0, 2)).Error)

	e.run(t, newFakeX2(t, order("6302", "FECHADO", daysAgo(1))))
	assert.Len(t, e.links(t, first), 1)
	assert.Empty(t, e.links(t, later))
}

func TestDiscovery_OrderAlreadyTrackedByAnotherLinkIsNeverReused(t *testing.T) {
	e := newDiscoveryEnv(t)
	owner := e.convertedOpp(t, "11144477735", 24*time.Hour, time.Hour)
	cod := "40"
	require.NoError(t, e.app.DB.Create(&models.SalesOpportunityXProcessLink{
		OrganizationID: e.org.ID, SalesOpportunityID: owner.ID, NumPedido: "6401", Documento: "11144477735", CodEmpresa: &cod,
	}).Error)
	// Another opportunity (other documento) sees the same order number/company.
	opp := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	e.run(t, newFakeX2(t, order("6401", "FECHADO", today())))
	assert.Empty(t, e.links(t, opp))
}

func TestDiscovery_NoDocumentMeansNoCallToX2(t *testing.T) {
	e := newDiscoveryEnv(t)
	opp := e.convertedOpp(t, "", 24*time.Hour, time.Hour)
	f := newFakeX2(t, order("6501", "FECHADO", today()))
	e.run(t, f)
	assert.Empty(t, e.links(t, opp))
	assert.Equal(t, int64(0), atomic.LoadInt64(&f.requests))
}

func TestDiscovery_OtherOrganizationsOpportunitiesAreNeverTouched(t *testing.T) {
	e := newDiscoveryEnv(t)
	other := newDiscoveryEnv(t)
	mine := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	theirs := other.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	e.run(t, newFakeX2(t, order("6601", "FECHADO", today())))
	assert.Len(t, e.links(t, mine), 1)
	assert.Empty(t, other.links(t, theirs), "same documento in another organization is not a competitor nor a target")
}

// --- date rule ---

func TestSaleOnOrAfterOpening_ComparesDatesInTheAppTimezone(t *testing.T) {
	// 02:00 UTC on 02/10 is 23:00 on 01/10 in America/Bahia: the opening DATE is 01/10.
	opened := time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)
	midnight := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	assert.True(t, handlers.SaleOnOrAfterOpeningForTest(midnight(2026, 10, 1), opened), "same local day counts")
	assert.True(t, handlers.SaleOnOrAfterOpeningForTest(midnight(2026, 10, 2), opened))
	assert.False(t, handlers.SaleOnOrAfterOpeningForTest(midnight(2026, 9, 30), opened))
}

// --- reconciliation guard and freight ---

func TestReconcile_AnOrderAlreadyHeldByAnotherOpportunityIsNotProcessedTwice(t *testing.T) {
	e := newDiscoveryEnv(t)
	first := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	cod := "40"
	require.NoError(t, e.app.DB.Create(&models.SalesOpportunityXProcessLink{
		OrganizationID: e.org.ID, SalesOpportunityID: first.ID, NumPedido: "6701", Documento: discoveryCPF, CodEmpresa: &cod,
	}).Error)

	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	second := newOpenOpportunity(t, e.app, e.org.ID, e.agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: e.org.ID, SalesOpportunityID: second.ID, NumPedido: "6701", Documento: "11144477735"}
	require.NoError(t, e.app.DB.Create(&link).Error)

	f := newFakeX2(t, order("6701", "SEPARACAO", today()))
	e.app.ReconcileXProcessLinkForTest(xprocess.New(e.app.Log, f.srv.URL), "key", &link)

	assert.Equal(t, models.SalesOpportunityStatusAberta, e.reload(t, second).Status, "no conversion for the second opportunity")
	var got models.SalesOpportunityXProcessLink
	require.NoError(t, e.app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Nil(t, got.StatusXProcess)
	assert.Nil(t, got.CodEmpresa)
}

// --- manual linking of a converted opportunity, and the candidates list ---

func (e discoveryEnv) putLink(t *testing.T, opp *models.SalesOpportunity, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, e.agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.UpsertSalesOpportunityXProcessLink(req))
	return testutil.GetResponseStatusCode(req)
}

func TestUpsertLink_AcceptsAConvertedOpportunityButNotALostOne(t *testing.T) {
	e := newDiscoveryEnv(t)
	converted := e.convertedOpp(t, "", 24*time.Hour, time.Hour)
	assert.Equal(t, fasthttp.StatusOK, e.putLink(t, converted, map[string]any{"num_pedido": "7001", "documento": "529.982.247-25"}))
	require.Len(t, e.links(t, converted), 1)
	assert.Equal(t, "agent", e.links(t, converted)[0].LinkSource)

	lost := e.convertedOpp(t, "", 24*time.Hour, time.Hour)
	require.NoError(t, e.app.DB.Model(lost).Update("status", models.SalesOpportunityStatusPerdida).Error)
	assert.Equal(t, fasthttp.StatusBadRequest, e.putLink(t, lost, map[string]any{"num_pedido": "7002", "documento": discoveryCPF}))
}

func TestUpsertLink_RefusesAnOrderAlreadyLinkedToAnotherOpportunity(t *testing.T) {
	e := newDiscoveryEnv(t)
	a := e.convertedOpp(t, "", 24*time.Hour, time.Hour)
	b := e.convertedOpp(t, "", 24*time.Hour, time.Hour)
	require.Equal(t, fasthttp.StatusOK, e.putLink(t, a, map[string]any{"num_pedido": "7101", "documento": discoveryCPF}))
	assert.Equal(t, fasthttp.StatusConflict, e.putLink(t, b, map[string]any{"num_pedido": "7101", "documento": "529.982.247-25"}))
	assert.Empty(t, e.links(t, b))
	// Editing its own link is still fine.
	assert.Equal(t, fasthttp.StatusOK, e.putLink(t, a, map[string]any{"num_pedido": "7101", "documento": discoveryCPF}))
}

func TestCandidates_ListedForAConvertedOpportunityIncludingASameDaySale(t *testing.T) {
	e := newDiscoveryEnv(t)
	opp := e.convertedOpp(t, discoveryCPF, 10*time.Minute, time.Minute)
	// The candidates endpoint reads the organization's X2 credential.
	f := newFakeX2(t, order("7201", "FECHADO", today()))
	require.NoError(t, e.app.DB.Create(&models.XProcessIntegration{OrganizationID: e.org.ID, BaseURL: f.srv.URL, APIKey: "key", IsActive: true}).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.ListSalesOpportunityXProcessCandidates(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data struct {
			Candidates []struct {
				NumPedido string `json:"num_pedido"`
			} `json:"candidates"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data.Candidates, 1)
	assert.Equal(t, "7201", resp.Data.Candidates[0].NumPedido)
}


func TestDiscovery_AnOldConvertedUnlinkedOpportunityDoesNotBlockTheMatch(t *testing.T) {
	// A returning customer: an old sale that was never linked (converted 30 days
	// ago, outside the 7-day window) must not make today's match ambiguous.
	e := newDiscoveryEnv(t)
	old := e.convertedOpp(t, discoveryCPF, 40*24*time.Hour, 30*24*time.Hour)
	fresh := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	e.run(t, newFakeX2(t, order("6801", "FECHADO", today())))
	assert.Len(t, e.links(t, fresh), 1)
	assert.Empty(t, e.links(t, old), "the old one is outside the window and is never touched")
}

// --- duplicates that already exist in the data, and concurrency ---

// Two open links for the same order, as an older version allowed. The OLDEST owns
// it and keeps being reconciled; only the newer one is held back. (A guard where
// each side saw "the other" would freeze both forever, cancellations included.)
func TestReconcile_PreExistingDuplicates_OlderLinkKeepsWorkingNewerIsHeld(t *testing.T) {
	e := newDiscoveryEnv(t)
	require.NoError(t, e.app.DB.Exec(`DROP INDEX IF EXISTS idx_sales_opp_xlink_open_order`).Error)
	cod := "40"
	older := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	olderLink := models.SalesOpportunityXProcessLink{OrganizationID: e.org.ID, SalesOpportunityID: older.ID, NumPedido: "9001", Documento: discoveryCPF, CodEmpresa: &cod}
	require.NoError(t, e.app.DB.Create(&olderLink).Error)
	time.Sleep(15 * time.Millisecond)

	newer := e.convertedOpp(t, "11144477735", 24*time.Hour, time.Hour)
	newerLink := models.SalesOpportunityXProcessLink{OrganizationID: e.org.ID, SalesOpportunityID: newer.ID, NumPedido: "9001", Documento: "11144477735", CodEmpresa: &cod}
	require.NoError(t, e.app.DB.Create(&newerLink).Error)

	f := newFakeX2(t, order("9001", "FECHADO", today()))
	client := xprocess.New(e.app.Log, f.srv.URL)

	e.app.ReconcileXProcessLinkForTest(client, "key", &newerLink)
	var n models.SalesOpportunityXProcessLink
	require.NoError(t, e.app.DB.First(&n, "id = ?", newerLink.ID).Error)
	assert.Nil(t, n.StatusXProcess, "the newer duplicate is held back")
	assert.Nil(t, e.reload(t, newer).RealizedValue)

	e.app.ReconcileXProcessLinkForTest(client, "key", &olderLink)
	var o models.SalesOpportunityXProcessLink
	require.NoError(t, e.app.DB.First(&o, "id = ?", olderLink.ID).Error)
	require.NotNil(t, o.StatusXProcess, "the older link is not frozen by the duplicate")
	assert.Equal(t, "FECHADO", *o.StatusXProcess)
	require.NotNil(t, e.reload(t, older).RealizedValue)
}

func TestReconcile_AnOlderAgentLinkNotYetCheckedStillOwnsTheOrder(t *testing.T) {
	// Older link typed by an agent (no cod_empresa yet, same documento) vs a newer one.
	e := newDiscoveryEnv(t)
	require.NoError(t, e.app.DB.Exec(`DROP INDEX IF EXISTS idx_sales_opp_xlink_open_order`).Error)
	older := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	olderLink := models.SalesOpportunityXProcessLink{OrganizationID: e.org.ID, SalesOpportunityID: older.ID, NumPedido: "9002", Documento: discoveryCPF}
	require.NoError(t, e.app.DB.Create(&olderLink).Error)
	time.Sleep(15 * time.Millisecond)
	newer := e.convertedOpp(t, "", 24*time.Hour, time.Hour)
	newerLink := models.SalesOpportunityXProcessLink{OrganizationID: e.org.ID, SalesOpportunityID: newer.ID, NumPedido: "9002", Documento: discoveryCPF}
	require.NoError(t, e.app.DB.Create(&newerLink).Error)

	f := newFakeX2(t, order("9002", "SEPARACAO", today()))
	client := xprocess.New(e.app.Log, f.srv.URL)
	e.app.ReconcileXProcessLinkForTest(client, "key", &newerLink)
	var n models.SalesOpportunityXProcessLink
	require.NoError(t, e.app.DB.First(&n, "id = ?", newerLink.ID).Error)
	assert.Nil(t, n.StatusXProcess)

	e.app.ReconcileXProcessLinkForTest(client, "key", &olderLink)
	var o models.SalesOpportunityXProcessLink
	require.NoError(t, e.app.DB.First(&o, "id = ?", olderLink.ID).Error)
	assert.NotNil(t, o.StatusXProcess)
}

// With the unique index in place, two opportunities racing for the same order end
// with exactly ONE link; the loser changes nothing (its transaction rolls back).
func TestDiscovery_UniqueIndexMakesTheRaceForAnOrderHaveOneWinner(t *testing.T) {
	e := newDiscoveryEnv(t)
	// The shared test database keeps rows from other tests (some deliberately duplicated):
	// soft-delete the duplicate open links so the unique index can be built.
	require.NoError(t, e.app.DB.Exec(`UPDATE sales_opportunity_xprocess_links SET deleted_at = now()
		WHERE deleted_at IS NULL AND resolved_at IS NULL AND cod_empresa IS NOT NULL
		AND (organization_id, cod_empresa, num_pedido) IN (
			SELECT organization_id, cod_empresa, num_pedido FROM sales_opportunity_xprocess_links
			WHERE deleted_at IS NULL AND resolved_at IS NULL AND cod_empresa IS NOT NULL
			GROUP BY 1, 2, 3 HAVING count(*) > 1)`).Error)
	require.NoError(t, e.app.DB.Exec(`DROP INDEX IF EXISTS idx_sales_opp_xlink_open_order`).Error)
	require.NoError(t, e.app.DB.Exec(`CREATE UNIQUE INDEX idx_sales_opp_xlink_open_order
		ON sales_opportunity_xprocess_links (organization_id, cod_empresa, num_pedido)
		WHERE cod_empresa IS NOT NULL AND resolved_at IS NULL AND deleted_at IS NULL`).Error)
	t.Cleanup(func() { e.app.DB.Exec(`DROP INDEX IF EXISTS idx_sales_opp_xlink_open_order`) })

	a := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	b := e.convertedOpp(t, "11144477735", 24*time.Hour, time.Hour)

	start := make(chan struct{})
	results := make(chan bool, 2)
	for _, opp := range []*models.SalesOpportunity{a, b} {
		go func(id uuid.UUID) {
			<-start
			results <- e.app.CreateDiscoveredXProcessLinkForTest(e.org.ID, id, "40", "9100", discoveryCPF)
		}(opp.ID)
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		if <-results {
			wins++
		}
	}
	assert.Equal(t, 1, wins, "exactly one opportunity gets the order")

	var links int64
	e.app.DB.Model(&models.SalesOpportunityXProcessLink{}).Where("organization_id = ? AND num_pedido = ?", e.org.ID, "9100").Count(&links)
	assert.Equal(t, int64(1), links)

	// The loser is untouched: no link, no pointer, no history event.
	for _, opp := range []*models.SalesOpportunity{a, b} {
		hasLink := len(e.links(t, opp)) == 1
		var evs int64
		e.app.DB.Model(&models.SalesOpportunityEvent{}).Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventXProcessLinked).Count(&evs)
		got := e.reload(t, opp)
		if hasLink {
			assert.Equal(t, int64(1), evs)
			require.NotNil(t, got.XProcessNumPedido)
		} else {
			assert.Equal(t, int64(0), evs)
			assert.Nil(t, got.XProcessNumPedido, "the loser's pointer was rolled back with its transaction")
		}
	}
}

// Without the index (it is skipped when duplicates already exist) the application
// guards still stop discovery from reusing a tracked order.
func TestDiscovery_WithoutTheIndexTheApplicationGuardsStillHold(t *testing.T) {
	e := newDiscoveryEnv(t)
	require.NoError(t, e.app.DB.Exec(`DROP INDEX IF EXISTS idx_sales_opp_xlink_open_order`).Error)
	cod := "40"
	owner := e.convertedOpp(t, "11144477735", 24*time.Hour, time.Hour)
	for i := 0; i < 2; i++ { // two pre-existing open links for the same order (the duplicate case)
		require.NoError(t, e.app.DB.Create(&models.SalesOpportunityXProcessLink{
			OrganizationID: e.org.ID, SalesOpportunityID: owner.ID, NumPedido: "9200", Documento: "11144477735", CodEmpresa: &cod,
		}).Error)
	}
	opp := e.convertedOpp(t, discoveryCPF, 24*time.Hour, time.Hour)
	e.run(t, newFakeX2(t, order("9200", "FECHADO", today())))
	assert.Empty(t, e.links(t, opp), "an already tracked order is never reused")
}
