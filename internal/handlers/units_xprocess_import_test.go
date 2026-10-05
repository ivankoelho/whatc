package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

var (
	lojaCamacari = loja{"42", "ATACADAO DOS PISOS LTDA  (CAMACARI)", "58.675.622/0001-08"}
	lojaSemParen = loja{"43", "ATACADAO DOS PISOS   MATRIZ LTDA", "58.675.622/0007-95"}
	lojaCNPJRuim = loja{"44", "ATACADAO DOS PISOS LTDA (FEIRA)", "00.000.000/0000-00"}
)

type xImportResult struct {
	Cod     string   `json:"cod_empresa"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason"`
	Notes   []string `json:"notes"`
	X2Store *struct {
		CodEmpresa string `json:"cod_empresa"`
	} `json:"x2_store"`
	Unit         *models.Unit `json:"unit"`
	ExistingUnit *struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"existing_unit"`
}

type xImportResp struct {
	Summary map[string]int  `json:"summary"`
	Results []xImportResult `json:"results"`
}

func (r xImportResp) result(cod string) xImportResult {
	for _, x := range r.Results {
		if x.Cod == cod {
			return x
		}
	}
	return xImportResult{}
}

func (e unitsX2Env) importCodes(t *testing.T, userID uuid.UUID, codes ...string) (int, xImportResp) {
	t.Helper()
	req := testutil.NewJSONRequest(t, map[string]any{"cod_empresa": codes})
	testutil.SetAuthContext(req, e.org.ID, userID)
	require.NoError(t, e.app.ImportUnitsFromXProcess(req))
	var resp struct {
		Data xImportResp `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func (e unitsX2Env) countUnits(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.app.DB.Model(&models.Unit{}).Where("organization_id = ?", e.org.ID).Count(&n).Error)
	return n
}

// Case 1: the listing says what importing each store would do.
func TestUnitsX2Import_ListShowsImportStatus(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSerrinha, lojaCamacari)
	linked := e.unit(t, "UNIDADE PORTO")
	require.NoError(t, e.app.DB.Model(&linked).Update("xprocess_cod_empresa", "40").Error)
	e.unit(t, "ATACADAO DOS PISOS LTDA  (CAMACARI)") // same name as store 42, no link: a clash

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.admin.ID)
	require.NoError(t, e.app.ListUnitXProcessLojas(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data struct {
			Lojas []struct {
				Cod            string `json:"cod_empresa"`
				ImportStatus   string `json:"import_status"`
				ImportName     string `json:"import_name"`
				ConflictReason string `json:"conflict_reason"`
				ConflictUnit   string `json:"conflict_unit_name"`
				UnitName       string `json:"unit_name"`
			} `json:"lojas"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	got := map[string]int{}
	for i, l := range resp.Data.Lojas {
		got[l.Cod] = i
	}
	porto, serrinha, camacari := resp.Data.Lojas[got["40"]], resp.Data.Lojas[got["41"]], resp.Data.Lojas[got["42"]]
	assert.Equal(t, "already_imported", porto.ImportStatus)
	assert.Equal(t, "UNIDADE PORTO", porto.UnitName)
	assert.Equal(t, "new", serrinha.ImportStatus)
	assert.Equal(t, "ATACADAO DOS PISOS LTDA  (SERRINHA)", serrinha.ImportName, "the name is X2's, unchanged")
	assert.Equal(t, "conflict", camacari.ImportStatus)
	assert.Equal(t, "name_in_use", camacari.ConflictReason)
	assert.Equal(t, "ATACADAO DOS PISOS LTDA  (CAMACARI)", camacari.ConflictUnit)
}

// Case 2: a new store becomes a unit (X2 name unchanged, CNPJ as digits,
// the code, active) and nothing else is invented.
func TestUnitsX2Import_CreatesTheUnit(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	status, resp := e.importCodes(t, e.admin.ID, "40")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, 1, resp.Summary["created"])
	r := resp.result("40")
	require.Equal(t, "created", r.Status)
	require.NotNil(t, r.Unit)

	var u models.Unit
	require.NoError(t, e.app.DB.First(&u, "id = ?", r.Unit.ID).Error)
	assert.Equal(t, e.org.ID, u.OrganizationID)
	assert.Equal(t, "ATACADAO DOS PISOS LTDA  (PORTO)", u.Name, "not renamed: exactly what X2 has")
	assert.Equal(t, "58675622000361", u.CNPJ)
	require.NotNil(t, u.XProcessCodEmpresa)
	assert.Equal(t, "40", *u.XProcessCodEmpresa)
	assert.True(t, u.Active)
	assert.Empty(t, u.Type)
	assert.Empty(t, u.Address)
}

// Case 3: several stores in one call; each is reported on its own.
func TestUnitsX2Import_Batch(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSerrinha, lojaCamacari)
	_, resp := e.importCodes(t, e.admin.ID, "40", "41", "42")
	assert.Equal(t, map[string]int{"selected": 3, "created": 3, "updated": 0, "already_exists": 0, "conflicts": 0, "failed": 0}, resp.Summary)
	assert.EqualValues(t, 3, e.countUnits(t))
}

// Case 4: idempotence. Importing again never creates a duplicate.
func TestUnitsX2Import_IsIdempotent(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSerrinha)

	_, first := e.importCodes(t, e.admin.ID, "40")
	require.Equal(t, 1, first.Summary["created"])
	_, again := e.importCodes(t, e.admin.ID, "40")
	assert.Equal(t, 0, again.Summary["created"])
	assert.Equal(t, 1, again.Summary["already_exists"])
	assert.Equal(t, first.result("40").Unit.ID, again.result("40").Unit.ID)
	assert.EqualValues(t, 1, e.countUnits(t))

	_, both := e.importCodes(t, e.admin.ID, "40", "41")
	assert.Equal(t, 1, both.Summary["created"], "only 41 is new")
	_, bothAgain := e.importCodes(t, e.admin.ID, "40", "41")
	assert.Equal(t, 0, bothAgain.Summary["created"])
	assert.Equal(t, 2, bothAgain.Summary["already_exists"])
	assert.EqualValues(t, 2, e.countUnits(t))

	// The same code twice in one request is one store.
	_, dup := e.importCodes(t, e.admin.ID, "40", " 40 ")
	assert.Equal(t, 1, dup.Summary["selected"])
}

// Case 5: a unit with the store's CNPJ and no link is a conflict, never a second unit.
func TestUnitsX2Import_CNPJConflict(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	a := models.Unit{OrganizationID: e.org.ID, Name: "Unidade A", CNPJ: "58675622000361", Active: true}
	require.NoError(t, e.app.DB.Create(&a).Error)

	_, resp := e.importCodes(t, e.admin.ID, "40")
	assert.Equal(t, 1, resp.Summary["conflicts"])
	r := resp.result("40")
	assert.Equal(t, "conflict", r.Status)
	assert.Equal(t, "cnpj_in_use", r.Reason)
	require.NotNil(t, r.ExistingUnit)
	assert.Equal(t, a.ID, r.ExistingUnit.ID)
	require.NotNil(t, r.X2Store)
	assert.Equal(t, "40", r.X2Store.CodEmpresa)
	assert.EqualValues(t, 1, e.countUnits(t), "nothing was created")
	assert.Nil(t, e.reload(t, a).XProcessCodEmpresa, "and the existing unit was not linked silently")
}

// Case 6: a store already tied to a unit is reported as existing. Local fields are
// not overwritten; an EMPTY CNPJ is filled; a different one is only noted.
func TestUnitsX2Import_StoreAlreadyLinkedToAnotherUnit(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSerrinha)
	mine := models.Unit{OrganizationID: e.org.ID, Name: "Nome local", Type: "matriz", Active: false, CNPJ: "11222333000181"}
	require.NoError(t, e.app.DB.Create(&mine).Error)
	require.NoError(t, e.app.DB.Model(&mine).Updates(map[string]any{"xprocess_cod_empresa": "40", "active": false}).Error) // active:false needs an update: GORM defaults a zero bool on create
	empty := models.Unit{OrganizationID: e.org.ID, Name: "Sem CNPJ", Active: true}
	require.NoError(t, e.app.DB.Create(&empty).Error)
	require.NoError(t, e.app.DB.Model(&empty).Update("xprocess_cod_empresa", "41").Error)

	_, resp := e.importCodes(t, e.admin.ID, "40", "41")
	r40, r41 := resp.result("40"), resp.result("41")

	assert.Equal(t, "already_exists", r40.Status)
	assert.Equal(t, mine.ID, r40.Unit.ID)
	assert.Contains(t, r40.Notes, "cnpj_differs")
	got := e.reload(t, mine)
	assert.Equal(t, "Nome local", got.Name)
	assert.Equal(t, "matriz", got.Type)
	assert.False(t, got.Active, "active is never overwritten")
	assert.Equal(t, "11222333000181", got.CNPJ, "a different local CNPJ is kept")

	assert.Equal(t, "updated", r41.Status)
	assert.Equal(t, "58675622000523", e.reload(t, empty).CNPJ, "an empty CNPJ is filled")
	assert.Equal(t, "Sem CNPJ", e.reload(t, empty).Name)
	assert.EqualValues(t, 2, e.countUnits(t))
}

// Case 7: another organization never reads or changes this one's units.
func TestUnitsX2Import_OrganizationIsolation(t *testing.T) {
	a := newUnitsX2Env(t, lojaPorto)
	b := newUnitsX2Env(t, lojaPorto)
	_, ra := a.importCodes(t, a.admin.ID, "40")
	require.Equal(t, "created", ra.result("40").Status)

	// B imports the same code: it gets its OWN unit, A's is untouched.
	_, rb := b.importCodes(t, b.admin.ID, "40")
	require.Equal(t, "created", rb.result("40").Status)
	assert.NotEqual(t, ra.result("40").Unit.ID, rb.result("40").Unit.ID)
	assert.Equal(t, b.org.ID, rb.result("40").Unit.OrganizationID)
	assert.EqualValues(t, 1, a.countUnits(t))
	assert.EqualValues(t, 1, b.countUnits(t))

	// B's administrator calling with A's data context cannot see A's unit as a conflict.
	req := testutil.NewJSONRequest(t, map[string]any{"cod_empresa": []string{"40"}})
	testutil.SetAuthContext(req, b.org.ID, b.admin.ID)
	require.NoError(t, b.app.ImportUnitsFromXProcess(req))
	var resp struct {
		Data xImportResp `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	assert.Equal(t, "already_exists", resp.Data.result("40").Status)
	assert.Equal(t, b.org.ID, resp.Data.result("40").Unit.OrganizationID)
}

// Case 8: units:write is required.
func TestUnitsX2Import_NeedsUnitsWrite(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	agentRole := testutil.CreateAgentRole(t, e.app.DB, e.org.ID)
	agent := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&agentRole.ID))
	status, _ := e.importCodes(t, agent.ID, "40")
	assert.Equal(t, fasthttp.StatusForbidden, status)
	assert.EqualValues(t, 0, e.countUnits(t))

	readOnly := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "units-read", []string{"units:read"})
	reader := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&readOnly.ID))
	status, _ = e.importCodes(t, reader.ID, "40")
	assert.Equal(t, fasthttp.StatusForbidden, status)

	// units:write alone (no X2 integration permission) is enough.
	role := testutil.CreateTestRoleWithKeys(t, e.app.DB, e.org.ID, "units-write", []string{"units:write"})
	writer := testutil.CreateTestUser(t, e.app.DB, e.org.ID, testutil.WithRoleID(&role.ID))
	status, resp := e.importCodes(t, writer.ID, "40")
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, 1, resp.Summary["created"])
}

// Cases 9 and 10: X2 unavailable or answering garbage writes nothing.
func TestUnitsX2Import_X2DownOrInvalidResponse(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)

	e.x2.status = http.StatusInternalServerError
	status, _ := e.importCodes(t, e.admin.ID, "40")
	assert.Equal(t, fasthttp.StatusBadGateway, status)

	for name, body := range map[string]string{"not json": `<html>oops</html>`, "ok false": `{"ok":false,"lojas":[]}`} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		t.Cleanup(bad.Close)
		require.NoError(t, e.app.DB.Model(&models.XProcessIntegration{}).Where("organization_id = ?", e.org.ID).Update("base_url", bad.URL).Error)
		status, _ = e.importCodes(t, e.admin.ID, "40")
		assert.Equal(t, fasthttp.StatusBadGateway, status, name)
	}
	assert.EqualValues(t, 0, e.countUnits(t))

	require.NoError(t, e.app.DB.Model(&models.XProcessIntegration{}).Where("organization_id = ?", e.org.ID).Update("is_active", false).Error)
	status, _ = e.importCodes(t, e.admin.ID, "40")
	assert.Equal(t, fasthttp.StatusConflict, status, "no active integration")
}

func TestUnitsX2Import_InputAndEdgeCases(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto, lojaSemParen, lojaCNPJRuim)

	status, _ := e.importCodes(t, e.admin.ID)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "nothing selected")
	status, _ = e.importCodes(t, e.admin.ID, "  ", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	_, resp := e.importCodes(t, e.admin.ID, "999", "43", "44")
	assert.Equal(t, "failed", resp.result("999").Status)
	assert.Equal(t, "not_in_x2", resp.result("999").Reason)
	assert.Equal(t, "ATACADAO DOS PISOS   MATRIZ LTDA", resp.result("43").Unit.Name, "no parentheses: the whole name, as in X2")
	bad := resp.result("44")
	assert.Equal(t, "created", bad.Status)
	assert.Equal(t, "ATACADAO DOS PISOS LTDA (FEIRA)", bad.Unit.Name)
	assert.Empty(t, bad.Unit.CNPJ, "an invalid store CNPJ is not copied")
	assert.Contains(t, bad.Notes, "store_cnpj_invalid")
	assert.Equal(t, map[string]int{"selected": 3, "created": 2, "updated": 0, "already_exists": 0, "conflicts": 0, "failed": 1}, resp.Summary)
}

// Two stores of one batch that would end with the same name: the second is a conflict.
func TestUnitsX2Import_SameNameInOneBatch(t *testing.T) {
	twin := loja{"50", "ATACADAO DOS PISOS LTDA  (PORTO)", "58.675.622/0005-23"}
	e := newUnitsX2Env(t, lojaPorto, twin)
	_, resp := e.importCodes(t, e.admin.ID, "40", "50")
	assert.Equal(t, 1, resp.Summary["created"])
	assert.Equal(t, 1, resp.Summary["conflicts"])
	assert.Equal(t, "name_in_use", resp.result("50").Reason)
	assert.EqualValues(t, 1, e.countUnits(t))
}

func TestUnitsX2Import_IsAudited(t *testing.T) {
	e := newUnitsX2Env(t, lojaPorto)
	_, resp := e.importCodes(t, e.admin.ID, "40")
	id := resp.result("40").Unit.ID
	var entry models.AuditLog
	require.Eventually(t, func() bool {
		return e.app.DB.Where("organization_id = ? AND resource_type = ? AND resource_id = ? AND user_id = ? AND action = ?",
			e.org.ID, "unit", id, e.admin.ID, models.AuditActionCreated).First(&entry).Error == nil
	}, 3*time.Second, 50*time.Millisecond)
	changes, _ := json.Marshal(entry.Changes)
	assert.Contains(t, string(changes), "xprocess_import")
	assert.Contains(t, string(changes), "xprocess_cod_empresa")
}
