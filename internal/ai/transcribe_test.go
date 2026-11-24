package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscribe_SendsAMultipartUploadAndReadsTheText(t *testing.T) {
	var gotPath, gotAuth, gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotCT = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"text":"  quero porcelanato para a sala  "}`))
	}))
	defer srv.Close()
	p, err := New(ProviderGroq, Config{APIKey: "gsk-secret", BaseURL: srv.URL})
	require.NoError(t, err)
	tr, ok := p.(Transcriber)
	require.True(t, ok, "Groq can transcribe")

	text, err := tr.Transcribe(context.Background(), []byte("OggS-fake-audio"), "nota.ogg", "audio/ogg")

	require.NoError(t, err)
	assert.Equal(t, "quero porcelanato para a sala", text)
	assert.Equal(t, "/audio/transcriptions", gotPath)
	assert.Equal(t, "Bearer gsk-secret", gotAuth)
	assert.True(t, strings.HasPrefix(gotCT, "multipart/form-data; boundary="))
	assert.Contains(t, gotBody, "whisper-large-v3-turbo")
	assert.Contains(t, gotBody, `name="language"`)
	assert.Contains(t, gotBody, "OggS-fake-audio")
	assert.Contains(t, gotBody, `filename="nota.ogg"`)
}

func TestTranscribe_ProviderErrorsAreClassifiedAndTheKeyIsNeverShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key gsk-secret"}}`))
	}))
	defer srv.Close()
	p, _ := New(ProviderGroq, Config{APIKey: "gsk-secret", BaseURL: srv.URL})

	_, err := p.(Transcriber).Transcribe(context.Background(), []byte("x"), "a.ogg", "audio/ogg")

	var aerr *Error
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, KindAuth, aerr.Kind)
	assert.NotContains(t, aerr.Message, "gsk-secret")
}

func TestTranscriptionModel_OnlyOpenAICompatibleProvidersHaveOne(t *testing.T) {
	assert.Equal(t, "whisper-large-v3-turbo", TranscriptionModel(ProviderGroq))
	assert.Equal(t, "whisper-1", TranscriptionModel(ProviderOpenAI))
	assert.Empty(t, TranscriptionModel(ProviderAnthropic))
	assert.Empty(t, TranscriptionModel(ProviderGoogle))
	for _, name := range []string{ProviderAnthropic, ProviderGoogle} {
		p, err := New(name, Config{APIKey: "k"})
		require.NoError(t, err)
		_, ok := p.(Transcriber)
		assert.False(t, ok, name+" is not wired for speech-to-text")
	}
}
