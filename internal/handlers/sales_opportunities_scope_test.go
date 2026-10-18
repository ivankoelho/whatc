package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Unified sales screen: admin/SAC supervisor (view_all) see everything, a
// store supervisor (view_team) sees the sellers who share a team with them,
// a seller sees only their own — in the list, the detail and the summary.

type scopeFixture struct {
	app                          *handlers.App
	orgID                        uuid.UUID
	seller, teammate, outsider   models.User
	supervisor                   models.User
	unit                         models.Unit
	oppSeller, oppMate, oppOther models.SalesOpportunity
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	sellerRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "seller", []string{"sales_opportunities:read", "sales_opportunities:write"})
	supRole := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "store-supervisor",
		[]string{"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_team"})

	f := scopeFixture{app: app, orgID: org.ID}
	f.seller = *testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&sellerRole.ID))
	f.teammate = *testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&sellerRole.ID))
	f.outsider = *testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&sellerRole.ID))
	f.supervisor = *testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&supRole.ID))

	f.unit = models.Unit{OrganizationID: org.ID, Name: "SERRINHA"}
	require.NoError(t, app.DB.Create(&f.unit).Error)
	team := models.Team{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Serrinha Vendas " + uuid.NewString()[:6],
		IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin, UnitID: &f.unit.ID,
	}
	require.NoError(t, app.DB.Create(&team).Error)
	for _, u := range []models.User{f.seller, f.teammate, f.supervisor} {
		require.NoError(t, app.DB.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()}, TeamID: team.ID, UserID: u.ID, Role: models.TeamRoleAgent}).Error)
	}

	mk := func(n string, owner models.User, status models.SalesOpportunityStatus, value float64) models.SalesOpportunity {
		c := testutil.CreateTestContact(t, app.DB, org.ID)
		o := models.SalesOpportunity{
			OrganizationID: org.ID, ContactID: c.ID, OpportunityNumber: "OPP-SCOPE-" + n,
			Stage: models.SalesOpportunityStagePotencial, Status: status,
			AssignedUserID: &owner.ID, StageChangedAt: time.Now(), EstimatedValue: &value,
		}
		now := time.Now()
		if status == models.SalesOpportunityStatusConvertida {
			o.ConvertedAt = &now
		}
		require.NoError(t, app.DB.Create(&o).Error)
		return o
	}
	f.oppSeller = mk("1", f.seller, models.SalesOpportunityStatusAberta, 100)
	f.oppMate = mk("2", f.teammate, models.SalesOpportunityStatusConvertida, 200)
	f.oppOther = mk("3", f.outsider, models.SalesOpportunityStatusAberta, 400)
	return f
}

func listIDs(t *testing.T, f scopeFixture, user models.User, params map[string]string) []uuid.UUID {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, f.orgID, user.ID)
	for k, v := range params {
		testutil.SetQueryParam(req, k, v)
	}
	require.NoError(t, f.app.ListSalesOpportunities(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data struct {
			Opportunities []models.SalesOpportunity `json:"opportunities"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	ids := make([]uuid.UUID, 0, len(resp.Data.Opportunities))
	for _, o := range resp.Data.Opportunities {
		ids = append(ids, o.ID)
	}
	return ids
}

func TestSalesScope_ViewTeamSeesTeamNotOutsiders(t *testing.T) {
	f := newScopeFixture(t)
	ids := listIDs(t, f, f.supervisor, nil)
	assert.ElementsMatch(t, []uuid.UUID{f.oppSeller.ID, f.oppMate.ID}, ids)

	// Seller is unchanged: own only.
	assert.ElementsMatch(t, []uuid.UUID{f.oppSeller.ID}, listIDs(t, f, f.seller, nil))
}

func TestSalesScope_UnitFilterUsesTheTeamsStore(t *testing.T) {
	f := newScopeFixture(t)
	ids := listIDs(t, f, f.supervisor, map[string]string{"unit_id": f.unit.ID.String()})
	assert.ElementsMatch(t, []uuid.UUID{f.oppSeller.ID, f.oppMate.ID}, ids)
}

func TestSalesScope_DetailFollowsTeamScope(t *testing.T) {
	f := newScopeFixture(t)
	get := func(id uuid.UUID) int {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, f.orgID, f.supervisor.ID)
		testutil.SetPathParam(req, "id", id.String())
		require.NoError(t, f.app.GetSalesOpportunity(req))
		return testutil.GetResponseStatusCode(req)
	}
	assert.Equal(t, fasthttp.StatusOK, get(f.oppMate.ID))
	assert.Equal(t, fasthttp.StatusForbidden, get(f.oppOther.ID))
}

func summary(t *testing.T, f scopeFixture, user models.User) map[string]float64 {
	t.Helper()
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, f.orgID, user.ID)
	require.NoError(t, f.app.GetSalesOpportunitiesSummary(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data map[string]float64 `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	return resp.Data
}

func TestSalesScope_SummaryIsScoped(t *testing.T) {
	f := newScopeFixture(t)

	s := summary(t, f, f.seller)
	assert.Equal(t, 1.0, s["total"])
	assert.Equal(t, 100.0, s["valor_prospecto"])
	assert.Equal(t, 0.0, s["convertida"])

	s = summary(t, f, f.supervisor)
	assert.Equal(t, 2.0, s["total"])
	assert.Equal(t, 1.0, s["convertida"])
	assert.Equal(t, 200.0, s["valor_convertido"])
	assert.Equal(t, 100.0, s["conversion_rate"])
}

func TestSalesScope_ScopeEndpointListsFilterableSellers(t *testing.T) {
	f := newScopeFixture(t)
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, f.orgID, f.supervisor.ID)
	require.NoError(t, f.app.GetSalesOpportunitiesScope(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var resp struct {
		Data struct {
			Scope   string `json:"scope"`
			Sellers []struct {
				ID uuid.UUID `json:"id"`
			} `json:"sellers"`
			Units []struct {
				ID uuid.UUID `json:"id"`
			} `json:"units"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
	assert.Equal(t, "team", resp.Data.Scope)
	sellerIDs := []uuid.UUID{}
	for _, s := range resp.Data.Sellers {
		sellerIDs = append(sellerIDs, s.ID)
	}
	assert.ElementsMatch(t, []uuid.UUID{f.seller.ID, f.teammate.ID, f.supervisor.ID}, sellerIDs)
	require.Len(t, resp.Data.Units, 1)
	assert.Equal(t, f.unit.ID, resp.Data.Units[0].ID)
}
