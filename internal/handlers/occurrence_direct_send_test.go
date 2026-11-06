package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Direct Send: closed window + account opted in → the send reaches the WhatsApp
// client as a utility message instead of being refused with 422.
func TestOccurrenceSendProtocol_DirectSend_OutsideWindow_SendsUtility(t *testing.T) {
	app, m, org, user, occ, contact := sendFixture(t, false)
	require.NoError(t, app.DB.Model(&models.WhatsAppAccount{}).
		Where("organization_id = ? AND name = ?", org.ID, contact.WhatsAppAccount).
		Update("direct_send_enabled", true).Error)

	req := sendProtocol(t, app, org.ID, user.ID, occ, nil)
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	require.Eventually(t, func() bool { return len(m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, "utility", m.sentMessages[0]["category"])
	assert.Equal(t, "text", m.sentMessages[0]["type"])
}

// Open window: even with Direct Send on, the conventional send is used.
func TestOccurrenceSendProtocol_DirectSend_InsideWindow_NoCategory(t *testing.T) {
	app, m, org, user, occ, contact := sendFixture(t, true)
	require.NoError(t, app.DB.Model(&models.WhatsAppAccount{}).
		Where("organization_id = ? AND name = ?", org.ID, contact.WhatsAppAccount).
		Update("direct_send_enabled", true).Error)

	req := sendProtocol(t, app, org.ID, user.ID, occ, nil)
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	require.Eventually(t, func() bool { return len(m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
	assert.NotContains(t, m.sentMessages[0], "category")
}

// Same gate on the reply endpoint; and without the flag it stays blocked.
func TestReplyToOccurrence_DirectSend_OutsideWindow(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		app, m, org, user, occ, contact := sendFixture(t, false)
		require.NoError(t, app.DB.Model(&models.WhatsAppAccount{}).
			Where("organization_id = ? AND name = ?", org.ID, contact.WhatsAppAccount).
			Update("direct_send_enabled", enabled).Error)

		req := testutil.NewJSONRequest(t, map[string]string{"content": "olá"})
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "id", occ.ID.String())
		require.NoError(t, app.ReplyToOccurrence(req))

		if !enabled {
			assert.Equal(t, fasthttp.StatusUnprocessableEntity, testutil.GetResponseStatusCode(req))
			assert.Empty(t, m.sentMessages)
			continue
		}
		assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
		require.Eventually(t, func() bool { return len(m.sentMessages) == 1 }, time.Second, 10*time.Millisecond)
		assert.Equal(t, "utility", m.sentMessages[0]["category"])
	}
}
