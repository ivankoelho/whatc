package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// xprocessCandidatesTestServer mocks GET /api/clientes and GET /api/vendas
// exactly as confirmed live against the real X2 API on 2026-09-27: a
// documento query resolves to a cod_cliente, which /api/vendas then filters
// by (one row per pedido ITEM, not per pedido).
func xprocessCandidatesTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/clientes":
			assert.Equal(t, "07378783000190", r.URL.Query().Get("documento"))
			_, _ = w.Write([]byte(`{"ok":true,"total":1,"clientes":[{"cod_cliente":"3"}]}`))
		case "/api/vendas":
			assert.Equal(t, "3", r.URL.Query().Get("cod_cliente"))
			_, _ = w.Write([]byte(`{"ok":true,"total":3,"vendas":[
				{"cod_empresa":"23","num_pedido":"20101","cod_cliente":"3","status":"FECHADO","data_venda":"2026-09-20T00:00:00","total":"100,0"},
				{"cod_empresa":"23","num_pedido":"20101","cod_cliente":"3","status":"FECHADO","data_venda":"2026-09-20T00:00:00","total":"50,0"},
				{"cod_empresa":"23","num_pedido":"19000","cod_cliente":"3","status":"FECHADO","data_venda":"2026-09-01T00:00:00","total":"200,0"}
			]}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
	}))
}

func newContactWithDocumento(t *testing.T, orgID uuid.UUID, cpfCnpj string) *models.Contact {
	t.Helper()
	uniqueID := uuid.New().String()[:8]
	contact := &models.Contact{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: orgID,
		PhoneNumber:    "+1234567890" + uniqueID[:4],
		ProfileName:    "Test Contact " + uniqueID,
		CPFCNPJ:        cpfCnpj,
	}
	return contact
}

func TestListSalesOpportunityXProcessCandidates_FindsPurchaseAfterOpenedAt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := newContactWithDocumento(t, org.ID, "07378783000190")
	require.NoError(t, app.DB.Create(contact).Error)

	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	// opened_at is set by BaseModel's autoCreateTime at insert; force it
	// well before both mocked pedidos so 20101 (Sep 20) qualifies and 19000
	// (Sep 1) does not.
	openedAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	require.NoError(t, app.DB.Model(opp).Update("opened_at", openedAt).Error)

	srv := xprocessCandidatesTestServer(t)
	defer srv.Close()
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: srv.URL, APIKey: "the-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ListSalesOpportunityXProcessCandidates(req))

	var resp struct {
		Candidates []struct {
			NumPedido    string  `json:"num_pedido"`
			ValorVendido float64 `json:"valor_vendido"`
		} `json:"candidates"`
	}
	testutil.ParseEnvelopeResponse(t, req, &resp)
	require.Len(t, resp.Candidates, 1, "only the pedido dated after opened_at should be a candidate")
	assert.Equal(t, "20101", resp.Candidates[0].NumPedido)
	assert.InDelta(t, 150.0, resp.Candidates[0].ValorVendido, 0.0001)
}

func TestListSalesOpportunityXProcessCandidates_SkipsAlreadyTrackedPedido(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := newContactWithDocumento(t, org.ID, "07378783000190")
	require.NoError(t, app.DB.Create(contact).Error)

	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Update("opened_at", time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)).Error)

	// Some OTHER opportunity (a different contact — the unique-open-per-
	// contact index forbids a second one on the same contact) already
	// tracks pedido 20101 for this same documento — it must not be
	// re-suggested as a new candidate here.
	otherContact := newContactWithDocumento(t, org.ID, "07378783000190")
	require.NoError(t, app.DB.Create(otherContact).Error)
	otherOpp := newOpenOpportunity(t, app, org.ID, agent.ID, otherContact.ID)
	require.NoError(t, app.DB.Create(&models.SalesOpportunityXProcessLink{
		OrganizationID: org.ID, SalesOpportunityID: otherOpp.ID,
		NumPedido: "20101", Documento: "07378783000190",
	}).Error)

	srv := xprocessCandidatesTestServer(t)
	defer srv.Close()
	integ := models.XProcessIntegration{OrganizationID: org.ID, BaseURL: srv.URL, APIKey: "the-key", IsActive: true}
	require.NoError(t, integ.EncryptSecrets(app.Config.App.EncryptionKey))
	require.NoError(t, app.DB.Create(&integ).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ListSalesOpportunityXProcessCandidates(req))

	var resp struct {
		Candidates []struct {
			NumPedido string `json:"num_pedido"`
		} `json:"candidates"`
	}
	testutil.ParseEnvelopeResponse(t, req, &resp)
	assert.Empty(t, resp.Candidates, "an already-tracked pedido must not be suggested again")
}

func TestListSalesOpportunityXProcessCandidates_NoDocumentoReturnsEmpty(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID) // no CPFCNPJ set
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ListSalesOpportunityXProcessCandidates(req))

	var resp struct {
		Candidates []struct {
			NumPedido string `json:"num_pedido"`
		} `json:"candidates"`
	}
	testutil.ParseEnvelopeResponse(t, req, &resp)
	assert.Empty(t, resp.Candidates, "without a documento there is nothing to search X2 for")
}
