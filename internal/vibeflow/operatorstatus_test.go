package vibeflow

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// verifiedPricing is a CostSummary fixture with vendor-verified prices.
func verifiedPricing(totalUSD float64, calls int) CostSummary {
	return CostSummary{
		SessionID:         "sess-test",
		TotalUSD:          totalUSD,
		CallCount:         calls,
		EstimateCount:     0,
		UnknownModelCount: 0,
	}
}

// estimatedPricing has placeholder prices.
func estimatedPricing(totalUSD float64, calls int) CostSummary {
	return CostSummary{
		SessionID:         "sess-test",
		TotalUSD:          totalUSD,
		CallCount:         calls,
		EstimateCount:     calls,
		UnknownModelCount: 0,
	}
}

// baseConfig is a realistic novice-operator config: cheap mode, minimal
// depth, fast tier, self-judge drift.
func baseConfig() StatusConfig {
	return StatusConfig{
		SessionID: "sess-l11",
		Persona:   "novato",
		Mode:      ModeDegraded,
		ModeState: State{
			Mode:        ModeDegraded,
			Elapsed:     95 * time.Second,
			ToolCalls:   2,
			Followups:   1,
			Doubts:      0,
			TokensUsed:  1400,
			Transitions: 1,
		},
		Depth: DepthDecision{
			Depth:  DepthMinimal,
			Base:   1,
			Reason: "base=1 -1(tier:novice) → minimal(1)",
		},
		Tier: TierFast,
		Drift: DriftPolicy{
			Mode:     SkipSelfJudge,
			Reason:   "tier:novice depth<=2 → self-judge",
			EstCalls: 0,
		},
		Cost: verifiedPricing(0.000197, 1),
	}
}

// ---------------------------------------------------------------------------
// EvidenceGrade
// ---------------------------------------------------------------------------

func TestEvidenceGrade_String(t *testing.T) {
	cases := []struct {
		g    EvidenceGrade
		want string
	}{
		{GradeUnverified, "unverified"},
		{GradeEstimated, "estimated"},
		{GradeModeled, "modeled"},
		{GradeMeasured, "measured"},
		{EvidenceGrade(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.g.String(); got != c.want {
			t.Errorf("EvidenceGrade(%d).String() = %q, want %q", int(c.g), got, c.want)
		}
	}
}

func TestEvidenceGrade_OnlyMeasuredIsTrusted(t *testing.T) {
	// Kadavath selective prediction: abstain everywhere but Measured.
	// Modeled is deliberately NOT trusted as fact — it is a plan.
	if GradeMeasured.Trusted() != true {
		t.Error("GradeMeasured must be trusted")
	}
	for _, g := range []EvidenceGrade{GradeUnverified, GradeEstimated, GradeModeled} {
		if g.Trusted() {
			t.Errorf("%s must NOT be trusted as fact", g)
		}
	}
	if EvidenceGrade(99).Trusted() {
		t.Error("unknown grade must not be trusted")
	}
}

func TestWeaker_PicksLowerGrade(t *testing.T) {
	cases := []struct{ a, b, want EvidenceGrade }{
		{GradeMeasured, GradeModeled, GradeModeled},
		{GradeModeled, GradeMeasured, GradeModeled},
		{GradeEstimated, GradeMeasured, GradeEstimated},
		{GradeUnverified, GradeMeasured, GradeUnverified},
		{GradeModeled, GradeModeled, GradeModeled},
		{GradeEstimated, GradeEstimated, GradeEstimated},
	}
	for _, c := range cases {
		if got := weaker(c.a, c.b); got != c.want {
			t.Errorf("weaker(%s, %s) = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestWeaker_IsCommutative(t *testing.T) {
	all := []EvidenceGrade{GradeUnverified, GradeEstimated, GradeModeled, GradeMeasured}
	for _, a := range all {
		for _, b := range all {
			if weaker(a, b) != weaker(b, a) {
				t.Errorf("weaker is not commutative: weaker(%s,%s) != weaker(%s,%s)", a, b, b, a)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Grading
// ---------------------------------------------------------------------------

func TestGradeCost_ZeroCallsIsUnverified(t *testing.T) {
	if got := GradeCost(CostSummary{}); got != GradeUnverified {
		t.Errorf("empty summary grade = %s, want unverified", got)
	}
}

func TestGradeCost_MeasuredWhenAllPricesVerified(t *testing.T) {
	if got := GradeCost(verifiedPricing(0.001, 3)); got != GradeMeasured {
		t.Errorf("verified summary grade = %s, want measured", got)
	}
}

func TestGradeCost_EstimatedWhenPlaceholderPrices(t *testing.T) {
	if got := GradeCost(estimatedPricing(0.001, 3)); got != GradeEstimated {
		t.Errorf("estimated summary grade = %s, want estimated", got)
	}
}

func TestGradeCost_EstimatedWhenUnknownModels(t *testing.T) {
	// Unknown models are recorded at $0 — the shape is wrong too, not
	// just the dollars. Must NOT grade as measured.
	s := verifiedPricing(0.001, 3)
	s.UnknownModelCount = 1
	if got := GradeCost(s); got != GradeEstimated {
		t.Errorf("unknown-model summary grade = %s, want estimated", got)
	}
}

func TestGradeEconomy_NeverMeasured(t *testing.T) {
	// The economy model is a projection by construction. It must never
	// claim to be measured, for any tier.
	for _, tier := range []ModelTier{TierFast, TierBalanced, TierStrong} {
		d := ComputeEconomyDelta(DepthLight, tier, SkipSingle)
		g := GradeEconomy(d, tier)
		if g == GradeMeasured {
			t.Errorf("tier %s: economy delta must never grade as measured", tier)
		}
		if g != GradeModeled && g != GradeEstimated {
			t.Errorf("tier %s: economy grade = %s, want modeled or estimated", tier, g)
		}
	}
}

// ---------------------------------------------------------------------------
// THE ANTI-HALLUCINATION GUARD
// ---------------------------------------------------------------------------

func TestComputeVerdict_NoCallsNeverClaimsHelping(t *testing.T) {
	// THE most important test in this file. A system that has never
	// run cannot claim it saved anything — regardless of how good the
	// modeled projection looks.
	for _, d := range []Depth{DepthSkip, DepthMinimal, DepthLight, DepthStandard, DepthFull, DepthExhaustive} {
		for _, tier := range []ModelTier{TierFast, TierBalanced, TierStrong} {
			for _, drift := range []DriftSkip{SkipNoDrift, SkipSelfJudge, SkipSingle, SkipMulti, SkipConsensus} {
				delta := ComputeEconomyDelta(d, tier, drift)
				v, grade := ComputeVerdict(CostSummary{}, delta)

				if v != VerdictInsufficientData {
					t.Errorf("depth=%s tier=%s drift=%s: no calls → verdict %s, want insufficient-data", d, tier, drift, v)
				}
				if grade != GradeUnverified {
					t.Errorf("depth=%s tier=%s drift=%s: no calls → grade %s, want unverified", d, tier, drift, grade)
				}
			}
		}
	}
}

func TestComputeVerdict_ZeroCallsEvenWhenSavingsAreHuge(t *testing.T) {
	// Construct the most flattering possible delta, then supply no
	// data. The guard must still hold.
	delta := ComputeEconomyDelta(DepthMinimal, TierFast, SkipNoDrift)
	if delta.SavedUSDPct < 50 {
		t.Fatalf("fixture precondition: expected large modeled saving, got %.1f%%", delta.SavedUSDPct)
	}
	v, _ := ComputeVerdict(CostSummary{}, delta)
	if v != VerdictInsufficientData {
		t.Errorf("verdict = %s, want insufficient-data despite %.1f%% modeled saving", v, delta.SavedUSDPct)
	}
}

// ---------------------------------------------------------------------------
// Confidence propagation (Kadavath)
// ---------------------------------------------------------------------------

func TestComputeVerdict_VerdictNeverExceedsModeled(t *testing.T) {
	// Measured spend compared against a MODELED baseline can never be
	// stronger than "modeled". This is the propagation rule.
	delta := ComputeEconomyDelta(DepthMinimal, TierFast, SkipNoDrift)
	_, grade := ComputeVerdict(verifiedPricing(0.0001, 1), delta)
	if grade == GradeMeasured {
		t.Error("verdict must not grade as measured — the baseline is modeled")
	}
	if grade != GradeModeled {
		t.Errorf("verdict grade = %s, want modeled", grade)
	}
}

func TestComputeVerdict_PlaceholderPricingDegradesVerdict(t *testing.T) {
	delta := ComputeEconomyDelta(DepthMinimal, TierFast, SkipNoDrift)
	_, grade := ComputeVerdict(estimatedPricing(0.0001, 1), delta)
	if grade != GradeEstimated {
		t.Errorf("verdict grade with placeholder prices = %s, want estimated", grade)
	}
}

func TestComputeVerdict_PropagationNeverUpgrades(t *testing.T) {
	// For every tier/drift combo, the verdict grade must be no
	// stronger than the input cost grade.
	for _, tier := range []ModelTier{TierFast, TierBalanced, TierStrong} {
		for _, drift := range []DriftSkip{SkipNoDrift, SkipSelfJudge, SkipSingle, SkipMulti, SkipConsensus} {
			delta := ComputeEconomyDelta(DepthLight, tier, drift)

			measured := GradeCost(verifiedPricing(0.001, 2))
			v, g := ComputeVerdict(verifiedPricing(0.001, 2), delta)
			if g > measured {
				t.Errorf("tier=%s drift=%s: verdict grade %s upgraded above input %s (verdict=%s)", tier, drift, g, measured, v)
			}

			est := GradeCost(estimatedPricing(0.001, 2))
			_, g2 := ComputeVerdict(estimatedPricing(0.001, 2), delta)
			if g2 > est {
				t.Errorf("tier=%s drift=%s: verdict grade %s upgraded above input %s", tier, drift, g2, est)
			}
		}
	}
}

func TestComputeVerdict_StrongTierVerdictIsEstimated(t *testing.T) {
	// TierStrong's representative pricing is a placeholder, so even a
	// measured spend yields an estimated verdict. This is the honest
	// answer: the dollars depend on a guess.
	delta := ComputeEconomyDelta(DepthLight, TierStrong, SkipMulti)
	v, grade := ComputeVerdict(verifiedPricing(0.0005, 2), delta)
	if grade != GradeEstimated {
		t.Errorf("strong tier verdict grade = %s, want estimated (placeholder pricing)", grade)
	}
	if v == VerdictInsufficientData {
		t.Error("with calls recorded the verdict must not be insufficient-data")
	}
}

// ---------------------------------------------------------------------------
// Verdict semantics
// ---------------------------------------------------------------------------

func TestComputeVerdict_HelpingNeutralHurting(t *testing.T) {
	delta := ComputeEconomyDelta(DepthMinimal, TierFast, SkipNoDrift)
	base := delta.BaselineUSD
	if base <= 0 {
		t.Fatalf("fixture precondition: BaselineUSD = %v, want > 0", base)
	}

	cases := []struct {
		name string
		usd  float64
		want Verdict
	}{
		{"spend well below baseline", base * 0.10, VerdictHelping},
		{"spend exactly at baseline", base, VerdictNeutral},
		{"spend above baseline", base * 1.50, VerdictHurting},
		{"spend hugely above baseline", base * 10, VerdictHurting},
	}
	for _, c := range cases {
		v, _ := ComputeVerdict(verifiedPricing(c.usd, 1), delta)
		if v != c.want {
			t.Errorf("%s: spend=$%.6f baseline=$%.6f → verdict %s, want %s",
				c.name, c.usd, base, v, c.want)
		}
	}
}

func TestComputeVerdict_HurtingIsReportedNotSmoothed(t *testing.T) {
	// Anti-overclaim: a real overspend must surface as hurting, not be
	// rounded to neutral by an epsilon threshold.
	delta := ComputeEconomyDelta(DepthMinimal, TierFast, SkipNoDrift)
	v, _ := ComputeVerdict(verifiedPricing(delta.BaselineUSD+0.000001, 1), delta)
	if v != VerdictHurting {
		t.Errorf("verdict = %s, want hurting for a $0.000001 overspend (no epsilon smoothing)", v)
	}
}

func TestComputeVerdict_HelpingIsReportedNotSmoothed(t *testing.T) {
	delta := ComputeEconomyDelta(DepthMinimal, TierFast, SkipNoDrift)
	v, _ := ComputeVerdict(verifiedPricing(delta.BaselineUSD-0.000001, 1), delta)
	if v != VerdictHelping {
		t.Errorf("verdict = %s, want helping for a $0.000001 saving (no epsilon smoothing)", v)
	}
}

// ---------------------------------------------------------------------------
// BuildOperatorStatus
// ---------------------------------------------------------------------------

func TestBuildOperatorStatus_Deterministic(t *testing.T) {
	cfg := baseConfig()
	a := BuildOperatorStatus(cfg)
	b := BuildOperatorStatus(cfg)
	if a.String() != b.String() {
		t.Error("BuildOperatorStatus must be byte-for-byte deterministic")
	}
	if len(a.Claims) != len(b.Claims) {
		t.Fatal("claim count differs between identical builds")
	}
	for i := range a.Claims {
		if a.Claims[i] != b.Claims[i] {
			t.Errorf("claim %d differs: %+v vs %+v", i, a.Claims[i], b.Claims[i])
		}
	}
}

func TestBuildOperatorStatus_ZeroConfigIsHonest(t *testing.T) {
	// A brand-new session: nothing observed, nothing to claim.
	s := BuildOperatorStatus(StatusConfig{})

	if s.Verdict != VerdictInsufficientData {
		t.Errorf("verdict = %s, want insufficient-data", s.Verdict)
	}
	if s.VerdictGrade != GradeUnverified {
		t.Errorf("verdict grade = %s, want unverified", s.VerdictGrade)
	}
	if s.VerdictGrade.Trusted() {
		t.Error("a zero-data status must never be trusted as fact")
	}
	if !strings.Contains(s.String(), "insufficient-data") {
		t.Error("rendered output must state insufficient-data")
	}
	if !strings.Contains(s.String(), "no calls recorded") {
		t.Error("spend claim must disclose that no calls were recorded")
	}
}

func TestBuildOperatorStatus_EconomyDerivedFromDecisions(t *testing.T) {
	// The economy delta must be derived from the decisions actually
	// made, not hardcoded — so changing depth must change the delta.
	cfg := baseConfig()
	a := BuildOperatorStatus(cfg)

	cfg2 := baseConfig()
	cfg2.Depth.Depth = DepthStandard
	cfg2.Drift.Mode = SkipSingle
	b := BuildOperatorStatus(cfg2)

	if a.Economy.Depth != DepthMinimal {
		t.Errorf("status economy depth = %s, want minimal", a.Economy.Depth)
	}
	if b.Economy.Depth != DepthStandard {
		t.Errorf("status economy depth = %s, want standard", b.Economy.Depth)
	}
	if a.Economy.SavedTokens == b.Economy.SavedTokens {
		t.Error("different depths must produce different modeled savings")
	}
	if a.Economy.OptimizedTokens >= b.Economy.OptimizedTokens {
		t.Errorf("minimal (%d) should use fewer tokens than standard (%d)", a.Economy.OptimizedTokens, b.Economy.OptimizedTokens)
	}
}

func TestBuildOperatorStatus_CarriesAllDecisions(t *testing.T) {
	cfg := baseConfig()
	s := BuildOperatorStatus(cfg)

	if s.Mode != ModeDegraded {
		t.Errorf("mode = %v, want degraded", s.Mode)
	}
	if s.Tier != TierFast {
		t.Errorf("tier = %v, want fast", s.Tier)
	}
	if s.Drift.Mode != SkipSelfJudge {
		t.Errorf("drift = %v, want selfjudge", s.Drift.Mode)
	}
	if s.Persona != "novato" {
		t.Errorf("persona = %q, want novato", s.Persona)
	}
	if s.SessionID != "sess-l11" {
		t.Errorf("session = %q, want sess-l11", s.SessionID)
	}
}

// ---------------------------------------------------------------------------
// Claims
// ---------------------------------------------------------------------------

func TestBuildOperatorStatus_ClaimCountAndOrderStable(t *testing.T) {
	s := BuildOperatorStatus(baseConfig())
	if len(s.Claims) != 7 {
		t.Fatalf("claim count = %d, want 7", len(s.Claims))
	}
	wantOrder := []string{
		"spend so far",
		"vs naive baseline (USD)",
		"vs naive baseline (tokens)",
		"vs naive baseline (latency)",
		"depth decision",
		"drift policy",
		"drift calls vs baseline",
	}
	for i, want := range wantOrder {
		if s.Claims[i].Label != want {
			t.Errorf("claim %d label = %q, want %q", i, s.Claims[i].Label, want)
		}
	}
}

func TestClaims_NeverUnlabeled(t *testing.T) {
	// Every claim must carry a grade and a source. A bare number in
	// this package would be a design regression.
	s := BuildOperatorStatus(baseConfig())
	for i, c := range s.Claims {
		if c.Grade < GradeUnverified || c.Grade > GradeMeasured {
			t.Errorf("claim %d (%s): invalid grade %d", i, c.Label, int(c.Grade))
		}
		if c.Source == "" {
			t.Errorf("claim %d (%s): empty source", i, c.Label)
		}
		if c.Value == "" {
			t.Errorf("claim %d (%s): empty value", i, c.Label)
		}
	}
}

func TestClaims_CaveatPresentWheneverNotMeasured(t *testing.T) {
	// Any claim weaker than Measured MUST explain why. This is the
	// mechanical guarantee behind "no bare numbers".
	s := BuildOperatorStatus(baseConfig())
	for i, c := range s.Claims {
		if c.Grade < GradeMeasured && c.Caveat == "" {
			t.Errorf("claim %d (%s) grade=%s has no caveat", i, c.Label, c.Grade)
		}
	}
}

func TestClaims_SpendClaimGradesToCostEvidence(t *testing.T) {
	// The spend claim is the only observational number; it must carry
	// the cost evidence grade, not a modeled one.
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg)
	if s.Claims[0].Grade != GradeMeasured {
		t.Errorf("spend claim grade = %s, want measured for verified pricing", s.Claims[0].Grade)
	}

	cfg.Cost = estimatedPricing(0.000197, 1)
	s2 := BuildOperatorStatus(cfg)
	if s2.Claims[0].Grade != GradeEstimated {
		t.Errorf("spend claim grade = %s, want estimated for placeholder pricing", s2.Claims[0].Grade)
	}

	cfg.Cost = CostSummary{}
	s3 := BuildOperatorStatus(cfg)
	if s3.Claims[0].Grade != GradeUnverified {
		t.Errorf("spend claim grade = %s, want unverified for zero calls", s3.Claims[0].Grade)
	}
}

func TestClaims_ModeledClaimsDiscloseNoCalls(t *testing.T) {
	// When nothing was measured, the modeled claims must say so.
	cfg := baseConfig()
	cfg.Cost = CostSummary{}
	s := BuildOperatorStatus(cfg)

	for i, c := range s.Claims {
		if c.Grade == GradeModeled || c.Grade == GradeEstimated {
			if c.Source == sourceEconomy && !strings.Contains(c.Caveat, "no calls recorded") {
				t.Errorf("claim %d (%s) must disclose that nothing was measured; caveat = %q", i, c.Label, c.Caveat)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Self-contradiction guard
// ---------------------------------------------------------------------------

func TestClaims_NoFalseNoCallsCaveatWhenCallsExist(t *testing.T) {
	// BUG FIX 2026-10-08 (L11.1): the modeled-claim caveat used to be
	// hardcoded to "no calls recorded in this session", which produced
	// a self-contradicting report —
	//
	//   spend so far        $0.000197 over 1 call(s)   [measured]
	//   vs naive baseline   +0.003875 (+95.2%)         [modeled]
	//       caveat: ... no calls recorded in this session   <- FALSE
	//
	// An auditor reading that output would be right to reject the
	// whole readout. No claim may claim ignorance of calls that the
	// same report says were recorded.
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg)

	if s.Cost.CallCount == 0 {
		t.Fatal("fixture precondition: expected calls recorded")
	}
	for i, c := range s.Claims {
		if strings.Contains(c.Caveat, "no calls recorded") {
			t.Errorf("claim %d (%s) falsely says 'no calls recorded' while CallCount=%d; caveat = %q",
				i, c.Label, s.Cost.CallCount, c.Caveat)
		}
	}
	if strings.Contains(s.String(), "no calls recorded") {
		t.Error("rendered output falsely claims no calls were recorded")
	}
}

func TestClaims_NoFalseNoCallsCaveatForManyCallCounts(t *testing.T) {
	// The contradiction must not reappear at any call count.
	for _, calls := range []int{1, 2, 5, 17, 100} {
		cfg := baseConfig()
		cfg.Cost = verifiedPricing(0.001, calls)
		s := BuildOperatorStatus(cfg)

		for i, c := range s.Claims {
			if strings.Contains(c.Caveat, "no calls recorded") {
				t.Errorf("CallCount=%d: claim %d (%s) falsely says 'no calls recorded'; caveat = %q",
					calls, i, c.Label, c.Caveat)
			}
		}
	}
}

func TestClaims_NoCallsCaveatOnlyWhenTrulyZero(t *testing.T) {
	// The converse guard: the honest disclosure must still appear when
	// the session really has no data.
	cfg := baseConfig()
	cfg.Cost = CostSummary{}
	s := BuildOperatorStatus(cfg)

	found := false
	for _, c := range s.Claims {
		if strings.Contains(c.Caveat, "no calls recorded") {
			found = true
		}
	}
	if !found {
		t.Error("zero-call session must disclose 'no calls recorded'")
	}
}

func TestClaims_ModeledWithCallsCaveatNamesTheBaselineModel(t *testing.T) {
	// With calls present, the caveat must explain what is actually
	// modeled (the baseline), not claim ignorance of the spend.
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg)

	for _, c := range s.Claims {
		if c.Source != sourceEconomy {
			continue
		}
		if !strings.Contains(c.Caveat, "DepthStandard+Balanced+Single") {
			t.Errorf("claim %s caveat must name the modeled baseline; got %q", c.Label, c.Caveat)
		}
		if !strings.Contains(c.Caveat, "not observed spend") {
			t.Errorf("claim %s caveat must distinguish model from observation; got %q", c.Label, c.Caveat)
		}
	}
}

func TestClaims_DriftClaimDisclosesCoverageReduction(t *testing.T) {
	// A skipped drift check is a real coverage reduction, not a free
	// saving. The claim must say so.
	cfg := baseConfig()
	cfg.Drift = DriftPolicy{Mode: SkipNoDrift, Reason: "depth<=1", EstCalls: 0}
	s := BuildOperatorStatus(cfg)

	var found bool
	for _, c := range s.Claims {
		if c.Label == "drift policy" {
			found = true
			if !strings.Contains(c.Caveat, "coverage reduction") {
				t.Errorf("drift claim caveat must mention coverage reduction; got %q", c.Caveat)
			}
		}
	}
	if !found {
		t.Fatal("drift policy claim not found")
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func TestOperatorStatus_String_RendersAllSections(t *testing.T) {
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg).String()

	for _, want := range []string{
		"Vibe-Flow Operator Status (Loop 11 L11.1)",
		"verdict   :",
		"evidence:",
		"decisions",
		"depth   :",
		"tier    :",
		"drift   :",
		"claims (every number carries its evidence grade)",
		"how to read the grades",
		"measured",
		"modeled",
		"estimated",
		"unverified",
		"caveat:",
		"source:",
		"not an independent evaluation",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered output missing %q", want)
		}
	}
}

func TestOperatorStatus_String_ShowsModeCounters(t *testing.T) {
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg).String()

	if !strings.Contains(s, "elapsed=95s") {
		t.Error("must render elapsed seconds from ModeState")
	}
	if !strings.Contains(s, "tools=2") {
		t.Error("must render tool call count")
	}
	if !strings.Contains(s, "followups=1") {
		t.Error("must render followup count")
	}
}

func TestOperatorStatus_String_ShowsDepthReason(t *testing.T) {
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg).String()

	if !strings.Contains(s, "base=1 -1(tier:novice)") {
		t.Error("must render the depth decision reason verbatim")
	}
	if !strings.Contains(s, "tier:novice depth<=2") {
		t.Error("must render the drift policy reason verbatim")
	}
}

func TestOperatorStatus_String_AlwaysCarriesHonestyFooter(t *testing.T) {
	// The footer must appear even on a fully favorable report, so a
	// screenshot of a good-looking status still carries the caveat.
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000001, 1) // very good news
	st := BuildOperatorStatus(cfg)
	s := st.String()

	if st.Verdict != VerdictHelping {
		t.Fatalf("fixture precondition: verdict = %s, want helping", st.Verdict)
	}
	if !strings.Contains(s, "not an independent evaluation") {
		t.Error("honesty footer missing on a favorable report")
	}
	if !strings.Contains(s, "self-reported, not that they were audited") {
		t.Error("footer must state the self-report limitation")
	}
}

func TestOperatorStatus_HeadlineCarriesVerdictAndGrade(t *testing.T) {
	// The operator must never read "helping" without also reading how
	// solid that word is.
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000001, 1)
	s := BuildOperatorStatus(cfg)

	if !strings.Contains(s.Headline, string(VerdictHelping)) {
		t.Errorf("headline %q must contain the verdict", s.Headline)
	}
	if !strings.Contains(s.Headline, s.VerdictGrade.String()) {
		t.Errorf("headline %q must contain the evidence grade", s.Headline)
	}
	for _, want := range []string{"mode=degraded", "depth=minimal", "tier=fast", "drift=selfjudge"} {
		if !strings.Contains(s.Headline, want) {
			t.Errorf("headline %q missing %q", s.Headline, want)
		}
	}
}

func TestOperatorStatus_ZeroConfigHeadlineHonest(t *testing.T) {
	s := BuildOperatorStatus(StatusConfig{})
	if !strings.Contains(s.Headline, "insufficient-data") {
		t.Errorf("zero-config headline = %q, must state insufficient-data", s.Headline)
	}
	if !strings.Contains(s.Headline, "unverified") {
		t.Errorf("zero-config headline = %q, must state unverified", s.Headline)
	}
}

// ---------------------------------------------------------------------------
// Export
// ---------------------------------------------------------------------------

func TestOperatorStatus_ExportClaims_OrderedAndLabeled(t *testing.T) {
	cfg := baseConfig()
	cfg.Cost = verifiedPricing(0.000197, 1)
	s := BuildOperatorStatus(cfg)

	lines := s.ExportClaims()
	if len(lines) != len(s.Claims) {
		t.Fatalf("ExportClaims len = %d, want %d", len(lines), len(s.Claims))
	}
	for i, line := range lines {
		if !strings.Contains(line, s.Claims[i].Label) {
			t.Errorf("line %d missing label %q", i, s.Claims[i].Label)
		}
		if !strings.Contains(line, "[") || !strings.Contains(line, "]") {
			t.Errorf("line %d missing grade bracket: %q", i, line)
		}
		if !strings.Contains(line, s.Claims[i].Source) {
			t.Errorf("line %d missing source: %q", i, line)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration with the real pipeline (Loops 8-10)
// ---------------------------------------------------------------------------

func TestOperatorStatus_RealPipelineEndToEnd(t *testing.T) {
	// Feed REAL decisions from ComputeDepth / SelectModelTier /
	// ComputeDriftPolicy into the status builder. Guards against the
	// readout drifting away from the system it reports on.
	class, conf, feats := Classify("investigate why the login page is slow and refactor the auth middleware", "C2")
	_ = conf

	ctx := Context{Tier: TierNovice, RushMode: false, Domain: DomainCode}
	depthDec := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: class,
		VibeCase:  "C2",
		Context:   ctx,
		Features:  feats,
	})
	tier := SelectModelTier(depthDec.Depth, ctx, "C2", feats)
	drift := ComputeDriftPolicy(depthDec.Depth, ctx, "C2", feats, false)

	cfg := StatusConfig{
		SessionID: "sess-e2e",
		Persona:   "novato",
		Mode:      ModeDegraded,
		Depth:     depthDec,
		Tier:      tier,
		Drift:     drift,
		Cost:      verifiedPricing(0.0004, 2),
	}

	s := BuildOperatorStatus(cfg)

	if s.Verdict == VerdictInsufficientData {
		t.Error("with 2 recorded calls the verdict must not be insufficient-data")
	}
	if s.VerdictGrade.Trusted() {
		t.Error("verdict must not be trusted as fact — the baseline is modeled")
	}
	if s.Depth.Reason == "" {
		t.Error("real ComputeDepth must supply a reason for the operator")
	}
	if s.Drift.Reason == "" {
		t.Error("real ComputeDriftPolicy must supply a reason for the operator")
	}
	if len(s.Claims) != 7 {
		t.Errorf("claim count = %d, want 7", len(s.Claims))
	}
	// The readout must echo the real decisions.
	out := s.String()
	if !strings.Contains(out, depthDec.Depth.String()) {
		t.Errorf("output missing real depth %s", depthDec.Depth)
	}
	if !strings.Contains(out, tier.String()) {
		t.Errorf("output missing real tier %s", tier)
	}
	if !strings.Contains(out, drift.Mode.String()) {
		t.Errorf("output missing real drift %s", drift.Mode)
	}
}

func TestOperatorStatus_ExhaustiveStrongIsReportedAsHurting(t *testing.T) {
	// The worst-case configuration must surface honestly as hurting
	// once real spend exceeds the baseline. This is the anti-overclaim
	// integration test.
	delta := ComputeEconomyDelta(DepthExhaustive, TierStrong, SkipConsensus)
	cfg := StatusConfig{
		SessionID: "sess-worst",
		Mode:      ModeFull,
		Depth:     DepthDecision{Depth: DepthExhaustive, Reason: "base=5 → exhaustive"},
		Tier:      TierStrong,
		Drift:     DriftPolicy{Mode: SkipConsensus, Reason: "depth>=5 → consensus"},
		Cost:      verifiedPricing(delta.BaselineUSD*4, 10),
	}
	s := BuildOperatorStatus(cfg)

	if s.Verdict != VerdictHurting {
		t.Errorf("verdict = %s, want hurting (4x baseline spend)", s.Verdict)
	}
	if s.VerdictGrade != GradeEstimated {
		t.Errorf("verdict grade = %s, want estimated (strong tier pricing is a placeholder)", s.VerdictGrade)
	}
	if !strings.Contains(s.String(), string(VerdictHurting)) {
		t.Error("rendered output must state hurting")
	}
}
