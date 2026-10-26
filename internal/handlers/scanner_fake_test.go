package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/shridarpatil/whatomate/internal/antimalware"
)

// eicarTest is the industry-standard, harmless antivirus test string (the real engine flags it too).
const eicarTest = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// fakeScanner stands in for clamd: clean unless the bytes contain the EICAR string. Tests flip it down,
// slow or change its signature version to exercise the policy without a real engine.
type fakeScanner struct {
	mu      sync.Mutex
	down    bool
	delay   time.Duration
	version string
	calls   int
}

func newFakeScanner() *fakeScanner { return &fakeScanner{version: "fake/1"} }

func (f *fakeScanner) set(fn func(*fakeScanner)) { f.mu.Lock(); defer f.mu.Unlock(); fn(f) }
func (f *fakeScanner) count() int                { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (f *fakeScanner) Scan(ctx context.Context, r io.Reader) (antimalware.Result, error) {
	f.mu.Lock()
	f.calls++
	down, delay := f.down, f.delay
	f.mu.Unlock()
	if down {
		return antimalware.Result{}, errors.New("connection refused")
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return antimalware.Result{}, ctx.Err()
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return antimalware.Result{}, err
	}
	if bytes.Contains(data, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
		return antimalware.Result{Infected: true, Signature: "Eicar-Test-Signature"}, nil
	}
	return antimalware.Result{}, nil
}

func (f *fakeScanner) Version(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return "", errors.New("connection refused")
	}
	return f.version, nil
}
