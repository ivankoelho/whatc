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

func usageRow(orgID uuid.UUID, account, wamid string, messageID *uuid.UUID) *models.MessageUsage {
	return &models.MessageUsage{
		OrganizationID: orgID, WhatsAppAccount: account, Wamid: wamid, MessageID: messageID,
		Direction: "outgoing", ActorType: "agent", Quantity: 1, SentAt: time.Now(),
	}
}

func TestMessageUsageSchema_MigrationIsAdditiveAndIdempotent(t *testing.T) {
	db := testutil.SetupTestDB(t)

	// the four tables are part of the production migration list
	names := map[string]bool{}
	for _, m := range database.GetMigrationModels() {
		names[m.Name] = true
	}
	for _, n := range []string{"WhatsAppRate", "MessageUsage", "MessagePricingEvent", "ContactStatusEvent"} {
		assert.True(t, names[n], "%s must be migrated", n)
	}

	// running the migration again changes nothing and does not fail
	require.NoError(t, database.AutoMigrate(db))
	require.NoError(t, database.AutoMigrate(db))
	for _, tbl := range []string{"message_usage", "message_pricing_events", "whatsapp_rates", "contact_status_events"} {
		assert.True(t, db.Migrator().HasTable(tbl), tbl)
	}

	// the messages table was not touched: nothing of the ledger leaked into it
	for _, col := range []string{"billing_state", "billable", "estimated_cost", "billing_category", "link_state"} {
		assert.False(t, db.Migrator().HasColumn("messages", col), "messages must not have %s", col)
	}
}

func TestMessageUsageSchema_OneMessageIsAtMostOneRow(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	msgID := uuid.New()

	require.NoError(t, db.Create(usageRow(org.ID, "acc", "wamid-1", &msgID)).Error)

	t.Run("same message_id", func(t *testing.T) {
		assert.Error(t, db.Create(usageRow(org.ID, "acc", "wamid-other", &msgID)).Error)
	})
	t.Run("same org, account and wamid", func(t *testing.T) {
		assert.Error(t, db.Create(usageRow(org.ID, "acc", "wamid-1", nil)).Error)
	})
	t.Run("same wamid in another account or organization is a different message", func(t *testing.T) {
		assert.NoError(t, db.Create(usageRow(org.ID, "acc-2", "wamid-1", nil)).Error)
		other := testutil.CreateTestOrganization(t, db)
		assert.NoError(t, db.Create(usageRow(other.ID, "acc", "wamid-1", nil)).Error)
	})
	t.Run("rows without wamid or message never collide", func(t *testing.T) {
		assert.NoError(t, db.Create(usageRow(org.ID, "acc", "", nil)).Error)
		assert.NoError(t, db.Create(usageRow(org.ID, "acc", "", nil)).Error)
	})
}

func TestMessageUsageSchema_PricingEventAndRateKeys(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	at := time.Now().UTC().Truncate(time.Second)

	ev := func(status string, when time.Time) *models.MessagePricingEvent {
		return &models.MessagePricingEvent{OrganizationID: org.ID, WhatsAppAccount: "acc", Wamid: "w", Status: status, EventAt: when}
	}
	require.NoError(t, db.Create(ev("sent", at)).Error)
	assert.Error(t, db.Create(ev("sent", at)).Error, "the same event twice is refused")
	assert.NoError(t, db.Create(ev("delivered", at)).Error)
	assert.NoError(t, db.Create(ev("sent", at.Add(time.Second))).Error)

	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rate := func() *models.WhatsAppRate {
		return &models.WhatsAppRate{OrganizationID: org.ID, Country: "55", Category: "utility", Price: 0.0068, Currency: "BRL", ValidFrom: day}
	}
	require.NoError(t, db.Create(rate()).Error)
	assert.Error(t, db.Create(rate()).Error, "same country, category and start date")
}
