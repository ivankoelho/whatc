package handlers_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

func TestUpsertSalesOpportunityXProcessLink_CreatesLinkAndFillsContactCPF(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{
		"num_pedido": "666", "documento": "529.982.247-25",
	})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var link models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).First(&link).Error)
	assert.Equal(t, "666", link.NumPedido)
	assert.Equal(t, "52998224725", link.Documento)
	assert.Nil(t, link.ResolvedAt)

	var updatedOpp models.SalesOpportunity
	require.NoError(t, app.DB.First(&updatedOpp, "id = ?", opp.ID).Error)
	require.NotNil(t, updatedOpp.XProcessNumPedido)
	assert.Equal(t, "666", *updatedOpp.XProcessNumPedido)

	var updatedContact models.Contact
	require.NoError(t, app.DB.First(&updatedContact, "id = ?", contact.ID).Error)
	assert.Equal(t, "52998224725", updatedContact.CPFCNPJ)
}

func TestUpsertSalesOpportunityXProcessLink_DoesNotOverwriteExistingContactCPF(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	// Column is physically "cpfcnpj" (no underscore) — see the fix note in
	// UpsertSalesOpportunityXProcessLink below; get this wrong here and the
	// test would silently pass against a column the handler never touches.
	require.NoError(t, app.DB.Model(contact).Update("cpfcnpj", "99999999999").Error)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "1", "documento": "52998224725"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(req))

	var updatedContact models.Contact
	require.NoError(t, app.DB.First(&updatedContact, "id = ?", contact.ID).Error)
	assert.Equal(t, "99999999999", updatedContact.CPFCNPJ, "must not clobber a CPF the contact already had")
}

func TestUpsertSalesOpportunityXProcessLink_InvalidDocumentoRejected(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "666", "documento": "123"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(req))
	testutil.AssertErrorResponse(t, req, fasthttp.StatusBadRequest, "")

	var count int64
	app.DB.Model(&models.SalesOpportunityXProcessLink{}).Where("sales_opportunity_id = ?", opp.ID).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestUpsertSalesOpportunityXProcessLink_UpdatesOpenLinkInPlace(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	first := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "111", "documento": "52998224725"})
	testutil.SetAuthContext(first, org.ID, agent.ID)
	first.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(first))

	second := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "222", "documento": "11144477735"})
	testutil.SetAuthContext(second, org.ID, agent.ID)
	second.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(second))

	var links []models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&links).Error)
	require.Len(t, links, 1, "editing an unresolved link must update it in place, not create a second row")
	assert.Equal(t, "222", links[0].NumPedido)
}

func TestUpsertSalesOpportunityXProcessLink_ResolvedLinkIsImmutable_CreatesNewRow(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	first := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "111", "documento": "52998224725"})
	testutil.SetAuthContext(first, org.ID, agent.ID)
	first.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(first))

	now := time.Now()
	require.NoError(t, app.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("sales_opportunity_id = ?", opp.ID).Update("resolved_at", now).Error)

	second := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "222", "documento": "11144477735"})
	testutil.SetAuthContext(second, org.ID, agent.ID)
	second.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(second))

	var links []models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at asc").Find(&links).Error)
	require.Len(t, links, 2, "a resolved link must never be overwritten — a new registration creates a new row")
	assert.Equal(t, "111", links[0].NumPedido)
	assert.NotNil(t, links[0].ResolvedAt, "the original resolved link must stay untouched")
	assert.Equal(t, "222", links[1].NumPedido)
	assert.Nil(t, links[1].ResolvedAt)
}

// TestUpsertSalesOpportunityXProcessLink_ResolvedBetweenReadAndWrite_FallsBackToCreate
// is the write-path counterpart to ...ResolvedLinkIsImmutable_CreatesNewRow
// above. That test resolves the link *before* the handler runs, so the
// handler's own open-link lookup (`WHERE resolved_at IS NULL`) never finds
// it and the buggy pre-fix code was never exercised — reverting the fix
// still passes that test. This test instead resolves the link from a GORM
// callback that fires right after the handler's SELECT of the open link
// (i.e. exactly between its read and its write), reproducing a
// reconciliation job landing in that window. It fails against the pre-fix
// code, which updated by primary key alone with no resolved_at re-check and
// would have silently overwritten the now-resolved row.
func TestUpsertSalesOpportunityXProcessLink_ResolvedBetweenReadAndWrite_FallsBackToCreate(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	first := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "111", "documento": "52998224725"})
	testutil.SetAuthContext(first, org.ID, agent.ID)
	first.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(first))

	// Simulate a concurrent reconciliation run resolving the link at the
	// exact moment the handler's write would race it: fire right after the
	// first (and only) query the second call issues against the links
	// table, which is the handler's own open-link lookup.
	const hookName = "test:resolve_between_read_and_write"
	fired := false
	require.NoError(t, app.DB.Callback().Query().After("gorm:query").Register(hookName, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "sales_opportunity_xprocess_links" {
			return
		}
		fired = true
		require.NoError(t, app.DB.Model(&models.SalesOpportunityXProcessLink{}).
			Where("sales_opportunity_id = ?", opp.ID).Update("resolved_at", time.Now()).Error)
	}))
	defer app.DB.Callback().Query().Remove(hookName)

	second := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "222", "documento": "11144477735"})
	testutil.SetAuthContext(second, org.ID, agent.ID)
	second.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(second))
	require.True(t, fired, "test setup bug: the resolve hook never fired")

	var links []models.SalesOpportunityXProcessLink
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Order("created_at asc").Find(&links).Error)
	require.Len(t, links, 2, "the write must lose the race (RowsAffected == 0) and fall back to creating a new row")
	assert.Equal(t, "111", links[0].NumPedido, "a link resolved mid-request must keep exactly what the reconciler wrote, not the agent's concurrent edit")
	assert.NotNil(t, links[0].ResolvedAt)
	assert.Equal(t, "222", links[1].NumPedido)
	assert.Nil(t, links[1].ResolvedAt)
}

func TestGetSalesOpportunityXProcessLink_ReturnsPendingReviewFlag(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	create := testutil.NewJSONRequest(t, map[string]any{"num_pedido": "666", "documento": "52998224725"})
	testutil.SetAuthContext(create, org.ID, agent.ID)
	create.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.UpsertSalesOpportunityXProcessLink(create))

	require.NoError(t, app.DB.Model(&models.SalesOpportunityXProcessLink{}).
		Where("sales_opportunity_id = ?", opp.ID).Update("consecutive_not_found", 5).Error)

	get := testutil.NewGETRequest(t)
	testutil.SetAuthContext(get, org.ID, agent.ID)
	get.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.GetSalesOpportunityXProcessLink(get))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(get))

	var resp struct {
		Data struct {
			NumPedido     string `json:"num_pedido"`
			PendingReview bool   `json:"pending_review"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(get), &resp))
	assert.True(t, resp.Data.PendingReview)
}
