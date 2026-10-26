package safehttp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCheckAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "0.0.0.0", "0.1.2.3", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "169.254.169.254", "169.254.0.1", "100.64.0.1", "100.100.100.200", "198.18.0.1", "192.0.2.1",
		"198.51.100.7", "203.0.113.9", "224.0.0.1", "239.255.255.250", "240.0.0.1", "255.255.255.255",
		"::", "::1", "fe80::1", "fc00::1", "fd00:ec2::254", "ff02::1", "2001:db8::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3", "::ffff:169.254.169.254", // IPv4-mapped
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", // NAT64 carrying 127.0.0.1 / 169.254.169.254
		"2002:7f00:1::1", "2002:a9fe:a9fe::1", // 6to4 carrying 127.0.0.1 / 169.254.169.254
	}
	for _, s := range blocked {
		if err := CheckAddr(netip.MustParseAddr(s)); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s must be blocked, got %v", s, err)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "11.0.0.1", "2606:4700:4700::1111", "2001:4860:4860::8888", "::ffff:8.8.8.8", "64:ff9b::808:808"}
	for _, s := range allowed {
		if err := CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s must be allowed, got %v", s, err)
		}
	}
}

func TestURLRules(t *testing.T) {
	f := New(Options{Resolver: stubResolver{}})
	bad := []string{
		"http://example.com/a.png", "ftp://example.com/a", "file:///etc/passwd", "gopher://example.com/", "//example.com/a",
		"https://user:pw@example.com/a.png", "https://example.com:8443/a.png", "https://example.com:80/a.png",
		"https://localhost/a.png", "https://app.localhost/a.png", "https://printer.local/a.png", "https://db.internal/a.png",
		"https://metadata.google.internal/computeMetadata/v1/", "https:///a.png", "", "not a url",
	}
	for _, u := range bad {
		if _, err := f.Fetch(context.Background(), u, 1<<20); err == nil {
			t.Errorf("%q must be refused", u)
		}
	}
	// IP literals are judged by the dialer: private ones never connect
	for _, u := range []string{"https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/latest/meta-data/", "https://10.0.0.1/", "https://[::ffff:7f00:1]/", "https://[fd00:ec2::254]/"} {
		if _, err := f.Fetch(context.Background(), u, 1<<20); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%q must be an address error, got %v", u, err)
		}
	}
}

type stubResolver map[string][]string

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, a := range s[host] {
		out = append(out, netip.MustParseAddr(a))
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}

// A resolver that gives a public answer first and a private one afterwards (DNS rebinding).
type rebindingResolver struct {
	mu    sync.Mutex
	calls int
}

func (r *rebindingResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
}

func TestDNSRebinding_ConnectsToTheValidatedAddressOnly(t *testing.T) {
	r := &rebindingResolver{}
	var dialed []string
	f := New(Options{Resolver: r, Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, errors.New("stop here: only the destination matters")
	}})
	_, _ = f.Fetch(context.Background(), "https://rebind.example/a.png", 1<<20)
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("must dial the validated IP, got %v", dialed)
	}
	if r.calls != 1 {
		t.Fatalf("the name is resolved once per connection; a second answer must never be consulted (%d lookups)", r.calls)
	}
	// the NEXT connection resolves again, gets the private answer and is refused before dialing
	dialed = nil
	_, err := f.Fetch(context.Background(), "https://rebind.example/a.png", 1<<20)
	if !errors.Is(err, ErrBlockedAddress) || len(dialed) != 0 {
		t.Fatalf("private answer must be refused without dialing: %v %v", err, dialed)
	}
}

func TestMixedAnswersAreRefusedAsAWhole(t *testing.T) {
	var dialed int
	f := New(Options{
		Resolver: stubResolver{"mixed.example": {"93.184.216.34", "10.0.0.5"}, "v6.example": {"2606:4700:4700::1111", "fe80::1"}},
		Dial:     func(context.Context, string, string) (net.Conn, error) { dialed++; return nil, errors.New("no") },
	})
	for _, u := range []string{"https://mixed.example/x", "https://v6.example/x"} {
		if _, err := f.Fetch(context.Background(), u, 1<<20); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: one non-public answer must refuse the host, got %v", u, err)
		}
	}
	if dialed != 0 {
		t.Fatal("nothing may be dialed")
	}
}

// loopbackOnly lets the tests reach httptest servers while still refusing everything else.
func loopbackOnly(a netip.Addr) error {
	if a.Unmap().IsLoopback() {
		return nil
	}
	return CheckAddr(a)
}

func testFetcher(srv *httptest.Server, hosts stubResolver, mod func(*Options)) *Fetcher {
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	o := Options{
		Resolver:  hosts,
		TLS:       &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server's self-signed certificate
		AllowAddr: loopbackOnly,
		Port:      port,
		Timeout:   2 * time.Second,
	}
	if mod != nil {
		mod(&o)
	}
	return New(o)
}

func target(srv *httptest.Server, host, path string) string {
	u, _ := url.Parse(srv.URL)
	return "https://" + host + ":" + u.Port() + path
}

func TestFetch_SuccessSizeAndHeaders(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1") // a proxy would break this: it must be ignored
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	var got http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()
	f := testFetcher(srv, stubResolver{"media.example": {"127.0.0.1"}}, nil)

	res, err := f.Fetch(context.Background(), target(srv, "media.example", "/a.png"), 10)
	if err != nil || string(res.Data) != "0123456789" || res.ContentType != "image/png" {
		t.Fatalf("exactly at the limit must pass: %v %+v", err, res)
	}
	if got.Get("Authorization") != "" || got.Get("Cookie") != "" || got.Get("X-Api-Key") != "" {
		t.Fatalf("no credentials, cookies or sensitive headers: %v", got)
	}
	if _, err := f.Fetch(context.Background(), target(srv, "media.example", "/a.png"), 9); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("one byte over the limit must be refused, not truncated: %v", err)
	}
}

func TestFetch_ChunkedBodyOverTheLimitIsRefused(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher) // no Content-Length: the limit must hold while reading
		for i := 0; i < 50; i++ {
			_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
			fl.Flush()
		}
	}))
	defer srv.Close()
	f := testFetcher(srv, stubResolver{"media.example": {"127.0.0.1"}}, nil)
	if _, err := f.Fetch(context.Background(), target(srv, "media.example", "/big"), 10_000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestFetch_Status(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	f := testFetcher(srv, stubResolver{"media.example": {"127.0.0.1"}}, nil)
	if _, err := f.Fetch(context.Background(), target(srv, "media.example", "/x"), 100); !errors.Is(err, ErrStatus) {
		t.Fatalf("got %v", err)
	}
}

func TestFetch_TimeoutsOnSlowHeadersAndSlowBodies(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/headers" {
			time.Sleep(2 * time.Second)
			return
		}
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte("start"))
		fl.Flush()
		time.Sleep(2 * time.Second) // a body that trickles
	}))
	defer srv.Close()
	f := testFetcher(srv, stubResolver{"media.example": {"127.0.0.1"}}, func(o *Options) { o.Timeout = 300 * time.Millisecond })
	for _, p := range []string{"/headers", "/body"} {
		began := time.Now()
		if _, err := f.Fetch(context.Background(), target(srv, "media.example", p), 1<<20); err == nil {
			t.Fatalf("%s must time out", p)
		}
		if time.Since(began) > time.Second {
			t.Fatalf("%s took %v: the deadline must bound the whole request", p, time.Since(began))
		}
	}
}

func TestFetch_ConnectTimeout(t *testing.T) {
	f := New(Options{
		Resolver: stubResolver{"hang.example": {"93.184.216.34"}}, DialTimeout: 200 * time.Millisecond, Timeout: time.Second,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() },
	})
	began := time.Now()
	if _, err := f.Fetch(context.Background(), "https://hang.example/x", 100); err == nil || time.Since(began) > 2*time.Second {
		t.Fatalf("an unreachable destination must time out quickly: %v after %v", err, time.Since(began))
	}
}

func TestRedirects(t *testing.T) {
	var cookieOnSecond, authOnSecond string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok-a":
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
			http.Redirect(w, r, "/ok-b", http.StatusFound)
		case "/ok-b":
			cookieOnSecond, authOnSecond = r.Header.Get("Cookie"), r.Header.Get("Authorization")
			_, _ = w.Write([]byte("done"))
		case "/to-private":
			http.Redirect(w, r, "https://evil.example:"+portOf(srv)+"/secret", http.StatusFound)
		case "/to-ip":
			http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://media.example/x", http.StatusFound)
		case "/to-port":
			http.Redirect(w, r, "https://media.example:6379/x", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	f := testFetcher(srv, stubResolver{"media.example": {"127.0.0.1"}, "evil.example": {"10.0.0.5"}}, nil)

	if res, err := f.Fetch(context.Background(), target(srv, "media.example", "/ok-a"), 100); err != nil || string(res.Data) != "done" {
		t.Fatalf("a redirect inside the policy is followed: %v", err)
	}
	if cookieOnSecond != "" || authOnSecond != "" {
		t.Fatalf("no cookies or credentials follow a redirect: %q %q", cookieOnSecond, authOnSecond)
	}
	cases := map[string]error{
		"/to-private": ErrBlockedAddress, // a name that now resolves to a private address
		"/to-ip":      ErrBlockedAddress, // the metadata endpoint
		"/to-http":    ErrBlockedURL,
		"/to-port":    ErrBlockedURL,
		"/loop":       ErrBlockedURL, // too many redirects
	}
	for path, want := range cases {
		if _, err := f.Fetch(context.Background(), target(srv, "media.example", path), 100); !errors.Is(err, want) {
			t.Errorf("%s: want %v, got %v", path, want, err)
		}
	}
}

func portOf(srv *httptest.Server) string { u, _ := url.Parse(srv.URL); return u.Port() }
