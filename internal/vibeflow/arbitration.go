package vibeflow

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Loop 12 L12.1 — G19 epistemic arbitration
//
// THE PROBLEM THIS LOOP EXISTS TO FIX
// ====================================
//
// On 2026-10-08 the operator overrode two drift_judge verdicts by hand
// (evals 2016 and 2017, recorded in row 2533). Both were false positives
// from the same judge, both followed the same pattern already seen in
// rows 482 / 1985 / 2013: nli=neutral or nli=contradiction presented as
// if it were fact. Row 1985 is the canonical case — a MiniMax-M3 drift
// verdict the operator had to overrule manually.
//
// When the operator alleges a false positive, gate G19
// (event E19, "false_positive_alleged") responds with:
//
//	judgment_history(target)      -> what did we already say?
//	research_recall(claim)        -> what else claims this?
//	judge(eval_type=grounding_check)  -> ...ask the judge again
//
// That third call is the bug. It re-runs the SAME authority on the SAME
// question. It is repetition, not verification.
//
// WHY REPETITION IS EPISTEMICALLY NULL
// ------------------------------------
// Panickssery, Bowman & Feng (2024), "LLM Evaluators Recognize and Favor
// Their Own Generations", arXiv:2404.13076 (submitted 15 Apr 2024;
// verified against the arXiv abstract page, 2026-10-08, tier-1):
//
//	"Self-evaluation using large language models (LLMs) has proven
//	 valuable ... But new biases are introduced due to the same LLM
//	 acting as both the evaluator and the evaluatee. One such bias is
//	 self-preference, where an LLM evaluator scores its own outputs
//	 higher than others' while human annotators consider them of
//	 equal quality."
//
// and, the finding that matters here:
//
//	"By fine-tuning LLMs, we discover a linear correlation between
//	 self-recognition capability and the strength of self-preference
//	 bias."
//
// So: when the same model grades the same artifact twice, its second
// grade carries essentially none of the independence a second opinion
// is supposed to provide. A correlated re-judge can CONFIRM. It can
// never DISPUTE. Re-running the judge is a null experiment, and today
// G19 presents it as evidence.
//
// THE MECHANICAL RULE OF L12.1
// ===========================
//
//	A VERDICT IS ONLY DISPUTED BY INDEPENDENT EVIDENCE.
//	CORRELATED RE-JUDGING IS IDEMPOTENT.
//
// Note the shape of that rule. It is the mirror image of L11.1's
// confidence-propagates-downward-only. L11.1 stopped confidence from
// travelling UP the evidence chain. L12.1 stops repetition from
// travelling ACROSS a second look. Both rules exist for the same
// reason: rows 1985 / 2013 / 2016 / 2017, where a weak signal was
// laundered into a strong claim and the operator paid for it.
//
// THE THIRD RULE, THE ONE THAT CLOSES ROW 1985
// ============================================
//
// A non-decisive verdict (nli=neutral, needs_human) can NEITHER confirm
// NOR overturn — in either direction. Symmetry is the point. It is no
// more honest to let a neutral verifier uphold a "drift" verdict than
// to let it overturn an "aligned" one. Both launder a non-answer into
// an answer.
//
// DESIGN PRINCIPLES (for human audit — same as L9.3 / L10.1 / L11.1)
// ====================================================================
//
//	A. INDEPENDENCE IS COMPUTED, NOT ASSERTED. Every arbitration takes
//	   the provider_id + model_rev of BOTH authorities and derives
//	   independence from them. Nobody can hand-wave "I checked again".
//	B. FAIL TOWARD ABSTENTION. Every uncertain path returns
//	   ResolutionAbstain. The default state of any judge verdict is
//	   UNVERIFIED — even when nobody disputes it (Rule D below).
//	C. AUTHORITY IS NOT EVIDENCE. An operator override STANDS (they own
//	   the system) but is recorded as BasisOperatorAuthority at
//	   GradeUnverified, so a later reader sees "the operator decided and
//	   nobody independently checked" instead of a silent override that
//	   reads like a clean verification.
//	D. THE UNEXAMINED DEFAULT. If no disagreement is alleged, the
//	   verdict stands — labeled GradeUnverified, Reason "stands
//	   unexamined". Nothing in this file will ever certify a verdict
//	   that nobody checked.
//	E. DETERMINISTIC. Pure functions. No clock, no randomness, no I/O.
//	   Same inputs → same Arbitration, byte for byte.
//
// ---------------------------------------------------------------------------

// JudgeSource identifies a judging authority precisely enough to
// compute whether a second look is independent of the first.
//
// ProviderID and ModelRev come straight from drift_judge's own response
// envelope (chat-minimax-cn / MiniMax-M3 in this deployment), so the
// independence computation is grounded in what the system actually
// reported rather than in what the caller remembers.
// Origin is the THIRD axis of independence, added in L13.1.
//
// L12.1 computed independence on two axes: model and provider. That is
// sufficient when the judge and the artifact author are the same agent
// (the row 1985 case). It is NOT sufficient once work is delegated,
// because a delegated artifact is produced by a different agent
// instance and the model/provider pair alone cannot say whether two
// claims came from the same mind or from two.
//
// Origin answers exactly one question: WHO produced this claim.
//
// It is deliberately an orthogonal axis rather than a fourth field
// folded into the model string, because origin and model fail
// differently. Two DIFFERENT models behind one origin can still share
// finetuning lineage. Two instances of the SAME model are sampling
// variance, not decorrelated error. Conflating those is the mistake.
type Origin string

const (
	// OriginUnspecified is the zero value. It means "we did not record
	// who produced this". It is deliberately NOT OriginSelf: treating
	// an unrecorded origin as "self" would silently change the
	// classification of every pre-L13.1 caller. Unspecified keeps
	// L12.1's two-axis ladder in force, which is the backward-
	// compatible behavior.
	OriginUnspecified Origin = ""

	// OriginSelf means the claim was produced by an agent. The specific
	// instance is not recorded, so two OriginSelf claims cannot be
	// shown to come from different minds and therefore cannot establish
	// independence. This is the Panickssery self-preference trap:
	// arXiv:2404.13076 shows an evaluator scoring its own output higher
	// than a human would.
	OriginSelf Origin = "self"

	// OriginSubagent means the claim was produced by a delegated
	// instance, registered via subagent_register / agent_memory_delegate.
	OriginSubagent Origin = "subagent"

	// OriginOperator means a human asserted it. Authority, not evidence
	// — see ArbitrateByOperator and BasisOperatorAuthority.
	OriginOperator Origin = "operator"

	// OriginExternal means a third party outside this system asserted
	// it (a vendored spec, a measured benchmark, an upstream release).
	OriginExternal Origin = "external"
)

// String renders the origin.
func (o Origin) String() string {
	if o == OriginUnspecified {
		return "<unspecified>"
	}
	return string(o)
}

// known reports whether the origin was actually recorded.
func (o Origin) known() bool { return o != OriginUnspecified }

type JudgeSource struct {
	// ProviderID is the judge provider, e.g. "chat-minimax-cn".
	ProviderID string

	// ModelRev is the model revision, e.g. "MiniMax-M3".
	ModelRev string

	// EvalType records which eval produced the claim. Recorded for the
	// audit trail; it is deliberately NOT part of the independence
	// computation, because asking the same authority a different
	// question does not make the second answer independent.
	EvalType string

	// Origin is the third independence axis (L13.1). Zero value
	// OriginUnspecified preserves L12.1's two-axis behavior exactly,
	// so adding this field is not a breaking change for existing
	// callers and existing tests.
	Origin Origin
}

// String renders the source for audit output.
func (s JudgeSource) String() string {
	p, m := s.ProviderID, s.ModelRev
	base := "<unspecified>"
	switch {
	case p == "" && m == "":
		base = "<unspecified>"
	case p == "":
		base = m
	case m == "":
		base = p
	default:
		base = p + "/" + m
	}
	if s.Origin.known() {
		return base + " [" + s.Origin.String() + "]"
	}
	return base
}

// ---------------------------------------------------------------------------
// Independence
// ---------------------------------------------------------------------------

// Independence classifies whether a second look is genuinely new
// evidence or merely a re-run of the same mind.
//
// The three classes form a ladder, and each rung buys progressively
// less. What each can do is pinned by test, because the whole value of
// the type is that these limits are mechanical:
//
//	None         -> must ABSTAIN. The identical authority was asked the
//	                same question again. Nothing was learned, not even
//	                weak corroboration. This is G19's current behavior.
//	Correlated   -> may UPHOLD, with the evidence grade held exactly
//	                flat (idempotent). A different serving path reached
//	                the same conclusion, which is weak corroboration.
//	                It may NEVER overturn: correlated error and genuine
//	                signal are indistinguishable.
//	Independent  -> the only rung that may OVERTURN, and the only one
//	                that may raise the evidence grade.
//
// The None/Correlated distinction is not pedantry. "The judge says what
// it said" and "a second process that shares this model's error
// structure agrees" are different epistemic objects, and conflating
// them is how a null experiment gets written up as a replication.
type Independence int

const (
	// IndependenceNone means the verifier IS the original authority (or
	// neither is identifiable). A re-run of one judge cannot test that
	// judge's own error. This is the null-experiment case.
	IndependenceNone Independence = iota

	// IndependenceCorrelated means the two share a model or a provider,
	// so their errors are expected to move together. Correlated
	// agreement corroborates; correlated disagreement is
	// uninformative.
	IndependenceCorrelated

	// IndependenceIndependent means both the model and the provider
	// differ. Only this class can overturn a verdict.
	IndependenceIndependent
)

// String renders the independence class.
func (i Independence) String() string {
	switch i {
	case IndependenceNone:
		return "none"
	case IndependenceCorrelated:
		return "correlated"
	case IndependenceIndependent:
		return "independent"
	default:
		return fmt.Sprintf("unknown(%d)", int(i))
	}
}

// ClassifyIndependence derives the independence of b relative to a.
//
// THREE AXES (L13.1). The ordering encodes the precedence of the bias
// sources, and the precedence itself is the design decision:
//
//	ORIGIN FIRST, AND IT CAN ONLY DEGRADE.
//
// arXiv:2404.13076 makes origin the load-bearing axis for the
// self-preference trap: the danger is not "a model was involved" but
// "the evaluator and the evaluatee are the same mind". A shared origin
// is therefore never independent, regardless of how different the
// provider strings look. Symmetrically, a missing origin on either side
// falls back to correlated, because an unrecorded origin cannot be shown
// to differ from a recorded one.
//
// Origin diversity is NEVER sufficient. Two instances of one model under
// two roles are sampling variance, not decorrelated error — so having
// different origins does not rescue a shared model, and the model/provider
// ladder below still runs and still governs.
//
// BACKWARD COMPATIBILITY. When either side carries OriginUnspecified
// (the zero value), the origin checks are skipped and behavior is
// byte-identical to L12.1. Adding the axis did not change any shipped
// classification, and TestL13_BackCompat pins that claim mechanically
// rather than asking a reader to trust a comment.
//
// Then, unchanged from L12.1:
//
//   - Same provider AND same model → None. There is nothing to be
//     independent of: this is the literal re-run that G19 performs
//     today, and the null-experiment case.
//     EvalType is deliberately excluded from this comparison. Struct
//     equality would include it, but changing eval_type on one model
//     must not manufacture independence — that would hand G19 a trivial
//     cheat.
//   - Same non-empty model → Correlated, EVEN ACROSS providers.
//   - Same non-empty provider → Correlated.
//   - Both models AND both providers known and differing → Independent.
//   - Otherwise → Correlated. Independence that cannot be proven is not
//     claimed, and a missing field is never a clean bill.
func ClassifyIndependence(a, b JudgeSource) Independence {
	bothKnown := a.Origin.known() && b.Origin.known()
	bothUnrecorded := !a.Origin.known() && !b.Origin.known()
	oneKnown := a.Origin.known() != b.Origin.known()
	sameWeights := a.ProviderID == b.ProviderID && a.ModelRev == b.ModelRev

	// Step 1 — None means THE IDENTICAL AUTHORITY. That holds when both
	// sides are the same weights AND the origin cannot distinguish them
	// as separate minds: either both unrecorded (the L12.1 case), or
	// both recorded as the SAME origin.
	//
	// The explicit exclusion of known-DIFFERENT origins is the L13.1
	// fix. Two instances sharing one model and provider are distinct
	// processes, not one authority asked twice, so calling that None
	// would understate the evidence — and it inverted the ladder,
	// reporting a delegated artifact as LESS independent than a
	// self-judged one.
	if sameWeights && (bothUnrecorded || (bothKnown && a.Origin == b.Origin)) {
		return IndependenceNone
	}

	// Step 2 — origin axis. It can only degrade, never create.
	//
	// Same recorded origin: these claims cannot be shown to come from
	// different minds, however different the provider strings look.
	// arXiv:2404.13076 makes origin the load-bearing axis for the
	// self-preference trap — the danger is not "a model was involved"
	// but "the evaluator and the evaluatee are the same mind".
	if bothKnown && a.Origin == b.Origin {
		return IndependenceCorrelated
	}

	// Step 3 — a half-recorded origin cannot prove difference.
	if oneKnown {
		return IndependenceCorrelated
	}

	// Step 4 — same model: distinct instances of one set of weights are
	// sampling variance, not decorrelated error. Different origins do
	// NOT rescue this.
	if a.ModelRev != "" && a.ModelRev == b.ModelRev {
		return IndependenceCorrelated
	}

	// Step 5 — same provider: shared serving stack and, in practice,
	// shared fine-tuning lineage.
	if a.ProviderID != "" && a.ProviderID == b.ProviderID {
		return IndependenceCorrelated
	}

	// Step 6 — all axes differ and are recorded. The only rung that
	// earns the right to dispute.
	if a.ModelRev != "" && b.ModelRev != "" && a.ProviderID != "" && b.ProviderID != "" {
		return IndependenceIndependent
	}

	// Step 7 — incomplete identity. Independence that cannot be proven
	// is not claimed.
	return IndependenceCorrelated
}

// ---------------------------------------------------------------------------
// Authority — the claim being arbitrated
// ---------------------------------------------------------------------------

// Authority is one authority's claim about an artifact. The four values
// map 1:1 onto what drift_judge actually returns, so no translation
// layer can quietly widen or narrow a verdict:
//
//	AuthorityAlign   <- aligned
//	AuthorityDrift   <- drift_detected
//	AuthorityNeutral <- needs_human / nli=neutral
//	AuthorityUnknown <- no claim was recorded at all
type Authority string

const (
	AuthorityAlign   Authority = "aligned"
	AuthorityDrift   Authority = "drift_detected"
	AuthorityNeutral Authority = "needs_human"
	AuthorityUnknown Authority = "unknown"
)

// String renders the authority's claim.
func (a Authority) String() string { return string(a) }

// decisive reports whether a claim is strong enough to CONFIRM or to
// OVERTURN anything.
//
// Only aligned and drift_detected are decisive. needs_human is the
// system explicitly saying "I could not decide", and unknown is
// silence. Treating either as evidence is precisely the row 1985
// failure: nli=neutral was recorded as if it were a finding.
//
// This predicate is what makes arbitration symmetric — a neutral claim
// is inert whether it would have helped or hurt the original verdict.
func (a Authority) decisive() bool {
	return a == AuthorityAlign || a == AuthorityDrift
}

// ---------------------------------------------------------------------------
// DisagreementClass — WHO is wrong
// ---------------------------------------------------------------------------

// DisagreementClass names the party at fault, so the operator learns
// something actionable rather than just "the verdict changed".
type DisagreementClass int

const (
	// ClassNone means no disagreement survives the evidence.
	ClassNone DisagreementClass = iota

	// ClassJudgeError means the judge mis-graded the artifact and the
	// artifact was fine. This is row 1985.
	ClassJudgeError

	// ClassArtifactError means the artifact really did drift and the
	// first judge missed it.
	ClassArtifactError

	// ClassSpecAmbiguity means both parties are defensible because the
	// spec does not actually settle the question. The fix belongs in
	// the spec, not in either artifact or verdict.
	ClassSpecAmbiguity

	// ClassOperatorError means the operator's allegation was unfounded.
	// It exists so the model is not structurally incapable of saying
	// so; it is not a suggestion that operators are usually wrong.
	ClassOperatorError

	// ClassUnresolved means the evidence cannot decide. ABSTAIN. This
	// is the honest default and the most frequent honest outcome of a
	// correlated second look.
	ClassUnresolved
)

// String renders the class.
func (c DisagreementClass) String() string {
	switch c {
	case ClassNone:
		return "no-disagreement"
	case ClassJudgeError:
		return "judge-error"
	case ClassArtifactError:
		return "artifact-error"
	case ClassSpecAmbiguity:
		return "spec-ambiguity"
	case ClassOperatorError:
		return "operator-error"
	case ClassUnresolved:
		return "unresolved"
	default:
		return fmt.Sprintf("unknown(%d)", int(c))
	}
}

// ---------------------------------------------------------------------------
// Resolution — WHAT happens to the original verdict
// ---------------------------------------------------------------------------

// Resolution states what happens to the ORIGINAL verdict. Both
// directions of disagreement resolve to OverturnVerdict and differ only
// in Class, so the field is never ambiguous about its own reference
// point.
type Resolution int

const (
	// ResolutionAbstain means no call. The honest default.
	ResolutionAbstain Resolution = iota

	// ResolutionUpholdVerdict means the original verdict stands.
	ResolutionUpholdVerdict

	// ResolutionOverturnVerdict means the original verdict is replaced.
	ResolutionOverturnVerdict

	// ResolutionAmendSpec means the spec, not the artifact and not the
	// verdict, is what needs to change.
	ResolutionAmendSpec
)

// String renders the resolution.
func (r Resolution) String() string {
	switch r {
	case ResolutionAbstain:
		return "abstain"
	case ResolutionUpholdVerdict:
		return "uphold"
	case ResolutionOverturnVerdict:
		return "overturn"
	case ResolutionAmendSpec:
		return "amend-spec"
	default:
		return fmt.Sprintf("unknown(%d)", int(r))
	}
}

// ---------------------------------------------------------------------------
// Basis — WHY we resolved it, and the anti-laundering marker
// ---------------------------------------------------------------------------

// Basis separates EVIDENCE from AUTHORITY. Without it, an operator
// override and an independent verification are indistinguishable in the
// audit trail, and that indistinguishability is how a weak claim starts
// looking like a settled fact.
type Basis int

const (
	// BasisNoEvidence means nothing was consulted. The verdict simply
	// stands, unexamined.
	BasisNoEvidence Basis = iota

	// BasisCorrelatedRepeat means a re-run that added no independent
	// information. Its epistemic value is zero.
	BasisCorrelatedRepeat

	// BasisIndependentEvidence means a genuinely different model AND
	// provider decided. The only basis that can change a verdict.
	BasisIndependentEvidence

	// BasisOperatorAuthority means the operator decided. Legitimate and
	// in force — the operator owns the system — but NOT verification.
	BasisOperatorAuthority
)

// String renders the basis.
func (b Basis) String() string {
	switch b {
	case BasisNoEvidence:
		return "no-evidence"
	case BasisCorrelatedRepeat:
		return "correlated-repeat"
	case BasisIndependentEvidence:
		return "independent-evidence"
	case BasisOperatorAuthority:
		return "operator-authority"
	default:
		return fmt.Sprintf("unknown(%d)", int(b))
	}
}

// ---------------------------------------------------------------------------
// Arbitration
// ---------------------------------------------------------------------------

// Arbitration is the structured result of adjudicating a disputed
// drift verdict. Every field exists so that a human reading the audit
// trail six months later can tell WHAT was decided, ON WHAT evidence,
// and HOW MUCH of it was independent.
type Arbitration struct {
	// Alleged is the operator's claim that a false positive occurred.
	// False means nobody disputed the verdict.
	Alleged bool

	// Independence classifies the verifier relative to the original.
	Independence Independence

	// Original and Verifier are the two competing claims.
	Original Authority
	Verifier Authority

	// OriginalGrade is the evidence grade the original verdict carried
	// on entry, so the idempotence rule can be enforced against the
	// real prior rather than a constant.
	OriginalGrade EvidenceGrade

	// Class names the party at fault.
	Class DisagreementClass

	// Resolution states what happens to the original verdict.
	Resolution Resolution

	// Basis is WHY — the anti-laundering marker.
	Basis Basis

	// Grade is the resulting evidence grade. Never above OriginalGrade
	// on a correlated basis; capped at GradeMeasured on independent
	// corroboration; GradeUnverified whenever authority, not evidence,
	// is what settled it.
	Grade EvidenceGrade

	// OperatorMayOverride is always true. The operator owns the system
	// and nothing here locks them out. It is recorded explicitly so
	// that a human knows arbitration ADVISES rather than DECIDES.
	OperatorMayOverride bool

	// Reason is a human-readable audit sentence. Never empty.
	Reason string
}

// promote returns the next stronger evidence grade, capped at
// GradeMeasured. Used ONLY when an independent authority corroborates:
// genuine independent agreement is worth exactly one step, never more,
// and a verdict that is already measured cannot be raised further.
func promote(g EvidenceGrade) EvidenceGrade {
	if g >= GradeMeasured {
		return GradeMeasured
	}
	return g + 1
}

// basisForIndependence reports what consulting a verifier of this
// independence class was actually WORTH.
//
// This exists so the audit trail never mislabels a real consultation.
// An independent authority that returns needs_human DID contribute
// something — the finding that an independent second opinion also
// cannot decide is itself informative. Filing that under
// "correlated-repeat" would be a false marker in the one field whose
// whole purpose is to separate evidence from noise.
func basisForIndependence(i Independence) Basis {
	switch i {
	case IndependenceIndependent:
		return BasisIndependentEvidence
	case IndependenceCorrelated:
		return BasisCorrelatedRepeat
	default:
		return BasisNoEvidence
	}
}

// String renders the arbitration for the operator. Kept plain-text and
// line-oriented so it survives grep and diff.
func (a Arbitration) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "arbitration  : %s (%s)\n", a.Resolution, a.Class)
	fmt.Fprintf(&b, "  basis     : %s\n", a.Basis)
	fmt.Fprintf(&b, "  evidence  : %s (trusted=%t)\n", a.Grade, a.Grade.Trusted())
	fmt.Fprintf(&b, "  claims    : original=%s verifier=%s independence=%s\n",
		a.Original, a.Verifier, a.Independence)
	fmt.Fprintf(&b, "  reason    : %s\n", a.Reason)
	fmt.Fprintf(&b, "  operator  : may override (advisory, not binding)\n")
	return b.String()
}

// Claim exports the arbitration as a Claim so it flows through the same
// L11.1 evidence surface as every other operator-facing statement. A
// dispute that bypasses the grade system would defeat L12.1's purpose.
func (a Arbitration) Claim() Claim {
	return Claim{
		Label:  "drift dispute",
		Value:  fmt.Sprintf("%s (%s)", a.Resolution, a.Class),
		Grade:  a.Grade,
		Source: "arbitration_model(L12.1)",
		Caveat: a.Reason,
	}
}

// Arbitrate adjudicates a disputed drift verdict.
//
// original / verifier are the two authorities; origAuthority /
// verAuthority are their claims; origGrade is the evidence grade the
// original verdict arrived with; alleged is the operator's assertion
// that a false positive occurred.
//
// The rule order below is load-bearing. Each rule is a precondition for
// the next, and moving them would let a null experiment masquerade as
// a finding.
func Arbitrate(
	original JudgeSource,
	origAuthority Authority,
	origGrade EvidenceGrade,
	verifier JudgeSource,
	verAuthority Authority,
	alleged bool,
) Arbitration {
	a := Arbitration{
		Alleged:             alleged,
		Independence:        IndependenceNone,
		Original:            origAuthority,
		Verifier:            verAuthority,
		OriginalGrade:       origGrade,
		Class:               ClassNone,
		Resolution:          ResolutionUpholdVerdict,
		Basis:               BasisNoEvidence,
		Grade:               GradeUnverified,
		OperatorMayOverride: true,
	}

	// Rule D — the unexamined default. Nobody disputed the verdict, so
	// it stands untouched. But standing is not the same as being
	// checked, and we label it GradeUnverified so that no reader can
	// mistake an unchallenged verdict for a verified one.
	if !alleged {
		a.Reason = "no disagreement alleged; the original verdict stands unexamined, not verified"
		return a
	}

	a.Independence = ClassifyIndependence(original, verifier)

	// Rule 1 — nothing to arbitrate. A non-decisive original cannot be
	// overturned, because it never asserted anything.
	if !origAuthority.decisive() {
		a.Class = ClassUnresolved
		a.Resolution = ResolutionAbstain
		a.Basis = basisForIndependence(a.Independence)
		a.Grade = GradeUnverified
		a.Reason = fmt.Sprintf(
			"the original verdict was %s, which decides nothing; there is no verdict to overturn",
			origAuthority)
		return a
	}

	// Rule 2 — THE HEADLINE. Re-running the same authority is a null
	// experiment. This is the exact call G19 makes today, and this is
	// why L12.1 exists.
	if a.Independence == IndependenceNone {
		a.Class = ClassUnresolved
		a.Resolution = ResolutionAbstain
		a.Basis = BasisCorrelatedRepeat
		a.Grade = GradeUnverified
		a.Reason = fmt.Sprintf(
			"verifier %s is the same authority as the original judge; re-running one judge cannot test its own error (Panickssery et al. 2024, arXiv:2404.13076) — independent verification required",
			original)
		return a
	}

	// Rule 3 — symmetry. A non-decisive verifier may neither confirm nor
	// overturn. This is the row 1985 guard, and it points both ways.
	if !verAuthority.decisive() {
		a.Class = ClassUnresolved
		a.Resolution = ResolutionAbstain
		a.Basis = basisForIndependence(a.Independence)
		a.Grade = GradeUnverified
		if a.Independence == IndependenceIndependent {
			// Worth stating precisely: an independent authority was
			// consulted and it declined to decide. That is a real
			// consultation, and it mildly strengthens the case for
			// abstaining — but it still overturns nothing.
			a.Reason = fmt.Sprintf(
				"independent authority %s also returned %s; a decisive verdict plus an independent abstention is no basis to overturn, and the question looks genuinely contested",
				verifier, verAuthority)
			return a
		}
		a.Reason = fmt.Sprintf(
			"verifier returned %s, which decides nothing; a non-decisive verdict can neither confirm nor overturn, in either direction",
			verAuthority)
		return a
	}

	// Rule 4 — correlated authorities. Agreement is corroboration with
	// no independence behind it, so the grade is held exactly flat
	// (idempotent). Disagreement is uninformative, because correlated
	// error and genuine signal are indistinguishable.
	if a.Independence == IndependenceCorrelated {
		a.Basis = BasisCorrelatedRepeat
		if origAuthority == verAuthority {
			a.Class = ClassNone
			a.Resolution = ResolutionUpholdVerdict
			a.Grade = origGrade // IDEMPOTENT — never upgraded.
			a.Reason = fmt.Sprintf(
				"correlated re-judge agreed, but %s shares its model or provider with the original; corroboration from a correlated authority cannot raise the evidence grade",
				verifier)
			return a
		}
		a.Class = ClassUnresolved
		a.Resolution = ResolutionAbstain
		a.Grade = GradeUnverified
		a.Reason = fmt.Sprintf(
			"correlated authorities disagree (%s vs %s) but %s shares its model or provider with the original judge; correlated error is indistinguishable from genuine signal, so the disagreement is uninformative",
			origAuthority, verAuthority, verifier)
		return a
	}

	// Rule 5 — INDEPENDENT. The only path in this file that can change
	// a verdict.
	a.Basis = BasisIndependentEvidence
	if origAuthority == verAuthority {
		a.Class = ClassNone
		a.Resolution = ResolutionUpholdVerdict
		a.Grade = promote(origGrade)
		a.Reason = fmt.Sprintf(
			"independent authority %s corroborated the verdict; the original grade is promoted one step to %s",
			verifier, promote(origGrade))
		return a
	}
	// Genuine disagreement between independent authorities. The original
	// verdict is replaced either way; Class records which party erred.
	a.Resolution = ResolutionOverturnVerdict
	a.Grade = GradeMeasured
	if verAuthority == AuthorityDrift {
		a.Class = ClassArtifactError
		a.Reason = fmt.Sprintf(
			"independent authority %s found drift that the original judge missed; the verdict is overturned and the artifact is at fault",
			verifier)
		return a
	}
	a.Class = ClassJudgeError
	a.Reason = fmt.Sprintf(
		"independent authority %s found no drift; the original verdict is overturned and the judge mis-graded (row 1985 pattern)",
		verifier)
	return a
}

// ArbitrateByOperator records an operator decision on a disputed verdict.
//
// Principle C, stated in code: the operator OWNS this system, so their
// call STANDS and Resolution is never Abstain on operator authority. But
// authority is not verification. The result is therefore labeled
// BasisOperatorAuthority at GradeUnverified, so that six months later a
// reader can see plainly:
//
//	"the operator decided this, and nobody independently checked it"
//
// instead of a bare override that reads like a clean verification. This
// is the difference between respecting the operator and laundering
// their word into evidence.
func ArbitrateByOperator(origAuthority Authority, class DisagreementClass) Arbitration {
	a := Arbitration{
		Alleged:             true,
		Independence:        IndependenceNone,
		Original:            origAuthority,
		Verifier:            AuthorityUnknown,
		OriginalGrade:       GradeUnverified,
		Class:               class,
		Resolution:          ResolutionUpholdVerdict,
		Basis:               BasisOperatorAuthority,
		Grade:               GradeUnverified,
		OperatorMayOverride: true,
	}
	// Resolution follows the MISMATCH between what the verdict claimed
	// and who the operator says erred. Deriving it rather than mapping
	// class→resolution blindly is what keeps ClassArtifactError honest:
	// "the artifact is at fault" upholds a drift verdict and overturns an
	// aligned one, and those are opposite actions.
	switch class {
	case ClassJudgeError:
		// The judge's grading is what was wrong. The verdict does not
		// stand. This is the row 1985 resolution.
		a.Resolution = ResolutionOverturnVerdict
	case ClassArtifactError:
		// The artifact is at fault. If the verdict already said so, it
		// stands; if it did not, the verdict is replaced.
		if origAuthority == AuthorityDrift {
			a.Resolution = ResolutionUpholdVerdict
		} else {
			a.Resolution = ResolutionOverturnVerdict
		}
	case ClassSpecAmbiguity:
		// Neither party erred. The spec is underspecified and that is
		// the thing to fix.
		a.Resolution = ResolutionAmendSpec
	default:
		// ClassNone, ClassOperatorError, ClassUnresolved: the original
		// verdict is left standing.
		a.Resolution = ResolutionUpholdVerdict
	}
	a.Reason = fmt.Sprintf(
		"the operator ruled the original %s verdict %s by authority; this decision is in force but carries no independent verification, so its evidence grade stays unverified",
		origAuthority, class)
	return a
}
