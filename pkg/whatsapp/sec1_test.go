package whatsapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEC-1: credentials never reach a host that is not Meta's, and never come back in an error.

func sec1Client(base string) *whatsapp.Client {
	return whatsapp.NewWithBaseURL(testutil.NopLogger(), base)
}

// a server that is already closed: every request fails at connect, and the URL is in the Go error.
func deadServerURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

// counter records every request a server sees, with the Authorization header it carried.
type counter struct {
	n    atomic.Int32
	auth atomic.Value
}

func newCounter(t *testing.T) (*httptest.Server, *counter) {
	t.Helper()
	c := &counter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.n.Add(1)
		c.auth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestSEC1_TokenExchangeTransportErrorDoesNotLeakSecretOrCode(t *testing.T) {
	t.Parallel()
	client := sec1Client(deadServerURL(t))
	const code, secret = "AQD-auth-code-9f8e7d6c", "app-secret-0123456789abcdef"

	_, err := client.ExchangeCodeForToken(testutil.TestContext(t), code, "123456789", secret, "v24.0")
	require.Error(t, err)
	for _, leak := range []string{code, secret, "client_secret", "client_id", "oauth/access_token"} {
		assert.NotContains(t, err.Error(), leak, "the error text must not carry %q", leak)
	}
}

func TestSEC1_TransportErrorDropsTheRequestURLAndKeepsTheClassification(t *testing.T) {
	t.Parallel()
	client := sec1Client(deadServerURL(t))
	acct := &whatsapp.Account{PhoneID: "PHONEID-777000111", APIVersion: "v24.0", AccessToken: "EAAG-token-xyz"}

	_, err := client.SendTextMessage(testutil.TestContext(t), acct, whatsapp.Recipient{Phone: "5511999990000"}, "oi")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "PHONEID-777000111", "the internal phone id must not appear")
	assert.NotContains(t, err.Error(), "/messages")
	assert.NotContains(t, err.Error(), "EAAG-token-xyz")
	assert.True(t, whatsapp.NotSent(err), "a connection that never opened is still a proven 'not sent'")
}

func TestSEC1_DownloadToAnUntrustedHostIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	evil, seen := newCounter(t)
	client := whatsapp.NewWithTimeout(testutil.NopLogger(), 5*time.Second) // production defaults: only Meta's hosts

	for _, target := range []string{evil.URL + "/steal", "http://graph.facebook.com.evil.example/x", "https://example.com/x", "ftp://scontent.xx.fbcdn.net/x", "/relative"} {
		_, err := client.DownloadMedia(testutil.TestContext(t), target, "EAAG-token-xyz")
		require.Error(t, err, target)
		assert.True(t, errors.Is(err, whatsapp.ErrUntrustedDownloadHost), "%s: %v", target, err)
	}
	assert.EqualValues(t, 0, seen.n.Load(), "no request, hence no token, reached the untrusted server")
}

func TestSEC1_DownloadRedirectToAnUntrustedHostCarriesNoToken(t *testing.T) {
	t.Parallel()
	evil, seen := newCounter(t)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/collect", http.StatusFound)
	}))
	t.Cleanup(meta.Close)
	client := sec1Client(meta.URL) // meta stands in for Meta's own host

	_, err := client.DownloadMedia(testutil.TestContext(t), meta.URL+"/media", "EAAG-token-xyz")
	require.Error(t, err)
	assert.True(t, errors.Is(err, whatsapp.ErrUntrustedDownloadHost), "%v", err)
	assert.EqualValues(t, 0, seen.n.Load(), "the redirect target must never be contacted")
}

func TestSEC1_APICallsNeverFollowRedirects(t *testing.T) {
	t.Parallel()
	evil, seen := newCounter(t)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/replay", http.StatusTemporaryRedirect) // 307 would replay method and body
	}))
	t.Cleanup(meta.Close)
	client := sec1Client(meta.URL)
	acct := &whatsapp.Account{PhoneID: "PH", APIVersion: "v24.0", AccessToken: "EAAG-token-xyz"}

	_, err := client.SendTextMessage(testutil.TestContext(t), acct, whatsapp.Recipient{Phone: "5511999990000"}, "oi")
	require.Error(t, err)
	assert.EqualValues(t, 0, seen.n.Load(), "the API client must not replay the request elsewhere")
	assert.False(t, whatsapp.NotSent(err), "a redirect proves nothing about delivery: the outcome stays unknown")
}

func TestSEC1_FlowAssetDownloadToAnUntrustedHostIsRefused(t *testing.T) {
	t.Parallel()
	evil, seen := newCounter(t)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"name": "flow.json", "asset_type": "FLOW_JSON", "download_url": evil.URL + "/flow.json"}}})
	}))
	t.Cleanup(meta.Close)
	client := sec1Client(meta.URL)
	acct := &whatsapp.Account{PhoneID: "PH", APIVersion: "v24.0", AccessToken: "EAAG-token-xyz"}

	_, err := client.GetFlowAssets(context.Background(), acct, "flow-1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, whatsapp.ErrUntrustedDownloadHost), "%v", err)
	assert.EqualValues(t, 0, seen.n.Load())
}

func TestSEC1_TrustedMetaHostsAreAccepted(t *testing.T) {
	t.Parallel()
	// the check itself (no network): Meta's CDN hosts pass, look-alikes do not
	client := whatsapp.NewWithTimeout(testutil.NopLogger(), time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a trusted host fails later (canceled context), never with ErrUntrustedDownloadHost
	for _, ok := range []string{"https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=1", "https://scontent.xx.fbcdn.net/v/x", "https://mmg.whatsapp.net/x", "https://graph.facebook.com/v24.0/1"} {
		_, err := client.DownloadMedia(ctx, ok, "t")
		require.Error(t, err)
		assert.False(t, errors.Is(err, whatsapp.ErrUntrustedDownloadHost), "%s was wrongly refused: %v", ok, err)
	}
	for _, bad := range []string{"https://fbcdn.net.evil.example/x", "https://evilfbcdn.net/x", "https://facebook.com.evil.example/x", "https://notwhatsapp.net/x"} {
		_, err := client.DownloadMedia(ctx, bad, "t")
		assert.True(t, errors.Is(err, whatsapp.ErrUntrustedDownloadHost), "%s must be refused: %v", bad, err)
	}
	_ = strings.TrimSpace
}

func TestDeliveryUnknown_OnlyForNetworkAndUnprovableAnswers(t *testing.T) {
	t.Parallel()
	// a proven refusal and a request that never left are NOT unknown
	assert.False(t, whatsapp.DeliveryUnknown(whatsapp.NewNotSentError(errors.New("template is required"))))
	assert.False(t, whatsapp.DeliveryUnknown(errors.New("unsupported message type: x")), "validation errors before any request keep their own text")
	assert.False(t, whatsapp.DeliveryUnknown(nil))

	// a dropped connection or a timeout IS unknown
	client := sec1Client(deadServerURL(t))
	acct := &whatsapp.Account{PhoneID: "PH", APIVersion: "v24.0", AccessToken: "t"}
	_, err := client.SendTextMessage(testutil.TestContext(t), acct, whatsapp.Recipient{Phone: "5511999990000"}, "oi")
	assert.False(t, whatsapp.DeliveryUnknown(err), "never connected: proven not sent")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(srv.Close)
	_, err = sec1Client(srv.URL).SendTextMessage(testutil.TestContext(t), acct, whatsapp.Recipient{Phone: "5511999990000"}, "oi")
	assert.True(t, whatsapp.DeliveryUnknown(err), "a 502 proves nothing about delivery")
}

func TestSEC1_TokenExchangeRequestConstructionErrorDoesNotLeak(t *testing.T) {
	t.Parallel()
	client := whatsapp.NewWithBaseURL(testutil.NopLogger(), "http://exa mple.invalid") // does not parse: net/http echoes the whole URL
	const code, secret = "AQD-auth-code-9f8e7d6c", "app-secret-0123456789abcdef"

	_, err := client.ExchangeCodeForToken(testutil.TestContext(t), code, "123456789", secret, "v24.0")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), code)
	assert.NotContains(t, err.Error(), "client_secret")
}

func TestSEC1_TokenExchangeEscapesTheQuery(t *testing.T) {
	t.Parallel()
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.URL.Query())
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok", "token_type": "bearer"})
	}))
	t.Cleanup(srv.Close)

	_, err := sec1Client(srv.URL).ExchangeCodeForToken(testutil.TestContext(t), "x&client_secret=evil", "app", "real-secret", "v24.0")
	require.NoError(t, err)
	q := got.Load().(url.Values)
	assert.Equal(t, []string{"x&client_secret=evil"}, q["code"], "the code is one value, whatever it contains")
	assert.Equal(t, []string{"real-secret"}, q["client_secret"], "a hostile code cannot override the secret")
}

func TestSEC1_DownloadFollowsARedirectBetweenTrustedHostsOnly(t *testing.T) {
	t.Parallel()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("bytes")) }))
	t.Cleanup(cdn.Close)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/blob", http.StatusFound)
	}))
	t.Cleanup(meta.Close)

	client := sec1Client(meta.URL)
	client.TrustHost(cdn.Listener.Addr().String()) // a trusted CDN host: the chain is allowed
	data, err := client.DownloadMedia(testutil.TestContext(t), meta.URL+"/media", "tok")
	require.NoError(t, err)
	assert.Equal(t, []byte("bytes"), data, "a legitimate redirect chain still works")
}

func TestSEC1_NoRedirectClientHandsBackThe3xx(t *testing.T) {
	t.Parallel()
	evil, seen := newCounter(t)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(meta.Close)

	resp, err := whatsapp.NoRedirectClient(&http.Client{}).Get(meta.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
	assert.EqualValues(t, 0, seen.n.Load())
}
