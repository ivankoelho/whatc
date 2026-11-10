package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func putAgentWait(t *testing.T, w *wiring, agentID string, body map[string]any) int {
	t.Helper()
	user := testutil.CreateTestUser(t, w.app.DB, w.f.org.ID, testutil.WithSuperAdmin())
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, w.f.org.ID, user.ID)
	testutil.SetPathParam(req, "id", agentID)
	require.NoError(t, w.app.UpdateAIAgent(req))
	return testutil.GetResponseStatusCode(req)
}

// The wait is stored per agent, bounded to 0..60 s, and an update that does not mention it leaves it alone.
func TestAIAgentsAPI_WaitSeconds(t *testing.T) {
	w := newWiring(t)
	id := w.f.agent.ID.String()

	assert.Equal(t, fasthttp.StatusOK, putAgentWait(t, w, id, map[string]any{"wait_seconds": 8}))
	var stored models.AIAgent
	require.NoError(t, w.app.DB.First(&stored, "id = ?", w.f.agent.ID).Error)
	assert.Equal(t, 8, stored.WaitSeconds)

	assert.Equal(t, fasthttp.StatusOK, putAgentWait(t, w, id, map[string]any{"tone": "direto"}))
	require.NoError(t, w.app.DB.First(&stored, "id = ?", w.f.agent.ID).Error)
	assert.Equal(t, 8, stored.WaitSeconds, "an update without wait_seconds keeps it")

	for _, bad := range []int{-1, 61, 100000} {
		assert.Equal(t, fasthttp.StatusBadRequest, putAgentWait(t, w, id, map[string]any{"wait_seconds": bad}), "%d is out of range", bad)
	}
	require.NoError(t, w.app.DB.First(&stored, "id = ?", w.f.agent.ID).Error)
	assert.Equal(t, 8, stored.WaitSeconds, "a rejected value changes nothing")

	assert.Equal(t, fasthttp.StatusOK, putAgentWait(t, w, id, map[string]any{"wait_seconds": 0}))
	require.NoError(t, w.app.DB.First(&stored, "id = ?", w.f.agent.ID).Error)
	assert.Equal(t, 0, stored.WaitSeconds, "0 turns the wait off")
}

// Two agents of the same organization keep independent waits.
func TestAIAgentsAPI_WaitIsPerAgent(t *testing.T) {
	w := newWiring(t)
	other := models.AIAgent{OrganizationID: w.f.org.ID, Name: "Outro", WaitSeconds: 3}
	require.NoError(t, w.app.DB.Create(&other).Error)

	require.Equal(t, fasthttp.StatusOK, putAgentWait(t, w, w.f.agent.ID.String(), map[string]any{"wait_seconds": 12}))

	var a, b models.AIAgent
	require.NoError(t, w.app.DB.First(&a, "id = ?", w.f.agent.ID).Error)
	require.NoError(t, w.app.DB.First(&b, "id = ?", other.ID).Error)
	assert.Equal(t, 12, a.WaitSeconds)
	assert.Equal(t, 3, b.WaitSeconds, "the other agent is untouched")
}
