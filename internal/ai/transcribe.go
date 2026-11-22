package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// MaxAudioBytes is the largest voice note sent for transcription (a WhatsApp voice note is far smaller; the
// providers accept 25 MB).
const MaxAudioBytes = 10 << 20

// Transcriber is implemented by the providers whose API turns speech into text.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, filename, mime string) (string, error)
}

// TranscriptionModel is the speech-to-text model used for a provider, or "" when the provider has none here
// (Anthropic and Google are not wired for it).
func TranscriptionModel(provider string) string {
	switch strings.ToLower(provider) {
	case ProviderGroq:
		return "whisper-large-v3-turbo"
	case ProviderOpenAI:
		return "whisper-1"
	}
	return ""
}

// Transcribe sends the audio to the provider's OpenAI-compatible /audio/transcriptions endpoint. The language
// is fixed to Portuguese: it is the customers' language and it makes the short voice notes much more reliable.
func (p *openAICompat) Transcribe(ctx context.Context, audio []byte, filename, mime string) (string, error) {
	model := TranscriptionModel(p.name)
	if model == "" {
		return "", &Error{Provider: p.name, Kind: KindInvalidRequest, Message: "no transcription model for this provider"}
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", model)
	_ = w.WriteField("language", "pt")
	_ = w.WriteField("response_format", "json")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+escapeQuotes(filename)+`"`)
	if mime == "" {
		mime = "application/octet-stream"
	}
	h.Set("Content-Type", mime)
	part, err := w.CreatePart(h)
	if err != nil {
		return "", &Error{Provider: p.name, Kind: KindInvalidRequest, Message: "failed to encode request"}
	}
	if _, err := part.Write(audio); err != nil {
		return "", &Error{Provider: p.name, Kind: KindInvalidRequest, Message: "failed to encode request"}
	}
	if err := w.Close(); err != nil {
		return "", &Error{Provider: p.name, Kind: KindInvalidRequest, Message: "failed to encode request"}
	}
	raw, derr := doBody(ctx, p.client, p.name, p.apiKey, "POST", p.baseURL+"/audio/transcriptions", p.headers(), &buf, w.FormDataContentType())
	if derr != nil {
		return "", derr
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", &Error{Provider: p.name, Kind: KindInvalidRequest, Message: "unreadable transcription answer"}
	}
	return strings.TrimSpace(out.Text), nil
}

func escapeQuotes(s string) string { return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s) }
