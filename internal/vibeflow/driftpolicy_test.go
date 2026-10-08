package vibeflow

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Skip mode basics
// ---------------------------------------------------------------------------

func TestDriftSkip_String(t *testing.T) {
	cases := []struct {
		d    DriftSkip
		want string
	}{
		{SkipNoDrift, "skip"},
		{SkipSelfJudge, "selfjudge"},
		{SkipSingle, "single"},
		{SkipMulti, "multi"},
		{SkipConsensus, "consensus"},
		{DriftSkip(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.want {
			t.Errorf("DriftSkip(%d).String() = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestDriftSkip_Description(t *testing.T) {
	for _, d := range []DriftSkip{SkipNoDrift, SkipSelfJudge, SkipSingle, SkipMulti, SkipConsensus} {
		if got := d.Description(); got == "" || got == "unknown skip mode" {
			t.Errorf("DriftSkip(%d).Description() = %q, want non-empty", d, got)
		}
	}
}

func TestDriftSkip_EstCalls(t *testing.T) {
	cases := []struct {
		d    DriftSkip
		want int
	}{
		{SkipNoDrift, 0},
		{SkipSelfJudge, 0},
		{SkipSingle, 1},
		{SkipMulti, 2},
		{SkipConsensus, 3},
		{DriftSkip(99), 1}, // safe default
	}
	for _, c := range cases {
		if got := c.d.EstCalls(); got != c.want {
			t.Errorf("DriftSkip(%d).EstCalls() = %d, want %d", c.d, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Depth-based rules
// ---------------------------------------------------------------------------

func TestPolicy_Depth0_Skip(t *testing.T) {
	p := ComputeDriftPolicy(DepthSkip, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipNoDrift {
		t.Errorf("DepthSkip = %q, want skip", p.Mode)
	}
	if p.EstCalls != 0 {
		t.Errorf("DepthSkip EstCalls = %d, want 0", p.EstCalls)
	}
}

func TestPolicy_Depth1_Skip(t *testing.T) {
	p := ComputeDriftPolicy(DepthMinimal, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipNoDrift {
		t.Errorf("DepthMinimal = %q, want skip", p.Mode)
	}
}

func TestPolicy_Depth2_DefaultSingle(t *testing.T) {
	p := ComputeDriftPolicy(DepthLight, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipSingle {
		t.Errorf("DepthLight (default) = %q, want single", p.Mode)
	}
}

func TestPolicy_Depth3_DefaultSingle(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipSingle {
		t.Errorf("DepthStandard (default) = %q, want single", p.Mode)
	}
}

func TestPolicy_Depth4_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthFull, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipMulti {
		t.Errorf("DepthFull = %q, want multi", p.Mode)
	}
}

func TestPolicy_Depth5_Consensus(t *testing.T) {
	p := ComputeDriftPolicy(DepthExhaustive, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipConsensus {
		t.Errorf("DepthExhaustive = %q, want consensus", p.Mode)
	}
}

// ---------------------------------------------------------------------------
// Critical surface and vibe_case overrides
// ---------------------------------------------------------------------------

func TestPolicy_CriticalSurface_Consensus(t *testing.T) {
	// Even at the shallowest depth, critical surface forces consensus.
	p := ComputeDriftPolicy(DepthSkip, DefaultContext, "C1", Features{}, true)
	if p.Mode != SkipConsensus {
		t.Errorf("critical surface = %q, want consensus (depth rule must yield)", p.Mode)
	}
}

func TestPolicy_C7_Consensus(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C7", Features{}, false)
	if p.Mode != SkipConsensus {
		t.Errorf("C7 = %q, want consensus (mixed bundle)", p.Mode)
	}
}

func TestPolicy_C7_AtLowDepth_StillConsensus(t *testing.T) {
	// C7 overrides even shallow depth.
	p := ComputeDriftPolicy(DepthLight, DefaultContext, "C7", Features{}, false)
	if p.Mode != SkipConsensus {
		t.Errorf("C7 at DepthLight = %q, want consensus (vibe_case overrides depth)", p.Mode)
	}
}

func TestPolicy_C3_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C3", Features{}, false)
	if p.Mode != SkipMulti {
		t.Errorf("C3 = %q, want multi (media)", p.Mode)
	}
}

func TestPolicy_C4_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C4", Features{}, false)
	if p.Mode != SkipMulti {
		t.Errorf("C4 = %q, want multi (media)", p.Mode)
	}
}

func TestPolicy_C5_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C5", Features{}, false)
	if p.Mode != SkipMulti {
		t.Errorf("C5 = %q, want multi (media)", p.Mode)
	}
}

func TestPolicy_C6_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C6", Features{}, false)
	if p.Mode != SkipMulti {
		t.Errorf("C6 = %q, want multi (media)", p.Mode)
	}
}

func TestPolicy_C1_DefaultSingle(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C1", Features{}, false)
	if p.Mode != SkipSingle {
		t.Errorf("C1 default = %q, want single", p.Mode)
	}
}

func TestPolicy_C2_DefaultSingle(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C2", Features{}, false)
	if p.Mode != SkipSingle {
		t.Errorf("C2 default = %q, want single", p.Mode)
	}
}

// ---------------------------------------------------------------------------
// Feature-based rules
// ---------------------------------------------------------------------------

func TestPolicy_MultiArtifact_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C1", Features{MultiArtifact: true}, false)
	if p.Mode != SkipMulti {
		t.Errorf("MultiArtifact = %q, want multi (regardless of depth)", p.Mode)
	}
}

func TestPolicy_MultiArtifact_OverridesSkip(t *testing.T) {
	// MultiArtifact must override the depth<=1 -> Skip rule.
	p := ComputeDriftPolicy(DepthMinimal, DefaultContext, "C1", Features{MultiArtifact: true}, false)
	if p.Mode != SkipMulti {
		t.Errorf("MultiArtifact at DepthMinimal = %q, want multi (overrides skip)", p.Mode)
	}
}

func TestPolicy_HasCodeSignals_AtStandard_Multi(t *testing.T) {
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C1", Features{HasCodeSignals: true}, false)
	if p.Mode != SkipMulti {
		t.Errorf("HasCodeSignals at DepthStandard = %q, want multi (code review)", p.Mode)
	}
}

func TestPolicy_HasCodeSignals_AtLight_StillSingle(t *testing.T) {
	// Code signals only matter at depth>=standard.
	p := ComputeDriftPolicy(DepthLight, DefaultContext, "C1", Features{HasCodeSignals: true}, false)
	if p.Mode != SkipSingle {
		t.Errorf("HasCodeSignals at DepthLight = %q, want single (light code review OK)", p.Mode)
	}
}

func TestPolicy_PanicKeywords_NoOverride(t *testing.T) {
	// Panic keywords should NOT slow down: don't go above Single.
	p := ComputeDriftPolicy(DepthStandard, DefaultContext, "C1", Features{PanicKeywords: true}, false)
	if p.Mode != SkipSingle {
		t.Errorf("PanicKeywords = %q, want single (don't slow panic)", p.Mode)
	}
}

// ---------------------------------------------------------------------------
// SelfJudge rule
// ---------------------------------------------------------------------------

func TestPolicy_TierNovice_Depth0_SkipNotSelfJudge(t *testing.T) {
	// Depth 0 is Skip even for novice.
	p := ComputeDriftPolicy(DepthSkip, Context{Tier: TierNovice}, "C1", Features{}, false)
	if p.Mode != SkipNoDrift {
		t.Errorf("Novice DepthSkip = %q, want skip (depth rule fires first)", p.Mode)
	}
}

func TestPolicy_TierNovice_Depth2_SelfJudge(t *testing.T) {
	p := ComputeDriftPolicy(DepthLight, Context{Tier: TierNovice}, "C1", Features{}, false)
	if p.Mode != SkipSelfJudge {
		t.Errorf("Novice DepthLight = %q, want selfjudge (cheap inline)", p.Mode)
	}
	if p.EstCalls != 0 {
		t.Errorf("Novice DepthLight EstCalls = %d, want 0 (inline)", p.EstCalls)
	}
}

func TestPolicy_TierNovice_Depth3_NotSelfJudge(t *testing.T) {
	// SelfJudge only at depth<=2.
	p := ComputeDriftPolicy(DepthStandard, Context{Tier: TierNovice}, "C1", Features{}, false)
	if p.Mode == SkipSelfJudge {
		t.Errorf("Novice DepthStandard = selfjudge, want default single")
	}
}

// ---------------------------------------------------------------------------
// Order of precedence
// ---------------------------------------------------------------------------

func TestPolicy_Order_CriticalBeatsEverything(t *testing.T) {
	// Critical should beat C7, depth=5, MultiArtifact, anything.
	p := ComputeDriftPolicy(DepthExhaustive, DefaultContext, "C7", Features{MultiArtifact: true, HasCodeSignals: true}, true)
	if p.Mode != SkipConsensus {
		t.Errorf("critical+d5+C7+multi+code = %q, want consensus", p.Mode)
	}
}

func TestPolicy_Order_C7BeatsDepth(t *testing.T) {
	// C7 at depth 0 should still be consensus.
	p := ComputeDriftPolicy(DepthSkip, DefaultContext, "C7", Features{}, false)
	if p.Mode != SkipConsensus {
		t.Errorf("C7 at depth 0 = %q, want consensus (C7 overrides depth)", p.Mode)
	}
}

func TestPolicy_Order_Depth5BeatsMedia(t *testing.T) {
	// Depth 5 is consensus, but C3 is multi. Depth rule fires first.
	// (At depth 5 we'd never see C3, but rule order should still be correct.)
	p := ComputeDriftPolicy(DepthExhaustive, DefaultContext, "C3", Features{}, false)
	if p.Mode != SkipConsensus {
		t.Errorf("depth 5 with C3 = %q, want consensus (depth rule before C3)", p.Mode)
	}
}

func TestPolicy_Order_MultiArtifactBeatsSkip(t *testing.T) {
	// At depth 0 with multi-artifact, should be multi not skip.
	p := ComputeDriftPolicy(DepthSkip, DefaultContext, "C1", Features{MultiArtifact: true}, false)
	if p.Mode != SkipMulti {
		t.Errorf("depth 0 + multi-artifact = %q, want multi", p.Mode)
	}
}

// ---------------------------------------------------------------------------
// 10 personas sanity check
// ---------------------------------------------------------------------------

func TestPolicy_10Personas_DepthLight_Default(t *testing.T) {
	// For DepthLight (default task on a 10-persona run), all 10
	// personas should land on single or cheaper. (DepthLight=2,
	// no critical, default C1, no special features.)
	personas := []Context{
		{Tier: TierNovice, RushMode: true},      // tactico
		{Tier: TierIntermediate, RushMode: true}, // panic-debugger
		{Tier: TierNovice},                      // novato
		{Tier: TierNovice},                      // no-tecnico
		{Tier: TierNovice},                      // movil
		{Tier: TierNovice, RushMode: true},      // rush-cognitivo
		{Tier: TierIntermediate},                // multi-idioma
		{Tier: TierExpert},                      // arquitecto
		{Tier: TierExpert},                      // investigador
		{Tier: TierExpert},                      // escritor
	}
	for i, p := range personas {
		got := ComputeDriftPolicy(DepthLight, p, "C1", Features{}, false)
		if got.EstCalls > 1 {
			t.Errorf("persona %d DepthLight C1 = %s (calls=%d), want <= single",
				i, got.Mode, got.EstCalls)
		}
	}
}

func TestPolicy_10Personas_DepthExhaustive_AllConsensus(t *testing.T) {
	personas := []Context{
		{Tier: TierNovice, RushMode: true},
		{Tier: TierIntermediate, RushMode: true},
		{Tier: TierNovice},
		{Tier: TierNovice},
		{Tier: TierNovice},
		{Tier: TierNovice, RushMode: true},
		{Tier: TierIntermediate},
		{Tier: TierExpert},
		{Tier: TierExpert},
		{Tier: TierExpert},
	}
	for i, p := range personas {
		got := ComputeDriftPolicy(DepthExhaustive, p, "C1", Features{}, false)
		if got.Mode != SkipConsensus {
			t.Errorf("persona %d DepthExhaustive = %q, want consensus", i, got.Mode)
		}
	}
}

func TestPolicy_10Personas_Depth0_AllSkip(t *testing.T) {
	personas := []Context{
		{Tier: TierNovice, RushMode: true},
		{Tier: TierIntermediate, RushMode: true},
		{Tier: TierNovice},
		{Tier: TierNovice},
		{Tier: TierNovice},
		{Tier: TierNovice, RushMode: true},
		{Tier: TierIntermediate},
		{Tier: TierExpert},
		{Tier: TierExpert},
		{Tier: TierExpert},
	}
	for i, p := range personas {
		got := ComputeDriftPolicy(DepthSkip, p, "C1", Features{}, false)
		if got.Mode != SkipNoDrift {
			t.Errorf("persona %d DepthSkip = %q, want skip", i, got.Mode)
		}
	}
}

// ---------------------------------------------------------------------------
// Reason field
// ---------------------------------------------------------------------------

func TestPolicy_Reason_AlwaysNonEmpty(t *testing.T) {
	cases := []struct {
		depth    Depth
		ctx      Context
		vibeCase string
		feats    Features
		critical bool
	}{
		{DepthSkip, DefaultContext, "C1", Features{}, false},
		{DepthMinimal, DefaultContext, "C1", Features{}, false},
		{DepthLight, DefaultContext, "C1", Features{}, false},
		{DepthStandard, DefaultContext, "C1", Features{}, false},
		{DepthFull, DefaultContext, "C1", Features{}, false},
		{DepthExhaustive, DefaultContext, "C1", Features{}, false},
		{DepthStandard, DefaultContext, "C7", Features{}, false},
		{DepthStandard, DefaultContext, "C1", Features{MultiArtifact: true}, false},
		{DepthStandard, DefaultContext, "C1", Features{HasCodeSignals: true}, false},
		{DepthStandard, DefaultContext, "C1", Features{}, true},
		{DepthLight, Context{Tier: TierNovice}, "C1", Features{}, false},
	}
	for i, c := range cases {
		got := ComputeDriftPolicy(c.depth, c.ctx, c.vibeCase, c.feats, c.critical)
		if got.Reason == "" {
			t.Errorf("case %d: Reason is empty", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Reproducibility
// ---------------------------------------------------------------------------

func TestPolicy_PureFunction(t *testing.T) {
	depth := DepthStandard
	ctx := Context{Tier: TierExpert, Domain: DomainCode}
	feats := Features{MultiArtifact: true, HasCodeSignals: true}
	p1 := ComputeDriftPolicy(depth, ctx, "C3", feats, false)
	p2 := ComputeDriftPolicy(depth, ctx, "C3", feats, false)
	if p1.Mode != p2.Mode || p1.EstCalls != p2.EstCalls || p1.Reason != p2.Reason {
		t.Errorf("not reproducible: %+v vs %+v", p1, p2)
	}
}
