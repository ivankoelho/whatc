package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Protocols are internal cases for SAC, Jurídico, the store supervision and Logística support. A team of a
// commercial department (Vendas) is never a destination, by creating, forwarding, returning or regularizing, and the
// server decides: the picker is only a convenience.
func TestProtocolDestination_CommercialTeamsAreRefusedEverywhere(t *testing.T) {
	w := newXDWorld(t)
	db := w.app.DB
	sac := w.u["SAC"]

	// the sales department is flagged and holds the sales teams; the other departments hold their own
	require.NoError(t, db.Model(w.dept["Vendas"]).Update("commercial", true).Error)
	for _, k := range []string{"Vendas", "VendasOutra"} {
		require.NoError(t, db.Model(w.team[k]).Update("department_id", w.dept["Vendas"].ID).Error)
	}
	require.NoError(t, db.Model(w.team["Log"]).Update("department_id", w.dept["Logística"].ID).Error)
	require.NoError(t, db.Model(w.team["SupLoja"]).Update("department_id", w.dept["Supervisão"].ID).Error)

	create := func(teamKey string) (int, []byte) {
		contact := testutil.CreateTestContact(t, db, w.org.ID)
		w.customerWrote(t, contact.ID)
		return chatDo(t, w.app.CreateOccurrence, sac.OrganizationID, sac.ID, map[string]any{
			"contact_id": contact.ID.String(), "title": "Destino " + teamKey, "team_id": w.team[teamKey].ID.String()}, nil, nil)
	}

	for _, ok := range []string{"SAC", "Log", "SupLoja", "SupSAC"} {
		code, raw := create(ok)
		assert.Equal(t, fasthttp.StatusOK, code, "valid destination %s: %s", ok, raw)
	}
	for _, bad := range []string{"Vendas", "VendasOutra"} {
		code, _ := create(bad)
		assert.Equal(t, fasthttp.StatusUnprocessableEntity, code, "commercial destination %s", bad)
	}
	var n int64
	db.Model(&models.Occurrence{}).Where("organization_id = ? AND team_id IN ?", w.org.ID, []any{w.team["Vendas"].ID, w.team["VendasOutra"].ID}).Count(&n)
	assert.Zero(t, n, "nothing was created for a commercial team")

	// the picker does not offer them
	code, raw := chatDo(t, w.app.ListForwardTargets, sac.OrganizationID, sac.ID, nil, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.NotContains(t, string(raw), w.team["Vendas"].ID.String())
	assert.NotContains(t, string(raw), w.team["VendasOutra"].ID.String())
	assert.Contains(t, string(raw), w.team["Log"].ID.String())

	// forwarding an existing case
	occ := w.open(t, sac, w.team["Log"], "Para encaminhar")
	log := w.u["Log"]
	code, _ = chatDo(t, w.app.ForwardOccurrence, log.OrganizationID, log.ID, map[string]any{"team_id": w.team["Vendas"].ID.String(), "reason": "tentativa comercial"}, map[string]string{"id": occ.String()}, nil)
	assert.Equal(t, fasthttp.StatusUnprocessableEntity, code)
	code, raw = chatDo(t, w.app.ForwardOccurrence, log.OrganizationID, log.ID, map[string]any{"team_id": w.team["SupLoja"].ID.String(), "reason": "supervisão da loja"}, map[string]string{"id": occ.String()}, nil)
	assert.Equal(t, fasthttp.StatusOK, code, string(raw))
}
