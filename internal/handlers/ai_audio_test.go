package handlers

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audioTransport answers the speech-to-text endpoint itself and leaves every other call to the wrapped one.
type audioTransport struct {
	inner http.RoundTripper
	text  string
	mu    sync.Mutex
	calls int
}

func (a *audioTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
		return a.inner.RoundTrip(r)
	}
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"text":"` + a.text + `"}`)), Request: r}, nil
}

func (a *audioTransport) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

type audioWorld struct {
	*wiring
	tr      *audioTransport
	contact *models.Contact
	wamid   string
	media   *MediaInfo
}

// newAudioWorld has a saved voice note on disk and its (empty-text) message row, like the webhook leaves it.
func newAudioWorld(t *testing.T, transcript string) *audioWorld {
	t.Helper()
	w := newWiring(t, "ok")
	tr := &audioTransport{inner: w.app.HTTPClient.Transport, text: transcript}
	w.app.HTTPClient.Transport = tr
	dir := t.TempDir()
	w.app.Config.Storage.LocalPath = dir
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "audio"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "audio", "nota.ogg"), []byte("OggS-fake"), 0o644))
	contact := testutil.CreateTestContact(t, w.app.DB, w.f.org.ID)
	wamid := "wamid." + uuid.NewString()[:8]
	require.NoError(t, w.app.DB.Create(&models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: w.f.org.ID, ContactID: contact.ID,
		WhatsAppAccount: w.f.account.Name, WhatsAppMessageID: wamid, Direction: models.DirectionIncoming,
		MessageType: models.MessageTypeAudio, Status: models.MessageStatusRead,
	}).Error)
	return &audioWorld{wiring: w, tr: tr, contact: contact, wamid: wamid,
		media: &MediaInfo{MediaURL: filepath.Join("audio", "nota.ogg"), MediaMimeType: "audio/ogg"}}
}

func (w *audioWorld) run() (string, bool) {
	return w.app.transcribeIncomingAudio(w.f.account, w.contact, w.f.settings, w.wamid, w.media)
}

func (w *audioWorld) content(t *testing.T) string {
	t.Helper()
	var m models.Message
	require.NoError(t, w.app.DB.Where("whats_app_message_id = ?", w.wamid).First(&m).Error)
	return m.Content
}

func TestAudio_AgentTranscribesAndTheTextBecomesTheMessage(t *testing.T) {
	w := newAudioWorld(t, "quero porcelanato para a sala")

	text, stop := w.run()

	assert.False(t, stop)
	assert.Equal(t, "quero porcelanato para a sala", text)
	assert.Equal(t, "quero porcelanato para a sala", w.content(t), "the attendant reads the transcript under the audio")
	assert.Equal(t, 1, w.tr.count())
	assert.Empty(t, w.textSends(), "nothing is said to the customer yet: the text now goes the typed-message way")
	var row models.AIUsageLog
	require.NoError(t, w.app.DB.Where("organization_id = ? AND feature = ?", w.f.org.ID, aiFeatureTranscription).First(&row).Error)
	assert.True(t, row.Success)
	assert.Positive(t, row.TotalTokens, "the transcript counts against the usage")
}

func TestAudio_CapReachedAsksTheCustomerToWriteAndNeverCallsTheProvider(t *testing.T) {
	w := newAudioWorld(t, "x")
	w.setCapState(t, 1, false) // below one reservation

	text, stop := w.run()

	assert.True(t, stop)
	assert.Empty(t, text)
	assert.Zero(t, w.tr.count(), "the provider was never reached")
	sent := w.textSends()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0], "Pode escrever sua mensagem")
	assert.Empty(t, w.content(t))
}

func TestAudio_NoCapDecisionIsRefusedLikeEveryOtherAgentCall(t *testing.T) {
	w := newAudioWorld(t, "x")
	w.setCapState(t, 0, false)

	_, stop := w.run()

	assert.True(t, stop)
	assert.Zero(t, w.tr.count())
}

func TestAudio_AProviderWithoutSpeechToTextAsksTheCustomerToWrite(t *testing.T) {
	w := newAudioWorld(t, "x")
	w.f.settings.AI.Provider = models.AIProvider("anthropic")

	_, stop := w.run()

	assert.True(t, stop)
	assert.Zero(t, w.tr.count())
	require.Len(t, w.textSends(), 1)
}

func TestAudio_NumberWithoutAnAgentIsUntouched(t *testing.T) {
	w := newAudioWorld(t, "x")
	w.f.settings.AI.DefaultAgentID = nil

	text, stop := w.run()

	assert.False(t, stop)
	assert.Empty(t, text)
	assert.Zero(t, w.tr.count(), "no agent, no transcription, nothing sent: exactly as before")
	assert.Empty(t, w.textSends())
}

func TestAudio_APersonWhoHasTheConversationIsNeverOverridden(t *testing.T) {
	w := newAudioWorld(t, "x")
	w.openAttendance(t, w.contact.ID)

	text, stop := w.run()

	assert.False(t, stop)
	assert.Empty(t, text)
	assert.Zero(t, w.tr.count(), "no audio goes to the provider while a person is on the conversation")
	assert.Empty(t, w.textSends())
}
