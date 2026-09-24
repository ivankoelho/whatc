package models_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestOccurrenceProcess_TableName(t *testing.T) {
	assert.Equal(t, "occurrence_processes", models.OccurrenceProcess{}.TableName())
}

func TestOccurrenceProcessMessage_TableName(t *testing.T) {
	assert.Equal(t, "occurrence_process_messages", models.OccurrenceProcessMessage{}.TableName())
}

func TestOccurrenceProcess_RequiredFieldsRoundTrip(t *testing.T) {
	p := models.OccurrenceProcess{
		OrganizationID: uuid.New(),
		Name:           "Divergência no recebimento",
		RequiredFields: models.JSONBArray{"invoice_number", "product_description"},
	}
	val, err := p.RequiredFields.Value()
	assert.NoError(t, err)
	var back models.JSONBArray
	assert.NoError(t, back.Scan(val))
	assert.Equal(t, models.JSONBArray{"invoice_number", "product_description"}, back)
}
