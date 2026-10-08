// Package vibeflow — Loop 9 L9.1: model tier selection.
//
// This file decides WHICH MODEL to use for a given operation, based
// on the depth (L8.3), persona (L8.1b), vibe_case, and features.
// The output is a ModelTier (Fast/Balanced/Strong) which the harness
// maps to a specific provider+model via ModelConfig.
//
// Design constraints (from Phase 20):
//   1. Cost-lens: Depth 0-1 (60%+ of tasks) MUST use TierFast.
//      MiniMax-M3 (TierBalanced) is ~30x more expensive than
//      deepseek-chat (TierFast) per token. A 5min task with 5 drift
//      calls at TierBalanced vs TierFast is a real cost difference.
//   2. Config-driven: ModelConfig lets the operator swap models
//      without code changes. The default is minimax-cn/MiniMax-M3
//      for Balanced/Strong (matches DARK_JUDGE_PROVIDER pin) and
//      deepseek-chat for Fast.
//   3. Deterministic: SelectModelTier is a pure function. Same input
//      always produces the same tier.
//   4. Audit-friendly: ModelSelection includes the reason so drift_judge
//      can verify the right model was used.
package vibeflow

import "fmt"

// ---------------------------------------------------------------------------
// Tier enum
// ---------------------------------------------------------------------------

// ModelTier is the strength tier of the LLM. Higher = stronger, more
// expensive, slower.
type ModelTier int

const (
	// TierFast = cheap, fast, lower quality. Used for shallow tasks
	// (Depth 0-1) and cost-lens personas. Typical candidates:
	// deepseek-chat, gpt-4o-mini, claude-haiku.
	TierFast ModelTier = iota

	// TierBalanced = mid-tier, default for most. Typical candidates:
	// MiniMax-M3, sonnet-3.5, gpt-4o.
	TierBalanced

	// TierStrong = premium, for complex tasks. Typical candidates:
	// MiniMax-M3-deep, opus, o1, deepseek-r1.
	TierStrong
)

// String returns the canonical tier name.
func (t ModelTier) String() string {
	switch t {
	case TierFast:
		return "fast"
	case TierBalanced:
		return "balanced"
	case TierStrong:
		return "strong"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// Description returns a one-line description for logs.
func (t ModelTier) Description() string {
	switch t {
	case TierFast:
		return "fast (cheap, low quality) — e.g. deepseek-chat"
	case TierBalanced:
		return "balanced (mid-tier, default) — e.g. MiniMax-M3"
	case TierStrong:
		return "strong (premium, for complex) — e.g. MiniMax-M3-deep"
	default:
		return "unknown tier"
	}
}

// ---------------------------------------------------------------------------
// Model config
// ---------------------------------------------------------------------------

// ModelConfig maps each tier to a (provider, model) pair. The
// operator can override via spec or env. DefaultModelConfig matches
// the current DARK_JUDGE_PROVIDER pin (minimax-cn, MiniMax-M3).
type ModelConfig struct {
	FastProvider     string
	FastModel        string
	BalancedProvider string
	BalancedModel    string
	StrongProvider   string
	StrongModel      string
}

// DefaultModelConfig is the default mapping. TierBalanced and
// TierStrong both use minimax-cn / MiniMax-M3 to match the existing
// judge pin; TierFast uses deepseek-chat as a cheap alternative.
var DefaultModelConfig = ModelConfig{
	FastProvider:     "deepseek",
	FastModel:        "deepseek-chat",
	BalancedProvider: "minimax-cn",
	BalancedModel:    "MiniMax-M3",
	StrongProvider:   "minimax-cn",
	StrongModel:      "MiniMax-M3",
}

// ModelForTier returns the (provider, model) pair for a given tier.
func ModelForTier(tier ModelTier, cfg ModelConfig) (provider, model string) {
	switch tier {
	case TierFast:
		return cfg.FastProvider, cfg.FastModel
	case TierBalanced:
		return cfg.BalancedProvider, cfg.BalancedModel
	case TierStrong:
		return cfg.StrongProvider, cfg.StrongModel
	default:
		// Unknown tier: safe default to balanced
		return cfg.BalancedProvider, cfg.BalancedModel
	}
}

// ---------------------------------------------------------------------------
// Tier selection
// ---------------------------------------------------------------------------

// ModelSelection is the output of SelectModel. Captures the tier,
// the resolved provider+model, and a one-line reason for audit.
type ModelSelection struct {
	Tier     ModelTier
	Provider string
	Model    string
	Reason   string
}

// SelectModelTier returns the model tier for a given (depth, persona,
// vibe_case, features). Pure function, deterministic.
//
// Selection rules (in order, first match wins):
//  1. Depth ≤ 1 → Fast (no drift anyway; cheap just in case)
//  2. RushMode OR TierNovice AND Depth ≤ 3 → Fast (cost-lens)
//  3. MultiArtifact → Strong (N files need strong judgment)
//  4. VibeCase C7 → Strong (mixed bundle, complex)
//  5. VibeCase C3/C4/C5/C6 → Balanced (media/multi-modal, need capability)
//  6. Depth ≥ 5 → Strong (exhaustive needs strong)
//  7. Default → Balanced
func SelectModelTier(depth Depth, ctx Context, vibeCase string, feats Features) ModelTier {
	// 1. Shallow depth: fast
	if depth <= DepthMinimal {
		return TierFast
	}

	// 2. Cost-lens: rush or novice on depth 2-3
	if (ctx.RushMode || ctx.Tier == TierNovice) && depth <= DepthStandard {
		return TierFast
	}

	// 3. MultiArtifact: complex scope
	if feats.MultiArtifact {
		return TierStrong
	}

	// 4. C7 (mixed bundle): consensus territory
	if vibeCase == "C7" {
		return TierStrong
	}

	// 5. Media / multi-modal: need capability
	switch vibeCase {
	case "C3", "C4", "C5", "C6":
		return TierBalanced
	}

	// 6. Exhaustive depth
	if depth >= DepthExhaustive {
		return TierStrong
	}

	// 7. Default: balanced
	return TierBalanced
}

// SelectModel bundles tier selection + provider/model resolution + reason.
func SelectModel(depth Depth, ctx Context, vibeCase string, feats Features, cfg ModelConfig) ModelSelection {
	tier := SelectModelTier(depth, ctx, vibeCase, feats)
	provider, model := ModelForTier(tier, cfg)
	reason := fmt.Sprintf(
		"tier=%s for depth=%s persona=(tier=%s rush=%t) vibe=%s",
		tier, depth, ctx.Tier, ctx.RushMode, vibeCase,
	)
	return ModelSelection{
		Tier:     tier,
		Provider: provider,
		Model:    model,
		Reason:   reason,
	}
}
