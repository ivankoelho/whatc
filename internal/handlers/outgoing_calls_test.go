package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// An account renamed after some contacts already snapshotted its old name
// (e.g. "[9600] - Atacadão dos Pisos" replacing plain "Atacadão dos Pisos"
// once a second line made the bare business name ambiguous) must still
// resolve for those contacts — an exact match alone leaves every one of
// them unable to call.
func TestResolveWhatsAppAccountByName_FallsBackToOldNameSubstring(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID,
		testutil.WithAccountName("[9600] - Atacadão dos Pisos"))

	// Current name: exact match still works.
	found, err := app.ResolveWhatsAppAccountByNameForTest(org.ID, "[9600] - Atacadão dos Pisos")
	require.NoError(t, err)
	assert.Equal(t, account.ID, found.ID)

	// Old, pre-rename name: exact match misses, substring fallback finds it.
	found, err = app.ResolveWhatsAppAccountByNameForTest(org.ID, "Atacadão dos Pisos")
	require.NoError(t, err)
	assert.Equal(t, account.ID, found.ID)
}

// A name that matches nothing, before or after the fallback, is a real
// not-found -- the fallback must not swallow a genuine miss.
func TestResolveWhatsAppAccountByName_NoMatchReturnsError(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID,
		testutil.WithAccountName("[9601] - Linha Sem Relacao"))

	_, err := app.ResolveWhatsAppAccountByNameForTest(org.ID, "Some Other Line")
	assert.Error(t, err)
}

// The fallback must stay scoped to the organization -- a name that only
// matches another org's account is still a miss.
func TestResolveWhatsAppAccountByName_ScopedToOrganization(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	otherOrg := testutil.CreateTestOrganization(t, app.DB)
	testutil.CreateTestWhatsAppAccountWith(t, app.DB, otherOrg.ID,
		testutil.WithAccountName("[9602] - Conta De Outra Organizacao"))

	_, err := app.ResolveWhatsAppAccountByNameForTest(org.ID, "Conta De Outra Organizacao")
	assert.Error(t, err)
}

// requireCallingEnabled's failure must stop the handler after writing
// exactly one response. Before the fix, SendErrorEnvelope's own return
// value (nil on a successful write) was returned directly instead of the
// errEnvelopeSent sentinel, so the caller's "if err != nil { return nil }"
// guard never fired and the handler kept running -- writing a second,
// success envelope on top of the error one. The client received two
// concatenated JSON bodies in one HTTP response, and any code trying to
// parse a single field out of it (e.g. an SDP string for setRemoteDescription)
// silently got garbage instead of a clean parse failure or a clean value.
// newTestApp leaves CallManager nil, so IsCallingEnabledForOrg is false by
// default -- no extra setup needed to hit the disabled path.
func TestInitiateOutgoingCall_DisabledOrgWritesExactlyOneEnvelope(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	adminRole := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&adminRole.ID))

	req := testutil.NewJSONRequest(t, map[string]any{
		"contact_id":       testutil.CreateTestContact(t, app.DB, org.ID).ID.String(),
		"whatsapp_account": "any-account",
		"sdp_offer":        "v=0\r\n",
	})
	testutil.SetAuthContext(req, org.ID, user.ID)

	require.NoError(t, app.InitiateOutgoingCall(req))
	assert.Equal(t, fasthttp.StatusServiceUnavailable, testutil.GetResponseStatusCode(req))

	body := testutil.GetResponseBody(req)

	var envelope struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	// A double-written response fails here: json.Unmarshal rejects trailing
	// non-whitespace content after the first complete JSON value, which is
	// exactly what a second, concatenated envelope would leave behind.
	require.NoError(t, json.Unmarshal(body, &envelope), "response body must be exactly one JSON value")
	assert.Equal(t, "error", envelope.Status)
	assert.Equal(t, "Calling is not enabled for this organization", envelope.Message)
}

func TestApp_GetCallPermission(t *testing.T) {
	tests := []struct {
		name           string
		callingEnabled bool
		metaStatus     int
		metaResponse   string
		wantStatus     string
		wantRequests   int64
	}{
		{
			name:         "disabled account skips Meta",
			metaStatus:   http.StatusBadRequest,
			metaResponse: `{"error":{"message":"Authentication Error","code":190}}`,
			wantStatus:   "unknown",
			wantRequests: 0,
		},
		{
			name:           "Meta authentication error returns unknown",
			callingEnabled: true,
			metaStatus:     http.StatusBadRequest,
			metaResponse:   `{"error":{"message":"Authentication Error","code":190}}`,
			wantStatus:     "unknown",
			wantRequests:   1,
		},
		{
			name:           "enabled account preserves permission status",
			callingEnabled: true,
			metaStatus:     http.StatusOK,
			metaResponse:   `{"permission":{"status":"temporary"}}`,
			wantStatus:     "temporary",
			wantRequests:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			org := testutil.CreateTestOrganization(t, app.DB)
			role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "call-reader", []string{"outgoing_calls:read"})
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
			account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, func(account *models.WhatsAppAccount) {
				account.BusinessCallingEnabled = tt.callingEnabled
			})
			contact := testutil.CreateTestContactWith(t, app.DB, org.ID,
				testutil.WithContactAccount(account.Name),
				testutil.WithPhoneNumber("15551234567"),
			)

			var requests atomic.Int64
			meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/"+account.APIVersion+"/"+account.PhoneID+"/call_permissions", r.URL.Path)
				assert.Equal(t, contact.PhoneNumber, r.URL.Query().Get("user_wa_id"))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.metaStatus)
				_, _ = w.Write([]byte(tt.metaResponse))
			}))
			t.Cleanup(meta.Close)
			app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, meta.URL)

			req := testutil.NewGETRequest(t)
			testutil.SetAuthContext(req, org.ID, user.ID)
			testutil.SetPathParam(req, "contactId", contact.ID.String())
			testutil.SetQueryParam(req, "whatsapp_account", account.Name)

			require.NoError(t, app.GetCallPermission(req))
			require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
			var response struct {
				Status string `json:"status"`
				Data   struct {
					Status string `json:"status"`
				} `json:"data"`
			}
			testutil.ParseJSONResponse(t, req, &response)
			assert.Equal(t, "success", response.Status)
			assert.Equal(t, tt.wantStatus, response.Data.Status)
			assert.Equal(t, tt.wantRequests, requests.Load())
		})
	}
}

// The access token is encrypted at rest: the call handlers must hand Meta the
// decrypted token, not the stored ciphertext.
func TestApp_GetCallPermission_SendsDecryptedAccessToken(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "call-reader", []string{"outgoing_calls:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))

	const plainToken = "plain-access-token"
	encrypted, err := crypto.Encrypt(plainToken, app.Config.App.EncryptionKey)
	require.NoError(t, err)
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, func(account *models.WhatsAppAccount) {
		account.BusinessCallingEnabled = true
		account.AccessToken = encrypted
	})
	contact := testutil.CreateTestContactWith(t, app.DB, org.ID,
		testutil.WithContactAccount(account.Name),
		testutil.WithPhoneNumber("15551234567"),
	)

	var gotAuth atomic.Value
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"permission":{"status":"temporary"}}`))
	}))
	t.Cleanup(meta.Close)
	app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, meta.URL)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "contactId", contact.ID.String())
	testutil.SetQueryParam(req, "whatsapp_account", account.Name)

	require.NoError(t, app.GetCallPermission(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	assert.Equal(t, "Bearer "+plainToken, gotAuth.Load())
}
