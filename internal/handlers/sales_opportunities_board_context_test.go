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
)

// What the CRM board card shows besides the opportunity: the ad that brought the customer, the booked meeting and
// whether the AI agent opened it; and the quick filters built on them.

func TestBoardContext_CardsCarryTheAdOriginTheMeetingAndTheAIMark(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "mgr", []string{"sales_opportunities:read", "sales_opportunities:write", "sales_opportunities:view_all"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	seller := testutil.CreateTestUser(t, app.DB, org.ID)
	cA, cB, cC := testutil.CreateTestContact(t, app.DB, org.ID), testutil.CreateTestContact(t, app.DB, org.ID), testutil.CreateTestContact(t, app.DB, org.ID)

	mk := func(c *models.Contact, n string, ai bool, assigned *uuid.UUID) models.SalesOpportunity {
		o := models.SalesOpportunity{OrganizationID: org.ID, ContactID: c.ID, OpportunityNumber: n, CreatedByAI: ai,
			Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta, AssignedUserID: assigned, StageChangedAt: time.Now()}
		require.NoError(t, app.DB.Create(&o).Error)
		return o
	}
	oA := mk(cA, "OPP-1", true, nil)
	oB := mk(cB, "OPP-2", false, nil)
	oC := mk(cC, "OPP-3", false, &seller.ID)

	ref := func(c *models.Contact, msg, headline string) {
		require.NoError(t, app.DB.Create(&models.ContactAdReferral{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
			ContactID: c.ID, MessageID: msg, SourceID: "AD-" + msg, Headline: headline}).Error)
	}
	ref(cA, "a1", "Consulta jurídica")
	ref(cA, "a2", "Outro anúncio")
	ref(cC, "c1", "Promoção")
	booking := func(c *models.Contact, start time.Time) {
		require.NoError(t, app.DB.Create(&models.AIAppointmentOffer{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
			ContactID: c.ID, SessionID: uuid.New(), WhatsAppAccount: "x", Token: uuid.NewString(), Status: models.AIAppointmentBooked,
			ExpiresAt: time.Now().Add(time.Hour), BookedStart: &start}).Error)
	}
	meeting := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	booking(cA, meeting)
	booking(cC, time.Now().Add(-24*time.Hour)) // already happened: not "has a meeting"
	require.NoError(t, app.DB.Create(&models.NextcloudCalendar{ID: uuid.New(), OrganizationID: org.ID, CalendarPath: "/x/", Username: "u", AppPassword: "x", Location: "Escritório Centro"}).Error)

	list := func(params map[string]string) map[string]models.SalesOpportunity {
		req := testutil.NewGETRequest(t)
		testutil.SetAuthContext(req, org.ID, user.ID)
		for k, v := range params {
			testutil.SetQueryParam(req, k, v)
		}
		require.NoError(t, app.ListSalesOpportunities(req))
		var resp struct {
			Data struct {
				Opportunities []models.SalesOpportunity `json:"opportunities"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &resp))
		out := map[string]models.SalesOpportunity{}
		for _, o := range resp.Data.Opportunities {
			out[o.OpportunityNumber] = o
		}
		return out
	}

	all := list(nil)
	require.Len(t, all, 3)
	a := all[oA.OpportunityNumber]
	assert.True(t, a.CreatedByAI)
	require.NotNil(t, a.AdOrigin)
	assert.Equal(t, "Consulta jurídica", a.AdOrigin.Headline, "the FIRST ad is the origin")
	assert.Equal(t, 2, a.AdOrigin.Clicks)
	require.NotNil(t, a.Meeting)
	assert.WithinDuration(t, meeting, a.Meeting.Start, time.Second)
	assert.Equal(t, "Escritório Centro", a.Meeting.Location)

	b := all[oB.OpportunityNumber]
	assert.False(t, b.CreatedByAI)
	assert.Nil(t, b.AdOrigin)
	assert.Nil(t, b.Meeting)
	c := all[oC.OpportunityNumber]
	require.NotNil(t, c.AdOrigin)
	assert.Nil(t, c.Meeting, "a meeting in the past is not shown")

	keys := func(m map[string]models.SalesOpportunity) []string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		return ks
	}
	assert.ElementsMatch(t, []string{"OPP-1"}, keys(list(map[string]string{"ai": "true"})))
	assert.ElementsMatch(t, []string{"OPP-1", "OPP-3"}, keys(list(map[string]string{"from_ad": "true"})))
	assert.ElementsMatch(t, []string{"OPP-1"}, keys(list(map[string]string{"has_meeting": "true"})))
	assert.ElementsMatch(t, []string{"OPP-1", "OPP-2"}, keys(list(map[string]string{"unassigned": "true"})))
	assert.ElementsMatch(t, []string{"OPP-1"}, keys(list(map[string]string{"ai": "true", "has_meeting": "true", "from_ad": "true"})), "filters combine")
	assert.Len(t, list(map[string]string{"ai": "false", "from_ad": "yes"}), 3, "only a literal true turns a filter on")
}
