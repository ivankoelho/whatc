package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func (e callsEnv) listConfirmations(t *testing.T, org, user uuid.UUID, query map[string]string) (int, []byte) {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org, user)
	for k, v := range query {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, e.app.ListAIToolConfirmations(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

type confirmationsPage struct {
	Data struct {
		Confirmations []map[string]any `json:"confirmations"`
		Total         int              `json:"total"`
		Limit         int              `json:"limit"`
	} `json:"data"`
}

func decodeConfirmations(t *testing.T, body []byte) confirmationsPage {
	t.Helper()
	var p confirmationsPage
	require.NoError(t, json.Unmarshal(body, &p))
	return p
}

func (e callsEnv) seedConfirmation(t *testing.T, org uuid.UUID, mut func(*models.AIToolConfirmation)) models.AIToolConfirmation {
	t.Helper()
	now := time.Now()
	c := models.AIToolConfirmation{
		OrganizationID: org, ContactID: uuid.New(), SessionID: uuid.New(), RunID: uuid.New(), ToolName: "request_agent_transfer", Risk: "write",
		TokenHash: "SENTINEL-HASH-" + uuid.NewString(), Args: models.JSONB{"reason": "SENTINEL-REASON"}, ActionDigest: "SENTINEL-DIGEST",
		Status: models.AIConfirmationPending, ProposedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		ConfirmWAMID: "SENTINEL-WAMID", ReconcileAttempts: 2,
	}
	if mut != nil {
		mut(&c)
	}
	require.NoError(t, e.app.DB.Create(&c).Error)
	return c
}

func TestAIToolConfirmationsAPI_ShowsWhereEachProposalStandsAndNeverTheSecrets(t *testing.T) {
	e := newCallsEnv(t)
	tid := uuid.New()
	confirmedAt := time.Now()
	e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) {
		c.Status, c.Outcome, c.TransferID, c.ConfirmedAt, c.FinishedAt = models.AIConfirmationExecuted, "created", &tid, &confirmedAt, &confirmedAt
	})

	status, body := e.listConfirmations(t, e.orgA.ID, e.adminA.ID, nil)
	require.Equal(t, fasthttp.StatusOK, status)
	for _, secret := range []string{"SENTINEL-HASH", "SENTINEL-REASON", "SENTINEL-DIGEST", "SENTINEL-WAMID", "token", "digest", "args", "reason", "wamid", "reconcile", "Opaque"} {
		assert.NotContains(t, string(body), secret)
	}
	p := decodeConfirmations(t, body)
	require.Len(t, p.Data.Confirmations, 1)
	var got []string
	for k := range p.Data.Confirmations[0] {
		got = append(got, k)
	}
	assert.ElementsMatch(t, []string{
		"id", "tool_name", "status", "outcome", "proposed_at", "expires_at", "confirmed_at", "finished_at",
		"subject_contact_id", "session_id", "run_id", "transfer_id",
	}, got, "exactly the documented metadata")
	assert.Equal(t, "executed", p.Data.Confirmations[0]["status"])
	assert.Equal(t, tid.String(), p.Data.Confirmations[0]["transfer_id"])
}

func TestAIToolConfirmationsAPI_PermissionsIsolationFiltersAndPagination(t *testing.T) {
	e := newCallsEnv(t)
	now := time.Now()
	contact := uuid.New()
	mine := e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) { c.ContactID = contact; c.ProposedAt = now.Add(-72 * time.Hour) })
	e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) {
		c.Status, c.Outcome, c.ProposedAt = models.AIConfirmationNotExecuted, "outside_hours", now.Add(-48*time.Hour)
	})
	e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) {
		c.Status, c.ProposedAt = models.AIConfirmationConfirmed, now.Add(-24*time.Hour)
	})
	e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) { c.Status, c.ProposedAt = models.AIConfirmationDeclined, now })
	theirs := e.seedConfirmation(t, e.orgB.ID, nil)

	// permissions
	for name, u := range map[string]uuid.UUID{"no permission": e.bad.ID, "write only": e.writer.ID} {
		status, _ := e.listConfirmations(t, e.orgA.ID, u, nil)
		assert.Equal(t, fasthttp.StatusForbidden, status, name)
	}
	status, _ := e.listConfirmations(t, e.orgA.ID, e.reader.ID, nil)
	assert.Equal(t, fasthttp.StatusOK, status, "ai_tools:read is enough")

	// isolation
	_, body := e.listConfirmations(t, e.orgA.ID, e.adminA.ID, nil)
	assert.NotContains(t, string(body), theirs.ID.String())
	_, body = e.listConfirmations(t, e.orgB.ID, e.adminB.ID, map[string]string{"contact_id": contact.String()})
	assert.Equal(t, 0, decodeConfirmations(t, body).Data.Total, "A's customer finds nothing in B")
	status, _ = e.listConfirmations(t, e.orgB.ID, e.adminA.ID, nil)
	assert.Equal(t, fasthttp.StatusForbidden, status)

	total := func(q map[string]string) int {
		status, body := e.listConfirmations(t, e.orgA.ID, e.adminA.ID, q)
		require.Equal(t, fasthttp.StatusOK, status, "%v", q)
		return decodeConfirmations(t, body).Data.Total
	}
	assert.Equal(t, 4, total(nil))
	assert.Equal(t, 1, total(map[string]string{"status": "confirmed"}), "the ones whose outcome is unknown are easy to find")
	assert.Equal(t, 1, total(map[string]string{"status": "declined"}))
	assert.Equal(t, 1, total(map[string]string{"outcome": "outside_hours"}))
	assert.Equal(t, 1, total(map[string]string{"contact_id": contact.String()}))
	assert.Equal(t, 2, total(map[string]string{"from": now.Add(-30 * time.Hour).Format(time.RFC3339)}))
	assert.Equal(t, 3, total(map[string]string{"to": now.Add(-12 * time.Hour).Format(time.RFC3339)}))

	// newest first, with a ceiling on the page
	_, body = e.listConfirmations(t, e.orgA.ID, e.adminA.ID, map[string]string{"limit": "1"})
	assert.Equal(t, "declined", decodeConfirmations(t, body).Data.Confirmations[0]["status"])
	_, body = e.listConfirmations(t, e.orgA.ID, e.adminA.ID, map[string]string{"limit": "100000"})
	assert.LessOrEqual(t, decodeConfirmations(t, body).Data.Limit, 100)
	assert.Contains(t, string(body), mine.ID.String())

	for _, q := range []map[string]string{{"status": "bogus"}, {"contact_id": "nope"}, {"from": "yesterday"}, {"to": "x"}} {
		status, _ := e.listConfirmations(t, e.orgA.ID, e.adminA.ID, q)
		assert.Equal(t, fasthttp.StatusBadRequest, status, "%v", q)
	}
}

func TestAIToolConfirmationsAPI_TimeFiltersAreUTC(t *testing.T) {
	e := newCallsEnv(t)
	before := e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) { c.ProposedAt = time.Date(2026, 10, 2, 23, 30, 0, 0, time.UTC) })
	after := e.seedConfirmation(t, e.orgA.ID, func(c *models.AIToolConfirmation) { c.ProposedAt = time.Date(2026, 10, 3, 0, 30, 0, 0, time.UTC) })
	ids := func(q map[string]string) []string {
		_, body := e.listConfirmations(t, e.orgA.ID, e.adminA.ID, q)
		var out []string
		for _, c := range decodeConfirmations(t, body).Data.Confirmations {
			out = append(out, c["id"].(string))
		}
		return out
	}
	assert.Equal(t, []string{after.ID.String()}, ids(map[string]string{"from": "2026-10-03"}))
	assert.Equal(t, []string{before.ID.String()}, ids(map[string]string{"to": "2026-10-02"}))
	assert.Equal(t, []string{after.ID.String()}, ids(map[string]string{"from": "2026-10-02T21:00:00-03:00"}))
}
