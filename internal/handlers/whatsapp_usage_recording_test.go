package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	wausage "github.com/shridarpatil/whatomate/internal/usage"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type recordingResp struct {
	Enabled       bool       `json:"enabled"`
	ServerEnabled bool       `json:"server_enabled"`
	PanelEnabled  bool       `json:"panel_enabled"`
	ChangedAt     *time.Time `json:"changed_at"`
	Since         *time.Time `json:"since"`
}

func (f *apiFixture) setRecording(t *testing.T, u *models.User, enabled bool) (int, recordingResp) {
	t.Helper()
	code, body := f.call(t, f.app.SetWhatsAppUsageRecording, u, map[string]any{"enabled": enabled}, nil, nil)
	var r recordingResp
	if code == fasthttp.StatusOK {
		r = decodeData[recordingResp](t, body)
	}
	return code, r
}

func (f *apiFixture) summaryRecording(t *testing.T) recordingResp {
	t.Helper()
	_, body := f.call(t, f.app.GetWhatsAppUsageSummary, f.reader, nil, nil, nil)
	return decodeData[struct {
		Recording recordingResp `json:"recording"`
	}](t, body).Recording
}

func TestRecordingSwitchAPI_OnlyAWriterCanFlipIt(t *testing.T) {
	f := newAPIFixture(t)
	for name, u := range map[string]*models.User{"reader": f.reader, "nobody": f.nobody} {
		code, _ := f.setRecording(t, u, false)
		assert.Equal(t, fasthttp.StatusForbidden, code, name)
	}
	assert.True(t, f.summaryRecording(t).Enabled, "nothing changed")
	code, r := f.setRecording(t, f.writer, false)
	require.Equal(t, fasthttp.StatusOK, code)
	assert.False(t, r.Enabled)
}

func TestRecordingSwitchAPI_TheSummaryReportsEachSwitchAndWhenItChanged(t *testing.T) {
	f := newAPIFixture(t)
	r := f.summaryRecording(t)
	assert.True(t, r.Enabled)
	assert.True(t, r.ServerEnabled)
	assert.True(t, r.PanelEnabled)
	assert.Nil(t, r.ChangedAt, "never touched")

	_, off := f.setRecording(t, f.writer, false)
	assert.False(t, off.Enabled)
	assert.False(t, off.PanelEnabled)
	assert.True(t, off.ServerEnabled)
	require.NotNil(t, off.ChangedAt)

	r = f.summaryRecording(t)
	assert.False(t, r.Enabled)
	assert.False(t, r.PanelEnabled)
	assert.NotNil(t, r.ChangedAt)

	_, on := f.setRecording(t, f.writer, true)
	assert.True(t, on.Enabled)
	require.NotNil(t, on.ChangedAt)
	assert.True(t, on.ChangedAt.After(*off.ChangedAt) || on.ChangedAt.Equal(*off.ChangedAt))
}

func TestRecordingSwitchAPI_FlippingToTheStateItAlreadyHasChangesNothing(t *testing.T) {
	f := newAPIFixture(t)
	_, first := f.setRecording(t, f.writer, false)
	time.Sleep(20 * time.Millisecond)
	_, second := f.setRecording(t, f.writer, false)
	require.NotNil(t, first.ChangedAt)
	require.NotNil(t, second.ChangedAt)
	assert.True(t, first.ChangedAt.Equal(*second.ChangedAt), "the instant of the real change is kept")

	// turning on an organization that never touched it does not write anything either
	g := newAPIFixture(t)
	_, r := g.setRecording(t, g.writer, true)
	assert.Nil(t, r.ChangedAt)
}

func TestRecordingSwitchAPI_IsPerOrganization(t *testing.T) {
	f := newAPIFixture(t)
	other := newAPIFixture(t)
	f.setRecording(t, f.writer, false)
	assert.True(t, other.summaryRecording(t).Enabled)
	assert.False(t, f.summaryRecording(t).Enabled)
}

func TestRecordingSwitchAPI_RefusesToTurnOnWhatTheServerTurnedOff(t *testing.T) {
	f := newAPIFixture(t)
	off := false
	f.app.Usage = wausage.New(f.app.DB, config.UsageConfig{RecordEnabled: &off})
	code, _ := f.setRecording(t, f.writer, true)
	assert.Equal(t, fasthttp.StatusConflict, code)
	r := f.summaryRecording(t)
	assert.False(t, r.Enabled)
	assert.False(t, r.ServerEnabled)
}

func TestRecordingSwitchAPI_RequiresTheFieldAndIsAudited(t *testing.T) {
	f := newAPIFixture(t)
	code, _ := f.call(t, f.app.SetWhatsAppUsageRecording, f.writer, map[string]any{}, nil, nil)
	assert.Equal(t, fasthttp.StatusBadRequest, code)

	f.setRecording(t, f.writer, false)
	var n int64
	require.Eventually(t, func() bool {
		f.app.DB.Model(&models.AuditLog{}).Where("organization_id = ? AND resource_type = ? AND user_id = ?", f.org.ID, "whatsapp_usage", f.writer.ID).Count(&n)
		return n == 1
	}, 3*time.Second, 50*time.Millisecond, "who turned the measurement off is on record")
}

// With the panel switch off nothing is recorded anywhere, and what the switch guards
// never changes how the message flows.
func TestRecordingSwitchOff_TheSendStillWorksAndNothingIsRecorded(t *testing.T) {
	f := newUsageSendFixture(t, config.UsageConfig{})
	_, err := f.app.Usage.SetRecording(context.Background(), f.org.ID, false, time.Now())
	require.NoError(t, err)

	agentID := makeAgentUser(t, f.app, f.org.ID, "Lia")
	opts := handlers.DefaultSendOptions()
	opts.SentByUserID = &agentID
	msg := f.send(t, testutil.TestContext(t), opts, nil)

	assert.Empty(t, f.usageRows(t))
	require.Len(t, f.mock.sentMessages, 1)
	var stored models.Message
	require.NoError(t, f.app.DB.First(&stored, msg.ID).Error)
	assert.Equal(t, models.MessageStatusSent, stored.Status)
	var c models.Contact
	require.NoError(t, f.app.DB.First(&c, f.contact.ID).Error)
	assert.Equal(t, models.ContactStatusInProgress, c.ContactStatus, "the status transition still happened")
	var n int64
	f.app.DB.Model(&models.ContactStatusEvent{}).Where("contact_id = ?", f.contact.ID).Count(&n)
	assert.Zero(t, n, "and its history is not written while the measurement is off")

	// turned on again, the next send is recorded
	_, err = f.app.Usage.SetRecording(context.Background(), f.org.ID, true, time.Now())
	require.NoError(t, err)
	f.send(t, testutil.TestContext(t), handlers.ChatbotSendOptions(), nil)
	assert.Len(t, f.usageRows(t), 1)
}
