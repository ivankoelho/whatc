package handlers_test

import (
	"encoding/json"
	"testing"

	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type segmentEnv struct {
	app      *handlers.App
	org      *models.Organization
	user     *models.User
	campaign *models.BulkMessageCampaign
	template *models.Template
}

func newSegmentEnv(t *testing.T) segmentEnv {
	t.Helper()
	app := newTestApp(t, withQueue(testutil.NewMockQueue()))
	org := testutil.CreateTestOrganization(t, app.DB)
	admin := testutil.CreateAdminRole(t, app.DB, org.ID)
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&admin.ID))
	account := testutil.CreateTestWhatsAppAccountWith(t, app.DB, org.ID, testutil.WithAccountName("segment-"+org.ID.String()[:8]))
	template := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name) // MARKETING
	campaign := createTestCampaign(t, app, org.ID, template.ID, user.ID, account.Name, models.CampaignStatusDraft)
	return segmentEnv{app, org, user, campaign, template}
}

// segment posts a segmentation request and returns the HTTP status and added_count.
func (e segmentEnv) segment(t *testing.T, body map[string]any) (int, int) {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, e.org.ID, e.user.ID)
	testutil.SetPathParam(req, "id", e.campaign.ID.String())
	require.NoError(t, e.app.AddRecipientsFromContacts(req))
	var res struct {
		Data struct {
			AddedCount int `json:"added_count"`
		} `json:"data"`
	}
	_ = json.Unmarshal(testutil.GetResponseBody(req), &res)
	return testutil.GetResponseStatusCode(req), res.Data.AddedCount
}

func (e segmentEnv) contact(t *testing.T, phone string, mutate func(*models.Contact)) *models.Contact {
	t.Helper()
	c := testutil.CreateTestContactWith(t, e.app.DB, e.org.ID, testutil.WithPhoneNumber(phone))
	if mutate != nil {
		mutate(c)
		require.NoError(t, e.app.DB.Save(c).Error)
	}
	return c
}

func (e segmentEnv) recipientCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	e.app.DB.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ?", e.campaign.ID).Count(&n)
	return n
}

func TestSegment_ByContactType(t *testing.T) {
	e := newSegmentEnv(t)
	e.contact(t, "5571900000001", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador })
	e.contact(t, "5571900000002", func(c *models.Contact) { c.ContactType = models.ContactTypeFornecedor })
	e.contact(t, "5571900000003", nil) // cliente

	status, added := e.segment(t, map[string]any{"contact_type": "colaborador"})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, 1, added)
}

func TestSegment_ByUnitDepartmentAndCombined(t *testing.T) {
	e := newSegmentEnv(t)
	unit := e.unitFor(t, "Loja 05")
	dept := e.deptFor(t, "TI")
	e.contact(t, "5571900000011", func(c *models.Contact) { c.UnitID, c.DepartmentID = &unit.ID, &dept.ID })
	e.contact(t, "5571900000012", func(c *models.Contact) { c.UnitID = &unit.ID })
	e.contact(t, "5571900000013", func(c *models.Contact) { c.DepartmentID = &dept.ID })
	e.contact(t, "5571900000014", nil)

	// Dry run counts without writing.
	status, added := e.segment(t, map[string]any{"unit_id": unit.ID.String(), "department_id": dept.ID.String(), "dry_run": true})
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, 1, added, "unit AND department")
	assert.Equal(t, int64(0), e.recipientCount(t), "dry_run must not write")

	_, added = e.segment(t, map[string]any{"unit_id": unit.ID.String()})
	assert.Equal(t, 2, added)
}

func TestSegment_CombinesWithDDD(t *testing.T) {
	e := newSegmentEnv(t)
	e.contact(t, "5571900000021", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador })
	e.contact(t, "5511900000022", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador })

	_, added := e.segment(t, map[string]any{"ddds": []string{"71"}, "contact_type": "colaborador"})
	assert.Equal(t, 1, added)
}

func TestSegment_DDDOnlyStillWorks(t *testing.T) {
	e := newSegmentEnv(t)
	e.contact(t, "5571900000031", nil)
	e.contact(t, "5511900000032", nil)
	_, added := e.segment(t, map[string]any{"ddds": []string{"71"}})
	assert.Equal(t, 1, added)
}

func TestSegment_RequiresAtLeastOneFilterAndValidatesInput(t *testing.T) {
	e := newSegmentEnv(t)
	e.contact(t, "5571900000041", nil)

	status, _ := e.segment(t, map[string]any{})
	assert.Equal(t, fasthttp.StatusBadRequest, status, "no filter must never mean the whole base")
	status, _ = e.segment(t, map[string]any{"unit_id": "", "department_id": ""})
	assert.Equal(t, fasthttp.StatusBadRequest, status, "empty strings are not filters")
	status, _ = e.segment(t, map[string]any{"contact_type": "parceiro"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	status, _ = e.segment(t, map[string]any{"unit_id": "not-a-uuid"})
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Equal(t, int64(0), e.recipientCount(t))
}

func TestSegment_UnitFromAnotherOrganizationRejectedAndOtherOrgContactsNeverIncluded(t *testing.T) {
	e := newSegmentEnv(t)
	other := testutil.CreateTestOrganization(t, e.app.DB)
	foreign := models.Unit{OrganizationID: other.ID, Name: "Alheia"}
	require.NoError(t, e.app.DB.Create(&foreign).Error)

	status, _ := e.segment(t, map[string]any{"unit_id": foreign.ID.String()})
	assert.Equal(t, fasthttp.StatusNotFound, status)

	// A contact of another organization with the same type is not picked up.
	oc := testutil.CreateTestContactWith(t, e.app.DB, other.ID, testutil.WithPhoneNumber("5571900000051"))
	require.NoError(t, e.app.DB.Model(oc).Update("contact_type", "colaborador").Error)
	_, added := e.segment(t, map[string]any{"contact_type": "colaborador"})
	assert.Equal(t, 0, added)
}

func TestSegment_SkipsOpenOccurrenceDuplicatesAndOptedOutMarketing(t *testing.T) {
	e := newSegmentEnv(t)
	complaining := e.contact(t, "5571900000061", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador })
	e.contact(t, "5571900000062", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador; c.MarketingOptOut = true })
	e.contact(t, "5571900000063", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador })
	occ := createOccurrenceWith(t, e.app, e.org.ID, e.user.ID, complaining.ID, nil)
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(occ))

	_, added := e.segment(t, map[string]any{"contact_type": "colaborador"})
	assert.Equal(t, 1, added, "open occurrence and marketing opt-out are left out")

	_, added = e.segment(t, map[string]any{"contact_type": "colaborador"})
	assert.Equal(t, 0, added, "already in the campaign")
}

func TestSegment_OptOutOnlyExcludedForMarketingTemplates(t *testing.T) {
	e := newSegmentEnv(t)
	require.NoError(t, e.app.DB.Model(e.template).Update("category", "UTILITY").Error)
	e.contact(t, "5571900000071", func(c *models.Contact) { c.ContactType = models.ContactTypeColaborador; c.MarketingOptOut = true })

	_, added := e.segment(t, map[string]any{"contact_type": "colaborador"})
	assert.Equal(t, 1, added, "opt-out only blocks MARKETING, like the worker")
}

func (e segmentEnv) unitFor(t *testing.T, name string) models.Unit {
	t.Helper()
	u := models.Unit{OrganizationID: e.org.ID, Name: name}
	require.NoError(t, e.app.DB.Create(&u).Error)
	return u
}

func (e segmentEnv) deptFor(t *testing.T, name string) models.Department {
	t.Helper()
	d := models.Department{OrganizationID: e.org.ID, Name: name}
	require.NoError(t, e.app.DB.Create(&d).Error)
	return d
}
