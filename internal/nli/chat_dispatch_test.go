package nli

import (
	"net/http"
	"testing"
)

// TestIsChatDispatchID: both dispatch prefixes select this provider.
//
// "chat-" is the NLI chain's prefix and "judge-" the LLM-judge
// chain's. Both bindings point at the same OpenAI-compatible chat
// endpoint, so the NLI fallback must accept a "judge-*" binding: before
// this, a project configured only with "judge-minimax" had no usable
// fallback and the verdict degraded to needs_human with "unknown
// provider_id".
func TestIsChatDispatchID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"chat-minimax", true},
		{"judge-minimax", true},
		{"chat-deepseek", true},
		{"judge-z-ai", true},
		{"minimax", false},
		{"deberta-x", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsChatDispatchID(c.id); got != c.want {
			t.Errorf("IsChatDispatchID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestChatDispatchCatalogID: the dispatch prefix is stripped and the
// rest is resolved against the canonical catalog.
func TestChatDispatchCatalogID(t *testing.T) {
	cases := []struct {
		id   string
		want string
		ok   bool
	}{
		{"chat-minimax", "minimax", true},
		{"judge-minimax", "minimax", true},
		{"judge-minimax-cn", "minimax-cn", true},
		{"chat-deepseek", "deepseek", true},
		{"judge-not-a-provider", "", false},
		{"minimax", "", false},
	}
	for _, c := range cases {
		got, ok := chatDispatchCatalogID(c.id)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("chatDispatchCatalogID(%q) = (%q, %v), want (%q, %v)", c.id, got, ok, c.want, c.ok)
		}
	}
}

// TestChatProviderAcceptsJudgePrefix: the provider itself accepts a
// "judge-*" id.
func TestChatProviderAcceptsJudgePrefix(t *testing.T) {
	cfg := ProviderConfig{
		ProviderID: "judge-minimax",
		Endpoint:   "https://api.minimax.io/v1/chat/completions",
		ModelRev:   "MiniMax-M3",
	}
	// Construction does no I/O: it only validates the config and
	// resolves the endpoint against the credential (none here).
	_, err := NewChatProvider(cfg, http.DefaultClient, 1024, 256)
	if err != nil {
		t.Fatalf("NewChatProvider with a judge- prefix: %v", err)
	}
}
