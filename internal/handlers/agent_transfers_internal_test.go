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
