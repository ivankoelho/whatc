package handlers_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	wausage "github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

type apiFixture struct {
	app    *handlers.App
	org    *models.Organization
	reader *models.User // whatsapp_usage:read
	writer *models.User // read + write
	nobody *models.User // no permission
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	app := newTestApp(t)
	app.Usage = wausage.New(app.DB, config.UsageConfig{})
	app.Config.Usage = config.UsageConfig{}
	org := testutil.CreateTestOrganization(t, app.DB)
	mk := func(name string, keys []string) *models.User {
		role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, name+"-"+uuid.NewString()[:6], keys)
		return testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	}
	return &apiFixture{app: app, org: org,
		reader: mk("usage-reader", []string{"whatsapp_usage:read"}),
		writer: mk("usage-writer", []string{"whatsapp_usage:read", "whatsapp_usage:write"}),
		nobody: mk("usage-nobody", []string{"chat:read"})}
}

func (f *apiFixture) call(t *testing.T, h func(*fastglue.Request) error, u *models.User, body any, query map[string]string, path map[string]string) (int, []byte) {
	t.Helper()
	var req *fastglue.Request
	if body != nil {
		req = testutil.NewJSONRequest(t, body)
	} else {
		req = testutil.NewGETRequest(t)
	}
	testutil.SetAuthContext(req, f.org.ID, u.ID)
	for k, v := range query {
		testutil.SetQueryParam(req, k, v)
	}
	for k, v := range path {
		testutil.SetPathParam(req, k, v)
	}
	require.NoError(t, h(req))
	return testutil.GetResponseStatusCode(req), testutil.GetResponseBody(req)
}

func rateBody(country, category string, price float64, from, to string) map[string]any {
	b := map[string]any{"country": country, "category": category, "price": price, "currency": "BRL", "valid_from": from}
	if to != "" {
		b["valid_to"] = to
	}
	return b
}

func decodeData[T any](t *testing.T, body []byte) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Data
}

func TestWhatsAppUsageAPI_Permissions(t *testing.T) {
	f := newAPIFixture(t)
	id := uuid.NewString()
	reads := map[string]func(*fastglue.Request) error{
		"summary": f.app.GetWhatsAppUsageSummary, "messages": f.app.ListWhatsAppUsageMessages, "rates": f.app.ListWhatsAppRates}
	writes := map[string]func(*fastglue.Request) error{
		"create": f.app.CreateWhatsAppRate, "update": f.app.UpdateWhatsAppRate, "delete": f.app.DeleteWhatsAppRate, "reprice": f.app.RepriceWhatsAppUsage}

	for name, h := range reads {
		code, _ := f.call(t, h, f.nobody, nil, nil, nil)
		assert.Equal(t, fasthttp.StatusForbidden, code, "read %s without permission", name)
		code, _ = f.call(t, h, f.reader, nil, nil, nil)
		assert.Equal(t, fasthttp.StatusOK, code, "read %s with read", name)
	}
	for name, h := range writes {
		code, _ := f.call(t, h, f.reader, rateBody("55", "utility", 1, "2026-01-01", ""), nil, map[string]string{"id": id})
		assert.Equal(t, fasthttp.StatusForbidden, code, "the manager-like reader cannot %s", name)
		code, _ = f.call(t, h, f.nobody, rateBody("55", "utility", 1, "2026-01-01", ""), nil, map[string]string{"id": id})
		assert.Equal(t, fasthttp.StatusForbidden, code, "nobody can %s", name)
	}
}

func TestWhatsAppRatesAPI_CreateValidation(t *testing.T) {
	f := newAPIFixture(t)
	bad := map[string]map[string]any{
		"country":  rateBody("br", "utility", 1, "2026-01-01", ""),
		"category": rateBody("55", "Util ity", 1, "2026-01-01", ""),
		"price":    rateBody("55", "utility", -1, "2026-01-01", ""),
		"from":     rateBody("55", "utility", 1, "01/01/2026", ""),
		"order":    rateBody("55", "utility", 1, "2026-02-01", "2026-01-01"),
	}
	cur := rateBody("55", "utility", 1, "2026-01-01", "")
	cur["currency"] = "real"
	bad["currency"] = cur
	for name, body := range bad {
		code, _ := f.call(t, f.app.CreateWhatsAppRate, f.writer, body, nil, nil)
		assert.Equal(t, fasthttp.StatusBadRequest, code, name)
	}
	code, body := f.call(t, f.app.CreateWhatsAppRate, f.writer, rateBody("*", "Utility", 0.0068, "2026-01-01", ""), nil, nil)
	require.Equal(t, fasthttp.StatusOK, code, string(body))
	rate := decodeData[models.WhatsAppRate](t, body)
	assert.Equal(t, "utility", rate.Category, "the category is normalised")
	assert.Equal(t, &f.writer.ID, rate.CreatedBy)
}

func TestWhatsAppRatesAPI_OverlapIsRefusedAdjacentIsAllowed(t *testing.T) {
	f := newAPIFixture(t)
	create := func(country, cat, from, to string) int {
		code, _ := f.call(t, f.app.CreateWhatsAppRate, f.writer, rateBody(country, cat, 1, from, to), nil, nil)
		return code
	}
	require.Equal(t, 200, create("55", "utility", "2026-01-01", "2026-03-31"))
	assert.Equal(t, 409, create("55", "utility", "2026-03-31", ""), "shares the last day")
	assert.Equal(t, 409, create("55", "utility", "2025-12-01", "2026-01-01"), "shares the first day")
	assert.Equal(t, 409, create("55", "utility", "2026-02-01", "2026-02-15"), "inside")
	assert.Equal(t, 200, create("55", "utility", "2026-04-01", ""), "the next day is a new period")
	assert.Equal(t, 409, create("55", "utility", "2027-01-01", ""), "overlaps the open period")
	assert.Equal(t, 200, create("55", "marketing", "2026-01-01", ""), "another category")
	assert.Equal(t, 200, create("1", "utility", "2026-01-01", ""), "another country")
}

func TestWhatsAppRatesAPI_UsedRatesAreImmutableExceptClosingThePeriod(t *testing.T) {
	f := newAPIFixture(t)
	code, body := f.call(t, f.app.CreateWhatsAppRate, f.writer, rateBody("55", "utility", 0.1, "2026-01-01", ""), nil, nil)
	require.Equal(t, 200, code)
	rate := decodeData[models.WhatsAppRate](t, body)
	path := map[string]string{"id": rate.ID.String()}

	// unused: any edit
	code, _ = f.call(t, f.app.UpdateWhatsAppRate, f.writer, rateBody("55", "utility", 0.2, "2026-01-01", ""), nil, path)
	assert.Equal(t, 200, code)

	// a message is priced with it
	sent := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	require.NoError(t, f.app.DB.Create(&models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: "a", Direction: "outgoing",
		ActorType: "agent", Quantity: 1, SentAt: sent, BillingState: "priced", LinkState: "linked", RateID: &rate.ID}).Error)

	code, _ = f.call(t, f.app.UpdateWhatsAppRate, f.writer, rateBody("55", "utility", 0.3, "2026-01-01", ""), nil, path)
	assert.Equal(t, 409, code, "the price of a used rate cannot change")
	code, _ = f.call(t, f.app.UpdateWhatsAppRate, f.writer, rateBody("55", "utility", 0.2, "2026-01-05", ""), nil, path)
	assert.Equal(t, 409, code, "nor its start")
	code, _ = f.call(t, f.app.UpdateWhatsAppRate, f.writer, rateBody("55", "utility", 0.2, "2026-01-01", "2026-02-09"), nil, path)
	assert.Equal(t, 409, code, "it cannot be closed before the last message it priced")
	code, _ = f.call(t, f.app.UpdateWhatsAppRate, f.writer, rateBody("55", "utility", 0.2, "2026-01-01", "2026-02-28"), nil, path)
	assert.Equal(t, 200, code, "closing the period is allowed")
	code, _ = f.call(t, f.app.DeleteWhatsAppRate, f.writer, nil, nil, path)
	assert.Equal(t, 409, code, "a used rate cannot be deleted")

	// and the next period can now be registered
	code, _ = f.call(t, f.app.CreateWhatsAppRate, f.writer, rateBody("55", "utility", 0.25, "2026-03-01", ""), nil, nil)
	assert.Equal(t, 200, code)

	_, lb := f.call(t, f.app.ListWhatsAppRates, f.reader, nil, nil, nil)
	list := decodeData[struct {
		Rates []struct {
			ID    uuid.UUID `json:"id"`
			InUse bool      `json:"in_use"`
		} `json:"rates"`
	}](t, lb)
	require.Len(t, list.Rates, 2)
	for _, r := range list.Rates {
		assert.Equal(t, r.ID == rate.ID, r.InUse)
	}
}

func TestWhatsAppRatesAPI_DeleteUnusedAndOrganizationIsolation(t *testing.T) {
	f := newAPIFixture(t)
	_, body := f.call(t, f.app.CreateWhatsAppRate, f.writer, rateBody("55", "utility", 1, "2026-01-01", ""), nil, nil)
	rate := decodeData[models.WhatsAppRate](t, body)

	other := newAPIFixture(t) // another organization, its own writer
	code, _ := other.call(t, other.app.DeleteWhatsAppRate, other.writer, nil, nil, map[string]string{"id": rate.ID.String()})
	assert.Equal(t, 404, code, "another organization cannot see or delete it")
	code, _ = other.call(t, other.app.UpdateWhatsAppRate, other.writer, rateBody("55", "utility", 2, "2026-01-01", ""), nil, map[string]string{"id": rate.ID.String()})
	assert.Equal(t, 404, code)
	_, lb := other.call(t, other.app.ListWhatsAppRates, other.reader, nil, nil, nil)
	assert.NotContains(t, string(lb), rate.ID.String())

	code, _ = f.call(t, f.app.DeleteWhatsAppRate, f.writer, nil, nil, map[string]string{"id": rate.ID.String()})
	assert.Equal(t, 200, code)
	var n int64
	f.app.DB.Model(&models.WhatsAppRate{}).Where("id = ?", rate.ID).Count(&n)
	assert.Zero(t, n)
}

type seedRow struct {
	state, link, currency, category, actor string
	cost                                   *float64
	unit                                   *uuid.UUID
	agent                                  *uuid.UUID
	sent                                   time.Time
	createdAgo                             time.Duration
	declared                               string
	direction                              string
}

func (f *apiFixture) seed(t *testing.T, org uuid.UUID, rows ...seedRow) {
	t.Helper()
	for i, r := range rows {
		if r.link == "" {
			r.link = "linked"
		}
		if r.direction == "" {
			r.direction = "outgoing"
		}
		if r.actor == "" {
			r.actor = "agent"
		}
		if r.sent.IsZero() {
			r.sent = time.Now().Add(-time.Hour)
		}
		u := models.MessageUsage{OrganizationID: org, WhatsAppAccount: "acc", Wamid: "w-" + uuid.NewString()[:8], Direction: r.direction,
			ActorType: r.actor, ActorUserID: r.agent, UnitID: r.unit, Quantity: 1, SentAt: r.sent, BillingState: r.state, LinkState: r.link,
			EstimatedCost: r.cost, EstimatedCurrency: r.currency, BillingCategory: r.category, DeclaredCategory: r.declared, RecipientCountry: "55"}
		require.NoError(t, f.app.DB.Create(&u).Error, i)
		if r.createdAgo > 0 {
			require.NoError(t, f.app.DB.Model(&u).Update("created_at", time.Now().Add(-r.createdAgo)).Error)
		}
	}
}

func fp(v float64) *float64 { return &v }

func TestWhatsAppUsageAPI_SummaryCountsAndCostsPerCurrency(t *testing.T) {
	f := newAPIFixture(t)
	f.app.Config.Usage = config.UsageConfig{UnlinkedAttentionHours: func() *int { v := 2; return &v }()}
	other := testutil.CreateTestOrganization(t, f.app.DB)
	f.seed(t, f.org.ID,
		seedRow{state: "priced", cost: fp(0.10), currency: "BRL", category: "utility"},
		seedRow{state: "priced", cost: fp(0.20), currency: "BRL", category: "marketing"},
		seedRow{state: "priced", cost: fp(0.05), currency: "USD", category: "utility"},
		seedRow{state: "not_billable", cost: fp(0), currency: "", category: "service"},
		seedRow{state: "awaiting_pricing", declared: "UTILITY"},
		seedRow{state: "no_rate", category: "marketing"},
		seedRow{state: "unconfirmed", cost: fp(0)},
		seedRow{state: "failed", cost: fp(0)},
		seedRow{state: "send_failed", cost: fp(0)},
		seedRow{state: "pending"},
		seedRow{state: "pending", link: "unlinked", createdAgo: 5 * time.Hour},
		seedRow{state: "pending", link: "unlinked", createdAgo: time.Minute},
	)
	// another organization's rows never count, and an old row is outside the period
	f.seed(t, other.ID, seedRow{state: "priced", cost: fp(99), currency: "BRL", category: "utility"})
	f.seed(t, f.org.ID, seedRow{state: "priced", cost: fp(77), currency: "BRL", category: "utility", sent: time.Now().AddDate(0, 0, -90)})
	// a price for the provisional cost of the awaiting row
	require.NoError(t, f.app.DB.Create(&models.WhatsAppRate{OrganizationID: f.org.ID, Country: "55", Category: "utility",
		Price: 0.07, Currency: "BRL", ValidFrom: time.Now().UTC().AddDate(0, 0, -60)}).Error)

	code, body := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, nil, nil)
	require.Equal(t, 200, code, string(body))
	var raw struct {
		Data struct {
			Counts      map[string]int64 `json:"counts"`
			Costs       []map[string]any `json:"costs"`
			Provisional []map[string]any `json:"provisional_cost"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &raw))
	c := raw.Data.Counts
	assert.EqualValues(t, 12, c["total"])
	assert.EqualValues(t, 3, c["billed"])
	assert.EqualValues(t, 1, c["not_billable"])
	assert.EqualValues(t, 1, c["awaiting_pricing"])
	assert.EqualValues(t, 1, c["no_rate"])
	assert.EqualValues(t, 1, c["unconfirmed"])
	assert.EqualValues(t, 1, c["failed"])
	assert.EqualValues(t, 1, c["send_failed"])
	assert.EqualValues(t, 3, c["pending"])
	assert.EqualValues(t, 2, c["unlinked"])
	assert.EqualValues(t, 1, c["unlinked_attention"], "only the old unlinked one, by the configured hours")

	require.Len(t, raw.Data.Costs, 2, "one entry per currency, never added together")
	byCur := map[string]float64{}
	for _, e := range raw.Data.Costs {
		byCur[e["currency"].(string)] = e["estimated_cost"].(float64)
	}
	assert.InDelta(t, 0.30, byCur["BRL"], 1e-9)
	assert.InDelta(t, 0.05, byCur["USD"], 1e-9)

	require.Len(t, raw.Data.Provisional, 1)
	assert.InDelta(t, 0.07, raw.Data.Provisional[0]["estimated_cost"].(float64), 1e-9)
	var stored int64
	f.app.DB.Model(&models.MessageUsage{}).Where("organization_id = ? AND billing_state = 'awaiting_pricing' AND estimated_cost IS NOT NULL", f.org.ID).Count(&stored)
	assert.Zero(t, stored, "the provisional cost is never written to the ledger")
}

func TestWhatsAppUsageAPI_FiltersAndGroups(t *testing.T) {
	f := newAPIFixture(t)
	unitA := &models.Unit{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Loja Centro", Active: true}
	unitB := &models.Unit{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Loja Norte", Active: true}
	require.NoError(t, f.app.DB.Create(unitA).Error)
	require.NoError(t, f.app.DB.Create(unitB).Error)
	agent := testutil.CreateTestUser(t, f.app.DB, f.org.ID, testutil.WithFullName("Milena Souza"))
	d1 := time.Now().Add(-48 * time.Hour)
	f.seed(t, f.org.ID,
		seedRow{state: "priced", cost: fp(1), currency: "BRL", category: "utility", unit: &unitA.ID, agent: &agent.ID},
		seedRow{state: "priced", cost: fp(2), currency: "BRL", category: "marketing", unit: &unitA.ID, sent: d1},
		seedRow{state: "priced", cost: fp(4), currency: "BRL", category: "utility", unit: &unitB.ID},
		seedRow{state: "not_billable", cost: fp(0), direction: "incoming", actor: "contact"},
	)

	type groupsResp struct {
		Groups []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Messages int64  `json:"messages"`
			Costs    []struct {
				Currency      string  `json:"currency"`
				EstimatedCost float64 `json:"estimated_cost"`
			} `json:"costs"`
		} `json:"groups"`
	}
	get := func(q map[string]string) groupsResp {
		code, body := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, q, nil)
		require.Equal(t, 200, code, string(body))
		return decodeData[groupsResp](t, body)
	}

	byUnit := get(map[string]string{"group_by": "unit"})
	labels := map[string]float64{}
	for _, g := range byUnit.Groups {
		if len(g.Costs) > 0 {
			labels[g.Label] += g.Costs[0].EstimatedCost
		}
	}
	assert.InDelta(t, 3, labels["Loja Centro"], 1e-9)
	assert.InDelta(t, 4, labels["Loja Norte"], 1e-9)

	byAgent := get(map[string]string{"group_by": "agent"})
	var seen bool
	for _, g := range byAgent.Groups {
		if g.Label == "Milena Souza" {
			seen = true
			assert.EqualValues(t, 1, g.Messages)
		}
	}
	assert.True(t, seen)

	assert.Len(t, get(map[string]string{"group_by": "day"}).Groups, 2, "two different days")
	assert.Len(t, get(map[string]string{"group_by": "category"}).Groups, 3, "utility, marketing and the incoming service-less one")

	code, _ := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{"group_by": "name; DROP TABLE x"}, nil)
	assert.Equal(t, 400, code, "group_by is whitelisted")

	// filters narrow the same figures
	code, body := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{"unit_id": unitA.ID.String()}, nil)
	require.Equal(t, 200, code)
	var narrowed struct {
		Counts map[string]int64 `json:"counts"`
	}
	narrowed = decodeData[struct {
		Counts map[string]int64 `json:"counts"`
	}](t, body)
	assert.EqualValues(t, 2, narrowed.Counts["total"])

	for q, want := range map[string]int64{"direction=incoming": 1, "billing_state=priced": 3, "category=marketing": 1, "category=unknown": 1} {
		kv := strings.SplitN(q, "=", 2)
		_, b := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{kv[0]: kv[1]}, nil)
		got := decodeData[struct {
			Counts map[string]int64 `json:"counts"`
		}](t, b)
		assert.Equal(t, want, got.Counts["total"], q)
	}

	// a period filter excludes the older row
	day := time.Now().Format("2006-01-02")
	_, b := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{"from": day, "to": day}, nil)
	assert.EqualValues(t, 3, decodeData[struct {
		Counts map[string]int64 `json:"counts"`
	}](t, b).Counts["total"])
	code, _ = f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{"from": "x", "to": day}, nil)
	assert.Equal(t, 400, code)
	code, _ = f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{"from": day}, nil)
	assert.Equal(t, 400, code)
}

func TestWhatsAppUsageAPI_MessagesAreIdentifiersOnlyAndPaginated(t *testing.T) {
	f := newAPIFixture(t)
	other := testutil.CreateTestOrganization(t, f.app.DB)
	unit := &models.Unit{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Loja Sul", Active: true}
	require.NoError(t, f.app.DB.Create(unit).Error)
	rows := make([]seedRow, 0, 7)
	for i := 0; i < 7; i++ {
		rows = append(rows, seedRow{state: "priced", cost: fp(1), currency: "BRL", category: "utility", unit: &unit.ID,
			sent: time.Now().Add(-time.Duration(i+1) * time.Minute)})
	}
	f.seed(t, f.org.ID, rows...)
	f.seed(t, other.ID, seedRow{state: "priced", cost: fp(1), currency: "BRL"})

	code, body := f.call(t, f.app.ListWhatsAppUsageMessages, f.reader, nil, map[string]string{"limit": "5"}, nil)
	require.Equal(t, 200, code, string(body))
	data := decodeData[struct {
		Messages []map[string]any `json:"messages"`
		Total    int64            `json:"total"`
	}](t, body)
	assert.EqualValues(t, 7, data.Total, "only this organization")
	require.Len(t, data.Messages, 5)
	assert.Equal(t, "Loja Sul", data.Messages[0]["unit_name"])
	for _, k := range []string{"content", "phone_number", "phone", "body"} {
		_, has := data.Messages[0][k]
		assert.False(t, has, "no %s in a ledger row", k)
	}
	_, b2 := f.call(t, f.app.ListWhatsAppUsageMessages, f.reader, nil, map[string]string{"limit": "5", "page": "2"}, nil)
	assert.Len(t, decodeData[struct {
		Messages []map[string]any `json:"messages"`
	}](t, b2).Messages, 2)
}

func TestWhatsAppUsageAPI_RepriceReachesRowsWaitingForAPrice(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	acc := "acc-reprice"
	row := models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: acc, Wamid: "w-rp", Direction: "outgoing", ActorType: "agent",
		Quantity: 1, SentAt: time.Now().Add(-time.Hour), BillingState: "no_rate", LinkState: "linked", RecipientCountry: "55"}
	require.NoError(t, f.app.DB.Create(&row).Error)
	_, err := f.app.Usage.RecordStatusEvent(ctx, wausage.StatusEvent{OrganizationID: f.org.ID, WhatsAppAccount: acc, Wamid: "w-rp",
		Status: "delivered", EventAt: time.Now(), Pricing: &wausage.Pricing{Billable: true, PricingModel: "PMP", Category: "utility"}})
	require.NoError(t, err)

	code, _ := f.call(t, f.app.CreateWhatsAppRate, f.writer, rateBody("55", "utility", 0.12, time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02"), ""), nil, nil)
	require.Equal(t, 200, code)

	code, body := f.call(t, f.app.RepriceWhatsAppUsage, f.writer, map[string]any{}, nil, nil)
	require.Equal(t, 200, code, string(body))
	res := decodeData[wausage.RepriceResult](t, body)
	assert.Equal(t, 1, res.Processed)
	assert.Equal(t, 1, res.Changed)

	var got models.MessageUsage
	require.NoError(t, f.app.DB.First(&got, "id = ?", row.ID).Error)
	assert.Equal(t, "priced", got.BillingState)
	require.NotNil(t, got.EstimatedCost)
	assert.InDelta(t, 0.12, *got.EstimatedCost, 1e-9)
	assert.Nil(t, got.MetaCost)

	// nothing left to do the second time
	_, body = f.call(t, f.app.RepriceWhatsAppUsage, f.writer, map[string]any{}, nil, nil)
	assert.Equal(t, 0, decodeData[wausage.RepriceResult](t, body).Processed)
}

func TestWhatsAppUsageAPI_RepriceDoesNotTouchOtherOrganizationsOrStates(t *testing.T) {
	f := newAPIFixture(t)
	other := testutil.CreateTestOrganization(t, f.app.DB)
	for _, org := range []uuid.UUID{f.org.ID, other.ID} {
		require.NoError(t, f.app.DB.Create(&models.MessageUsage{OrganizationID: org, WhatsAppAccount: "a", Wamid: "w-" + uuid.NewString()[:6],
			Direction: "outgoing", ActorType: "agent", Quantity: 1, SentAt: time.Now(), BillingState: "no_rate", LinkState: "linked"}).Error)
	}
	require.NoError(t, f.app.DB.Create(&models.MessageUsage{OrganizationID: f.org.ID, WhatsAppAccount: "a", Wamid: "w-done",
		Direction: "outgoing", ActorType: "agent", Quantity: 1, SentAt: time.Now(), BillingState: "failed", LinkState: "linked"}).Error)

	_, body := f.call(t, f.app.RepriceWhatsAppUsage, f.writer, map[string]any{}, nil, nil)
	assert.Equal(t, 1, decodeData[wausage.RepriceResult](t, body).Processed, "only this organization's waiting rows")
}

func TestWhatsAppUsageAPI_RepriceWithRecordingOffIsRefused(t *testing.T) {
	f := newAPIFixture(t)
	off := false
	f.app.Usage = wausage.New(f.app.DB, config.UsageConfig{RecordEnabled: &off})
	code, _ := f.call(t, f.app.RepriceWhatsAppUsage, f.writer, map[string]any{}, nil, nil)
	assert.Equal(t, 409, code)
}

func TestWhatsAppUsageAPI_SummaryReportsTheActivationState(t *testing.T) {
	f := newAPIFixture(t)
	type resp struct {
		Recording struct {
			Enabled bool       `json:"enabled"`
			Since   *time.Time `json:"since"`
		} `json:"recording"`
	}
	_, body := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, nil, nil)
	r := decodeData[resp](t, body)
	assert.True(t, r.Recording.Enabled)
	assert.Nil(t, r.Recording.Since, "nothing was recorded yet: the screen can say so")

	f.seed(t, f.org.ID, seedRow{state: "pending"})
	_, body = f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, nil, nil)
	assert.NotNil(t, decodeData[resp](t, body).Recording.Since)

	off := false
	f.app.Usage = wausage.New(f.app.DB, config.UsageConfig{RecordEnabled: &off})
	_, body = f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, nil, nil)
	assert.False(t, decodeData[resp](t, body).Recording.Enabled)
}

// An empty list must be [] in the JSON, never null: the screens iterate over it.
func TestWhatsAppUsageAPI_EmptyListsAreArraysNotNull(t *testing.T) {
	f := newAPIFixture(t)
	_, rates := f.call(t, f.app.ListWhatsAppRates, f.reader, nil, nil, nil)
	assert.Contains(t, string(rates), `"rates":[]`)
	_, msgs := f.call(t, f.app.ListWhatsAppUsageMessages, f.reader, nil, nil, nil)
	assert.Contains(t, string(msgs), `"messages":[]`)
	_, sum := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, map[string]string{"group_by": "day"}, nil)
	assert.Contains(t, string(sum), `"groups":[]`)
	assert.Contains(t, string(sum), `"costs":[]`)
	assert.Contains(t, string(sum), `"provisional_cost":[]`)
}
