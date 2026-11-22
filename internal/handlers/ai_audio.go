package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/ai"
	"github.com/shridarpatil/whatomate/internal/models"
)

// Voice notes for the AI agent. A customer who answers with a voice note used to get no answer at all: the audio
// was saved for the attendant and the chatbot saw no text. When an agent is in charge of the number, the note is
// transcribed and the text goes through exactly the path a typed message takes (wait, history, qualification,
// handoff, cap). If it cannot be transcribed the customer is asked once to write instead.

const (
	aiFeatureTranscription = "audio_transcription"
	// aiTranscriptionEstimate is the tokens reserved against the monthly cap for one voice note. Speech-to-text
	// is billed by audio length, not tokens: what is recorded afterwards is the size of the text, in tokens.
	aiTranscriptionEstimate = 300
	aiAudioFallbackMessage  = "Não consegui ouvir seu áudio. Pode escrever sua mensagem?"
)

var (
	errAudioUnsupported = errors.New("the AI provider has no speech-to-text here")
	errAudioTooLarge    = errors.New("the voice note is too large to transcribe")
	errAudioEmpty       = errors.New("the transcription came back empty")
)

// transcribeAudio turns a saved voice note into text. It runs under the same rules as an agent's model call: the
// monthly cap (limit, or the explicit acceptance of none) is enforced before the provider is reached and what the
// call used is recorded in the usage log.
func (a *App) transcribeAudio(ctx context.Context, settings *models.ChatbotSettings, meta aiCallMeta, relPath, mime string) (string, error) {
	provider := string(settings.AI.Provider)
	meta.Feature, meta.Agent = aiFeatureTranscription, true
	model := ai.TranscriptionModel(provider)

	ctx, cancel := context.WithTimeout(ctx, aiCallDeadline)
	defer cancel()
	if limit := settings.AI.MonthlyTokenLimit; limit > 0 {
		res, err := a.reserveAITokens(meta.OrgID, limit, aiTranscriptionEstimate, time.Now())
		if err != nil {
			_ = a.recordAIUsage(meta, provider, model, nil, 0, err)
			return "", err
		}
		meta.reservation = res
		defer res.Settle()
	} else if !settings.AI.UnlimitedAck {
		_ = a.recordAIUsage(meta, provider, model, nil, 0, ErrAICapNotConfigured)
		return "", ErrAICapNotConfigured
	}

	fail := func(err error) (string, error) {
		_ = a.recordAIUsage(meta, provider, model, nil, 0, err)
		return "", err
	}
	if model == "" {
		return fail(errAudioUnsupported)
	}
	apiKey, err := a.resolveAIAPIKey(settings)
	if err != nil {
		return fail(err)
	}
	p, err := a.newAIProvider(provider, apiKey)
	if err != nil {
		return fail(err)
	}
	tr, ok := p.(ai.Transcriber)
	if !ok {
		return fail(errAudioUnsupported)
	}

	path := filepath.Join(a.getMediaStoragePath(), relPath)
	info, err := os.Stat(path)
	if err != nil {
		return fail(fmt.Errorf("the voice note is not on disk: %w", err))
	}
	if info.Size() > ai.MaxAudioBytes {
		return fail(errAudioTooLarge)
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		return fail(fmt.Errorf("the voice note could not be read: %w", err))
	}

	start := time.Now()
	text, err := tr.Transcribe(ctx, audio, filepath.Base(relPath), mime)
	if err == nil && text == "" {
		err = errAudioEmpty
	}
	var resp *ai.Response
	if err == nil {
		n := len([]rune(text))/3 + 1
		resp = &ai.Response{Model: model, Usage: ai.Usage{OutputTokens: n, TotalTokens: n}}
	}
	_ = a.recordAIUsage(meta, provider, model, resp, time.Since(start), err)
	return text, err
}

// transcribeIncomingAudio is called for an incoming voice note that carries no text. It returns the transcript
// when there is one, and stop=true when the customer was asked to write instead (nothing more to do with this
// message). When no agent is in charge of the number, or a person already has the conversation, it does nothing
// and the message goes the way it always went (saved for the attendant, no chatbot answer).
func (a *App) transcribeIncomingAudio(account *models.WhatsAppAccount, contact *models.Contact, settings *models.ChatbotSettings, wamid string, media *MediaInfo) (text string, stop bool) {
	agent := a.resolveAIAgent(settings, account)
	if agent == nil || !settings.IsEnabled || !settings.AI.Enabled || media == nil || media.MediaURL == "" {
		return "", false
	}
	if a.humanTookOver(account.OrganizationID, contact.ID) {
		return "", false
	}
	contactID := contact.ID
	meta := aiCallMeta{OrgID: account.OrganizationID, Account: account.Name, ContactID: &contactID}
	text, err := a.transcribeAudio(context.Background(), settings, meta, media.MediaURL, media.MediaMimeType)
	if err != nil {
		a.Log.Warn("Voice note could not be transcribed; asking the customer to write", "error", err, "contact_id", contact.ID)
		if serr := a.sendAndSaveAgentText(aiCtx("ai_audio_fallback"), account, contact, aiAudioFallbackMessage, newAISender(agent)); serr != nil {
			a.Log.Error("Could not ask the customer to write", "error", serr, "contact_id", contact.ID)
		}
		return "", true
	}
	text = strings.TrimSpace(text)
	// The transcript becomes the message's text: the attendant reads it under the audio, and the agent's turn
	// (which rebuilds what the customer said from the messages table) sees it like a typed message.
	if err := a.DB.Model(&models.Message{}).
		Where("organization_id = ? AND whats_app_message_id = ?", account.OrganizationID, wamid).
		Update("content", text).Error; err != nil {
		a.Log.Error("Could not store the transcript on the message", "error", err, "contact_id", contact.ID)
	}
	return text, false
}
