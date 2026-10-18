package middleware

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/zerodha/logf"
)

// ProbeRateLimiter checks, once at startup, that the Redis client the rate limiters use can
// really run the limiter's own primitive (IncrWindow: the Lua script with INCR, PTTL and
// PEXPIRE). A PING does not prove that: scripting can be disabled or an ACL can forbid
// EVAL/EVALSHA while PING still works.
//
// It uses a throw-away key that expires after one second, so nothing is left behind and it
// is safe to run on every start and from several instances at once. It changes no policy:
// when the check fails the limiters keep failing open exactly as before, and this only says
// so out loud, once, at the moment it can be fixed. It reports whether the limiter works and
// never aborts the start.
func ProbeRateLimiter(ctx context.Context, rdb *redis.Client, log logf.Logger) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	n, err := IncrWindow(ctx, rdb, "ratelimit:startup_probe:"+uuid.NewString(), time.Second)
	if err != nil {
		log.Error("Rate limiter is NOT available: the Redis check failed at startup; rate limits will fail open (requests are allowed) until this is fixed",
			"component", "ratelimit.startup", "error", err)
		return false
	}
	if n != 1 {
		log.Error("Rate limiter check returned an unexpected count; rate limits may misbehave",
			"component", "ratelimit.startup", "count", n)
		return false
	}
	log.Info("Rate limiter check passed", "component", "ratelimit.startup")
	return true
}
