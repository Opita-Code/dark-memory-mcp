package vibeflow

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Tier basics
// ---------------------------------------------------------------------------

func TestModelTier_String(t *testing.T) {
	cases := []struct {
		t    ModelTier
		want string
	}{
		{TierFast, "fast"},
		{TierBalanced, "balanced"},
		{TierStrong, "strong"},
		{ModelTier(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.t.String(); got != c.want {
			t.Errorf("ModelTier(%d).String() = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestModelTier_Description(t *testing.T) {
	for _, tier := range []ModelTier{TierFast, TierBalanced, TierStrong} {
		if got := tier.Description(); got == "" || got == "unknown tier" {
			t.Errorf("ModelTier(%d).Description() = %q, want non-empty", tier, got)
		}
	}
}

// ---------------------------------------------------------------------------
// ModelForTier
// ---------------------------------------------------------------------------

func TestModelForTier_DefaultConfig(t *testing.T) {
	cases := []struct {
		tier        ModelTier
		wantProv    string
		wantModel   string
	}{
		{TierFast, "deepseek", "deepseek-chat"},
		{TierBalanced, "minimax-cn", "MiniMax-M3"},
		{TierStrong, "minimax-cn", "MiniMax-M3"},
	}
	for _, c := range cases {
		gotProv, gotModel := ModelForTier(c.tier, DefaultModelConfig)
		if gotProv != c.wantProv {
			t.Errorf("ModelForTier(%d) provider = %q, want %q", c.tier, gotProv, c.wantProv)
		}
		if gotModel != c.wantModel {
			t.Errorf("ModelForTier(%d) model = %q, want %q", c.tier, gotModel, c.wantModel)
		}
	}
}

func TestModelForTier_CustomConfig(t *testing.T) {
	cfg := ModelConfig{
		FastProvider:     "openai",
		FastModel:        "gpt-4o-mini",
		BalancedProvider: "anthropic",
		BalancedModel:    "claude-3.5-sonnet",
		StrongProvider:   "anthropic",
		StrongModel:      "claude-3-opus",
	}
	if prov, model := ModelForTier(TierFast, cfg); prov != "openai" || model != "gpt-4o-mini" {
		t.Errorf("TierFast = %s/%s, want openai/gpt-4o-mini", prov, model)
	}
	if prov, model := ModelForTier(TierStrong, cfg); prov != "anthropic" || model != "claude-3-opus" {
		t.Errorf("TierStrong = %s/%s, want anthropic/claude-3-opus", prov, model)
	}
}

func TestModelForTier_UnknownTier_DefaultsBalanced(t *testing.T) {
	prov, model := ModelForTier(ModelTier(99), DefaultModelConfig)
	if prov != "minimax-cn" || model != "MiniMax-M3" {
		t.Errorf("unknown tier should default to balanced, got %s/%s", prov, model)
	}
}

// ---------------------------------------------------------------------------
// SelectModelTier — depth-based rules
// ---------------------------------------------------------------------------

func TestSelectModelTier_Depth0_Fast(t *testing.T) {
	tier := SelectModelTier(DepthSkip, DefaultContext, "C1", Features{})
	if tier != TierFast {
		t.Errorf("DepthSkip = %q, want fast", tier)
	}
}

func TestSelectModelTier_Depth1_Fast(t *testing.T) {
	tier := SelectModelTier(DepthMinimal, DefaultContext, "C1", Features{})
	if tier != TierFast {
		t.Errorf("DepthMinimal = %q, want fast", tier)
	}
}

func TestSelectModelTier_Depth2_DefaultBalanced(t *testing.T) {
	tier := SelectModelTier(DepthLight, DefaultContext, "C1", Features{})
	if tier != TierBalanced {
		t.Errorf("DepthLight (intermediate, no rush) = %q, want balanced", tier)
	}
}

func TestSelectModelTier_Depth5_Strong(t *testing.T) {
	tier := SelectModelTier(DepthExhaustive, DefaultContext, "C1", Features{})
	if tier != TierStrong {
		t.Errorf("DepthExhaustive = %q, want strong", tier)
	}
}

// ---------------------------------------------------------------------------
// SelectModelTier — cost-lens rules
// ---------------------------------------------------------------------------

func TestSelectModelTier_RushMode_Depth2_Fast(t *testing.T) {
	tier := SelectModelTier(DepthLight, Context{Tier: TierIntermediate, RushMode: true}, "C1", Features{})
	if tier != TierFast {
		t.Errorf("RushMode DepthLight = %q, want fast (cost-lens)", tier)
	}
}

func TestSelectModelTier_RushMode_Depth3_Fast(t *testing.T) {
	tier := SelectModelTier(DepthStandard, Context{Tier: TierIntermediate, RushMode: true}, "C1", Features{})
	if tier != TierFast {
		t.Errorf("RushMode DepthStandard = %q, want fast (cost-lens)", tier)
	}
}

func TestSelectModelTier_RushMode_Depth4_Balanced(t *testing.T) {
	// Cost-lens only goes up to depth 3. Depth 4 = Full → balanced
	tier := SelectModelTier(DepthFull, Context{Tier: TierIntermediate, RushMode: true}, "C1", Features{})
	if tier != TierBalanced {
		t.Errorf("RushMode DepthFull = %q, want balanced (cost-lens only up to depth 3)", tier)
	}
}

func TestSelectModelTier_Novice_Depth2_Fast(t *testing.T) {
	tier := SelectModelTier(DepthLight, Context{Tier: TierNovice}, "C1", Features{})
	if tier != TierFast {
		t.Errorf("Novice DepthLight = %q, want fast (cost-lens)", tier)
	}
}

func TestSelectModelTier_Novice_Depth5_StillStrong(t *testing.T) {
	// Even novice needs strong at depth 5
	tier := SelectModelTier(DepthExhaustive, Context{Tier: TierNovice}, "C1", Features{})
	if tier != TierStrong {
		t.Errorf("Novice DepthExhaustive = %q, want strong (depth rule wins)", tier)
	}
}

func TestSelectModelTier_Expert_Depth3_Balanced(t *testing.T) {
	tier := SelectModelTier(DepthStandard, Context{Tier: TierExpert}, "C1", Features{})
	if tier != TierBalanced {
		t.Errorf("Expert DepthStandard = %q, want balanced (no rush, no novice)", tier)
	}
}

// ---------------------------------------------------------------------------
// SelectModelTier — feature and vibe_case rules
// ---------------------------------------------------------------------------

func TestSelectModelTier_MultiArtifact_Strong(t *testing.T) {
	tier := SelectModelTier(DepthStandard, DefaultContext, "C1", Features{MultiArtifact: true})
	if tier != TierStrong {
		t.Errorf("MultiArtifact = %q, want strong (regardless of depth)", tier)
	}
}

func TestSelectModelTier_C7_Strong(t *testing.T) {
	tier := SelectModelTier(DepthStandard, DefaultContext, "C7", Features{})
	if tier != TierStrong {
		t.Errorf("C7 = %q, want strong (mixed bundle)", tier)
	}
}

func TestSelectModelTier_C3_Balanced(t *testing.T) {
	tier := SelectModelTier(DepthStandard, DefaultContext, "C3", Features{})
	if tier != TierBalanced {
		t.Errorf("C3 = %q, want balanced (media)", tier)
	}
}

func TestSelectModelTier_C4_Balanced(t *testing.T) {
	tier := SelectModelTier(DepthStandard, DefaultContext, "C4", Features{})
	if tier != TierBalanced {
		t.Errorf("C4 = %q, want balanced (media)", tier)
	}
}

func TestSelectModelTier_C1_DefaultBalanced(t *testing.T) {
	tier := SelectModelTier(DepthStandard, DefaultContext, "C1", Features{})
	if tier != TierBalanced {
		t.Errorf("C1 default = %q, want balanced", tier)
	}
}

func TestSelectModelTier_C2_DefaultBalanced(t *testing.T) {
	tier := SelectModelTier(DepthStandard, DefaultContext, "C2", Features{})
	if tier != TierBalanced {
		t.Errorf("C2 default = %q, want balanced", tier)
	}
}

// ---------------------------------------------------------------------------
// SelectModel — bundling
// ---------------------------------------------------------------------------

func TestSelectModel_Bundles(t *testing.T) {
	m := SelectModel(DepthStandard, DefaultContext, "C1", Features{}, DefaultModelConfig)
	if m.Tier != TierBalanced {
		t.Errorf("tier = %q, want balanced", m.Tier)
	}
	if m.Provider != "minimax-cn" || m.Model != "MiniMax-M3" {
		t.Errorf("provider/model = %s/%s, want minimax-cn/MiniMax-M3", m.Provider, m.Model)
	}
	if !strings.Contains(m.Reason, "balanced") {
		t.Errorf("reason should mention tier, got %q", m.Reason)
	}
}

func TestSelectModel_CostLens_UsesFast(t *testing.T) {
	m := SelectModel(DepthLight, Context{Tier: TierNovice}, "C1", Features{}, DefaultModelConfig)
	if m.Tier != TierFast {
		t.Errorf("cost-lens tier = %q, want fast", m.Tier)
	}
	if m.Provider != "deepseek" || m.Model != "deepseek-chat" {
		t.Errorf("cost-lens provider/model = %s/%s, want deepseek/deepseek-chat", m.Provider, m.Model)
	}
}

// ---------------------------------------------------------------------------
// 10 personas sanity check
// ---------------------------------------------------------------------------

func TestSelectModel_10Personas_DepthMedium(t *testing.T) {
	// For a "medium task on a code question" (the most common L8.1a
	// classification for dev work), each persona should get a
	// sensible tier.
	personas := []struct {
		name string
		ctx  Context
		want ModelTier
	}{
		{"tactico", Context{Tier: TierNovice, RushMode: true}, TierFast},             // depth 2 + novice + rush = fast
		{"panic-debugger", Context{Tier: TierIntermediate, RushMode: true}, TierFast}, // rush = fast
		{"novato", Context{Tier: TierNovice}, TierFast},                              // novice = fast
		{"no-tecnico", Context{Tier: TierNovice}, TierFast},                          // novice = fast
		{"movil", Context{Tier: TierNovice}, TierFast},                               // novice = fast
		{"rush-cognitivo", Context{Tier: TierNovice, RushMode: true}, TierFast},      // novice + rush = fast
		{"multi-idioma", Context{Tier: TierIntermediate}, TierBalanced},              // intermediate, no rush = balanced
		{"arquitecto", Context{Tier: TierExpert}, TierBalanced},                      // expert, no rush = balanced
		{"investigador", Context{Tier: TierExpert}, TierBalanced},
		{"escritor", Context{Tier: TierExpert}, TierBalanced},
	}
	for _, p := range personas {
		got := SelectModelTier(DepthLight, p.ctx, "C1", Features{})
		if got != p.want {
			t.Errorf("persona %q = %q, want %q", p.name, got, p.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Cost savings sanity check
// ---------------------------------------------------------------------------

func TestSelectModelTier_CostLens_60Percent_AreFast(t *testing.T) {
	// For the 10 personas at a DepthLight task, count how many get
	// TierFast. Should be at least 6 (cost-lens majority).
	personas := []Context{
		{Tier: TierNovice, RushMode: true},    // tactico
		{Tier: TierIntermediate, RushMode: true}, // panic-debugger
		{Tier: TierNovice},                    // novato
		{Tier: TierNovice},                    // no-tecnico
		{Tier: TierNovice},                    // movil
		{Tier: TierNovice, RushMode: true},    // rush-cognitivo
		{Tier: TierIntermediate},              // multi-idioma
		{Tier: TierExpert},                    // arquitecto
		{Tier: TierExpert},                    // investigador
		{Tier: TierExpert},                    // escritor
	}
	fastCount := 0
	for _, p := range personas {
		if SelectModelTier(DepthLight, p, "C1", Features{}) == TierFast {
			fastCount++
		}
	}
	if fastCount < 6 {
		t.Errorf("expected ≥6/10 personas in TierFast, got %d", fastCount)
	}
}

// ---------------------------------------------------------------------------
// Reproducibility
// ---------------------------------------------------------------------------

func TestSelectModelTier_PureFunction(t *testing.T) {
	ctx := Context{Tier: TierExpert, Domain: DomainCode, RushMode: false}
	t1 := SelectModelTier(DepthStandard, ctx, "C3", Features{MultiArtifact: true})
	t2 := SelectModelTier(DepthStandard, ctx, "C3", Features{MultiArtifact: true})
	if t1 != t2 {
		t.Errorf("not reproducible: %q vs %q", t1, t2)
	}
}
