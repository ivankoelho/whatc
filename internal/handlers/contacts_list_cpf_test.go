package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The conversation list is where the chat panel gets its contact from, so the list must carry the
// CPF/CNPJ the panel shows (header and flow panel fields), masked exactly like the detail response.

func (e phase2Env) getContact(t *testing.T, id string) (int, string) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	testutil.SetPathParam(req, "id", id)
	require.NoError(t, e.app.GetContact(req))
	status, c := decodeContact(t, req)
	return status, c.CPFCNPJ
}

func TestListContacts_ReturnsTheCPFCNPJOfClientesAndColaboradores(t *testing.T) {
	e := newPhase2Env(t)
	unit := e.unit(t, "Loja 01")
	_, cliente := e.createContact(t, map[string]any{"phone_number": "5511993330101", "profile_name": "Cliente CPF", "cpf_cnpj": validCPF})
	_, colab := e.createContact(t, map[string]any{"phone_number": "5511993330102", "profile_name": "Colab CNPJ", "contact_type": "colaborador", "unit_id": unit.ID.String(), "cpf_cnpj": validCNPJ})
	_, semDoc := e.createContact(t, map[string]any{"phone_number": "5511993330103", "profile_name": "Sem documento"})

	_, list := e.listContacts(t, nil)
	byID := map[string]string{}
	for _, c := range list {
		byID[c.ID.String()] = c.CPFCNPJ
	}
	assert.Equal(t, validCPFDigits, byID[cliente.ID.String()], "cliente")
	assert.Equal(t, validCNPJDigits, byID[colab.ID.String()], "colaborador")
	assert.Empty(t, byID[semDoc.ID.String()], "no document: the field stays empty")
}

func TestListContacts_MasksTheCPFCNPJLikeTheDetailResponse(t *testing.T) {
	e := newPhase2Env(t)
	_, created := e.createContact(t, map[string]any{"phone_number": "5511993330104", "profile_name": "Mascarado", "cpf_cnpj": validCPF})
	require.NoError(t, e.app.DB.Model(&models.Organization{}).Where("id = ?", e.org.ID).
		Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)

	_, list := e.listContacts(t, nil)
	var fromList string
	for _, c := range list {
		if c.ID == created.ID {
			fromList = c.CPFCNPJ
		}
	}
	status, fromDetail := e.getContact(t, created.ID.String())
	require.Equal(t, 200, status)

	assert.Equal(t, "*******4725", fromList, "all but the last four characters are masked")
	assert.NotContains(t, fromList, validCPFDigits)
	assert.Equal(t, fromDetail, fromList, "the list masks exactly like the detail")
}
