package models_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestSalesOpportunityXProcessLink_TableName(t *testing.T) {
	assert.Equal(t, "sales_opportunity_xprocess_links", models.SalesOpportunityXProcessLink{}.TableName())
}
