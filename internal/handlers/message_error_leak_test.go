package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What is STORED about a failed send (and then shown in the conversation and pushed over the websocket) is
// never a raw provider answer: a proxy page or an echoed request must not reach the database, the screen or the
// logs of the message row. A Meta error keeps its message.
func TestFailedSend_StoredErrorNeverCarriesARawProviderBody(t *testing.T) {
	mockServer := newMockWhatsAppServer()
	defer mockServer.close()
	app := newMsgTestApp(t, mockServer)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := createTestAccount(t, app, org.ID)
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID, testutil.WithContactAccount(account.Name))

	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`<html>gateway says token=EAABsecretTOKEN code=739104 to 5571999990000</html>`))
	}))
	defer raw.Close()
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), raw.URL)

	msg, err := app.SendOutgoingMessage(testutil.TestContext(t), handlers.OutgoingMessageRequest{
		Account: account, Contact: contact, Type: models.MessageTypeText, Content: "Oi",
	}, handlers.ChatbotSendOptions())
	require.NoError(t, err)
	app.WaitForBackgroundTasks()

	var got models.Message
	require.NoError(t, app.DB.First(&got, msg.ID).Error)
	assert.Equal(t, models.MessageStatusFailed, got.Status)
	for _, s := range []string{"EAABsecretTOKEN", "739104", "5571999990000", "gateway says", "<html>"} {
		assert.NotContains(t, got.ErrorMessage, s)
	}
	assert.Contains(t, got.ErrorMessage, "502", "the status is what remains")
}
