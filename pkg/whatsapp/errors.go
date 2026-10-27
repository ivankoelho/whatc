package whatsapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Outcome says what a failed send tells us about whether Meta accepted the message.
// Meta offers no idempotency key, so "I do not know" must stay "I do not know".
type Outcome int

const (
	// OutcomeUnknown: nothing proves the message was not accepted (timeouts, resets,
	// 5xx, 408/425, unreadable answers, errors Meta does not document as a rejection).
	// Callers must treat the message as possibly delivered and never resend it on their own.
	OutcomeUnknown Outcome = iota
	// OutcomeRefused: Meta rejected the request at the door (invalid or missing
	// parameter, template or token problems, policy). Retrying the same request
	// cannot succeed until something is fixed, and it was not sent.
	OutcomeRefused
	// OutcomeTransient: Meta rejected the request because of throttling and said to
	// retry later. Not sent; a later retry may succeed.
	OutcomeTransient
)

// What counts as a rejection is decided by the Meta error CODE together with a 4xx
// status, never by the status alone: Meta's error-code reference does not map HTTP
// statuses to "not sent", and says nothing about 408, 425 or 429. A 4xx with an
// unlisted code, or without a Meta error body, stays unknown (conservative).
var (
	// refusedCodes are validation, template, authentication and policy errors raised
	// before a message exists: 100 invalid parameter, 131008/131009 missing or invalid
	// parameter, 131021 same sender and recipient, 131047 outside the 24h window,
	// 131051 unsupported type, 132000/132001/132005/132007/132012 template parameter
	// and language errors, 132015/132016 paused or disabled template, 190 expired
	// token, 368 restricted account.
	refusedCodes = map[int]bool{
		100: true, 131008: true, 131009: true, 131021: true, 131047: true, 131051: true,
		132000: true, 132001: true, 132005: true, 132007: true, 132012: true,
		132015: true, 132016: true, 190: true, 368: true,
	}
	// throttleCodes are the documented "retry later" limits: 130429 throughput,
	// 131056 same-recipient pair limit, 80007 account rate limit.
	throttleCodes = map[int]bool{130429: true, 131056: true, 80007: true}
)

// APIError is an error answered by the Meta API: a response was received. Its
// text is the same the client has always produced; Status and Code are kept so
// callers can tell a refusal from an unknown outcome.
type APIError struct {
	Status int // HTTP status of the response
	Code   int // Meta error code, 0 when the body was not a Meta error
	msg    string
}

func (e *APIError) Error() string { return e.msg }

// Outcome classifies the answer. Only a 4xx status WITH a documented rejection
// code proves the message was not accepted; everything else is unknown.
func (e *APIError) Outcome() Outcome {
	if e.Status < 400 || e.Status >= 500 {
		return OutcomeUnknown
	}
	// 408 (request timeout) and 425 (too early) describe the transport, not a
	// decision about the message: whatever the body says, the outcome is unknown.
	if e.Status == 408 || e.Status == 425 {
		return OutcomeUnknown
	}
	switch {
	case refusedCodes[e.Code]:
		return OutcomeRefused
	case throttleCodes[e.Code]:
		return OutcomeTransient
	}
	return OutcomeUnknown
}

// outcomer is implemented by the errors this package returns.
type outcomer interface{ Outcome() Outcome }

// notSentError marks a failure that happened before any request left (for
// example the template components could not be built).
type notSentError struct{ err error }

func (e *notSentError) Error() string    { return e.err.Error() }
func (e *notSentError) Unwrap() error    { return e.err }
func (e *notSentError) Outcome() Outcome { return OutcomeRefused }

// NewNotSentError wraps err as a failure that provably happened before sending.
func NewNotSentError(err error) error {
	if err == nil {
		return nil
	}
	return &notSentError{err: err}
}

// Classify reports what err proves about delivery: a documented refusal, a
// documented throttle, a request that never left (a connection that was never
// established, or a failure before the request was built), or nothing at all.
func Classify(err error) Outcome {
	if err == nil {
		return OutcomeUnknown
	}
	var o outcomer
	if errors.As(err, &o) {
		return o.Outcome()
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return OutcomeRefused // never connected: nothing was written
	}
	return OutcomeUnknown
}

// NotSent reports whether err PROVES the message was not accepted by Meta
// (a refusal or a throttle). Everything else leaves the outcome unknown, and
// callers must not assume the message was not sent.
func NotSent(err error) bool { return Classify(err) != OutcomeUnknown }

// WithRedactedText returns an error that reads text (a redacted version of err's
// message) and keeps err's classification, without keeping err itself.
func WithRedactedText(err error, text string) error {
	if err == nil {
		return nil
	}
	return &redactedError{msg: text, outcome: Classify(err)}
}

// redactedError is an error whose text had secrets masked. It keeps only the
// classification, not the original error, so the secret cannot be reached by
// unwrapping.
type redactedError struct {
	msg     string
	outcome Outcome
}

func (e *redactedError) Error() string    { return e.msg }
func (e *redactedError) Outcome() Outcome { return e.outcome }

// PublicErrorText is the text of err that may be stored and shown to people: a raw provider response body
// (carried by the generic "API returned status N: <body>" error when the answer was not a Meta error object)
// is cut away and only the status is kept. Meta error objects are already reduced to code and message, and
// secrets were masked before this point.
func PublicErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	const marker = "API returned status "
	i := strings.Index(text, marker)
	if i < 0 {
		return text
	}
	j := i + len(marker)
	k := j
	for k < len(text) && text[k] >= '0' && text[k] <= '9' {
		k++
	}
	return text[:k]
}

// answerSummary is all that an error may say about a provider answer's BODY: the Meta error message when the
// answer is a Meta error object, nothing otherwise. A raw body (an HTML page from a proxy, an echoed request, any
// text the provider chose to send) never goes into an error: errors are stored, logged and shown to people.
func answerSummary(body []byte, secrets []string) string {
	var m MetaAPIError
	if json.Unmarshal(body, &m) == nil && m.Error.Message != "" {
		return fmt.Sprintf("API error %d: %s", m.Error.Code, redactSecrets(m.Error.Message, secrets))
	}
	return ""
}

// statusText builds "<prefix> with status N[: Meta message]"; secrets (the access token, an authorization code,
// the app secret) are masked in the Meta message.
func statusText(prefix string, status int, body []byte, secrets ...string) string {
	if s := answerSummary(body, secrets); s != "" {
		return fmt.Sprintf("%s with status %d: %s", prefix, status, s)
	}
	return fmt.Sprintf("%s with status %d", prefix, status)
}
