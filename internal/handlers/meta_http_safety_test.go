package handlers

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEC-1 for the two places that call Meta with the shared app HTTP client: the token must not follow a
// redirect, and an error must not return the URL.

func TestFetchMetaJSON_DoesNotFollowRedirectsAndKeepsTheURLOutOfErrors(t *testing.T) {
	var evilHits atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { evilHits.Add(1) }))
	t.Cleanup(evil.Close)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(meta.Close)
	a := &App{HTTPClient: &http.Client{}}

	_, status, _ := a.fetchMetaJSON(meta.URL+"/v24.0/PHONEID-123", "EAAG-token")
	assert.Equal(t, http.StatusTemporaryRedirect, status)
	assert.EqualValues(t, 0, evilHits.Load(), "the redirect target was never contacted")

	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/v24.0/PHONEID-123"
	dead.Close()
	_, _, err := a.fetchMetaJSON(url, "EAAG-token")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "PHONEID-123", "the URL (and the internal id in it) stays out of the error")
	assert.NotContains(t, err.Error(), "EAAG-token")
}
