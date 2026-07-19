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
