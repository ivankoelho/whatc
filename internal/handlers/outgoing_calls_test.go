package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
