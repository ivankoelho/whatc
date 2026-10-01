package handlers_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	validCPF         = "529.982.247-25"
	validCPFDigits   = "52998224725"
	validCNPJ        = "07.378.783/0001-90"
	validCNPJDigits  = "07378783000190"
	invalidCPFDigits = "52998224726"
)

type phase2Env struct {
	app  *handlers.App
	org  *models.Organization
	user *models.User
}

func newPhase2Env(t *testing.T) phase2Env {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	return phase2Env{app, org, user}
}

func (e phase2Env) createContact(t *testing.T, body map[string]any) (int, handlers.ContactResponse) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	require.NoError(t, e.app.CreateContact(req))
	return decodeContact(t, req)
}

func (e phase2Env) updateContact(t *testing.T, id string, body map[string]any) (int, handlers.ContactResponse) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	testutil.SetPathParam(req, "id", id)
	require.NoError(t, e.app.UpdateContact(req))
	return decodeContact(t, req)
}

func decodeContact(t *testing.T, req *fastglue.Request) (int, handlers.ContactResponse) {
	t.Helper()
	var resp struct {
		Data handlers.ContactResponse `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data
}

func (e phase2Env) unit(t *testing.T, name string) models.Unit {
	t.Helper()
	u := models.Unit{OrganizationID: e.org.ID, Name: name}
	require.NoError(t, e.app.DB.Create(&u).Error)
	return u
}

func (e phase2Env) department(t *testing.T, name string) models.Department {
	t.Helper()
	d := models.Department{OrganizationID: e.org.ID, Name: name}
	require.NoError(t, e.app.DB.Create(&d).Error)
	return d
}

// --- contact_type ---

func TestCreateContact_TypeDefaultsToCliente(t *testing.T) {
	e := newPhase2Env(t)
	status, c := e.createContact(t, map[string]any{"phone_number": "5511990001111"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, models.ContactTypeCliente, c.ContactType)

	var stored models.Contact
	require.NoError(t, e.app.DB.First(&stored, "id = ?", c.ID).Error)
	assert.Equal(t, models.ContactTypeCliente, stored.ContactType)
}

func TestContact_ColumnDefaultIsCliente(t *testing.T) {
	// Rows created without the field (inbound WhatsApp, imports) and rows that
	// existed before the column both land on "cliente".
	e := newPhase2Env(t)
	c := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	var stored models.Contact
	require.NoError(t, e.app.DB.First(&stored, "id = ?", c.ID).Error)
	assert.Equal(t, models.ContactTypeCliente, stored.ContactType)
}

func TestCreateContact_ExplicitTypeAndInvalidType(t *testing.T) {
	e := newPhase2Env(t)
	for i, ct := range []string{"cliente", "fornecedor", "colaborador"} {
		status, c := e.createContact(t, map[string]any{"phone_number": fmt.Sprintf("551199000220%d", i), "contact_type": ct})
		require.Equal(t, fasthttp.StatusOK, status, ct)
		assert.Equal(t, models.ContactType(ct), c.ContactType)
	}
	status, _ := e.createContact(t, map[string]any{"phone_number": "5511990003333", "contact_type": "parceiro"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestUpdateContact_TypeChangeAndInvalid(t *testing.T) {
	e := newPhase2Env(t)
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)

	status, c := e.updateContact(t, contact.ID.String(), map[string]any{"contact_type": "colaborador"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, models.ContactTypeColaborador, c.ContactType)

	status, _ = e.updateContact(t, contact.ID.String(), map[string]any{"contact_type": ""})
	assert.Equal(t, fasthttp.StatusBadRequest, status, "an empty type is not a type")
	status, _ = e.updateContact(t, contact.ID.String(), map[string]any{"contact_type": "x"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	var stored models.Contact
	require.NoError(t, e.app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Equal(t, models.ContactTypeColaborador, stored.ContactType, "a rejected update changes nothing")
}

// --- documento ---

func TestContactDocument_Validation(t *testing.T) {
	e := newPhase2Env(t)

	cases := []struct {
		name, doc, stored string
		status            int
	}{
		{"CPF with mask", validCPF, validCPFDigits, fasthttp.StatusOK},
		{"CPF without mask", validCPFDigits, validCPFDigits, fasthttp.StatusOK},
		{"CNPJ with mask", validCNPJ, validCNPJDigits, fasthttp.StatusOK},
		{"empty", "", "", fasthttp.StatusOK},
		{"CPF with bad check digit", invalidCPFDigits, "", fasthttp.StatusBadRequest},
		{"wrong length", "12345", "", fasthttp.StatusBadRequest},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phone := fmt.Sprintf("551199100%d000", i)
			status, resp := e.createContact(t, map[string]any{"phone_number": phone, "cpf_cnpj": c.doc})
			require.Equal(t, c.status, status)
			if c.status == fasthttp.StatusOK {
				assert.Equal(t, c.stored, resp.CPFCNPJ)
			}
		})
	}
}

func TestUpdateContact_InvalidDocumentRejectedValidStoredAsDigits(t *testing.T) {
	e := newPhase2Env(t)
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)

	status, _ := e.updateContact(t, contact.ID.String(), map[string]any{"cpf_cnpj": invalidCPFDigits})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	// The map-based Updates() must keep writing the physical column "cpfcnpj".
	status, _ = e.updateContact(t, contact.ID.String(), map[string]any{"cpf_cnpj": validCPF})
	require.Equal(t, fasthttp.StatusOK, status)
	var stored models.Contact
	require.NoError(t, e.app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Equal(t, validCPFDigits, stored.CPFCNPJ)

	// Clearing is allowed.
	status, _ = e.updateContact(t, contact.ID.String(), map[string]any{"cpf_cnpj": ""})
	require.Equal(t, fasthttp.StatusOK, status)
	require.NoError(t, e.app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Equal(t, "", stored.CPFCNPJ)
}

func TestContactDocument_ClaimingExistingContactValidatesToo(t *testing.T) {
	e := newPhase2Env(t)
	existing := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithPhoneNumber("5511990009998"))

	status, _ := e.createContact(t, map[string]any{"phone_number": existing.PhoneNumber, "cpf_cnpj": invalidCPFDigits})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	status, _ = e.createContact(t, map[string]any{"phone_number": existing.PhoneNumber, "cpf_cnpj": validCPF, "contact_type": "fornecedor"})
	require.Equal(t, fasthttp.StatusOK, status)
	var stored models.Contact
	require.NoError(t, e.app.DB.First(&stored, "id = ?", existing.ID).Error)
	assert.Equal(t, validCPFDigits, stored.CPFCNPJ)
	assert.Equal(t, models.ContactTypeFornecedor, stored.ContactType)
}

func TestContactDocument_DuplicatesAreAllowed(t *testing.T) {
	e := newPhase2Env(t)
	s1, _ := e.createContact(t, map[string]any{"phone_number": "5511991110001", "cpf_cnpj": validCPF})
	s2, _ := e.createContact(t, map[string]any{"phone_number": "5511991110002", "cpf_cnpj": validCPFDigits})
	assert.Equal(t, fasthttp.StatusOK, s1)
	assert.Equal(t, fasthttp.StatusOK, s2, "the same person may have several contacts")

	var n int64
	e.app.DB.Model(&models.Contact{}).Where("organization_id = ? AND cpfcnpj = ?", e.org.ID, validCPFDigits).Count(&n)
	assert.Equal(t, int64(2), n)
}

func (e phase2Env) listContacts(t *testing.T, query map[string]string) (int, []handlers.ContactResponse) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	for k, v := range query {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, e.app.ListContacts(req))
	var resp struct {
		Data struct {
			Contacts []handlers.ContactResponse `json:"contacts"`
		} `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &resp)
	return testutil.GetResponseStatusCode(req), resp.Data.Contacts
}

func TestListContacts_SearchByDocumentWithAndWithoutMask(t *testing.T) {
	e := newPhase2Env(t)
	e.createContact(t, map[string]any{"phone_number": "5511992220001", "cpf_cnpj": validCPF, "profile_name": "Ana"})
	e.createContact(t, map[string]any{"phone_number": "5511992220002", "profile_name": "Bia"})

	for _, term := range []string{validCPF, validCPFDigits, "529.982", "982247"} {
		status, list := e.listContacts(t, map[string]string{"search": term})
		require.Equal(t, fasthttp.StatusOK, status, term)
		require.Len(t, list, 1, "search %q", term)
		assert.Equal(t, "Ana", list[0].ProfileName)
	}
	// Name and phone search keep working.
	_, list := e.listContacts(t, map[string]string{"search": "Bia"})
	require.Len(t, list, 1)
	_, list = e.listContacts(t, map[string]string{"search": "5511992220002"})
	require.Len(t, list, 1)
}

func TestListContacts_FilterByTypeUnitDepartment(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 05")
	dept := e.department(t, "Expedição")
	e.createContact(t, map[string]any{"phone_number": "5511993330001", "contact_type": "colaborador", "unit_id": unit.ID.String(), "department_id": dept.ID.String()})
	e.createContact(t, map[string]any{"phone_number": "5511993330002"})

	_, list := e.listContacts(t, map[string]string{"contact_type": "colaborador"})
	require.Len(t, list, 1)
	assert.Equal(t, &unit.ID, list[0].UnitID)
	assert.Equal(t, &dept.ID, list[0].DepartmentID)

	_, list = e.listContacts(t, map[string]string{"unit_id": unit.ID.String(), "department_id": dept.ID.String()})
	require.Len(t, list, 1)

	status, _ := e.listContacts(t, map[string]string{"contact_type": "x"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	status, _ = e.listContacts(t, map[string]string{"unit_id": "not-a-uuid"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

// --- unit / department on a contact ---

func TestContact_PlacementMustBelongToTheOrganization(t *testing.T) {
	e := newPhase2Env(t)
	other := testutil.CreateTestOrganization(t, e.app.DB)
	foreignUnit := models.Unit{OrganizationID: other.ID, Name: "Loja Alheia"}
	foreignDept := models.Department{OrganizationID: other.ID, Name: "Dept Alheio"}
	require.NoError(t, e.app.DB.Create(&foreignUnit).Error)
	require.NoError(t, e.app.DB.Create(&foreignDept).Error)

	status, _ := e.createContact(t, map[string]any{"phone_number": "5511994440001", "unit_id": foreignUnit.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = e.createContact(t, map[string]any{"phone_number": "5511994440001", "department_id": foreignDept.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = e.createContact(t, map[string]any{"phone_number": "5511994440001", "unit_id": "nope"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	status, _ = e.updateContact(t, contact.ID.String(), map[string]any{"unit_id": foreignUnit.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _ = e.updateContact(t, contact.ID.String(), map[string]any{"department_id": foreignDept.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	var stored models.Contact
	require.NoError(t, e.app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Nil(t, stored.UnitID)
	assert.Nil(t, stored.DepartmentID)
}

func TestContact_PlacementSetAndClear(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 01")
	dept := e.department(t, "TI")
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)

	status, c := e.updateContact(t, contact.ID.String(), map[string]any{"unit_id": unit.ID.String(), "department_id": dept.ID.String()})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, &unit.ID, c.UnitID)

	// Absent fields are left alone.
	status, c = e.updateContact(t, contact.ID.String(), map[string]any{"contact_type": "fornecedor"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, &unit.ID, c.UnitID)
	assert.Equal(t, &dept.ID, c.DepartmentID)

	// An empty string clears one without touching the other.
	status, c = e.updateContact(t, contact.ID.String(), map[string]any{"unit_id": ""})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Nil(t, c.UnitID)
	assert.Equal(t, &dept.ID, c.DepartmentID)
}

// --- deleting a unit / department that something still points at ---

func TestDeleteUnit_BlockedByContactAndUser(t *testing.T) {
	e := newPhase2Env(t)
	del := func(u models.Unit) int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, e.org.ID, e.user.ID)
		testutil.SetPathParam(req, "id", u.ID.String())
		require.NoError(t, e.app.DeleteUnit(req))
		return testutil.GetResponseStatusCode(req)
	}

	withContact := e.unit(t, "Com contato")
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact).Update("unit_id", withContact.ID).Error)
	assert.Equal(t, fasthttp.StatusConflict, del(withContact))

	withUser := e.unit(t, "Com usuario")
	require.NoError(t, e.app.DB.Model(e.user).Update("unit_id", withUser.ID).Error)
	assert.Equal(t, fasthttp.StatusConflict, del(withUser))

	free := e.unit(t, "Livre")
	assert.Equal(t, fasthttp.StatusOK, del(free))

	// Once the reference is gone the unit can be deleted.
	require.NoError(t, e.app.DB.Model(contact).Update("unit_id", nil).Error)
	assert.Equal(t, fasthttp.StatusOK, del(withContact))
}

func TestDeleteUnit_SoftDeletedContactStillBlocks(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja X")
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact).Update("unit_id", unit.ID).Error)
	require.NoError(t, e.app.DB.Delete(contact).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	testutil.SetPathParam(req, "id", unit.ID.String())
	require.NoError(t, e.app.DeleteUnit(req))
	assert.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req),
		"restoring the contact would otherwise bring back a pointer to a deleted unit")
}

func TestDeleteDepartment_BlockedByContactAndUser(t *testing.T) {
	e := newPhase2Env(t)
	del := func(d models.Department) int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, e.org.ID, e.user.ID)
		testutil.SetPathParam(req, "id", d.ID.String())
		require.NoError(t, e.app.DeleteDepartment(req))
		return testutil.GetResponseStatusCode(req)
	}
	d1 := e.department(t, "D1")
	contact := testutil.CreateTestContact(t, e.app.DB, e.org.ID)
	require.NoError(t, e.app.DB.Model(contact).Update("department_id", d1.ID).Error)
	assert.Equal(t, fasthttp.StatusConflict, del(d1))

	d2 := e.department(t, "D2")
	require.NoError(t, e.app.DB.Model(e.user).Update("department_id", d2.ID).Error)
	assert.Equal(t, fasthttp.StatusConflict, del(d2))

	assert.Equal(t, fasthttp.StatusOK, del(e.department(t, "D3")))
}
