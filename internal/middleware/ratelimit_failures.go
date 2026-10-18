package middleware

import (
	"sync"
	"time"

	"github.com/zerodha/logf"
)

// FailureLog throttles the log lines about rate limiter failures. A Redis that is down
// would otherwise write one error per request (the global API limiter runs on every
// /api call), burying everything else, while a limiter that fails silently hides that its
// protection is off. The first failure of each (component, operation) is logged at once;
// repeats inside the interval are only counted; the next line after the interval says how
// many were left out.
//
// It only decides what is LOGGED. It never changes what the limiter does with the request.
// The line carries the component and the operation (a limiter name such as "login"), the
// cause, and the count: never the rate limit key (it holds IPs and user ids), credentials
// or anything from the request.
type FailureLog struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	state    map[string]*failureState
}

type failureState struct {
	lastLogged time.Time
	suppressed int
}

// NewFailureLog builds a throttle; now is injectable so tests do not depend on real time.
func NewFailureLog(interval time.Duration, now func() time.Time) *FailureLog {
	return &FailureLog{interval: interval, now: now, state: map[string]*failureState{}}
}

// Failures is the throttle shared by every limiter in the process.
var Failures = NewFailureLog(time.Minute, time.Now)

// Report records a failure of the rate limiter's Redis call; the caller has already decided
// to let the request through (fail open), which the message says.
func (f *FailureLog) Report(log logf.Logger, component, operation string, err error) {
	key := component + "|" + operation
	f.mu.Lock()
	st := f.state[key]
	t := f.now()
	if st != nil && t.Sub(st.lastLogged) < f.interval {
		st.suppressed++
		f.mu.Unlock()
		return
	}
	suppressed := 0
	if st == nil {
		st = &failureState{}
		f.state[key] = st
	} else {
		suppressed = st.suppressed
	}
	st.lastLogged, st.suppressed = t, 0
	f.mu.Unlock()

	log.Error("Rate limiter Redis failure: failing open (the request was allowed)",
		"component", component, "operation", operation, "error", err,
		"suppressed_since_last_log", suppressed)
}
