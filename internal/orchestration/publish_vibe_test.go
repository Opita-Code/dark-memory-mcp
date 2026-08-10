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
// for JSON without verdict or aligned fields. Returns drift_detected
// (the conservative default).
func TestParseDriftVerdict_Malformed(t *testing.T) {
	json := `{"garbage":"yes","nothing_useful":true}`
	if got := parseDriftVerdict(json, 0.9); got != "drift_detected" {
		t.Errorf("malformed: got %q, want %q (lenient default)", got, "drift_detected")
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
// don't recognize falls through to drift_detected (the conservative
// fallback).
func TestParseDriftVerdict_UnknownStringValue(t *testing.T) {
	json := `{"verdict":"maybe_aligned","confidence":0.9}`
	if got := parseDriftVerdict(json, 0.9); got != "drift_detected" {
		t.Errorf("unknown verdict: got %q, want %q", got, "drift_detected")
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

// TestParseVerdict_LowConfidence verifies the confidence floor still
// overrides every eval_type-specific mapping.
func TestParseVerdict_LowConfidence(t *testing.T) {
	json := `{"grounded":true,"confidence":0.4}`
	if got := parseVerdict("grounding_check", json, 0.4); got != "needs_human" {
		t.Errorf("low conf grounding_check: got %q, want %q", got, "needs_human")
	}
}
