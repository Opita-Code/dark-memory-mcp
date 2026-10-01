// Package delegation — DECIDE + PLAN (deterministic v1, Phase 7 alpha.19).
//
// DECIDE is the deterministic priority chain from alpha.18.1 (chunk 6.3).
// PLAN is the deterministic sentence split. Both are pure functions —
// no LLM, no DB, no I/O. This file is the same logic that lived in
// internal/v4alpha/transport/mcp/delegation.go:97-145 in alpha.18.1,
// moved here per SPEC §3.1 to make room for EXTRACT in extract.go.
package delegation

import (
	"fmt"
	"strings"
)

// DecideDelegation applies deterministic rules to choose the delegation mode.
// Pure function — no LLM, no DB.
//
// Priority chain (alpha.18.1 row 2286 §A):
//
//  1. Refusal markers (highest priority)
//  2. Delegation markers
//  3. C7 multi vibe_case
//  4. Length-based default (>200 chars)
//  5. Default → inline
//
// Returns the decision + a reasoning string. The reasoning starts
// with "DECIDE:" so callers can distinguish it from PLAN/MIND/CURATE
// explanations in the wire shape.
//
// Per SPEC §3.1 §3.1 P1 (P1=C, EXTRACT intermediario), the EXTRACT step
// only runs when this function returns decision="delegate" AND one of:
//   - len(task) > 200
//   - vibeCase == "C7"
//
// Other "delegate" decisions (marker match only) use the deterministic PLAN.
//
// Exported (capital D) so transport/mcp can call it from
// handleDelegateIntent.
func DecideDelegation(vibeCase, task string) (decision, reason string) {
	lower := strings.ToLower(task)

	// Refusal keywords (highest priority).
	refusalMarkers := []string{"impossible", "cannot", "out of scope", "do not", "don't"}
	for _, m := range refusalMarkers {
		if strings.Contains(lower, m) {
			return "refused", fmt.Sprintf(
				"DECIDE: refused — task contains refusal marker %q (out of scope for the operator).",
				m,
			)
		}
	}

	// Delegation markers.
	delegateMarkers := []string{
		"parallel", "concurrent", "step by step", "first ... then",
		"and then", "split into", "subtask", "in parallel",
	}
	for _, m := range delegateMarkers {
		if strings.Contains(lower, m) {
			return "delegate", fmt.Sprintf(
				"DECIDE: delegate — task contains coordination marker %q (multi-step / parallel).",
				m,
			)
		}
	}

	// Vibe_case-based default.
	if vibeCase == "C7" {
		return "delegate", "DECIDE: delegate — C7 multi-modal vibe_case requires multi-vibe sub-agent dispatch."
	}

	// Length-based default.
	if len(task) > 200 {
		return "delegate", fmt.Sprintf(
			"DECIDE: delegate — task is %d chars (long enough to warrant planning + delegation).",
			len(task),
		)
	}

	return "inline", "DECIDE: inline — short task, no delegation markers, single vibe_case."
}

// ShouldExtract reports whether the EXTRACT step should run for this
// (decision, vibeCase, task) tuple. Per SPEC §3.1 P1 (Alt-1+):
//
//	EXTRACT iff decision=="delegate" AND (len(task)>200 OR vibe_case=="C7")
//
// Returns false for "inline" and "refused" decisions (EXTRACT would be
// wasteful) and for short "delegate" tasks (the deterministic PLAN
// handles them well enough).
func ShouldExtract(decision, vibeCase, task string) bool {
	if decision != "delegate" {
		return false
	}
	if vibeCase == "C7" {
		return true
	}
	if len(task) > 200 {
		return true
	}
	return false
}

// PlanSubtasks splits the task by sentence boundaries. Returns at
// least one subtask (the whole task) even if no boundaries found.
// Pure function. Returns [] for decision="refused".
//
// alpha.18.1 row 2287 §B: PLAN was deterministic. Chunk 7.1 keeps
// this for the cases where EXTRACT is skipped (short delegate tasks)
// and for the fallback alternative ("fallback-plan" in types.go).
//
// Exported (capital P) so transport/mcp can call it from
// handleDelegateIntent.
func PlanSubtasks(vibeCase, task, decision string) []Subtask {
	if decision == "inline" {
		return []Subtask{{
			ID:          "subtask-1",
			Description: task,
			VibeCase:    vibeCase,
			Model:       "inherit",
			Tools:       []string{},
		}}
	}
	if decision == "refused" {
		return nil
	}
	// Split on '.' '!' '?' + ';' + newline.
	sentences := splitSentences(task)
	if len(sentences) == 0 || len(sentences) == 1 {
		return []Subtask{{
			ID:          "subtask-1",
			Description: task,
			VibeCase:    vibeCase,
			Model:       "inherit",
			Tools:       []string{},
		}}
	}
	out := make([]Subtask, 0, len(sentences))
	for i, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < MinSubtaskLength {
			continue
		}
		out = append(out, Subtask{
			ID:          fmt.Sprintf("subtask-%d", i+1),
			Description: s,
			VibeCase:    vibeCase,
			Model:       "inherit",
			Tools:       []string{},
		})
	}
	if len(out) == 0 {
		return []Subtask{{
			ID:          "subtask-1",
			Description: task,
			VibeCase:    vibeCase,
			Model:       "inherit",
			Tools:       []string{},
		}}
	}
	return out
}

// splitSentences splits text on terminal punctuation (.!?) and
// semicolons (;). Pure function.
func splitSentences(text string) []string {
	var out []string
	var current strings.Builder
	for _, r := range text {
		switch r {
		case '.', '!', '?', ';':
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		case '\n':
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}