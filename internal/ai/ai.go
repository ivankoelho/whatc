// Package ai is the provider-neutral layer between Whatc's features (chatbot
// today; assistant, classification and automations later) and the LLM vendors.
// Features talk to Provider only; each vendor has an adapter. Swapping or adding
// a provider never touches business code.
//
// Deliberately NOT here: prompt/context assembly (that belongs to the caller),
// credentials storage and decryption (handlers), and usage logging (handlers).
package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Provider names, as stored in ChatbotSettings.AI.Provider.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGoogle    = "google"
	ProviderGroq      = "groq"
)

// DefaultTimeout bounds one provider call when the caller's context has no
// deadline of its own.
const DefaultTimeout = 30 * time.Second

// Role of a conversation message. The system prompt is not a message: it is
// Request.System, and each adapter places it where its API expects it.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

// Request is one completion. Fields map to what the chatbot already sent
// before this layer existed; Temperature <= 0 means "provider default".
type Request struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int
	Temperature float64
}

// Usage is token accounting as reported by the provider (0 when not reported).
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type Response struct {
	Text  string
	Model string // the model the provider says it used, when reported
	Usage Usage
}

// ModelInfo is one model offered by a provider's own listing endpoint. Whatc
// does not keep model lists of its own.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	OwnedBy     string `json:"owned_by,omitempty"`
}

// Capabilities states what the provider's API can do, so future features (tool
// calling, structured outputs, streaming) can be gated per provider. It is NOT a
// statement that Whatc already uses them: today only Complete and ListModels are
// implemented. Structured outputs are model-dependent on some providers.
type Capabilities struct {
	ListModels        bool
	ToolCalling       bool
	StructuredOutputs bool
	Streaming         bool
}

// Provider is the interface features consume.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Complete(ctx context.Context, req Request) (*Response, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// Config builds a provider. APIKey is the plaintext credential, decrypted by
// the caller just for this call. BaseURL overrides the vendor endpoint (tests,
// proxies); empty means the vendor default.
type Config struct {
	APIKey     string
	HTTPClient *http.Client
	BaseURL    string
}

// ErrUnsupportedProvider is returned by New for an unknown provider name.
var ErrUnsupportedProvider = errors.New("unsupported AI provider")

// New returns the adapter for name.
func New(name string, cfg Config) (Provider, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	switch strings.ToLower(name) {
	case ProviderOpenAI:
		return newOpenAICompat(ProviderOpenAI, "OpenAI", "https://api.openai.com/v1", cfg), nil
	case ProviderGroq:
		// Groq exposes an OpenAI-compatible Chat Completions API: Bearer auth,
		// POST /chat/completions, GET /models.
		return newOpenAICompat(ProviderGroq, "Groq", "https://api.groq.com/openai/v1", cfg), nil
	case ProviderAnthropic:
		return newAnthropic(cfg), nil
	case ProviderGoogle:
		return newGoogle(cfg), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedProvider, name)
	}
}

// Supported lists the provider names New accepts, in display order.
func Supported() []string {
	return []string{ProviderOpenAI, ProviderAnthropic, ProviderGoogle, ProviderGroq}
}
