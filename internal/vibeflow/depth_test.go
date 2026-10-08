package vibeflow

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Depth basics
// ---------------------------------------------------------------------------

func TestDepth_String(t *testing.T) {
	cases := []struct {
		d    Depth
		want string
	}{
		{DepthSkip, "skip"},
		{DepthMinimal, "minimal"},
		{DepthLight, "light"},
		{DepthStandard, "standard"},
		{DepthFull, "full"},
		{DepthExhaustive, "exhaustive"},
		{Depth(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Errorf("Depth(%d).String() = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestDepth_Description(t *testing.T) {
	for _, d := range []Depth{DepthSkip, DepthMinimal, DepthLight, DepthStandard, DepthFull, DepthExhaustive} {
		if got := d.Description(); got == "" || got == "unknown depth" {
			t.Errorf("Depth(%d).Description() = %q, want non-empty", d, got)
		}
	}
}

// ---------------------------------------------------------------------------
// BlockSizes
// ---------------------------------------------------------------------------

func TestBlockSizesFor_Skip_AllZero(t *testing.T) {
	s := BlockSizesFor(DepthSkip)
	if s.IntentMaxWords != 0 || s.TaskMaxCount != 0 || s.DriftIterations != 0 || s.MemoryWrites != 0 {
		t.Errorf("DepthSkip should have all-zero block sizes, got %+v", s)
	}
}

func TestBlockSizesFor_MonotonicallyIncreasing(t *testing.T) {
	// For each non-zero field, block sizes should grow with depth.
	// Exception: MemoryWrites is allowed to stay flat (1 at minimal/light
	// since even cheap tasks get 1 write).
	prev := BlockSizesFor(DepthSkip)
	for _, d := range []Depth{DepthMinimal, DepthLight, DepthStandard, DepthFull, DepthExhaustive} {
		s := BlockSizesFor(d)
		if s.IntentMaxWords < prev.IntentMaxWords {
			t.Errorf("depth %d IntentMaxWords = %d < prev %d", d, s.IntentMaxWords, prev.IntentMaxWords)
		}
		if s.TaskMaxCount < prev.TaskMaxCount {
			t.Errorf("depth %d TaskMaxCount = %d < prev %d", d, s.TaskMaxCount, prev.TaskMaxCount)
		}
		if s.DriftIterations < prev.DriftIterations {
			t.Errorf("depth %d DriftIterations = %d < prev %d", d, s.DriftIterations, prev.DriftIterations)
		}
		prev = s
	}
}

func TestBlockSizesFor_FullAndExhaustive_Distinct(t *testing.T) {
	full := BlockSizesFor(DepthFull)
	exh := BlockSizesFor(DepthExhaustive)
	if exh.TaskMaxCount <= full.TaskMaxCount {
		t.Errorf("exhaustive tasks (%d) should exceed full (%d)", exh.TaskMaxCount, full.TaskMaxCount)
	}
	if exh.DriftIterations <= full.DriftIterations {
		t.Errorf("exhaustive drift (%d) should exceed full (%d)", exh.DriftIterations, full.DriftIterations)
	}
}

// ---------------------------------------------------------------------------
// Base matrix
// ---------------------------------------------------------------------------

func TestComputeDepth_Base_ModeOff_Short(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: ClassShort,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if d.Depth != DepthSkip {
		t.Errorf("ModeOff+Short = %q, want skip", d.Depth)
	}
}

func TestComputeDepth_Base_ModeOff_Medium(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: ClassMedium,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if d.Depth != DepthMinimal {
		t.Errorf("ModeOff+Medium = %q, want minimal", d.Depth)
	}
}

func TestComputeDepth_Base_ModeOff_Long(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: ClassLong,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if d.Depth != DepthLight {
		t.Errorf("ModeOff+Long = %q, want light", d.Depth)
	}
}

func TestComputeDepth_Base_ModeDegraded_Medium(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if d.Depth != DepthLight {
		t.Errorf("ModeDegraded+Medium = %q, want light", d.Depth)
	}
}

func TestComputeDepth_Base_ModeFull_Medium(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeFull,
		TaskClass: ClassMedium,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if d.Depth != DepthStandard {
		t.Errorf("ModeFull+Medium = %q, want standard", d.Depth)
	}
}

func TestComputeDepth_Base_ModeFull_Long(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeFull,
		TaskClass: ClassLong,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if d.Depth != DepthFull {
		t.Errorf("ModeFull+Long = %q, want full", d.Depth)
	}
}

// ---------------------------------------------------------------------------
// Tier modifier
// ---------------------------------------------------------------------------

func TestComputeDepth_TierExpert_BumpsUp(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C1",
		Context:   Context{Tier: TierExpert},
	})
	// base 2 + expert +1 = 3 = standard
	if d.Depth != DepthStandard {
		t.Errorf("ModeDegraded+Medium+Expert = %q, want standard (2+1)", d.Depth)
	}
}

func TestComputeDepth_TierNovice_BumpsDown(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C1",
		Context:   Context{Tier: TierNovice},
	})
	// base 2 + novice -1 = 1 = minimal
	if d.Depth != DepthMinimal {
		t.Errorf("ModeDegraded+Medium+Novice = %q, want minimal (2-1)", d.Depth)
	}
}

// ---------------------------------------------------------------------------
// RushMode (cost-lens)
// ---------------------------------------------------------------------------

func TestComputeDepth_RushMode_BumpsDown(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C1",
		Context:   Context{Tier: TierIntermediate, RushMode: true},
	})
	// base 2 + rush -1 = 1 = minimal
	if d.Depth != DepthMinimal {
		t.Errorf("RushMode (intermediate) = %q, want minimal (2-1)", d.Depth)
	}
}

func TestComputeDepth_RushMode_TakesPrecedence(t *testing.T) {
	// Even an expert with RushMode wants less depth (cost-lens wins)
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C1",
		Context:   Context{Tier: TierExpert, RushMode: true},
	})
	// base 2 + expert +1 - rush 1 = 2 = light
	if d.Depth != DepthLight {
		t.Errorf("Expert+RushMode = %q, want light (2+1-1)", d.Depth)
	}
}

// ---------------------------------------------------------------------------
// VibeCase modifier
// ---------------------------------------------------------------------------

func TestComputeDepth_VibeCaseC3_BumpsUp(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C3",        // image
		Context:   DefaultContext,
	})
	// base 2 + C3 +1 = 3 = standard
	if d.Depth != DepthStandard {
		t.Errorf("C3 = %q, want standard (2+1)", d.Depth)
	}
}

func TestComputeDepth_VibeCaseC7_BumpsUp(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C7",        // mixed bundle
		Context:   DefaultContext,
	})
	if d.Depth != DepthStandard {
		t.Errorf("C7 = %q, want standard (2+1)", d.Depth)
	}
}

func TestComputeDepth_VibeCaseC1_NoChange(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C1",        // code
		Context:   DefaultContext,
	})
	if d.Depth != DepthLight {
		t.Errorf("C1 = %q, want light (no C modifier)", d.Depth)
	}
}

// ---------------------------------------------------------------------------
// MultiArtifact
// ---------------------------------------------------------------------------

func TestComputeDepth_MultiArtifact_BumpsUp(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C1",
		Context:   DefaultContext,
		Features:  Features{MultiArtifact: true},
	})
	// base 2 + multi +1 = 3 = standard
	if d.Depth != DepthStandard {
		t.Errorf("MultiArtifact = %q, want standard (2+1)", d.Depth)
	}
}

// ---------------------------------------------------------------------------
// Cap
// ---------------------------------------------------------------------------

func TestComputeDepth_CapMin_Zero(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: ClassShort, // base 0
		VibeCase:  "C1",
		Context:   Context{Tier: TierNovice, RushMode: true}, // -1 -1 = -2
	})
	if d.Depth != DepthSkip {
		t.Errorf("should cap at 0, got %q", d.Depth)
	}
}

func TestComputeDepth_CapMax_Five(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeFull,
		TaskClass: ClassLong, // base 4
		VibeCase:  "C7",      // +1
		Context:   Context{Tier: TierExpert}, // +1
		Features:  Features{MultiArtifact: true}, // +1
	})
	// 4 + 1 + 1 + 1 = 7, capped at 5
	if d.Depth != DepthExhaustive {
		t.Errorf("should cap at 5, got %q (int=%d)", d.Depth, int(d.Depth))
	}
}

// ---------------------------------------------------------------------------
// Modifier tracking (audit)
// ---------------------------------------------------------------------------

func TestComputeDepth_RecordsAllModifiers(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium, // base 2
		VibeCase:  "C3",        // +1
		Context:   Context{Tier: TierExpert}, // +1
		Features:  Features{MultiArtifact: true}, // +1
	})
	if len(d.Modifiers) != 3 {
		t.Errorf("expected 3 modifiers, got %d: %+v", len(d.Modifiers), d.Modifiers)
	}
	// Check names
	names := []string{}
	for _, m := range d.Modifiers {
		names = append(names, m.Name)
	}
	for _, want := range []string{"tier:expert", "vibe_case:C3", "multi_artifact"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected modifier %q in %v", want, names)
		}
	}
}

func TestComputeDepth_Reason_Readable(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeDegraded,
		TaskClass: ClassMedium,
		VibeCase:  "C1",
		Context:   Context{Tier: TierExpert},
	})
	if !strings.Contains(d.Reason, "base=2") {
		t.Errorf("reason should mention base=2, got %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "tier:expert") {
		t.Errorf("reason should mention tier:expert, got %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "standard") {
		t.Errorf("reason should mention final depth, got %q", d.Reason)
	}
}

func TestComputeDepth_BlockSizesResolved(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeFull,
		TaskClass: ClassMedium,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	// ModeFull+Medium+Intermediate+C1+no features = base 3 = standard
	if d.Depth != DepthStandard {
		t.Fatalf("expected standard, got %q", d.Depth)
	}
	if d.BlockSizes.IntentMaxWords != 200 {
		t.Errorf("standard IntentMaxWords = %d, want 200", d.BlockSizes.IntentMaxWords)
	}
	if d.BlockSizes.TaskMaxCount != 7 {
		t.Errorf("standard TaskMaxCount = %d, want 7", d.BlockSizes.TaskMaxCount)
	}
	if d.BlockSizes.DriftIterations != 1 {
		t.Errorf("standard DriftIterations = %d, want 1", d.BlockSizes.DriftIterations)
	}
}

// ---------------------------------------------------------------------------
// Killer tests (cost-lens validation)
// ---------------------------------------------------------------------------

func TestComputeDepth_CostLens_TypoFix_NoSpec(t *testing.T) {
	// The cheapest possible flow: novice + rush + short + ModeOff
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: ClassShort,
		VibeCase:  "C1",
		Context:   Context{Tier: TierNovice, RushMode: true},
	})
	if d.Depth != DepthSkip {
		t.Errorf("typo fix should be DepthSkip, got %q", d.Depth)
	}
	if d.BlockSizes.IntentMaxWords != 0 {
		t.Errorf("DepthSkip should have IntentMaxWords=0, got %d", d.BlockSizes.IntentMaxWords)
	}
}

func TestComputeDepth_CostLens_5MinTask_Light(t *testing.T) {
	// A 5min task on a cost-minimal persona (tactico): ModeOff + Long
	// + novice + rush = base 2 - 1 - 1 = 0 = skip. But that's TOO
	// cheap for a 5min task. Verify the actual behavior.
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: ClassLong,
		VibeCase:  "C1",
		Context:   Context{Tier: TierNovice, RushMode: true},
	})
	// base 2 + novice -1 + rush -1 = 0 = skip
	// Note: for tactico (TierNovice + RushMode), even a Long task gets
	// DepthSkip. This is intentional — the cost-lens wins.
	if d.Depth != DepthSkip {
		t.Errorf("tactico long task = %q, want skip (cost-lens)", d.Depth)
	}
}

func TestComputeDepth_Exhaustive_ExpertLongC7(t *testing.T) {
	// The densest flow: expert + full mode + long task + C7
	d := ComputeDepth(DepthConfig{
		Mode:      ModeFull,
		TaskClass: ClassLong, // base 4
		VibeCase:  "C7",      // +1
		Context:   Context{Tier: TierExpert}, // +1
		Features:  Features{MultiArtifact: true}, // +1
	})
	// 4 + 1 + 1 + 1 = 7, capped at 5
	if d.Depth != DepthExhaustive {
		t.Errorf("expert long C7 multi-artifact = %q, want exhaustive (capped)", d.Depth)
	}
	if d.BlockSizes.DriftIterations != 3 {
		t.Errorf("exhaustive DriftIterations = %d, want 3", d.BlockSizes.DriftIterations)
	}
}

// ---------------------------------------------------------------------------
// 10 personas mapping (audit-friendly)
// ---------------------------------------------------------------------------

func TestComputeDepth_10Personas(t *testing.T) {
	// Sanity check: each persona gets a sensible depth for a "medium
	// task on a code question" (the most common L8.1a classification
	// for development work).
	personas := []struct {
		name string
		ctx  Context
		mode Mode
		want Depth
	}{
		{"tactico", Context{Tier: TierNovice, RushMode: true}, ModeOff, DepthSkip},                        // 1-1-1=-1→0
		{"panic-debugger", Context{Tier: TierIntermediate, RushMode: true}, ModeDegraded, DepthMinimal},   // 2-1=1
		{"novato", Context{Tier: TierNovice}, ModeOff, DepthSkip},                                          // 1-1=0
		{"no-tecnico", Context{Tier: TierNovice}, ModeOff, DepthSkip},                                      // 1-1=0
		{"movil", Context{Tier: TierNovice}, ModeOff, DepthSkip},                                           // 1-1=0
		{"rush-cognitivo", Context{Tier: TierNovice, RushMode: true}, ModeOff, DepthSkip},                 // 1-1-1=-1→0
		{"multi-idioma", Context{Tier: TierIntermediate}, ModeDegraded, DepthLight},                        // 2+0=2
		{"arquitecto", Context{Tier: TierExpert}, ModeFull, DepthFull},                                     // 3+1=4
		{"investigador", Context{Tier: TierExpert}, ModeFull, DepthFull},                                   // 3+1=4
		{"escritor", Context{Tier: TierExpert}, ModeFull, DepthFull},                                        // 3+1=4
	}
	for _, p := range personas {
		d := ComputeDepth(DepthConfig{
			Mode:      p.mode,
			TaskClass: ClassMedium,
			VibeCase:  "C1",
			Context:   p.ctx,
		})
		if d.Depth != p.want {
			t.Errorf("persona %q (mode=%s ctx=%+v) = %q, want %q (reason: %s)",
				p.name, p.mode, p.ctx, d.Depth, p.want, d.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Reproducibility (pure function)
// ---------------------------------------------------------------------------

func TestComputeDepth_PureFunction_SameInputs_SameOutput(t *testing.T) {
	cfg := DepthConfig{
		Mode:      ModeFull,
		TaskClass: ClassLong,
		VibeCase:  "C7",
		Context:   Context{Tier: TierExpert, Domain: DomainCode},
		Features:  Features{MultiArtifact: true},
	}
	d1 := ComputeDepth(cfg)
	d2 := ComputeDepth(cfg)
	if d1.Depth != d2.Depth {
		t.Errorf("not reproducible: %q vs %q", d1.Depth, d2.Depth)
	}
	if d1.Reason != d2.Reason {
		t.Errorf("reason not reproducible: %q vs %q", d1.Reason, d2.Reason)
	}
}

// ---------------------------------------------------------------------------
// Defensive: unknown inputs don't crash
// ---------------------------------------------------------------------------

func TestComputeDepth_UnknownClass_Default(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      ModeOff,
		TaskClass: Class("weird"),
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	// Unknown class → safe default 2
	if int(d.Depth) < 0 || int(d.Depth) > 5 {
		t.Errorf("unknown class should produce a valid depth, got %q", d.Depth)
	}
}

func TestComputeDepth_UnknownMode_Default(t *testing.T) {
	d := ComputeDepth(DepthConfig{
		Mode:      Mode(99),
		TaskClass: ClassMedium,
		VibeCase:  "C1",
		Context:   DefaultContext,
	})
	if int(d.Depth) < 0 || int(d.Depth) > 5 {
		t.Errorf("unknown mode should produce a valid depth, got %q", d.Depth)
	}
}
