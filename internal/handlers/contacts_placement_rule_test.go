package handlers_test

import (
	"fmt"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Unit and department belong to a colaborador only: the server decides the persisted state,
// whatever the client sends.

func (e phase2Env) stored(t *testing.T, id string) models.Contact {
	t.Helper()
	var c models.Contact
	require.NoError(t, e.app.DB.First(&c, "id = ?", id).Error)
	return c
}

func TestCreateContact_OnlyAColaboradorKeepsUnitAndDepartment(t *testing.T) {
	e := newPhase2Env(t)
	unit, dept := e.unit(t, "Loja 01"), e.department(t, "TI")
	body := func(phone, typ string) map[string]any {
		m := map[string]any{"phone_number": phone, "unit_id": unit.ID.String(), "department_id": dept.ID.String()}
		if typ != "" {
			m["contact_type"] = typ
		}
		return m
	}

	status, c := e.createContact(t, body("5511993330001", "colaborador"))
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, &unit.ID, c.UnitID)
	assert.Equal(t, &dept.ID, c.DepartmentID)

	for i, typ := range []string{"", "cliente", "fornecedor"} {
		status, c = e.createContact(t, body(fmt.Sprintf("551199333001%d", i), typ))
		require.Equal(t, fasthttp.StatusOK, status, typ)
		assert.Nil(t, c.UnitID, "type %q", typ)
		assert.Nil(t, c.DepartmentID, "type %q", typ)
		got := e.stored(t, c.ID.String())
		assert.Nil(t, got.UnitID, "persisted, type %q", typ)
		assert.Nil(t, got.DepartmentID, "persisted, type %q", typ)
	}
}

func TestUpdateContact_LeavingColaboradorClearsUnitAndDepartmentWhateverThePayloadSays(t *testing.T) {
	e := newPhase2Env(t)
	unit, dept := e.unit(t, "Loja 01"), e.department(t, "TI")
	colaborador := func() *models.Contact {
		return testutil.CreateTestContactWith(t, e.app.DB, e.org.ID,
			testutil.WithContactType(models.ContactTypeColaborador), testutil.WithContactPlacement(&unit.ID, &dept.ID))
	}

	// the type changes and the payload even tries to keep them
	contact := colaborador()
	status, c := e.updateContact(t, contact.ID.String(), map[string]any{
		"contact_type": "cliente", "unit_id": unit.ID.String(), "department_id": dept.ID.String(),
	})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Nil(t, c.UnitID)
	assert.Nil(t, c.DepartmentID)
	got := e.stored(t, contact.ID.String())
	assert.Equal(t, models.ContactTypeCliente, got.ContactType)
	assert.Nil(t, got.UnitID)
	assert.Nil(t, got.DepartmentID)

	// a type change alone clears too
	other := colaborador()
	status, _ = e.updateContact(t, other.ID.String(), map[string]any{"contact_type": "fornecedor"})
	require.Equal(t, fasthttp.StatusOK, status)
	got = e.stored(t, other.ID.String())
	assert.Nil(t, got.UnitID)
	assert.Nil(t, got.DepartmentID)

	// and "" from the frontend is the same clear
	third := colaborador()
	status, _ = e.updateContact(t, third.ID.String(), map[string]any{"contact_type": "cliente", "unit_id": "", "department_id": ""})
	require.Equal(t, fasthttp.StatusOK, status)
	got = e.stored(t, third.ID.String())
	assert.Nil(t, got.UnitID)
	assert.Nil(t, got.DepartmentID)
}

func TestUpdateContact_ANonColaboradorCannotGetAUnitFromTheClient(t *testing.T) {
	e := newPhase2Env(t)
	unit, dept := e.unit(t, "Loja 01"), e.department(t, "TI")
	for _, typ := range []models.ContactType{models.ContactTypeCliente, models.ContactTypeFornecedor} {
		contact := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactType(typ))
		status, c := e.updateContact(t, contact.ID.String(), map[string]any{"unit_id": unit.ID.String(), "department_id": dept.ID.String()})
		require.Equal(t, fasthttp.StatusOK, status, string(typ))
		assert.Nil(t, c.UnitID, string(typ))
		assert.Nil(t, c.DepartmentID, string(typ))
		got := e.stored(t, contact.ID.String())
		assert.Nil(t, got.UnitID, string(typ))
		assert.Nil(t, got.DepartmentID, string(typ))
	}

	// turning a cliente into a colaborador in the same request keeps what was sent
	contact := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID)
	status, c := e.updateContact(t, contact.ID.String(), map[string]any{"contact_type": "colaborador", "unit_id": unit.ID.String()})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, &unit.ID, c.UnitID)
}

func TestUpdateContact_ColaboradorStillRequiresTheUnitToBelongToTheOrganization(t *testing.T) {
	e := newPhase2Env(t)
	other := testutil.CreateTestOrganization(t, e.app.DB)
	foreign := models.Unit{OrganizationID: other.ID, Name: "Loja Alheia"}
	require.NoError(t, e.app.DB.Create(&foreign).Error)

	contact := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactType(models.ContactTypeColaborador))
	status, _ := e.updateContact(t, contact.ID.String(), map[string]any{"unit_id": foreign.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, status)
	assert.Nil(t, e.stored(t, contact.ID.String()).UnitID)
}

func TestUpdateContact_LegacyNonColaboradorWithAUnitIsOnlyCleanedWhenItIsEdited(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 01")
	legacy := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactPlacement(&unit.ID, nil)) // cliente with a unit
	untouched := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactPlacement(&unit.ID, nil))

	// no migration: nothing changed it yet
	assert.Equal(t, &unit.ID, e.stored(t, untouched.ID.String()).UnitID)

	// an unrelated edit of the legacy contact cleans it; the other one stays as it was
	status, c := e.updateContact(t, legacy.ID.String(), map[string]any{"profile_name": "Renomeado"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Nil(t, c.UnitID)
	assert.Nil(t, e.stored(t, legacy.ID.String()).UnitID)
	assert.Equal(t, &unit.ID, e.stored(t, untouched.ID.String()).UnitID)

	// an edit that touches neither stays an ordinary edit: an empty one is still "no fields"
	clean := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID)
	status, _ = e.updateContact(t, clean.ID.String(), map[string]any{})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestCreateContact_ClaimingAnExistingContactAppliesTheSameRule(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 01")
	existing := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithPhoneNumber("5511992220001"),
		testutil.WithContactPlacement(&unit.ID, nil)) // unassigned, cliente, with a legacy unit

	status, c := e.createContact(t, map[string]any{"phone_number": "5511992220001", "unit_id": unit.ID.String()})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, existing.ID, c.ID)
	assert.Nil(t, c.UnitID)
	assert.Nil(t, e.stored(t, existing.ID.String()).UnitID)
}

// The read-only count the design note gives to size the legacy before any cleanup is decided.
func TestLegacyPlacementCount_ByOrganization(t *testing.T) {
	e := newPhase2Env(t)
	unit, dept := e.unit(t, "Loja 01"), e.department(t, "TI")
	testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactPlacement(&unit.ID, nil))
	testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactType(models.ContactTypeFornecedor), testutil.WithContactPlacement(nil, &dept.ID))
	testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithContactType(models.ContactTypeColaborador), testutil.WithContactPlacement(&unit.ID, &dept.ID))
	testutil.CreateTestContactWith(t, e.app.DB, e.org.ID) // cliente without placement

	var n int64
	require.NoError(t, e.app.DB.Raw(`SELECT COUNT(*) FROM contacts
		WHERE organization_id = ? AND deleted_at IS NULL AND contact_type <> 'colaborador'
		AND (unit_id IS NOT NULL OR department_id IS NOT NULL)`, e.org.ID).Scan(&n).Error)
	assert.Equal(t, int64(2), n)
}
