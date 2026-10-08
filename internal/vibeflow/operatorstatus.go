// Package vibeflow — Loop 11 L11.1: operator visibility + epistemic labeling.
//
// # WHY THIS FILE EXISTS
//
// Row 2534 (operator-validated, pinned): the operator cannot tell
// whether vibe-flow is HELPING or HURTING. Loops 8-10 made vibe-flow
// cheaper and smarter, but the operator has no single readout of what
// the system decided, why, what it cost, and what it saved. The system
// optimizes blind and reports blind.
//
// This file is the operator's chair: one struct, one String(), that
// answers "what did vibe-flow do for me, and should I believe it?"
//
// THE SOTA MOVE: Kadavath et al. 2022 (arXiv:2207.05221, row 2537)
// established two things we apply here:
//
//  1. CALIBRATION — a system should know how much to trust its own
//     output. We label every number with an EvidenceGrade.
//  2. SELECTIVE PREDICTION — the system abstains where it is likely
//     wrong, instead of always producing a confident answer.
//
// Translated to a self-reporting agent, this becomes a hard rule:
//
//	THE VERDICT'S EVIDENCE GRADE IS THE WEAKEST GRADE OF ITS INPUTS.
//
// Confidence propagates DOWNWARD only. A verdict built from one
// estimated number can never be stronger than "estimated". This is
// the opposite of the failure mode documented in rows 1985/2013/2016/
// 2017 (MiniMax-M3 nli=neutral presented as fact) — we refuse to
// launder weak evidence into strong claims.
//
// # THE ANTI-HALLUCINATION GUARD
//
// With zero recorded calls, the verdict is ALWAYS `insufficient-data`.
// It is never "helping". A system that has never run cannot claim it
// saved you anything. This is the single most important line in the
// file and it is enforced by test, not by comment.
//
// Purity: pure functions, deterministic, no clock, no randomness, no
// I/O. The caller supplies already-observed state.
package vibeflow

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// EvidenceGrade (Kadavath 2022 calibration)
// ---------------------------------------------------------------------------

// EvidenceGrade labels how much an operator should trust a
// self-reported number. Ordered weakest → strongest; lower is worse.
//
//	Unverified  no data at all
//	Estimated   data exists but rests on placeholder pricing
//	Modeled     deterministic projection from documented assumptions
//	Measured    computed from actually-recorded observations
type EvidenceGrade int

const (
	// GradeUnverified means there is no data behind the claim.
	GradeUnverified EvidenceGrade = iota

	// GradeEstimated means real calls were recorded but at least one
	// used placeholder pricing (IsEstimate=true) or an unknown model
	// (recorded at $0). The shape is right; the dollars are a guess.
	GradeEstimated

	// GradeModeled means the number comes from a deterministic
	// projection over documented assumptions (e.g. the L10.1 economy
	// model), not from observation.
	GradeModeled

	// GradeMeasured means every input was an actually-recorded
	// observation and every price was vendor-verified.
	GradeMeasured
)

// String returns the lowercase grade name for display.
func (g EvidenceGrade) String() string {
	switch g {
	case GradeUnverified:
		return "unverified"
	case GradeEstimated:
		return "estimated"
	case GradeModeled:
		return "modeled"
	case GradeMeasured:
		return "measured"
	default:
		return fmt.Sprintf("unknown(%d)", int(g))
	}
}

// Trusted reports whether the grade is strong enough to state as fact.
// Only Measured qualifies. This mirrors Kadavath's selective
// prediction: abstain everywhere else.
//
// Modeled is deliberately NOT trusted as a fact. It is a plan. The
// operator may believe it, but the system must not assert it.
func (g EvidenceGrade) Trusted() bool {
	return g == GradeMeasured
}

// weaker returns the weaker (lower) of two grades. Used for the
// confidence-propagation rule: a claim is never stronger than its
// weakest input.
func weaker(a, b EvidenceGrade) EvidenceGrade {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Claim
// ---------------------------------------------------------------------------

// Claim is one operator-facing number carrying its own trust label.
// The operator never sees a bare number in this package — every figure
// is wrapped in a Claim so its epistemic status travels with it.
type Claim struct {
	// Label is the human-readable name ("spend so far").
	Label string

	// Value is the pre-formatted value ("$0.004072").
	Value string

	// Grade is how much the operator should trust Value.
	Grade EvidenceGrade

	// Source names the subsystem that produced Value.
	Source string

	// Caveat is a non-empty honesty note whenever Grade is below
	// Measured. Empty only for fully-trusted claims.
	Caveat string
}

// ---------------------------------------------------------------------------
// Verdict
// ---------------------------------------------------------------------------

// Verdict is the operator-facing answer to "is vibe-flow helping me?".
type Verdict string

const (
	// VerdictInsufficientData means we lack observations to answer.
	// This is the DEFAULT and is preferred over a guess.
	VerdictInsufficientData Verdict = "insufficient-data"

	// VerdictHelping means measured spend is below the modeled
	// baseline.
	VerdictHelping Verdict = "helping"

	// VerdictNeutral means measured spend matches the modeled
	// baseline (e.g. the persona already ran at baseline config).
	VerdictNeutral Verdict = "neutral"

	// VerdictHurting means measured spend exceeds the modeled
	// baseline. Reported honestly — never suppressed, never clamped.
	VerdictHurting Verdict = "hurting"
)

// ---------------------------------------------------------------------------
// StatusConfig — caller-supplied observed state
// ---------------------------------------------------------------------------

// StatusConfig bundles everything BuildOperatorStatus needs. All
// fields are values the caller already holds; nothing is fetched,
// inferred, or clocked here.
type StatusConfig struct {
	// SessionID identifies the session being reported on.
	SessionID string

	// Persona is the operator-facing persona label ("novato"). May be
	// empty.
	Persona string

	// Mode is the current vibe-flow activation level (L8.2).
	Mode Mode

	// ModeState is the L8.2 state (elapsed, tool calls, followups).
	// Zero value is fine and renders as zeros.
	ModeState State

	// Depth is the L8.3 depth decision, including its Reason.
	Depth DepthDecision

	// Tier is the L9.1 selected model tier.
	Tier ModelTier

	// Drift is the L9.2 drift policy decision, including its Reason.
	Drift DriftPolicy

	// Cost is the L9.3 cost summary. A zero value means "no calls
	// recorded" and forces VerdictInsufficientData.
	Cost CostSummary
}

// ---------------------------------------------------------------------------
// OperatorStatus
// ---------------------------------------------------------------------------

// OperatorStatus is the single operator readout: what vibe-flow
// decided, why, what it cost, what it saved, and how much of that to
// believe.
type OperatorStatus struct {
	// SessionID / Persona identify what is being reported.
	SessionID string
	Persona   string

	// Mode, Depth, Tier, Drift are the decisions (L8.2/L8.3/L9.1/L9.2).
	Mode  Mode
	Depth DepthDecision
	Tier  ModelTier
	Drift DriftPolicy

	// ModeState carries the L8.2 counters.
	ModeState State

	// Economy is the L10.1 modeled delta vs the naive baseline.
	Economy EconomyDelta

	// Cost is the L9.3 measured summary.
	Cost CostSummary

	// Claims are the labeled operator-facing numbers, in stable order.
	Claims []Claim

	// Verdict is the headline answer.
	Verdict Verdict

	// VerdictGrade is the evidence grade OF THE VERDICT ITSELF. This
	// is the weakest grade among its inputs (Kadavath propagation).
	VerdictGrade EvidenceGrade

	// Headline is a one-line human summary of the verdict.
	Headline string
}

// ---------------------------------------------------------------------------
// Grading helpers
// ---------------------------------------------------------------------------

// GradeCost returns the evidence grade of a CostSummary.
//
//	0 calls                                   -> Unverified
//	calls with estimate/unknown pricing       -> Estimated
//	calls with all vendor-verified pricing    -> Measured
//
// Note: even a fully-measured summary is NOT stronger than Measured —
// cost is real data, so Measured is the ceiling. Callers that combine
// cost with modeled projections must use weaker() to propagate.
func GradeCost(sum CostSummary) EvidenceGrade {
	if sum.CallCount == 0 {
		return GradeUnverified
	}
	if sum.EstimateCount > 0 || sum.UnknownModelCount > 0 {
		return GradeEstimated
	}
	return GradeMeasured
}

// GradeEconomy returns the evidence grade of an L10.1 economy delta.
// The economy model is a deterministic projection over documented
// assumptions, so it is Modeled by construction — never Measured.
// If the tier's representative pricing is a placeholder, the claim
// drops to Estimated because the dollars are a guess.
func GradeEconomy(delta EconomyDelta, tier ModelTier) EvidenceGrade {
	if RepresentativePricing(tier).IsEstimate {
		return GradeEstimated
	}
	// A degenerate delta (baseline == optimized) carries no claim at
	// all, but callers still label it; Modeled is the honest floor.
	return GradeModeled
}

// ---------------------------------------------------------------------------
// Verdict computation
// ---------------------------------------------------------------------------

// ComputeVerdict answers "is vibe-flow helping?" from a measured cost
// summary and the modeled baseline comparison.
//
// THE GUARD: with no recorded calls the verdict is ALWAYS
// insufficient-data. It is never helping, never neutral, never
// hurting. A system that has never run cannot claim credit or blame.
//
// THE PROPAGATION: the returned grade is weaker(costGrade,
// GradeModeled). The verdict compares measured spend against a MODELED
// baseline, so it can never exceed Modeled even when the spend itself
// is fully measured. When spend rests on placeholder pricing the
// verdict degrades further to Estimated.
//
// The second return value is the evidence grade of the verdict itself.
func ComputeVerdict(sum CostSummary, delta EconomyDelta) (Verdict, EvidenceGrade) {
	costGrade := GradeCost(sum)

	// Anti-hallucination guard. No observations → no verdict.
	if costGrade == GradeUnverified {
		return VerdictInsufficientData, GradeUnverified
	}

	// Confidence propagates downward only.
	grade := weaker(costGrade, GradeEconomy(delta, delta.Tier))

	// Insufficient data is never asserted at any strength.
	if grade == GradeUnverified {
		return VerdictInsufficientData, GradeUnverified
	}

	// Compare measured spend against the modeled baseline. Strict
	// sign-based, no thresholds and no clamping: a 1-cent saving and a
	// 1-cent overspend both count, and a real overspend is reported
	// as hurting rather than smoothed to neutral.
	switch {
	case sum.TotalUSD > delta.BaselineUSD:
		return VerdictHurting, grade
	case sum.TotalUSD < delta.BaselineUSD:
		return VerdictHelping, grade
	default:
		return VerdictNeutral, grade
	}
}

// ---------------------------------------------------------------------------
// Claim construction
// ---------------------------------------------------------------------------

const (
	sourceCost    = "cost_session(L9.3)"
	sourceEconomy = "economy_model(L10.1)"
	sourceDepth   = "depth_decision(L8.3)"
	sourceDrift   = "drift_policy(L9.2)"
	sourceMode    = "mode_state(L8.2)"
)

const caveatEstimatePricing = "pricing is a placeholder (IsEstimate=true); verify at the vendor URL in DefaultPricingTable before treating these dollars as fact"

const caveatModeledNoCalls = "modeled projection, not a measurement — no calls recorded in this session"

const caveatModeledWithCalls = "modeled projection, not a measurement — the baseline (DepthStandard+Balanced+Single) is a deterministic model, not observed spend for this session"

// econCaveat returns the honest caveat for a modeled claim.
//
// Bug fix 2026-10-08 (L11.1): this previously returned
// caveatModeledNoCalls unconditionally, which produced a self-
// contradicting report — "spend so far: $0.000197 over 1 call(s)"
// rendered directly above "caveat: ... no calls recorded in this
// session". An auditor reading that output would be right to reject
// the whole readout. The caveat must track the actual call state.
func econCaveat(costGrade, econGrade EvidenceGrade) string {
	if costGrade == GradeUnverified {
		return caveatModeledNoCalls
	}
	if econGrade == GradeEstimated {
		return caveatModeledWithCalls + "; " + caveatEstimatePricing
	}
	return caveatModeledWithCalls
}

// buildClaims returns the operator-facing claims in a STABLE order
// (a slice, never a map, so String() is deterministic).
func buildClaims(cfg StatusConfig, delta EconomyDelta, costGrade EvidenceGrade) []Claim {
	claims := make([]Claim, 0, 7)

	// 1. Spend so far — the only genuinely observational number here.
	spendCaveat := ""
	if costGrade == GradeUnverified {
		spendCaveat = "no calls recorded in this session"
	} else if costGrade == GradeEstimated {
		spendCaveat = caveatEstimatePricing
	}
	claims = append(claims, Claim{
		Label:  "spend so far",
		Value:  fmt.Sprintf("$%.6f over %d call(s)", cfg.Cost.TotalUSD, cfg.Cost.CallCount),
		Grade:  costGrade,
		Source: sourceCost,
		Caveat: spendCaveat,
	})

	// 2-4. Modeled deltas. These are a plan, never a fact.
	econGrade := GradeEconomy(delta, delta.Tier)
	econNote := econCaveat(costGrade, econGrade)
	claims = append(claims, Claim{
		Label:  "vs naive baseline (USD)",
		Value:  fmt.Sprintf("%+.6f (%+.1f%%)", delta.SavedUSD, delta.SavedUSDPct),
		Grade:  econGrade,
		Source: sourceEconomy,
		Caveat: econNote,
	})
	claims = append(claims, Claim{
		Label:  "vs naive baseline (tokens)",
		Value:  fmt.Sprintf("%+d (%+.1f%%)", delta.SavedTokens, delta.SavedPct),
		Grade:  econGrade,
		Source: sourceEconomy,
		Caveat: econNote,
	})
	claims = append(claims, Claim{
		Label:  "vs naive baseline (latency)",
		Value:  fmt.Sprintf("%+dms (%+.1f%%)", delta.SavedMs, delta.SavedMsPct),
		Grade:  econGrade,
		Source: sourceEconomy,
		Caveat: econNote,
	})

	// 5. The depth decision and its reason — deterministic, so Modeled.
	claims = append(claims, Claim{
		Label:  "depth decision",
		Value:  fmt.Sprintf("%s (%s)", cfg.Depth.Depth, cfg.Depth.Reason),
		Grade:  GradeModeled,
		Source: sourceDepth,
		Caveat: "deterministic function of (mode, task class, persona, features) — reproducible, but assumes the classifier saw the whole task",
	})

	// 6. The drift policy and its reason.
	claims = append(claims, Claim{
		Label:  "drift policy",
		Value:  fmt.Sprintf("%s (%s)", cfg.Drift.Mode, cfg.Drift.Reason),
		Grade:  GradeModeled,
		Source: sourceDrift,
		Caveat: "deterministic; a skipped drift check is a real coverage reduction, not a free saving",
	})

	// 7. Drift calls avoided vs baseline.
	claims = append(claims, Claim{
		Label:  "drift calls vs baseline",
		Value:  fmt.Sprintf("%d vs %d", delta.OptimizedDriftCalls, delta.BaselineDriftCalls),
		Grade:  econGrade,
		Source: sourceEconomy,
		Caveat: econNote,
	})

	return claims
}

// ---------------------------------------------------------------------------
// BuildOperatorStatus
// ---------------------------------------------------------------------------

// BuildOperatorStatus assembles the operator readout from observed
// state. Pure and deterministic: same input → same output, byte for
// byte.
//
// It never fabricates data. A zero CostSummary yields
// VerdictInsufficientData with an Unverified verdict grade, which is
// the honest state for a session that has not run yet.
func BuildOperatorStatus(cfg StatusConfig) OperatorStatus {
	// Derive the modeled delta from the decisions the system made.
	// This is the L10.1 economy model applied to the actual decisions.
	delta := ComputeEconomyDelta(cfg.Depth.Depth, cfg.Tier, cfg.Drift.Mode)

	costGrade := GradeCost(cfg.Cost)
	verdict, verdictGrade := ComputeVerdict(cfg.Cost, delta)

	s := OperatorStatus{
		SessionID:    cfg.SessionID,
		Persona:      cfg.Persona,
		Mode:         cfg.Mode,
		Depth:        cfg.Depth,
		Tier:         cfg.Tier,
		Drift:        cfg.Drift,
		ModeState:    cfg.ModeState,
		Economy:      delta,
		Cost:         cfg.Cost,
		Claims:       buildClaims(cfg, delta, costGrade),
		Verdict:      verdict,
		VerdictGrade: verdictGrade,
	}
	s.Headline = buildHeadline(s)
	return s
}

// buildHeadline renders the one-line answer. It states the verdict
// AND its evidence grade together, so the operator can never read
// "helping" without also reading how solid that word is.
func buildHeadline(s OperatorStatus) string {
	return fmt.Sprintf("%s [%s] — mode=%s depth=%s tier=%s drift=%s",
		s.Verdict, s.VerdictGrade, s.Mode, s.Depth.Depth, s.Tier, s.Drift.Mode)
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// String renders the full operator readout: the headline, the
// decisions with their reasons, and every claim with its grade.
//
// Output is deterministic: claims are an ordered slice, never a map
// range.
func (s OperatorStatus) String() string {
	var b strings.Builder

	b.WriteString("Vibe-Flow Operator Status (Loop 11 L11.1)\n")
	b.WriteString("------------------------------------------------------------\n")
	b.WriteString(fmt.Sprintf("verdict   : %s  (evidence: %s, trusted=%t)\n",
		s.Verdict, s.VerdictGrade, s.VerdictGrade.Trusted()))
	b.WriteString(fmt.Sprintf("headline  : %s\n", s.Headline))
	if s.SessionID != "" {
		b.WriteString(fmt.Sprintf("session   : %s\n", s.SessionID))
	}
	if s.Persona != "" {
		b.WriteString(fmt.Sprintf("persona   : %s\n", s.Persona))
	}
	b.WriteString("\n")

	// Mode state — why we are in this mode.
	b.WriteString("decisions\n")
	b.WriteString(fmt.Sprintf("  mode    : %s (elapsed=%ds tools=%d followups=%d doubts=%d transitions=%d)\n",
		s.Mode,
		int(s.ModeState.Elapsed.Seconds()),
		s.ModeState.ToolCalls, s.ModeState.Followups, s.ModeState.Doubts, s.ModeState.Transitions))
	b.WriteString(fmt.Sprintf("  depth   : %s\n            %s\n", s.Depth.Depth, s.Depth.Reason))
	b.WriteString(fmt.Sprintf("  tier    : %s\n", s.Tier))
	b.WriteString(fmt.Sprintf("  drift   : %s\n            %s\n", s.Drift.Mode, s.Drift.Reason))
	b.WriteString("\n")

	// Claims — every number labeled.
	b.WriteString("claims (every number carries its evidence grade)\n")
	width := 0
	for _, c := range s.Claims {
		if len(c.Label) > width {
			width = len(c.Label)
		}
	}
	for _, c := range s.Claims {
		b.WriteString(fmt.Sprintf("  %-*s %-26s [%s]\n", width, c.Label, c.Value, c.Grade))
		if c.Caveat != "" {
			b.WriteString(fmt.Sprintf("      caveat: %s\n", c.Caveat))
		}
		b.WriteString(fmt.Sprintf("      source: %s\n", c.Source))
	}
	b.WriteString("\n")

	b.WriteString("how to read the grades\n")
	b.WriteString("  measured   : recorded observation, vendor-verified prices — trust as fact\n")
	b.WriteString("  modeled    : deterministic projection over documented assumptions — a plan, not a fact\n")
	b.WriteString("  estimated  : real shape, placeholder prices — verify before acting on the dollars\n")
	b.WriteString("  unverified : no data — the system refuses to answer\n")
	b.WriteString("\n")

	// The honesty footer. Always present, even when everything looks
	// great, so a screenshot of a favorable report still carries the
	// caveat with it.
	//
	// Lines are wrapped at word boundaries but key phrases are never
	// split across a newline — an operator grepping the output (or a
	// test asserting on it) must find these phrases intact.
	b.WriteString("------------------------------------------------------------\n")
	b.WriteString("This readout reports on the system's OWN decisions.\n")
	b.WriteString("It is not an independent evaluation.\n")
	b.WriteString("A favorable verdict means the numbers are self-consistent\n")
	b.WriteString("and self-reported, not that they were audited by anyone else.\n")
	b.WriteString("See CostSession.Disclaimer() for what the cost system does not do.\n")

	return b.String()
}

// JSON-ish export helper: ExportClaims returns the claims as a flat,
// ordered slice suitable for structured logging. Deterministic.
func (s OperatorStatus) ExportClaims() []string {
	out := make([]string, 0, len(s.Claims))
	for _, c := range s.Claims {
		out = append(out, fmt.Sprintf("%s=%s [%s] (%s)", c.Label, c.Value, c.Grade, c.Source))
	}
	return out
}
