package handlers_test

import (
	"errors"
	"testing"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Integration regressions for the merge of the one-time-code protection with the case notifications.

// ExtraMetadata (used by case notifications) is copied after the message was sanitised. For an authentication
// send it must never reach the stored message, and no caller can plant the "masked" marker on any message.
func TestOutgoingMessage_ExtraMetadataCannotReintroduceTheCodeOrSpoofTheMarker(t *testing.T) {
	app := newTestApp(t)
	acct := &models.WhatsAppAccount{Name: "acc"}
	ct := &models.Contact{}

	auth := app.BuildOutgoingMessageForTest(handlers.OutgoingMessageRequest{
		Account: acct, Contact: ct,
		Type: models.MessageTypeTemplate,
		Template: &models.Template{Name: "codigo", DisplayName: "codigo", Category: "AUTHENTICATION",
			BodyContent: "Seu código é *{{1}}*."},
		BodyParams:    map[string]string{"1": testOTP},
		ExtraMetadata: models.JSONB{"code": testOTP, "otp": testOTP, "note": "x " + testOTP, handlers.MetadataAuthCodeMasked: false},
	})
	require.NotNil(t, auth)
	assert.NotContains(t, auth.Content, testOTP, "the stored text is masked")
	assert.Equal(t, true, auth.Metadata[handlers.MetadataAuthCodeMasked], "the marker is the server's, not the caller's")
	for k, v := range auth.Metadata {
		assert.NotContains(t, k, "otp")
		assert.NotEqual(t, testOTP, v, "key %q", k)
		if s, ok := v.(string); ok {
			assert.NotContains(t, s, testOTP, "key %q", k)
		}
	}
	assert.NotContains(t, auth.Metadata, "code")
	assert.NotContains(t, auth.Metadata, "note", "an authentication message takes no caller metadata at all")

	util := app.BuildOutgoingMessageForTest(handlers.OutgoingMessageRequest{
		Account: acct, Contact: ct,
		Type:          models.MessageTypeTemplate,
		Template:      &models.Template{Name: "aviso", DisplayName: "aviso", Category: "UTILITY", BodyContent: "Olá {{1}}"},
		BodyParams:    map[string]string{"1": "Ana"},
		ExtraMetadata: models.JSONB{"occurrence_id": "o1", "template_name": "forged", handlers.MetadataAuthCodeMasked: true},
	})
	require.NotNil(t, util)
	assert.Equal(t, "o1", util.Metadata["occurrence_id"], "ordinary links are kept")
	assert.Equal(t, "aviso", util.Metadata["template_name"], "a key the function owns is not overwritten")
	assert.NotContains(t, util.Metadata, handlers.MetadataAuthCodeMasked, "a non-authentication message cannot claim to be masked")
}

// The provider error contract after the merge: Meta's code decides what is proven; the unknown stays unknown;
// a raw response body is never what gets stored about a failed send.
func TestNotifier_ClassificationUsesTheProviderOutcomeAndNeverStoresRawBodies(t *testing.T) {
	status := func(err error) (string, bool, string) { return handlers.ClassifySendErrorForTest(err) }

	api := func(st, code int) error {
		return whatsapp.ParseMetaAPIError(st, []byte(`{"error":{"message":"m","code":`+itoa(code)+`}}`))
	}
	s, retry, reason := status(api(400, 100))
	assert.Equal(t, []any{models.NotificationFailed, false, "provider_rejected"}, []any{s, retry, reason}, "documented refusal")
	s, retry, reason = status(api(429, 130429))
	assert.Equal(t, []any{models.NotificationFailed, true, "provider_rate_limited"}, []any{s, retry, reason}, "documented throttle: retried")
	s, retry, reason = status(api(500, 2))
	assert.Equal(t, []any{models.NotificationUncertain, false, "provider_server_error"}, []any{s, retry, reason}, "a server fault proves nothing")
	s, retry, reason = status(api(400, 999999))
	assert.Equal(t, []any{models.NotificationUncertain, false, "provider_result_unknown"}, []any{s, retry, reason}, "an undocumented 4xx stays unknown: never resent on its own")
	s, _, _ = status(errors.New("connection reset"))
	assert.Equal(t, models.NotificationUncertain, s, "a transport failure is unknown")
	s, _, reason = status(whatsapp.NewNotSentError(errors.New("components could not be built")))
	assert.Equal(t, []any{models.NotificationFailed, "not_sent"}, []any{s, reason})

	raw := whatsapp.ParseMetaAPIError(502, []byte("<html>gateway secret=abc123 token EAAB-xyz</html>"))
	text := handlers.PublicSendErrorForTest(raw)
	assert.NotContains(t, text, "abc123")
	assert.NotContains(t, text, "EAAB-xyz")
	assert.Equal(t, "API returned status 502", text)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
