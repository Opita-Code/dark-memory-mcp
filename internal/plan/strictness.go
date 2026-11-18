// Package plan (continuation) — SPEC-194 (T4): the strictness of the
// plan gate.
//
// This file is the EXACT mirror of internal/drift/strictness.go, with
// its own name and its own env var. The mirror is deliberate: the
// repo already has one operator policy switch with three modes, and a
// second switch with a different shape would be two dialects of the
// same idea.
//
// # Why the default is warn and not off
//
// The drift strictness defaults to `off` because the drift check
// costs an LLM round-trip (60-130 s) on every artifact-creating
// write, and an operator who does not want that wait should not pay
// it. The plan gate is different on both counts:
//
//   - It is DETERMINISTIC. There is no LLM in it, so it costs a
//     store read, not a round-trip.
//   - The whole point of the PR B is to MEASURE the obligation (D8,
//     SPEC-194 §3.6): "the gate is born measuring, not born
//     blocking". A default of `off` would switch the measurement off
//     on the first day, and the KPI of T10 would have no data.
//
// So `warn` is the default: the gate checks, records the refusal in
// write_audit and in the Error Observatory, and lets the call
// through. The refusal is visible without the gate stopping the work
// that today does not follow the flow.
//
// # Why `strict` is not the default either
//
// Measured: this repo's own work (writing specs, publishing
// releases, updating memories) calls mutating tools without an
// approved plan. A gate that stops the work is a gate that gets
// disabled on day one. `strict` is enabled when the adapter (W2/K2)
// is standing, or earlier only for the tools that declare an
// irreversible effect (AICA: reversibility x impact).
package plan

import (
	"fmt"
	"os"
	"strings"
)

// Strictness is the operator's policy choice for the plan gate.
//
//	StrictnessOff:    the gate does not check. Default for a caller
//	                  that explicitly disables it.
//
//	StrictnessWarn:   the gate checks and, on refusal, RECORDS the
//	                  refusal (write_audit + Error Observatory) and
//	                  lets the call through. This is the default of
//	                  the operator switch.
//
//	StrictnessStrict: the gate checks and, on refusal, REFUSES the
//	                  call with ErrPlanRequired / ErrNotApproved /
//	                  ErrBudgetExceeded.
//
// The zero value is StrictnessOff, exactly like the drift one: a
// zero-valued struct must not start refusing calls.
type Strictness int

const (
	// StrictnessOff skips the plan check entirely.
	StrictnessOff Strictness = iota
	// StrictnessWarn records the refusal and lets the call through.
	StrictnessWarn
	// StrictnessStrict refuses the call.
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

// ParseStrictness maps an env-string to a Strictness.
//
// It is the same defensive contract as the drift one: an UNKNOWN
// value never refuses a call. But the fallback is NOT the same, and
// the difference is deliberate:
//
//   - drift: unknown -> off. The drift check costs an LLM round-trip,
//     so the safe default is the one that does not spend it.
//   - plan: unknown -> warn. The plan check is free, and the point of
//     the switch is to MEASURE. Falling back to `off` because of a
//     typo would silently switch the measurement off, which is the
//     opposite of what an operator who typed a typo wants.
//
// The warning is emitted through the optional callback (nil = no
// warning), so the caller decides where it goes.
//
// Accepted values (case-insensitive, whitespace-trimmed):
//
//	""      -> StrictnessWarn (the default of the switch)
//	"off"   -> StrictnessOff
//	"0"     -> StrictnessOff
//	"false" -> StrictnessOff
//	"no"    -> StrictnessOff
//	"warn"  -> StrictnessWarn
//	"1"     -> StrictnessWarn (numeric alternate)
//	"true"  -> StrictnessWarn (boolean alternate)
//	"yes"   -> StrictnessWarn
//	"strict"-> StrictnessStrict
//	"2"     -> StrictnessStrict (numeric alternate)
func ParseStrictness(s string, warnf func(format string, args ...any)) Strictness {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "", "warn", "1", "true", "yes":
		return StrictnessWarn
	case "off", "0", "false", "no":
		return StrictnessOff
	case "strict", "2":
		return StrictnessStrict
	default:
		if warnf != nil {
			warnf("plan: DARK_PLAN_STRICTNESS=%q invalid (want off|warn|strict); defaulting to warn", s)
		}
		return StrictnessWarn
	}
}

// StrictnessFromEnv reads DARK_PLAN_STRICTNESS from env. Convenience
// wrapper used at boot time.
//
// The default of the ENV VAR is `warn` (see the package comment). The
// zero value of the TYPE is StrictnessOff, so a caller that builds a
// Strictness without asking the environment gets the conservative
// "does not block" behaviour, while the operator switch measures.
func StrictnessFromEnv() Strictness {
	return ParseStrictness(os.Getenv("DARK_PLAN_STRICTNESS"), nil)
}
