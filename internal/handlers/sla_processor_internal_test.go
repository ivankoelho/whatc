package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/websocket"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSLATestApp creates a minimal App for SLA processor internal tests.
func newSLATestApp(t *testing.T) *App {
	t.Helper()
	db := testutil.SetupTestDB(t)
	log := testutil.NopLogger()
	hub := websocket.NewHub(log)
	go hub.Run()

	app := &App{
		DB:    db,
		Log:   log,
		WSHub: hub,
	}
	if rdb := testutil.SetupTestRedis(t); rdb != nil {
		app.Redis = rdb
	}
	return app
}

// createSLATestTransfer creates an active agent transfer in the DB with the given SLA fields.
func createSLATestTransfer(t *testing.T, app *App, orgID, contactID, agentID uuid.UUID, accountName string, sla models.SLATracking) *models.AgentTransfer {
	t.Helper()
	transfer := &models.AgentTransfer{
		BaseModel:       models.BaseModel{ID: uuid.New()},
		OrganizationID:  orgID,
		ContactID:       contactID,
		AgentID:         &agentID,
		WhatsAppAccount: accountName,
		PhoneNumber:     "+1234567890",
		Status:          models.TransferStatusActive,
		SLA:             sla,
	}
	require.NoError(t, app.DB.Create(transfer).Error)
	return transfer
}

// createTestAgentMessage creates an outgoing message from the given agent for the given contact.
func createTestAgentMessage(t *testing.T, app *App, orgID, contactID, agentID uuid.UUID, accountName string, sentAt time.Time) {
	t.Helper()
	msg := &models.Message{
		BaseModel:       models.BaseModel{ID: uuid.New(), CreatedAt: sentAt},
		OrganizationID:  orgID,
		ContactID:       contactID,
		WhatsAppAccount: accountName,
		Direction:       models.DirectionOutgoing,
		MessageType:     models.MessageTypeText,
		Content:         "agent reply",
		SentByUserID:    &agentID,
		Status:          models.MessageStatusSent,
	}
	require.NoError(t, app.DB.Create(msg).Error)
}

// --- agentRespondedSince ---

func TestAgentRespondedSince_TrueWhenMessageAfterTimestamp(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	since := time.Now().Add(-10 * time.Minute)
	// Agent sent a message 5 minutes ago (after since)
	createTestAgentMessage(t, app, org.ID, contact.ID, agent.ID, account.Name, time.Now().Add(-5*time.Minute))

	proc := NewSLAProcessor(app, time.Minute)
	transfer := models.AgentTransfer{
		ContactID: contact.ID,
		AgentID:   &agent.ID,
	}

	assert.True(t, proc.agentRespondedSince(transfer, since))
}

func TestAgentRespondedSince_FalseWhenNoMessages(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)

	proc := NewSLAProcessor(app, time.Minute)
	transfer := models.AgentTransfer{
		ContactID: contact.ID,
		AgentID:   &agent.ID,
	}

	assert.False(t, proc.agentRespondedSince(transfer, time.Now().Add(-1*time.Hour)))
}

func TestAgentRespondedSince_FalseWhenNoAgent(t *testing.T) {
	app := newSLATestApp(t)

	proc := NewSLAProcessor(app, time.Minute)
	transfer := models.AgentTransfer{
		ContactID: uuid.New(),
		AgentID:   nil,
	}

	assert.False(t, proc.agentRespondedSince(transfer, time.Now().Add(-1*time.Hour)))
}

func TestAgentRespondedSince_FalseWhenMessageBeforeTimestamp(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	// Agent sent a message 20 minutes ago
	createTestAgentMessage(t, app, org.ID, contact.ID, agent.ID, account.Name, time.Now().Add(-20*time.Minute))

	proc := NewSLAProcessor(app, time.Minute)
	transfer := models.AgentTransfer{
		ContactID: contact.ID,
		AgentID:   &agent.ID,
	}

	// Check since 10 minutes ago — the message at -20m is before that
	assert.False(t, proc.agentRespondedSince(transfer, time.Now().Add(-10*time.Minute)))
}

// --- autoCloseExpiredTransfers: skipped when agent active ---

func TestSLAAutoCloseSkippedWhenAgentActive(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	autoCloseHours := 2
	// Transfer created 3 hours ago, expired 1 hour ago
	expiresAt := time.Now().Add(-1 * time.Hour)
	transfer := createSLATestTransfer(t, app, org.ID, contact.ID, agent.ID, account.Name, models.SLATracking{
		ExpiresAt: &expiresAt,
	})

	// Agent sent a message 30 minutes ago (after transfer was created)
	createTestAgentMessage(t, app, org.ID, contact.ID, agent.ID, account.Name, time.Now().Add(-30*time.Minute))

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA: models.SLAConfig{
			Enabled:        true,
			AutoCloseHours: autoCloseHours,
		},
	}

	proc := NewSLAProcessor(app, time.Minute)
	proc.autoCloseExpiredTransfers(org.ID, settings, time.Now())

	// Reload transfer — should still be active with extended expiry
	var updated models.AgentTransfer
	require.NoError(t, app.DB.Where("id = ?", transfer.ID).First(&updated).Error)

	assert.Equal(t, models.TransferStatusActive, updated.Status, "transfer should still be active")
	require.NotNil(t, updated.SLA.ExpiresAt)
	assert.True(t, updated.SLA.ExpiresAt.After(time.Now().Add(time.Duration(autoCloseHours-1)*time.Hour)),
		"expires_at should be extended into the future")
}

// --- autoCloseExpiredTransfers: fires when no agent response ---

func TestSLAAutoCloseFiresWhenNoAgentResponse(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	// Transfer expired 1 hour ago, no agent messages at all
	expiresAt := time.Now().Add(-1 * time.Hour)
	transfer := createSLATestTransfer(t, app, org.ID, contact.ID, agent.ID, account.Name, models.SLATracking{
		ExpiresAt: &expiresAt,
	})

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA: models.SLAConfig{
			Enabled:        true,
			AutoCloseHours: 2,
		},
	}

	proc := NewSLAProcessor(app, time.Minute)
	proc.autoCloseExpiredTransfers(org.ID, settings, time.Now())

	// Reload transfer — should be expired
	var updated models.AgentTransfer
	require.NoError(t, app.DB.Where("id = ?", transfer.ID).First(&updated).Error)

	assert.Equal(t, models.TransferStatusExpired, updated.Status, "transfer should be expired")
}

// --- autoCloseExpiredTransfers: releases the contact on auto-close ---

func TestSLAAutoCloseReleasesContact(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID,
		"contact_status":   models.ContactStatusInProgress,
	}).Error)

	// Transfer expired 1 hour ago, no agent messages at all
	expiresAt := time.Now().Add(-1 * time.Hour)
	createSLATestTransfer(t, app, org.ID, contact.ID, agent.ID, account.Name, models.SLATracking{
		ExpiresAt: &expiresAt,
	})

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA: models.SLAConfig{
			Enabled:        true,
			AutoCloseHours: 2,
		},
	}

	proc := NewSLAProcessor(app, time.Minute)
	proc.autoCloseExpiredTransfers(org.ID, settings, time.Now())

	var stored models.Contact
	require.NoError(t, app.DB.First(&stored, "id = ?", contact.ID).Error)
	assert.Nil(t, stored.AssignedUserID, "SLA auto-close must free the contact, same as a manual close")
	assert.Equal(t, models.ContactStatusResolved, stored.ContactStatus)
}

// --- escalateTransfers: skipped when agent active ---

func TestSLAEscalationSkippedWhenAgentActive(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	escalationMinutes := 30
	// Realistic flow: transfer started ~35 min ago, original escalation was
	// due 5 min ago. Without forcing transferred_at into the past, GORM's
	// autoCreate sets it to now and the agent's 10-min-old reply ends up
	// "before" the transfer existed — which the variant-2 semantic
	// correctly treats as "not a response to this transfer."
	escalationAt := time.Now().Add(-5 * time.Minute)
	transferredAt := time.Now().Add(-35 * time.Minute)
	transfer := createSLATestTransfer(t, app, org.ID, contact.ID, agent.ID, account.Name, models.SLATracking{
		EscalationAt:    &escalationAt,
		EscalationLevel: 0,
	})
	require.NoError(t, app.DB.Model(transfer).Update("transferred_at", transferredAt).Error)

	// Agent sent a message 10 minutes ago (after the transfer started)
	createTestAgentMessage(t, app, org.ID, contact.ID, agent.ID, account.Name, time.Now().Add(-10*time.Minute))

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA: models.SLAConfig{
			Enabled:           true,
			EscalationMinutes: escalationMinutes,
		},
	}

	proc := NewSLAProcessor(app, time.Minute)
	proc.escalateTransfers(org.ID, settings, time.Now())

	// Reload transfer — should still be at escalation level 0 with extended deadline
	var updated models.AgentTransfer
	require.NoError(t, app.DB.Where("id = ?", transfer.ID).First(&updated).Error)

	assert.Equal(t, 0, updated.SLA.EscalationLevel, "escalation level should not increase")
	require.NotNil(t, updated.SLA.EscalationAt)
	assert.True(t, updated.SLA.EscalationAt.After(time.Now().Add(time.Duration(escalationMinutes-1)*time.Minute)),
		"escalation_at should be extended into the future")
}

// --- closeInactiveAttendances ---

func TestCloseInactiveAttendances(t *testing.T) {
	t.Run("closes an attendance idle beyond the threshold and frees the contact", func(t *testing.T) {
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		idle := time.Now().Add(-90 * time.Minute)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"last_message_at":  idle,
		}).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  idle,
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		proc := NewSLAProcessor(app, time.Minute)
		proc.closeInactiveAttendances(org.ID, settings, time.Now())

		var storedTransfer models.AgentTransfer
		require.NoError(t, app.DB.First(&storedTransfer, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusExpired, storedTransfer.Status)

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.Nil(t, storedContact.AssignedUserID)
		assert.Equal(t, models.ContactStatusResolved, storedContact.ContactStatus)
	})

	t.Run("leaves a recently active attendance alone", func(t *testing.T) {
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("last_message_at", time.Now().Add(-5*time.Minute)).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now(),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		proc := NewSLAProcessor(app, time.Minute)
		proc.closeInactiveAttendances(org.ID, settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status)
	})

	t.Run("leaves a soft-deleted contact's attendance untouched", func(t *testing.T) {
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		idle := time.Now().Add(-90 * time.Minute)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"last_message_at":  idle,
			"contact_status":   models.ContactStatusInProgress,
		}).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  idle,
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		// Soft-delete the contact without closing its active transfer —
		// mirrors DeleteContact, which does not release active transfers.
		require.NoError(t, app.DB.Delete(contact).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		proc := NewSLAProcessor(app, time.Minute)
		proc.closeInactiveAttendances(org.ID, settings, time.Now())

		var storedTransfer models.AgentTransfer
		require.NoError(t, app.DB.First(&storedTransfer, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, storedTransfer.Status,
			"a soft-deleted contact's transfer must not be swept up by the inactivity closer")

		var storedContact models.Contact
		require.NoError(t, app.DB.Unscoped().First(&storedContact, "id = ?", contact.ID).Error)
		assert.NotNil(t, storedContact.AssignedUserID, "soft-deleted contact must be left untouched")
		assert.Equal(t, models.ContactStatusInProgress, storedContact.ContactStatus)
	})

	t.Run("leaves a freshly opened attendance alone", func(t *testing.T) {
		// An agent picks up a conversation that has been quiet for 90 minutes.
		// Opening an attendance writes no message, so contacts.last_message_at
		// alone would have the very next tick close it and tell the customer
		// they were inactive. The attendance itself must also be older than the
		// threshold before it can be closed.
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"last_message_at":  time.Now().Add(-90 * time.Minute),
		}).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now(),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		proc := NewSLAProcessor(app, time.Minute)
		proc.closeInactiveAttendances(org.ID, settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status,
			"an attendance opened moments ago must not be closed for customer inactivity")

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.NotNil(t, storedContact.AssignedUserID, "the contact must stay with its agent")
	})

	t.Run("leaves a just-picked-up attendance alone even when the transfer is old", func(t *testing.T) {
		// A transfer sat in a queue for 90 minutes, then an agent picked it up a
		// minute ago. Both transferred_at and last_message_at are well past the
		// window, but picked_up_at is recent — closing here would sweep the
		// attendance out from under the agent who just took it and tell the
		// customer they were inactive.
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		old := time.Now().Add(-90 * time.Minute)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"last_message_at":  old,
		}).Error)

		pickedUp := time.Now().Add(-1 * time.Minute)
		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  old,
			SLA:            models.SLATracking{PickedUpAt: &pickedUp},
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60

		proc := NewSLAProcessor(app, time.Minute)
		proc.closeInactiveAttendances(org.ID, settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status,
			"a just-picked-up attendance must not be closed even when the transfer is old")

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.NotNil(t, storedContact.AssignedUserID, "the contact must stay with the agent who just picked up")
	})

	// idleSwept builds an org + a stale, idle attendance ready to be closed by
	// the inactivity sweep, so the gating subtests differ only in settings.
	idleSwept := func(t *testing.T) (*App, models.ChatbotSettings, uuid.UUID) {
		t.Helper()
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

		idle := time.Now().Add(-90 * time.Minute)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"last_message_at":  idle,
		}).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  idle,
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 60
		return app, settings, transfer.ID
	}

	t.Run("does not run at all when client inactivity is disabled", func(t *testing.T) {
		// The sweep now has its own opt-in gate, CloseInactiveAttendances
		// (default false). With everything off, nothing may close.
		app, settings, transferID := idleSwept(t)
		settings.ClientInactivity.ReminderEnabled = false
		settings.ClientInactivity.CloseInactiveAttendances = false

		proc := NewSLAProcessor(app, time.Minute)
		proc.processOrganizationSLA(settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transferID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status,
			"the inactivity pass must be opt-in: nothing may close while it is off")
	})

	t.Run("does not fire when close_inactive_attendances is off even with reminders on", func(t *testing.T) {
		// Enabling chatbot reminders must not drag the human-attendance sweep
		// along: the sweep rides only CloseInactiveAttendances.
		app, settings, transferID := idleSwept(t)
		settings.ClientInactivity.ReminderEnabled = true
		settings.ClientInactivity.CloseInactiveAttendances = false

		proc := NewSLAProcessor(app, time.Minute)
		proc.processOrganizationSLA(settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transferID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status,
			"reminders on but sweep off: the attendance must stay active")
	})

	t.Run("fires when close_inactive_attendances is on", func(t *testing.T) {
		app, settings, transferID := idleSwept(t)
		settings.ClientInactivity.ReminderEnabled = false
		settings.ClientInactivity.CloseInactiveAttendances = true

		proc := NewSLAProcessor(app, time.Minute)
		proc.processOrganizationSLA(settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transferID).Error)
		assert.Equal(t, models.TransferStatusExpired, stored.Status,
			"the sweep must fire when its own flag is on")
	})

	t.Run("does nothing when auto-close is disabled", func(t *testing.T) {
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Update("last_message_at", time.Now().Add(-10*time.Hour)).Error)

		transfer := models.AgentTransfer{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			OrganizationID: org.ID,
			ContactID:      contact.ID,
			PhoneNumber:    contact.PhoneNumber,
			Status:         models.TransferStatusActive,
			Source:         models.TransferSourceManual,
			AgentID:        &agent.ID,
			TransferredAt:  time.Now().Add(-10 * time.Hour),
		}
		require.NoError(t, app.DB.Create(&transfer).Error)

		settings := models.ChatbotSettings{OrganizationID: org.ID}
		settings.ClientInactivity.AutoCloseMinutes = 0

		proc := NewSLAProcessor(app, time.Minute)
		proc.closeInactiveAttendances(org.ID, settings, time.Now())

		var stored models.AgentTransfer
		require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, stored.Status)
	})
}

// --- escalateTransfers: fires when no agent response ---

func TestSLAEscalationFiresWhenNoAgentResponse(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	// Escalation was due 5 minutes ago, no agent messages
	escalationAt := time.Now().Add(-5 * time.Minute)
	transfer := createSLATestTransfer(t, app, org.ID, contact.ID, agent.ID, account.Name, models.SLATracking{
		EscalationAt:    &escalationAt,
		EscalationLevel: 0,
	})

	settings := models.ChatbotSettings{
		OrganizationID: org.ID,
		SLA: models.SLAConfig{
			Enabled:           true,
			EscalationMinutes: 30,
		},
	}

	proc := NewSLAProcessor(app, time.Minute)
	proc.escalateTransfers(org.ID, settings, time.Now())

	// Reload transfer — should be escalated to level 1
	var updated models.AgentTransfer
	require.NoError(t, app.DB.Where("id = ?", transfer.ID).First(&updated).Error)

	assert.Equal(t, 1, updated.SLA.EscalationLevel, "escalation level should increase to 1")
	require.NotNil(t, updated.SLA.EscalatedAt)
}

// --- processStaleTransfers: full tick (org selection + pass gating) ---
//
// These exercise the real path the fix touches: getSLAEnabledSettingsCached must
// now load an org that opted into the inactivity sweep without SLA, and
// processOrganizationSLA must still gate every SLA-only pass behind SLA.Enabled.

// persistSLASettings wipes the (global, org-agnostic-keyed) chatbot_settings
// table, invalidates the shared SLA settings cache, and stores exactly one
// settings row, so a processStaleTransfers tick visits only this org. The cache
// is invalidated again after the write so the tick re-reads from the DB.
func persistSLASettings(t *testing.T, app *App, settings models.ChatbotSettings) {
	t.Helper()
	if app.Redis == nil {
		t.Skip("TEST_REDIS_URL not set; a processor tick reads the SLA settings cache")
	}
	require.NoError(t, app.DB.Exec("DELETE FROM chatbot_settings").Error)
	if settings.ID == uuid.Nil {
		settings.ID = uuid.New()
	}
	require.NoError(t, app.DB.Create(&settings).Error)
	app.InvalidateSLASettingsCache()
}

// TestProcessTick_SweepRunsForSLADisabledOrg is the core of the fix: an org with
// SLA off but close_inactive_attendances on must have its idle attendances swept
// by a tick. Before the fix, getSLAEnabledSettingsCached filtered WHERE
// sla_enabled = true, so this org was never loaded and the sweep silently never
// ran — the toggle that lied.
func TestProcessTick_SweepRunsForSLADisabledOrg(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	idle := time.Now().Add(-90 * time.Minute)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID,
		"last_message_at":  idle,
	}).Error)

	transfer := models.AgentTransfer{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		ContactID:      contact.ID,
		PhoneNumber:    contact.PhoneNumber,
		Status:         models.TransferStatusActive,
		Source:         models.TransferSourceManual,
		AgentID:        &agent.ID,
		TransferredAt:  idle,
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	settings := models.ChatbotSettings{OrganizationID: org.ID}
	settings.SLA.Enabled = false
	settings.ClientInactivity.CloseInactiveAttendances = true
	settings.ClientInactivity.AutoCloseMinutes = 60
	persistSLASettings(t, app, settings)

	proc := NewSLAProcessor(app, time.Minute)
	proc.processStaleTransfers()

	var stored models.AgentTransfer
	require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
	assert.Equal(t, models.TransferStatusExpired, stored.Status,
		"an SLA-disabled org with the sweep flag on must have idle attendances closed by a tick")
}

// TestProcessTick_SLAPassSkippedForSLADisabledOrg guards the trap: widening the
// org query must not switch the SLA-only passes on for orgs that never opted
// into SLA. The org here IS loaded (its sweep flag is on) and the tick runs, but
// its expired transfer — which autoCloseExpiredTransfers would close if SLA were
// on — must be left untouched. The transfer is deliberately not sweep-eligible
// (fresh transferred_at / last_message_at), so the only thing that could close
// it is the SLA pass, and it must not.
func TestProcessTick_SLAPassSkippedForSLADisabledOrg(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)

	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID,
		"last_message_at":  time.Now(), // fresh — not eligible for the inactivity sweep
	}).Error)

	// Expired an hour ago, no agent reply → autoCloseExpiredTransfers would close
	// it were the SLA passes to run. createSLATestTransfer leaves transferred_at
	// at "now", so the inactivity sweep cannot match it either.
	expiresAt := time.Now().Add(-1 * time.Hour)
	transfer := createSLATestTransfer(t, app, org.ID, contact.ID, agent.ID, account.Name, models.SLATracking{
		ExpiresAt: &expiresAt,
	})

	settings := models.ChatbotSettings{OrganizationID: org.ID}
	settings.SLA.Enabled = false   // SLA off
	settings.SLA.AutoCloseHours = 2 // would auto-close the expired transfer if the pass ran
	settings.ClientInactivity.CloseInactiveAttendances = true // org loaded only for the sweep
	settings.ClientInactivity.AutoCloseMinutes = 60
	persistSLASettings(t, app, settings)

	proc := NewSLAProcessor(app, time.Minute)
	proc.processStaleTransfers()

	var stored models.AgentTransfer
	require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
	assert.Equal(t, models.TransferStatusActive, stored.Status,
		"autoCloseExpiredTransfers is an SLA-only pass: it must not run for an SLA-disabled org, even one loaded for the sweep")
}

// TestProcessTick_ClosesOnceWhenBothEnabled verifies that an org with both
// sla_enabled and the sweep flag on closes an idle, SLA-expired attendance
// exactly once. Both the SLA auto-close pass and the inactivity sweep are
// candidates for this transfer; the sweep re-queries status = active, so the
// already-committed close leaves nothing for it to redo. The close note carries
// a per-pass marker, so exactly one "[Auto-closed:" marker proves a single close
// (no double customer message / double broadcast).
func TestProcessTick_ClosesOnceWhenBothEnabled(t *testing.T) {
	app := newSLATestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)

	idle := time.Now().Add(-90 * time.Minute)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID,
		"last_message_at":  idle,
	}).Error)

	expiresAt := time.Now().Add(-1 * time.Hour)
	transfer := models.AgentTransfer{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		ContactID:      contact.ID,
		PhoneNumber:    contact.PhoneNumber,
		Status:         models.TransferStatusActive,
		Source:         models.TransferSourceManual,
		AgentID:        &agent.ID,
		TransferredAt:  idle,
		SLA:            models.SLATracking{ExpiresAt: &expiresAt},
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	settings := models.ChatbotSettings{OrganizationID: org.ID}
	settings.SLA.Enabled = true
	settings.SLA.AutoCloseHours = 2
	settings.ClientInactivity.CloseInactiveAttendances = true
	settings.ClientInactivity.AutoCloseMinutes = 60
	persistSLASettings(t, app, settings)

	proc := NewSLAProcessor(app, time.Minute)
	proc.processStaleTransfers()

	var stored models.AgentTransfer
	require.NoError(t, app.DB.First(&stored, "id = ?", transfer.ID).Error)
	assert.Equal(t, models.TransferStatusExpired, stored.Status, "the attendance must be closed")
	assert.Equal(t, 1, strings.Count(stored.Notes, "[Auto-closed:"),
		"an attendance closed by one tick must carry exactly one auto-close marker — no double close")

	var storedContact models.Contact
	require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
	assert.Nil(t, storedContact.AssignedUserID, "the contact must be released exactly once")
}
