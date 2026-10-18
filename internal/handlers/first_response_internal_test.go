package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordFirstResponse_StampsOnlyTheOwnersFirstReply(t *testing.T) {
	app := newProcessorTestApp(t)
	org, account := createProcessorTestOrg(t, app)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	owner := testutil.CreateTestUser(t, app.DB, org.ID).ID
	other := testutil.CreateTestUser(t, app.DB, org.ID).ID

	tr := &models.AgentTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		WhatsAppAccount: account.Name, PhoneNumber: contact.PhoneNumber,
		Status: models.TransferStatusActive, Source: models.TransferSourceManual,
		AgentID: &owner, TransferredAt: time.Now().Add(-time.Hour),
	}
	require.NoError(t, app.DB.Create(tr).Error)
	read := func() *time.Time {
		var got models.AgentTransfer
		require.NoError(t, app.DB.First(&got, "id = ?", tr.ID).Error)
		return got.SLA.FirstResponseAt
	}

	app.recordFirstResponse(org.ID, contact.ID, other)
	assert.Nil(t, read(), "a message from someone who is not the owner is not the first response")

	app.recordFirstResponse(org.ID, contact.ID, owner)
	first := read()
	require.NotNil(t, first)

	time.Sleep(20 * time.Millisecond)
	app.recordFirstResponse(org.ID, contact.ID, owner)
	second := read()
	require.NotNil(t, second)
	assert.True(t, first.Equal(*second), "the first response is never rewritten")
}
