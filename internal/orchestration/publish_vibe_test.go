// Tests for parseDriftVerdict — the function that maps the LLM Judge's
// verdict JSON to the canonical drift verdict string returned to the
// VibePublish caller.
//
// The INFRA-001 bug (2026-07-19): pre-fix parseDriftVerdict only recognized
// the legacy {"aligned":bool} shape and silently returned "drift_detected"
// for the modern {"verdict":"aligned"|"drift_detected"|"needs_human"}
// shape produced by dark_memory_judge. These tests pin down both shapes
// to prevent regression.
package orchestration

import (
	"strings"
	"testing"
)

// TestParseDriftVerdict_Modern_Aligned verifies the modern judge output
// {"verdict":"aligned", "confidence":0.9, "reasoning":"..."} parses to
// "aligned". This is the canonical post-v1.4.0 shape.
func TestParseDriftVerdict_Modern_Aligned(t *testing.T) {
	json := `{"verdict":"aligned","confidence":0.92,"reasoning":"ok"}`
	if got := parseDriftVerdict(json, 0.92); got != "aligned" {
		t.Errorf("modern aligned: got %q, want %q", got, "aligned")
	}
}

// TestParseDriftVerdict_Modern_DriftDetected verifies
// {"verdict":"drift_detected", ...} parses correctly.
func TestParseDriftVerdict_Modern_DriftDetected(t *testing.T) {
	json := `{"verdict":"drift_detected","confidence":0.85,"reasoning":"missing field"}`
	if got := parseDriftVerdict(json, 0.85); got != "drift_detected" {
		t.Errorf("modern drift_detected: got %q, want %q", got, "drift_detected")
	}
}

// TestParseDriftVerdict_Modern_NeedsHuman verifies
// {"verdict":"needs_human", ...} parses correctly.
func TestParseDriftVerdict_Modern_NeedsHuman(t *testing.T) {
	json := `{"verdict":"needs_human","confidence":0.7,"reasoning":"operator review required"}`
	if got := parseDriftVerdict(json, 0.7); got != "needs_human" {
		t.Errorf("modern needs_human: got %q, want %q", got, "needs_human")
	}
}

// TestParseDriftVerdict_Legacy_Aligned verifies the legacy
// {"aligned":true, ...} shape parses to "aligned".
func TestParseDriftVerdict_Legacy_Aligned(t *testing.T) {
	json := `{"aligned":true,"confidence":0.92,"issues":[]}`
	if got := parseDriftVerdict(json, 0.92); got != "aligned" {
		t.Errorf("legacy aligned:true: got %q, want %q", got, "aligned")
	}
}

// TestParseDriftVerdict_Legacy_Drift verifies the legacy
// {"aligned":false, ...} shape parses to "drift_detected".
func TestParseDriftVerdict_Legacy_Drift(t *testing.T) {
	json := `{"aligned":false,"drift_items":["missing_x"],"confidence":0.85}`
	if got := parseDriftVerdict(json, 0.85); got != "drift_detected" {
		t.Errorf("legacy aligned:false: got %q, want %q", got, "drift_detected")
	}
}

// TestParseDriftVerdict_LowConfidence overrides any verdict when
// confidence < 0.5.
func TestParseDriftVerdict_LowConfidence(t *testing.T) {
	json := `{"verdict":"aligned","confidence":0.4,"reasoning":"weak"}`
	if got := parseDriftVerdict(json, 0.4); got != "needs_human" {
		t.Errorf("low conf: got %q, want %q (must override any verdict)", got, "needs_human")
	}
}

// TestParseDriftVerdict_Malformed verifies the lenient fallback works
// for JSON without verdict or aligned fields. v2.21.0 (spec 1200 P0):
// returns needs_human — an unparseable verdict is parse/infra failure
// (operator reviews), NOT semantic drift. The pre-fix default was
// drift_detected, which turned every YAML/markdown verdict into a
// spurious drift (root cause of the "judge degraded" era).
func TestParseDriftVerdict_Malformed(t *testing.T) {
	json := `{"garbage":"yes","nothing_useful":true}`
	if got := parseDriftVerdict(json, 0.9); got != "needs_human" {
		t.Errorf("malformed: got %q, want %q (fail-safe default)", got, "needs_human")
	}
}

// TestParseDriftVerdict_WhitespaceLenient verifies the substring
// fallback tolerates whitespace and case variation.
func TestParseDriftVerdict_WhitespaceLenient(t *testing.T) {
	// Modern shape but with extra whitespace (some judges pretty-print).
	json := `{
		"Verdict" : "aligned",
		"Confidence" : 0.9
	}`
	if got := parseDriftVerdict(json, 0.9); got != "aligned" {
		t.Errorf("whitespace: got %q, want %q", got, "aligned")
	}
}

// TestParseDriftVerdict_NormalizerDebug is a debugging aid: it exercises
// the substring normalizer and prints the compact form for inspection.
// Disabled by default; remove the "Skip" prefix to enable.
func TestParseDriftVerdict_NormalizerDebug_Skip(t *testing.T) {
	t.Skip("debug-only")
	json := `{
		"Verdict" : "aligned",
		"Confidence" : 0.9
	}`
	normalized := strings.ToLower(json)
	t.Logf("normalized: %q", normalized)
}

// TestParseDriftVerdict_UnknownStringValue — a verdict string we
// don't recognize falls through to needs_human (the fail-safe default
// per v2.21.0 spec 1200 P0; unknown verdict = infra/parse failure,
// not drift).
func TestParseDriftVerdict_UnknownStringValue(t *testing.T) {
	json := `{"verdict":"maybe_aligned","confidence":0.9}`
	if got := parseDriftVerdict(json, 0.9); got != "needs_human" {
		t.Errorf("unknown verdict: got %q, want %q", got, "needs_human")
	}
}

// --- v2.13.0 eval_type-aware tests (OPITA-006) ---
//
// parseVerdict must translate each judge's type-specific JSON shape into
// the canonical three-state verdict. These pin down the fix: previously
// parseDriftVerdict was eval_type-blind and every non-drift_judge verdict
// fell through to "drift_detected" (e.g. consensus(grounding_check) said
// drift_detected even when the LLM replied grounded:true).

// TestParseVerdict_GroundingCheck_True verifies grounding_check
// {"grounded":true} maps to "aligned".
func TestParseVerdict_GroundingCheck_True(t *testing.T) {
	json := `{"grounded":true,"confidence":0.95,"evidence_quote":"quote"}`
	if got := parseVerdict("grounding_check", json, 0.95); got != "aligned" {
		t.Errorf("grounding_check grounded:true: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_GroundingCheck_False verifies grounding_check
// {"grounded":false} maps to "drift_detected".
func TestParseVerdict_GroundingCheck_False(t *testing.T) {
	json := `{"grounded":false,"confidence":0.9,"evidence_quote":"quote"}`
	if got := parseVerdict("grounding_check", json, 0.9); got != "drift_detected" {
		t.Errorf("grounding_check grounded:false: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_PiiDetect_False verifies pii_detect {"pii_found":false}
// maps to "aligned" (no PII = content passes).
func TestParseVerdict_PiiDetect_False(t *testing.T) {
	json := `{"pii_found":false,"items":[]}`
	if got := parseVerdict("pii_detect", json, 0.9); got != "aligned" {
		t.Errorf("pii_detect pii_found:false: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_PiiDetect_True verifies pii_detect {"pii_found":true}
// maps to "drift_detected" (PII present = content fails the check).
func TestParseVerdict_PiiDetect_True(t *testing.T) {
	json := `{"pii_found":true,"items":[{"kind":"email","value":"a@b.c"}]}`
	if got := parseVerdict("pii_detect", json, 0.9); got != "drift_detected" {
		t.Errorf("pii_detect pii_found:true: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_PromptInjectionScan_False verifies
// prompt_injection_scan {"injection_found":false} maps to "aligned".
func TestParseVerdict_PromptInjectionScan_False(t *testing.T) {
	json := `{"injection_found":false,"evidence":""}`
	if got := parseVerdict("prompt_injection_scan", json, 0.9); got != "aligned" {
		t.Errorf("prompt_injection_scan injection_found:false: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_PromptInjectionScan_True verifies
// prompt_injection_scan {"injection_found":true} maps to "drift_detected".
func TestParseVerdict_PromptInjectionScan_True(t *testing.T) {
	json := `{"injection_found":true,"evidence":"ignore prior instructions"}`
	if got := parseVerdict("prompt_injection_scan", json, 0.9); got != "drift_detected" {
		t.Errorf("prompt_injection_scan injection_found:true: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_BrandMatch_Match verifies brand_match {"verdict":"match"}
// maps to "aligned".
func TestParseVerdict_BrandMatch_Match(t *testing.T) {
	json := `{"verdict":"match","score":0.9,"issues":[]}`
	if got := parseVerdict("brand_match", json, 0.9); got != "aligned" {
		t.Errorf("brand_match match: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_BrandMatch_DriftDetected verifies brand_match
// {"verdict":"drift_detected"} maps to "drift_detected".
func TestParseVerdict_BrandMatch_DriftDetected(t *testing.T) {
	json := `{"verdict":"drift_detected","score":0.4,"issues":["tone"]}`
	if got := parseVerdict("brand_match", json, 0.9); got != "drift_detected" {
		t.Errorf("brand_match drift_detected: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_ComplianceCheck_Compliant verifies compliance_check
// {"verdict":"compliant"} maps to "aligned".
func TestParseVerdict_ComplianceCheck_Compliant(t *testing.T) {
	json := `{"verdict":"compliant","issues":[],"required_disclosures":[]}`
	if got := parseVerdict("compliance_check", json, 0.9); got != "aligned" {
		t.Errorf("compliance_check compliant: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_ComplianceCheck_NonCompliant verifies compliance_check
// {"verdict":"non_compliant"} maps to "drift_detected".
func TestParseVerdict_ComplianceCheck_NonCompliant(t *testing.T) {
	json := `{"verdict":"non_compliant","issues":["missing notice"],"required_disclosures":[]}`
	if got := parseVerdict("compliance_check", json, 0.9); got != "drift_detected" {
		t.Errorf("compliance_check non_compliant: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_DriftJudge_Aligned verifies the wrapper still maps
// drift_judge {"verdict":"aligned"} to "aligned" (no regression).
func TestParseVerdict_DriftJudge_Aligned(t *testing.T) {
	json := `{"verdict":"aligned","confidence":0.9,"reasoning":"ok"}`
	if got := parseVerdict("drift_judge", json, 0.9); got != "aligned" {
		t.Errorf("drift_judge aligned: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_YamlVerdicts (v2.21.0, spec 1200 P0 regression):
// MiniMax-M3 with thinking adaptive answers in YAML (or markdown with
// fenced yaml), not JSON. A valid `verdict: needs_human` YAML must map
// to needs_human — the pre-fix parser fell through to the fail-open
// drift_detected default, which turned every YAML verdict into a
// spurious drift and is the root cause of the "judge degraded" era.
func TestParseVerdict_YamlVerdicts(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			"yaml_needs_human", // exact shape from eval 980
			"```yaml\nverdict: needs_human\nreasoning: insufficient information to ground verdict\nevidence:\n  - file: <report markdown provided by operator>\n    line: 1\n```",
			"needs_human",
		},
		{
			"yaml_aligned",
			"```yaml\nverdict: aligned\nconfidence: 0.9\nreasoning: implementation matches spec\n```",
			"aligned",
		},
		{
			"yaml_drift_detected",
			"```yaml\nverdict: drift_detected\nreasoning: artifact omits spec section\n```",
			"drift_detected",
		},
		{
			"markdown_needs_human", // exact shape from eval 981
			"# Logical Judge — Audit Verdict\n\n**verdict:** `needs_human`\n**reasoning:** insufficient information to ground verdict — no artifact text, no file path\n",
			"needs_human",
		},
		{
			"markdown_aligned",
			"# Verdict\n\n**verdict:** `aligned`\n**confidence:** 0.92\n",
			"aligned",
		},
		{
			"yaml_quoted_verdict",
			"```yaml\nverdict: \"drift_detected\"\nreasoning: no\n```",
			"drift_detected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVerdict("drift_judge", tc.json, 0.7); got != tc.want {
				t.Errorf("parseVerdict(%q): got %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestParseVerdict_UnparseableFailsSafe (v2.21.0, spec 1200 P0): an
// unparseable/empty verdict must surface as needs_human (operator
// reviews), NOT drift_detected. Parse/infra failure ≠ semantic drift.
func TestParseVerdict_UnparseableFailsSafe(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"empty", ""},
		{"garbage", "not a verdict at all"},
		{"empty_json_object", "{}"},
		{"thinking_only", `{"type":"thinking","thinking":"let me reason about this..."}`},
		{"partial", `{"reasoning":"no verdict field"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseVerdict("drift_judge", tc.json, 0.7); got != "needs_human" {
				t.Errorf("parseVerdict(%q): got %q, want needs_human", tc.name, got)
			}
		})
	}
}

// TestParseVerdict_LowConfidence verifies the confidence floor still
// overrides every eval_type-specific mapping.
func TestParseVerdict_LowConfidence(t *testing.T) {
	json := `{"grounded":true,"confidence":0.4}`
	if got := parseVerdict("grounding_check", json, 0.4); got != "needs_human" {
		t.Errorf("low conf grounding_check: got %q, want %q", got, "needs_human")
	}
}

// --- v2.15.0 (Fase 2) testing eval types (dark-testing skill v2.0.0) ---
//
// spec_test_alignment (M6), mutation_score_check (M1),
// security_coverage (M7), resilience_check (M8), test_quality_review,
// oracle_quality — each judge emits a distinct JSON shape; parseVerdict
// must map it to the canonical three-state verdict.

// TestParseVerdict_SpecTestAlignment_High verifies spec_test_alignment
// {"alignment":0.9} (>= 0.7) maps to "aligned".
func TestParseVerdict_SpecTestAlignment_High(t *testing.T) {
	json := `{"alignment":0.9,"spec_claims_verified":9,"spec_claims_total":10,"missing_claims":["invariant M4"],"reasoning":"one invariant lacks a test"}`
	if got := parseVerdict("spec_test_alignment", json, 0.9); got != "aligned" {
		t.Errorf("spec_test_alignment alignment:0.9: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_SpecTestAlignment_Low verifies spec_test_alignment
// {"alignment":0.5} (< 0.7) maps to "drift_detected".
func TestParseVerdict_SpecTestAlignment_Low(t *testing.T) {
	json := `{"alignment":0.5,"spec_claims_verified":5,"spec_claims_total":10,"missing_claims":["a","b","c","d","e"],"reasoning":"half the spec is untested"}`
	if got := parseVerdict("spec_test_alignment", json, 0.9); got != "drift_detected" {
		t.Errorf("spec_test_alignment alignment:0.5: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_MutationScoreCheck_Pass verifies mutation_score_check
// {"pass":true} maps to "aligned" (score >= threshold).
func TestParseVerdict_MutationScoreCheck_Pass(t *testing.T) {
	json := `{"score":0.82,"threshold":0.7,"pass":true,"mutants_killed":82,"mutants_total":100,"reasoning":"above gate"}`
	if got := parseVerdict("mutation_score_check", json, 0.9); got != "aligned" {
		t.Errorf("mutation_score_check pass:true: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_MutationScoreCheck_Fail verifies mutation_score_check
// {"pass":false} maps to "drift_detected".
func TestParseVerdict_MutationScoreCheck_Fail(t *testing.T) {
	json := `{"score":0.55,"threshold":0.7,"pass":false,"mutants_killed":55,"mutants_total":100,"reasoning":"below gate"}`
	if got := parseVerdict("mutation_score_check", json, 0.9); got != "drift_detected" {
		t.Errorf("mutation_score_check pass:false: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_SecurityCoverage_High verifies security_coverage
// {"coverage":0.9} (>= 0.8) maps to "aligned".
func TestParseVerdict_SecurityCoverage_High(t *testing.T) {
	json := `{"coverage":0.9,"vectors_covered":["LLM01","LLM02","LLM06","LLM08","LLM10"],"vectors_missing":[],"reasoning":"all applicable vectors tested"}`
	if got := parseVerdict("security_coverage", json, 0.9); got != "aligned" {
		t.Errorf("security_coverage coverage:0.9: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_SecurityCoverage_Low verifies security_coverage
// {"coverage":0.6} (< 0.8) maps to "drift_detected".
func TestParseVerdict_SecurityCoverage_Low(t *testing.T) {
	json := `{"coverage":0.6,"vectors_covered":["LLM01"],"vectors_missing":["LLM03","LLM06","LLM08"],"reasoning":"most vectors untested"}`
	if got := parseVerdict("security_coverage", json, 0.9); got != "drift_detected" {
		t.Errorf("security_coverage coverage:0.6: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_ResilienceCheck_Pass verifies resilience_check
// {"passed":true} maps to "aligned" (>= 0.90 resilience target).
func TestParseVerdict_ResilienceCheck_Pass(t *testing.T) {
	json := `{"passed":true,"experiments_passed":9,"experiments_run":10,"abort_guards_triggered":[],"reasoning":"safe degradation"}`
	if got := parseVerdict("resilience_check", json, 0.9); got != "aligned" {
		t.Errorf("resilience_check passed:true: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_ResilienceCheck_Fail verifies resilience_check
// {"passed":false} maps to "drift_detected" (abort guard fired).
func TestParseVerdict_ResilienceCheck_Fail(t *testing.T) {
	json := `{"passed":false,"experiments_passed":6,"experiments_run":10,"abort_guards_triggered":["dispatch_latency>2s"],"reasoning":"retry storm"}`
	if got := parseVerdict("resilience_check", json, 0.9); got != "drift_detected" {
		t.Errorf("resilience_check passed:false: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_TestQualityReview_Aligned verifies test_quality_review
// {"verdict":"aligned"} maps verbatim (reviewer-style judge).
func TestParseVerdict_TestQualityReview_Aligned(t *testing.T) {
	json := `{"verdict":"aligned","confidence":0.85,"issues":[],"strengths":["table-driven"],"reasoning":"clean"}`
	if got := parseVerdict("test_quality_review", json, 0.85); got != "aligned" {
		t.Errorf("test_quality_review aligned: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_TestQualityReview_Drift verifies test_quality_review
// {"verdict":"drift_detected"} maps verbatim.
func TestParseVerdict_TestQualityReview_Drift(t *testing.T) {
	json := `{"verdict":"drift_detected","confidence":0.8,"issues":["no error-path test"],"reasoning":"missing coverage"}`
	if got := parseVerdict("test_quality_review", json, 0.8); got != "drift_detected" {
		t.Errorf("test_quality_review drift_detected: got %q, want %q", got, "drift_detected")
	}
}

// TestParseVerdict_OracleQuality_Aligned verifies oracle_quality
// {"verdict":"aligned"} maps verbatim (oracle-adequacy reviewer).
func TestParseVerdict_OracleQuality_Aligned(t *testing.T) {
	json := `{"verdict":"aligned","confidence":0.9,"issues":[],"recommendations":["keep pass@k"],"reasoning":"oracle adequate"}`
	if got := parseVerdict("oracle_quality", json, 0.9); got != "aligned" {
		t.Errorf("oracle_quality aligned: got %q, want %q", got, "aligned")
	}
}

// TestParseVerdict_OracleQuality_Drift verifies oracle_quality
// {"verdict":"drift_detected"} maps verbatim.
func TestParseVerdict_OracleQuality_Drift(t *testing.T) {
	json := `{"verdict":"drift_detected","confidence":0.7,"issues":["single-sample assert on LLM output"],"recommendations":["pass@k>=10"],"reasoning":"non-deterministic oracle"}`
	if got := parseVerdict("oracle_quality", json, 0.7); got != "drift_detected" {
		t.Errorf("oracle_quality drift_detected: got %q, want %q", got, "drift_detected")
	}
}

// TestDefaultSystemForEval_TestingTypes verifies the v2.15.0 eval types
// get a non-empty, JSON-mode system prompt (the LLM must stay in
// JSON-output mode for downstream parsing).
func TestDefaultSystemForEval_TestingTypes(t *testing.T) {
	types := []string{
		"spec_test_alignment",
		"mutation_score_check",
		"test_quality_review",
		"security_coverage",
		"resilience_check",
		"oracle_quality",
	}
	for _, et := range types {
		s := defaultSystemForEval(et)
		if s == "" {
			t.Errorf("defaultSystemForEval(%q) returned empty", et)
		}
		if !strings.Contains(s, "Schema:") {
			t.Errorf("defaultSystemForEval(%q) missing JSON Schema instruction", et)
		}
	}
}

// TestJudgeTimeoutMultipliers_TestingTypes verifies the v2.15.0 eval
// types have a timeout multiplier (default 1.0 when unset — but the
// Fase 2 contract requires explicit entries).
func TestJudgeTimeoutMultipliers_TestingTypes(t *testing.T) {
	types := []string{
		"spec_test_alignment",
		"mutation_score_check",
		"test_quality_review",
		"security_coverage",
		"resilience_check",
		"oracle_quality",
	}
	for _, et := range types {
		if m := judgeTimeoutMultipliers[et]; m <= 0 {
			t.Errorf("judgeTimeoutMultipliers[%q] missing (got %v)", et, m)
		}
	}
}
