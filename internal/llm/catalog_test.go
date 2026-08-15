package llm

import "testing"

// TestCatalog_CanonicalIDs pins the 9 canonical provider ids in order.
func TestCatalog_CanonicalIDs(t *testing.T) {
	want := []string{
		"anthropic", "openai", "google",
		"deepseek", "minimax", "minimax-cn", "zhipu", "moonshot", "qwen",
	}
	got := CanonicalIDs()
	if len(got) != len(want) {
		t.Fatalf("CanonicalIDs len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("CanonicalIDs[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestCatalog_ResolveAliases pins the legacy-id mapping.
func TestCatalog_ResolveAliases(t *testing.T) {
	cases := []struct{ in, want string; wasAlias bool }{
		{"zhipu", "zhipu", false},
		{"z-ai", "zhipu", true},
		{"dashscope", "qwen", true},
		{"qwen", "qwen", false},
		{"deepseek", "deepseek", false},
	}
	for _, c := range cases {
		got, was := ResolveID(c.in)
		if got != c.want || was != c.wasAlias {
			t.Errorf("ResolveID(%q) = (%q, %v), want (%q, %v)", c.in, got, was, c.want, c.wasAlias)
		}
	}
}

// TestCatalog_SpecByID verifies canonical + legacy id lookup.
func TestCatalog_SpecByID(t *testing.T) {
	if s := SpecByID("deepseek"); s == nil || s.ID != "deepseek" {
		t.Fatalf("SpecByID(deepseek) = %+v, want deepseek", s)
	}
	if s := SpecByID("z-ai"); s == nil || s.ID != "zhipu" {
		t.Fatalf("SpecByID(z-ai) = %+v, want canonical zhipu", s)
	}
	if s := SpecByID("dashscope"); s == nil || s.ID != "qwen" {
		t.Fatalf("SpecByID(dashscope) = %+v, want canonical qwen", s)
	}
	if s := SpecByID("not-a-provider"); s != nil {
		t.Fatalf("SpecByID(not-a-provider) = %+v, want nil", s)
	}
}

// TestCatalog_EnvKeysPinned guards the env-var mapping (keystore
// migration + detection depend on it).
func TestCatalog_EnvKeysPinned(t *testing.T) {
	want := map[string]string{
		"anthropic":   "ANTHROPIC_API_KEY",
		"openai":      "OPENAI_API_KEY",
		"google":      "GEMINI_API_KEY",
		"deepseek":    "DEEPSEEK_API_KEY",
		"minimax":     "MINIMAX_API_KEY",
		"minimax-cn":  "MINIMAX_API_KEY_CN",
		"zhipu":       "ZAI_API_KEY",
		"moonshot":    "MOONSHOT_API_KEY",
		"qwen":        "DASHSCOPE_API_KEY",
	}
	for id, envKey := range want {
		if got := EnvKeyForProvider(id); got != envKey {
			t.Errorf("EnvKeyForProvider(%s) = %q, want %q", id, got, envKey)
		}
	}
}

// TestCatalog_ProbeFieldsNoEmpty verifies every catalog entry has a
// probe path + auth mode (T3 depends on them).
func TestCatalog_ProbeFieldsNoEmpty(t *testing.T) {
	for i := range Catalog {
		p := &Catalog[i]
		if p.ProbePath == "" {
			t.Errorf("provider %s: ProbePath empty", p.ID)
		}
		if p.ProbeAuthMode != ProbeAuthBearer && p.ProbeAuthMode != ProbeAuthXAPIKey {
			t.Errorf("provider %s: ProbeAuthMode %q invalid", p.ID, p.ProbeAuthMode)
		}
	}
}
