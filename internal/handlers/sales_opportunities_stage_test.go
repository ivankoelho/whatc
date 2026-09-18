package handlers_test

import (
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

func newOpenOpportunity(t *testing.T, app *handlers.App, orgID, agentID, contactID uuid.UUID) *models.SalesOpportunity {
	opp := &models.SalesOpportunity{
		OrganizationID: orgID, ContactID: contactID, OpportunityNumber: "OPP-20260917-000001",
		Stage: models.SalesOpportunityStagePotencial, Status: models.SalesOpportunityStatusAberta,
		AssignedUserID: &agentID, StageChangedAt: time.Now(),
	}
	require.NoError(t, app.DB.Create(opp).Error)
	return opp
}

func TestChangeSalesOpportunityStage_ToDirecionadaWithoutDirecionamentoFails(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Update("stage", models.SalesOpportunityStageAbrirOrcamento).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "direcionada"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityStage(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestChangeSalesOpportunityStage_ToDirecionadaWithDirecionamentoSetsStageChangedAt(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	visita := models.SalesDirecionamentoVisita
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"stage": models.SalesOpportunityStageAbrirOrcamento, "direcionamento": visita,
	}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "direcionada"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityStage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStageDirecionada, got.Stage)
	assert.True(t, got.StageChangedAt.After(opp.StageChangedAt))

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventStageChanged).Find(&events).Error)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].FromStage)
	require.NotNil(t, events[0].ToStage)
	assert.Equal(t, models.SalesOpportunityStageAbrirOrcamento, *events[0].FromStage)
	assert.Equal(t, models.SalesOpportunityStageDirecionada, *events[0].ToStage)
}

func TestChangeSalesOpportunityStage_LeavingDirecionadaClearsSLABreached(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	visita := models.SalesDirecionamentoVisita
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"stage": models.SalesOpportunityStageDirecionada, "direcionamento": visita, "sla_breached": true,
	}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "abrir_orcamento"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityStage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.False(t, got.SLABreached)
}

func TestChangeSalesOpportunityStage_ResendingCurrentStageIsANoOp(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	visita := models.SalesDirecionamentoVisita
	stageChangedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(t, app.DB.Model(opp).Updates(map[string]any{
		"stage": models.SalesOpportunityStageDirecionada, "direcionamento": visita, "stage_changed_at": stageChangedAt,
	}).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"stage": "direcionada"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityStage(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStageDirecionada, got.Stage)
	assert.True(t, stageChangedAt.Equal(got.StageChangedAt), "stage_changed_at must not be restamped by a same-stage resend")

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventStageChanged).Find(&events).Error)
	assert.Len(t, events, 0, "resending the current stage must not log a spurious stage_changed event")
}

func TestChangeSalesOpportunityDirecionamento_ChangeableInAnyStageWithoutMovingStage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"direcionamento": "whatsapp"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityDirecionamento(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStagePotencial, got.Stage, "must not change stage")
	require.NotNil(t, got.Direcionamento)
	assert.Equal(t, models.SalesDirecionamentoWhatsApp, *got.Direcionamento)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventDirecionamentoChanged).Find(&events).Error)
	require.Len(t, events, 1)
}

func TestChangeSalesOpportunityDirecionamento_ResendingCurrentValueIsANoOp(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	whatsapp := models.SalesDirecionamentoWhatsApp
	require.NoError(t, app.DB.Model(opp).Update("direcionamento", whatsapp).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"direcionamento": "whatsapp"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ChangeSalesOpportunityDirecionamento(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	require.NotNil(t, got.Direcionamento)
	assert.Equal(t, models.SalesDirecionamentoWhatsApp, *got.Direcionamento)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventDirecionamentoChanged).Find(&events).Error)
	assert.Len(t, events, 0, "resending the current direcionamento must not log a duplicate direcionamento_changed event")
}
