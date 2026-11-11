package handlers_test

import (
	"encoding/json"
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

func pinCall(t *testing.T, app *handlers.App, orgID, userID, contactID uuid.UUID, unpin bool) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetPathParam(req, "id", contactID.String())
	if unpin {
		require.NoError(t, app.UnpinConversation(req))
	} else {
		require.NoError(t, app.PinConversation(req))
	}
	return testutil.GetResponseStatusCode(req)
}

func listPinnedView(t *testing.T, app *handlers.App, orgID, userID uuid.UUID) (ids []uuid.UUID, pinned map[uuid.UUID]bool) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	require.NoError(t, app.ListContacts(req))
	var resp struct {
		Data struct {
			Contacts []handlers.ContactResponse `json:"contacts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	pinned = map[uuid.UUID]bool{}
	for _, c := range resp.Data.Contacts {
		ids = append(ids, c.ID)
		pinned[c.ID] = c.IsPinned
	}
	return
}

func TestPinConversation(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	me := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))
	other := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	// 5 conversations; c[0] is the oldest, c[4] the most recent.
	var c []*models.Contact
	for i := 0; i < 5; i++ {
		ct := testutil.CreateTestContact(t, app.DB, org.ID)
		at := time.Now().Add(time.Duration(i) * time.Minute)
		require.NoError(t, app.DB.Model(ct).Update("last_message_at", at).Error)
		c = append(c, ct)
	}

	t.Run("pinned come first, most recently pinned on top, others by recency", func(t *testing.T) {
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[0].ID, false))
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[1].ID, false))
		ids, pinned := listPinnedView(t, app, org.ID, me.ID)
		assert.Equal(t, []uuid.UUID{c[1].ID, c[0].ID, c[4].ID, c[3].ID, c[2].ID}, ids)
		assert.True(t, pinned[c[0].ID])
		assert.True(t, pinned[c[1].ID])
		assert.False(t, pinned[c[4].ID])
	})

	t.Run("pinning is personal", func(t *testing.T) {
		ids, pinned := listPinnedView(t, app, org.ID, other.ID)
		assert.Equal(t, c[4].ID, ids[0], "another user's order is untouched")
		for _, p := range pinned {
			assert.False(t, p)
		}
	})

	t.Run("idempotent and limited to 3", func(t *testing.T) {
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[1].ID, false), "re-pin does not use a slot")
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[2].ID, false)) // 3rd
		assert.Equal(t, fasthttp.StatusConflict, pinCall(t, app, org.ID, me.ID, c[3].ID, false), "4th is refused")
		var n int64
		app.DB.Model(&models.ConversationPin{}).Where("user_id = ?", me.ID).Count(&n)
		assert.Equal(t, int64(3), n)
		// the limit is per user
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, other.ID, c[3].ID, false))
	})

	t.Run("unpin frees a slot and is idempotent", func(t *testing.T) {
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[0].ID, true))
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[0].ID, true))
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, me.ID, c[3].ID, false))
	})

	t.Run("cannot pin a conversation from another org", func(t *testing.T) {
		otherOrg := testutil.CreateTestOrganization(t, app.DB)
		foreign := testutil.CreateTestContact(t, app.DB, otherOrg.ID)
		assert.Equal(t, fasthttp.StatusNotFound, pinCall(t, app, org.ID, me.ID, foreign.ID, false))
	})
}

// A user who cannot see a conversation cannot pin it, and a pin left on a
// conversation that went out of their scope must not eat one of their slots.
func TestPinConversation_RespectsVisibility(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agentRole := testutil.CreateAgentRole(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&agentRole.ID))

	// Nobody owns it, no queue, no team: invisible to an agent.
	hidden := testutil.CreateTestContact(t, app.DB, org.ID)
	assert.Equal(t, fasthttp.StatusNotFound, pinCall(t, app, org.ID, agent.ID, hidden.ID, false))

	// Visible conversations: assigned to the agent.
	var mine []*models.Contact
	for i := 0; i < 4; i++ {
		ct := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(ct).Update("assigned_user_id", agent.ID).Error)
		mine = append(mine, ct)
	}
	for i := 0; i < 3; i++ {
		assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, agent.ID, mine[i].ID, false))
	}
	assert.Equal(t, fasthttp.StatusConflict, pinCall(t, app, org.ID, agent.ID, mine[3].ID, false))

	// One of the pinned ones is taken away from the agent: its pin no longer
	// counts, so the slot is free again.
	require.NoError(t, app.DB.Model(mine[0]).Update("assigned_user_id", nil).Error)
	assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, agent.ID, mine[3].ID, false))
	// and the leftover mark can still be cleared
	assert.Equal(t, fasthttp.StatusOK, pinCall(t, app, org.ID, agent.ID, mine[0].ID, true))
}

// Two quick clicks (or two tabs) must not both pass the limit check.
func TestPinConversation_ConcurrentPinsRespectLimit(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	me := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	const n = 8
	statuses := make(chan int, n)
	for i := 0; i < n; i++ {
		ct := testutil.CreateTestContact(t, app.DB, org.ID)
		go func() { statuses <- pinCall(t, app, org.ID, me.ID, ct.ID, false) }()
	}
	ok, refused := 0, 0
	for i := 0; i < n; i++ {
		switch <-statuses {
		case fasthttp.StatusOK:
			ok++
		case fasthttp.StatusConflict:
			refused++
		}
	}
	var rows int64
	app.DB.Model(&models.ConversationPin{}).Where("user_id = ?", me.ID).Count(&rows)
	assert.Equal(t, int64(models.MaxPinnedConversations), rows)
	assert.Equal(t, models.MaxPinnedConversations, ok)
	assert.Equal(t, n-models.MaxPinnedConversations, refused)
}
