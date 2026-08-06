// Package orchestration — mindset_apply_test.go
//
// v2.7.0-alpha: focused unit tests for the pure-function helpers in
// mindset_apply.go and mindset_meta_prompts.go. Integration tests
// (mock LLM + mock Store) for the full composition loop are deferred
// to a follow-up — the surface is large and the orchestrator test
// harness isn't mature yet.
package orchestration

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// TestCacheKey_Deterministic verifies that cacheKey returns the same
// hash for the same inputs and different hashes for different inputs.
// Truncates to 32 hex chars (128 bits) — collision-resistant for cache use.
func TestCacheKey_Deterministic(t *testing.T) {
	a := cacheKey("C1", "review this auth code", "sonnet")
	b := cacheKey("C1", "review this auth code", "sonnet")
	if a != b {
		t.Fatalf("cacheKey not deterministic: %q != %q", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("cacheKey length: want 32 hex chars, got %d (%q)", len(a), a)
	}
	c := cacheKey("C1", "review this auth code", "haiku") // different model_floor
	if a == c {
		t.Fatalf("cacheKey should differ on model_floor: %q == %q", a, c)
	}
	d := cacheKey("C2", "review this auth code", "sonnet") // different vibe_case
	if a == d {
		t.Fatalf("cacheKey should differ on vibe_case: %q == %q", a, d)
	}
	e := cacheKey("C1", "review different code", "sonnet") // different task
	if a == e {
		t.Fatalf("cacheKey should differ on task: %q == %q", a, e)
	}
}

// TestExtractFirstJSONObject verifies the best-effort JSON extractor
// handles: balanced objects, prose wrappers, nested objects, escapes.
func TestExtractFirstJSONObject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"pure_json", `{"a":1,"b":2}`, `{"a":1,"b":2}`},
		{"prose_wrapper", `Here is the JSON: {"a":1,"b":2} hope it helps`, `{"a":1,"b":2}`},
		{"nested", `{"a":{"b":1},"c":3}`, `{"a":{"b":1},"c":3}`},
		{"no_json", "no braces here", ""},
		{"only_open", `{"a":1`, ""},
		{"escaped_quote_in_string", `{"a":"say \"hi\"","b":2}`, `{"a":"say \"hi\"","b":2}`},
		{"multiple_objects", `first {"x":1} second {"y":2}`, `{"x":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFirstJSONObject(tt.in)
			if got != tt.want {
				t.Errorf("extractFirstJSONObject(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestComposeSystemPrompt verifies the assembled system_prompt shape.
// Empty/nil inputs should produce empty/clean output.
func TestComposeSystemPrompt(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got := composeSystemPrompt(nil); got != "" {
			t.Errorf("nil mindset: want empty, got %q", got)
		}
	})
	t.Run("full", func(t *testing.T) {
		m := &mindsetAttempt{
			Role:             "senior appsec researcher",
			Goal:             "find security flaws",
			Backstory:        "12 years in OWASP Top 10",
			Constraints:      []string{"don't invent CVEs", "don't propose code changes outside scope"},
			ToolsRecommended: []string{"Read", "Grep", "Glob"},
			ModelRecommended: "sonnet",
		}
		got := composeSystemPrompt(m)
		for _, want := range []string{
			"You are senior appsec researcher",
			"find security flaws",
			"12 years in OWASP Top 10",
			"Constraints:",
			"don't invent CVEs",
			"don't propose code changes outside scope",
			"Recommended tools: Read, Grep, Glob",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("composeSystemPrompt missing %q\n  got=%q", want, got)
			}
		}
	})
	t.Run("role_only", func(t *testing.T) {
		m := &mindsetAttempt{Role: "just a role"}
		got := composeSystemPrompt(m)
		if !strings.Contains(got, "You are just a role") {
			t.Errorf("role_only: missing role\n  got=%q", got)
		}
		// No constraints/tools sections should appear.
		if strings.Contains(got, "Constraints:") {
			t.Errorf("role_only: unexpected Constraints section\n  got=%q", got)
		}
	})
}

// TestRenderComposePrompt verifies meta-prompt substitution for first
// iteration (no history) and later iterations (history block).
func TestRenderComposePrompt(t *testing.T) {
	in := MindsetApplyInput{
		VibeCase:        "C1",
		TaskDescription: "review this code for SQL injection",
		ModelFloor:      "sonnet",
	}
	t.Run("first_iter", func(t *testing.T) {
		got := renderComposePrompt(vibecase.CaseCode, in, "c1/security-review", nil, MindsetJudgeVerdict{}, 1)
		for _, want := range []string{"C1", "review this code for SQL injection", "sonnet", "c1/security-review"} {
			if !strings.Contains(got, want) {
				t.Errorf("first_iter missing %q", want)
			}
		}
		// No history block on first iteration.
		if strings.Contains(got, "PREVIOUS ATTEMPTS") || strings.Contains(got, "{{PREVIOUS_ATTEMPTS_HISTORY}}") {
			t.Errorf("first_iter should not contain history placeholders\n  got=%q", got)
		}
	})
	t.Run("later_iter_with_history", func(t *testing.T) {
		lastAttempt := &mindsetAttempt{
			Role:             "experienced engineer", // intentionally bad — judge should fail OVER_QUALIFIED
			Goal:             "review code",
			Backstory:        "10 years",
			Constraints:      []string{"be helpful"},
			ToolsRecommended: []string{"Read"},
			ModelRecommended: "sonnet",
			Iteration:        1,
		}
		lastVerdict := MindsetJudgeVerdict{
			Verdict:        "drift_detected",
			Reasoning:      "over-qualification is generic; constraints <3",
			CriteriaFailed: []string{"OVER_QUALIFIED", "CONSTRAINT_PRIMED"},
		}
		got := renderComposePrompt(vibecase.CaseCode, in, "c1/security-review", lastAttempt, lastVerdict, 2)
		for _, want := range []string{
			"Attempt 1:", "experienced engineer", "OVER_QUALIFIED", "CONSTRAINT_PRIMED",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("later_iter missing %q\n  got=%q", want, got)
			}
		}
		// History template markers should be replaced.
		if strings.Contains(got, "{{") {
			t.Errorf("later_iter has unreplaced placeholders\n  got=%q", got)
		}
	})
}

// TestPickCategoryKey verifies keyword-based category picking + fallback
// by vibe_case.
func TestPickCategoryKey(t *testing.T) {
	tests := []struct {
		name        string
		vibeCase    string
		task        string
		wantContain string
	}{
		{"security_keyword", "C1", "review this for security flaws and CVE attribution", "security-review"},
		{"refactor_keyword", "C1", "refactor this module to use generics", "refactor"},
		{"docs_keyword", "C2", "explain how the cache works in this repo", "docs-explain"},
		{"marketing_keyword", "C2", "write marketing copy for the landing page", "marketing-copy"},
		{"c1_fallback", "C1", "what does this function do?", "refactor"},
		{"c2_fallback", "C2", "summarize this document", "docs-explain"},
		{"c3_fallback", "C3", "describe this image", "generalist"},
		{"empty_task", "C1", "", "refactor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PickCategoryKey(tt.vibeCase, tt.task)
			if !strings.Contains(got, tt.wantContain) {
				t.Errorf("PickCategoryKey(%q, %q) = %q, want to contain %q", tt.vibeCase, tt.task, got, tt.wantContain)
			}
		})
	}
}

// TestCategoryHint_AllCategoriesHaveHints verifies every category in
// MetaCategories has a non-empty hint (so the LLM has starting material).
func TestCategoryHint_AllCategoriesHaveHints(t *testing.T) {
	for _, c := range MetaCategories {
		if c.Hint == "" {
			t.Errorf("category %q has empty hint", c.Key)
		}
		if CategoryHint(c.Key) == "" {
			t.Errorf("CategoryHint(%q) returned empty", c.Key)
		}
	}
	if got := CategoryHint("nonexistent"); got != "" {
		t.Errorf("CategoryHint(nonexistent) = %q, want empty", got)
	}
}

// TestResolveEnvDefaults verifies the env-var resolvers return defaults
// for unset / malformed input, and accept valid overrides.
func TestResolveEnvDefaults(t *testing.T) {
	t.Run("max_iterations_default", func(t *testing.T) {
		t.Setenv("DARK_MINDSET_MAX_ITERATIONS", "")
		if got := resolveMaxIterations(); got != 3 {
			t.Errorf("default: want 3, got %d", got)
		}
	})
	t.Run("max_iterations_override", func(t *testing.T) {
		t.Setenv("DARK_MINDSET_MAX_ITERATIONS", "5")
		if got := resolveMaxIterations(); got != 5 {
			t.Errorf("override: want 5, got %d", got)
		}
	})
	t.Run("max_iterations_malformed", func(t *testing.T) {
		t.Setenv("DARK_MINDSET_MAX_ITERATIONS", "not-a-number")
		if got := resolveMaxIterations(); got != 3 {
			t.Errorf("malformed: want default 3, got %d", got)
		}
	})
	t.Run("max_iterations_clamped_high", func(t *testing.T) {
		t.Setenv("DARK_MINDSET_MAX_ITERATIONS", "999")
		if got := resolveMaxIterations(); got != 10 {
			t.Errorf("clamped high: want 10, got %d", got)
		}
	})
	t.Run("timeout_default_ms", func(t *testing.T) {
		t.Setenv("DARK_MINDSET_TIMEOUT_MS", "")
		if got := resolveTimeout(); got != 15000*1_000_000 {
			t.Errorf("default: want 15000ms, got %v", got)
		}
	})
	t.Run("cache_ttl_default", func(t *testing.T) {
		t.Setenv("DARK_MINDSET_CACHE_TTL", "")
		if got := resolveCacheTTL(); got != 3600*1_000_000_000 {
			t.Errorf("default: want 3600s, got %v", got)
		}
	})
}
