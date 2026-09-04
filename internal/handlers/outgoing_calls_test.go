package handlers_test

import (
	"encoding/json"
	"testing"

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
