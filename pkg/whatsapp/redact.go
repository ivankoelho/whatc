package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// SecretMask replaces secret values (one-time codes) in error text.
const SecretMask = "••••••"

// minSecretLen keeps very short values (which would also match ordinary words
// or ids) from being treated as secrets.
const minSecretLen = 4

type secretsKey struct{}

// WithSecrets tells the client which values in the requests made with ctx are
// secrets (e.g. a one-time code). Anything the Meta API or the transport echoes
// back in an error is redacted before the error is returned or logged.
func WithSecrets(ctx context.Context, values ...string) context.Context {
	prev, _ := ctx.Value(secretsKey{}).([]string)
	all := append(append([]string{}, prev...), values...)
	return context.WithValue(ctx, secretsKey{}, all)
}

// requestSecrets returns the secrets of one request: those declared on ctx plus
// the parameters of template button components (where Meta wants the OTP) and
// any body parameter carrying the same value. Nothing else is treated as secret,
// so names, ids and error codes stay readable.
func requestSecrets(ctx context.Context, jsonBody []byte) []string {
	secrets, _ := ctx.Value(secretsKey{}).([]string)
	secrets = append([]string{}, secrets...)

	var req struct {
		Template struct {
			Components []struct {
				Type       string `json:"type"`
				Parameters []struct {
					Text       string `json:"text"`
					CouponCode string `json:"coupon_code"`
				} `json:"parameters"`
			} `json:"components"`
		} `json:"template"`
	}
	if len(jsonBody) == 0 || json.Unmarshal(jsonBody, &req) != nil {
		return secrets
	}
	for _, comp := range req.Template.Components {
		if comp.Type != "button" {
			continue
		}
		for _, p := range comp.Parameters {
			secrets = append(secrets, p.Text, p.CouponCode)
		}
	}
	return secrets
}

// redactSecrets masks whole-token occurrences of each secret in s: "code 123456."
// is masked, "id 91234567" and "131008" are not touched by the secret 123456.
func redactSecrets(s string, secrets []string) string {
	for _, sec := range secrets {
		sec = strings.TrimSpace(sec)
		if len(sec) < minSecretLen {
			continue
		}
		var out strings.Builder
		rest := s
		for {
			i := strings.Index(rest, sec)
			if i < 0 {
				out.WriteString(rest)
				break
			}
			before, after := rest[:i], rest[i+len(sec):]
			if isWordBoundary(before, true) && isWordBoundary(after, false) {
				out.WriteString(before)
				out.WriteString(SecretMask)
			} else {
				out.WriteString(before + sec)
			}
			rest = after
		}
		s = out.String()
	}
	return s
}

func isWordBoundary(s string, trailing bool) bool {
	if s == "" {
		return true
	}
	rs := []rune(s)
	r := rs[0]
	if trailing {
		r = rs[len(rs)-1]
	}
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// redactError returns err with secrets masked in its text; the original is
// returned untouched when nothing was masked.
func redactError(err error, secrets []string) error {
	if err == nil || len(secrets) == 0 {
		return err
	}
	if msg := err.Error(); redactSecrets(msg, secrets) != msg {
		return fmt.Errorf("%s", redactSecrets(msg, secrets))
	}
	return err
}

// parseMetaAPIErrorRedacted is ParseMetaAPIError with the secrets masked inside
// the human-readable fields only: the numeric error code stays intact.
func parseMetaAPIErrorRedacted(statusCode int, respBody []byte, secrets []string) error {
	var apiErr MetaAPIError
	if err := json.Unmarshal(respBody, &apiErr); err == nil && apiErr.Error.Message != "" {
		e := &apiErr.Error
		errMsg := fmt.Sprintf("API error %d: %s", e.Code, redactSecrets(e.Message, secrets))
		if e.ErrorData.Details != "" {
			errMsg += " - Details: " + redactSecrets(e.ErrorData.Details, secrets)
		}
		if e.ErrorUserMsg != "" {
			errMsg += " - " + redactSecrets(e.ErrorUserMsg, secrets)
		}
		return fmt.Errorf("%s", errMsg)
	}
	return fmt.Errorf("API returned status %d: %s", statusCode, redactSecrets(string(respBody), secrets))
}
