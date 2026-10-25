package templateutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/crypto"
)

// One-time codes that must survive until a worker sends them (campaigns) are
// stored sealed: AES-256-GCM with app.encryption_key, plus the instant they
// were sealed so they can be dropped after SealedTTL. Redis carries the same
// sealed value, never the code. A hash would not do: the worker needs the
// original to send it.
const (
	SealedKey   = "_sealed"
	SealedAtKey = "_sealed_at"
	// SealedTTL is the hard cap on how long a sealed code is kept. Meta codes
	// live at most 90 minutes, so a day-old one is useless anyway.
	SealedTTL = 24 * time.Hour
)

var (
	// ErrSealKeyMissing: refusing to "seal" with no key (crypto.Encrypt would
	// silently return plaintext).
	ErrSealKeyMissing = errors.New("app.encryption_key is not configured")
	// ErrSealInvalid: the envelope does not hold an authenticated ciphertext.
	ErrSealInvalid = errors.New("sealed value is not a valid ciphertext")
	// ErrSealExpired: the sealed value is past SealedTTL or was already scrubbed.
	ErrSealExpired = errors.New("verification code expired or no longer available")
)

// IsSealed reports whether params is a sealed envelope.
func IsSealed(params map[string]any) bool {
	_, ok := params[SealedKey].(string)
	return ok
}

// SealParams encrypts params into an envelope. It fails closed without a key.
func SealParams(params map[string]any, key string, now time.Time) (map[string]any, error) {
	if key == "" {
		return nil, ErrSealKeyMissing
	}
	if IsSealed(params) {
		return params, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	enc, err := crypto.Encrypt(string(raw), key)
	if err != nil {
		return nil, err
	}
	return map[string]any{SealedKey: enc, SealedAtKey: now.Unix()}, nil
}

// OpenParams returns the clear params. A non-sealed map is returned as is
// (legacy rows). An expired envelope returns ErrSealExpired.
func OpenParams(params map[string]any, key string, now time.Time) (map[string]any, error) {
	if !IsSealed(params) {
		return params, nil
	}
	if SealedExpired(params, now) {
		return nil, ErrSealExpired
	}
	enc, _ := params[SealedKey].(string)
	// crypto.Decrypt passes unprefixed values (and everything, with an empty key)
	// through unchanged, to read legacy data. A sealed envelope must never be
	// accepted that way: only a real "enc:" ciphertext, opened with a real key.
	if key == "" {
		return nil, ErrSealKeyMissing
	}
	if !crypto.IsEncrypted(enc) {
		return nil, ErrSealInvalid
	}
	plain, err := crypto.Decrypt(enc, key)
	if err != nil {
		return nil, fmt.Errorf("failed to open sealed params: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SealedExpired reports whether a sealed envelope is older than SealedTTL.
func SealedExpired(params map[string]any, now time.Time) bool {
	var at int64
	switch v := params[SealedAtKey].(type) {
	case float64:
		at = int64(v)
	case int64:
		at = v
	case json.Number:
		at, _ = v.Int64()
	}
	return at > 0 && now.Sub(time.Unix(at, 0)) > SealedTTL
}

// RedactValues replaces every occurrence of the given secret values in s with
// AuthCodeMask. Used on error text before it is logged, stored or broadcast.
func RedactValues(s string, params map[string]any) string {
	for _, v := range params {
		val := strings.TrimSpace(fmt.Sprint(v))
		if val != "" && val != "<nil>" {
			s = strings.ReplaceAll(s, val, AuthCodeMask)
		}
	}
	return s
}

// RedactStrings is RedactValues for map[string]string params.
func RedactStrings(s string, params map[string]string) string {
	m := make(map[string]any, len(params))
	for k, v := range params {
		m[k] = v
	}
	return RedactValues(s, m)
}

// ---- OTP validity ---------------------------------------------------------
//
// Three different clocks, deliberately kept apart:
//
//   - validity (functional): until when the issuer says the code works. It
//     comes from the system that generates and validates the OTP, as
//     valid_until, and is the only thing that makes a code sendable. Whatomate
//     never assumes or renews it.
//   - send window: OTPPolicy.MaxValidity caps how long after sealing a code may
//     still be sent (5 minutes by default), whatever the issuer says; the
//     smaller of the two wins.
//   - retention (SealedTTL, 24h): how long a sealed secret may stay stored. It
//     says nothing about validity.

const (
	// SealedValidUntilKey holds the issuer's deadline, unix seconds (UTC). It is
	// not secret, so it lives next to the ciphertext, not inside it.
	SealedValidUntilKey = "_valid_until"

	// Reasons written to the recipient when a code is not sent.
	ReasonOTPExpired         = "otp_expired"
	ReasonOTPValidityMissing = "otp_validity_missing"
)

var (
	// ErrOTPExpired: the code is past its validity; it must not be sent.
	ErrOTPExpired = errors.New(ReasonOTPExpired)
	// ErrOTPValidityMissing: validity is required but the issuer gave none.
	ErrOTPValidityMissing = errors.New(ReasonOTPValidityMissing)
)

// DefaultOTPMaxValidity is the operational reference send window.
const DefaultOTPMaxValidity = 5 * time.Minute

// OTPPolicy decides whether a sealed code may still be sent.
type OTPPolicy struct {
	// MaxValidity caps the send window counted from sealing. Zero = default.
	MaxValidity time.Duration
	// RequireValidUntil makes a code without issuer validity unsendable.
	// While false, codes without valid_until keep the previous behaviour (only
	// the retention window applies) so existing campaigns are not invalidated.
	RequireValidUntil bool
}

func (p OTPPolicy) maxValidity() time.Duration {
	if p.MaxValidity > 0 {
		return p.MaxValidity
	}
	return DefaultOTPMaxValidity
}

func unixField(params map[string]any, key string) int64 {
	switch v := params[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// Deadline returns the last instant the code may be sent: the earlier of the
// issuer's valid_until and sealing time + MaxValidity. ok=false when the issuer
// gave no validity.
func (p OTPPolicy) Deadline(params map[string]any) (deadline time.Time, ok bool) {
	until := unixField(params, SealedValidUntilKey)
	if until <= 0 {
		return time.Time{}, false
	}
	deadline = time.Unix(until, 0).UTC()
	if at := unixField(params, SealedAtKey); at > 0 {
		if cap := time.Unix(at, 0).UTC().Add(p.maxValidity()); cap.Before(deadline) {
			deadline = cap
		}
	}
	return deadline, true
}

// Check is called right before the Meta request. It returns ErrOTPExpired,
// ErrOTPValidityMissing or nil.
func (p OTPPolicy) Check(params map[string]any, now time.Time) error {
	deadline, ok := p.Deadline(params)
	if !ok {
		if p.RequireValidUntil {
			return ErrOTPValidityMissing
		}
		return nil // legacy: retention window only (enforced when opening)
	}
	if !now.Before(deadline) {
		return ErrOTPExpired
	}
	return nil
}

// Expired reports whether the code can no longer be sent, so its secret can be
// dropped: past its deadline, or past the retention window. A code without
// issuer validity is never reported expired here before retention, so the
// sweeper does not delete codes of legacy campaigns that are still pending.
func (p OTPPolicy) Expired(params map[string]any, now time.Time) bool {
	if deadline, ok := p.Deadline(params); ok {
		return !now.Before(deadline)
	}
	return SealedExpired(params, now)
}

// SealParamsUntil is SealParams that also records the issuer's valid_until.
func SealParamsUntil(params map[string]any, key string, now time.Time, validUntil *time.Time) (map[string]any, error) {
	sealed, err := SealParams(params, key, now)
	if err != nil {
		return nil, err
	}
	if validUntil != nil && !validUntil.IsZero() && !IsSealed(params) {
		sealed[SealedValidUntilKey] = validUntil.UTC().Unix()
	}
	return sealed, nil
}
