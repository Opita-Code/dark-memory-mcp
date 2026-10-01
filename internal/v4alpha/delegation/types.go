// Package delegation — Sub-task extraction for delegate_intent (Phase 7 alpha.19).
//
// Per SPEC-alpha-11-phase7.md §3.1, this package replaces the alpha.18.1 v1
// deterministic DECIDE router with a hybrid: DECIDE determinista + EXTRACT
// LLM-based (only when DECIDE=delegate AND length>200 OR vibe_case=C7).
// Validation via drift_judge (eval_type=subtask_extraction). Failure surfaces
// to operator via needs_human with alternatives[].
//
// # Architecture (Alt-1+ per SPEC §3.1)
//
//	DECIDE (deterministic v1) →
//	  if decision=="delegate" AND (length>200 OR vibe_case=="C7"):
//	    EXTRACT (LLM call, persona judge-delegator) →
//	      cache lookup → LLM call → parse JSON → validate → drift_judge →
//	      on drift_detected: refine + retry (max 2) →
//	      on needs_human: surface alternatives[] to operator
//	  else:
//	    PLAN (deterministic v1 sentence split)
//	MIND per subtask (composeSystemPrompt from mindset.go)
//	CURATE (subagent_register per subtask — wired in Chunk 7.2)
//
// # File map
//
//	router.go    — DECIDE + PLAN (moved from delegation.go alpha.18.1)
//	extract.go   — EXTRACT step (LLM call + JSON parse + cache)
//	validate.go  — cap 8 subtasks, min_length 10, topological sort
//	cache.go     — LLM result cache (sha256 key) with in-memory fallback
//	judge.go     — validateSubtasks via drift_judge (eval_type=subtask_extraction)
//	types.go     — Subtask, ExtractResult, Alternative, FailureMode (this file)
package delegation

import "fmt"

// MaxSubtasks is the cap on subtasks per EXTRACT call. SPEC §3.1 §3.1 P4
// constraint: prevents explosion. >8 truncates + audit row kind=observation.
const MaxSubtasks = 8

// MinSubtaskLength is the floor for each subtask's description (chars).
// SPEC §3.1 P4: prevents "write 'h'" + "write 'world'" granularity.
const MinSubtaskLength = 10

// MaxRetries is the cap on retry rounds after drift_detected. After MaxRetries
// the failure surfaces to operator via alternatives[] (needs_human). SPEC §3.1
// decision sub-Q2: 2 retries = 3 total attempts (matches DARK_MINDSET_MAX_ITERATIONS=3).
const MaxRetries = 2

// Subtask is one extracted atomic unit. The LLM produces these via the
// EXTRACT step; MIND populates SystemPrompt + Model + Tools; CURATE
// (Chunk 7.2) populates SubagentID + DelegationContext.
type Subtask struct {
	// ID is the canonical subtask identifier ("subtask-1", "subtask-2", ...).
	// Assigned by the EXTRACT step in order returned by the LLM.
	ID string

	// Description is the atomic work description (≥MinSubtaskLength chars).
	// This is what gets passed to MIND for system_prompt composition.
	Description string

	// VibeCase is the canonical C1..C7 for this subtask. Defaults to
	// the parent task's vibe_case. Phase 7 alpha.19 Chunk 7.1 does not
	// support per-subtask vibe_case override (planned for alpha.20+).
	VibeCase string

	// Dependencies lists other subtask IDs that must complete before this
	// one starts. Empty when independent. Validated for topological sort
	// in validate.go.
	Dependencies []string

	// SystemPrompt is composed by MIND (composeSystemPrompt from mindset.go).
	// Populated after EXTRACT returns.
	SystemPrompt string

	// Model is the recommended model. Always "inherit" (parent model) per
	// alpha.18.1 chunk 6.3 default.
	Model string

	// Tools lists the recommended tools for this subtask. Empty array
	// (no override) by default.
	Tools []string

	// SubagentID is the opaque uuid from dark_memory_subagent_register.
	// Populated by CURATE in Chunk 7.2. Empty in Chunk 7.1.
	SubagentID string

	// DelegationContext is the JSON-encoded parent_agent + parent_session +
	// project_id + subtask_index metadata. Populated by CURATE in Chunk 7.2.
	DelegationContext string
}

// ExtractResult is what the EXTRACT step returns. Contains the LLM's
// decision + the extracted subtasks + the reasoning trace.
type ExtractResult struct {
	// Decision is the LLM's choice: "inline" | "delegate" | "refused".
	// Inherits from DECIDE for "inline" and "refused" (LLM not invoked).
	Decision string

	// Subtasks is the LLM-extracted decomposition. Empty when Decision !=
	// "delegate". Capped at MaxSubtasks.
	Subtasks []Subtask

	// Reasoning is the LLM's explanation. Empty for "inline" and "refused"
	// (DECIDE reasoning is used instead).
	Reasoning string

	// CacheHit is true when the EXTRACT result was served from cache.
	CacheHit bool

	// Verdict is the drift_judge validation verdict: aligned | drift_detected
	// | needs_human | cached | errored. Per SPEC §3.1 wire shape v2.
	Verdict string

	// Alternatives lists the options presented to the operator when
	// Verdict="needs_human". Empty otherwise. Per SPEC §3.1 failure modes.
	Alternatives []Alternative
}

// Alternative is one option presented to the operator when needs_human fires.
// Each alternative has a stable ID + a human-readable label + a deterministic
// outcome that the orchestrator can apply automatically if the operator picks it.
type Alternative struct {
	// ID is the canonical identifier ("accept-as-is", "fallback-plan",
	// "fallback-refuse", "refine-and-retry", "custom-instructions").
	ID string

	// Label is the human-readable one-line description.
	Label string

	// Outcome is the deterministic result if this alternative is selected.
	// Pure function of the original ExtractResult + choice.
	Outcome AlternativeOutcome
}

// AlternativeOutcome describes what happens when the operator picks an
// alternative. The orchestrator can dispatch on Kind.
type AlternativeOutcome struct {
	// Kind is "accept-as-is" | "fallback-plan" | "fallback-refuse" |
	// "refine-and-retry" | "custom-instructions".
	Kind string

	// Subtasks is the resulting subtask list. For "accept-as-is" this is
	// the original extraction; for "fallback-plan" it's the deterministic
	// PLAN result; for "fallback-refuse" it's empty.
	Subtasks []Subtask

	// Reasoning is the explanation to write to audit_log.
	Reasoning string
}

// FailureMode is one row of the SPEC §3.1 failure modes table. Each row
// maps a failure type to a verdict + an alternatives[] offering.
type FailureMode struct {
	// ID is the canonical failure identifier (used in audit_log kind=observation).
	ID string

	// Trigger describes what fired the failure.
	Trigger string

	// Verdict is always "needs_human" per SPEC P3.
	Verdict string

	// Alternatives is the fixed list offered to the operator.
	Alternatives []Alternative
}

// Predefined failure modes per SPEC §3.1 failure modes table.
var failureModes = map[string]FailureMode{
	"llm_network_error": {
		ID:      "llm_network_error",
		Trigger: "judge.LLMProvider.Call returned net error",
		Verdict: "needs_human",
		Alternatives: []Alternative{
			{ID: "fallback-plan", Label: "Use deterministic PLAN (sentence split) instead of LLM extraction", Outcome: AlternativeOutcome{Kind: "fallback-plan"}},
			{ID: "fallback-refuse", Label: "Refuse the task entirely (escalate to operator)", Outcome: AlternativeOutcome{Kind: "fallback-refuse"}},
			{ID: "refine-and-retry", Label: "Retry the LLM call after 30s", Outcome: AlternativeOutcome{Kind: "refine-and-retry"}},
		},
	},
	"llm_rate_limit": {
		ID:      "llm_rate_limit",
		Trigger: "LLM provider returned 429",
		Verdict: "needs_human",
		Alternatives: []Alternative{
			{ID: "fallback-plan", Label: "Use deterministic PLAN instead of LLM extraction", Outcome: AlternativeOutcome{Kind: "fallback-plan"}},
			{ID: "refine-and-retry", Label: "Retry after 60s (rate limit cooldown)", Outcome: AlternativeOutcome{Kind: "refine-and-retry"}},
			{ID: "fallback-refuse", Label: "Refuse the task", Outcome: AlternativeOutcome{Kind: "fallback-refuse"}},
		},
	},
	"llm_parse_error": {
		ID:      "llm_parse_error",
		Trigger: "LLM output did not match JSON schema",
		Verdict: "needs_human",
		Alternatives: []Alternative{
			{ID: "fallback-plan", Label: "Use deterministic PLAN", Outcome: AlternativeOutcome{Kind: "fallback-plan"}},
			{ID: "accept-partial", Label: "Accept the parsed subtasks (even if incomplete)", Outcome: AlternativeOutcome{Kind: "accept-as-is"}},
			{ID: "fallback-refuse", Label: "Refuse the task", Outcome: AlternativeOutcome{Kind: "fallback-refuse"}},
		},
	},
	"judge_drift_exhausted": {
		ID:      "judge_drift_exhausted",
		Trigger: "drift_judge returned drift_detected on all retry attempts",
		Verdict: "needs_human",
		Alternatives: []Alternative{
			{ID: "accept-as-is", Label: "Accept the LLM extraction despite judge concerns", Outcome: AlternativeOutcome{Kind: "accept-as-is"}},
			{ID: "fallback-plan", Label: "Use deterministic PLAN instead", Outcome: AlternativeOutcome{Kind: "fallback-plan"}},
			{ID: "custom-instructions", Label: "Provide custom instructions and re-invoke", Outcome: AlternativeOutcome{Kind: "custom-instructions"}},
		},
	},
	"judge_needs_human": {
		ID:      "judge_needs_human",
		Trigger: "drift_judge returned needs_human (judge can't decide quality)",
		Verdict: "needs_human",
		Alternatives: []Alternative{
			{ID: "accept-as-is", Label: "Accept the LLM extraction as-is", Outcome: AlternativeOutcome{Kind: "accept-as-is"}},
			{ID: "fallback-plan", Label: "Use deterministic PLAN instead", Outcome: AlternativeOutcome{Kind: "fallback-plan"}},
			{ID: "fallback-refuse", Label: "Refuse the task", Outcome: AlternativeOutcome{Kind: "fallback-refuse"}},
		},
	},
	"topo_cycle": {
		ID:      "topo_cycle",
		Trigger: "subtask dependencies form a cycle",
		Verdict: "needs_human",
		Alternatives: []Alternative{
			{ID: "drop-deps", Label: "Accept the extraction but drop dependency edges", Outcome: AlternativeOutcome{Kind: "accept-as-is"}},
			{ID: "fallback-plan", Label: "Use deterministic PLAN (no deps)", Outcome: AlternativeOutcome{Kind: "fallback-plan"}},
			{ID: "fallback-refuse", Label: "Refuse the task", Outcome: AlternativeOutcome{Kind: "fallback-refuse"}},
		},
	},
}

// FailureModeByID returns the FailureMode for the given ID. Used by extract.go
// when a failure occurs to populate ExtractResult.Alternatives.
func FailureModeByID(id string) (FailureMode, error) {
	fm, ok := failureModes[id]
	if !ok {
		return FailureMode{}, fmt.Errorf("delegation: unknown failure mode %q", id)
	}
	return fm, nil
}