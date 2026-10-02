package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxBody = 4 << 20 // 4 MiB is far above any chat completion

// doJSON sends one request and returns the body of a 2xx reply. Every failure
// comes back as *Error with the key redacted.
func doJSON(ctx context.Context, client *http.Client, provider, apiKey, method, endpoint string, headers map[string]string, payload any) ([]byte, *Error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, &Error{Provider: provider, Kind: KindInvalidRequest, Message: "failed to encode request"}
		}
		body = bytes.NewReader(b)
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, &Error{Provider: provider, Kind: KindInvalidRequest, Message: "failed to build request"}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		kind := KindNetwork
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			kind = KindTimeout
		}
		// url.Error embeds the request URL, which must never carry a key; redact anyway.
		var ue *url.Error
		msg := err.Error()
		if errors.As(err, &ue) {
			msg = ue.Err.Error()
		}
		return nil, &Error{Provider: provider, Kind: kind, Message: redact(msg, apiKey)}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{
			Provider:   provider,
			Kind:       kindForStatus(resp.StatusCode),
			Status:     resp.StatusCode,
			Message:    redact(errorMessage(raw), apiKey),
			Code:       errorCode(raw),
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
		}
	}
	return raw, nil
}

// errorMessage extracts {"error":{"message":...}} (the shape all four vendors
// use), falling back to a short slice of the body.
func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// errorCode is the provider's short error code ({"error":{"code":...}}); a body carrying
// "failed_generation" (Groq: the model produced an unusable tool call) reports as tool_use_failed.
func errorCode(raw []byte) string {
	var e struct {
		Error struct {
			Code             any             `json:"code"`
			FailedGeneration json.RawMessage `json:"failed_generation"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	if len(e.Error.FailedGeneration) > 0 {
		return "tool_use_failed"
	}
	if s, ok := e.Error.Code.(string); ok && len(s) <= 64 {
		return s
	}
	return ""
}

func retryAfter(h string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

func badResponse(provider string) *Error {
	return &Error{Provider: provider, Kind: KindBadResponse, Message: "failed to parse response"}
}

func emptyResponse(provider string) *Error {
	return &Error{Provider: provider, Kind: KindEmptyResponse, Message: "no response from " + provider}
}
