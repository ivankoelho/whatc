package database_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureActiveTransferUniqueness_ReportsDuplicatesAndChangesNothing(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+database.ActiveTransferIndexName).Error)
	t.Cleanup(func() {
		// leave the shared test database with the index in place
		_ = db.Exec("DELETE FROM agent_transfers WHERE phone_number LIKE 'dup-%'").Error
		_, _, _ = database.EnsureActiveTransferUniqueness(db)
	})

	org := testutil.CreateTestOrganization(t, db)
	contact := testutil.CreateTestContact(t, db, org.ID)
	mk := func(age time.Duration) uuid.UUID {
		tr := &models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "acc",
			PhoneNumber: "dup-" + uuid.NewString()[:6], Status: models.TransferStatusActive,
			Source: models.TransferSourceManual, TransferredAt: time.Now().Add(-age),
		}
		require.NoError(t, db.Create(tr).Error)
		return tr.ID
	}
	oldID, newID := mk(2*time.Hour), mk(time.Hour)

	created, dups, err := database.EnsureActiveTransferUniqueness(db)
	require.NoError(t, err)
	assert.False(t, created, "must not create the index while duplicates exist")
	require.Len(t, dups, 1)
	d := dups[0]
	assert.Equal(t, org.ID, d.OrganizationID)
	assert.Equal(t, contact.ID, d.ContactID)
	assert.Equal(t, 2, d.ActiveCount)
	assert.Equal(t, []uuid.UUID{oldID, newID}, d.TransferIDs, "ids are listed oldest first")
	assert.True(t, d.OldestAt.Before(d.NewestAt))
	assert.Contains(t, database.FormatActiveTransferDuplicates(dups), contact.ID.String())

	var rows int64
	db.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Count(&rows)
	assert.EqualValues(t, 2, rows, "no row may be deleted, merged or altered automatically")

	// Once an operator resolves the duplicate, the next run creates the index.
	require.NoError(t, db.Exec("UPDATE agent_transfers SET status = 'resumed' WHERE id = ?", oldID).Error)
	created, dups, err = database.EnsureActiveTransferUniqueness(db)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Empty(t, dups)
}
