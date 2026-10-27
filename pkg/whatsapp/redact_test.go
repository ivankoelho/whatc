package whatsapp_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/logf"
)

const leakOTP = "123456"

func otpComponents(code string) []map[string]any {
	return []map[string]any{
		{"type": "body", "parameters": []map[string]any{{"type": "text", "text": code}}},
		{"type": "button", "sub_type": "url", "index": "0", "parameters": []map[string]any{{"type": "text", "text": code}}},
	}
}

// logCapture returns a client logging everything (debug included) to a buffer.
func logCapture(t *testing.T, handler http.HandlerFunc) (*whatsapp.Client, *bytes.Buffer, *whatsapp.Account, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	var buf bytes.Buffer
	log := logf.New(logf.Opts{Level: logf.DebugLevel, Writer: &buf})
	c := whatsapp.NewWithBaseURL(log, srv.URL)
	return c, &buf, &whatsapp.Account{PhoneID: "111", AccessToken: "tok", APIVersion: "v21.0"}, srv.Close
}

func metaError(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}
}

func TestClient_MetaErrorEchoingOTPIsRedactedBeforeReturnAndLog(t *testing.T) {
	body := `{"error":{"message":"(#131008) Invalid parameter 123456 in button","type":"OAuthException","code":131008,` +
		`"error_subcode":2018001,"error_user_msg":"Code 123456 is not valid.","error_data":{"details":"param=123456."},"fbtrace_id":"Abc123Trace"}}`
	c, logs, acct, closeSrv := logCapture(t, metaError(body))
	defer closeSrv()

	_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "codigo_acesso", "pt_BR", otpComponents(leakOTP))
	require.Error(t, err)

	assert.NotContains(t, err.Error(), leakOTP)
	assert.NotContains(t, logs.String(), leakOTP, "nothing the client logs may contain the code")
	assert.Contains(t, err.Error(), "API error 131008", "error code preserved")
	assert.Contains(t, err.Error(), whatsapp.SecretMask)
	assert.Contains(t, err.Error(), "Invalid parameter")
	assert.Contains(t, logs.String(), "131008", "the log keeps the diagnostic")
}

func TestClient_UnparseableErrorBodyIsRedacted(t *testing.T) {
	c, logs, acct, closeSrv := logCapture(t, metaError("gateway said: bad code 123456!"))
	defer closeSrv()

	_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR", otpComponents(leakOTP))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), leakOTP)
	assert.NotContains(t, logs.String(), leakOTP)
	assert.Contains(t, err.Error(), "status 400")
}

func TestClient_TransportErrorWithCodeInURLIsRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := logf.New(logf.Opts{Level: logf.DebugLevel, Writer: &buf})
	// Nothing listens on port 1; the dial error repeats the URL, which carries a code.
	c := whatsapp.NewWithBaseURL(log, "http://127.0.0.1:1/otp-"+leakOTP)
	acct := &whatsapp.Account{PhoneID: "111", AccessToken: "tok", APIVersion: "v21.0"}

	_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR", otpComponents(leakOTP))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), leakOTP)
	assert.NotContains(t, buf.String(), leakOTP)
}

func TestClient_ContextSecretsCoverTemplatesWithoutButtonParameter(t *testing.T) {
	c, logs, acct, closeSrv := logCapture(t, metaError(`{"error":{"message":"bad value 987654","code":100}}`))
	defer closeSrv()
	bodyOnly := []map[string]any{{"type": "body", "parameters": []map[string]any{{"type": "text", "text": "987654"}}}}

	// Without a declared secret nothing marks the body value as sensitive...
	_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR", bodyOnly)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "987654", "body values are not guessed to be secrets")

	// ...so callers that know (authentication templates) declare it.
	ctx := whatsapp.WithSecrets(context.Background(), "987654")
	_, err = c.SendTemplateMessage(ctx, acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR", bodyOnly)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "987654")
	assert.NotContains(t, logs.String()[strings.LastIndex(logs.String(), "Failed to send template message"):], "987654")
}

func TestClient_RedactionDoesNotMangleIdsOrCodes(t *testing.T) {
	// The code equals the numeric error code, and lookalike ids contain it.
	body := `{"error":{"message":"ref 91234567 and 123456 and 1234567, trace 123456abc","code":123456,"fbtrace_id":"X"}}`
	c, _, acct, closeSrv := logCapture(t, metaError(body))
	defer closeSrv()

	_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR", otpComponents("123456"))
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "API error 123456:", "numeric error code is structured data and stays")
	assert.Contains(t, msg, "91234567", "longer ids that merely contain the code stay")
	assert.Contains(t, msg, "1234567")
	assert.Contains(t, msg, "123456abc")
	assert.Equal(t, 1, strings.Count(msg, whatsapp.SecretMask), "only the standalone code is masked")
}

func TestClient_ErrorsWithoutSecretsAreUnchanged(t *testing.T) {
	c, _, acct, closeSrv := logCapture(t, metaError(`{"error":{"message":"Invalid phone number","code":100}}`))
	defer closeSrv()
	_, err := c.SendTemplateMessage(context.Background(), acct, whatsapp.Recipient{Phone: "5511999990000"}, "t", "pt_BR",
		[]map[string]any{{"type": "body", "parameters": []map[string]any{{"type": "text", "text": "Alice"}}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error 100: Invalid phone number")
}
