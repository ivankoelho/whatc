package whatsapp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider answer that is not a Meta error object (a proxy's HTML page, an echoed request, anything the other
// side decides to write) must never end up inside an error: errors are stored, logged and shown to people. A real
// Meta error keeps its code and message, and the classification (status + code) is untouched.

const leaky = `<html>gateway says token=EAABsecretTOKEN code=739104 phone=5571999990000</html>`

func serverAnswering(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func assertNoLeak(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for _, s := range []string{"EAABsecretTOKEN", "739104", "5571999990000", "gateway says", "<html>"} {
		assert.NotContains(t, err.Error(), s)
	}
}

func TestErrors_RawProviderBodiesNeverEnterTheErrorText(t *testing.T) {
	srv := serverAnswering(502, leaky)
	defer srv.Close()
	c := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)
	acc := &whatsapp.Account{PhoneID: "p", BusinessID: "b", APIVersion: "v21.0", AccessToken: "tok"}
	ctx := context.Background()

	_, err := c.SendTextMessage(ctx, acc, whatsapp.Recipient{Phone: "5571999990000"}, "oi")
	assertNoLeak(t, err)
	var api *whatsapp.APIError
	require.ErrorAs(t, err, &api)
	assert.Equal(t, 502, api.Status, "the status is kept for the classification")
	assert.Equal(t, whatsapp.OutcomeUnknown, whatsapp.Classify(err), "a 5xx stays unknown")

	_, err = c.UploadMedia(ctx, acc, []byte("x"), "image/png", "a.png")
	assertNoLeak(t, err)
	_, err = c.ExchangeCodeForToken(ctx, "code", "app", "secret", "v21.0")
	assertNoLeak(t, err)
	assertNoLeak(t, whatsapp.ParseMetaAPIError(418, []byte(leaky)))
}

func TestErrors_AMetaErrorObjectKeepsItsCodeAndMessage(t *testing.T) {
	srv := serverAnswering(400, `{"error":{"message":"Invalid parameter","code":100}}`)
	defer srv.Close()
	c := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)
	acc := &whatsapp.Account{PhoneID: "p", APIVersion: "v21.0", AccessToken: "tok"}

	_, err := c.SendTextMessage(context.Background(), acc, whatsapp.Recipient{Phone: "1"}, "oi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid parameter")
	var api *whatsapp.APIError
	require.ErrorAs(t, err, &api)
	assert.Equal(t, []int{400, 100}, []int{api.Status, api.Code})
	assert.Equal(t, whatsapp.OutcomeRefused, whatsapp.Classify(err), "a documented refusal is still a refusal")

	_, err = c.UploadMedia(context.Background(), acc, []byte("x"), "image/png", "a.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "media upload failed with status 400: API error 100: Invalid parameter")
}

func TestErrors_AnUnreadableCallAnswerDoesNotEchoItsBody(t *testing.T) {
	srv := serverAnswering(200, `{"x":"`+leaky+`"}`)
	defer srv.Close()
	c := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)
	_, err := c.InitiateCall(context.Background(), &whatsapp.Account{PhoneID: "p", APIVersion: "v18.0", AccessToken: "tok"}, whatsapp.Recipient{Phone: "1"}, "sdp")
	assertNoLeak(t, err)
	assert.Contains(t, err.Error(), "failed to parse call_id")
}

// A structured Meta message that repeats a credential is masked: the account access token on every call, and the
// authorization code and app secret on the token exchange. Phone numbers are personal data, not secrets, and stay.
func TestErrors_StructuredMessagesDoNotRepeatCredentials(t *testing.T) {
	const tok, code, secret = "EAABsecretTOKEN", "authcode98765", "appsecret4321"
	srv := serverAnswering(400, `{"error":{"message":"bad `+tok+` / `+code+` / `+secret+` for 5571999990000","code":190}}`)
	defer srv.Close()
	c := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)
	acc := &whatsapp.Account{PhoneID: "p", BusinessID: "b", APIVersion: "v21.0", AccessToken: tok}
	ctx := context.Background()

	_, err := c.SendTextMessage(ctx, acc, whatsapp.Recipient{Phone: "5571999990000"}, "oi")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok)
	assert.Contains(t, err.Error(), "5571999990000", "a phone number is not a secret")
	var api *whatsapp.APIError
	require.ErrorAs(t, err, &api)
	assert.Equal(t, []int{400, 190}, []int{api.Status, api.Code}, "classification inputs are untouched")

	_, err = c.UploadMedia(ctx, acc, []byte("x"), "image/png", "a.png")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), tok)

	_, err = c.ExchangeCodeForToken(ctx, code, "app", secret, "v21.0")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), code)
	assert.NotContains(t, err.Error(), secret)
}
