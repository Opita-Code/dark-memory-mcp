package vibeflow

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// L12.1 fixtures — the two authorities from the real row 1985 incident.
// ---------------------------------------------------------------------------

// originalJudge is the judge that produced the disputed verdict in the
// live incident: provider chat-minimax-cn, model MiniMax-M3. Rows
// 1985 / 2013 record this authority returning false-positive drift
// verdicts that the operator had to overrule.
func originalJudge() JudgeSource {
	return JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", EvalType: "drift_judge"}
}

// sameJudge is the exact re-run G19 performs today. Distinct EvalType
// (grounding_check vs drift_judge) so the test proves EvalType is NOT
// part of the independence computation — asking the same mind a
// different question is still the same mind.
func sameJudge() JudgeSource {
	return JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", EvalType: "grounding_check"}
}

// independentJudge is a different model served by a different vendor.
func independentJudge() JudgeSource {
	return JudgeSource{ProviderID: "judge-deepseek", ModelRev: "deepseek-v4", EvalType: "drift_judge"}
}

// sameModelOtherVendor shares the model but not the provider. Still
// correlated: the paper's finding is about weights recognizing their
// own output, not about who serves them.
func sameModelOtherVendor() JudgeSource {
	return JudgeSource{ProviderID: "judge-openrouter", ModelRev: "MiniMax-M3", EvalType: "drift_judge"}
}

// sameProviderOtherModel shares the vendor but not the model.
func sameProviderOtherModel() JudgeSource {
	return JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "abab6.5s-chat", EvalType: "drift_judge"}
}

// ---------------------------------------------------------------------------
// ClassifyIndependence
// ---------------------------------------------------------------------------

func TestClassifyIndependence(t *testing.T) {
	cases := []struct {
		name string
		a, b JudgeSource
		want Independence
	}{
		{"identical authority is none", originalJudge(), sameJudge(), IndependenceNone},
		{"exact same struct is none", originalJudge(), originalJudge(), IndependenceNone},
		{"both unspecified is none", JudgeSource{}, JudgeSource{}, IndependenceNone},
		{"one unspecified is conservative", JudgeSource{}, originalJudge(), IndependenceCorrelated},
		{"same model other provider is correlated", originalJudge(), sameModelOtherVendor(), IndependenceCorrelated},
		{"same provider other model is correlated", originalJudge(), sameProviderOtherModel(), IndependenceCorrelated},
		{"both differ is independent", originalJudge(), independentJudge(), IndependenceIndependent},
		{"reversed pair is independent", independentJudge(), originalJudge(), IndependenceIndependent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyIndependence(tc.a, tc.b); got != tc.want {
				t.Errorf("ClassifyIndependence(%s, %s) = %s, want %s", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// The EvalType field is recorded for audit but must NOT buy independence.
// If it did, G19 could manufacture independence just by picking a
// different eval_type on the same model, which is precisely the cheat
// this loop closes.
func TestClassifyIndependence_IgnoresEvalType(t *testing.T) {
	a := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", EvalType: "drift_judge"}
	b := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", EvalType: "grounding_check"}
	if got := ClassifyIndependence(a, b); got != IndependenceNone {
		t.Errorf("changing only EvalType must not create independence; got %s", got)
	}
}

// Missing identity fields must never be read as a clean bill of
// independence. Conservatism here is the whole safety property.
func TestClassifyIndependence_MissingFieldsAreConservative(t *testing.T) {
	cases := []struct {
		name string
		a, b JudgeSource
	}{
		{"model known, verifier model unknown", originalJudge(), JudgeSource{ProviderID: "judge-x"}},
		{"provider known, verifier provider unknown", originalJudge(), JudgeSource{ModelRev: "other-v9"}},
		{"original fully blank", JudgeSource{}, independentJudge()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIndependence(tc.a, tc.b)
			if got == IndependenceIndependent {
				t.Errorf("incomplete identity must not claim independence; %s vs %s got %s",
					tc.a, tc.b, got)
			}
			if got == IndependenceNone {
				t.Errorf("incomplete identity should be Correlated (not None), got %s", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// THE HEADLINE RULE — re-running the same judge is not verification
// ---------------------------------------------------------------------------

func TestArbitrate_SameJudgeReRunAbstains(t *testing.T) {
	// The exact G19 behavior today: drift judge says drift, then
	// grounding_check from the SAME model also says drift. A naive
	// system reads that as "corroborated!" and closes the dispute.
	// L12.1 refuses.
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameJudge(), AuthorityDrift, true)

	if a.Resolution != ResolutionAbstain {
		t.Errorf("same-judge re-run must abstain, got %s", a.Resolution)
	}
	if a.Class != ClassUnresolved {
		t.Errorf("same-judge re-run must be ClassUnresolved, got %s", a.Class)
	}
	if a.Basis != BasisCorrelatedRepeat {
		t.Errorf("same-judge re-run must be BasisCorrelatedRepeat, got %s", a.Basis)
	}
	if a.Grade != GradeUnverified {
		t.Errorf("null experiment must yield GradeUnverified, got %s", a.Grade)
	}
}

// The inverse: same judge DISAGREEING is equally uninformative. A
// system that only guards the agreement case is not really guarding
// anything.
func TestArbitrate_SameJudgeDisagreementAlsoAbstains(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameJudge(), AuthorityAlign, true)
	if a.Resolution != ResolutionAbstain {
		t.Errorf("same-judge disagreement must abstain, got %s", a.Resolution)
	}
	if a.Class != ClassUnresolved {
		t.Errorf("same-judge disagreement must be ClassUnresolved, got %s", a.Class)
	}
}

func TestArbitrate_SameJudgeRejectionNamesThePaper(t *testing.T) {
	// The audit trail must cite WHY, so a future reader who wonders
	// "why did it abstain?" finds the answer instead of a shrug.
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameJudge(), AuthorityDrift, true)
	if !strings.Contains(a.Reason, "2404.13076") {
		t.Errorf("rejection reason must cite the SOTA source; got %q", a.Reason)
	}
	if !strings.Contains(a.Reason, "cannot test its own error") {
		t.Errorf("rejection reason must explain the null experiment; got %q", a.Reason)
	}
}

// ---------------------------------------------------------------------------
// Idempotence — correlated corroboration never raises the grade
// ---------------------------------------------------------------------------

func TestArbitrate_CorrelatedAgreementIsIdempotent(t *testing.T) {
	// This is the mechanical heart of the rule: the grade must come out
	// EXACTLY as it went in, for every grade.
	for g := GradeUnverified; g <= GradeMeasured; g++ {
		a := Arbitrate(originalJudge(), AuthorityDrift, g,
			sameModelOtherVendor(), AuthorityDrift, true)
		if a.Resolution != ResolutionUpholdVerdict {
			t.Errorf("grade %s: correlated agreement should uphold; got %s", g, a.Resolution)
		}
		if a.Basis != BasisCorrelatedRepeat {
			t.Errorf("grade %s: correlated agreement must be BasisCorrelatedRepeat; got %s", g, a.Basis)
		}
		if a.Grade != g {
			t.Errorf("grade %s: correlated agreement must be IDEMPOTENT, got %s", g, a.Grade)
		}
	}
}

func TestArbitrate_CorrelatedDisagreementIsUninformative(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameProviderOtherModel(), AuthorityAlign, true)
	if a.Resolution != ResolutionAbstain {
		t.Errorf("correlated disagreement must abstain, got %s", a.Resolution)
	}
	if a.Class != ClassUnresolved {
		t.Errorf("correlated disagreement must be ClassUnresolved, got %s", a.Class)
	}
	if a.Grade != GradeUnverified {
		t.Errorf("correlated disagreement must yield GradeUnverified, got %s", a.Grade)
	}
	if !strings.Contains(a.Reason, "indistinguishable") {
		t.Errorf("reason must explain why the disagreement is uninformative; got %q", a.Reason)
	}
}

// ---------------------------------------------------------------------------
// Independence — the only path that can change a verdict
// ---------------------------------------------------------------------------

func TestArbitrate_IndependentAgreementPromotesOneStep(t *testing.T) {
	cases := []struct {
		in   EvidenceGrade
		want EvidenceGrade
	}{
		{GradeUnverified, GradeEstimated},
		{GradeEstimated, GradeModeled},
		{GradeModeled, GradeMeasured},
		{GradeMeasured, GradeMeasured}, // capped
	}
	for _, tc := range cases {
		a := Arbitrate(originalJudge(), AuthorityDrift, tc.in,
			independentJudge(), AuthorityDrift, true)
		if a.Resolution != ResolutionUpholdVerdict {
			t.Errorf("%s: independent agreement should uphold; got %s", tc.in, a.Resolution)
		}
		if a.Basis != BasisIndependentEvidence {
			t.Errorf("%s: basis must be BasisIndependentEvidence; got %s", tc.in, a.Basis)
		}
		if a.Grade != tc.want {
			t.Errorf("%s: independent agreement should promote to %s; got %s", tc.in, tc.want, a.Grade)
		}
	}
}

func TestArbitrate_IndependentDisagreementOverturns(t *testing.T) {
	// Row 1985 resolved honestly: an independent authority finds no
	// drift, so the original judge is the one at fault.
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		independentJudge(), AuthorityAlign, true)
	if a.Resolution != ResolutionOverturnVerdict {
		t.Errorf("independent disagreement must overturn; got %s", a.Resolution)
	}
	if a.Class != ClassJudgeError {
		t.Errorf("verifier saying aligned means judge-error; got %s", a.Class)
	}
	if a.Grade != GradeMeasured {
		t.Errorf("resolved independent disagreement must be GradeMeasured; got %s", a.Grade)
	}
	if !strings.Contains(a.Reason, "1985") {
		t.Errorf("reason should reference the row 1985 pattern; got %q", a.Reason)
	}
}

// The mirror case: the independent authority finds drift the first
// judge MISSED. Same Resolution, different Class. This proves the
// arbitration is not rigged toward the operator.
func TestArbitrate_IndependentDisagreementOtherDirection(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityAlign, GradeModeled,
		independentJudge(), AuthorityDrift, true)
	if a.Resolution != ResolutionOverturnVerdict {
		t.Errorf("independent disagreement must overturn; got %s", a.Resolution)
	}
	if a.Class != ClassArtifactError {
		t.Errorf("verifier saying drift means artifact-error; got %s", a.Class)
	}
	if !strings.Contains(a.Reason, "missed") {
		t.Errorf("reason should say the drift was missed; got %q", a.Reason)
	}
}

// ---------------------------------------------------------------------------
// Row 1985 guard — nli=neutral is inert, in BOTH directions
// ---------------------------------------------------------------------------

func TestArbitrate_NonDecisiveOriginalCannotBeOverturned(t *testing.T) {
	for _, orig := range []Authority{AuthorityNeutral, AuthorityUnknown} {
		a := Arbitrate(originalJudge(), orig, GradeUnverified,
			independentJudge(), AuthorityAlign, true)
		if a.Resolution != ResolutionAbstain {
			t.Errorf("original %s: must abstain; got %s", orig, a.Resolution)
		}
		if a.Class != ClassUnresolved {
			t.Errorf("original %s: must be ClassUnresolved; got %s", orig, a.Class)
		}
	}
}

// THE GUARD. A neutral verifier must not be able to overturn an aligned
// verdict any more than it can uphold a drift one. Asymmetry here is
// the row 1985 bug reproduced.
func TestArbitrate_NonDecisiveVerifierCannotOverturn(t *testing.T) {
	for _, orig := range []Authority{AuthorityAlign, AuthorityDrift} {
		for _, ver := range []Authority{AuthorityNeutral, AuthorityUnknown} {
			a := Arbitrate(originalJudge(), orig, GradeMeasured,
				independentJudge(), ver, true)
			if a.Resolution != ResolutionAbstain {
				t.Errorf("orig=%s ver=%s: neutral verifier must abstain; got %s",
					orig, ver, a.Resolution)
			}
			if a.Grade != GradeUnverified {
				t.Errorf("orig=%s ver=%s: neutral verifier must yield GradeUnverified; got %s",
					orig, ver, a.Grade)
			}
		}
	}
}

// Regression for the defect found while writing L12.1: Rule 3 originally
// hardcoded BasisCorrelatedRepeat, which filed an INDEPENDENT
// consultation under a "correlated repeat" label. The Basis field is
// the anti-laundering marker, so a false marker there defeats the file.
func TestArbitrate_IndependentNonDecisiveVerifierIsNotMislabeled(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		independentJudge(), AuthorityNeutral, true)
	if a.Basis != BasisIndependentEvidence {
		t.Errorf("an independent consultation must not be labeled correlated-repeat; got %s", a.Basis)
	}
	if !strings.Contains(a.Reason, "genuinely contested") {
		t.Errorf("reason should note the question looks contested; got %q", a.Reason)
	}
}

// Conversely, a genuinely correlated non-decisive verifier MUST be
// labeled correlated-repeat. Guards the other direction.
func TestArbitrate_CorrelatedNonDecisiveVerifierIsLabeledCorrelated(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameModelOtherVendor(), AuthorityNeutral, true)
	if a.Basis != BasisCorrelatedRepeat {
		t.Errorf("correlated consultation must be BasisCorrelatedRepeat; got %s", a.Basis)
	}
}

// ---------------------------------------------------------------------------
// Rule D — the unexamined default
// ---------------------------------------------------------------------------

// Nothing in L12.1 may ever certify a verdict nobody checked.
func TestArbitrate_NoAllegationLeavesVerdictUnverified(t *testing.T) {
	for _, auth := range []Authority{AuthorityAlign, AuthorityDrift} {
		a := Arbitrate(originalJudge(), auth, GradeMeasured,
			independentJudge(), AuthorityDrift, false)
		if a.Resolution != ResolutionUpholdVerdict {
			t.Errorf("unalleged verdict must stand; got %s", a.Resolution)
		}
		if a.Grade != GradeUnverified {
			t.Errorf("STANDS is not VERIFIED: unalleged %s must be GradeUnverified, got %s",
				auth, a.Grade)
		}
		if !strings.Contains(a.Reason, "unexamined") {
			t.Errorf("reason must say the verdict stands unexamined; got %q", a.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Operator authority — in force, but not verification
// ---------------------------------------------------------------------------

func TestArbitrateByOperator_AuthorityIsNotEvidence(t *testing.T) {
	a := ArbitrateByOperator(AuthorityDrift, ClassJudgeError)
	if a.Basis != BasisOperatorAuthority {
		t.Errorf("basis must be BasisOperatorAuthority; got %s", a.Basis)
	}
	if a.Grade != GradeUnverified {
		t.Errorf("operator authority must not produce a verified grade; got %s", a.Grade)
	}
	if a.Grade.Trusted() {
		t.Error("an operator override must never be Trusted() as fact")
	}
	if a.Resolution == ResolutionAbstain {
		t.Error("the operator owns the system; their call must not abstain")
	}
	if a.Resolution != ResolutionOverturnVerdict {
		t.Errorf("judge-error class should overturn; got %s", a.Resolution)
	}
	if !a.OperatorMayOverride {
		t.Error("OperatorMayOverride must always be true")
	}
}

func TestArbitrateByOperator_ClassToResolution(t *testing.T) {
	// Resolution derives from the MISMATCH between the verdict's claim
	// and who erred — so ClassArtifactError flips action depending on
	// what the verdict originally said.
	cases := []struct {
		orig  Authority
		class DisagreementClass
		want  Resolution
	}{
		// Judge erred -> the verdict never stands. This is row 1985.
		{AuthorityDrift, ClassJudgeError, ResolutionOverturnVerdict},
		{AuthorityAlign, ClassJudgeError, ResolutionOverturnVerdict},
		// Artifact at fault -> upholds a drift verdict, overturns an aligned one.
		{AuthorityDrift, ClassArtifactError, ResolutionUpholdVerdict},
		{AuthorityAlign, ClassArtifactError, ResolutionOverturnVerdict},
		// Nobody erred -> fix the spec.
		{AuthorityDrift, ClassSpecAmbiguity, ResolutionAmendSpec},
		{AuthorityAlign, ClassSpecAmbiguity, ResolutionAmendSpec},
		// No finding -> leave it alone.
		{AuthorityDrift, ClassNone, ResolutionUpholdVerdict},
		{AuthorityDrift, ClassUnresolved, ResolutionUpholdVerdict},
		{AuthorityDrift, ClassOperatorError, ResolutionUpholdVerdict},
	}
	for _, tc := range cases {
		a := ArbitrateByOperator(tc.orig, tc.class)
		if a.Resolution != tc.want {
			t.Errorf("orig=%s class=%s: want %s, got %s",
				tc.orig, tc.class, tc.want, a.Resolution)
		}
		if a.Grade != GradeUnverified {
			t.Errorf("orig=%s class=%s: authority must stay GradeUnverified, got %s",
				tc.orig, tc.class, a.Grade)
		}
		if a.Basis != BasisOperatorAuthority {
			t.Errorf("orig=%s class=%s: basis must be operator-authority, got %s",
				tc.orig, tc.class, a.Basis)
		}
	}
}

// ---------------------------------------------------------------------------
// Rendering + Claim integration with L11.1
// ---------------------------------------------------------------------------

func TestArbitration_StringRendersEveryField(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		independentJudge(), AuthorityAlign, true)
	s := a.String()
	for _, want := range []string{"arbitration", "basis", "evidence", "claims", "reason", "operator", "judge-error"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

// A dispute must flow through the same L11.1 evidence surface as every
// other operator-facing claim. Bypassing it would defeat L12.1.
func TestArbitration_ExportsAsClaimWithReason(t *testing.T) {
	a := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameJudge(), AuthorityDrift, true)
	c := a.Claim()
	if c.Source != "arbitration_model(L12.1)" {
		t.Errorf("claim source = %q", c.Source)
	}
	if c.Caveat != a.Reason {
		t.Errorf("claim caveat must carry the arbitration reason; got %q", c.Caveat)
	}
	if c.Grade != a.Grade {
		t.Errorf("claim grade %s != arbitration grade %s", c.Grade, a.Grade)
	}
	// L11.1 invariant: anything weaker than measured needs a caveat.
	if c.Grade != GradeMeasured && c.Caveat == "" {
		t.Error("non-measured claim must carry a caveat")
	}
}

// ---------------------------------------------------------------------------
// Determinism + exhaustiveness
// ---------------------------------------------------------------------------

func TestArbitrate_Deterministic(t *testing.T) {
	o, v := originalJudge(), independentJudge()
	first := Arbitrate(o, AuthorityDrift, GradeModeled, v, AuthorityAlign, true)
	for i := 0; i < 50; i++ {
		got := Arbitrate(o, AuthorityDrift, GradeModeled, v, AuthorityAlign, true)
		if got != first {
			t.Fatalf("Arbitrate must be deterministic; iteration %d diverged:\n%+v\n%+v", i, got, first)
		}
	}
}

// The full matrix, pinned against the independence LADDER rather than a
// loose "must abstain" blanket. Each rung's ceiling is checked
// separately, which is the property that actually matters:
//
//	None        -> abstain, always
//	Correlated  -> may uphold, never overturn, grade never rises
//	Independent -> may overturn
func TestArbitrate_ExhaustiveMatrix(t *testing.T) {
	auths := []Authority{AuthorityAlign, AuthorityDrift, AuthorityNeutral, AuthorityUnknown}
	sources := []JudgeSource{originalJudge(), sameJudge(), independentJudge(), sameModelOtherVendor(), JudgeSource{}}
	grades := []EvidenceGrade{GradeUnverified, GradeEstimated, GradeModeled, GradeMeasured}

	var nNone, nCorrelated, nIndependent int
	var abstain, uphold, overturn int

	for _, o := range auths {
		for _, v := range auths {
			for _, so := range sources {
				for _, sv := range sources {
					for _, g := range grades {
						a := Arbitrate(so, o, g, sv, v, true)
						indep := ClassifyIndependence(so, sv)

						switch indep {
						case IndependenceNone:
							nNone++
							if a.Resolution != ResolutionAbstain {
								t.Fatalf("None must ALWAYS abstain; got %s (%+v)", a.Resolution, a)
							}
							if a.Grade != GradeUnverified {
								t.Fatalf("None must be GradeUnverified; got %s (%+v)", a.Grade, a)
							}
							if a.Basis == BasisIndependentEvidence {
								t.Fatalf("None must never claim independent evidence: %+v", a)
							}
							// Rule 2 (the null-experiment report) only fires when
							// the original verdict actually asserted something. A
							// non-decisive original is caught earlier by Rule 1,
							// where no-evidence is the honest basis.
							if o.decisive() && v.decisive() && a.Basis != BasisCorrelatedRepeat {
								t.Fatalf("decisive None pair must be BasisCorrelatedRepeat; got %s (%+v)", a.Basis, a)
							}
						case IndependenceCorrelated:
							nCorrelated++
							if a.Resolution == ResolutionOverturnVerdict {
								t.Fatalf("Correlated must NEVER overturn (%+v)", a)
							}
							if a.Resolution == ResolutionUpholdVerdict && a.Grade != g {
								t.Fatalf("Correlated uphold must be grade-flat: in=%s out=%s (%+v)", g, a.Grade, a)
							}
						case IndependenceIndependent:
							nIndependent++
						}

						switch a.Resolution {
						case ResolutionAbstain:
							abstain++
							if a.Grade != GradeUnverified {
								t.Fatalf("abstention must be GradeUnverified: %+v", a)
							}
						case ResolutionUpholdVerdict:
							uphold++
							if !o.decisive() && !v.decisive() {
								t.Fatalf("non-decisive pair upheld: %+v", a)
							}
						case ResolutionOverturnVerdict:
							overturn++
							if indep != IndependenceIndependent {
								t.Fatalf("OVERTURN WITHOUT INDEPENDENCE: %+v", a)
							}
							if !o.decisive() || !v.decisive() {
								t.Fatalf("non-decisive pair overturned: %+v", a)
							}
						default:
							t.Fatalf("unexpected resolution: %+v", a)
						}

						if a.Reason == "" {
							t.Fatalf("reason must never be empty: %+v", a)
						}
						if !a.OperatorMayOverride {
							t.Fatalf("OperatorMayOverride must always be true: %+v", a)
						}
						if !a.Alleged {
							t.Fatalf("alleged must be true in this matrix: %+v", a)
						}
					}
				}
			}
		}
	}

	// The ladder must actually be exercised, or this matrix proves
	// nothing about the rule it claims to pin.
	if nNone == 0 || nCorrelated == 0 || nIndependent == 0 {
		t.Fatalf("all three rungs must be exercised; got None=%d Correlated=%d Independent=%d",
			nNone, nCorrelated, nIndependent)
	}
	if overturn == 0 {
		t.Error("no independent overturn ever occurred; the matrix never tests the rule it exists for")
	}
	if abstain <= uphold {
		t.Errorf("abstentions must dominate; abstain=%d uphold=%d overturn=%d", abstain, uphold, overturn)
	}
	t.Logf("matrix: none=%d correlated=%d independent=%d | abstain=%d uphold=%d overturn=%d",
		nNone, nCorrelated, nIndependent, abstain, uphold, overturn)
}

// The invariant that ties L12.1 to L11.1: the arbitration grade may
// never exceed GradeMeasured, and may only ever RISE on an independent
// basis.
func TestArbitrate_GradeNeverExceedsMeasured(t *testing.T) {
	auths := []Authority{AuthorityAlign, AuthorityDrift, AuthorityNeutral, AuthorityUnknown}
	grades := []EvidenceGrade{GradeUnverified, GradeEstimated, GradeModeled, GradeMeasured}
	for _, o := range auths {
		for _, v := range auths {
			for _, g := range grades {
				for _, sv := range []JudgeSource{sameJudge(), independentJudge(), sameModelOtherVendor()} {
					a := Arbitrate(originalJudge(), o, g, sv, v, true)
					if a.Grade > GradeMeasured {
						t.Fatalf("grade escaped ceiling: %+v", a)
					}
					if a.Grade > g && a.Basis != BasisIndependentEvidence {
						t.Fatalf("grade rose without independent evidence: %+v", a)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrency (the race detector is part of the ship gate)
// ---------------------------------------------------------------------------

func TestArbitrate_ParallelSafe(t *testing.T) {
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
					independentJudge(), AuthorityAlign, true)
				ArbitrateByOperator(AuthorityDrift, ClassJudgeError)
				ClassifyIndependence(originalJudge(), sameJudge())
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// Documentation example — the row 1985 incident, end to end
// ---------------------------------------------------------------------------

func TestExample_Arbitrate_Row1985FalsePositive(t *testing.T) {
	// What actually happened: MiniMax-M3 returned a false-positive
	// drift verdict. The operator alleged it. G19 asked MiniMax-M3
	// again, and MiniMax-M3 said drift again. Two "agreeing" verdicts,
	// zero new information.
	step1 := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		sameJudge(), AuthorityDrift, true)
	t.Log("step 1 (re-ask the same judge):\n" + step1.String())
	if step1.Resolution != ResolutionAbstain {
		t.Fatalf("step 1 must abstain; got %s", step1.Resolution)
	}

	// The honest fix: bring in a genuinely different authority.
	step2 := Arbitrate(originalJudge(), AuthorityDrift, GradeModeled,
		independentJudge(), AuthorityAlign, true)
	t.Log("step 2 (independent authority):\n" + step2.String())
	if step2.Class != ClassJudgeError {
		t.Fatalf("step 2 must conclude judge-error; got %s", step2.Class)
	}

	fmt.Println()
	fmt.Println("row 1985, arbitrated honestly:")
	fmt.Print(step1.String())
	fmt.Println()
	fmt.Print(step2.String())
}
