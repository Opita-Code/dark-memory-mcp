package vibeflow

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// L11.2: grade the confidence the PRODUCTION path emits.
// ---------------------------------------------------------------------------

// The load-bearing case. A skipped drift check yields confidence 0.
// Before this wiring the operator saw `{"verdict":"skipped",
// "confidence":0}` with nothing else — which reads exactly like "the
// judge measured zero confidence". It did not measure anything.
func TestL11_2_SkippedIsUnverified(t *testing.T) {
	o := JudgeOutcome{Verdict: VerdictSkipped, Confidence: 0}
	if got := GradeJudgeConfidence(o); got != GradeUnverified {
		t.Fatalf("skipped must be unverified, got %v", got)
	}
	cav := JudgeConfidenceCaveat(o)
	if !strings.Contains(cav, "no judge ran") {
		t.Errorf("caveat must state no judge ran, got %q", cav)
	}
	if !strings.Contains(cav, "not a measured zero") {
		t.Errorf("caveat must distinguish missing observation from a zero measurement, got %q", cav)
	}
}

// Async publishes return pending with confidence 0. Nothing has been
// observed yet, so nothing may be claimed.
func TestL11_2_PendingIsUnverified(t *testing.T) {
	for _, v := range []string{VerdictPendingState, ""} {
		o := JudgeOutcome{Verdict: v, Confidence: 0}
		if got := GradeJudgeConfidence(o); got != GradeUnverified {
			t.Errorf("verdict %q must be unverified, got %v", v, got)
		}
	}
	if cav := JudgeConfidenceCaveat(JudgeOutcome{Verdict: VerdictPendingState}); !strings.Contains(cav, "still running") {
		t.Errorf("pending caveat must say the check is still running, got %q", cav)
	}
}

// THE REAL FINDING: publish_vibe returns needs_human with confidence 0
// from three infra-failure paths where the judge never ran (drift_judge
// unavailable, materialize failed, LLM infra failure). Those are not
// drift findings. Without this rule the operator reads a 0 and may
// conclude the artifact was measured and found wanting.
func TestL11_2_InfraFailureNeedsHumanIsUnverified(t *testing.T) {
	// Mirrors runJudgePipeline's infra-failure return: verdict set,
	// confidence 0, no provider recorded.
	o := JudgeOutcome{Verdict: VerdictNeedsHuman, Confidence: 0}
	if got := GradeJudgeConfidence(o); got != GradeUnverified {
		t.Fatalf("infra-failure needs_human must be unverified, got %v", got)
	}
	if got := GradeJudgeConfidence(o); got.Trusted() {
		t.Fatal("an unobserved verdict must never be Trusted()")
	}
	cav := JudgeConfidenceCaveat(o)
	if !strings.Contains(cav, "no judge provider recorded") {
		t.Errorf("must say no provider produced the number, got %q", cav)
	}
	// Crucially it must NOT claim the judge ran.
	if strings.Contains(cav, "single-judge measurement") {
		t.Errorf("infra failure must not be described as a measurement: %q", cav)
	}
}

// A real NLI confidence IS an observation, so it is honestly measured.
// But it is measured as a measurement: one authority is correlated, not
// independently reviewed. This is L12.1's rule reaching production.
func TestL11_2_RealJudgeConfidenceIsMeasuredButNotIndependent(t *testing.T) {
	o := JudgeOutcome{
		Verdict:    VerdictAligned,
		Confidence: 0.92,
		ProviderID: "chat-minimax-cn",
		ModelRev:   "MiniMax-M3",
	}
	if got := GradeJudgeConfidence(o); got != GradeMeasured {
		t.Fatalf("real confidence must be measured, got %v", got)
	}
	cav := JudgeConfidenceCaveat(o)
	if !strings.Contains(cav, "chat-minimax-cn/MiniMax-M3") {
		t.Errorf("caveat must name the authority, got %q", cav)
	}
	if !strings.Contains(cav, "not independent review") {
		t.Errorf("measured must still be qualified as correlated, got %q", cav)
	}
}

// The MiniMax false-positive case (rows 482/1985/2013/2016/2017,
// overruled twice at evals 2016/2017). An operator seeing
// needs_human + 0.9 would reasonably conclude "90% sure it is broken".
// The confidence describes the escalation, not evidence against the
// artifact.
func TestL11_2_NeedsHumanConfidenceDescribesEscalationNotGuilt(t *testing.T) {
	o := JudgeOutcome{
		Verdict:    VerdictNeedsHuman,
		Confidence: 0.9,
		ProviderID: "chat-minimax-cn",
		ModelRev:   "MiniMax-M3",
	}
	cav := JudgeConfidenceCaveat(o)
	if !strings.Contains(cav, "ESCALATION") {
		t.Errorf("caveat must say the confidence describes the escalation, got %q", cav)
	}
	if !strings.Contains(cav, "not the same as finding drift") {
		t.Errorf("caveat must distinguish declining-to-certify from finding drift, got %q", cav)
	}
	if !strings.Contains(cav, "false-positive") {
		t.Errorf("caveat must disclose the documented false-positive pattern, got %q", cav)
	}
}

// Never claim estimated or modeled: this number is either an
// observation or it is absent. Nothing is projected.
func TestL11_2_NeverEstimatedOrModeled(t *testing.T) {
	verdicts := []string{
		VerdictAligned, VerdictDriftDetect, VerdictNeedsHuman,
		VerdictSkipped, VerdictPendingState, "",
	}
	for _, v := range verdicts {
		for _, c := range []float32{0, 0.01, 0.5, 0.999, 1} {
			o := JudgeOutcome{Verdict: v, Confidence: c, ProviderID: "p", ModelRev: "m"}
			g := GradeJudgeConfidence(o)
			if g == GradeEstimated || g == GradeModeled {
				t.Errorf("verdict %q conf %v graded %v; nothing here is projected", v, c, g)
			}
		}
	}
}

// The ladder never goes up: once unverified, always unverified.
func TestL11_2_GradeNeverStrengthensWithConfidence(t *testing.T) {
	prev := GradeJudgeConfidence(JudgeOutcome{Verdict: VerdictSkipped})
	for _, c := range []float32{0.0, 0.1, 0.5, 0.9, 1.0} {
		g := GradeJudgeConfidence(JudgeOutcome{Verdict: VerdictSkipped, Confidence: c})
		if g < prev {
			t.Fatalf("grade strengthened from %v to %v at conf %v", prev, g, c)
		}
		prev = g
	}
	if prev != GradeUnverified {
		t.Fatalf("skipped must stay unverified regardless of confidence, got %v", prev)
	}
}

// Claim rendering reuses the L11.1 type and always carries a caveat.
func TestL11_2_ClaimAlwaysCarriesCaveatAndGrade(t *testing.T) {
	cases := []JudgeOutcome{
		{Verdict: VerdictSkipped},
		{Verdict: VerdictPendingState},
		{Verdict: VerdictNeedsHuman},
		{Verdict: VerdictAligned, Confidence: 0.8, ProviderID: "p", ModelRev: "m"},
		{Verdict: VerdictDriftDetect, Confidence: 0.8, ProviderID: "p", ModelRev: "m"},
	}
	for _, o := range cases {
		c := JudgeConfidenceClaim(o)
		if c.Caveat == "" {
			t.Errorf("verdict %q: caveat must never be empty", o.Verdict)
		}
		if c.Grade != GradeJudgeConfidence(o) {
			t.Errorf("verdict %q: claim grade %v disagrees with GradeJudgeConfidence %v",
				o.Verdict, c.Grade, GradeJudgeConfidence(o))
		}
		if !strings.Contains(c.Value, o.Verdict) {
			t.Errorf("verdict %q: value must show which verdict was measured, got %q", o.Verdict, c.Value)
		}
		if c.Source == "" {
			t.Errorf("verdict %q: source must be named", o.Verdict)
		}
	}
}

// Reuses the L11.1 vocabulary — no new epistemic words invented.
func TestL11_2_GradeStringsAreExistingL11Vocabulary(t *testing.T) {
	want := map[EvidenceGrade]string{
		GradeUnverified: "unverified",
		GradeEstimated:  "estimated",
		GradeModeled:    "modeled",
		GradeMeasured:   "measured",
	}
	for g, s := range want {
		if g.String() != s {
			t.Errorf("grade %d renders %q, want %q — L11.2 must not add vocabulary", g, g.String(), s)
		}
	}
}
