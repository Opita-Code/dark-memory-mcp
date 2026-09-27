// Tests for verifier.go (independent post-check that catches F7).
package judge

import (
	"context"
	"testing"
)

func TestVerifier_NoOverrideOnNonAligned(t *testing.T) {
	v := NewVerifier()
	proposed := &Verdict{Verdict: VerdictDriftDetected, Reasoning: "fail"}
	pc := newPC(t, func(p *PipelineContext) {
		p.Evidence = []Evidence{{Source: "x", Snippet: "fail and broken", Relevance: 0.9}}
	})
	out := v.Verify(context.Background(), pc, proposed)
	if out != proposed {
		t.Error("verifier modified verdict that wasn't aligned (should be no-op)")
	}
	if out.Verdict != VerdictDriftDetected {
		t.Errorf("verdict = %s; want unchanged", out.Verdict)
	}
}

func TestVerifier_OverrideAlignedByEvidenceSnippet(t *testing.T) {
	// F7 case 2: LLM said aligned but evidence contains "FAIL".
	v := NewVerifier()
	proposed := &Verdict{Verdict: VerdictAligned, Reasoning: "all looks good"}
	pc := newPC(t, func(p *PipelineContext) {
		p.Evidence = []Evidence{{Source: "test_output", Snippet: "FAIL: test_Foo expected 5, got 4", Relevance: 0.9}}
	})
	out := v.Verify(context.Background(), pc, proposed)
	if out.Verdict != VerdictDriftDetected {
		t.Errorf("verdict = %s; want drift_detected", out.Verdict)
	}
	if out.Reasoning == proposed.Reasoning {
		t.Error("Reasoning unchanged after override")
	}
	found := false
	for _, h := range out.EdgeCaseHits {
		if h.ID == "VERIFIER-OVERRIDE" && h.Catches == "F7" {
			found = true
		}
	}
	if !found {
		t.Errorf("VERIFIER-OVERRIDE hit not recorded: %+v", out.EdgeCaseHits)
	}
}

func TestVerifier_OverrideAlignedByReasoningKeyword(t *testing.T) {
	// F7 case 3: LLM said aligned but reasoning mentions "drift".
	v := NewVerifier()
	proposed := &Verdict{Verdict: VerdictAligned, Reasoning: "aligned overall, though drift detected in section X"}
	pc := newPC(t)
	out := v.Verify(context.Background(), pc, proposed)
	if out.Verdict != VerdictNeedsHuman {
		t.Errorf("verdict = %s; want needs_human", out.Verdict)
	}
}

func TestVerifier_NoFalsePositive(t *testing.T) {
	// Case from row 2052: clean aligned verdict, no contradictions.
	v := NewVerifier()
	proposed := &Verdict{Verdict: VerdictAligned, Reasoning: "tests pass cleanly with no issues"}
	pc := newPC(t, func(p *PipelineContext) {
		p.Evidence = []Evidence{{Source: "test_output", Snippet: "all 12 tests pass", Relevance: 0.9}}
	})
	out := v.Verify(context.Background(), pc, proposed)
	if out != proposed {
		t.Error("verifier false-positive: should NOT override clean aligned verdict")
	}
}

func TestVerifier_NilInputs(t *testing.T) {
	v := NewVerifier()
	if out := v.Verify(context.Background(), nil, nil); out != nil {
		t.Error("nil proposed should yield nil")
	}
	pc := newPC(t)
	if out := v.Verify(context.Background(), pc, nil); out != nil {
		t.Error("nil proposed with valid pc should yield nil")
	}
}

func TestVerifier_PreservesExtendedFields(t *testing.T) {
	// Override must preserve PersonaID, RubricVersion, Criteria, etc.
	v := NewVerifier()
	proposed := &Verdict{
		Verdict:       VerdictAligned,
		Reasoning:     "ok",
		PersonaID:     "judge-logical",
		RubricVersion: "abcd1234",
		Criteria:      []Criterion{{Name: "x", Weight: 0.5, Score: 0.5}},
		Evidence:      []Evidence{},
		TemperatureNote: TemperatureNote{Provider: "minimax", Model: "MiniMax-M3"},
	}
	pc := newPC(t, func(p *PipelineContext) {
		p.Evidence = []Evidence{{Source: "test", Snippet: "fail detected", Relevance: 0.9}}
	})
	out := v.Verify(context.Background(), pc, proposed)
	if out.PersonaID != "judge-logical" {
		t.Errorf("PersonaID lost: %q", out.PersonaID)
	}
	if out.RubricVersion != "abcd1234" {
		t.Errorf("RubricVersion lost: %q", out.RubricVersion)
	}
	if len(out.Criteria) != 1 {
		t.Errorf("Criteria lost")
	}
	if out.TemperatureNote.Provider != "minimax" {
		t.Errorf("TemperatureNote lost")
	}
}
