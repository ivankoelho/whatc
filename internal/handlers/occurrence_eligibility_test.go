package handlers_test

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Real sending stays off until someone turns it on, and even then only reaches
// customers who started the conversation.
func TestEligibility_TheMasterSwitchHoldsEverythingBack(t *testing.T) {
	w := newNotifWorld(t)
	w.app.Config.WhatsApp.AutomationSendingEnabled = false
	id := w.forward(t, nil)

	assert.Equal(t, 0, w.run(), "nothing is claimed while the switch is off")
	assert.Zero(t, w.meta.count())
	assert.Equal(t, models.NotificationPending, w.intent(t, id).Status, "the intent waits, intact")

	// The screen can tell: the listing carries the switch.
	code, body := w.call(t, w.app.ListOccurrenceChannelConfigs, w.admin.ID, id, nil, nil)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.Contains(t, string(body), `"sending_enabled":false`)

	// A cancelled intent stays cancelled when it is switched on later.
	require.NoError(t, w.app.DB.Model(&models.OccurrenceNotification{}).Where("occurrence_id = ?", id).Update("status", models.NotificationCancelled).Error)
	w.app.Config.WhatsApp.AutomationSendingEnabled = true
	assert.Equal(t, 0, w.run())
	assert.Zero(t, w.meta.count())
}

func TestEligibility_OnlyCustomersWhoWroteToUsAreContacted(t *testing.T) {
	w := newNotifWorld(t)

	// A contact that never wrote (e.g. just registered by an agent): skipped.
	contact := testutil.CreateTestContact(t, w.app.DB, w.org.ID)
	req := testutil.NewJSONRequest(t, map[string]any{"contact_id": contact.ID.String(), "title": "Sem contato prévio",
		"team_id": w.teamPorto.ID.String(), "order_ref": "1"})
	testutil.SetAuthContext(req, w.org.ID, w.sac.ID)
	require.NoError(t, w.app.CreateOccurrence(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))
	var occ models.Occurrence
	require.NoError(t, w.app.DB.Where("contact_id = ?", contact.ID).First(&occ).Error)
	w.run()
	n := w.intent(t, occ.ID)
	assert.Equal(t, models.NotificationSkipped, n.Status)
	assert.Equal(t, "no_customer_initiated_contact", n.Reason)
	assert.Zero(t, w.meta.count())

	// An old message does not count either: the window is 90 days.
	old := w.customerWrote(t, contact.ID)
	require.NoError(t, w.app.DB.Model(old).Update("created_at", time.Now().Add(-100*24*time.Hour)).Error)
	require.NoError(t, w.app.DB.Model(&models.OccurrenceNotification{}).Where("id = ?", n.ID).Updates(map[string]any{"status": models.NotificationPending, "reason": ""}).Error)
	w.run()
	assert.Equal(t, "no_customer_initiated_contact", w.intent(t, occ.ID).Reason)

	// A recent one does, and the basis is recorded with its evidence.
	fresh := w.customerWrote(t, contact.ID)
	require.NoError(t, w.app.DB.Model(&models.OccurrenceNotification{}).Where("id = ?", n.ID).Updates(map[string]any{"status": models.NotificationPending, "reason": ""}).Error)
	w.run()
	got := w.intent(t, occ.ID)
	assert.Equal(t, models.NotificationAccepted, got.Status)
	assert.Equal(t, "customer_initiated", got.ConsentBasis)
	assert.Equal(t, fresh.ID.String(), got.ConsentEvidence)
	assert.Equal(t, "order_service_update", got.Purpose)
	assert.Equal(t, 1, w.meta.count())
}
