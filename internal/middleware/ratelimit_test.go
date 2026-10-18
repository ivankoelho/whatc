package middleware_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}

	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Redis:     rdb,
		Log:       testutil.NopLogger(),
		Max:       5,
		Window:    10 * time.Second,
		KeyPrefix: "test_allow_" + uuid.New().String()[:8],
	})

	for i := 0; i < 5; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.1:12345"))
		result := rl(req)
		require.NotNil(t, result, "request %d should be allowed", i+1)
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	t.Parallel()
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}

	prefix := "test_block_" + uuid.New().String()[:8]
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Redis:     rdb,
		Log:       testutil.NopLogger(),
		Max:       3,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	for i := 0; i < 3; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.2:12345"))
		result := rl(req)
		require.NotNil(t, result)
	}

	// 4th request should be blocked
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.2:12345"))
	result := rl(req)
	assert.Nil(t, result, "request over limit should be blocked")
	assert.Equal(t, fasthttp.StatusTooManyRequests, req.RequestCtx.Response.StatusCode())
	assert.NotEmpty(t, string(req.RequestCtx.Response.Header.Peek("Retry-After")))
}

func TestRateLimit_DifferentIPsGetSeparateLimits(t *testing.T) {
	t.Parallel()
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}

	prefix := "test_sep_" + uuid.New().String()[:8]
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Redis:     rdb,
		Log:       testutil.NopLogger(),
		Max:       2,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	// Exhaust limit for IP A
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.3:12345"))
		require.NotNil(t, rl(req))
	}

	// IP B should still be allowed
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.4:12345"))
	result := rl(req)
	assert.NotNil(t, result, "different IP should have its own limit")
}

func TestUserAwareRateLimit_KeysByUserID(t *testing.T) {
	t.Parallel()
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}

	prefix := "test_user_" + uuid.New().String()[:8]
	rl := middleware.UserAwareRateLimit(middleware.RateLimitOpts{
		Redis:     rdb,
		Log:       testutil.NopLogger(),
		Max:       2,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	userA := uuid.New()
	userB := uuid.New()

	// Exhaust limit for user A (same IP for both users — simulates VPN)
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.5:12345"))
		req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, userA)
		require.NotNil(t, rl(req), "user A request %d should be allowed", i+1)
	}

	// User A is now blocked
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.5:12345"))
	req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, userA)
	assert.Nil(t, rl(req), "user A should be blocked after exceeding limit")

	// User B on the SAME IP should still be allowed
	req = newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.5:12345"))
	req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, userB)
	assert.NotNil(t, rl(req), "user B on same IP should have its own limit")
}

func TestUserAwareRateLimit_FallsBackToIP_WhenUnauthenticated(t *testing.T) {
	t.Parallel()
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}

	prefix := "test_noauth_" + uuid.New().String()[:8]
	rl := middleware.UserAwareRateLimit(middleware.RateLimitOpts{
		Redis:     rdb,
		Log:       testutil.NopLogger(),
		Max:       2,
		Window:    10 * time.Second,
		KeyPrefix: prefix,
	})

	// No user ID set — should key by IP
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.6:12345"))
		require.NotNil(t, rl(req))
	}

	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("10.0.0.6:12345"))
	assert.Nil(t, rl(req), "unauthenticated requests should be rate limited by IP")
}

// mockAddr implements net.Addr for testing.
type mockAddr string

func (a mockAddr) Network() string { return "tcp" }
func (a mockAddr) String() string  { return string(a) }

// Ensure newTestRequest is available (defined in middleware_test.go already,
// but if running in isolation this provides it).
func init() {
	_ = func() *fastglue.Request {
		ctx := &fasthttp.RequestCtx{}
		return &fastglue.Request{RequestCtx: ctx}
	}
}

func TestIncrWindow_CountsAndSetsExpiry(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := t.Context()
	key := "test_incr_" + uuid.New().String()
	defer rdb.Del(ctx, key)

	for want := int64(1); want <= 3; want++ {
		n, err := middleware.IncrWindow(ctx, rdb, key, 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, want, n)
	}
	ttl, err := rdb.PTTL(ctx, key).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, 5*time.Second)

	// Later hits do not push the window out: it is fixed, not sliding.
	time.Sleep(300 * time.Millisecond)
	_, _ = middleware.IncrWindow(ctx, rdb, key, 5*time.Second)
	ttl2, _ := rdb.PTTL(ctx, key).Result()
	assert.Less(t, ttl2, ttl, "the expiry keeps counting down")
}

// A counter left without an expiry (the old INCR-then-EXPIRE pair could be cut in two)
// must not stay over the limit forever: the next hit gives it one.
func TestIncrWindow_HealsAKeyThatHasNoExpiry(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := t.Context()
	key := "test_heal_" + uuid.New().String()
	defer rdb.Del(ctx, key)
	require.NoError(t, rdb.Set(ctx, key, 99, 0).Err()) // no TTL: the stuck state
	ttl, _ := rdb.PTTL(ctx, key).Result()
	require.Equal(t, time.Duration(-1), ttl)

	n, err := middleware.IncrWindow(ctx, rdb, key, time.Second)
	require.NoError(t, err)
	assert.EqualValues(t, 100, n)
	ttl, _ = rdb.PTTL(ctx, key).Result()
	assert.Greater(t, ttl, time.Duration(0), "it now expires")
	time.Sleep(1200 * time.Millisecond)
	n, _ = middleware.IncrWindow(ctx, rdb, key, time.Second)
	assert.EqualValues(t, 1, n, "and the window really resets")
}

func TestIncrWindow_ParallelHitsAreAllCountedWithOneExpiry(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := t.Context()
	key := "test_parallel_" + uuid.New().String()
	defer rdb.Del(ctx, key)
	const hits = 60
	seen := make(chan int64, hits)
	for i := 0; i < hits; i++ {
		go func() {
			n, err := middleware.IncrWindow(ctx, rdb, key, 5*time.Second)
			if err != nil {
				n = -1
			}
			seen <- n
		}()
	}
	got := map[int64]bool{}
	for i := 0; i < hits; i++ {
		got[<-seen] = true
	}
	assert.Len(t, got, hits, "every hit saw its own count: no duplicates, no errors")
	assert.True(t, got[1] && got[hits])
	ttl, _ := rdb.PTTL(ctx, key).Result()
	assert.Greater(t, ttl, time.Duration(0))
}
