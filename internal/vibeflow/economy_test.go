package vibeflow

import (
	"math"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TokenProfile math
// ---------------------------------------------------------------------------

func TestTokenProfile_Totals(t *testing.T) {
	p := TokenProfile{
		SpecIn: 100, SpecOut: 200,
		GenIn: 300, GenOut: 400,
		DriftCalls: 2, DriftIn: 500, DriftOut: 50,
	}
	if got := p.TotalIn(); got != 100+300+2*500 {
		t.Errorf("TotalIn = %d, want %d", got, 100+300+2*500)
	}
	if got := p.TotalOut(); got != 200+400+2*50 {
		t.Errorf("TotalOut = %d, want %d", got, 200+400+2*50)
	}
	if got := p.Total(); got != p.TotalIn()+p.TotalOut() {
		t.Errorf("Total = %d, want %d", got, p.TotalIn()+p.TotalOut())
	}
}

// ---------------------------------------------------------------------------
// SpecTokensFor — grounded in BlockSizes
// ---------------------------------------------------------------------------

func TestWordsToTokens_ExactIntegerArithmetic(t *testing.T) {
	// Auditable: no float truncation. 200 words -> 260 tokens exactly.
	cases := []struct {
		words int
		want  int
	}{
		{0, 0},
		{10, 13},
		{100, 130},
		{200, 260},
		{350, 455},
		{2000, 2600},
	}
	for _, c := range cases {
		if got := wordsToTokens(c.words); got != c.want {
			t.Errorf("wordsToTokens(%d) = %d, want %d", c.words, got, c.want)
		}
	}
}

func TestSpecTokensFor_DepthSkip_IsZero(t *testing.T) {
	// DepthSkip has IntentMaxWords=0 and TaskMaxCount=0 → no spec at all.
	in, out := SpecTokensFor(DepthSkip)
	if in != 0 || out != 0 {
		t.Errorf("DepthSkip spec = in=%d out=%d, want 0/0", in, out)
	}
}

func TestSpecTokensFor_Monotonic(t *testing.T) {
	// Deeper depth → more spec tokens. This is the grounding property.
	prev := -1
	for _, d := range []Depth{DepthSkip, DepthMinimal, DepthLight, DepthStandard, DepthFull, DepthExhaustive} {
		_, out := SpecTokensFor(d)
		if out < prev {
			t.Errorf("depth %s spec out=%d < previous %d (not monotonic)", d, out, prev)
		}
		prev = out
	}
}

func TestSpecTokensFor_DepthStandard_UsesBlockSizes(t *testing.T) {
	// DepthStandard: IntentMaxWords=200, TaskMaxCount=7, TaskDescMaxWords=50,
	// ConstitutionMaxLines=4.
	// out = wordsToTokens(200) + wordsToTokens(350) = 260 + 455 = 715
	// in  = 4*20 + 715 = 80 + 715 = 795
	in, out := SpecTokensFor(DepthStandard)
	wantOut := wordsToTokens(200) + wordsToTokens(7*50)
	if out != wantOut {
		t.Errorf("DepthStandard spec out = %d, want %d", out, wantOut)
	}
	wantIn := 4*20 + wantOut
	if in != wantIn {
		t.Errorf("DepthStandard spec in = %d, want %d", in, wantIn)
	}
}

// ---------------------------------------------------------------------------
// ProfileFor
// ---------------------------------------------------------------------------

func TestProfileFor_DriftSkipNoDrift_ZeroCalls(t *testing.T) {
	p := ProfileFor(DepthStandard, SkipNoDrift)
	if p.DriftCalls != 0 {
		t.Errorf("SkipNoDrift calls = %d, want 0", p.DriftCalls)
	}
}

func TestProfileFor_SelfJudge_ZeroExternalCalls(t *testing.T) {
	p := ProfileFor(DepthLight, SkipSelfJudge)
	if p.DriftCalls != 0 {
		t.Errorf("SkipSelfJudge calls = %d, want 0 (inline, no external call)", p.DriftCalls)
	}
}

func TestProfileFor_Single_OneCall(t *testing.T) {
	p := ProfileFor(DepthStandard, SkipSingle)
	if p.DriftCalls != 1 {
		t.Errorf("SkipSingle calls = %d, want 1", p.DriftCalls)
	}
	if p.DriftIn == 0 || p.DriftOut == 0 {
		t.Error("drift phase should have in/out token counts when calls>0")
	}
}

func TestProfileFor_Consensus_ThreeCalls(t *testing.T) {
	p := ProfileFor(DepthExhaustive, SkipConsensus)
	if p.DriftCalls != 3 {
		t.Errorf("SkipConsensus calls = %d, want 3", p.DriftCalls)
	}
}

func TestProfileFor_GenerationScalesWithDepth(t *testing.T) {
	shallow := ProfileFor(DepthMinimal, SkipNoDrift)
	deep := ProfileFor(DepthExhaustive, SkipNoDrift)
	if deep.GenOut <= shallow.GenOut {
		t.Errorf("deeper depth should generate more: shallow=%d deep=%d",
			shallow.GenOut, deep.GenOut)
	}
}

// ---------------------------------------------------------------------------
// Baseline profile
// ---------------------------------------------------------------------------

func TestBaselineProfile_IsStandardSingle(t *testing.T) {
	// Baseline = DepthStandard + SkipSingle.
	want := ProfileFor(DepthStandard, SkipSingle)
	got := BaselineProfile()
	if got != want {
		t.Errorf("BaselineProfile = %+v, want %+v", got, want)
	}
}

func TestBaselineProfile_HasOneDriftCall(t *testing.T) {
	if BaselineProfile().DriftCalls != 1 {
		t.Errorf("baseline drift calls = %d, want 1", BaselineProfile().DriftCalls)
	}
}

// ---------------------------------------------------------------------------
// ProfileUSD
// ---------------------------------------------------------------------------

func TestProfileUSD_ZeroForZeroProfile(t *testing.T) {
	if got := ProfileUSD(TokenProfile{}, TierBalanced); got != 0 {
		t.Errorf("empty profile = %v, want 0", got)
	}
}

func TestProfileUSD_FastCheaperThanBalanced(t *testing.T) {
	// Same token profile, different tier → fast must be cheaper.
	p := ProfileFor(DepthStandard, SkipSingle)
	fast := ProfileUSD(p, TierFast)
	bal := ProfileUSD(p, TierBalanced)
	if fast >= bal {
		t.Errorf("fast (%v) should be < balanced (%v) for same profile", fast, bal)
	}
}

func TestProfileUSD_MatchesComputeCallCost(t *testing.T) {
	// ProfileUSD must be equivalent to summing ComputeCallCost over the
	// non-drift part and the drift part. Verifies no hidden math.
	p := ProfileFor(DepthLight, SkipMulti)
	pricing := RepresentativePricing(TierBalanced)

	nonDriftIn := p.SpecIn + p.GenIn
	nonDriftOut := p.SpecOut + p.GenOut
	want := ComputeCallCost(pricing, TokenUsage{nonDriftIn, nonDriftOut})
	want += ComputeCallCost(pricing, TokenUsage{p.DriftCalls * p.DriftIn, p.DriftCalls * p.DriftOut})

	got := ProfileUSD(p, TierBalanced)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("ProfileUSD = %v, want %v", got, want)
	}
}

func TestProfileUSD_StrongMoreThanBalanced(t *testing.T) {
	p := ProfileFor(DepthStandard, SkipSingle)
	bal := ProfileUSD(p, TierBalanced)
	strong := ProfileUSD(p, TierStrong)
	if strong <= bal {
		t.Errorf("strong (%v) should be > balanced (%v)", strong, bal)
	}
}

// ---------------------------------------------------------------------------
// ProfileLatencyMs
// ---------------------------------------------------------------------------

func TestProfileLatencyMs_ZeroForZeroProfile(t *testing.T) {
	if got := ProfileLatencyMs(TokenProfile{}, TierBalanced); got != 0 {
		t.Errorf("empty profile latency = %d, want 0", got)
	}
}

func TestProfileLatencyMs_SubThousandTokensNotZero(t *testing.T) {
	// Regression guard (bug fix 2026-10-08): the latency model used to
	// truncate to 0 ms for any profile under 1000 output tokens because
	// it computed (genOut/1000)*msPerK. A 426-token generation must
	// report a non-zero, permille-accurate latency.
	p := TokenProfile{GenIn: 100, GenOut: 426}
	got := ProfileLatencyMs(p, TierBalanced)
	// 426 tokens * 200 ms/1K / 1000 = 85 ms
	want := 426 * llmLatencyMsPerKTokens / 1000
	if got != want {
		t.Errorf("latency = %d, want %d (permille math)", got, want)
	}
	if got == 0 {
		t.Error("sub-1000-token generation must not report 0 ms")
	}
}

func TestProfileLatencyMs_PermilleMathExact(t *testing.T) {
	// Exact permille arithmetic, hand-checkable by an auditor.
	cases := []struct {
		outTokens int
		wantMs    int
	}{
		{0, 0},
		{100, 20},   // 100*200/1000 = 20
		{426, 85},   // 426*200/1000 = 85 (85.2 truncated)
		{500, 100},  // 500*200/1000 = 100
		{1000, 200}, // 1000*200/1000 = 200
		{1500, 300}, // 1500*200/1000 = 300
	}
	for _, c := range cases {
		p := TokenProfile{GenOut: c.outTokens}
		got := ProfileLatencyMs(p, TierBalanced)
		if got != c.wantMs {
			t.Errorf("GenOut=%d → %d ms, want %d", c.outTokens, got, c.wantMs)
		}
	}
}

func TestProfileLatencyMs_DriftAddsOverhead(t *testing.T) {
	none := ProfileLatencyMs(ProfileFor(DepthStandard, SkipNoDrift), TierBalanced)
	single := ProfileLatencyMs(ProfileFor(DepthStandard, SkipSingle), TierBalanced)
	if single <= none {
		t.Errorf("single (%d) should be > no-drift (%d)", single, none)
	}
}

func TestProfileLatencyMs_TierOrdering(t *testing.T) {
	p := ProfileFor(DepthStandard, SkipSingle)
	fast := ProfileLatencyMs(p, TierFast)
	bal := ProfileLatencyMs(p, TierBalanced)
	strong := ProfileLatencyMs(p, TierStrong)
	if !(fast < bal && bal < strong) {
		t.Errorf("latency ordering broken: fast=%d bal=%d strong=%d", fast, bal, strong)
	}
}

// ---------------------------------------------------------------------------
// RepresentativePricing
// ---------------------------------------------------------------------------

func TestRepresentativePricing_TierMapping(t *testing.T) {
	if p := RepresentativePricing(TierFast); p.Model != "deepseek-chat" {
		t.Errorf("TierFast = %s, want deepseek-chat", p.Model)
	}
	if p := RepresentativePricing(TierBalanced); p.Model != "MiniMax-M3" {
		t.Errorf("TierBalanced = %s, want MiniMax-M3", p.Model)
	}
	if p := RepresentativePricing(TierStrong); p.Model != "MiniMax-M3-deep" {
		t.Errorf("TierStrong = %s, want MiniMax-M3-deep", p.Model)
	}
}

func TestRepresentativePricing_UnknownTier_DefaultsBalanced(t *testing.T) {
	p := RepresentativePricing(ModelTier(99))
	if p.Model != "MiniMax-M3" {
		t.Errorf("unknown tier = %s, want MiniMax-M3 (balanced default)", p.Model)
	}
}

func TestTierLatencyFactor(t *testing.T) {
	if tierLatencyFactor(TierFast) >= tierLatencyFactor(TierBalanced) {
		t.Error("fast should be faster than balanced")
	}
	if tierLatencyFactor(TierBalanced) >= tierLatencyFactor(TierStrong) {
		t.Error("balanced should be faster than strong")
	}
	if tierLatencyFactor(ModelTier(99)) != 1.0 {
		t.Error("unknown tier factor should default to 1.0")
	}
}

// ---------------------------------------------------------------------------
// ComputeEconomyDelta
// ---------------------------------------------------------------------------

func TestEconomyDelta_IdenticalChoice_ZeroSaving(t *testing.T) {
	// If optimized == baseline (DepthStandard + Balanced + Single),
	// the saving must be exactly 0. This is the honesty property.
	d := ComputeEconomyDelta(DepthStandard, TierBalanced, SkipSingle)
	if d.SavedTokens != 0 {
		t.Errorf("identical choice saved tokens = %d, want 0", d.SavedTokens)
	}
	if math.Abs(d.SavedUSD) > 1e-12 {
		t.Errorf("identical choice saved USD = %v, want 0", d.SavedUSD)
	}
	if math.Abs(d.SavedPct) > 1e-9 {
		t.Errorf("identical choice saved pct = %v, want 0", d.SavedPct)
	}
}

func TestEconomyDelta_ShallowFastSkip_SavesAll(t *testing.T) {
	// DepthMinimal + Fast + SelfJudge should save on every axis.
	d := ComputeEconomyDelta(DepthMinimal, TierFast, SkipSelfJudge)
	if d.SavedTokens <= 0 {
		t.Errorf("saved tokens = %d, want > 0", d.SavedTokens)
	}
	if d.SavedUSD <= 0 {
		t.Errorf("saved USD = %v, want > 0", d.SavedUSD)
	}
	if d.SavedMs <= 0 {
		t.Errorf("saved ms = %d, want > 0", d.SavedMs)
	}
}

func TestEconomyDelta_DeeperCostsMore(t *testing.T) {
	shallow := ComputeEconomyDelta(DepthLight, TierBalanced, SkipSingle)
	deep := ComputeEconomyDelta(DepthExhaustive, TierStrong, SkipConsensus)
	if deep.OptimizedUSD <= shallow.OptimizedUSD {
		t.Errorf("deep+strong should cost more: deep=%v shallow=%v",
			deep.OptimizedUSD, shallow.OptimizedUSD)
	}
	if deep.SavedUSD >= shallow.SavedUSD {
		t.Error("deep+strong should save less (or cost more) than shallow")
	}
}

func TestEconomyDelta_StrongerTier_SavesLess(t *testing.T) {
	// Same depth+drift, only tier differs: Fast saves more than Strong.
	fast := ComputeEconomyDelta(DepthStandard, TierFast, SkipSingle)
	strong := ComputeEconomyDelta(DepthStandard, TierStrong, SkipSingle)
	if fast.SavedUSD <= strong.SavedUSD {
		t.Errorf("fast saving (%v) should exceed strong saving (%v)",
			fast.SavedUSD, strong.SavedUSD)
	}
}

func TestEconomyDelta_PercentagesUnboundedButConsistent(t *testing.T) {
	// Savings are NOT clamped to [-100,100]. A negative value is a
	// legitimate and important signal: the optimized path spends MORE
	// than the naive baseline (e.g. DepthExhaustive + Strong +
	// Consensus). Clamping would hide real overspend from the
	// operator, so the honest contract is:
	//   pct < 0  <=>  optimized > baseline  (genuine overspend)
	//   pct > 0  <=>  optimized < baseline  (genuine saving)
	//   pct == 0 <=>  optimized == baseline
	cases := []struct {
		d Depth
		t ModelTier
		s DriftSkip
	}{
		{DepthSkip, TierFast, SkipNoDrift},
		{DepthMinimal, TierFast, SkipSelfJudge},
		{DepthLight, TierFast, SkipSingle},
		{DepthStandard, TierBalanced, SkipSingle},
		{DepthFull, TierStrong, SkipMulti},
		{DepthExhaustive, TierStrong, SkipConsensus},
	}
	for _, c := range cases {
		d := ComputeEconomyDelta(c.d, c.t, c.s)

		// Sign consistency: percentage sign must match the absolute delta.
		if d.OptimizedTokens < d.BaselineTokens && d.SavedPct <= 0 {
			t.Errorf("depth=%s: optimized tokens (%d) < baseline (%d) but SavedPct = %.2f",
				c.d, d.OptimizedTokens, d.BaselineTokens, d.SavedPct)
		}
		if d.OptimizedTokens > d.BaselineTokens && d.SavedPct >= 0 {
			t.Errorf("depth=%s: optimized tokens (%d) > baseline (%d) but SavedPct = %.2f",
				c.d, d.OptimizedTokens, d.BaselineTokens, d.SavedPct)
		}
		if d.OptimizedUSD < d.BaselineUSD && d.SavedUSDPct <= 0 {
			t.Errorf("depth=%s: optimized USD (%v) < baseline (%v) but SavedUSDPct = %.2f",
				c.d, d.OptimizedUSD, d.BaselineUSD, d.SavedUSDPct)
		}

		// Overspend must be reported, never silently capped.
		if d.OptimizedUSD > d.BaselineUSD*2 && d.SavedUSDPct > -100 {
			t.Errorf("depth=%s: %.0f%% overspend but SavedUSDPct = %.2f (must report true overspend)",
				c.d, (d.OptimizedUSD/d.BaselineUSD-1)*100, d.SavedUSDPct)
		}
	}
}

func TestEconomyDelta_OverspendIsVisibleForExhaustive(t *testing.T) {
	// Regression guard: exhaustive+strong+consensus genuinely costs more
	// than baseline. The delta must say so rather than claim a saving.
	d := ComputeEconomyDelta(DepthExhaustive, TierStrong, SkipConsensus)
	if d.SavedUSD >= 0 {
		t.Errorf("expected overspend for exhaustive+strong+consensus, got saving %v", d.SavedUSD)
	}
	if d.SavedUSDPct >= 0 {
		t.Errorf("expected negative SavedUSDPct, got %.2f", d.SavedUSDPct)
	}
}

func TestEconomyDelta_PureFunction(t *testing.T) {
	a := ComputeEconomyDelta(DepthLight, TierFast, SkipSingle)
	b := ComputeEconomyDelta(DepthLight, TierFast, SkipSingle)
	if a != b {
		t.Errorf("not reproducible: %+v vs %+v", a, b)
	}
}

func TestEconomyDelta_FieldsPopulated(t *testing.T) {
	d := ComputeEconomyDelta(DepthLight, TierFast, SkipSingle)
	if d.Depth != DepthLight || d.Tier != TierFast || d.Drift != SkipSingle {
		t.Errorf("decision fields not echoed: %+v", d)
	}
	if d.BaselineTokens == 0 || d.OptimizedTokens == 0 {
		t.Error("token counts should be non-zero")
	}
	if d.BaselineUSD == 0 || d.OptimizedUSD == 0 {
		t.Error("USD should be non-zero")
	}
}

// ---------------------------------------------------------------------------
// BuildEconomyReport
// ---------------------------------------------------------------------------

func TestBuildEconomyReport_Empty(t *testing.T) {
	r := BuildEconomyReport(nil)
	if len(r.Personas) != 0 {
		t.Errorf("empty report has %d personas", len(r.Personas))
	}
	if r.AvgSavedPct != 0 || r.AvgSavedUSDPct != 0 {
		t.Error("empty report averages should be 0")
	}
}

func TestBuildEconomyReport_PreservesOrder(t *testing.T) {
	rows := []PersonaRow{
		{Name: "a", Depth: DepthMinimal, Tier: TierFast, Drift: SkipSelfJudge},
		{Name: "b", Depth: DepthStandard, Tier: TierBalanced, Drift: SkipSingle},
		{Name: "c", Depth: DepthExhaustive, Tier: TierStrong, Drift: SkipConsensus},
	}
	r := BuildEconomyReport(rows)
	if len(r.Personas) != 3 {
		t.Fatalf("got %d personas, want 3", len(r.Personas))
	}
	for i, want := range []string{"a", "b", "c"} {
		if r.Personas[i].Name != want {
			t.Errorf("persona %d = %q, want %q (order must be preserved)", i, r.Personas[i].Name, want)
		}
	}
}

func TestBuildEconomyReport_TotalsMatchSum(t *testing.T) {
	rows := PersonaDecisions10()
	r := BuildEconomyReport(rows)
	var tokSum, msSum int
	var usdSum float64
	for _, p := range r.Personas {
		tokSum += p.Delta.SavedTokens
		msSum += p.Delta.SavedMs
		usdSum += p.Delta.SavedUSD
	}
	if r.TotalSavedTokens != tokSum {
		t.Errorf("TotalSavedTokens = %d, want %d", r.TotalSavedTokens, tokSum)
	}
	if r.TotalSavedMs != msSum {
		t.Errorf("TotalSavedMs = %d, want %d", r.TotalSavedMs, msSum)
	}
	if math.Abs(r.TotalSavedUSD-usdSum) > 1e-9 {
		t.Errorf("TotalSavedUSD = %v, want %v", r.TotalSavedUSD, usdSum)
	}
}

func TestBuildEconomyReport_AvgIsMeanOfPcts(t *testing.T) {
	rows := PersonaDecisions10()
	r := BuildEconomyReport(rows)
	var sum float64
	for _, p := range r.Personas {
		sum += p.Delta.SavedPct
	}
	want := sum / float64(len(rows))
	if math.Abs(r.AvgSavedPct-want) > 1e-9 {
		t.Errorf("AvgSavedPct = %v, want %v", r.AvgSavedPct, want)
	}
}

// ---------------------------------------------------------------------------
// PersonaDecisions10 — the 10-persona fixture
// ---------------------------------------------------------------------------

func TestPersonaDecisions10_HasTenPersonas(t *testing.T) {
	if got := len(PersonaDecisions10()); got != 10 {
		t.Errorf("got %d personas, want 10", got)
	}
}

func TestPersonaDecisions10_UniqueNames(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range PersonaDecisions10() {
		if seen[p.Name] {
			t.Errorf("duplicate persona name %q", p.Name)
		}
		seen[p.Name] = true
	}
}

func TestPersonaDecisions10_CostLensMajority(t *testing.T) {
	// ≥6/10 personas should be on the Fast tier (the cost-lens claim
	// from Loop 9.1, now measured end-to-end).
	rows := PersonaDecisions10()
	fast := 0
	for _, p := range rows {
		if p.Tier == TierFast {
			fast++
		}
	}
	if fast < 6 {
		t.Errorf("only %d/10 personas on TierFast, want ≥6", fast)
	}
}

func TestPersonaDecisions10_MostlyShallowDepth(t *testing.T) {
	// Loop 8.3 collapses depth for the ModeOff personas. Verify at
	// least 6/10 are shallower than DepthStandard.
	rows := PersonaDecisions10()
	shallow := 0
	for _, p := range rows {
		if p.Depth < DepthStandard {
			shallow++
		}
	}
	if shallow < 6 {
		t.Errorf("only %d/10 personas below DepthStandard, want ≥6", shallow)
	}
}

func TestPersonaDecisions10_CostLensSavesRealMoney(t *testing.T) {
	// The value proposition: the 10-persona mix saves real USD.
	rows := PersonaDecisions10()
	r := BuildEconomyReport(rows)
	if r.TotalSavedUSD <= 0 {
		t.Errorf("10-persona mix saved %v USD, want > 0", r.TotalSavedUSD)
	}
	if r.AvgSavedUSDPct <= 0 {
		t.Errorf("avg USD saving = %.2f%%, want > 0", r.AvgSavedUSDPct)
	}
}

// ---------------------------------------------------------------------------
// Report rendering (the human-audit surface)
// ---------------------------------------------------------------------------

func TestEconomyReport_String_ContainsHeader(t *testing.T) {
	r := BuildEconomyReport(PersonaDecisions10())
	s := r.String()
	for _, phrase := range []string{
		"Token Economy Report",
		"Baseline",
		"Persona",
		"Avg saved",
	} {
		if !strings.Contains(s, phrase) {
			t.Errorf("report missing %q", phrase)
		}
	}
}

func TestEconomyReport_String_ListsAllPersonas(t *testing.T) {
	r := BuildEconomyReport(PersonaDecisions10())
	s := r.String()
	for _, p := range r.Personas {
		if !strings.Contains(s, p.Name) {
			t.Errorf("report missing persona %q", p.Name)
		}
	}
}

func TestEconomyReport_String_EmptyIsReadable(t *testing.T) {
	// An empty report must still render without panicking.
	r := BuildEconomyReport(nil)
	s := r.String()
	if s == "" {
		t.Error("empty report should still render a header")
	}
}