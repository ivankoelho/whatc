package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureDefaultSalesOpportunityWidgets_SeedsOnce(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)

	require.NoError(t, app.EnsureDefaultSalesOpportunityWidgetsForTest(org.ID))
	var count int64
	require.NoError(t, app.DB.Model(&models.Widget{}).
		Where("organization_id = ? AND data_source = ?", org.ID, "sales_opportunities").
		Count(&count).Error)
	assert.Positive(t, count)

	// Idempotent: calling again does not duplicate.
	require.NoError(t, app.EnsureDefaultSalesOpportunityWidgetsForTest(org.ID))
	var countAfter int64
	require.NoError(t, app.DB.Model(&models.Widget{}).
		Where("organization_id = ? AND data_source = ?", org.ID, "sales_opportunities").
		Count(&countAfter).Error)
	assert.Equal(t, count, countAfter)
}
