package recall

import "math"

// log10Fn wraps math.Log10 so the strategy files don't need to
// import math directly. Tiny indirection; lets us add caching or
// precomputed tables later without rewriting strategy callers.
func log10Fn(x float64) float64 { return math.Log10(x) }
