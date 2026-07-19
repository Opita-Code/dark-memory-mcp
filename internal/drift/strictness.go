// Package drift implements the drift-at-write interceptor (M6) per
// ACTIVE_MEMORY_RFC.md §A6. The interceptor runs the drift_judge
// LLM-as-judge check SYNCHRONOUSLY before the artifact is persisted,
// then either refuses (strict mode) or flags (warn mode) per the
// configured Strictness.
//
// # Atomicity contract (5A.vi, M6)
//   - TWO public types: Strictness (enum) + Verdict (decision).
//   - ONE public function: Checker.CheckArtifact.
//   - DEPENDS on internal/orchestration (for Judge) and
//     internal/store (for GetSpec).
//   - DEPENDS on internal/vibeflow (for Artifact type).
//
// # Placement
// Per RFC §A6 the interceptor is "pre-commit on Store.SaveArtifact".
// In dark-mem-mcp the gate (internal/policy) wraps every tool call
// with PreCheck + Invoke + PostCheck. PostCheck is the closest hook
// that has access to both the LLM and the artifact payload; the
// interceptor lives there. The "in the same transaction" property
// is approximated: PostCheck runs BEFORE the orchestrator commits
// the artifact, in the same goroutine. Concurrent tool calls do
// race the artifact row, but the gate's refusal is "no save" not
// "rollback", so the race resolves at the orchestrator layer.
package drift

import (
	"fmt"
	"os"
	"strings"
)

// Strictness is the operator's policy choice for drift-at-write
// interception. Per ACTIVE_MEMORY_RFC.md §A6:
//
//   - StrictnessOff: skip drift check entirely. Default. Operators
//     who don't want the LLM round-trip on every SaveArtifact set
//     this.
//   - StrictnessWarn: run drift_judge. On drift_detected / needs_human,
//     LOG the verdict + allow the save with validation_status
//     "drift_pending". Operators can review the pending artifacts
//     out-of-band and call dark_memory_resolve_drift per row.
//   - StrictnessStrict: run drift_judge. On drift_detected /
//     needs_human, REFUSE the save with ErrDriftAtWrite. The
//     operator must resolve the drift via dark_memory_resolve_drift
//     before retrying.
//
// Per-project strictness is reserved for a future schema bump. For
// v1, the strictness is process-wide via DARK_DRIFT_STRICTNESS.
type Strictness int

const (
	// StrictnessOff skips the drift check. Default.
	StrictnessOff Strictness = iota
	// StrictnessWarn logs drift_detected + allows the save.
	StrictnessWarn
	// StrictnessStrict refuses drift_detected with ErrDriftAtWrite.
	StrictnessStrict
)

// String returns the canonical env-friendly name.
func (s Strictness) String() string {
	switch s {
	case StrictnessOff:
		return "off"
	case StrictnessWarn:
		return "warn"
	case StrictnessStrict:
		return "strict"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// ParseStrictness maps an env-string to a Strictness. Unknown values
// fall back to StrictnessOff (defensive — never accidentally refuse
// a save because the operator mis-typed the env).
//
// Accepted values (case-insensitive, whitespace-trimmed):
//
//	"off"     → StrictnessOff
//	""        → StrictnessOff (default)
//	"0"       → StrictnessOff
//	"false"   → StrictnessOff
//	"warn"    → StrictnessWarn
//	"1"       → StrictnessWarn (numeric alternate)
//	"true"    → StrictnessWarn (boolean alternate)
//	"strict"  → StrictnessStrict
//	"2"       → StrictnessStrict (numeric alternate)
//
// Unknown non-empty values log a warning via the optional callback
// (caller-supplied; nil = no warning) and return StrictnessOff.
func ParseStrictness(s string, warnf func(format string, args ...any)) Strictness {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "", "off", "0", "false", "no":
		return StrictnessOff
	case "warn", "1", "true", "yes":
		return StrictnessWarn
	case "strict", "2":
		return StrictnessStrict
	default:
		if warnf != nil {
			warnf("drift: DARK_DRIFT_STRICTNESS=%q invalid (want off|warn|strict); defaulting to off", s)
		}
		return StrictnessOff
	}
}

// StrictnessFromEnv reads DARK_DRIFT_STRICTNESS from env. Convenience
// wrapper used at boot time.
func StrictnessFromEnv() Strictness {
	return ParseStrictness(os.Getenv("DARK_DRIFT_STRICTNESS"), nil)
}

// ResolveStrictness (Wave 5X.3) merges per-project override with
// env-driven default. Resolution order:
//
//  1. projectOverride == "" (empty / unset / "default") → use env.
//  2. projectOverride is a valid strictness value → use it.
//  3. projectOverride is invalid → log a warning (if warnf != nil)
//     and fall back to env. Defensive — operators who set the
//     column to a typo don't get their saves silently refused.
//
// Examples:
//
//	ResolveStrictness("",         StrictnessWarn) → StrictnessWarn  (project says "use env"; env says warn)
//	ResolveStrictness("strict",   StrictnessOff)  → StrictnessStrict (project overrides env)
//	ResolveStrictness("default",  StrictnessOff)  → StrictnessOff   ("default" is the sentinel for env)
//	ResolveStrictness("garbage",  StrictnessOff)  → StrictnessOff   (warning + fallback)
//
// Calling StrictnessFromEnv() and passing the result is the
// canonical pattern at boot or per-call time.
func ResolveStrictness(projectOverride string, envValue Strictness, warnf func(format string, args ...any)) Strictness {
	trimmed := strings.ToLower(strings.TrimSpace(projectOverride))
	if trimmed == "" || trimmed == "default" {
		return envValue
	}
	parsed := ParseStrictness(trimmed, warnf)
	if parsed == StrictnessOff && trimmed != "off" && trimmed != "0" && trimmed != "false" && trimmed != "no" {
		// ParseStrictness fell back to Off because the input was
		// unknown. We've already warned via the callback (if any);
		// fall through to env.
		return envValue
	}
	return parsed
}