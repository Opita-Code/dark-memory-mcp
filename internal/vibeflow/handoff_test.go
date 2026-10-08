package vibeflow

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// L13.1 fixtures
// ---------------------------------------------------------------------------

// parentJudge is the orchestrating agent: MiniMax-M3 via minimax.
func parentJudge() JudgeSource {
	return JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", EvalType: "drift_judge", Origin: OriginSelf}
}

// childA is a sub-agent on a DIFFERENT model and provider. This is the
// genuinely independent delegate — the thing that makes convergence
// worth anything.
func childA() JudgeSource {
	return JudgeSource{ProviderID: "judge-deepseek", ModelRev: "deepseek-v4", Origin: OriginSubagent}
}

// childSameModel is a sub-agent on the parent's model. Spawning it twice
// and counting the agreement is exactly the laundering L13.1 forbids.
func childSameModel() JudgeSource {
	return JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSubagent}
}

// childUnknownOrigin is a sub-agent whose origin was never recorded.
func childUnknownOrigin() JudgeSource {
	return JudgeSource{ProviderID: "judge-deepseek", ModelRev: "deepseek-v4"}
}

// finding builds a graded finding. id is the unique finding identity;
// claim is the subject several findings can share so convergence has
// something to operate on.
func finding(id, claim string, src JudgeSource, grade EvidenceGrade, summary string) Finding {
	f := Finding{
		ID: id, Claim: claim, Summary: summary, Grade: grade,
		Source: "agent_memory_recall", Origin: src.Origin,
		Delegate: src,
	}
	if grade != GradeMeasured {
		f.Caveat = "modeled over documented assumptions, not observed"
	}
	return f
}

// oneClaim builds n findings from the same delegate, all addressing the
// same claim. It is the canonical "panel" fixture.
func oneClaim(src JudgeSource, n int, grade EvidenceGrade, summary string) []Finding {
	out := make([]Finding, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, finding(fmt.Sprintf("f%d", i), "claim-parser", src, grade, summary))
	}
	return out
}

// ---------------------------------------------------------------------------
// Origin — the third axis
// ---------------------------------------------------------------------------

func TestOrigin_String(t *testing.T) {
	cases := map[Origin]string{
		OriginUnspecified: "<unspecified>",
		OriginSelf:        "self",
		OriginSubagent:    "subagent",
		OriginOperator:    "operator",
		OriginExternal:    "external",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Origin(%q).String() = %q, want %q", string(o), got, want)
		}
	}
}

func TestOrigin_KnownOnlyWhenRecorded(t *testing.T) {
	if OriginUnspecified.known() {
		t.Error("the zero value must not claim a known origin")
	}
	for _, o := range []Origin{OriginSelf, OriginSubagent, OriginOperator, OriginExternal} {
		if !o.known() {
			t.Errorf("%s must be known", o)
		}
	}
}

// THE BACKWARD-COMPATIBILITY CLAIM, PINNED MECHANICALLY.
// L12.1 shipped with a two-axis ladder. Adding Origin must not have
// changed any classification for callers that leave it unset. This is the
// test that makes "not a breaking change" a fact rather than a comment.
func TestL13_BackCompat(t *testing.T) {
	cases := []struct {
		name string
		a, b JudgeSource
		want Independence
	}{
		{"identical authority", legacy("chat-minimax-cn", "MiniMax-M3"), legacy("chat-minimax-cn", "MiniMax-M3"), IndependenceNone},
		{"both unspecified", JudgeSource{}, JudgeSource{}, IndependenceNone},
		{"same model other provider", legacy("chat-minimax-cn", "MiniMax-M3"), legacy("judge-openrouter", "MiniMax-M3"), IndependenceCorrelated},
		{"same provider other model", legacy("chat-minimax-cn", "MiniMax-M3"), legacy("chat-minimax-cn", "abab6.5s-chat"), IndependenceCorrelated},
		{"both differ", legacy("chat-minimax-cn", "MiniMax-M3"), legacy("judge-deepseek", "deepseek-v4"), IndependenceIndependent},
		{"partial identity", legacy("chat-minimax-cn", "MiniMax-M3"), JudgeSource{ProviderID: "judge-x"}, IndependenceCorrelated},
		{"one unspecified", JudgeSource{}, legacy("chat-minimax-cn", "MiniMax-M3"), IndependenceCorrelated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyIndependence(tc.a, tc.b); got != tc.want {
				t.Errorf("with Origin unset, ClassifyIndependence = %s, want %s (L12.1 behavior must be unchanged)", got, tc.want)
			}
		})
	}
}

func legacy(provider, model string) JudgeSource {
	return JudgeSource{ProviderID: provider, ModelRev: model}
}

// The origin axis can only DEGRADE. It can never manufacture
// independence. This is the core L13.1 claim.
func TestClassifyIndependence_OriginNeverCreatesIndependence(t *testing.T) {
	// Same origin but different model AND provider: L12.1 would have
	// called this Independent. With origin recorded on both sides it is
	// downgraded, because two claims both attributed to the same origin
	// cannot be shown to come from different minds.
	a := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSelf}
	b := JudgeSource{ProviderID: "judge-deepseek", ModelRev: "deepseek-v4", Origin: OriginSelf}
	if got := ClassifyIndependence(a, b); got != IndependenceCorrelated {
		t.Errorf("same origin must downgrade to correlated, got %s", got)
	}
}

// Origin diversity is necessary but NOT sufficient — the model ladder
// still governs.
func TestClassifyIndependence_OriginDiversityAloneIsNotEnough(t *testing.T) {
	a := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSelf}
	b := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSubagent}
	if got := ClassifyIndependence(a, b); got != IndependenceCorrelated {
		t.Errorf("different origins on the same model must remain correlated, got %s", got)
	}
	if got := ClassifyIndependence(a, b); got == IndependenceIndependent {
		t.Error("origin diversity must never reach independent on a shared model")
	}
}

// A half-recorded origin cannot prove difference. Conservative, matching
// the missing-model rule.
func TestClassifyIndependence_PartiallyRecordedOriginIsConservative(t *testing.T) {
	a := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSubagent}
	b := legacy("judge-deepseek", "deepseek-v4")
	if got := ClassifyIndependence(a, b); got != IndependenceCorrelated {
		t.Errorf("an unrecorded origin cannot establish difference; got %s, want correlated", got)
	}
}

// The case that motivates the whole loop: a parent judge evaluating a
// sub-agent's artifact IS more independent than one evaluating its own.
func TestClassifyIndependence_SubagentArtifactIsMoreIndependentThanSelf(t *testing.T) {
	selfJudged := ClassifyIndependence(
		JudgeSource{ProviderID: "judge-x", ModelRev: "m1", Origin: OriginSelf},
		JudgeSource{ProviderID: "judge-x", ModelRev: "m1", Origin: OriginSelf})
	delegated := ClassifyIndependence(
		JudgeSource{ProviderID: "judge-x", ModelRev: "m1", Origin: OriginSelf},
		JudgeSource{ProviderID: "judge-x", ModelRev: "m1", Origin: OriginSubagent})
	if delegated <= selfJudged {
		t.Errorf("a sub-agent artifact must be strictly more independent than a self-judged one: self=%s delegated=%s", selfJudged, delegated)
	}
}

func TestJudgeSource_StringIncludesOrigin(t *testing.T) {
	withOrigin := parentJudge().String()
	if !strings.Contains(withOrigin, "self") {
		t.Errorf("String() must expose the origin; got %q", withOrigin)
	}
	if got := legacy("chat-minimax-cn", "MiniMax-M3").String(); strings.Contains(got, "[") {
		t.Errorf("unspecified origin must not add a bracket; got %q", got)
	}
}

// ---------------------------------------------------------------------------
// HandoffSpec — the contract
// ---------------------------------------------------------------------------

func validSpec() HandoffSpec {
	return HandoffSpec{
		SubagentID: "sub-001",
		Task:       "audit the parser for injection risk",
		Findings: []Finding{
			finding("f1", "claim-parser", childA(), GradeMeasured, "validator rejects nested AST"),
			finding("f2", "claim-parser", parentJudge(), GradeModeled, "block sizes imply ~3.6k tokens"),
		},
	}
}

func TestHandoffSpec_ValidSpecPasses(t *testing.T) {
	if err := validSpec().Errs(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestHandoffSpec_RequiresIdentityAndTask(t *testing.T) {
	s := validSpec()
	s.SubagentID = "  "
	s.Task = ""
	errs := s.Validate()
	if len(errs) != 2 {
		t.Fatalf("want 2 errors, got %d: %v", len(errs), errs)
	}
}

// PRINCIPLE A, mechanically. A weaker claim cannot cross the boundary
// uncaveated. This mirrors the L11.1 Claim invariant so changing
// container cannot launder a claim.
func TestHandoffSpec_RejectsUncaveatedWeakFinding(t *testing.T) {
	s := validSpec()
	bad := finding("f3", "claim-parser", childA(), GradeModeled, "a modeled claim")
	bad.Caveat = ""
	s.Findings = append(s.Findings, bad)

	errs := s.Validate()
	if len(errs) != 1 {
		t.Fatalf("want exactly 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "no caveat") {
		t.Errorf("error must name the caveat obligation; got %q", errs[0])
	}
}

// Measured findings are exempt from the caveat rule, or the invariant
// would be unfalsifiable in the other direction.
func TestHandoffSpec_MeasuredNeedsNoCaveat(t *testing.T) {
	f := finding("f1", "claim-parser", childA(), GradeMeasured, "vendor-verified")
	f.Caveat = ""
	s := HandoffSpec{SubagentID: "s", Task: "t", Findings: []Finding{f}}
	if err := s.Errs(); err != nil {
		t.Errorf("a measured finding must be allowed without a caveat: %v", err)
	}
}

func TestHandoffSpec_RejectsDuplicateIDs(t *testing.T) {
	s := validSpec()
	dup := finding("f1", "claim-parser", childA(), GradeMeasured, "same id, different claim")
	s.Findings = append(s.Findings, dup)
	if !strings.Contains(strings.Join(s.Validate(), " "), "duplicate id") {
		t.Errorf("duplicate ids must be rejected; got %v", s.Validate())
	}
}

// PRINCIPLE D, mechanically. Only UNRESOLVED disputes travel. Shipping a
// settled dispute misinforms the child; dropping one launders it.
func TestHandoffSpec_OnlyUnresolvedDisputesTravel(t *testing.T) {
	settled := Arbitrate(parentJudge(), AuthorityDrift, GradeModeled, childA(), AuthorityDrift, true)
	if settled.Resolution == ResolutionAbstain {
		t.Fatal("fixture precondition: this arbitration should be settled")
	}
	s := validSpec()
	s.Disputes = []Arbitration{settled}
	if err := s.Errs(); err == nil {
		t.Fatal("a settled dispute must not travel in a handoff")
	}

	open := Arbitrate(parentJudge(), AuthorityDrift, GradeModeled, parentJudge(), AuthorityDrift, true)
	if open.Resolution != ResolutionAbstain {
		t.Fatalf("fixture precondition: expected an open dispute, got %s", open.Resolution)
	}
	s.Disputes = []Arbitration{open}
	if err := s.Errs(); err != nil {
		t.Errorf("an unresolved dispute must be allowed to travel: %v", err)
	}
}

// The carried dispute must retain its basis, so the child can see the
// claim is NOT settled. Losing the basis is the laundering path.
func TestHandoffSpec_CarriedDisputeKeepsItsBasis(t *testing.T) {
	open := Arbitrate(parentJudge(), AuthorityDrift, GradeModeled,
		parentJudge(), AuthorityDrift, true)
	if open.Basis != BasisCorrelatedRepeat {
		t.Fatalf("precondition: expected correlated-repeat basis, got %s", open.Basis)
	}
	s := HandoffSpec{SubagentID: "s", Task: "t", Disputes: []Arbitration{open}}
	if err := s.Errs(); err != nil {
		t.Fatalf("open dispute should travel: %v", err)
	}
	if s.Disputes[0].Basis != BasisCorrelatedRepeat {
		t.Error("the basis must survive the handoff so the child sees it is unverified")
	}
}

func TestHandoffSpec_GradedFindingsFlowThroughL11Claims(t *testing.T) {
	claims := validSpec().GradedFindings()
	if len(claims) != 2 {
		t.Fatalf("want 2 claims, got %d", len(claims))
	}
	for _, c := range claims {
		if c.Source == "" || c.Value == "" {
			t.Errorf("claim must carry source and value: %+v", c)
		}
		if c.Grade != GradeMeasured && c.Caveat == "" {
			t.Errorf("non-measured claim must carry a caveat: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// ComputeConvergence — PRINCIPLE C, the anti-voting property
// ---------------------------------------------------------------------------

// THE headline property. Headcount must not move the grade. One delegate
// and one hundred delegates on one model are worth the same.
func TestConvergence_HeadcountIsNotEvidence(t *testing.T) {
	for _, n := range []int{2, 3, 5, 10, 50, 100} {
		// Same model, same provider, same origin: one mind sampled n times.
		src := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSubagent}
		fs := oneClaim(src, n, GradeModeled, "the parser has a hole")
		c := ComputeConvergence(fs, GradeModeled)
		if c.Count != n {
			t.Fatalf("count should be reported verbatim; got %d want %d", c.Count, n)
		}
		if c.Grade != GradeModeled {
			t.Fatalf("n=%d correlated panel must hold the grade flat, got %s", n, c.Grade)
		}
		if c.Basis != BasisCorrelatedRepeat {
			t.Fatalf("n=%d must be BasisCorrelatedRepeat, got %s", n, c.Basis)
		}
	}
}

// Corollary: adding a 100th identical delegate cannot promote.
func TestConvergence_CorrelatedPanelNeverPromotes(t *testing.T) {
	for _, in := range []EvidenceGrade{GradeUnverified, GradeEstimated, GradeModeled} {
		src := JudgeSource{ProviderID: "judge-openrouter", ModelRev: "MiniMax-M3", Origin: OriginSubagent}
		fs := []Finding{finding("a", "claim-parser", src, in, "x"), finding("b", "claim-parser", src, in, "x")}
		c := ComputeConvergence(fs, in)
		if c.Grade != in {
			t.Errorf("incoming %s must be held flat, got %s", in, c.Grade)
		}
	}
}

// A genuinely independent pair promotes exactly one step — the same
// promote() L12.1 uses.
func TestConvergence_IndependentPairPromotesOneStep(t *testing.T) {
	cases := []struct{ in, want EvidenceGrade }{
		{GradeUnverified, GradeEstimated},
		{GradeEstimated, GradeModeled},
		{GradeModeled, GradeMeasured},
		{GradeMeasured, GradeMeasured}, // capped
	}
	for _, tc := range cases {
		fs := []Finding{
			finding("a", "claim-parser", parentJudge(), tc.in, "shared conclusion"),
			finding("b", "claim-parser", childA(), tc.in, "shared conclusion"),
		}
		c := ComputeConvergence(fs, tc.in)
		if c.Resolution != ResolutionUpholdVerdict {
			t.Errorf("%s: agreement should uphold; got %s", tc.in, c.Resolution)
		}
		if c.Basis != BasisIndependentEvidence {
			t.Errorf("%s: basis should be independent; got %s", tc.in, c.Basis)
		}
		if c.Grade != tc.want {
			t.Errorf("%s: want grade %s, got %s", tc.in, tc.want, c.Grade)
		}
	}
}

// PRINCIPLE B at the set level: origin diversity is visible and
// counted, and it participates in the verdict.
func TestConvergence_OriginDiversityIsCounted(t *testing.T) {
	fs := []Finding{
		finding("a", "claim-parser", parentJudge(), GradeModeled, "x"),
		finding("b", "claim-parser", childA(), GradeModeled, "x"),
	}
	c := ComputeConvergence(fs, GradeModeled)
	if c.DistinctOrigins != 2 {
		t.Errorf("want 2 distinct origins, got %d", c.DistinctOrigins)
	}
	if c.DistinctModels != 2 {
		t.Errorf("want 2 distinct models, got %d", c.DistinctModels)
	}
	// Unrecorded origins must not be counted as diversity.
	same := ComputeConvergence([]Finding{
		finding("a", "claim-parser", JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3"}, GradeModeled, "x"),
		finding("b", "claim-parser", JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3"}, GradeModeled, "x"),
	}, GradeModeled)
	if same.DistinctOrigins != 0 {
		t.Errorf("unrecorded origins must count as zero diversity, got %d", same.DistinctOrigins)
	}
}

// An empty panel is not a unanimous one. Zero evidence must not be the
// strongest evidence.
func TestConvergence_EmptyPanelIsInsufficient(t *testing.T) {
	c := ComputeConvergence(nil, GradeModeled)
	if c.Resolution != ResolutionAbstain {
		t.Errorf("empty panel must abstain, got %s", c.Resolution)
	}
	if c.Grade != GradeUnverified {
		t.Errorf("empty panel must be GradeUnverified, got %s", c.Grade)
	}
	if c.SameClaim {
		t.Error("an empty panel must not satisfy the same-claim precondition")
	}
}

// REGRESSION for the original bug in ComputeConvergence. It compared
// finding IDs, treated distinct IDs as AGREEMENT, and therefore promoted
// a panel whose members had reported opposite things — the exact
// laundering this loop exists to prevent. A panel spanning several
// claims corroborates none of them.
func TestConvergence_MultipleClaimsCannotCorroborate(t *testing.T) {
	// Two independent agents, opposite conclusions, distinct claim ids.
	// Before the fix this PROMOTED to GradeMeasured.
	fs := []Finding{
		finding("claim-A", "claim-A-is-safe", parentJudge(), GradeModeled, "no hole in the parser"),
		finding("claim-B", "claim-B-unsafe", childA(), GradeMeasured, "there IS a hole in the parser"),
	}
	c := ComputeConvergence(fs, GradeModeled)
	if c.SameClaim {
		t.Error("a panel spanning two claims must not satisfy the same-claim precondition")
	}
	if c.Resolution != ResolutionAbstain {
		t.Errorf("multi-claim panel must abstain, got %s", c.Resolution)
	}
	if c.Grade >= GradeMeasured {
		t.Errorf("a multi-claim panel must never promote; got %s", c.Grade)
	}
	if !strings.Contains(c.Reason, "Arbitrate") {
		t.Errorf("reason must point at escalation; got %q", c.Reason)
	}
}

// This function deliberately does not verify semantic agreement. Saying so
// in a test keeps the limitation from being quietly forgotten when
// someone later assumes "convergence" means the panel agreed.
func TestConvergence_DoesNotClaimSemanticAgreement(t *testing.T) {
	// One claim, one agent, two findings that literally contradict
	// each other. The function cannot detect that, and must not pretend.
	fs := []Finding{
		{ID: "same", Claim: "claim-parser", Summary: "the parser is safe", Grade: GradeMeasured,
			Source: "probe", Origin: OriginSubagent, Delegate: childA()},
		{ID: "same", Claim: "claim-parser", Summary: "the parser is NOT safe", Grade: GradeMeasured,
			Source: "probe", Origin: OriginSubagent, Delegate: childA()},
	}
	c := ComputeConvergence(fs, GradeMeasured)
	if !c.SameClaim {
		t.Fatal("precondition: both findings address the same claim id")
	}
	// Same agent twice => correlated => grade held flat. The panel cannot
	// promote regardless of what the summaries say.
	if c.Grade != GradeMeasured {
		t.Errorf("grade must be held flat by the correlated panel, got %s", c.Grade)
	}
	// The audit reason must not claim the findings were verified as
	// agreeing.
	if strings.Contains(strings.ToLower(c.Reason), "agree") {
		t.Errorf("reason must not assert semantic agreement this function never checked; got %q", c.Reason)
	}
}

// A single delegate has no independent counterpart. Saying so plainly
// beats letting it read as consensus.
func TestConvergence_SingleDelegateIsUncorroborated(t *testing.T) {
	c := ComputeConvergence([]Finding{finding("a", "claim-parser", parentJudge(), GradeModeled, "x")}, GradeModeled)
	if c.Basis != BasisCorrelatedRepeat {
		t.Errorf("single delegate must be BasisCorrelatedRepeat, got %s", c.Basis)
	}
	if c.Grade != GradeModeled {
		t.Errorf("single delegate must hold the grade flat, got %s", c.Grade)
	}
	if !strings.Contains(c.Reason, "no independent counterpart") {
		t.Errorf("reason must say why; got %q", c.Reason)
	}
}

func TestConvergence_StringIsAuditable(t *testing.T) {
	c := ComputeConvergence([]Finding{
		finding("a", "claim-parser", parentJudge(), GradeModeled, "x"),
		finding("b", "claim-parser", childA(), GradeModeled, "x"),
	}, GradeModeled)
	s := c.String()
	for _, want := range []string{"convergence", "basis", "grade", "diversity", "models", "providers", "origins", "reason"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
	// The rendering must not assert an agreement this function never
	// checked. An audit line claiming "agreeing findings" would launder
	// a semantic judgment into a mechanical result.
	if strings.Contains(strings.ToLower(s), "agree") {
		t.Errorf("String() must not claim semantic agreement; got:\n%s", s)
	}
	if !strings.Contains(s, "one claim") {
		t.Errorf("String() must describe the panel as one claim; got:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// Determinism + invariants
// ---------------------------------------------------------------------------

func TestComputeConvergence_Deterministic(t *testing.T) {
	fs := []Finding{
		finding("a", "claim-parser", parentJudge(), GradeModeled, "x"),
		finding("b", "claim-parser", childA(), GradeModeled, "x"),
		finding("c", "claim-parser", legacy("chat-minimax-cn", "MiniMax-M3"), GradeModeled, "x"),
	}
	first := ComputeConvergence(fs, GradeModeled)
	for i := 0; i < 50; i++ {
		if got := ComputeConvergence(fs, GradeModeled); got != first {
			t.Fatalf("diverged at iteration %d:\n%+v\n%+v", i, got, first)
		}
	}
}

func TestComputeConvergence_ReasonNeverEmpty(t *testing.T) {
	sources := []JudgeSource{parentJudge(), childA(), childSameModel(), childUnknownOrigin(), {}}
	for _, s1 := range sources {
		for _, s2 := range sources {
			for _, n := range []int{0, 1, 2} {
				var fs []Finding
				for i := 0; i < n; i++ {
					_ = i
				}
				fs = oneClaim(s1, n, GradeModeled, "x")
				if n == 2 {
					fs = append(fs, finding("g", "claim-parser", s2, GradeModeled, "y"))
				}
				c := ComputeConvergence(fs, GradeModeled)
				if c.Reason == "" {
					t.Fatalf("reason must never be empty: %+v", c)
				}
				if c.Grade > GradeMeasured {
					t.Fatalf("grade escaped the ceiling: %+v", c)
				}
				if c.Grade > GradeModeled && c.Basis != BasisIndependentEvidence {
					t.Fatalf("grade rose without independent evidence: %+v", c)
				}
			}
		}
	}
}

// Parallel-safe: pure functions, but the race detector is part of the
// ship gate, so prove it rather than assume it.
func TestL13_ParallelSafe(t *testing.T) {
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				ComputeConvergence([]Finding{
					finding("a", "claim-parser", parentJudge(), GradeModeled, "x"),
					finding("b", "claim-parser", childA(), GradeModeled, "x"),
				}, GradeModeled)
				validSpec().Validate()
				ClassifyIndependence(parentJudge(), childA())
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// Documentation example — the laundering path, end to end
// ---------------------------------------------------------------------------

func TestExample_Convergence_TheG19TrapInADifferentHat(t *testing.T) {
	// The temptation: spawn five sub-agents, they all report the same
	// thing, therefore it is confirmed.
	same := JudgeSource{ProviderID: "chat-minimax-cn", ModelRev: "MiniMax-M3", Origin: OriginSubagent}
	panel := oneClaim(same, 5, GradeModeled, "the parser accepts nested AST")
	correlated := ComputeConvergence(panel, GradeModeled)

	// The honest alternative: one delegate on the same model, one on a
	// genuinely different model and provider.
	diverse := ComputeConvergence([]Finding{
		finding("probe-a", "claim-parser", parentJudge(), GradeModeled, "the parser accepts nested AST"),
		finding("probe-b", "claim-parser", childA(), GradeModeled, "the parser accepts nested AST"),
	}, GradeModeled)

	t.Log("five sub-agents, one model:\n" + correlated.String())
	t.Log("two delegates, genuinely different:\n" + diverse.String())

	if correlated.Grade >= diverse.Grade {
		t.Errorf("five correlated delegates (%s) must not be worth more than two independent ones (%s)",
			correlated.Grade, diverse.Grade)
	}
	if correlated.Grade != GradeModeled {
		t.Errorf("the correlated panel must hold the grade flat, got %s", correlated.Grade)
	}
	if diverse.Grade != GradeMeasured {
		t.Errorf("the independent pair should have promoted to measured, got %s", diverse.Grade)
	}

	fmt.Println()
	fmt.Println("the anti-voting property, live:")
	fmt.Print("  5 delegates / 1 model  -> " + correlated.String())
	fmt.Print("  2 delegates / 2 models -> " + diverse.String())
}
