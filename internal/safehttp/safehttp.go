// Package safehttp downloads content from URLs that a PERSON supplied (a template header image, say).
//
// Such a URL is untrusted input aimed at the server's network position: without care it reaches the
// loopback interface, the private network, or a cloud metadata endpoint. This client therefore:
//
//   - speaks HTTPS only, to port 443, with no credentials in the URL;
//   - resolves the name itself, refuses it if ANY resolved address is not public, and then dials the
//     validated IP directly, so a second DNS answer (rebinding) can never change the destination;
//   - checks the address again at the moment of connection (net.Dialer.Control), for every connection,
//     including each redirect, which is limited in number and held to the same rules;
//   - uses no proxy, no cookies and no caller headers beyond a fixed User-Agent;
//   - bounds connect, handshake, header and total time, and the bytes received.
//
// It deliberately shares nothing with the Nextcloud or LDAP network policy: those integrations may
// need private networks; a URL typed by a user never may.
package safehttp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var (
	// ErrBlockedURL: the URL itself is not acceptable (scheme, port, credentials, host).
	ErrBlockedURL = errors.New("safehttp: URL not allowed")
	// ErrBlockedAddress: the name resolves, or a redirect leads, to an address that is not public.
	ErrBlockedAddress = errors.New("safehttp: address not allowed")
	// ErrTooLarge: the body is larger than the caller's limit.
	ErrTooLarge = errors.New("safehttp: response too large")
	// ErrStatus: the server did not answer 200.
	ErrStatus = errors.New("safehttp: unexpected status")
)

// Resolver is what the dialer needs from DNS (net.DefaultResolver satisfies it).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// blocked address space. Anything not matched here must still be a global unicast address.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",       // "this" network
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // carrier-grade NAT (also Alibaba's metadata 100.100.100.200)
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, incl. the cloud metadata service 169.254.169.254
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved (incl. the broadcast address)
	"::/128",          // unspecified
	"::1/128",         // loopback
	"fc00::/7",        // unique local (incl. AWS IPv6 metadata fd00:ec2::254)
	"fe80::/10",       // link-local
	"ff00::/8",        // multicast
	"2001:db8::/32",   // documentation
	"2001::/32",       // Teredo (tunnels to an embedded IPv4)
	"100::/64",        // discard-only
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// embedded IPv4 inside an IPv6 address: judged by the IPv4 it carries.
var (
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixto4 = netip.MustParsePrefix("2002::/16")
)

// CheckAddr returns nil only for a public unicast address.
func CheckAddr(a netip.Addr) error {
	a = a.Unmap()
	if !a.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlockedAddress)
	}
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64.Contains(a):
			return CheckAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixto4.Contains(a):
			return CheckAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, a)
		}
	}
	if !a.IsGlobalUnicast() {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, a)
	}
	return nil
}

// blockedHostnames are refused before any lookup (they would also fail the address check).
func blockedHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	return h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") ||
		strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".localdomain") || h == "metadata.google.internal"
}

// Options configures a Fetcher. The zero value is the production policy.
type Options struct {
	Resolver     Resolver
	Timeout      time.Duration // whole request, redirects included (default 20 s)
	DialTimeout  time.Duration // default 5 s
	MaxRedirects int           // default 3

	// Test seams. Production code leaves them nil.
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error) // replaces the TCP dial (after validation)
	TLS       *tls.Config
	AllowAddr func(netip.Addr) error // replaces CheckAddr
	Port      string                 // replaces 443
}

// Fetcher downloads with the policy above.
type Fetcher struct {
	client *http.Client
	opt    Options
}

// New builds a Fetcher.
func New(o Options) *Fetcher {
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = 3
	}
	if o.AllowAddr == nil {
		o.AllowAddr = CheckAddr
	}
	if o.Port == "" {
		o.Port = "443"
	}
	f := &Fetcher{opt: o}
	tr := &http.Transport{
		Proxy:                  nil, // never an environment proxy: it would see (and be a way around) the policy
		DialContext:            f.dialContext,
		TLSClientConfig:        o.TLS,
		TLSHandshakeTimeout:    o.DialTimeout,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxIdleConns:           0,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	f.client = &http.Client{
		Transport: tr,
		Timeout:   o.Timeout,
		Jar:       nil, // no cookies, ever
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > o.MaxRedirects {
				return fmt.Errorf("%w: too many redirects", ErrBlockedURL)
			}
			// Every hop is held to the URL rules; the address rules apply again in the dialer.
			return f.checkURL(req.URL)
		},
	}
	return f
}

var defaultFetcher = New(Options{})

// Default is the production Fetcher.
func Default() *Fetcher { return defaultFetcher }

func (f *Fetcher) checkURL(u *url.URL) error {
	if u == nil || u.Scheme != "https" {
		return fmt.Errorf("%w: only https is allowed", ErrBlockedURL)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in the URL", ErrBlockedURL)
	}
	if blockedHost(u.Hostname()) {
		return fmt.Errorf("%w: host", ErrBlockedURL)
	}
	if p := u.Port(); p != "" && p != f.opt.Port {
		return fmt.Errorf("%w: port", ErrBlockedURL)
	}
	return nil
}

// dialContext resolves, validates EVERY answer, and connects to the validated IP itself.
func (f *Fetcher) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ips = []netip.Addr{ip} // an IP literal is judged as it is
	} else {
		ips, err = f.opt.Resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no address", ErrBlockedAddress)
	}
	for _, ip := range ips { // one bad answer poisons the lot: no picking the "good" one
		if err := f.opt.AllowAddr(ip); err != nil {
			return nil, err
		}
	}
	d := &net.Dialer{
		Timeout: f.opt.DialTimeout,
		// The last word is the connection itself: whatever address is actually being connected to.
		Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(h)
			if err != nil {
				return err
			}
			return f.opt.AllowAddr(a)
		},
	}
	dial := d.DialContext
	if f.opt.Dial != nil {
		dial = f.opt.Dial
	}
	var last error
	for _, ip := range ips {
		c, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// Result is a downloaded body.
type Result struct {
	Data        []byte
	ContentType string
}

// Fetch GETs rawURL and returns at most maxBytes; a longer body is an error (ErrTooLarge), never a truncation.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) (*Result, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlockedURL, err)
	}
	if err := f.checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "whatc-media-fetch/1")
	req.Header.Set("Accept", "*/*")
	resp, err := f.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return &Result{Data: data, ContentType: resp.Header.Get("Content-Type")}, nil
}
