package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func (w *isoWorld) find(t *testing.T, orgID, userID uuid.UUID, q string) (int, string) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, orgID, userID)
	testutil.SetQueryParam(req, "q", q)
	require.NoError(t, w.app.LookupContacts(req))
	return testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req))
}

// Finding is not reading: the SAC and Logistics find a customer whose chat
// another agent holds, and learn nothing about that chat.
func TestContactFind_LocatesSharedContactsWithoutOpeningTheirConversation(t *testing.T) {
	w := newIsoWorld(t)
	enableStrictVisibility(t, w.app, w.org.ID)
	contact := testutil.CreateTestContactWith(t, w.app.DB, w.org.ID, testutil.WithPhoneNumber("5511977776666"))
	require.NoError(t, w.app.DB.Model(contact).Updates(map[string]any{"assigned_user_id": w.logOutra.ID, "profile_name": "Maria da Silva", "cpfcnpj": "12345678909"}).Error)

	// The regular list does not show it to the SAC (strict visibility)...
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	testutil.SetQueryParam(req, "search", "5511977776666")
	require.NoError(t, w.app.ListContacts(req))
	assert.NotContains(t, string(testutil.GetResponseBody(req)), "Maria da Silva")

	// ...but the finder does, by phone and by name, for SAC and Logistics.
	for _, u := range []*models.User{w.sac, w.logPorto} {
		code, body := w.find(t, w.org.ID, u.ID, "5511977776666")
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Contains(t, body, "Maria da Silva")
		assert.Contains(t, body, `"can_open_conversation":false`)
		code, body = w.find(t, w.org.ID, u.ID, "maria da")
		require.Equal(t, fasthttp.StatusOK, code)
		assert.Contains(t, body, contact.ID.String())
	}

	// And it exposes nothing beyond number and name.
	_, body := w.find(t, w.org.ID, w.sac.ID, "5511977776666")
	for _, secret := range []string{"12345678909", "assigned", w.logOutra.ID.String(), "cpf", "metadata", "team"} {
		assert.NotContains(t, strings.ToLower(body), strings.ToLower(secret), secret)
	}
	// It did not open the chat: history stays closed to the SAC.
	req = testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	require.NoError(t, w.app.GetMessages(req))
	assert.Equal(t, fasthttp.StatusNotFound, testutil.GetResponseStatusCode(req))
}

func TestContactFind_IsBoundedAuthorizedAndOrganizationScoped(t *testing.T) {
	w := newIsoWorld(t)
	other := newIsoWorld(t)
	foreign := testutil.CreateTestContactWith(t, other.app.DB, other.org.ID, testutil.WithPhoneNumber("5511966665555"))
	require.NotNil(t, foreign)

	code, _ := w.find(t, w.org.ID, w.sac.ID, "5511966665555")
	assert.Equal(t, fasthttp.StatusOK, code)
	_, body := w.find(t, w.org.ID, w.sac.ID, "5511966665555")
	assert.NotContains(t, body, foreign.ID.String(), "never across organizations")

	for _, q := range []string{"", "ab", "123", "a%", "1234567"} {
		code, _ = w.find(t, w.org.ID, w.sac.ID, q)
		assert.Equal(t, fasthttp.StatusBadRequest, code, "too short to be a search: %q", q)
	}

	// Results are capped, and % is literal.
	for i := 0; i < 15; i++ {
		testutil.CreateTestContactWith(t, w.app.DB, w.org.ID, testutil.WithPhoneNumber("551190000"+uuid.NewString()[:4]))
	}
	code, body = w.find(t, w.org.ID, w.sac.ID, "55119000")
	require.Equal(t, fasthttp.StatusOK, code)
	var env struct {
		Data struct {
			Contacts []map[string]any `json:"contacts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &env))
	assert.LessOrEqual(t, len(env.Data.Contacts), 10)

	blind := testutil.CreateTestRoleWithKeys(t, w.app.DB, w.org.ID, "sem-contatos"+uuid.NewString()[:4], []string{"occurrences:read"})
	u := testutil.CreateTestUser(t, w.app.DB, w.org.ID, testutil.WithRoleID(&blind.ID))
	code, _ = w.find(t, w.org.ID, u.ID, "55119000")
	assert.Equal(t, fasthttp.StatusForbidden, code, "finding needs contacts:read")

	// Per-user rate limit.
	last := 0
	for i := 0; i < 40 && last != fasthttp.StatusTooManyRequests; i++ {
		last, _ = w.find(t, w.org.ID, w.logPorto.ID, "55119000")
	}
	assert.Equal(t, fasthttp.StatusTooManyRequests, last)
}

// The Logistics agent finds a customer and starts the authorized flow: a case
// for its own team, which queues the receipt message on the logistics account.
func TestContactFind_LogisticsStartsTheAuthorizedFlowFromAFoundContact(t *testing.T) {
	w := newNotifWorld(t)
	enableStrictVisibility(t, w.app, w.org.ID)
	contact := testutil.CreateTestContactWith(t, w.app.DB, w.org.ID, testutil.WithPhoneNumber("5511955554444"))
	require.NoError(t, w.app.DB.Model(contact).Update("assigned_user_id", w.sac.ID).Error)

	_, body := w.find(t, w.org.ID, w.logPorto.ID, "5511955554444")
	require.Contains(t, body, contact.ID.String())

	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "title": "Previsão de entrega",
		"team_id": w.teamPorto.ID.String(), "order_ref": "70001"})
	testutil.SetAuthContext(req, w.org.ID, w.logPorto.ID)
	require.NoError(t, w.app.CreateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
	var env struct {
		Data struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &env))
	assert.Equal(t, models.NotificationPending, w.intent(t, env.Data.ID).Status)
	w.run()
	assert.Equal(t, 1, w.meta.count())
	assert.Contains(t, w.meta.last().Path, w.account.PhoneID)

	var after models.Contact
	require.NoError(t, w.app.DB.First(&after, contact.ID).Error)
	assert.Equal(t, w.sac.ID, *after.AssignedUserID, "the conversation keeps its owner")
}
