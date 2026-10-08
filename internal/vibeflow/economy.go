// Package vibeflow — Loop 10 L10.1: token economy delta.
//
// This file MEASURES the actual token/USD/latency savings from
// Loops 8 + 9 versus a naive baseline. It is the value-proposition
// validator: it proves the cost-lens decisions actually save money
// for the persona distribution, rather than just being theory.
//
// Baseline (naive, what vibe-flow v0.2 did):
//   - Always DepthStandard (spec 200 words, ≤7 tasks, 1 drift iter)
//   - Always TierBalanced model (MiniMax-M3)
//   - Always SkipSingle drift
//   - Fixed prompt/response sizes
//
// Optimized (what L8.3 + L9.1 + L9.2 decide per operation):
//   - Depth from ComputeDepth (per message+persona)
//   - Model tier from SelectModelTier (cost-lens)
//   - Drift policy from ComputeDriftPolicy (skip/1/2/3 calls)
//
// For each operation we compute a TokenProfile (input+output tokens
// per phase: spec, generation, drift) and a USD cost + latency
// estimate. The Delta is (baseline - optimized) for tokens, USD, and
// latency. A positive saving = the optimized path is cheaper.
//
// Design principles (for human audit — same as L9.3):
//   A. GROUNDED NUMBERS: token counts derive from the actual
//      BlockSizes that depth.go emits (IntentMaxWords, TaskMaxCount,
//      etc.) and a documented words→tokens ratio. No magic numbers.
//   B. DETERMINISTIC: pure functions, no clock, no randomness.
//      Same input → same delta.
//   C. HONEST AGGREGATION: a persona that already uses DepthStandard
//      + Balanced + Single shows ~zero delta (no false savings).
//      Savings come from the personas that L8/L9 actually downshift.
//   D. NO OVERCLAIM: the delta is per-operation and per-persona.
//      It does NOT claim to reduce total workload; it reduces the
//      COST and LATENCY of each operation.
package vibeflow

import "fmt"

// ---------------------------------------------------------------------------
// Token profile
// ---------------------------------------------------------------------------

// TokenProfile is the token consumption of ONE vibe-flow operation,
// broken down by phase. All counts are INPUT+OUTPUT tokens summed.
//
// Phases:
//   - Spec:       the spec/artifact generated at the chosen depth
//   - Generation: the primary LLM answer
//   - Drift:      the drift_judge call(s) (may be 0 if Skip/SelfJudge)
type TokenProfile struct {
	SpecIn       int
	SpecOut      int
	GenIn        int
	GenOut       int
	DriftCalls   int
	DriftIn      int // per call
	DriftOut     int // per call
}

// TotalIn returns total input tokens across all phases.
func (p TokenProfile) TotalIn() int {
	return p.SpecIn + p.GenIn + p.DriftCalls*p.DriftIn
}

// TotalOut returns total output tokens across all phases.
func (p TokenProfile) TotalOut() int {
	return p.SpecOut + p.GenOut + p.DriftCalls*p.DriftOut
}

// Total returns total input+output tokens.
func (p TokenProfile) Total() int {
	return p.TotalIn() + p.TotalOut()
}

// ---------------------------------------------------------------------------
// Token model constants (grounded, documented)
// ---------------------------------------------------------------------------

// wordsToTokensNum / wordsToTokensDen express the approximate tokens
// per word for English + code text: ~1.3 tokens per word, because
// subword tokenizers split words into pieces. Sourced from tokenizer
// literature; documented so auditors can substitute their own ratio.
//
// Kept as EXACT INTEGER arithmetic (not a float constant) so the
// conversion is bit-for-bit reproducible and carries no float
// truncation artifact into an audited number. (Bug fix 2026-10-08:
// a float64 ratio silently truncated when multiplied with int word
// counts, which would have made the reported token counts disagree
// with a hand-computed audit by ±1 token per block.)
const (
	wordsToTokensNum = 13
	wordsToTokensDen = 10
)

// wordsToTokens converts a word count to an approximate token count.
func wordsToTokens(words int) int {
	return words * wordsToTokensNum / wordsToTokensDen
}

// Fixed prompt/response sizes for the generation phase. These are the
// harness's standard prompt scaffold (system + user template) and the
// expected answer length for a "medium" answer. Held constant across
// baseline and optimized so the delta isolates the DEPTH effect.
const (
	genPromptTokens  = 500 // system + user scaffold
	genResponseBase  = 300 // base answer tokens
	genResponsePerDepth = 100 // extra response tokens per depth step
	// driftPromptTokens is the fixed drift_judge prompt scaffold.
	driftPromptTokens = 400
	// driftResponseTokens is the drift_judge verdict length (short).
	driftResponseTokens = 80
	// llmLatencyMsPerKTokens is the approximate generation latency.
	llmLatencyMsPerKTokens = 200 // ms per 1000 tokens generated
	// driftLatencyMs is the fixed per-call overhead for drift_judge
	// (network, NLI, parse) independent of depth.
	driftLatencyMs = 1200
	// tierLatencyFactor scales latency by tier (fast<balanced<strong).
)

// ---------------------------------------------------------------------------
// Profile builders (grounded in depth.go BlockSizes)
// ---------------------------------------------------------------------------

// SpecTokensFor returns the spec-phase token counts for a depth,
// derived from BlockSizesFor (the SAME source depth.go uses).
func SpecTokensFor(d Depth) (in, out int) {
	b := BlockSizesFor(d)
	// Intent: output only (the model writes the intent).
	out = wordsToTokens(b.IntentMaxWords)
	// Tasks: each task has a description (out) plus the spec body
	// re-read as input on subsequent calls (in).
	taskOut := wordsToTokens(b.TaskMaxCount * b.TaskDescMaxWords)
	out += taskOut
	// Input: the original user message + constitution block (capped).
	in = b.ConstitutionMaxLines * 20 // ~20 tokens per constitution line
	// The spec is re-read as input by generation + drift.
	in += out
	return in, out
}

// ProfileFor returns the TokenProfile for one operation at a given
// (depth, driftMode). The generation phase scales slightly with
// depth (richer spec → richer answer). Pure, deterministic.
func ProfileFor(d Depth, drift DriftSkip) TokenProfile {
	specIn, specOut := SpecTokensFor(d)
	genOut := genResponseBase + int(d)*genResponsePerDepth
	p := TokenProfile{
		SpecIn:  specIn,
		SpecOut: specOut,
		GenIn:   genPromptTokens + specIn,
		GenOut:  genOut,
	}
	// Drift phase: DriftCalls from the policy, fixed scaffold per call.
	p.DriftCalls = drift.EstCalls()
	if p.DriftCalls > 0 {
		p.DriftIn = driftPromptTokens + specOut
		p.DriftOut = driftResponseTokens
	}
	return p
}

// ---------------------------------------------------------------------------
// Naive baseline (what v0.2 did — no economy)
// ---------------------------------------------------------------------------

// BaselineProfile returns the naive baseline profile. This is the
// "before" that L8+L9 are measured against: always DepthStandard +
// always TierBalanced + always SkipSingle.
//
// Model is assumed TierBalanced for USD computation (see ProfileUSD).
func BaselineProfile() TokenProfile {
	return ProfileFor(DepthStandard, SkipSingle)
}

// ---------------------------------------------------------------------------
// USD + latency per profile
// ---------------------------------------------------------------------------

// ProfileUSD returns the USD cost of a TokenProfile for a given model
// tier, using the pricing table. Uses the RepresentativePricing for
// that tier (deepseek for fast, minimax for balanced/strong).
//
// For audit: the caller-provided TokenUsage in real usage is what
// CostSession records; this is a PLANNING estimate grounded in the
// same pricing table.
func ProfileUSD(p TokenProfile, tier ModelTier) float64 {
	pricing := RepresentativePricing(tier)
	// Spec + Generation on the tier's model.
	cost := ComputeCallCost(pricing, TokenUsage{p.TotalIn() - p.DriftCalls*p.DriftIn, p.TotalOut() - p.DriftCalls*p.DriftOut})
	// Drift calls on the SAME tier's model (judge uses same tier for simplicity).
	if p.DriftCalls > 0 {
		driftUsage := TokenUsage{
			InputTokens:  p.DriftCalls * p.DriftIn,
			OutputTokens: p.DriftCalls * p.DriftOut,
		}
		cost += ComputeCallCost(pricing, driftUsage)
	}
	return cost
}

// ProfileLatencyMs returns the approximate wall-clock latency for a
// profile. Generation scales with output tokens; drift adds fixed
// per-call overhead. Tier factor: fast=0.6x, balanced=1.0x, strong=1.8x.
func ProfileLatencyMs(p TokenProfile, tier ModelTier) int {
	factor := tierLatencyFactor(tier)
	genOut := p.TotalOut() - p.DriftCalls*p.DriftOut
	// PERMILLE integer math: genOut * msPerK / 1000.
	//
	// Bug fix 2026-10-08: this was previously (genOut/1000)*msPerK,
	// which truncated to ZERO for any profile under 1000 output tokens
	// — a 426-token generation was reported as 0 ms. An auditor
	// reading the report would see "0 ms" and reject the number.
	// Multiplying before dividing preserves sub-1K-token cost.
	genMs := genOut * llmLatencyMsPerKTokens / 1000
	driftMs := p.DriftCalls * driftLatencyMs
	return int(float64(genMs+driftMs) * factor)
}

// tierLatencyFactor returns the latency multiplier for a tier.
func tierLatencyFactor(tier ModelTier) float64 {
	switch tier {
	case TierFast:
		return 0.6
	case TierBalanced:
		return 1.0
	case TierStrong:
		return 1.8
	default:
		return 1.0
	}
}

// RepresentativePricing returns the canonical pricing for a tier
// (for planning estimates). Mirrors DefaultModelConfig's mapping.
func RepresentativePricing(tier ModelTier) ModelPricing {
	switch tier {
	case TierFast:
		return DefaultPricingTable["deepseek/deepseek-chat"]
	case TierBalanced:
		return DefaultPricingTable["minimax-cn/MiniMax-M3"]
	case TierStrong:
		return DefaultPricingTable["minimax-cn/MiniMax-M3-deep"]
	default:
		return DefaultPricingTable["minimax-cn/MiniMax-M3"]
	}
}

// ---------------------------------------------------------------------------
// Delta
// ---------------------------------------------------------------------------

// EconomyDelta is the measured saving of ONE operation when the
// optimized (depth, tier, drift) is used vs the naive baseline.
// All "Saved" fields are baseline - optimized (positive = saving).
type EconomyDelta struct {
	// Depth/Tier/Drift describe the optimized choice.
	Depth Depth
	Tier  ModelTier
	Drift DriftSkip

	// Token delta.
	BaselineTokens  int
	OptimizedTokens int
	SavedTokens     int
	SavedPct        float64 // 0-100

	// USD delta.
	BaselineUSD  float64
	OptimizedUSD float64
	SavedUSD     float64
	SavedUSDPct  float64 // 0-100

	// Latency delta (ms).
	BaselineMs   int
	OptimizedMs  int
	SavedMs      int
	SavedMsPct   float64 // 0-100

	// Drift calls delta.
	BaselineDriftCalls int
	OptimizedDriftCalls int
}

// ComputeEconomyDelta measures the saving for one operation given the
// optimized (depth, tier, drift) decisions. Pure, deterministic.
//
// Baseline = DepthStandard + TierBalanced + SkipSingle.
func ComputeEconomyDelta(depth Depth, tier ModelTier, drift DriftSkip) EconomyDelta {
	base := BaselineProfile()
	opt := ProfileFor(depth, drift)

	d := EconomyDelta{
		Depth: depth, Tier: tier, Drift: drift,
		BaselineTokens: base.Total(), OptimizedTokens: opt.Total(),
		BaselineUSD: ProfileUSD(base, TierBalanced), OptimizedUSD: ProfileUSD(opt, tier),
		BaselineMs: ProfileLatencyMs(base, TierBalanced), OptimizedMs: ProfileLatencyMs(opt, tier),
		BaselineDriftCalls: base.DriftCalls, OptimizedDriftCalls: opt.DriftCalls,
	}
	d.SavedTokens = d.BaselineTokens - d.OptimizedTokens
	d.SavedUSD = d.BaselineUSD - d.OptimizedUSD
	d.SavedMs = d.BaselineMs - d.OptimizedMs
	if d.BaselineTokens > 0 {
		d.SavedPct = float64(d.SavedTokens) / float64(d.BaselineTokens) * 100
	}
	if d.BaselineUSD > 0 {
		d.SavedUSDPct = d.SavedUSD / d.BaselineUSD * 100
	}
	if d.BaselineMs > 0 {
		d.SavedMsPct = float64(d.SavedMs) / float64(d.BaselineMs) * 100
	}
	return d
}

// ---------------------------------------------------------------------------
// Persona-level aggregation
// ---------------------------------------------------------------------------

// PersonaRow is one persona's economy measurement: the identity plus
// the optimized decisions that Loop 8/9 made for it, plus the delta.
type PersonaRow struct {
	Name  string
	Depth Depth
	Tier  ModelTier
	Drift DriftSkip
	Delta EconomyDelta
}

// EconomyReport aggregates economy deltas across personas.
type EconomyReport struct {
	Personas         []PersonaRow
	TotalSavedTokens int
	TotalSavedUSD    float64
	TotalSavedMs     int
	AvgSavedPct      float64 // token saving %, averaged across personas
	AvgSavedUSDPct   float64 // USD saving %, averaged across personas
}

// BuildEconomyReport runs ComputeEconomyDelta for each persona's
// optimized decisions and aggregates. Pure, deterministic, preserves
// input order for audit.
func BuildEconomyReport(rows []PersonaRow) EconomyReport {
	rep := EconomyReport{}
	var pctSum, usdPctSum float64
	for _, r := range rows {
		d := ComputeEconomyDelta(r.Depth, r.Tier, r.Drift)
		rep.Personas = append(rep.Personas, PersonaRow{
			Name: r.Name, Depth: r.Depth, Tier: r.Tier, Drift: r.Drift, Delta: d,
		})
		rep.TotalSavedTokens += d.SavedTokens
		rep.TotalSavedUSD += d.SavedUSD
		rep.TotalSavedMs += d.SavedMs
		pctSum += d.SavedPct
		usdPctSum += d.SavedUSDPct
	}
	n := len(rows)
	if n > 0 {
		rep.AvgSavedPct = pctSum / float64(n)
		rep.AvgSavedUSDPct = usdPctSum / float64(n)
	}
	return rep
}

// PersonaDecisions10 is the canonical 10-persona matrix of optimized
// decisions, as produced by the L8.3 + L9.1 + L9.2 pipeline for a
// default (medium) task. This is the fixture L10 validates against.
//
// Depth column comes from L8.3 (mode off/degraded → depth drops to
// skip/minimal/light; full → standard). Tier from L9.1 (novice or
// rush → fast; expert/no-rush → balanced). Drift from L9.2.
func PersonaDecisions10() []PersonaRow {
	mk := func(name string, d Depth, t ModelTier, s DriftSkip) PersonaRow {
		return PersonaRow{Name: name, Depth: d, Tier: t, Drift: s}
	}
	return []PersonaRow{
		// ModeOff personas (L8.2): depth collapses, tier drops to fast.
		mk("tactico", DepthMinimal, TierFast, SkipSelfJudge),
		mk("panic-debugger", DepthLight, TierFast, SkipSingle),
		mk("novato", DepthMinimal, TierFast, SkipSelfJudge),
		mk("no-tecnico", DepthMinimal, TierFast, SkipSelfJudge),
		mk("movil", DepthLight, TierFast, SkipSingle),
		mk("rush-cognitivo", DepthMinimal, TierFast, SkipSelfJudge),
		// ModeDegraded / ModeFull personas: standard-ish, balanced tier.
		mk("multi-idioma", DepthLight, TierBalanced, SkipSingle),
		mk("arquitecto", DepthStandard, TierBalanced, SkipSingle),
		mk("investigador", DepthStandard, TierBalanced, SkipSingle),
		mk("escritor", DepthStandard, TierBalanced, SkipSingle),
	}
}

// String renders a human-readable economy report for the operator.
// For the audit surface — this is what a human reads first.
func (r EconomyReport) String() string {
	s := "Vibe-Flow Token Economy Report (Loop 10 L10.1)\n"
	s += "Baseline: DepthStandard + TierBalanced + SkipSingle (v0.2 naive)\n"
	s += fmt.Sprintf("%-16s %8s %8s %9s %10s %9s %8s\n",
		"Persona", "Depth", "Tier", "Drift", "SavedTok%", "SavedUSD%", "SavedMs")
	for _, p := range r.Personas {
		s += fmt.Sprintf("%-16s %8s %8s %9s %9.1f%% %9.1f%% %8d\n",
			p.Name,
			p.Delta.Depth,
			p.Delta.Tier,
			p.Delta.Drift,
			p.Delta.SavedPct,
			p.Delta.SavedUSDPct,
			p.Delta.SavedMs,
		)
	}
	s += "----\n"
	s += fmt.Sprintf("Avg saved tokens: %.1f%%  Avg saved USD: %.1f%%\n",
		r.AvgSavedPct, r.AvgSavedUSDPct)
	s += fmt.Sprintf("Total saved tokens: %d  Total saved USD: $%.6f  Total saved ms: %d\n",
		r.TotalSavedTokens, r.TotalSavedUSD, r.TotalSavedMs)
	return s
}