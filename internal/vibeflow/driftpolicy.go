// Package vibeflow — Loop 9 L9.2: drift skip policy.
//
// This file decides WHETHER to run drift_judge (and how many times)
// for a given operation, based on depth (L8.3), persona (L8.1b),
// vibe_case, and features (L8.1b).
//
// Five skip modes (cheapest → most expensive):
//   - Skip:       0 calls. Trust the output. For trivial tasks.
//   - SelfJudge:  0 calls. Model evaluates its own output inline.
//                 Cheaper than a separate drift_judge call but less
//                 unbiased. Used when model tier is Fast and the
//                 task is simple enough that bias is acceptable.
//   - Single:     1 call. Default. One drift_judge verdict.
//   - Multi:      2 calls. Two drift_judge verdicts (different
//                 specs or N=2 consensus). For complex code/media.
//   - Consensus:  3 calls. N=3 consensus. For critical surface,
//                 mixed bundles, exhaustive depth.
//
// Design constraints (from Phase 20):
//   1. Cost-lens: ≥6/10 personas at a default task should be Single
//      or cheaper. Skip/SelfJudge/Single for routine; Multi only
//      for media/code; Consensus only for critical.
//   2. Critical surface always wins: drift_judge code itself,
//      security mod, constitution changes — all force Consensus.
//   3. Deterministic: pure function. Same input → same policy.
//   4. Audit-friendly: Reason + EstCalls captured for drift_judge
//      to verify the right policy was used.
package vibeflow

import "fmt"

// ---------------------------------------------------------------------------
// Skip mode enum
// ---------------------------------------------------------------------------

// DriftSkip is the policy for how many drift_judge calls to make.
// Ordered from cheapest (0 calls) to most expensive (3 calls).
type DriftSkip int

const (
	// SkipNoDrift = 0 calls. Trust the output.
	SkipNoDrift DriftSkip = iota

	// SkipSelfJudge = 0 external calls, but the model evaluates its
	// own output inline as part of the generation call. Cheaper
	// than a separate drift_judge call but biased toward the model's
	// own answer.
	SkipSelfJudge

	// SkipSingle = 1 drift_judge call. The default.
	SkipSingle

	// SkipMulti = 2 drift_judge calls. Two verdicts.
	SkipMulti

	// SkipConsensus = 3 drift_judge calls. N=3 consensus.
	SkipConsensus
)

// String returns the canonical skip mode name.
func (d DriftSkip) String() string {
	switch d {
	case SkipNoDrift:
		return "skip"
	case SkipSelfJudge:
		return "selfjudge"
	case SkipSingle:
		return "single"
	case SkipMulti:
		return "multi"
	case SkipConsensus:
		return "consensus"
	default:
		return fmt.Sprintf("unknown(%d)", int(d))
	}
}

// Description returns a one-line description for logs.
func (d DriftSkip) Description() string {
	switch d {
	case SkipNoDrift:
		return "skip (0 calls — trust the output)"
	case SkipSelfJudge:
		return "selfjudge (0 external calls — inline self-eval)"
	case SkipSingle:
		return "single (1 drift_judge call — default)"
	case SkipMulti:
		return "multi (2 drift_judge calls — for code/media)"
	case SkipConsensus:
		return "consensus (3 drift_judge calls — N=3)"
	default:
		return "unknown skip mode"
	}
}

// EstCalls returns the estimated number of external drift_judge calls
// for this policy. SelfJudge is 0 (inline).
func (d DriftSkip) EstCalls() int {
	switch d {
	case SkipNoDrift:
		return 0
	case SkipSelfJudge:
		return 0
	case SkipSingle:
		return 1
	case SkipMulti:
		return 2
	case SkipConsensus:
		return 3
	default:
		return 1 // safe default
	}
}

// ---------------------------------------------------------------------------
// Policy output
// ---------------------------------------------------------------------------

// DriftPolicy is the output of ComputeDriftPolicy. Captures the
// skip mode, the resolved number of calls, and a one-line reason
// for audit.
type DriftPolicy struct {
	Mode     DriftSkip
	Reason   string
	EstCalls int
}

// ---------------------------------------------------------------------------
// Policy computation
// ---------------------------------------------------------------------------

// ComputeDriftPolicy returns the drift skip policy for a given
// (depth, persona, vibe_case, features, critical-surface flag).
// Pure function, deterministic.
//
// Selection rules (in order, first match wins):
//  1. Critical surface                    -> Consensus
//  2. VibeCase C7 (mixed bundle)          -> Consensus
//  3. Depth >= 5 (exhaustive)             -> Consensus
//  4. Depth >= 4 OR MultiArtifact         -> Multi
//  5. VibeCase C3/C4/C5/C6 (media)        -> Multi
//  6. HasCodeSignals AND Depth >= 3       -> Multi
//  7. Depth <= 1 AND !MultiArtifact       -> Skip
//  8. TierNovice AND Depth <= 2           -> SelfJudge
//  9. Default                             -> Single
//
// The isCritical flag is set by the harness for surfaces that
// require the strictest evaluation: drift_judge code, security mod,
// constitution, audit-trail code.
func ComputeDriftPolicy(
	depth Depth,
	ctx Context,
	vibeCase string,
	feats Features,
	isCritical bool,
) DriftPolicy {
	// 1. Critical surface: always consensus.
	if isCritical {
		return policy(SkipConsensus, "critical surface requires consensus", depth, ctx, vibeCase, feats)
	}

	// 2. C7: mixed bundle — too varied for single judge.
	if vibeCase == "C7" {
		return policy(SkipConsensus, "vibe_case C7 mixed bundle requires consensus", depth, ctx, vibeCase, feats)
	}

	// 3. Exhaustive depth.
	if depth >= DepthExhaustive {
		return policy(SkipConsensus, fmt.Sprintf("depth=%s exhaustive requires consensus", depth), depth, ctx, vibeCase, feats)
	}

	// 4. Full depth or multi-artifact.
	if depth >= DepthFull || feats.MultiArtifact {
		return policy(SkipMulti, "depth=full or multi-artifact requires multi", depth, ctx, vibeCase, feats)
	}

	// 5. Media / multi-modal: need more eyes.
	switch vibeCase {
	case "C3", "C4", "C5", "C6":
		return policy(SkipMulti, fmt.Sprintf("vibe_case %s media requires multi", vibeCase), depth, ctx, vibeCase, feats)
	}

	// 6. Code changes on non-trivial depth: more review.
	if feats.HasCodeSignals && depth >= DepthStandard {
		return policy(SkipMulti, "code signals at depth>=standard require multi", depth, ctx, vibeCase, feats)
	}

	// 7. Shallow depth, single artifact: trust.
	if depth <= DepthMinimal && !feats.MultiArtifact {
		return policy(SkipNoDrift, fmt.Sprintf("depth=%s minimal, trust the output", depth), depth, ctx, vibeCase, feats)
	}

	// 8. Novice persona on light task: self-judge (cheap inline).
	// Rationale: novice persona → L9.1 already chose TierFast model.
	// Fast model evaluating its own output is biased but cheap; the
	// task is light enough that the bias is acceptable.
	if ctx.Tier == TierNovice && depth <= DepthLight {
		return policy(SkipSelfJudge, "novice persona depth<=light uses selfjudge", depth, ctx, vibeCase, feats)
	}

	// 9. Default: single.
	return policy(SkipSingle, "default single drift call", depth, ctx, vibeCase, feats)
}

// policy is a small constructor that builds a DriftPolicy with a
// canonical reason string. Keeps the rule code readable.
func policy(mode DriftSkip, reason string, depth Depth, ctx Context, vibeCase string, feats Features) DriftPolicy {
	return DriftPolicy{
		Mode:     mode,
		Reason:   reason,
		EstCalls: mode.EstCalls(),
	}
}
