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
	w.customerWrote(t, contact.ID) // the customer started the conversation
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
	assert.Equal(t, "no_automation_configured", n.Reason)
	assert.False(t, n.Eligible)
	assert.Equal(t, "occ:"+id.String()+":created", n.IdempotencyKey)
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

func TestForwardTargets_ListOrgTeamsOnlyAndNeedsWritePermission(t *testing.T) {
	w := newIsoWorld(t)
	other := newIsoWorld(t)
	get := func(user uuid.UUID) (int, string) {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, w.org.ID, user)
		require.NoError(t, w.app.ListForwardTargets(req))
		return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
	}
	code, body := get(w.sac.ID)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, body, w.teamPorto.ID.String(), "the SAC can pick another store's team")
	assert.Contains(t, body, w.unitPorto.Name)
	assert.NotContains(t, body, other.teamPorto.ID.String(), "teams of another organization never show")
	assert.NotContains(t, body, "members")

	reader := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "leitor"+uuid.NewString()[:4], []string{"occurrences:read"})
	u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&reader.ID))
	code, _ = get(u.ID)
	assert.Equal(t, fasthttp.StatusForbidden, code)
}

// customerWrote records an inbound WhatsApp message: the customer-initiated
// contact the automatic follow-up is allowed to build on.
func (w *isoWorld) customerWrote(t *testing.T, contactID uuid.UUID) *models.Message {
	t.Helper()
	m := &models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.org.ID, WhatsAppAccount: "customer-chat",
		ContactID: contactID, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "cadê meu pedido?",
		Status: models.MessageStatusDelivered}
	require.NoError(t, w.app.DB.Create(m).Error)
	return m
}
