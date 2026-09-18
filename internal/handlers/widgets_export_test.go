package handlers

import (
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
)

// QuerySalesOpportunitiesForTest exports querySalesOpportunities for tests
// in the handlers_test package (widgets_sales_test.go), which can't reach
// the unexported function directly.
func (a *App) QuerySalesOpportunitiesForTest(orgID uuid.UUID, metric, field string, filters []FilterInput, start, end time.Time) float64 {
	return a.querySalesOpportunities(orgID, metric, field, filters, start, end)
}

// SalesConversionRateForTest exports salesConversionRate for tests in the
// handlers_test package.
func (a *App) SalesConversionRateForTest(orgID uuid.UUID, start, end time.Time) float64 {
	return a.salesConversionRate(orgID, start, end)
}

// GetTableRowsForTest exports getTableRows for tests in the handlers_test
// package.
func (a *App) GetTableRowsForTest(orgID uuid.UUID, widget models.Widget, filters []FilterInput, periodStart, periodEnd time.Time) []TableRow {
	return a.getTableRows(orgID, widget, filters, periodStart, periodEnd)
}

// IsFilterFieldAllowedForTest exports the allowedFilterFields whitelist
// lookup for tests in the handlers_test package.
func IsFilterFieldAllowedForTest(dataSource, field string) bool {
	return allowedFilterFields[dataSource][field]
}
