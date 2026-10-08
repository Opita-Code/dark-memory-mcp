package vibeflow

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Math correctness — known answers
// ---------------------------------------------------------------------------

func TestComputeCallCost_KnownAnswer_DeepSeek(t *testing.T) {
	// 1000 input @ $0.14/M + 500 output @ $0.28/M
	// = 0.001 * 0.14 + 0.0005 * 0.28
	// = 0.00014 + 0.00014 = 0.00028 USD
	p := DefaultPricingTable["deepseek/deepseek-chat"]
	got := ComputeCallCost(p, TokenUsage{InputTokens: 1000, OutputTokens: 500})
	want := 0.00028
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("ComputeCallCost(deepseek, 1k/500) = %v, want %v", got, want)
	}
}

func TestComputeCallCost_KnownAnswer_MiniMax(t *testing.T) {
	// 1000 input @ $0.40/M + 500 output @ $2.00/M
	// = 0.001 * 0.40 + 0.0005 * 2.00
	// = 0.0004 + 0.001 = 0.0014 USD
	p := DefaultPricingTable["minimax-cn/MiniMax-M3"]
	got := ComputeCallCost(p, TokenUsage{InputTokens: 1000, OutputTokens: 500})
	want := 0.0014
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("ComputeCallCost(minimax, 1k/500) = %v, want %v", got, want)
	}
}

func TestComputeCallCost_KnownAnswer_AnthropicOpus(t *testing.T) {
	// 1000 input @ $15/M + 500 output @ $75/M
	// = 0.001 * 15 + 0.0005 * 75 = 0.015 + 0.0375 = 0.0525 USD
	p := DefaultPricingTable["anthropic/claude-3-opus"]
	got := ComputeCallCost(p, TokenUsage{InputTokens: 1000, OutputTokens: 500})
	want := 0.0525
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("ComputeCallCost(opus, 1k/500) = %v, want %v", got, want)
	}
}

func TestComputeCallCost_ZeroTokens(t *testing.T) {
	p := DefaultPricingTable["deepseek/deepseek-chat"]
	if got := ComputeCallCost(p, TokenUsage{}); got != 0 {
		t.Errorf("zero tokens = %v, want 0", got)
	}
}

func TestComputeCallCost_NegativeTokens_ReturnsZero(t *testing.T) {
	// Defensive: never panic, never produce negative cost.
	p := DefaultPricingTable["deepseek/deepseek-chat"]
	if got := ComputeCallCost(p, TokenUsage{InputTokens: -100, OutputTokens: 500}); got != 0 {
		t.Errorf("negative input = %v, want 0", got)
	}
	if got := ComputeCallCost(p, TokenUsage{InputTokens: 100, OutputTokens: -500}); got != 0 {
		t.Errorf("negative output = %v, want 0", got)
	}
}

func TestTokenUsage_Total(t *testing.T) {
	u := TokenUsage{InputTokens: 1000, OutputTokens: 500}
	if got := u.Total(); got != 1500 {
		t.Errorf("Total() = %d, want 1500", got)
	}
}

// ---------------------------------------------------------------------------
// Pricing table integrity — auditor checks
// ---------------------------------------------------------------------------

func TestDefaultPricingTable_HasAllL91Models(t *testing.T) {
	// L9.1 default config uses these models. They must be in the table.
	required := []string{
		"deepseek/deepseek-chat",
		"minimax-cn/MiniMax-M3",
		"minimax-cn/MiniMax-M3-deep",
	}
	for _, k := range required {
		if _, ok := DefaultPricingTable[k]; !ok {
			t.Errorf("missing pricing for %q", k)
		}
	}
}

func TestDefaultPricingTable_AllEntriesHaveSource(t *testing.T) {
	// Every entry must have a non-empty Source. Auditors rely on this.
	for k, p := range DefaultPricingTable {
		if p.Source == "" {
			t.Errorf("entry %q has empty Source", k)
		}
	}
}

func TestDefaultPricingTable_NoZeroPrices(t *testing.T) {
	// Verified prices should be > 0. Zero price is a sign of error.
	for k, p := range DefaultPricingTable {
		if !p.IsEstimate {
			if p.InputPerMTok <= 0 || p.OutputPerMTok <= 0 {
				t.Errorf("verified entry %q has zero price: in=%v out=%v",
					k, p.InputPerMTok, p.OutputPerMTok)
			}
		}
	}
}

func TestDefaultPricingTable_DeepSeekIsVerified(t *testing.T) {
	// DeepSeek is publicly documented — must be IsEstimate=false.
	if p, ok := DefaultPricingTable["deepseek/deepseek-chat"]; !ok {
		t.Fatal("deepseek-chat missing")
	} else if p.IsEstimate {
		t.Error("deepseek-chat should be verified (IsEstimate=false)")
	}
}

func TestDefaultPricingTable_MiniMaxIsEstimate(t *testing.T) {
	// MiniMax pricing is not verified — must be IsEstimate=true.
	if p, ok := DefaultPricingTable["minimax-cn/MiniMax-M3"]; !ok {
		t.Fatal("MiniMax-M3 missing")
	} else if !p.IsEstimate {
		t.Error("MiniMax-M3 should be IsEstimate=true (operator must verify)")
	}
}

func TestDefaultPricingTable_DeepSeekCheaperThanMiniMax(t *testing.T) {
	// Sanity: Fast < Balanced < Strong in cost per call.
	ds := DefaultPricingTable["deepseek/deepseek-chat"]
	mm := DefaultPricingTable["minimax-cn/MiniMax-M3"]
	md := DefaultPricingTable["minimax-cn/MiniMax-M3-deep"]
	dsCost := ds.InputPerMTok + ds.OutputPerMTok
	mmCost := mm.InputPerMTok + mm.OutputPerMTok
	mdCost := md.InputPerMTok + md.OutputPerMTok
	if !(dsCost < mmCost && mmCost < mdCost) {
		t.Errorf("cost ordering broken: deepseek=%v minimax=%v minimax-deep=%v",
			dsCost, mmCost, mdCost)
	}
}

// ---------------------------------------------------------------------------
// Lookup
// ---------------------------------------------------------------------------

func TestLookupPricing_Found(t *testing.T) {
	p, ok := LookupPricing(DefaultPricingTable, "deepseek", "deepseek-chat")
	if !ok {
		t.Fatal("expected found")
	}
	if p.Provider != "deepseek" || p.Model != "deepseek-chat" {
		t.Errorf("got %s/%s, want deepseek/deepseek-chat", p.Provider, p.Model)
	}
}

func TestLookupPricing_NotFound(t *testing.T) {
	_, ok := LookupPricing(DefaultPricingTable, "nope", "nada")
	if ok {
		t.Error("expected not found")
	}
}

func TestUnknownPricing_HasMarker(t *testing.T) {
	p := UnknownPricing("foo", "bar")
	if !p.IsEstimate {
		t.Error("UnknownPricing should be IsEstimate=true")
	}
	if p.Source == "" || p.Source[:7] != "UNKNOWN" {
		t.Errorf("Source should start with UNKNOWN, got %q", p.Source)
	}
}

// ---------------------------------------------------------------------------
// CostSession — basic Record
// ---------------------------------------------------------------------------

func TestCostSession_Record_BasicCost(t *testing.T) {
	s := NewCostSession("test-1")
	call := CallCost{
		SessionID: "test-1", Provider: "deepseek", Model: "deepseek-chat",
		Operation: "drift_judge", Usage: TokenUsage{InputTokens: 1000, OutputTokens: 500},
		DriftMode: SkipSingle, Depth: DepthStandard, Persona: "intermediate",
		VibeCase: "C1", LatencyMs: 1500,
	}
	got := s.Record(call)
	if got.USD <= 0 {
		t.Errorf("expected positive cost, got %v", got.USD)
	}
	if got.Pricing.Provider != "deepseek" {
		t.Errorf("pricing not attached")
	}
	if got.IsEstimate {
		t.Error("deepseek should not be estimate")
	}
}

func TestCostSession_Record_UnknownModel_ZeroCost_EstimateTrue(t *testing.T) {
	s := NewCostSession("test-2")
	call := CallCost{
		SessionID: "test-2", Provider: "fake-provider", Model: "fake-model",
		Usage: TokenUsage{InputTokens: 1000, OutputTokens: 500},
	}
	got := s.Record(call)
	if got.USD != 0 {
		t.Errorf("unknown model should be $0, got %v", got.USD)
	}
	if !got.IsEstimate {
		t.Error("unknown model should be IsEstimate=true")
	}
	if got.Pricing.Source[:7] != "UNKNOWN" {
		t.Errorf("pricing source should mark UNKNOWN, got %q", got.Pricing.Source)
	}
}

func TestCostSession_Record_AssignsIDAndTimestamp(t *testing.T) {
	s := NewCostSession("test-3")
	before := time.Now()
	c1 := s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{100, 50}})
	c2 := s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{100, 50}})
	after := time.Now()
	if c1.ID == 0 || c2.ID == 0 {
		t.Error("ID should be non-zero")
	}
	if c1.ID == c2.ID {
		t.Error("IDs should be unique")
	}
	if c1.Timestamp.Before(before) || c1.Timestamp.After(after) {
		t.Errorf("timestamp out of range: %v", c1.Timestamp)
	}
}

func TestCostSession_Record_DefaultTimestamp(t *testing.T) {
	// If Timestamp is zero, Record assigns time.Now().
	s := NewCostSession("test-3b")
	c := s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat"})
	if c.Timestamp.IsZero() {
		t.Error("timestamp should be assigned by Record")
	}
}

func TestCostSession_Record_PreservesExplicitTimestamp(t *testing.T) {
	// If Timestamp is set, Record keeps it.
	s := NewCostSession("test-3c")
	fixed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Timestamp: fixed})
	if !c.Timestamp.Equal(fixed) {
		t.Errorf("timestamp = %v, want %v", c.Timestamp, fixed)
	}
}

// ---------------------------------------------------------------------------
// CostSession — SetPricing
// ---------------------------------------------------------------------------

func TestCostSession_SetPricing_Override(t *testing.T) {
	s := NewCostSession("test-4")
	// Override deepseek-chat with a higher price.
	s.SetPricing("deepseek", "deepseek-chat", ModelPricing{
		Provider: "deepseek", Model: "deepseek-chat",
		InputPerMTok: 999, OutputPerMTok: 999, IsEstimate: false,
	})
	c := s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat",
		Usage: TokenUsage{InputTokens: 1000, OutputTokens: 500}})
	// 0.001 * 999 + 0.0005 * 999 = 0.999 + 0.4995 = 1.4985
	want := 1.4985
	if math.Abs(c.USD-want) > 1e-9 {
		t.Errorf("override cost = %v, want %v", c.USD, want)
	}
}

// ---------------------------------------------------------------------------
// CostSession — Summary
// ---------------------------------------------------------------------------

func TestCostSession_Summary_Empty(t *testing.T) {
	s := NewCostSession("test-empty")
	sum := s.Summary()
	if sum.TotalUSD != 0 || sum.CallCount != 0 || sum.AvgCost != 0 {
		t.Errorf("empty session: %+v", sum)
	}
}

func TestCostSession_Summary_TotalMatchesCalls(t *testing.T) {
	s := NewCostSession("test-5")
	ds := DefaultPricingTable["deepseek/deepseek-chat"]
	mm := DefaultPricingTable["minimax-cn/MiniMax-M3"]
	// 2 deepseek calls (different token counts) + 1 minimax call
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{2000, 1000}})
	s.Record(CallCost{Provider: "minimax-cn", Model: "MiniMax-M3", Usage: TokenUsage{1000, 500}})

	sum := s.Summary()
	want := ComputeCallCost(ds, TokenUsage{1000, 500}) +
		ComputeCallCost(ds, TokenUsage{2000, 1000}) +
		ComputeCallCost(mm, TokenUsage{1000, 500})
	if math.Abs(sum.TotalUSD-want) > 1e-9 {
		t.Errorf("total = %v, want %v", sum.TotalUSD, want)
	}
	if sum.CallCount != 3 {
		t.Errorf("call count = %d, want 3", sum.CallCount)
	}
	if math.Abs(sum.AvgCost-sum.TotalUSD/3) > 1e-9 {
		t.Errorf("avg = %v, want %v", sum.AvgCost, sum.TotalUSD/3)
	}
}

func TestCostSession_Summary_ByMode(t *testing.T) {
	s := NewCostSession("test-6")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", DriftMode: SkipSingle, Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", DriftMode: SkipSingle, Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", DriftMode: SkipMulti, Usage: TokenUsage{1000, 500}})

	sum := s.Summary()
	if len(sum.ByMode) != 2 {
		t.Fatalf("expected 2 mode entries, got %d", len(sum.ByMode))
	}
	// Sorted by mode enum (SkipSingle=2, SkipMulti=3)
	if sum.ByMode[0].Mode != SkipSingle || sum.ByMode[0].Count != 2 {
		t.Errorf("first mode = %+v, want SkipSingle count=2", sum.ByMode[0])
	}
	if sum.ByMode[1].Mode != SkipMulti || sum.ByMode[1].Count != 1 {
		t.Errorf("second mode = %+v, want SkipMulti count=1", sum.ByMode[1])
	}
}

func TestCostSession_Summary_ByPersona(t *testing.T) {
	s := NewCostSession("test-7")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Persona: "novice", Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Persona: "novice", Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Persona: "expert", Usage: TokenUsage{1000, 500}})

	sum := s.Summary()
	if sum.ByPersona["novice"] != sum.ByPersona["expert"]*2 {
		t.Errorf("by persona: novice=%v expert=%v (want 2:1)",
			sum.ByPersona["novice"], sum.ByPersona["expert"])
	}
}

func TestCostSession_Summary_ByVibeCase(t *testing.T) {
	s := NewCostSession("test-8")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", VibeCase: "C1", Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", VibeCase: "C3", Usage: TokenUsage{2000, 1000}})

	sum := s.Summary()
	if sum.ByVibeCase["C1"] >= sum.ByVibeCase["C3"] {
		t.Errorf("C3 (2x tokens) should cost more than C1: %v vs %v",
			sum.ByVibeCase["C1"], sum.ByVibeCase["C3"])
	}
}

func TestCostSession_Summary_ByOperation(t *testing.T) {
	s := NewCostSession("test-9")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Operation: "drift_judge", Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Operation: "generation", Usage: TokenUsage{500, 200}})

	sum := s.Summary()
	if sum.ByOperation["drift_judge"] <= sum.ByOperation["generation"] {
		t.Error("drift_judge (1k+500) should cost more than generation (500+200)")
	}
}

func TestCostSession_Summary_ByTier(t *testing.T) {
	s := NewCostSession("test-10")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{1000, 500}}) // TierFast
	s.Record(CallCost{Provider: "minimax-cn", Model: "MiniMax-M3", Usage: TokenUsage{1000, 500}}) // TierBalanced

	sum := s.Summary()
	if len(sum.ByTier) != 2 {
		t.Fatalf("expected 2 tier entries, got %d", len(sum.ByTier))
	}
	// Find the tier entries
	var fastCost, balCost float64
	for _, t := range sum.ByTier {
		if t.Tier == TierFast {
			fastCost = t.Total
		}
		if t.Tier == TierBalanced {
			balCost = t.Total
		}
	}
	if fastCost >= balCost {
		t.Errorf("Fast should be cheaper than Balanced: fast=%v bal=%v", fastCost, balCost)
	}
}

func TestCostSession_Summary_EstimateCount(t *testing.T) {
	s := NewCostSession("test-11")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{100, 50}})    // verified
	s.Record(CallCost{Provider: "minimax-cn", Model: "MiniMax-M3", Usage: TokenUsage{100, 50}})    // estimate
	s.Record(CallCost{Provider: "minimax-cn", Model: "MiniMax-M3", Usage: TokenUsage{100, 50}})    // estimate
	s.Record(CallCost{Provider: "fake", Model: "fake", Usage: TokenUsage{100, 50}})               // unknown

	sum := s.Summary()
	if sum.EstimateCount != 3 {
		t.Errorf("estimate count = %d, want 3 (1 verified + 2 estimate + 1 unknown = 2 estimate + 1 unknown)", sum.EstimateCount)
	}
	if sum.UnknownModelCount != 1 {
		t.Errorf("unknown count = %d, want 1", sum.UnknownModelCount)
	}
}

func TestCostSession_Summary_Latency(t *testing.T) {
	s := NewCostSession("test-12")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", LatencyMs: 1000, Usage: TokenUsage{100, 50}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", LatencyMs: 2000, Usage: TokenUsage{100, 50}})
	sum := s.Summary()
	if sum.LatencyTotalMs != 3000 {
		t.Errorf("latency total = %d, want 3000", sum.LatencyTotalMs)
	}
}

// ---------------------------------------------------------------------------
// CostSession — Projected
// ---------------------------------------------------------------------------

func TestCostSession_Projected_Basic(t *testing.T) {
	s := NewCostSession("test-13")
	// 1 input token at $1000/M = 1/1M * 1000 = $0.001 per call.
	// 10 calls = $0.01. avg = $0.001. Projected(100) = 100 * $0.001 = $0.1
	s.SetPricing("test", "test", ModelPricing{
		InputPerMTok: 1000, OutputPerMTok: 0,
	})
	for i := 0; i < 10; i++ {
		s.Record(CallCost{Provider: "test", Model: "test", Usage: TokenUsage{1, 0}})
	}
	got := s.Projected(100)
	want := 0.1
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Projected(100) = %v, want %v", got, want)
	}
}

func TestCostSession_Projected_Zero(t *testing.T) {
	s := NewCostSession("test-14")
	if got := s.Projected(100); got != 0 {
		t.Errorf("empty session Projected(100) = %v, want 0", got)
	}
}

func TestCostSession_Projected_Negative(t *testing.T) {
	s := NewCostSession("test-15")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{1000, 500}})
	if got := s.Projected(-5); got != 0 {
		t.Errorf("negative ops = %v, want 0", got)
	}
}

func TestCostSession_Projected_Capped(t *testing.T) {
	s := NewCostSession("test-16")
	// 5 calls
	for i := 0; i < 5; i++ {
		s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{1000, 500}})
	}
	// 10x cap = 50 ops. 1000 ops should be capped to 50.
	got := s.Projected(1000)
	// avg = $0.00028, capped at 50 ops = $0.014
	avg := 0.00028
	want := avg * 50.0
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("Projected(1000) = %v, want %v (capped at 50)", got, want)
	}
}

// ---------------------------------------------------------------------------
// CostSession — Export
// ---------------------------------------------------------------------------

func TestCostSession_Export_ValidJSON(t *testing.T) {
	s := NewCostSession("test-export")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{1000, 500}})
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["session_id"] != "test-export" {
		t.Errorf("session_id = %v, want test-export", parsed["session_id"])
	}
	if _, ok := parsed["calls"]; !ok {
		t.Error("missing calls field")
	}
	if _, ok := parsed["total_usd"]; !ok {
		t.Error("missing total_usd field")
	}
}

func TestCostSession_Export_RoundtripTotal(t *testing.T) {
	s := NewCostSession("test-rt")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{1000, 500}})
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{2000, 1000}})

	data, _ := s.Export()
	var parsed struct {
		Total float64 `json:"total_usd"`
		Calls []struct {
			USD float64 `json:"USD"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	sumCalls := 0.0
	for _, c := range parsed.Calls {
		sumCalls += c.USD
	}
	if math.Abs(parsed.Total-sumCalls) > 1e-9 {
		t.Errorf("export total %v != sum of calls %v", parsed.Total, sumCalls)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestCostSession_Record_ThreadSafe(t *testing.T) {
	s := NewCostSession("test-concurrent")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{100, 50}})
		}()
	}
	wg.Wait()
	if len(s.calls) != 100 {
		t.Errorf("expected 100 calls, got %d (race condition?)", len(s.calls))
	}
}

// ---------------------------------------------------------------------------
// Disclaimer
// ---------------------------------------------------------------------------

func TestCostSession_Disclaimer_NonEmpty(t *testing.T) {
	s := NewCostSession("test-disc")
	d := s.Disclaimer()
	if d == "" {
		t.Fatal("disclaimer empty")
	}
	for _, phrase := range []string{"DOES NOT", "AUDIT CHECKLIST", "WHAT THIS DOES"} {
		if !contains(d, phrase) {
			t.Errorf("disclaimer missing %q", phrase)
		}
	}
}

func TestCostSession_EstimateErrorString_CleanWhenNoEstimates(t *testing.T) {
	s := NewCostSession("test-clean")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{100, 50}})
	if msg := s.EstimateErrorString(); msg != "" {
		t.Errorf("clean session should have empty warning, got %q", msg)
	}
}

func TestCostSession_EstimateErrorString_WarnsOnEstimates(t *testing.T) {
	s := NewCostSession("test-warn")
	s.Record(CallCost{Provider: "minimax-cn", Model: "MiniMax-M3", Usage: TokenUsage{100, 50}}) // estimate
	if msg := s.EstimateErrorString(); msg == "" {
		t.Error("estimate session should have warning")
	}
}

func TestCostSession_EstimateErrorString_WarnsOnUnknown(t *testing.T) {
	s := NewCostSession("test-warn2")
	s.Record(CallCost{Provider: "fake", Model: "fake", Usage: TokenUsage{100, 50}}) // unknown
	if msg := s.EstimateErrorString(); msg == "" {
		t.Error("unknown-model session should have warning")
	}
}

// ---------------------------------------------------------------------------
// Real-world pipeline: 10 personas at default task
// ---------------------------------------------------------------------------

func TestCostSession_10Personas_DefaultPipeline(t *testing.T) {
	// Simulate: each persona asks a default-depth question.
	// 10 personas, 1 generation + 1 drift per persona.
	s := NewCostSession("test-10p")

	type persona struct {
		name  string
		tier  string
		model string
	}
	personas := []persona{
		{"tactico", "novice", "deepseek/deepseek-chat"},
		{"panic-debugger", "intermediate", "minimax-cn/MiniMax-M3"},
		{"novato", "novice", "deepseek/deepseek-chat"},
		{"no-tecnico", "novice", "deepseek/deepseek-chat"},
		{"movil", "novice", "deepseek/deepseek-chat"},
		{"rush-cognitivo", "novice", "deepseek/deepseek-chat"},
		{"multi-idioma", "intermediate", "minimax-cn/MiniMax-M3"},
		{"arquitecto", "expert", "minimax-cn/MiniMax-M3"},
		{"investigador", "expert", "minimax-cn/MiniMax-M3"},
		{"escritor", "expert", "minimax-cn/MiniMax-M3"},
	}
	providers := map[string]string{
		"deepseek/deepseek-chat": "deepseek",
		"minimax-cn/MiniMax-M3":  "minimax-cn",
	}
	models := map[string]string{
		"deepseek/deepseek-chat": "deepseek-chat",
		"minimax-cn/MiniMax-M3":  "MiniMax-M3",
	}
	for _, p := range personas {
		prov := providers[p.model]
		mdl := models[p.model]
		// generation
		s.Record(CallCost{
			Provider: prov, Model: mdl, Operation: "generation",
			Persona: p.tier, Usage: TokenUsage{InputTokens: 1000, OutputTokens: 500},
		})
		// drift
		s.Record(CallCost{
			Provider: prov, Model: mdl, Operation: "drift_judge",
			Persona: p.tier, Usage: TokenUsage{InputTokens: 500, OutputTokens: 200},
		})
	}
	sum := s.Summary()
	if sum.CallCount != 20 {
		t.Errorf("call count = %d, want 20", sum.CallCount)
	}
	if sum.TotalUSD <= 0 {
		t.Error("total should be positive")
	}
	// Cost-lens check: novice personas (4 of 10) used Fast (deepseek)
	// so their cost should be visible in the breakdown.
	if sum.ByPersona["novice"] >= sum.ByPersona["expert"] {
		t.Errorf("novice (deepseek) should be cheaper than expert (minimax): novice=%v expert=%v",
			sum.ByPersona["novice"], sum.ByPersona["expert"])
	}
}

func TestCostSession_UnknownModel_AuditableViaSummary(t *testing.T) {
	// Auditor test: an unknown model in the session is visible in Summary.
	s := NewCostSession("test-audit")
	s.Record(CallCost{Provider: "deepseek", Model: "deepseek-chat", Usage: TokenUsage{100, 50}})    // OK
	s.Record(CallCost{Provider: "nope", Model: "nada", Usage: TokenUsage{100, 50}})                  // unknown
	sum := s.Summary()
	if sum.UnknownModelCount != 1 {
		t.Errorf("auditor: unknown count = %d, want 1", sum.UnknownModelCount)
	}
}

// ---------------------------------------------------------------------------
// (contains helper exists in mode_test.go)
// ---------------------------------------------------------------------------
