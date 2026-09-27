package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/xprocess"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeXProcessServer returns a canned /api/pedido response for every request.
func fakeXProcessServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pedidoJSON(status string) string {
	return fmt.Sprintf(`{"ok":true,"pedido":[
		{"cod_empresa":"7","num_pedido":"666","cod_cliente":"9446","cod_vendedor":"2586","status":%q,"total":"100,0","data_venda":null}
	]}`, status)
}

func TestReconcileXProcessLink_NotFound_IncrementsCounter(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusNotFound, `{"detail":"Pedido nao encontrado"}`)
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Equal(t, 1, got.ConsecutiveNotFound)
	assert.Nil(t, got.ResolvedAt)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusAberta, oppAfter.Status)
}

func TestReconcileXProcessLink_Separacao_ConvertsOpenOpportunity(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("SEPARACAO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, oppAfter.Status)
	require.NotNil(t, oppAfter.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceXProcess, *oppAfter.ConversionSource)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.SalesOpportunityEventConverted, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventSourceXProcess, events[0].Source)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Nil(t, got.ResolvedAt, "SEPARACAO must keep the job checking (design §5)")
	require.NotNil(t, got.ValorVendido)
	assert.InDelta(t, 100.0, *got.ValorVendido, 0.001)
}

func TestReconcileXProcessLink_Separacao_DoesNotReconvertAlreadyConverted(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceManual,
	}).Error)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("SEPARACAO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	assert.Empty(t, events, "an already-converted opportunity must not get a duplicate converted event")

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	require.NotNil(t, oppAfter.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceManual, *oppAfter.ConversionSource, "manual conversion must not be overwritten")
}

func TestReconcileXProcessLink_Fechado_FirstTime_SetsFirstClosedAtButStaysUnresolved(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("FECHADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.FirstClosedAt)
	assert.Nil(t, got.ResolvedAt, "resolved_at must NOT be set on the same round that first sees FECHADO — "+
		"the job must keep checking through the 7-day grace window")

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, oppAfter.Status)
}

func TestReconcileXProcessLink_Fechado_WithinGracePeriod_StaysUnresolved(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceXProcess,
	}).Error)
	threeDaysAgo := time.Now().Add(-3 * 24 * time.Hour)
	link := models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900",
		FirstClosedAt: &threeDaysAgo,
	}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("FECHADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	assert.Nil(t, got.ResolvedAt, "3 days after FECHADO is still inside the 7-day grace window")
	assert.WithinDuration(t, threeDaysAgo, *got.FirstClosedAt, time.Second, "first_closed_at must never be rewritten on a later round")
}

func TestReconcileXProcessLink_Fechado_AfterGracePeriod_Resolves(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceXProcess,
	}).Error)
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	link := models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900",
		FirstClosedAt: &eightDaysAgo,
	}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("FECHADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.ResolvedAt, "8 days after first_closed_at is past the 7-day grace window")
}

func TestReconcileXProcessLink_Cancelado_NeverConverted_MarksLost(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("CANCELADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusPerdida, oppAfter.Status)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.ResolvedAt)
}

func TestReconcileXProcessLink_Cancelado_AlreadyConverted_MarksCancelledPreservingHistory(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"status": models.SalesOpportunityStatusConvertida, "conversion_source": models.SalesConversionSourceXProcess,
	}).Error)
	require.NoError(t, app.DB.Create(&models.SalesOpportunityEvent{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID,
		Type: models.SalesOpportunityEventConverted, Source: models.SalesOpportunityEventSourceXProcess,
	}).Error)
	twoDaysAgo := time.Now().Add(-2 * 24 * time.Hour)
	link := models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "666", Documento: "12345678900",
		FirstClosedAt: &twoDaysAgo, // still well inside the 7-day grace window
	}
	require.NoError(t, app.DB.Create(&link).Error)

	srv := fakeXProcessServer(t, http.StatusOK, pedidoJSON("CANCELADO"))
	client := xprocess.New(app.Log, srv.URL)
	app.ReconcileXProcessLinkForTest(client, "key", &link)

	var oppAfter models.SalesOpportunity
	require.NoError(t, app.DB.First(&oppAfter, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusCancelada, oppAfter.Status)
	require.NotNil(t, oppAfter.CancelledAt)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at asc").Find(&events).Error)
	require.Len(t, events, 2, "the original converted event must be preserved, a cancelled event added")
	assert.Equal(t, models.SalesOpportunityEventConverted, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventCancelled, events[1].Type)

	var got models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.First(&got, "id = ?", link.ID).Error)
	require.NotNil(t, got.ResolvedAt, "CANCELADO within the grace window must resolve immediately, not wait for 7 days")
}

func TestRunXProcessReconciliation_ProcessesOnlyPendingLinksForActiveIntegrations(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	openOpp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	openLink := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: openOpp.ID, NumPedido: "1", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&openLink).Error)

	// Separate contact: idx_sales_opp_org_contact_open is a partial unique
	// index on (organization_id, contact_id) WHERE status='aberta', so two
	// aberta opportunities can't share a contact (see widgets_sales_test.go).
	resolvedContact := testutil.CreateTestContact(t, app.DB, org.ID)
	resolvedOpp := newOpenOpportunity(t, app, org.ID, agent.ID, resolvedContact.ID)
	resolvedAt := time.Now()
	resolvedLink := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: resolvedOpp.ID, NumPedido: "2", Documento: "12345678900", ResolvedAt: &resolvedAt}
	require.NoError(t, app.DB.Create(&resolvedLink).Error)

	var requestedNums []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NumeroPedido string `json:"numero_pedido"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		requestedNums = append(requestedNums, body.NumeroPedido)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pedidoJSON("SEPARACAO")))
	}))
	defer srv.Close()

	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: srv.URL, APIKey: "the-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	app.RunXProcessReconciliation()

	assert.Equal(t, []string{"1"}, requestedNums, "only the unresolved link must be checked")
}

func TestRunXProcessReconciliation_SkipsInactiveIntegration(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	link := models.SalesOpportunityXProcessLink{OrganizationID: org.ID, SalesOpportunityID: opp.ID, NumPedido: "1", Documento: "12345678900"}
	require.NoError(t, app.DB.Create(&link).Error)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	// IsActive is created true then flipped to false via Update: the model's
	// `gorm:"default:true"` tag makes GORM omit an explicit false (its Go zero
	// value) from the INSERT, so Create alone would silently leave it active
	// (same footgun documented in goroutines_test.go for models.Webhook).
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: srv.URL, APIKey: "the-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)
	require.NoError(t, app.DB.Model(&integ).Update("is_active", false).Error)

	app.RunXProcessReconciliation()
	assert.False(t, called, "an inactive integration must never be queried")
}

func TestXProcessReconciler_RunsOnceAtTriggerHourNotBefore(t *testing.T) {
	app := newTestApp(t)
	var runs int
	reconciler := handlers.NewXProcessReconcilerForTest(app, func() { runs++ })

	// maybeRun compares against the deployment's real local time
	// (America/Bahia by default, see helpers.go's appLocation), not the
	// server's bare wall clock (the deployment runs with TZ=UTC). These
	// timestamps are expressed directly in that location so the test
	// asserts real-world trigger-hour behavior, not UTC clock time.
	bahia, err := time.LoadLocation("America/Bahia")
	require.NoError(t, err)

	reconciler.MaybeRunForTest(time.Date(2026, 9, 27, 1, 59, 0, 0, bahia))
	assert.Equal(t, 0, runs, "must not run before the trigger hour")

	reconciler.MaybeRunForTest(time.Date(2026, 9, 27, 2, 5, 0, 0, bahia))
	assert.Equal(t, 1, runs)

	reconciler.MaybeRunForTest(time.Date(2026, 9, 27, 3, 0, 0, 0, bahia))
	assert.Equal(t, 1, runs, "must not run twice on the same calendar day")

	reconciler.MaybeRunForTest(time.Date(2026, 9, 28, 2, 5, 0, 0, bahia))
	assert.Equal(t, 2, runs, "must run again the next day")
}
