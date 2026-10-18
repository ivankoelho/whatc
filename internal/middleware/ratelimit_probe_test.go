package middleware_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/logf"
)

func bufLogger(buf *bytes.Buffer) logf.Logger {
	return logf.New(logf.Opts{Writer: buf, Level: logf.InfoLevel})
}

func TestProbeRateLimiter_AvailableRedis(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	var logs bytes.Buffer
	ok := middleware.ProbeRateLimiter(t.Context(), rdb, bufLogger(&logs))
	assert.True(t, ok)
	assert.Contains(t, logs.String(), "Rate limiter check passed")
	assert.NotContains(t, logs.String(), "NOT available")

	// It leaves nothing behind: the probe key lives for one second.
	time.Sleep(1200 * time.Millisecond)
	keys, err := rdb.Keys(t.Context(), "ratelimit:startup_probe:*").Result()
	require.NoError(t, err)
	assert.Empty(t, keys, "the probe key expired")
}

func TestProbeRateLimiter_UnreachableRedisIsReportedNotFatal(t *testing.T) {
	// Nothing listens on port 1: a connection error.
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 300 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = rdb.Close() }()

	var logs bytes.Buffer
	start := time.Now()
	ok := middleware.ProbeRateLimiter(t.Context(), rdb, bufLogger(&logs))

	assert.False(t, ok, "reports that the limiter does not work")
	assert.Less(t, time.Since(start), 4*time.Second, "bounded: it cannot hang the start")
	out := logs.String()
	assert.Contains(t, out, "Rate limiter is NOT available")
	assert.Contains(t, out, "fail open", "says what that means for the requests")
	assert.Contains(t, out, "ratelimit.startup", "names the component")
	assert.Contains(t, out, "connect", "keeps the cause")
}

// PING works but the limiter's script is forbidden: the case a PING-only start check misses.
func TestProbeRateLimiter_ScriptingForbiddenByACL(t *testing.T) {
	admin := testutil.SetupTestRedis(t)
	if admin == nil {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx := t.Context()
	const user = "rl_probe_noeval"
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", user, "on", ">probe-test-pw", "~*", "&*", "+@all", "-eval", "-evalsha").Err())
	defer admin.Do(ctx, "ACL", "DELUSER", user)

	opts := admin.Options()
	restricted := redis.NewClient(&redis.Options{Addr: opts.Addr, DB: opts.DB, Username: user, Password: "probe-test-pw", MaxRetries: -1})
	defer func() { _ = restricted.Close() }()
	require.NoError(t, restricted.Ping(ctx).Err(), "this identity connects fine")
	who, err := restricted.Do(ctx, "ACL", "WHOAMI").Text()
	require.NoError(t, err)
	require.Equal(t, user, who, "the test really runs as the restricted user, not as default")

	var logs bytes.Buffer
	ok := middleware.ProbeRateLimiter(ctx, restricted, bufLogger(&logs))
	assert.False(t, ok)
	out := logs.String()
	assert.Contains(t, out, "Rate limiter is NOT available")
	assert.True(t, strings.Contains(strings.ToLower(out), "noperm") || strings.Contains(strings.ToLower(out), "permission"),
		"the permission error is in the log: %s", out)
	assert.NotContains(t, out, "Rate limiter check passed")
}
