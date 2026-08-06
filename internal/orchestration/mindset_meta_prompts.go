// Package orchestration — mindset_meta_prompts.go
//
// v2.7.0-alpha: the meta-prompts that drive the procedural composition
// loop in MindsetApply. These are exported so:
//   - operators can grep them and know exactly what the LLM sees
//   - tests can reference them and assert well-formedness
//   - they are version-controlled in code, not in opaque config files
//
// The two prompts correspond to the two Judge eval_types added in v2.7.0-alpha:
//   - EvalMindsetCompose: GENERATIVE — LLM synthesizes a subagent system_prompt
//   - EvalMindsetQuality:  VALIDATIVE — LLM judges whether the prompt is well-formed
//
// Per the orchestrator pattern (LLMClient.Judge), the meta-prompt is sent as
// the user message (`req.Content`); the per-eval_type system prompt in
// `defaultSystemForEval` (llm_client.go) sets the LLM into JSON-output mode
// and defines the schema. The meta-prompt is what *defines the task*.

package orchestration

import (
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// MetaPromptCompose is the user message sent to the LLM when generating
// a subagent system_prompt for a delegated task. The LLM is told (via
// defaultSystemForEval for eval_type=mindset_compose) to reply with a
// specific JSON schema; this prompt defines the inputs and constraints.
//
// The 5 meta-categories (c1/security-review, c1/refactor, c2/docs-explain,
// c2/marketing-copy, c3+generalist) are starting frames — the LLM uses
// them as inspiration for the right kind of "over-qualification" given
// the actual task, not as pre-baked content.
const MetaPromptCompose = `You are a meta-system-prompt engineer. Your job is to produce a system prompt for a subagent that will execute a delegated task. The harness (Claude Code, opencode, Cursor, etc.) will spawn this subagent with your prompt as its system message.

HARD CONSTRAINTS (the judge will FAIL you if you violate these):
1. OVER-QUALIFY with specific expertise and track record, not generic ("senior appsec researcher with 12 years in OWASP Top 10 and CVE triage" beats "experienced engineer").
2. TASK-APPROPRIATE: the expertise you name must match the actual task being delegated. A C2 (text) task with a security mindset = FAIL.
3. CONSTRAINT-PRIMED: include 3-5 explicit "do not" statements. The subagent must know what NOT to do as much as what to do. This is the calibration knob.
4. MINIMAL-TOOLS: propose 3-6 tools, all directly relevant to the task. No over-permissioning. If the task doesn't need Bash, don't include it.
5. NO-LEAKAGE: the subagent must NOT know it was generated procedurally. No mentions of "meta-prompt", "generated", "judge", "iteration", "synthesis", or any reference to this prompt itself. The subagent should read the prompt and believe it was written by the principal.

INPUTS:
  vibe_case: {{VIBE_CASE}}              (C1=code, C2=text, C3=image, C4=video, C5=audio, C6=multimodal, C7=mixed)
  task_description: {{TASK_DESCRIPTION}}
  model_floor: {{MODEL_FLOOR}}          (the subagent's capability floor; "sonnet", "opus", "haiku")
  category_hint: {{CATEGORY}}           (one of: c1/security-review, c1/refactor, c2/docs-explain, c2/marketing-copy, c3+generalist — use as starting frame, not verbatim)

{{IF PREVIOUS ATTEMPTS}}
PREVIOUS ATTEMPTS + JUDGE FEEDBACK (incorporate this — do not repeat the same mistakes):
{{PREVIOUS_ATTEMPTS_HISTORY}}
{{END}}

OUTPUT (strict JSON, no prose, no markdown fences):
{
  "role": "<one-line role statement>",
  "goal": "<one-line goal statement>",
  "backstory": "<2-4 sentence backstory establishing expertise + track record>",
  "constraints": ["<do-not-1>", "<do-not-2>", "<do-not-3>", "<do-not-4-optional>", "<do-not-5-optional>"],
  "tools_recommended": ["<Tool1>", "<Tool2>", "<Tool3>", "<Tool4-optional>", "<Tool5-optional>", "<Tool6-optional>"],
  "model_recommended": "<sonnet|opus|haiku|inherit>"
}

The 'role + goal + backstory' will be composed into a final system_prompt that the harness uses. Make every word earn its place.`

// MetaPromptQuality is the user message sent to the LLM when VALIDATING
// a proposed subagent system_prompt against the 5 pass criteria. The LLM
// is told (via defaultSystemForEval for eval_type=mindset_quality) to
// reply with a verdict schema; this prompt defines the criteria.
const MetaPromptQuality = `You are evaluating whether a proposed subagent system prompt is well-formed for the given delegated task. The principal will use your verdict to either accept the prompt (and spawn the subagent) or reject it (and regenerate).

INPUTS:
  vibe_case: {{VIBE_CASE}}              (C1=code, C2=text, C3=image, ...)
  task_description: {{TASK_DESCRIPTION}}
  proposed_system_prompt: {{PROPOSED_SYSTEM_PROMPT}}

PASS CRITERIA — ALL must be true for verdict="aligned":

1. OVER-QUALIFIED
   The backstory names SPECIFIC expertise with track record (years of experience, named frameworks/methodologies, specific domain).
   PASS examples: "12 years in OWASP Top 10 and CVE triage", "senior conversion copywriter with 200+ A/B tests at Shopify-stripe scale"
   FAIL examples: "experienced engineer", "you are helpful", "you are an expert"

2. TASK-APPROPRIATE
   The named expertise must actually match the (vibe_case, task_description) pair.
   PASS examples: C2 marketing-copy task with "conversion copywriter" expertise
   FAIL examples: C2 marketing-copy task with "security researcher" expertise

3. CONSTRAINT-PRIMED
   At least 3 explicit "do not" statements that constrain the subagent's behavior. The constraints must address REAL drift risks for this task type.
   PASS examples: ["do not invent CVE IDs", "do not propose code changes outside the file under review", "do not recommend dependencies without checking the project's existing stack"]
   FAIL examples: ["don't be bad", "follow best practices", fewer than 3 constraints

4. MINIMAL-TOOLS
   3-6 tools total, each directly relevant to the task. No "just in case" tools.
   PASS examples: ["Read", "Grep", "Glob"] for code review
   FAIL examples: ["Read", "Write", "Edit", "Bash", "Glob", "Grep", "WebFetch", "TodoWrite"] (over-permissioned)

5. NO-LEAKAGE
   The proposed system prompt must NOT contain any reference to being procedurally generated. No mentions of "meta-prompt", "generated", "judge", "iteration", "synthesis", "validation", or any indication the subagent should be aware of its origin.
   PASS examples: none of the forbidden terms appear
   FAIL examples: any forbidden term appears

OUTPUT (strict JSON, no prose, no markdown fences):
{
  "verdict": "aligned" | "drift_detected" | "needs_human",
  "confidence": 0.0-1.0,
  "reasoning": "<1-3 sentences explaining which criteria pass and which fail>",
  "criteria_failed": ["OVER_QUALIFIED"|"TASK_APPROPRIATE"|"CONSTRAINT_PRIMED"|"MINIMAL_TOOLS"|"NO_LEAKAGE", ...]
}

Use "needs_human" ONLY if the task itself is sensitive/safety-adjacent and a subagent should not be spawned regardless of prompt quality (e.g. tasks about credentials, PII, destructive operations). Otherwise, use "aligned" or "drift_detected" with specific criteria_failed to drive regeneration.`

// MetaCategories is the canonical list of meta-categories used as
// starting frames for procedural composition. v2.7.0-alpha Phase 1.
// Adding a category is a MINOR change (additive); removing/renaming
// is BREAKING (changes the user-facing category_used field).
//
// Each category is a HINT to the LLM — not a pre-baked prompt. The
// LLM synthesizes a task-specific system_prompt inspired by the
// category's "over-qualification direction".
var MetaCategories = []struct {
	Key      string
	VibeCase string // "" = any
	Hint     string
}{
	{
		Key:      "c1/security-review",
		VibeCase: "C1",
		Hint:     "senior application security researcher with 12 years in OWASP Top 10 and CVE triage; over-qualify toward adversarial thinking and root-cause analysis",
	},
	{
		Key:      "c1/refactor",
		VibeCase: "C1",
		Hint:     "senior software engineer with 15 years maintaining production systems at scale; over-qualify toward idempotent changes, read-before-write, and explicit preservation of public API",
	},
	{
		Key:      "c2/docs-explain",
		VibeCase: "C2",
		Hint:     "senior technical writer with deep empathy for junior engineers; over-qualify toward concrete examples, minimal abstraction, and progressive disclosure",
	},
	{
		Key:      "c2/marketing-copy",
		VibeCase: "C2",
		Hint:     "senior conversion copywriter with 200+ A/B tests shipped; over-qualify toward clarity over cleverness, specific audience, single CTA per piece",
	},
	{
		Key:      "c3+generalist",
		VibeCase: "",
		Hint:     "focused task-execution specialist; over-qualify toward scope discipline, explicit completion criteria, and minimal drift into adjacent concerns",
	},
}

// PickCategoryKey returns the best-matching meta-category key for a
// (vibe_case, task_description) pair. Heuristic: exact vibe_case match
// first; falls back to "c3+generalist". Operators can override by
// passing a category hint via task_description keywords, but Phase 1
// keeps this simple — the LLM does the synthesis work.
func PickCategoryKey(vibeCase, taskDescription string) string {
	// Tokenize task description (lowercase, word boundaries).
	taskLower := strings.ToLower(taskDescription)

	// Keywords → category mapping (first match wins).
	keywordMap := []struct {
		keywords []string
		key      string
	}{
		{[]string{"security", "vulnerability", "cve", "owasp", "exploit", "auth bypass", "injection", "xss", "csrf"}, "c1/security-review"},
		{[]string{"refactor", "cleanup", "reorganize", "migrate", "rename", "modernize"}, "c1/refactor"},
		{[]string{"explain", "document", "tutorial", "guide", "walkthrough", "how does", "what is"}, "c2/docs-explain"},
		{[]string{"marketing", "copy", "landing", "cta", "conversion", "headline", "tagline"}, "c2/marketing-copy"},
	}
	for _, m := range keywordMap {
		for _, kw := range m.keywords {
			if strings.Contains(taskLower, kw) {
				return m.key
			}
		}
	}

	// Fallback by vibe_case.
	switch vibecase.Case(vibeCase) {
	case vibecase.CaseCode:
		return "c1/refactor"
	case vibecase.CaseText:
		return "c2/docs-explain"
	default:
		return "c3+generalist"
	}
}

// CategoryHint returns the hint string for a category key. Returns
// "" for unknown keys (caller falls back to generalist).
func CategoryHint(key string) string {
	for _, c := range MetaCategories {
		if c.Key == key {
			return c.Hint
		}
	}
	return ""
}
