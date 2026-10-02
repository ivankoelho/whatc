package ai

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorKind classifies a failure independently of the vendor, so callers (and
// usage logs) can tell "bad key" from "rate limited" from "provider down".
type ErrorKind string

const (
	KindAuth           ErrorKind = "auth"            // 401/403: key invalid, revoked or without access
	KindRateLimit      ErrorKind = "rate_limit"      // 429
	KindInvalidRequest ErrorKind = "invalid_request" // 400/404/422: bad model name, bad parameter
	KindProvider       ErrorKind = "provider"        // 5xx and other provider-side failures
	KindTimeout        ErrorKind = "timeout"
	KindNetwork        ErrorKind = "network"
	KindEmptyResponse  ErrorKind = "empty_response" // 200 with nothing usable
	KindBadResponse    ErrorKind = "bad_response"   // 200 that could not be parsed

	KindInvalidToolCall ErrorKind = "invalid_tool_call" // the model produced a tool call that cannot be used
)

// Error is the only error type adapters return for provider failures. Message
// is safe to log and show: it never contains the API key.
type Error struct {
	Provider   string
	Kind       ErrorKind
	Status     int
	Message    string
	RetryAfter time.Duration
	Code       string // the provider's short error code, when it sends one
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s API error (%s)", e.Provider, e.Kind)
	}
	return fmt.Sprintf("%s API error: %s", e.Provider, e.Message)
}

// KindOf returns the ErrorKind of err, or "" if it is not an *Error.
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

func kindForStatus(status int) ErrorKind {
	switch {
	case status == 401 || status == 403:
		return KindAuth
	case status == 429:
		return KindRateLimit
	case status >= 400 && status < 500:
		return KindInvalidRequest
	default:
		return KindProvider
	}
}

// redact removes the API key from s, in case a provider echoes it back.
func redact(s, apiKey string) string {
	if apiKey != "" {
		s = strings.ReplaceAll(s, apiKey, "[redacted]")
	}
	return s
}
