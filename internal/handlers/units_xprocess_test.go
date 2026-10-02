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
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// X2 stores in the shape of the real /api/lojas sample (CNPJ with punctuation,
// the store name in parentheses, sometimes a double space).
type loja struct{ Cod, Razao, CNPJ string }

func (l loja) json() map[string]any {
	return map[string]any{"cod_empresa": l.Cod, "razao_social_empresa": l.Razao, "cnpj_empresa": l.CNPJ, "data_referencia_dados": "2026-10-01T01:18:33"}
}

type lojasServer struct {
	srv      *httptest.Server
	requests int64
	status   int
	lojas    []loja
}

func newLojasServer(t *testing.T, lojas ...loja) *lojasServer {
	ls := &lojasServer{lojas: lojas, status: http.StatusOK}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&ls.requests, 1)
		if r.URL.Path != "/api/lojas" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ls.status != http.StatusOK {
			w.WriteHeader(ls.status)
			return
		}
		out := []map[string]any{}
		for _, l := range ls.lojas {
			out = append(out, l.json())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "total": len(out), "lojas": out})
	}))
	t.Cleanup(ls.srv.Close)
	return ls
}

var (
	lojaPorto    = loja{"40", "ATACADAO DOS PISOS LTDA  (PORTO)", "58.675.622/0003-61"}
	lojaSerrinha = loja{"41", "ATACADAO DOS PISOS LTDA  (SERRINHA)", "58.675.622/0005-23"}
)

type unitsX2Env struct {
	app   *handlers.App
	org   *models.Organization
	admin *models.User
	x2    *lojasServer
}

func newUnitsX2Env(t *testing.T, lojas ...loja) unitsX2Env {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateAdminRole(t, app.DB, org.ID)
	admin := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	x2 := newLojasServer(t, lojas...)
	require.NoError(t, app.DB.Create(&models.XProcessIntegration{OrganizationID: org.ID, BaseURL: x2.srv.URL, APIKey: "key", IsActive: true}).Error)
	return unitsX2Env{app, org, admin, x2}
}

func (e unitsX2Env) unit(t *testing.T, name string) models.Unit {
	t.Helper()
	u := models.Unit{OrganizationID: e.org.ID, Name: name, Active: true}
	require.NoError(t, e.app.DB.Create(&u).Error)
	return u
}

func (e unitsX2Env) reload(t *testing.T, u models.Unit) models.Unit {
	t.Helper()
	var got models.Unit
	require.NoError(t, e.app.DB.First(&got, "id = ?", u.ID).Error)
	return got
}

func (e unitsX2Env) set(t *testing.T, userID uuid.UUID, u models.Unit, body map[string]any) (int, map[string]any) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, userID)
	testutil.SetPathParam(req, "id", u.ID.String())
	require.NoError(t, e.app.SetUnitXProcessLoja(req))
	var resp struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

type lojaRow struct {
	Cod, Razao, UnitName, SuggestedName string
	UnitID, SuggestedID                 *uuid.UUID
}

func (e unitsX2Env) list(t *testing.T, userID uuid.UUID) (int, []lojaRow) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, userID)
	require.NoError(t, e.app.ListUnitXProcessLojas(req))
	var resp struct {
		Data struct {
			Lojas []struct {
				Cod       string     `json:"cod_empresa"`
				Razao     string     `json:"razao_social_empresa"`
				UnitID    *uuid.UUID `json:"unit_id"`
				UnitName  string     `json:"unit_name"`
				SugID     *uuid.UUID `json:"suggested_unit_id"`
				SugName   string     `json:"suggested_unit_name"`
				CNPJ      string     `json:"cnpj_empresa"`
				UnusedKey string     `json:"-"`
			} `json:"lojas"`
		} `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	rows := []lojaRow{}
	for _, l := range resp.Data.Lojas {
		rows = append(rows, lojaRow{Cod: l.Cod, Razao: l.Razao, UnitName: l.UnitName, SuggestedName: l.SugName, UnitID: l.UnitID, SuggestedID: l.SugID})
	}
	return testutil.GetResponseStatusCode(req), rows
}

func rowFor(rows []lojaRow, cod string) lojaRow {
	for _, r := range rows {
		if r.Cod == cod {
			return r
		}
	}
	return lojaRow{}
}

// --- listing and suggestions ---

func TestUnitsX2_ListShowsStoresMappedUnitsAndOnlySuggests(t *testing.T) {
	e := newUnitsX2Env(t,
		lojaPorto, lojaSerrinha,
		loja{"17", "ATC PISOS LTDA - ME (CAMACARI)", "14.591.222/0006-45"},
		loja{"30", "ATC PISOS LTDA (CAMACARI 2)", "14.591.222/0010-21"},
		loja{"99", "OUTRA EMPRESA LTDA (NOVA)", "11.222.333/0001-81"},
	)
	porto := e.unit(t, "PORTO SEGURO")
	serrinha := e.unit(t, "SERRINHA")
	cam := e.unit(t, "CAMACARI")
	cam2 := e.unit(t, "CAMACARI 2")

	status, rows := e.list(t, e.admin.ID)
	require.Equal(t, fasthttp.StatusOK, status)
	require.Len(t, rows, 5)
	assert.Equal(t, "17", rows[0].Cod, "sorted by store code (numeric)")

	assert.Equal(t, "ATACADAO DOS PISOS LTDA (PORTO)", rowFor(rows, "40").Razao, "double spaces are collapsed")
	assert.Equal(t, "PORTO SEGURO", rowFor(rows, "40").SuggestedName, "PORTO is a word-prefix of PORTO SEGURO, unique both ways")
	assert.Equal(t, "SERRINHA", rowFor(rows, "41").SuggestedName, "exact name")
	assert.Equal(t, "CAMACARI", rowFor(rows, "17").SuggestedName, "exact name wins over the prefix")
	assert.Equal(t, "CAMACARI 2", rowFor(rows, "30").SuggestedName)
	assert.Empty(t, rowFor(rows, "99").SuggestedName, "no unit looks like it")

	// A suggestion is only a hint: nothing was saved.
	for _, u := range []models.Unit{porto, serrinha, cam, cam2} {
		assert.Nil(t, e.reload(t, u).XProcessCodEmpresa)
	}

	// Once linked, the store shows its unit and is no longer suggested to another.
	status, _ = e.set(t, e.admin.ID, porto, map[string]any{"cod_empresa": "40"})
	require.Equal(t, fasthttp.StatusOK, status)
	_, rows = e.list(t, e.admin.ID)
	r40 := rowFor(rows, "40")
	assert.Equal(t, "PORTO SEGURO", r40.UnitName)
	require.NotNil(t, r40.UnitID)
	assert.Equal(t, porto.ID, *r40.UnitID)
	assert.Empty(t, r40.SuggestedName)
}

func TestUnitsX2_AmbiguousNamesYieldNoSuggestion(t *testing.T) {
	// "LAURO" is a word-prefix of both stores: guessing would be wrong half the time.
	e := newUnitsX2Env(t,
		loja{"18", "ATC PISOS LTDA - ME (LAURO DE FREITAS)", "14.591.222/0007-26"},
		loja{"31", "COMERCIAL DE PISOS JACUIPE LTDA (LAURO 2)", "47.960.311/0003-28"},
	)
	e.unit(t, "LAURO")
	_, rows := e.list(t, e.admin.ID)
	assert.Empty(t, rowFor(rows, "18").SuggestedName)
	assert.Empty(t, rowFor(rows, "31").SuggestedName)
}

// --- link, change, clear ---

func TestUnitsX2_LinkChangeAndClear(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSerrinha)
	u := e.unit(t, "PORTO SEGURO")

	status, data := e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "40"})
	require.Equal(t, fasthttp.StatusOK, status)
	require.NotNil(t, e.reload(t, u).XProcessCodEmpresa)
	assert.Equal(t, "40", *e.reload(t, u).XProcessCodEmpresa)
	assert.Equal(t, false, data["cnpj_filled"])

	// Change to another store.
	status, _ = e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "41"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "41", *e.reload(t, u).XProcessCodEmpresa)

	// Clearing is local: it must work without calling X2 at all.
	before := atomic.LoadInt64(&e.x2.requests)
	status, _ = e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": ""})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Nil(t, e.reload(t, u).XProcessCodEmpresa)
	assert.Equal(t, before, atomic.LoadInt64(&e.x2.requests), "clearing does not query X2")

	// ...even when X2 is down.
	e.x2.status = http.StatusInternalServerError
	status, _ = e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": ""})
	assert.Equal(t, fasthttp.StatusOK, status)
}

func TestUnitsX2_ValidatesTheCodeAgainstX2OnlyWhenSaving(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	u := e.unit(t, "PORTO SEGURO")

	status, _ := e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "777"})
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a store that does not exist in X2 is refused")
	assert.Nil(t, e.reload(t, u).XProcessCodEmpresa)

	status, _ = e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "40"})
	require.Equal(t, fasthttp.StatusOK, status)

	// After saving, reading the mapping never calls X2 (see the link tests below);
	// an X2 outage does not break it, and only a NEW link needs X2.
	e.x2.status = http.StatusBadGateway
	assert.Equal(t, "40", *e.reload(t, u).XProcessCodEmpresa)
	other := e.unit(t, "SERRINHA")
	status, _ = e.set(t, e.admin.ID, other, map[string]any{"cod_empresa": "40"})
	assert.Equal(t, fasthttp.StatusBadGateway, status, "X2 unavailable while linking: a clear error, nothing saved")
	assert.Nil(t, e.reload(t, other).XProcessCodEmpresa)
}

func TestUnitsX2_OneUnitPerStoreAndPerOrganization(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	a := e.unit(t, "PORTO SEGURO")
	b := e.unit(t, "PORTO 2")
	require.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, a, map[string]any{"cod_empresa": "40"})))
	assert.Equal(t, fasthttp.StatusConflict, mustStatus(e.set(t, e.admin.ID, b, map[string]any{"cod_empresa": "40"})))
	assert.Nil(t, e.reload(t, b).XProcessCodEmpresa)
	// Re-saving the same link on the same unit is fine.
	assert.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, a, map[string]any{"cod_empresa": "40"})))

	// Another organization may link its own unit to the same X2 store code.
	e2 := newUnitsX2Env(t, lojaPorto)
	c := e2.unit(t, "PORTO")
	assert.Equal(t, fasthttp.StatusOK, mustStatus(e2.set(t, e2.admin.ID, c, map[string]any{"cod_empresa": "40"})))
}

func mustStatus(status int, _ map[string]any) int { return status }

func TestUnitsX2_DeletingAUnitFreesItsStore(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	a := e.unit(t, "PORTO SEGURO")
	require.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, a, map[string]any{"cod_empresa": "40"})))

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.admin.ID)
	testutil.SetPathParam(req, "id", a.ID.String())
	require.NoError(t, e.app.DeleteUnit(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	b := e.unit(t, "PORTO NOVO")
	assert.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, b, map[string]any{"cod_empresa": "40"})))
}

func TestUnitsX2_EditingTheUnitKeepsItsStore(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	u := e.unit(t, "PORTO SEGURO")
	require.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "40"})))

	req := testutil.NewJSONRequest(t, map[string]any{"name": "PORTO SEGURO (renomeada)", "type": "loja", "active": true})
	testutil.SetAuthContext(req, e.org.ID, e.admin.ID)
	testutil.SetPathParam(req, "id", u.ID.String())
	require.NoError(t, e.app.UpdateUnit(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	got := e.reload(t, u)
	require.NotNil(t, got.XProcessCodEmpresa)
	assert.Equal(t, "40", *got.XProcessCodEmpresa, "the regular unit form does not touch the X2 link")
}

// --- scope, permissions, X2 availability ---

func TestUnitsX2_IsolationAndPermissions(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	u := e.unit(t, "PORTO SEGURO")

	// A unit of another organization does not exist for the caller.
	other := newUnitsX2Env(t, lojaPorto)
	foreign := other.unit(t, "ALHEIA")
	assert.Equal(t, fasthttp.StatusNotFound, mustStatus(e.set(t, e.admin.ID, foreign, map[string]any{"cod_empresa": "40"})))
	assert.Nil(t, other.reload(t, foreign).XProcessCodEmpresa)

	// units:write is required to list the stores and to link (agents cannot).
	agentRole := testutil.CreateAgentRole(t, e.app.DB, e.org.ID)
	agent := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&agentRole.ID))
	status, _ := e.list(t, agent.ID)
	assert.Equal(t, fasthttp.StatusForbidden, status)
	assert.Equal(t, fasthttp.StatusForbidden, mustStatus(e.set(t, agent.ID, u, map[string]any{"cod_empresa": "40"})))
	assert.Nil(t, e.reload(t, u).XProcessCodEmpresa)

	// A role with units:write and NOT xprocess_integration:read is enough.
	role := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "unit-admin", []string{"units:write"})
	unitAdmin := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&role.ID))
	status, rows := e.list(t, unitAdmin.ID)
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Len(t, rows, 1)
	assert.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, unitAdmin.ID, u, map[string]any{"cod_empresa": "40"})))
}

func TestUnitsX2_NoIntegrationOrX2Down(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	u := e.unit(t, "PORTO SEGURO")

	e.x2.status = http.StatusInternalServerError
	status, _ := e.list(t, e.admin.ID)
	assert.Equal(t, fasthttp.StatusBadGateway, status)

	require.NoError(t, e.app.DB.Model(&models.XProcessIntegration{}).Where("organization_id = ?", e.org.ID).Update("is_active", false).Error)
	status, _ = e.list(t, e.admin.ID)
	assert.Equal(t, fasthttp.StatusConflict, status, "no active integration")
	assert.Equal(t, fasthttp.StatusConflict, mustStatus(e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "40"})))
}

// --- CNPJ: optional, only fills an empty one, never blocks the link ---

func TestUnitsX2_FillCNPJIsOptionalAndOnlyFillsAnEmptyOne(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSerrinha)

	// Off by default (not ticked): the CNPJ stays empty.
	a := e.unit(t, "PORTO SEGURO")
	_, data := e.set(t, e.admin.ID, a, map[string]any{"cod_empresa": "40"})
	assert.Equal(t, false, data["cnpj_filled"])
	assert.Equal(t, "", e.reload(t, a).CNPJ)

	// Ticked and empty: filled with the digits of the store's CNPJ.
	b := e.unit(t, "SERRINHA")
	status, data := e.set(t, e.admin.ID, b, map[string]any{"cod_empresa": "41", "fill_cnpj": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, true, data["cnpj_filled"])
	assert.Equal(t, "58675622000523", e.reload(t, b).CNPJ)

	// Ticked but the unit already has a CNPJ: never replaced; the link is still saved.
	e2 := newUnitsX2Env(t, lojaPorto)
	c := e2.unit(t, "PORTO")
	require.NoError(t, e2.app.DB.Model(&c).Update("cnpj", "07378783000190").Error)
	status, data = e2.set(t, e2.admin.ID, c, map[string]any{"cod_empresa": "40", "fill_cnpj": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, false, data["cnpj_filled"])
	assert.Equal(t, "unit_has_cnpj", data["cnpj_note"])
	got := e2.reload(t, c)
	assert.Equal(t, "07378783000190", got.CNPJ)
	assert.Equal(t, "40", *got.XProcessCodEmpresa)
}

func TestUnitsX2_FillCNPJConflictOrInvalidDoesNotBlockTheLink(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, loja{"50", "EMPRESA (RUIM)", "11.111.111/1111-11"})
	// Another unit of the organization already holds the store's CNPJ.
	holder := e.unit(t, "OUTRA")
	require.NoError(t, e.app.DB.Model(&holder).Update("cnpj", "58675622000361").Error)
	u := e.unit(t, "PORTO SEGURO")
	status, data := e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "40", "fill_cnpj": true})
	require.Equal(t, fasthttp.StatusOK, status, "the link is saved even though the CNPJ is taken")
	assert.Equal(t, false, data["cnpj_filled"])
	assert.Equal(t, "cnpj_in_use", data["cnpj_note"])
	got := e.reload(t, u)
	assert.Equal(t, "", got.CNPJ)
	assert.Equal(t, "40", *got.XProcessCodEmpresa)

	// A store with an invalid CNPJ in X2 is not copied.
	bad := e.unit(t, "RUIM")
	status, data = e.set(t, e.admin.ID, bad, map[string]any{"cod_empresa": "50", "fill_cnpj": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "store_cnpj_invalid", data["cnpj_note"])
	assert.Equal(t, "", e.reload(t, bad).CNPJ)
}

// --- audit ---

func TestUnitsX2_LinkingIsAudited(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	u := e.unit(t, "PORTO SEGURO")
	require.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, u, map[string]any{"cod_empresa": "40"})))
	// The audit row is written asynchronously.
	var entry models.AuditLog
	require.Eventually(t, func() bool {
		return e.app.DB.Where("organization_id = ? AND resource_type = ? AND resource_id = ? AND user_id = ?", e.org.ID, "unit", u.ID, e.admin.ID).
			First(&entry).Error == nil
	}, 3*time.Second, 50*time.Millisecond)
	changes, _ := json.Marshal(entry.Changes)
	assert.Contains(t, string(changes), "xprocess_cod_empresa", "the audit records the new store code")
}

// --- the store is shown where the X2 order is shown ---

func TestUnitsX2_TheOrderShowsItsStoreFromTheLocalMappingWithoutCallingX2(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	porto := e.unit(t, "PORTO SEGURO")
	require.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, porto, map[string]any{"cod_empresa": "40"})))

	// An opportunity whose X2 order came from store 40 (and another from store 99, unmapped).
	salesRole := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "sales", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&salesRole.ID))
	linkFor := func(cod string) map[string]any {
		contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
		opp := newOpenOpportunity(t, e.app, e.org.ID, agent.ID, contact.ID)
		cd := cod
		require.NoError(t, e.app.DB.Create(&models.SalesOpportunityXProcessLink{
			OrganizationID: e.org.ID, SalesOpportunityID: opp.ID, NumPedido: "5" + cod, Documento: "52998224725", CodEmpresa: &cd,
		}).Error)
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, e.org.ID, agent.ID)
		req.RequestCtx.SetUserValue("id", opp.ID.String())
		require.NoError(t, e.app.GetSalesOpportunityXProcessLink(req))
		require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		var resp struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		return resp.Data
	}

	x2Before := atomic.LoadInt64(&e.x2.requests)
	mapped := linkFor("40")
	assert.Equal(t, "40", mapped["cod_empresa"])
	assert.Equal(t, "PORTO SEGURO", mapped["unit_name"])
	assert.Equal(t, porto.ID.String(), mapped["unit_id"])

	unmapped := linkFor("99")
	assert.Equal(t, "99", unmapped["cod_empresa"], "the order keeps working: the X2 code is still shown")
	assert.Nil(t, unmapped["unit_name"])
	assert.Equal(t, x2Before, atomic.LoadInt64(&e.x2.requests), "reading the order never calls X2")
}

func TestUnitsX2_CandidatesCarryTheUnitName(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	porto := e.unit(t, "PORTO SEGURO")
	require.Equal(t, fasthttp.StatusOK, mustStatus(e.set(t, e.admin.ID, porto, map[string]any{"cod_empresa": "40"})))

	salesRole := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "sales", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&salesRole.ID))
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact).Update("cpfcnpj", "52998224725").Error)
	opp := newOpenOpportunity(t, e.app, e.org.ID, agent.ID, contact.ID)
	require.NoError(t, e.app.DB.Model(opp).Update("opened_at", time.Now().Add(-time.Hour)).Error)

	// Point the integration at an X2 that has sales (the stores server has none).
	f := newFakeX2(t, order("7701", "FECHADO", today()))
	require.NoError(t, e.app.DB.Model(&models.XProcessIntegration{}).Where("organization_id = ?", e.org.ID).Update("base_url", f.srv.URL).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, e.app.ListSalesOpportunityXProcessCandidates(req))
	var resp struct {
		Data struct {
			Candidates []struct {
				NumPedido  string `json:"num_pedido"`
				CodEmpresa string `json:"cod_empresa"`
				UnitName   string `json:"unit_name"`
			} `json:"candidates"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	require.Len(t, resp.Data.Candidates, 1)
	assert.Equal(t, "40", resp.Data.Candidates[0].CodEmpresa)
	assert.Equal(t, "PORTO SEGURO", resp.Data.Candidates[0].UnitName)
}
