// v2.16.0: T9 — Smoke Test (dual positive + negative).
//
// Per Spec 1150 v2 T9: the smoke test runs the judge pipeline twice —
// once with verbatim bytes (positive test, expect aligned) and once
// with a synthetic recap (negative test, expect needs_human). The
// CONTRAST between the two tests is the proof that the recap-prevention
// defense works end-to-end.
//
// This test does NOT call a real LLM — it synthesizes JudgeVerdict
// responses and exercises the Transport + Validator + Anchor chain.
// The LLM call would be exercised by an integration test with a mock
// LLM client (out of scope for v2.16.0).
package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeArtifactFile writes content to a temp file with a known line
// layout, padded to exceed MinArtifactBytes (5000). Returns the path
// and the slice of original lines (padding lines are appended after).
func makeArtifactFile(t *testing.T, lines []string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.md")
	// Pad to exceed MinArtifactBytes (5000) regardless of test input.
	// Each "padding line ..." is 37 bytes; 200 of them = ~7400 bytes.
	pad := strings.Repeat("padding line to satisfy the size guard\n", 200)
	content := strings.Join(lines, "\n") + "\n" + pad
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path, lines
}

// findLineIndex returns the 1-indexed line where needle appears, or
// -1 if not found. Used to cite real lines from the test artifact.
func findLineIndex(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	return -1
}

// TestSmoke_DarkTestingSkill_Verbatim_Aligned is the POSITIVE test:
// a real artifact file is loaded via Transport, a JudgeVerdict that
// correctly cites file:line+quote is validated, and the validator
// must accept it. Mirrors the dark-testing v4 SKILL.md scenario
// (576 lines, >50KB).
func TestSmoke_DarkTestingSkill_Verbatim_Aligned(t *testing.T) {
	// 1. Create a realistic artifact file (> MinArtifactBytes).
	originalLines := []string{
		"# dark-testing skill",
		"",
		"## Principles",
		"",
		"Tests are evidence. Coverage is necessary but insufficient.",
		"",
		"## Anti-patterns",
		"",
		"Assertion roulette, eager test, mystery guest, over-mocking,",
		"conditional test, sleep-driven wait, magic number, no oracle.",
	}
	path, lines := makeArtifactFile(t, originalLines)

	// 2. Load via Transport.
	tr := NewTransport()
	la, err := tr.LoadArtifact(path)
	if err != nil {
		t.Fatalf("T0 transport failed: %v", err)
	}
	if la.Sha256Hex == "" {
		t.Error("expected non-empty SHA256")
	}

	// 3. Find real line indices for the quotes we want to cite.
	principlesLine := findLineIndex(lines, "Tests are evidence")
	antiPatternsLine := findLineIndex(lines, "Assertion roulette")
	if principlesLine < 1 || antiPatternsLine < 1 {
		t.Fatalf("test artifact missing expected lines: principles=%d, antiPatterns=%d",
			principlesLine, antiPatternsLine)
	}

	// Build the JudgeVerdict JSON dynamically with the real line
	// numbers. Note: this JudgeVerdict cites REAL lines from the
	// test artifact, so T2 Validate must accept it.
	raw := `{
		"verdict": "aligned",
		"confidence": 0.92,
		"evidence": [
			{"file": "artifact.md", "line": ` + itoa(principlesLine) + `, "quote": "Tests are evidence. Coverage is necessary but insufficient.", "concern": "matches P1 tests-are-evidence principle"},
			{"file": "artifact.md", "line": ` + itoa(antiPatternsLine) + `, "quote": "Assertion roulette, eager test, mystery guest, over-mocking,", "concern": "lists 8 anti-patterns"}
		],
		"reasoning": "spec satisfied — language-agnostic, principled, anti-pattern-centric",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`

	// 4. Validate via T2.
	v := ValidateOrNeedsHuman(raw, "judge-logical", "drift_judge", "opencode", "anthropic/claude-sonnet-4.5", la)
	if v.Verdict != VerdictAligned {
		t.Errorf("expected aligned (verbatim + valid evidence), got %s (reasoning=%q)",
			v.Verdict, v.Reasoning)
	}
	if len(v.Evidence) < 1 {
		t.Errorf("expected ≥1 evidence items, got %d", len(v.Evidence))
	}

	// 5. Confirm anchor would have been injected.
	injected := InjectAnchor("test persona")
	if !strings.Contains(injected, "needs_human") {
		t.Error("anchor injection broken — anti-hallucination anchor missing")
	}
}

// TestSmoke_DarkTestingSkill_Recap_NeedsHuman is the NEGATIVE test:
// a synthetic recap (< MinArtifactBytes) is rejected by T0 at load
// time. If T0 doesn't catch it (e.g., if the threshold is misconfigured),
// T2 catches it: the recap has no real file:line, so any evidence
// would fail to match the artifact.
//
// This proves the recap-prevention defense is operational end-to-end.
func TestSmoke_DarkTestingSkill_Recap_NeedsHuman(t *testing.T) {
	// 1. Create the REAL artifact first (> MinArtifactBytes).
	realPath, realLines := makeArtifactFile(t, []string{
		"# dark-testing skill",
		"",
		"## Principles",
		"",
		"Tests are evidence. Coverage is necessary but insufficient.",
	})

	// 2. Create a SYNTHETIC RECAP that is STRICTLY LESS than
	// MinArtifactBytes (5000). Each "x" is 1 byte; we use 1000 bytes.
	recapDir := t.TempDir()
	recapPath := filepath.Join(recapDir, "recap.md")
	recap := strings.Repeat("x", 1000) // 1000 bytes — clearly < 5000
	if err := os.WriteFile(recapPath, []byte(recap), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 3. T0 transport MUST reject the recap.
	tr := NewTransport()
	_, err := tr.LoadArtifact(recapPath)
	if err == nil {
		t.Fatal("expected T0 transport to reject recap, but it loaded successfully")
	}
	if !errIs(err, ErrArtifactTooSmall) {
		t.Errorf("expected ErrArtifactTooSmall, got: %v", err)
	}

	// 4. Even if T0 didn't catch it (e.g., recap accidentally exceeded
	// the threshold), T2 would catch a hallucinated quote. Synthesize
	// a verdict that cites a quote NOT in the real artifact and try
	// to validate against the real artifact's reader — must fail.
	realLA, err := tr.LoadArtifact(realPath)
	if err != nil {
		t.Fatalf("real artifact load failed: %v", err)
	}
	principlesLine := findLineIndex(realLines, "Tests are evidence")
	if principlesLine < 1 {
		t.Fatal("could not find real principles line")
	}
	badRaw := `{
		"verdict": "aligned",
		"confidence": 0.95,
		"evidence": [{"file": "artifact.md", "line": ` + itoa(principlesLine) + `, "quote": "This is a hallucinated quote that does NOT exist in the real artifact", "concern": "ok"}],
		"reasoning": "ok",
		"persona_id": "judge-logical",
		"eval_type": "drift_judge",
		"harness_id": "opencode",
		"model_used": "anthropic/claude-sonnet-4.5",
		"timestamp": "2026-08-14T12:00:00Z"
	}`
	v := ValidateOrNeedsHuman(badRaw, "judge-logical", "drift_judge", "opencode", "anthropic/claude-sonnet-4.5", realLA)
	if v.Verdict != VerdictNeedsHuman {
		t.Errorf("expected needs_human (hallucinated quote rejected), got %s", v.Verdict)
	}
	if !strings.Contains(v.Reasoning, "insufficient information") {
		t.Errorf("expected anti-hallucination reasoning, got: %q", v.Reasoning)
	}
}

// errIs is a tiny helper because errors.Is doesn't unwrap custom
// errors directly without Unwrap() — our ErrArtifactTooSmall is a
// sentinel so direct equality is fine.
func errIs(err, target error) bool {
	return err == target || strings.Contains(err.Error(), target.Error())
}

// itoa is a tiny helper to avoid strconv import just for these tests.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
