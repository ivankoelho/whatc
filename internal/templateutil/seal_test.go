package templateutil

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const k = "test-key-longer-than-thirty-two-characters!!"

func TestSealOpenRoundTrip(t *testing.T) {
	now := time.Now()
	sealed, err := SealParams(map[string]any{"1": "123456"}, k, now)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) {
		t.Fatal("expected sealed envelope")
	}
	for _, v := range sealed {
		if s, ok := v.(string); ok && strings.Contains(s, "123456") {
			t.Fatalf("clear code found in envelope: %v", sealed)
		}
	}
	got, err := OpenParams(sealed, k, now)
	if err != nil || got["1"] != "123456" {
		t.Fatalf("open = %v, %v", got, err)
	}
}

func TestSealFailsClosedWithoutKey(t *testing.T) {
	if _, err := SealParams(map[string]any{"1": "1"}, "", time.Now()); !errors.Is(err, ErrSealKeyMissing) {
		t.Fatalf("err = %v, want ErrSealKeyMissing", err)
	}
}

func TestOpenRejectsExpiredAndWrongKey(t *testing.T) {
	old := time.Now().Add(-SealedTTL - time.Minute)
	sealed, _ := SealParams(map[string]any{"1": "1"}, k, old)
	if _, err := OpenParams(sealed, k, time.Now()); !errors.Is(err, ErrSealExpired) {
		t.Fatalf("err = %v, want ErrSealExpired", err)
	}
	fresh, _ := SealParams(map[string]any{"1": "1"}, k, time.Now())
	if _, err := OpenParams(fresh, "another-key-another-key-another-key-xx", time.Now()); err == nil {
		t.Fatal("wrong key must not open")
	}
}

func TestOpenPassesLegacyPlaintextThrough(t *testing.T) {
	got, err := OpenParams(map[string]any{"1": "x"}, k, time.Now())
	if err != nil || got["1"] != "x" {
		t.Fatalf("legacy passthrough failed: %v %v", got, err)
	}
}

func TestRedactValues(t *testing.T) {
	got := RedactStrings("bad param 123456 for +55", map[string]string{"1": "123456", "2": ""})
	if strings.Contains(got, "123456") || !strings.Contains(got, AuthCodeMask) {
		t.Fatalf("not redacted: %q", got)
	}
}

func TestOpenNeverAcceptsPlaintextInsideAnEnvelope(t *testing.T) {
	now := time.Now()
	// A writer of the DB column cannot smuggle clear JSON through the sealed path.
	forged := map[string]any{SealedKey: `{"1":"000000"}`, SealedAtKey: now.Unix()}
	if _, err := OpenParams(forged, k, now); !errors.Is(err, ErrSealInvalid) {
		t.Fatalf("forged plaintext: err = %v, want ErrSealInvalid", err)
	}
	// With no key configured, nothing is "opened" as is.
	sealed, _ := SealParams(map[string]any{"1": "1"}, k, now)
	if _, err := OpenParams(sealed, "", now); !errors.Is(err, ErrSealKeyMissing) {
		t.Fatalf("empty key: err = %v, want ErrSealKeyMissing", err)
	}
}

func TestOpenDetectsTamperingAndTruncation(t *testing.T) {
	now := time.Now()
	sealed, _ := SealParams(map[string]any{"1": "123456"}, k, now)
	enc := sealed[SealedKey].(string)
	flipped := enc[:len(enc)-3] + "AAA" // corrupt the tail (auth tag)
	if flipped == enc {
		flipped = enc[:len(enc)-3] + "BBB"
	}
	for name, v := range map[string]string{"flipped": flipped, "truncated": enc[:8], "prefix only": "enc:"} {
		env := map[string]any{SealedKey: v, SealedAtKey: now.Unix()}
		if got, err := OpenParams(env, k, now); err == nil {
			t.Fatalf("%s: opened to %v", name, got)
		}
	}
}

func TestSealNonceIsUniquePerOperation(t *testing.T) {
	now := time.Now()
	a, _ := SealParams(map[string]any{"1": "123456"}, k, now)
	b, _ := SealParams(map[string]any{"1": "123456"}, k, now)
	if a[SealedKey] == b[SealedKey] {
		t.Fatal("same plaintext produced the same ciphertext: nonce reused")
	}
}

func TestOTPPolicy_DeadlineIsTheSmallerOfIssuerAndWindow(t *testing.T) {
	sealedAt := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	p := OTPPolicy{} // default 5-minute window
	mk := func(until time.Time) map[string]any {
		s, _ := SealParamsUntil(map[string]any{"1": "1"}, k, sealedAt, &until)
		return s
	}
	cases := []struct {
		name  string
		until time.Time
		want  time.Time
	}{
		{"issuer shorter", sealedAt.Add(2 * time.Minute), sealedAt.Add(2 * time.Minute)},
		{"issuer longer, window caps", sealedAt.Add(time.Hour), sealedAt.Add(5 * time.Minute)},
		{"issuer equal", sealedAt.Add(5 * time.Minute), sealedAt.Add(5 * time.Minute)},
	}
	for _, c := range cases {
		got, ok := p.Deadline(mk(c.until))
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s: deadline = %v (ok=%v), want %v", c.name, got, ok, c.want)
		}
	}
	if d, _ := (OTPPolicy{MaxValidity: 10 * time.Minute}).Deadline(mk(sealedAt.Add(time.Hour))); !d.Equal(sealedAt.Add(10 * time.Minute)) {
		t.Errorf("configured window ignored: %v", d)
	}
}

func TestOTPPolicy_CheckBoundaries(t *testing.T) {
	sealedAt := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	until := sealedAt.Add(time.Minute)
	env, _ := SealParamsUntil(map[string]any{"1": "1"}, k, sealedAt, &until)
	p := OTPPolicy{}
	if err := p.Check(env, until.Add(-time.Second)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := p.Check(env, until); !errors.Is(err, ErrOTPExpired) {
		t.Fatalf("at the deadline the code is already expired: %v", err)
	}
	if err := p.Check(env, until.Add(time.Hour)); !errors.Is(err, ErrOTPExpired) {
		t.Fatalf("after: %v", err)
	}
}

func TestOTPPolicy_MissingValidity(t *testing.T) {
	now := time.Now()
	env, _ := SealParams(map[string]any{"1": "1"}, k, now)
	if err := (OTPPolicy{}).Check(env, now); err != nil {
		t.Fatalf("transition mode must not invalidate legacy codes: %v", err)
	}
	if err := (OTPPolicy{RequireValidUntil: true}).Check(env, now); !errors.Is(err, ErrOTPValidityMissing) {
		t.Fatalf("enforced: %v", err)
	}
	// Unsealed legacy params count as "no validity" too.
	if err := (OTPPolicy{RequireValidUntil: true}).Check(map[string]any{"1": "x"}, now); !errors.Is(err, ErrOTPValidityMissing) {
		t.Fatalf("legacy plaintext: %v", err)
	}
}

func TestSealParamsUntil_StoresUTCRegardlessOfOffset(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	tokyo := time.FixedZone("JST", 9*3600)
	local := time.Date(2030, 1, 1, 10, 0, 0, 0, brt)  // 13:00Z
	same := time.Date(2030, 1, 1, 22, 0, 0, 0, tokyo) // 13:00Z
	a, _ := SealParamsUntil(map[string]any{"1": "1"}, k, time.Now(), &local)
	b, _ := SealParamsUntil(map[string]any{"1": "1"}, k, time.Now(), &same)
	want := time.Date(2030, 1, 1, 13, 0, 0, 0, time.UTC).Unix()
	if a[SealedValidUntilKey] != want || b[SealedValidUntilKey] != want {
		t.Fatalf("valid_until = %v / %v, want %v", a[SealedValidUntilKey], b[SealedValidUntilKey], want)
	}
	// The deadline is not a secret: it is readable without the key.
	if IsSealed(a) && a[SealedKey] == "" {
		t.Fatal("envelope lost its ciphertext")
	}
}

func TestExpiredDoesNotDropLegacyCodesBeforeRetention(t *testing.T) {
	now := time.Now()
	legacy, _ := SealParams(map[string]any{"1": "1"}, k, now.Add(-2*time.Hour))
	if (OTPPolicy{RequireValidUntil: true}).Expired(legacy, now) {
		t.Fatal("a pending legacy campaign must keep its code until the retention limit")
	}
	old, _ := SealParams(map[string]any{"1": "1"}, k, now.Add(-SealedTTL-time.Minute))
	if !(OTPPolicy{}).Expired(old, now) {
		t.Fatal("retention limit must drop it")
	}
}
