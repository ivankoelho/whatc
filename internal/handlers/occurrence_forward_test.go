package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func (w *isoWorld) call(t *testing.T, h func(*fastglue.Request) error, userID uuid.UUID, occID uuid.UUID, body map[string]any, params map[string]any) (int, []byte) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, w.org.ID, userID)
	testutil.SetPathParam(req, "id", occID.String())
	for k, v := range params {
		testutil.SetPathParam(req, k, v)
	}
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

// forward opens a case from the SAC to the Porto team, as the SAC would.
func (w *isoWorld) forward(t *testing.T, extra map[string]any) uuid.UUID {
	t.Helper()
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	body := map[string]any{"contact_id": contact.ID.String(), "title": "Pedido 45872 atrasado",
		"team_id": w.teamPorto.ID.String(), "order_ref": "45872", "priority": "high", "description": "Cliente sem previsão"}
	for k, v := range extra {
		body[k] = v
	}
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	require.NoError(t, w.app.CreateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	var env struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &env))
	return env.Data.ID
}

func TestForward_LeavesTrailParticipantAndPreparedNotification(t *testing.T) {
	w := newIsoWorld(t)
	id := w.forward(t, nil)

	var occ models.Occurrence
	require.NoError(t, w.app.DB.First(&occ, id).Error)
	assert.Nil(t, occ.AssignedUserID, "born without owner")
	assert.Equal(t, w.teamPorto.ID, *occ.TeamID)
	assert.Equal(t, w.unitPorto.ID, *occ.UnitID)
	assert.Equal(t, "45872", occ.OrderRef)
	assert.Equal(t, models.GeneralStateNew, occ.GeneralState)

	var types []string
	w.app.DB.Model(&models.OccurrenceEvent{}).Where("occurrence_id = ?", id).Order("created_at").Pluck("type", &types)
	assert.Contains(t, types, string(models.OccurrenceEventOpened))
	assert.Contains(t, types, string(models.OccurrenceEventForwarded))

	var part models.OccurrenceParticipant
	require.NoError(t, w.app.DB.Where("occurrence_id = ? AND user_id = ?", id, w.sac.ID).First(&part).Error)
	assert.Equal(t, []string{models.OccurrenceCapComment}, part.Capabilities, "the requester follows and comments, nothing more")

	// No channel configured yet: the notification is recorded as skipped, never sent.
	var n models.OccurrenceNotification
	require.NoError(t, w.app.DB.Where("occurrence_id = ?", id).First(&n).Error)
	assert.Equal(t, "skipped", n.Status)
	assert.Equal(t, "no_channel_configured", n.Reason)
	assert.False(t, n.Eligible)
	assert.Equal(t, "occ:"+id.String()+":created", n.IdempotencyKey)
}

func TestForward_ChannelConfigPerUnitWinsOverOrgDefault(t *testing.T) {
	w := newIsoWorld(t)
	admin := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "acc"+uuid.NewString()[:5], []string{"accounts:read", "accounts:write"})
	u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&admin.ID))
	orgAcc := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)
	portoAcc := testutil.CreateTestWhatsAppAccount(t, w.app.DB, w.org.ID)

	set := func(body map[string]any) int {
		req := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(req, w.org.ID, u.ID)
		require.NoError(t, w.app.SetOccurrenceChannelConfig(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, set(map[string]any{"whatsapp_account": "inexistente"}))
	assert.Equal(t, fasthttp.StatusOK, set(map[string]any{"whatsapp_account": orgAcc.Name}))
	assert.Equal(t, fasthttp.StatusOK, set(map[string]any{"whatsapp_account": portoAcc.Name, "unit_id": w.unitPorto.ID.String()}))
	assert.Equal(t, fasthttp.StatusOK, set(map[string]any{"whatsapp_account": portoAcc.Name, "unit_id": w.unitPorto.ID.String()}), "idempotent update")

	id := w.forward(t, nil)
	var n models.OccurrenceNotification
	require.NoError(t, w.app.DB.Where("occurrence_id = ?", id).First(&n).Error)
	assert.Equal(t, "prepared", n.Status)
	assert.Equal(t, portoAcc.Name, n.WhatsAppAccount, "the unit's own account")
	assert.False(t, n.Eligible, "nothing is dispatched in this phase")

	// Outra loja has no override: falls back to the organization default.
	var cnt int64
	w.app.DB.Model(&models.OccurrenceChannelConfig{}).Where("organization_id = ?", w.org.ID).Count(&cnt)
	assert.EqualValues(t, 2, cnt)
}

func TestForward_FullFlowSACToLogistics(t *testing.T) {
	w := newIsoWorld(t)
	id := w.forward(t, nil)
	// Kanban "Novo" of Porto's logistics has it; the other store's has not.
	assert.True(t, w.listContainsState(t, w.logPorto.ID, "new", id))
	assert.False(t, w.listContainsState(t, w.logOutra.ID, "new", id))

	code, _ := w.call(t, w.app.ClaimOccurrence, w.logPorto.ID, id, map[string]any{}, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	code, _ = w.call(t, w.app.ClaimOccurrence, w.logOutra.ID, id, map[string]any{}, nil)
	assert.Equal(t, fasthttp.StatusForbidden, code)

	for _, st := range []string{models.GeneralStateInProgress, models.GeneralStateWaitingCustomer, models.GeneralStateInProgress, models.GeneralStateResolved} {
		code, _ = w.call(t, w.app.ChangeOccurrenceStage, w.logPorto.ID, id, map[string]any{"stage_id": w.stageOf(t, st).ID.String()}, nil)
		require.Equal(t, fasthttp.StatusOK, code, st)
	}
	// The SAC sees the resolution.
	code, body := w.call(t, w.app.GetOccurrence, w.sac.ID, id, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, string(body), `"general_state":"resolved"`)
}

func (w *isoWorld) listContainsState(t *testing.T, userID uuid.UUID, state string, id uuid.UUID) bool {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, userID)
	testutil.SetQueryParam(req, "general_state", state)
	require.NoError(t, w.app.ListOccurrences(req))
	var env struct {
		Data struct {
			Occurrences []struct {
				ID uuid.UUID `json:"id"`
			} `json:"occurrences"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &env))
	for _, o := range env.Data.Occurrences {
		if o.ID == id {
			return true
		}
	}
	return false
}
