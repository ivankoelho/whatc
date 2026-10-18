package middleware_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func lines(buf *bytes.Buffer) []string {
	s := strings.TrimSpace(buf.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestFailureLog_FirstNowThenOnePerInterval(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	fl := middleware.NewFailureLog(time.Minute, clock.Now)
	var buf bytes.Buffer
	log := bufLogger(&buf)
	boom := errors.New("dial tcp: connection refused")

	fl.Report(log, "ratelimit.middleware", "login", boom)
	require.Len(t, lines(&buf), 1, "the first failure is logged at once")
	assert.Contains(t, buf.String(), "suppressed_since_last_log=0")

	for i := 0; i < 5; i++ { // repeats inside the interval are only counted
		clock.Advance(5 * time.Second)
		fl.Report(log, "ratelimit.middleware", "login", boom)
	}
	assert.Len(t, lines(&buf), 1, "no spam for a persistent failure")

	clock.Advance(40 * time.Second) // 65s since the first line
	fl.Report(log, "ratelimit.middleware", "login", boom)
	require.Len(t, lines(&buf), 2, "after the interval it speaks again")
	assert.Contains(t, lines(&buf)[1], "suppressed_since_last_log=5", "and says how many it left out")

	fl.Report(log, "ratelimit.middleware", "login", boom) // right after: counted again, from zero
	assert.Len(t, lines(&buf), 2)
}

func TestFailureLog_EachComponentAndOperationHasItsOwnFirstLine(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	fl := middleware.NewFailureLog(time.Minute, clock.Now)
	var buf bytes.Buffer
	log := bufLogger(&buf)
	boom := errors.New("boom")

	fl.Report(log, "ratelimit.middleware", "login", boom)
	fl.Report(log, "ratelimit.middleware", "api_global", boom)
	fl.Report(log, "chat.upload", "upload_rate_limit", boom)
	fl.Report(log, "chat.upload", "upload_rate_limit", boom) // suppressed
	out := buf.String()
	require.Len(t, lines(&buf), 3, "one distinct failure each: an outage of one limiter never hides another")
	for _, want := range []string{"component=ratelimit.middleware", "operation=login", "operation=api_global", "component=chat.upload", "operation=upload_rate_limit"} {
		assert.Contains(t, out, want)
	}
	assert.Contains(t, out, "failing open")
}

// A Redis that is down while the limiter keeps serving: requests still pass, and the log is one line, not one per request.
func TestEnforce_RedisDown_FailsOpenAndLogsOnceWithoutIdentifiers(t *testing.T) {
	broken := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, MaxRetries: -1})
	defer broken.Close()

	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	orig := middleware.Failures
	middleware.Failures = middleware.NewFailureLog(time.Minute, clock.Now)
	defer func() { middleware.Failures = orig }()

	var buf bytes.Buffer
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Redis: broken, Log: bufLogger(&buf), Max: 1, Window: time.Minute, KeyPrefix: "login",
	})
	for i := 0; i < 8; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("203.0.113.77:5555"))
		assert.NotNil(t, rl(req), "fail open: the request %d is allowed", i)
	}
	require.Len(t, lines(&buf), 1, "8 failing requests, one log line")
	out := buf.String()
	assert.Contains(t, out, "operation=login", "the limiter is identified by its name")
	assert.NotContains(t, out, "203.0.113.77", "the client IP is not logged")
	assert.NotContains(t, out, "ratelimit:login:", "nor the rate limit key")

	// Per-user limiter: the user id must not show up either.
	uid := uuid.New()
	var buf2 bytes.Buffer
	url := middleware.UserAwareRateLimit(middleware.RateLimitOpts{
		Redis: broken, Log: bufLogger(&buf2), Max: 1, Window: time.Minute, KeyPrefix: "api_global",
	})
	req := newTestRequest()
	req.RequestCtx.SetUserValue(middleware.ContextKeyUserID, uid)
	req.RequestCtx.SetRemoteAddr(mockAddr("203.0.113.78:5555"))
	assert.NotNil(t, url(req))
	require.Len(t, lines(&buf2), 1)
	assert.NotContains(t, buf2.String(), uid.String(), "the user id is not logged")
	assert.Contains(t, buf2.String(), "operation=api_global")

	// After the interval a persistent failure is reported again, with the count.
	clock.Advance(2 * time.Minute)
	req = newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("203.0.113.77:5555"))
	assert.NotNil(t, rl(req))
	require.Len(t, lines(&buf), 2)
	assert.Contains(t, lines(&buf)[1], "suppressed_since_last_log=7")
}

// A healthy Redis logs nothing and still limits: the throttle changes nothing about the decision.
func TestEnforce_HealthyRedis_StillLimitsAndLogsNothing(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	var buf bytes.Buffer
	rl := middleware.RateLimit(middleware.RateLimitOpts{
		Redis: rdb, Log: bufLogger(&buf), Max: 2, Window: 10 * time.Second, KeyPrefix: "fl_" + uuid.NewString()[:8],
	})
	for i := 0; i < 2; i++ {
		req := newTestRequest()
		req.RequestCtx.SetRemoteAddr(mockAddr("198.51.100.9:1"))
		require.NotNil(t, rl(req))
	}
	req := newTestRequest()
	req.RequestCtx.SetRemoteAddr(mockAddr("198.51.100.9:1"))
	assert.Nil(t, rl(req), "the third request is still refused")
	assert.Empty(t, lines(&buf), "no failure, no log line")
}
