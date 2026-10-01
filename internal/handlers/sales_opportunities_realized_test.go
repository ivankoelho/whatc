package handlers_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type salesEnv struct {
	app     *handlers.App
	org     *models.Organization
	agent   *models.User
	contact *models.Contact
}

func newSalesEnv(t *testing.T) salesEnv {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	return salesEnv{app, org, agent, contact}
}

func (e salesEnv) openOpp(t *testing.T) *models.SalesOpportunity {
	t.Helper()
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	return newOpenOpportunity(t, e.app, e.org.ID, e.agent.ID, contact.ID)
}

func (e salesEnv) details(t *testing.T, opp *models.SalesOpportunity, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, e.agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.UpdateSalesOpportunityDetails(req))
	return testutil.GetResponseStatusCode(req)
}

func (e salesEnv) convert(t *testing.T, opp *models.SalesOpportunity, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, e.agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.ConvertSalesOpportunity(req))
	return testutil.GetResponseStatusCode(req)
}

func (e salesEnv) reload(t *testing.T, opp *models.SalesOpportunity) models.SalesOpportunity {
	t.Helper()
	var got models.SalesOpportunity
	require.NoError(t, e.app.DB.First(&got, "id = ?", opp.ID).Error)
	return got
}

// --- decimal quantities ---

func TestOpportunityQuantity_AcceptsDecimalsAndKeepsThemExact(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)

	require.Equal(t, fasthttp.StatusOK, e.details(t, opp, map[string]any{"estimated_quantity": 12.345, "unit_of_measure": "M2"}))
	got := e.reload(t, opp)
	require.NotNil(t, got.EstimatedQuantity)
	assert.InDelta(t, 12.345, *got.EstimatedQuantity, 1e-9)
	require.NotNil(t, got.UnitOfMeasure)
	assert.Equal(t, models.SalesUnitMetro2, *got.UnitOfMeasure)

	require.Equal(t, fasthttp.StatusOK, e.details(t, opp, map[string]any{"estimated_quantity": 50}))
	got = e.reload(t, opp)
	assert.InDelta(t, 50.0, *got.EstimatedQuantity, 1e-9)
}

func TestOpportunityQuantity_RejectsNegativeTooPreciseAndTooLarge(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)

	for name, q := range map[string]float64{"negative": -1, "four decimals": 1.2345, "too large": 1e12} {
		assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"estimated_quantity": q}), name)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"estimated_value": -5}), "negative value")
	assert.Nil(t, e.reload(t, opp).EstimatedQuantity, "nothing was written")
}

func TestOpportunityQuantity_ExistingIntegerValueSurvivesTheColumnChange(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	// What the old bigint column held for an existing opportunity.
	require.NoError(t, e.app.DB.Model(opp).Update("estimated_quantity", 50).Error)
	got := e.reload(t, opp)
	require.NotNil(t, got.EstimatedQuantity)
	assert.InDelta(t, 50.0, *got.EstimatedQuantity, 1e-9)
	assert.Nil(t, got.UnitOfMeasure, "an opportunity from before units existed has none")
	assert.Nil(t, got.RealizedValue)
	assert.Nil(t, got.RealizedQuantity)
}

// --- unit of measure ---

func TestOpportunityUnit_ClosedList(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)

	for _, u := range models.SalesUnitsOfMeasure {
		require.Equal(t, fasthttp.StatusOK, e.details(t, opp, map[string]any{"unit_of_measure": string(u)}), string(u))
		assert.Equal(t, u, *e.reload(t, opp).UnitOfMeasure)
	}
	assert.Equal(t, []models.SalesUnitOfMeasure{"UN", "M", "M2", "KG", "PCT", "CX"}, models.SalesUnitsOfMeasure)

	for _, bad := range []string{"LITRO", "m", "un", "KG "} {
		assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"unit_of_measure": bad}), bad)
	}
	assert.Equal(t, models.SalesUnitCaixa, *e.reload(t, opp).UnitOfMeasure, "a rejected unit changes nothing")

	// An empty string clears it back to NULL.
	require.Equal(t, fasthttp.StatusOK, e.details(t, opp, map[string]any{"unit_of_measure": ""}))
	assert.Nil(t, e.reload(t, opp).UnitOfMeasure)
}

func TestOpportunityUnit_DatabaseConstraintMatchesTheList(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	// The test database is built by AutoMigrate only, so apply the same statement
	// the real migration runs and check it rejects what the API rejects.
	require.NoError(t, e.app.DB.Exec(`ALTER TABLE sales_opportunities DROP CONSTRAINT IF EXISTS chk_sales_opp_unit_of_measure`).Error)
	require.NoError(t, e.app.DB.Exec(`ALTER TABLE sales_opportunities ADD CONSTRAINT chk_sales_opp_unit_of_measure CHECK (unit_of_measure IS NULL OR unit_of_measure IN ('UN','M','M2','KG','PCT','CX'))`).Error)
	t.Cleanup(func() {
		e.app.DB.Exec(`ALTER TABLE sales_opportunities DROP CONSTRAINT IF EXISTS chk_sales_opp_unit_of_measure`)
	})
	assert.Error(t, e.app.DB.Model(opp).Update("unit_of_measure", "LITRO").Error)
	assert.NoError(t, e.app.DB.Model(opp).Update("unit_of_measure", "KG").Error)
}

// --- creation ---

func TestCreateOpportunity_QuantityAndUnitButNoRealized(t *testing.T) {
	e := newSalesEnv(t)
	admin := testutil.CreateAdminRole(t, e.app.DB, e.org.ID)
	user := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&admin.ID))

	create := func(body map[string]any) int {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, e.org.ID, user.ID)
		require.NoError(t, e.app.CreateSalesOpportunity(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, create(map[string]any{"contact_id": e.contact.ID, "unit_of_measure": "LITRO"}))
	assert.Equal(t, fasthttp.StatusBadRequest, create(map[string]any{"contact_id": e.contact.ID, "estimated_quantity": 1.2345}))

	require.Equal(t, fasthttp.StatusOK, create(map[string]any{
		"contact_id": e.contact.ID, "interest": "Piso", "estimated_value": 1000, "estimated_quantity": 25.5,
		"unit_of_measure": "M2", "realized_value": 999, // realized is not a creation concept: ignored
	}))
	var got models.SalesOpportunity
	require.NoError(t, e.app.DB.Where("contact_id = ?", e.contact.ID).First(&got).Error)
	assert.InDelta(t, 25.5, *got.EstimatedQuantity, 1e-9)
	assert.Equal(t, models.SalesUnitMetro2, *got.UnitOfMeasure)
	assert.Nil(t, got.RealizedValue)
	assert.Nil(t, got.RealizedQuantity)
}

// --- manual realized ---

func TestConvert_WithoutRealizedLeavesThemNull(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	require.NoError(t, e.app.DB.Model(opp).Update("estimated_value", 500).Error)

	assert.Equal(t, fasthttp.StatusOK, e.convert(t, opp, nil))
	got := e.reload(t, opp)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, got.Status)
	assert.Nil(t, got.RealizedValue)
	assert.Nil(t, got.RealizedQuantity)
	assert.InDelta(t, 500.0, *got.EstimatedValue, 1e-9)
}

func TestConvert_WithManualRealizedDoesNotTouchEstimates(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	require.NoError(t, e.app.DB.Model(opp).Updates(map[string]any{"estimated_value": 500, "estimated_quantity": 10}).Error)

	require.Equal(t, fasthttp.StatusOK, e.convert(t, opp, map[string]any{"realized_value": 480.5, "realized_quantity": 9.75}))
	got := e.reload(t, opp)
	assert.InDelta(t, 480.5, *got.RealizedValue, 1e-9)
	assert.InDelta(t, 9.75, *got.RealizedQuantity, 1e-9)
	assert.InDelta(t, 500.0, *got.EstimatedValue, 1e-9, "estimated_value is the expectation, never overwritten")
	assert.InDelta(t, 10.0, *got.EstimatedQuantity, 1e-9)
}

func TestConvert_InvalidRealizedIsRejectedAndNothingConverts(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	assert.Equal(t, fasthttp.StatusBadRequest, e.convert(t, opp, map[string]any{"realized_quantity": 1.23456}))
	assert.Equal(t, fasthttp.StatusBadRequest, e.convert(t, opp, map[string]any{"realized_value": -1}))
	assert.Equal(t, models.SalesOpportunityStatusAberta, e.reload(t, opp).Status)
}

func TestDetails_RealizedOnlyOnConverted_EstimatesOnlyOnOpen(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)

	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"realized_value": 10}), "open: realized not allowed")
	require.Equal(t, fasthttp.StatusOK, e.convert(t, opp, nil))

	assert.Equal(t, fasthttp.StatusOK, e.details(t, opp, map[string]any{"realized_value": 123.45, "realized_quantity": 7.5}))
	got := e.reload(t, opp)
	assert.InDelta(t, 123.45, *got.RealizedValue, 1e-9)
	assert.InDelta(t, 7.5, *got.RealizedQuantity, 1e-9)

	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"estimated_value": 1}), "converted: estimates frozen")
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"realized_value": 1, "interest": "x"}), "mixed edit is refused")
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"unit_of_measure": "KG"}))
}

func TestDetails_LostOpportunityCannotBeEdited(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	require.NoError(t, e.app.DB.Model(opp).Update("status", models.SalesOpportunityStatusPerdida).Error)
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"realized_value": 1}))
	assert.Equal(t, fasthttp.StatusBadRequest, e.details(t, opp, map[string]any{"interest": "x"}))
}

// --- organization isolation and permissions ---

func TestOpportunityDetails_OrganizationIsolationAndPermissions(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)

	other := testutil.CreateTestOrganization(t, e.app.DB)
	otherRole := testutil.CreateTestRoleWithKeys(t, e.app.DB, other.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	outsider := testutil.CreateTestUser(t, e.app.DB, other.ID, testutil.WithRoleID(&otherRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{"unit_of_measure": "KG"})
	testutil.SetAuthContext(req, other.ID, outsider.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req), "another organization's opportunity does not exist for the caller")

	// Read-only user cannot write.
	roRole := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "ro", []string{"sales_opportunities:read"})
	ro := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&roRole.ID))
	req = testutil.NewJSONRequest(t, map[string]any{"unit_of_measure": "KG"})
	testutil.SetAuthContext(req, e.org.ID, ro.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))

	// A writer who is not the owner and lacks view_all is refused too.
	peerRole := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "peer", []string{"sales_opportunities:read", "sales_opportunities:write"})
	peer := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&peerRole.ID))
	req = testutil.NewJSONRequest(t, map[string]any{"unit_of_measure": "KG"})
	testutil.SetAuthContext(req, e.org.ID, peer.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.UpdateSalesOpportunityDetails(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))
	assert.Nil(t, e.reload(t, opp).UnitOfMeasure, "none of the refused calls wrote anything")
}

// --- X2 reconciliation ---

func pedidoWithTotal(status, total string) string {
	return fmt.Sprintf(`{"ok":true,"pedido":[
		{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","status":%q,"total":%q,"data_venda":null}
	]}`, status, total)
}

func (e salesEnv) reconcile(t *testing.T, opp *models.SalesOpportunity, status, total string) {
	t.Helper()
	var link models.SalesOpportunityXProcessLink
	if err := e.app.DB.Where("sales_opportunity_id = ?", opp.ID).First(&link).Error; err != nil {
		link = models.SalesOpportunityXProcessLink{OrganizationID: e.org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "52998224725"}
		require.NoError(t, e.app.DB.Create(&link).Error)
	}
	srv := fakeXProcessServer(t, http.StatusOK, pedidoWithTotal(status, total))
	e.app.ReconcileXProcessLinkForTest(xprocess.New(e.app.Log, srv.URL), "key", &link)
}

func TestReconcile_FillsRealizedValueAndNeverTouchesEstimates(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	require.NoError(t, e.app.DB.Model(opp).Updates(map[string]any{"estimated_value": 900, "estimated_quantity": 30, "unit_of_measure": "M"}).Error)

	e.reconcile(t, opp, "SEPARACAO", "1234,56")
	got := e.reload(t, opp)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, got.Status)
	require.NotNil(t, got.RealizedValue)
	assert.InDelta(t, 1234.56, *got.RealizedValue, 1e-6)
	assert.InDelta(t, 900.0, *got.EstimatedValue, 1e-9, "estimated_value is untouched")
	assert.InDelta(t, 30.0, *got.EstimatedQuantity, 1e-9)
	assert.Equal(t, models.SalesUnitMetro, *got.UnitOfMeasure)
	assert.Nil(t, got.RealizedQuantity, "X2 returns no quantity: manual only")

	// The value follows the order until it closes.
	e.reconcile(t, opp, "FECHADO", "1300,00")
	assert.InDelta(t, 1300.0, *e.reload(t, opp).RealizedValue, 1e-6)
}

func TestReconcile_X2ReplacesManualRealizedValue(t *testing.T) {
	e := newSalesEnv(t)
	opp := e.openOpp(t)
	require.Equal(t, fasthttp.StatusOK, e.convert(t, opp, map[string]any{"realized_value": 50, "realized_quantity": 4}))

	e.reconcile(t, opp, "FECHADO", "100,00")
	got := e.reload(t, opp)
	assert.InDelta(t, 100.0, *got.RealizedValue, 1e-6, "X2 is the official source of the realized value")
	assert.InDelta(t, 4.0, *got.RealizedQuantity, 1e-9, "the manual quantity is not X2's to overwrite")
}

func TestReconcile_DoesNotWriteForCancelledZeroOrNonConvertedOpportunities(t *testing.T) {
	e := newSalesEnv(t)

	// Zero total is not a valid realized value.
	opp := e.openOpp(t)
	e.reconcile(t, opp, "SEPARACAO", "0,00")
	assert.Nil(t, e.reload(t, opp).RealizedValue)

	// A cancelled order never records a realized value.
	opp2 := e.openOpp(t)
	e.reconcile(t, opp2, "CANCELADO", "500,00")
	got := e.reload(t, opp2)
	assert.Nil(t, got.RealizedValue)
	assert.Equal(t, models.SalesOpportunityStatusPerdida, got.Status)

	// A lost opportunity keeps no realized value even if its order shows up.
	opp3 := e.openOpp(t)
	require.NoError(t, e.app.DB.Model(opp3).Update("status", models.SalesOpportunityStatusPerdida).Error)
	e.reconcile(t, opp3, "FECHADO", "500,00")
	assert.Nil(t, e.reload(t, opp3).RealizedValue)
}

func TestReconcile_OtherOrganizationsOpportunityIsNotTouched(t *testing.T) {
	e := newSalesEnv(t)
	other := newSalesEnv(t)
	oppA := e.openOpp(t)
	oppB := other.openOpp(t)

	e.reconcile(t, oppA, "FECHADO", "77,00")
	assert.InDelta(t, 77.0, *e.reload(t, oppA).RealizedValue, 1e-6)
	assert.Nil(t, other.reload(t, oppB).RealizedValue)
}
