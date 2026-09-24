package handlers_test

import (
	"sync"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestConvertSalesOpportunity_SetsConversionSourceManual(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, got.Status)
	require.NotNil(t, got.ConversionSource)
	assert.Equal(t, models.SalesConversionSourceManual, *got.ConversionSource)
	require.NotNil(t, got.ConvertedAt)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventConverted).Find(&events).Error)
	require.Len(t, events, 1)
}

func TestLoseSalesOpportunity_RequiresLossReason(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestLoseSalesOpportunity_RejectsReasonOutsideClosedList(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "motivo_inventado"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))
}

func TestLoseSalesOpportunity_ValidReasonSetsLostAtAndEvent(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	req := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "preco", "loss_notes": "Concorrente 10% mais barato"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusPerdida, got.Status)
	require.NotNil(t, got.LossReason)
	assert.Equal(t, models.SalesLossReasonPreco, *got.LossReason)
	require.NotNil(t, got.LostAt)
}

// GATE — máquina de estados (spec §5.1): nenhuma dessas transições é permitida.
func TestSalesOpportunityStateMachine_RejectsInvalidTransitions(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	// convertida -> perdida
	convertida := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(convertida).Update("status", models.SalesOpportunityStatusConvertida).Error)
	req := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "preco"})
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", convertida.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req))

	// perdida -> convertida
	contact2 := testutil.CreateTestContact(t, app.DB, org.ID)
	perdida := newOpenOpportunity(t, app, org.ID, agent.ID, contact2.ID)
	require.NoError(t, app.DB.Model(perdida).Update("status", models.SalesOpportunityStatusPerdida).Error)
	req2 := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req2, org.ID, agent.ID)
	req2.RequestCtx.SetUserValue("id", perdida.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req2))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req2))

	// cancelada -> convertida (terminal, no edge out — even though Entrega 1
	// never writes cancelada itself, the guard must already hold for Entrega 2)
	contact3 := testutil.CreateTestContact(t, app.DB, org.ID)
	cancelada := newOpenOpportunity(t, app, org.ID, agent.ID, contact3.ID)
	require.NoError(t, app.DB.Model(cancelada).Update("status", models.SalesOpportunityStatusCancelada).Error)
	req3 := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req3, org.ID, agent.ID)
	req3.RequestCtx.SetUserValue("id", cancelada.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req3))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req3))

	// cancelada -> perdida
	contact4 := testutil.CreateTestContact(t, app.DB, org.ID)
	cancelada2 := newOpenOpportunity(t, app, org.ID, agent.ID, contact4.ID)
	require.NoError(t, app.DB.Model(cancelada2).Update("status", models.SalesOpportunityStatusCancelada).Error)
	req4 := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "preco"})
	testutil.SetAuthContext(req4, org.ID, agent.ID)
	req4.RequestCtx.SetUserValue("id", cancelada2.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req4))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req4))

	// convertida -> convertida (terminal, no self-edge either)
	contact5 := testutil.CreateTestContact(t, app.DB, org.ID)
	convertida2 := newOpenOpportunity(t, app, org.ID, agent.ID, contact5.ID)
	require.NoError(t, app.DB.Model(convertida2).Update("status", models.SalesOpportunityStatusConvertida).Error)
	req5 := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req5, org.ID, agent.ID)
	req5.RequestCtx.SetUserValue("id", convertida2.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req5))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req5))

	// perdida -> perdida (terminal, no self-edge either)
	contact6 := testutil.CreateTestContact(t, app.DB, org.ID)
	perdida2 := newOpenOpportunity(t, app, org.ID, agent.ID, contact6.ID)
	require.NoError(t, app.DB.Model(perdida2).Update("status", models.SalesOpportunityStatusPerdida).Error)
	req6 := testutil.NewJSONRequest(t, map[string]any{"loss_reason": "preco"})
	testutil.SetAuthContext(req6, org.ID, agent.ID)
	req6.RequestCtx.SetUserValue("id", perdida2.ID.String())
	require.NoError(t, app.LoseSalesOpportunity(req6))
	assert.Equal(t, fasthttp.StatusBadRequest, testutil.GetResponseStatusCode(req6))
}

// Final review finding 8 (TOCTOU): two concurrent ConvertSalesOpportunity
// calls on the same open opportunity raced on a Go-level `opp.Status` read
// followed by a bare `WHERE id = ?` update, so both could pass the guard and
// both write. Mirrors TestCreateOrRetriggerSalesOpportunity_UniqueUnderConcurrency's
// goroutines + WaitGroup pattern in sales_opportunities_creation_test.go.
func TestConvertSalesOpportunity_ConcurrentConvertsYieldExactlyOneSuccess(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)

	const n = 10
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := testutil.NewJSONRequest(t, nil)
			testutil.SetAuthContext(req, org.ID, agent.ID)
			req.RequestCtx.SetUserValue("id", opp.ID.String())
			_ = app.ConvertSalesOpportunity(req)
			statuses[idx] = testutil.GetResponseStatusCode(req)
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, s := range statuses {
		if s == fasthttp.StatusOK {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "exactly one of the concurrent converts must succeed")

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStatusConvertida, got.Status)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?", opp.ID, models.SalesOpportunityEventConverted).Find(&events).Error)
	assert.Len(t, events, 1, "only the single winning convert may write a converted event")
}

// stage must freeze at whatever value it had when the opportunity terminates
// (spec §5.1) — convert/lose must never touch stage.
func TestConvertSalesOpportunity_DoesNotChangeStage(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "agent", []string{"sales_opportunities:read", "sales_opportunities:write"})
	agent := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	opp := newOpenOpportunity(t, app, org.ID, agent.ID, contact.ID)
	require.NoError(t, app.DB.Model(opp).Update("stage", models.SalesOpportunityStageAbrirOrcamento).Error)

	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, agent.ID)
	req.RequestCtx.SetUserValue("id", opp.ID.String())
	require.NoError(t, app.ConvertSalesOpportunity(req))
	assert.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var got models.SalesOpportunity
	require.NoError(t, app.DB.First(&got, "id = ?", opp.ID).Error)
	assert.Equal(t, models.SalesOpportunityStageAbrirOrcamento, got.Stage)
}
