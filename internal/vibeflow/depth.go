// Package vibeflow — Loop 8 L8.3: adaptive depth.
//
// This file decides HOW MUCH gate-keeping/spec structure is applied
// to a vibe_publish, given the L8.1a classification (Short/Medium/Long),
// the L8.1b persona context, and the L8.2 runtime mode.
//
// Design constraints (from Phase 20):
//   1. Cost-lens: short tasks should pay the minimum spec overhead.
//      Depth 0 = "no spec, raw LLM call" is a real state.
//   2. Granularity: 6 levels (not 3) so a typo fix and a small refactor
//      don't share a depth. If only 3 levels, the cost-lens would force
//      80% of jobs into the wrong bucket.
//   3. Audit-friendly: DepthDecision records every modifier, so
//      drift_judge can verify the spec matched the assigned depth.
//   4. Reproducible: pure function, no side effects, no time injection.
package vibeflow

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Depth levels
// ---------------------------------------------------------------------------

// Depth is the gate-keeping depth applied to a vibe_publish. Higher
// values mean more spec structure (intent, tasks, constitution, drift).
type Depth int

const (
	// DepthSkip = no spec, raw LLM call. The cheapest possible flow.
	// Used for typo fixes, lookups, and 1-line answers in ModeOff.
	DepthSkip Depth = 0

	// DepthMinimal = 1-sentence intent, no tasks, no drift. Used for
	// small tasks where a spec adds overhead without value.
	DepthMinimal Depth = 1

	// DepthLight = 1-paragraph intent, ≤3 tasks, no drift. Used for
	// medium tasks in cost-lens personas.
	DepthLight Depth = 2

	// DepthStandard = 1-2 paragraphs intent, ≤7 tasks, 1 drift iter.
	// The default for ModeDegraded+Medium.
	DepthStandard Depth = 3

	// DepthFull = 1-2 pages intent, ≤12 tasks, 2 drift iters with
	// re-judge. Used for ModeFull+Medium or ModeDegraded+Long.
	DepthFull Depth = 4

	// DepthExhaustive = full intent (no cap), ≤20 tasks, 3 drift
	// iters with consensus. Used for ModeFull+Long+Expert+C7.
	DepthExhaustive Depth = 5
)

// String returns the canonical depth name.
func (d Depth) String() string {
	switch d {
	case DepthSkip:
		return "skip"
	case DepthMinimal:
		return "minimal"
	case DepthLight:
		return "light"
	case DepthStandard:
		return "standard"
	case DepthFull:
		return "full"
	case DepthExhaustive:
		return "exhaustive"
	default:
		return fmt.Sprintf("unknown(%d)", int(d))
	}
}

// Description returns a one-line human-readable description.
func (d Depth) Description() string {
	switch d {
	case DepthSkip:
		return "no spec, raw LLM call (cheapest)"
	case DepthMinimal:
		return "1-sentence intent, no tasks, no drift"
	case DepthLight:
		return "1 paragraph, ≤3 tasks, no drift"
	case DepthStandard:
		return "1-2 paragraphs, ≤7 tasks, 1 drift iter"
	case DepthFull:
		return "1-2 pages, ≤12 tasks, 2 drift iters"
	case DepthExhaustive:
		return "full intent, ≤20 tasks, 3 drift iters + consensus"
	default:
		return "unknown depth"
	}
}

// ---------------------------------------------------------------------------
// Block sizes (per-depth limits, hints to the LLM)
// ---------------------------------------------------------------------------

// BlockSizes are the per-depth limits. The LLM that generates a spec
// at depth D is told to respect these limits. Drift judge verifies.
//
// Zero values mean "do not emit this block at all" (e.g., DepthSkip
// has IntentMaxWords=0, so no spec.intent is expected).
type BlockSizes struct {
	IntentMaxWords       int // max words in spec.intent (0 = no spec)
	TaskMaxCount         int // max number of spec.tasks (0 = no decomposition)
	TaskDescMaxWords     int // max words per task description
	ConstitutionMaxLines int // max lines in spec.constitution (0 = no constitution)
	DriftIterations      int // number of drift_judge iterations (0 = no drift)
	MemoryWrites         int // max agent_memory writes per task
	SummaryMaxWords      int // max words in post-task summary
}

// BlockSizesFor returns the canonical block sizes for a depth.
func BlockSizesFor(d Depth) BlockSizes {
	switch d {
	case DepthSkip:
		return BlockSizes{
			IntentMaxWords:       0,
			TaskMaxCount:         0,
			TaskDescMaxWords:     0,
			ConstitutionMaxLines: 0,
			DriftIterations:      0,
			MemoryWrites:         0,
			SummaryMaxWords:      0,
		}
	case DepthMinimal:
		return BlockSizes{
			IntentMaxWords:       20,
			TaskMaxCount:         0,
			TaskDescMaxWords:     0,
			ConstitutionMaxLines: 0,
			DriftIterations:      0,
			MemoryWrites:         1,
			SummaryMaxWords:      20,
		}
	case DepthLight:
		return BlockSizes{
			IntentMaxWords:       100,
			TaskMaxCount:         3,
			TaskDescMaxWords:     30,
			ConstitutionMaxLines: 0,
			DriftIterations:      0,
			MemoryWrites:         1,
			SummaryMaxWords:      50,
		}
	case DepthStandard:
		return BlockSizes{
			IntentMaxWords:       200,
			TaskMaxCount:         7,
			TaskDescMaxWords:     50,
			ConstitutionMaxLines: 4,
			DriftIterations:      1,
			MemoryWrites:         3,
			SummaryMaxWords:      100,
		}
	case DepthFull:
		return BlockSizes{
			IntentMaxWords:       500,
			TaskMaxCount:         12,
			TaskDescMaxWords:     80,
			ConstitutionMaxLines: 8,
			DriftIterations:      2,
			MemoryWrites:         5,
			SummaryMaxWords:      200,
		}
	case DepthExhaustive:
		return BlockSizes{
			IntentMaxWords:       2000,
			TaskMaxCount:         20,
			TaskDescMaxWords:     150,
			ConstitutionMaxLines: 20,
			DriftIterations:      3,
			MemoryWrites:         6,
			SummaryMaxWords:      500,
		}
	default:
		return BlockSizes{}
	}
}

// ---------------------------------------------------------------------------
// Decision
// ---------------------------------------------------------------------------

// DepthModifier records a single depth adjustment. Stored in
// DepthDecision.Modifiers for audit.
type DepthModifier struct {
	Name  string // e.g. "tier:expert", "vibe_case:C7", "rush_mode"
	Delta int    // signed change to base depth
}

// DepthDecision is the output of ComputeDepth. Captures the final
// depth plus the reasoning (base + modifiers) so drift_judge can
// verify the spec matched the depth.
type DepthDecision struct {
	Depth     Depth
	Base      int             // base from (Mode, TaskClass) matrix
	Modifiers []DepthModifier // applied in order
	Reason    string          // human-readable summary
	BlockSizes BlockSizes     // resolved sizes
}

// DepthConfig is the input to ComputeDepth. It bundles outputs from
// L8.1a (TaskClass), L8.1b (Context, Features), and L8.2 (Mode).
type DepthConfig struct {
	Mode      Mode     // L8.2 runtime mode
	TaskClass Class    // L8.1a classification: short/medium/long
	VibeCase  string   // C1..C8 (kept as string to avoid import cycle)
	Context   Context  // L8.1b persona context
	Features  Features // L8.1a/b message features
}

// ComputeDepth returns the depth decision for a vibe_publish given
// the current Mode (L8.2), TaskClass (L8.1a), persona Context (L8.1b),
// and message Features (L8.1a/b).
//
// Pure function: deterministic, no time injection, no side effects.
//
// Calibration (in order):
//  1. Base from (Mode, TaskClass) matrix.
//  2. Tier modifier: TierExpert +1, TierNovice -1.
//  3. RushMode modifier: -1 (cost-lens).
//  4. VibeCase modifier: C3 (image, needs deliberation) +1, C7 (mixed
//     bundle, complex) +1. Others 0.
//  5. MultiArtifact modifier: +1 (covers N files).
//  6. Cap at [0, 5].
//
// PanicKeywords is intentionally NOT a depth modifier — the panic
// handling lives in L8.2 (PanicMod: RushMode+Panic → suppress doubt).
// Depth is about gate-keeping, not about panic.
func ComputeDepth(cfg DepthConfig) DepthDecision {
	// 1. Base from matrix
	base := baseFromModeAndClass(cfg.Mode, cfg.TaskClass)
	mods := []DepthModifier{}

	// 2. Tier modifier
	switch cfg.Context.Tier {
	case TierExpert:
		base++
		mods = append(mods, DepthModifier{Name: "tier:expert", Delta: +1})
	case TierNovice:
		base--
		mods = append(mods, DepthModifier{Name: "tier:novice", Delta: -1})
	case TierIntermediate, Tier(""):
		// no change
	}

	// 3. RushMode modifier (cost-lens precedence)
	if cfg.Context.RushMode {
		base--
		mods = append(mods, DepthModifier{Name: "rush_mode", Delta: -1})
	}

	// 4. VibeCase modifier
	switch strings.ToUpper(cfg.VibeCase) {
	case "C3": // image — needs deliberation
		base++
		mods = append(mods, DepthModifier{Name: "vibe_case:C3", Delta: +1})
	case "C7": // mixed bundle — complex
		base++
		mods = append(mods, DepthModifier{Name: "vibe_case:C7", Delta: +1})
	}

	// 5. MultiArtifact modifier (L8.1b feature)
	if cfg.Features.MultiArtifact {
		base++
		mods = append(mods, DepthModifier{Name: "multi_artifact", Delta: +1})
	}

	// 6. Cap at [0, 5]
	capped := capDepth(base)

	return DepthDecision{
		Depth:      capped,
		Base:       baseFromModeAndClass(cfg.Mode, cfg.TaskClass),
		Modifiers:  mods,
		Reason:     reasonFromDecision(baseFromModeAndClass(cfg.Mode, cfg.TaskClass), mods, capped),
		BlockSizes: BlockSizesFor(capped),
	}
}

// baseFromModeAndClass returns the base depth from the (Mode, Class)
// matrix. This is the starting point before modifiers.
func baseFromModeAndClass(mode Mode, class Class) int {
	switch mode {
	case ModeOff:
		switch class {
		case ClassShort:
			return 0
		case ClassMedium:
			return 1
		case ClassLong:
			return 2
		}
	case ModeDegraded:
		switch class {
		case ClassShort:
			return 1
		case ClassMedium:
			return 2
		case ClassLong:
			return 3
		}
	case ModeFull:
		switch class {
		case ClassShort:
			return 2
		case ClassMedium:
			return 3
		case ClassLong:
			return 4
		}
	}
	// Unknown mode/class → safe default
	return 2
}

// capDepth clamps the depth to [0, 5].
func capDepth(d int) Depth {
	if d < 0 {
		return DepthSkip
	}
	if d > 5 {
		return DepthExhaustive
	}
	return Depth(d)
}

// reasonFromDecision builds a one-line human-readable summary of the
// decision (base + modifiers + final).
func reasonFromDecision(base int, mods []DepthModifier, final Depth) string {
	parts := []string{fmt.Sprintf("base=%d", base)}
	for _, m := range mods {
		if m.Delta != 0 {
			sign := "+"
			if m.Delta < 0 {
				sign = ""
			}
			parts = append(parts, fmt.Sprintf("%s%d(%s)", sign, m.Delta, m.Name))
		}
	}
	parts = append(parts, fmt.Sprintf("→ %s(%d)", final, int(final)))
	return strings.Join(parts, " ")
}
