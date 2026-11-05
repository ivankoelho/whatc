package whatsapp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The preflight runs immediately before the request is handed to the HTTP client. When it refuses, no request is
// made at all and the failure is classified as "never sent" (a proven failure), never as an unknown outcome.
func TestPreflight_ARefusalMakesNoRequest_AndIsAProvenNotSent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.X"}]}`))
	}))
	defer srv.Close()
	client := whatsapp.NewWithBaseURL(testutil.NopLogger(), srv.URL)
	acct := &whatsapp.Account{PhoneID: "1", AccessToken: "t", APIVersion: "v24.0"}
	rcpt := whatsapp.Recipient{Phone: "5511999990000"}

	refused := errors.New("past the deadline")
	ctx := whatsapp.WithPreflight(context.Background(), func(context.Context) error { return refused })
	_, err := client.SendTextMessage(ctx, acct, rcpt, "oi")
	require.Error(t, err)
	assert.ErrorIs(t, err, refused)
	assert.True(t, whatsapp.NotSent(err), "a preflight refusal is a proven 'not sent'")
	assert.EqualValues(t, 0, calls.Load(), "no request reached the server")

	// An allowing preflight changes nothing.
	var asked atomic.Int32
	ok := whatsapp.WithPreflight(context.Background(), func(context.Context) error { asked.Add(1); return nil })
	id, err := client.SendTextMessage(ok, acct, rcpt, "oi")
	require.NoError(t, err)
	assert.Equal(t, "wamid.X", id)
	assert.EqualValues(t, 1, asked.Load(), "asked exactly once, right before the request")
	assert.EqualValues(t, 1, calls.Load())

	// And with no preflight at all nothing is asked.
	_, err = client.SendTextMessage(context.Background(), acct, rcpt, "oi")
	require.NoError(t, err)
	assert.EqualValues(t, 1, asked.Load())
	assert.EqualValues(t, 2, calls.Load())
}
