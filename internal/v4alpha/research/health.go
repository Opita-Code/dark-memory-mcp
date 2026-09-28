// Backend health state machine for the v4 research executor. Each
// backend has a Health struct that records its current status, the
// rolling error count, the total call count, and the last time it
// successfully answered. The executor consults Health before every
// call: a Dead backend is skipped; a Degraded one is deprioritized;
// only a Healthy one is called as primary.
//
// The state machine is the operator's defence against the
// already-observed reality (research probe 2026-09-27): urlhaus
// flipped to 401, ahmia to 404, archive.org to 000 — backends die
// without warning. A 12 s timeout in the HTTP layer prevents the
// caller from waiting; a 3-consecutive-error downgrade prevents the
// caller from waiting AGAIN. A 10-error hard-kill prevents the
// backend from showing up in `tools/list` output at all until the
// operator runs `?refresh=true` (handled in research_executor.go).
package research

import (
	"errors"
	"sync"
	"time"
)

// BackendStatus is the public health classification of one backend.
type BackendStatus int

const (
	// StatusHealthy means the backend answered the last call
	// without error and no consecutive-error threshold is breached.
	StatusHealthy BackendStatus = iota

	// StatusDegraded means the backend has had ConsecutiveErrors
	// (3 by default) without a success. The executor still uses
	// it as a last-resort fallback, but never as primary.
	StatusDegraded

	// StatusDead means the backend has had HardKillErrors
	// (10 by default) without a success. The executor skips it
	// entirely until the operator runs `?refresh=true`.
	StatusDead
)

// String renders the status for audit and operator-facing logs.
// Stable across versions so audit queries keep working.
func (s BackendStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusDegraded:
		return "degraded"
	case StatusDead:
		return "dead"
	default:
		return "unknown"
	}
}

// Sentinel errors for the health package. Callers use errors.Is to
// distinguish a Dead backend from a Degraded one.
var (
	// ErrBackendDead is returned by Executor when the chosen
	// backend is StatusDead. The caller should try the next one.
	ErrBackendDead = errors.New("research: backend is dead (operator refresh required)")

	// ErrBackendDegraded is a soft signal; the executor may still
	// try the call but should record the outcome. The transport
	// layer turns this into a warning, not an error.
	ErrBackendDegraded = errors.New("research: backend is degraded")
)

// Health thresholds. Operators can change these in a future slice;
// the defaults match the §3.2 risk matrix (R3, AC-R1, AC-R2).
const (
	// DegradeAfter is the rolling consecutive-error count that
	// downgrades a backend to StatusDegraded. A single success
	// resets the counter. 3 is conservative: a 1-off 500 should
	// not be over-interpreted, but 3 in a row is a real signal.
	DegradeAfter = 3

	// KillAfter is the rolling consecutive-error count that
	// hard-kills a backend. After KillAfter, the backend is
	// skipped entirely until the operator runs `?refresh=true`.
	// 10 means a backend that "always fails" is removed from
	// the router for the rest of the session.
	KillAfter = 10
)

// Health tracks one backend's state. Safe for concurrent use: the
// executor calls RecordSuccess / RecordError from many goroutines,
// and reads via Snapshot from the same.
type Health struct {
	mu sync.Mutex

	// Status is the current classification. StatusHealthy is
	// the zero value.
	Status BackendStatus

	// ConsecutiveErrors is the count since the last success.
	// Reset to 0 on every success.
	ConsecutiveErrors int

	// TotalCalls and TotalErrors are cumulative since boot. They
	// survive consecutive-error resets; they let the operator see
	// "this backend has 90% error rate over 200 calls" rather than
	// just "this backend has had 3 errors in a row".
	TotalCalls  int64
	TotalErrors int64

	// LastSeen is the timestamp of the last successful call. Zero
	// for a backend that has never answered. Operators consult
	// LastSeen to decide whether a refresh is worth running.
	LastSeen time.Time

	// LastError is the wrapped error of the most recent failed
	// call. Used for audit; never returned to operators unfiltered.
	LastError string
}

// NewHealth returns a Health in StatusHealthy with zero counters.
// Backend name is not stored here — the caller maps Health to name
// in the executor's map. Splitting keeps Health a pure value type.
func NewHealth() *Health {
	return &Health{Status: StatusHealthy}
}

// Snapshot returns a copy of the current state. Useful for the audit
// row the executor emits after a call. The returned Health is a deep
// value, not a reference to the live one.
func (h *Health) Snapshot() Health {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Health{
		Status:            h.Status,
		ConsecutiveErrors: h.ConsecutiveErrors,
		TotalCalls:        h.TotalCalls,
		TotalErrors:       h.TotalErrors,
		LastSeen:          h.LastSeen,
		LastError:         h.LastError,
	}
}

// RecordSuccess notes that a call to this backend returned without
// error. ConsecutiveErrors resets to 0, LastSeen updates, Status
// returns to StatusHealthy. A Success on a Dead backend is a normal
// recovery (the operator ran `?refresh=true`); the dead flag is
// removed and the backend is re-entered into the rotation.
func (h *Health) RecordSuccess(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.TotalCalls++
	h.ConsecutiveErrors = 0
	h.LastSeen = now
	h.LastError = ""
	h.Status = StatusHealthy
}

// RecordError notes that a call to this backend failed. The error
// string is captured (truncated to 256 chars) for audit. Status
// transitions:
//   - Healthy + DegradeAfter consecutive errors → Degraded
//   - Healthy + KillAfter consecutive errors → Dead
//   - Degraded + (DegradeAfter - 1) more → still Degraded
//   - Degraded + KillAfter cumulative consecutive → Dead
//
// A Success between errors resets the counter, so a "12 failures
// in 3 months" is not a 12-consecutive-error run; it would only
// downgrade after 3 in a row.
func (h *Health) RecordError(err error, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.TotalCalls++
	h.TotalErrors++
	h.ConsecutiveErrors++
	if err != nil {
		msg := err.Error()
		if len(msg) > 256 {
			msg = msg[:256]
		}
		h.LastError = msg
	}
	switch {
	case h.ConsecutiveErrors >= KillAfter:
		h.Status = StatusDead
	case h.ConsecutiveErrors >= DegradeAfter:
		h.Status = StatusDegraded
	}
}

// IsCallable reports whether the executor should attempt a call to
// this backend. StatusDead returns false; the others return true.
// The Degraded case is still callable (with a warning); the executor
// uses Tier and explicit allow-list to skip a Degraded backend when
// a Healthy alternative exists.
func (h *Health) IsCallable() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Status != StatusDead
}
