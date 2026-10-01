package recall

import (
	"math"
	"time"
)

// log10Fn wraps math.Log10 so the strategy files don't need to
// import math directly. Tiny indirection; lets us add caching or
// precomputed tables later without rewriting strategy callers.
func log10Fn(x float64) float64 { return math.Log10(x) }

// mathExp wraps math.Exp. Same rationale as log10Fn.
func mathExp(x float64) float64 { return math.Exp(x) }

// timeSince returns the elapsed hours between t and now.
// Returns 0 for zero time (so decay function doesn't blow up
// on legacy rows with NULL updated_at).
func timeSince(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return time.Since(t).Hours()
}
