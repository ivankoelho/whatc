package handlers

import (
	"time"

	"github.com/google/uuid"
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
