package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Hardening of the HTTP client (SEC-1).
//
//   - A transport failure (url.Error) carries the full request URL. For some calls that URL holds
//     credentials (the OAuth code exchange puts client_secret and code in the query) and for all of them
//     it holds internal identifiers (the phone number id). transportError drops the URL and masks the
//     secrets before the error leaves the package, so logs, API answers and the chat UI never see them.
//   - The token is only ever sent to Meta. A download URL comes from a Meta answer, but it is still data
//     from the network: it is checked against a list of Meta hosts before a request carrying the token is
//     made, and again on every redirect hop.
//   - Calls to the Graph API never follow redirects: the API does not redirect, so a 3xx is either a
//     fault or an attack, and following it would replay the request (and its body) elsewhere.

// trustedMediaHosts are the hosts a credentialed media/asset download may go to (the host itself or any
// subdomain). Anything else is refused before the request is made.
var trustedMediaHosts = []string{
	"facebook.com", "fbsbx.com", "fbcdn.net", "whatsapp.net", "whatsapp.com",
}

// ErrUntrustedDownloadHost is returned when a credentialed download would leave Meta's hosts.
var ErrUntrustedDownloadHost = errors.New("refusing to send credentials to a host that is not Meta's")

// transportError turns an error from http.Client.Do into one safe to log and to show: the URL is removed
// and every secret masked. The delivery classification (a connection that never opened is a proven
// "not sent") is kept.
func transportError(prefix string, err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	reason := err
	var ue *url.Error
	if errors.As(err, &ue) {
		reason = ue.Err // the cause without the "Post \"https://...\"" wrapper
	}
	msg := redactSecrets(fmt.Sprintf("%s: %s", prefix, reason.Error()), secrets)
	if errors.Is(err, ErrUntrustedDownloadHost) { // a refusal of ours: callers may still recognise it
		return &untrustedHostError{msg: msg}
	}
	return &transportFailure{redactedError{msg: msg, outcome: Classify(err)}}
}

// transportFailure is a failure of the network call itself (the request may or may not have been handled).
type transportFailure struct{ redactedError }

func (e *transportFailure) Error() string    { return e.msg }
func (e *transportFailure) Outcome() Outcome { return e.outcome }

// DeliveryUnknown reports whether err leaves it genuinely open that the provider handled the request: the
// network call failed or timed out, or a response arrived that proves nothing (5xx, 408/425, unreadable).
// Errors raised BEFORE any request (validation, unsupported type, missing template) are not that: they are
// plain failures and keep their own text.
func DeliveryUnknown(err error) bool {
	if err == nil || NotSent(err) {
		return false
	}
	var tf *transportFailure
	var ae *APIError
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &tf) || errors.As(err, &ae) || errors.As(err, &ue) || errors.As(err, &ne) ||
		errors.Is(err, context.DeadlineExceeded)
}

// untrustedHostError keeps ErrUntrustedDownloadHost reachable through errors.Is after the text was cleaned.
type untrustedHostError struct{ msg string }

func (e *untrustedHostError) Error() string    { return e.msg }
func (e *untrustedHostError) Unwrap() error    { return ErrUntrustedDownloadHost }
func (e *untrustedHostError) Outcome() Outcome { return OutcomeRefused } // the request was never made

// hostTrusted reports whether host (host or host:port) may receive credentials: Meta's own hosts, or the
// exact host:port of the configured base URL (tests point it at a local server).
func (c *Client) hostTrusted(u *url.URL) bool {
	if base, err := url.Parse(c.getBaseURL()); err == nil && base.Host != "" && strings.EqualFold(base.Host, u.Host) {
		return true
	}
	for _, h := range c.extraTrustedHosts {
		if strings.EqualFold(h, u.Host) {
			return true
		}
	}
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, t := range trustedMediaHosts {
		if h == t || strings.HasSuffix(h, "."+t) {
			return true
		}
	}
	return false
}

// checkCredentialedURL refuses a download URL that credentials must not be sent to.
func (c *Client) checkCredentialedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: unreadable download URL", ErrUntrustedDownloadHost)
	}
	if !c.hostTrusted(u) {
		return fmt.Errorf("%w (%s)", ErrUntrustedDownloadHost, u.Hostname())
	}
	return nil
}

// downloadClient is the HTTP client for credentialed downloads: same transport and timeout, but every
// redirect hop is checked against the trusted hosts (and capped).
func (c *Client) downloadClient() *http.Client {
	dl := *c.HTTPClient
	dl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after too many redirects")
		}
		if !c.hostTrusted(req.URL) {
			return fmt.Errorf("%w (redirect to %s)", ErrUntrustedDownloadHost, req.URL.Hostname())
		}
		return nil
	}
	return &dl
}

// noRedirects makes a client hand back a 3xx instead of following it.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
