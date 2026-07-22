package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveButtonClose_DispatchesTransferWebhook guards Finding 3: the
// resolve-button close used to skip the lifecycle webhook, so integrations
// tracking attendance lifecycle never saw resolve-button closes. Now every
// close path dispatches it exactly once through closeAttendance.
func TestResolveButtonClose_DispatchesTransferWebhook(t *testing.T) {
	app := newSLATestApp(t)
	app.HTTPClient = &http.Client{Timeout: 5 * time.Second}

	org := testutil.CreateTestOrganization(t, app.DB)
	agent := testutil.CreateTestUser(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
		"assigned_user_id": agent.ID,
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
		TransferredAt:  time.Now(),
	}
	require.NoError(t, app.DB.Create(&transfer).Error)

	// Capture the dispatched webhook event on a local server.
	received := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(body, &payload)
		select {
		case received <- payload.Event:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, app.DB.Create(&models.Webhook{
		BaseModel:      models.BaseModel{ID: uuid.New()},
		OrganizationID: org.ID,
		Name:           "lifecycle-hook",
		URL:            srv.URL,
		Events:         models.StringArray{string(models.WebhookEventTransferResumed)},
		IsActive:       true,
	}).Error)

	require.NoError(t, app.closeActiveAttendanceForContact(contact, &agent.ID, "resolved"))

	// DispatchWebhook delivers asynchronously, tracked on app.wg.
	app.wg.Wait()

	select {
	case event := <-received:
		assert.Equal(t, string(models.WebhookEventTransferResumed), event,
			"resolve-button close must dispatch the transfer lifecycle webhook")
	case <-time.After(2 * time.Second):
		t.Fatal("resolve-button close did not dispatch the transfer webhook")
	}
}

// TestCloseAttendance_TransactionAndNilContact guards Finding 4: the close is
// atomic (a failure anywhere in the transaction leaves the attendance active
// and the contact untouched, never a half-closed conversation), and a nil
// contact closes the transfer without panicking.
func TestCloseAttendance_TransactionAndNilContact(t *testing.T) {
	t.Run("a successful close commits both writes", func(t *testing.T) {
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"contact_status":   models.ContactStatusInProgress,
		}).Error)
		contact.AssignedUserID = &agent.ID
		contact.ContactStatus = models.ContactStatusInProgress

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

		err := app.closeAttendance(&transfer, contact, map[string]any{
			"status":     models.TransferStatusResumed,
			"resumed_at": time.Now(),
		}, &agent.ID, "resolved", closeAttendanceOptions{})
		require.NoError(t, err)

		var storedTransfer models.AgentTransfer
		require.NoError(t, app.DB.First(&storedTransfer, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusResumed, storedTransfer.Status, "transfer must be closed")

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.Nil(t, storedContact.AssignedUserID, "contact must be released")
		assert.Equal(t, models.ContactStatusResolved, storedContact.ContactStatus, "contact must be resolved")
	})

	t.Run("a failure in the transaction rolls back the transfer close", func(t *testing.T) {
		// Force the shared close transaction to error with a bogus update column.
		// Both the transfer update and the contact release live in one
		// transaction, so a failure anywhere must leave the transfer active and
		// the contact untouched — never a half-closed conversation nobody owns.
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)
		require.NoError(t, app.DB.Model(contact).Updates(map[string]any{
			"assigned_user_id": agent.ID,
			"contact_status":   models.ContactStatusInProgress,
		}).Error)
		contact.AssignedUserID = &agent.ID
		contact.ContactStatus = models.ContactStatusInProgress

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

		err := app.closeAttendance(&transfer, contact, map[string]any{
			"status":                        models.TransferStatusResumed,
			"nonexistent_column_to_fail_tx": true,
		}, &agent.ID, "resolved", closeAttendanceOptions{})
		require.Error(t, err, "a broken transaction must surface an error")

		var storedTransfer models.AgentTransfer
		require.NoError(t, app.DB.First(&storedTransfer, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusActive, storedTransfer.Status,
			"the transfer must NOT be left closed when the transaction fails")

		var storedContact models.Contact
		require.NoError(t, app.DB.First(&storedContact, "id = ?", contact.ID).Error)
		assert.NotNil(t, storedContact.AssignedUserID, "the contact must not be released on a failed close")
		assert.Equal(t, models.ContactStatusInProgress, storedContact.ContactStatus,
			"the contact status must not persist a change on a failed close")
		// The in-memory contact.ContactStatus is restored to its previous value
		// on the error path (see closeAttendance: `contact.ContactStatus =
		// previousStatus`), so a caller holding the struct never sees a status
		// that never committed.
		assert.Equal(t, models.ContactStatusInProgress, contact.ContactStatus,
			"in-memory contact status must be restored on the error path")
	})

	t.Run("closes the transfer with a nil contact and does not panic", func(t *testing.T) {
		app := newSLATestApp(t)
		org := testutil.CreateTestOrganization(t, app.DB)
		agent := testutil.CreateTestUser(t, app.DB, org.ID)
		contact := testutil.CreateTestContact(t, app.DB, org.ID)

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

		var err error
		require.NotPanics(t, func() {
			err = app.closeAttendance(&transfer, nil, map[string]any{
				"status":     models.TransferStatusResumed,
				"resumed_at": time.Now(),
			}, nil, "nil-contact close", closeAttendanceOptions{})
		})
		require.NoError(t, err)

		var storedTransfer models.AgentTransfer
		require.NoError(t, app.DB.First(&storedTransfer, "id = ?", transfer.ID).Error)
		assert.Equal(t, models.TransferStatusResumed, storedTransfer.Status,
			"a nil-contact close must still close the transfer")
	})
}
