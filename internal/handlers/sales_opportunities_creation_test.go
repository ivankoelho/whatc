package handlers_test

import (
	"sync"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOrRetriggerSalesOpportunity_CreatesInPotencial(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(&contact).Update("assigned_user_id", user.ID).Error)
	contact.AssignedUserID = &user.ID

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	assert.Equal(t, models.SalesOpportunityStagePotencial, opp.Stage)
	assert.Equal(t, models.SalesOpportunityStatusAberta, opp.Status)
	require.NotNil(t, opp.AssignedUserID)
	assert.Equal(t, user.ID, *opp.AssignedUserID)
	assert.Regexp(t, `^OPP-\d{8}-\d{6}$`, opp.OpportunityNumber)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ?", opp.ID).Find(&events).Error)
	require.Len(t, events, 1)
	assert.Equal(t, models.SalesOpportunityEventOpened, events[0].Type)
	assert.Equal(t, models.SalesOpportunityEventSourceSystem, events[0].Source)
}

func TestCreateOrRetriggerSalesOpportunity_AssignedUserIDCanBeNil(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	opp, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	assert.Nil(t, opp.AssignedUserID)
}

func TestCreateOrRetriggerSalesOpportunity_RetriggerDoesNotDuplicate(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	first, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).Where("id = ?", first.ID).
		Update("stage", models.SalesOpportunityStageAbrirOrcamento).Error)

	second, err := app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, models.SalesOpportunityStageAbrirOrcamento, second.Stage, "retrigger must not reset stage")

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).
		Where("contact_id = ? AND status = ?", contact.ID, models.SalesOpportunityStatusAberta).
		Count(&count).Error)
	assert.EqualValues(t, 1, count)

	var events []models.SalesOpportunityEvent
	require.NoError(t, app.DB.Where("sales_opportunity_id = ? AND type = ?",
		first.ID, models.SalesOpportunityEventRetriggered).Find(&events).Error)
	require.Len(t, events, 1)
}

// GATE. Concurrent creation for the same contact must yield exactly one open
// opportunity — the partial unique index (organization_id, contact_id) WHERE
// status='aberta' is what makes this safe (spec §5.3), same pattern as
// TestOccurrenceProtocol_UniqueUnderConcurrency / TestSalesOpportunityProtocol_UniqueUnderConcurrency.
func TestCreateOrRetriggerSalesOpportunity_UniqueUnderConcurrency(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = app.CreateOrRetriggerSalesOpportunityForTest(contact, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "chamada %d falhou", i)
	}

	var count int64
	require.NoError(t, app.DB.Model(&models.SalesOpportunity{}).
		Where("contact_id = ? AND status = ?", contact.ID, models.SalesOpportunityStatusAberta).
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}
