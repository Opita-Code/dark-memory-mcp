package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/llm"
	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
)

// newLLMConfigTestSetup registers the 4 LLM_CONFIG tools with a memory
// keystore + stub failover so no OS keyring / background health loop
// is touched.
func newLLMConfigTestSetup(t *testing.T) (*Registry, *llmkeystore.MemoryStore) {
	t.Helper()
	reg := NewRegistry()
	RegisterLLMConfig(reg)
	mem := llmkeystore.NewMemoryStore()

	llmConfigKS = mem
	llmConfigFailover = &stubFailover{
		status: []llm.CandidateStatus{
			{ProviderID: "deepseek", HasKey: true, KeySource: "memory", Pinned: true},
			{ProviderID: "openai", HasKey: false, KeySource: "none"},
		},
		last: "deepseek",
	}
	t.Cleanup(func() {
		llmConfigKS = nil
		llmConfigFailover = nil
	})
	return reg, mem
}

// stubFailover is a minimal llmConfigFailoverer for tests.
type stubFailover struct {
	status []llm.CandidateStatus
	last   string
}

func (s *stubFailover) Status() []llm.CandidateStatus { return s.status }
func (s *stubFailover) LastProviderID() string        { return s.last }

// callLLMConfig invokes a tool by name with JSON args and returns the
// ToolResponse (or fails on Go error).
func callLLMConfig(t *testing.T, reg *Registry, name string, args map[string]any) *ToolResponse {
	t.Helper()
	tool := reg.Get(name)
	if tool == nil {
		t.Fatalf("%s not registered", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler Go error: %v", err)
	}
	return resp
}

// TestLLMKeyAdd_StoresAndValidates verifies llm_key_add stores a key in
// the keystore and reports source. Validation is ON by default but the
// probe hits a real provider endpoint — so tests pass validate=false
// to avoid network, and a separate probe test covers the validation
// path.
func TestLLMKeyAdd_StoresWithoutNetwork(t *testing.T) {
	reg, mem := newLLMConfigTestSetup(t)
	f := false
	resp := callLLMConfig(t, reg, "llm_key_add", map[string]any{
		"provider": "deepseek",
		"key":      "sk-test",
		"validate": f,
	})
	if resp.Error != nil {
		t.Fatalf("llm_key_add error: %v", resp.Error)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data not map: %T", resp.Data)
	}
	if data["provider"] != "deepseek" || data["stored"] != true {
		t.Errorf("data = %v, want provider=deepseek stored=true", data)
	}
	if v, _ := mem.Get("deepseek"); v != "sk-test" {
		t.Errorf("keystore Get = %q, want sk-test", v)
	}
}

// TestLLMKeyAdd_UnknownProvider fails cleanly (handler returns a Go
// error, which the MCP layer surfaces as a tool error).
func TestLLMKeyAdd_UnknownProvider(t *testing.T) {
	reg, _ := newLLMConfigTestSetup(t)
	tool := reg.Get("llm_key_add")
	raw, _ := json.Marshal(map[string]any{
		"provider": "not-a-provider",
		"key":      "k",
		"validate": false,
	})
	_, err := tool.Handler(context.Background(), raw)
	if err == nil {
		t.Fatal("expected Go error for unknown provider")
	}
}

// TestLLMKeyList_NeverReturnsValue verifies llm_key_list exposes state
// only — the key VALUE must never appear in the response.
func TestLLMKeyList_NeverReturnsValue(t *testing.T) {
	reg, mem := newLLMConfigTestSetup(t)
	_ = mem.Set("deepseek", "SUPER-SECRET-KEY")
	resp := callLLMConfig(t, reg, "llm_key_list", nil)
	if resp.Error != nil {
		t.Fatalf("llm_key_list error: %v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Data)
	s := string(raw)
	if contains(s, "SUPER-SECRET-KEY") {
		t.Fatal("llm_key_list leaked the key value — SECURITY VIOLATION")
	}
	if !contains(s, "deepseek") || !contains(s, "memory") {
		t.Errorf("response missing provider/source info: %s", s)
	}
}

// TestLLMKeyRemove deletes from the keystore.
func TestLLMKeyRemove(t *testing.T) {
	reg, mem := newLLMConfigTestSetup(t)
	_ = mem.Set("deepseek", "sk-test")
	resp := callLLMConfig(t, reg, "llm_key_remove", map[string]any{"provider": "deepseek"})
	if resp.Error != nil {
		t.Fatalf("llm_key_remove error: %v", resp.Error)
	}
	if mem.Has("deepseek") {
		t.Fatal("keystore still has deepseek after remove")
	}
}

// TestLLMProviderStatus reports the routing view.
func TestLLMProviderStatus(t *testing.T) {
	reg, _ := newLLMConfigTestSetup(t)
	resp := callLLMConfig(t, reg, "llm_provider_status", nil)
	if resp.Error != nil {
		t.Fatalf("llm_provider_status error: %v", resp.Error)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data not map: %T", resp.Data)
	}
	if data["active_provider"] != "deepseek" {
		t.Errorf("active_provider = %v, want deepseek", data["active_provider"])
	}
	cands, ok := data["candidates"].([]llm.CandidateStatus)
	if !ok || len(cands) != 2 {
		t.Fatalf("candidates = %#v, want 2 entries", data["candidates"])
	}
	if !cands[0].Pinned || cands[1].HasKey {
		t.Errorf("candidates[0]=%+v candidates[1]=%+v, want [pinned deepseek, keyless openai]", cands[0], cands[1])
	}
}

// TestLLMConfigToolsRegisteredInCanonicalOrder verifies the 4 tools
// are canonical (present in CanonicalOrder, after delegation before
// judge).
func TestLLMConfigToolsRegisteredInCanonicalOrder(t *testing.T) {
	names := CanonicalOrder()
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	for _, tool := range []string{"llm_key_add", "llm_key_list", "llm_key_remove", "llm_provider_status"} {
		if _, ok := idx[tool]; !ok {
			t.Errorf("%s missing from canonical order", tool)
		}
	}
	if idx["llm_key_add"] < idx["delegate_intent"] || idx["llm_key_add"] > idx["judge"] {
		t.Errorf("llm_key_add position %d must be between delegate_intent (%d) and judge (%d)", idx["llm_key_add"], idx["delegate_intent"], idx["judge"])
	}
}
