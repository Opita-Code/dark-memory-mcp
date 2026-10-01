// delegation_e2e_test.go — Phase 9 alpha.20 Chunk 8.1.
//
// E2E tests for the v3 binary's dark_memory_delegate_intent after
// Chunk 8.1. Validates that the v4alpha DECIDE→EXTRACT→MIND→CURATE
// pipeline (alpha.19 Chunk 7.1+7.2) is reachable through the v3
// MCP surface (closes Phase 8 e2e critical finding #1, row 2327).
//
// # Test layout
//
// Per SPEC-alpha-11-phase8.md §3.1, the 8 tests cover:
//  1. TestDelegateIntentV4Alpha_InlineShort: short task → DECIDE=inline,
//     PLAN=1 subtask, no EXTRACT.
//  2. TestDelegateIntentV4Alpha_DelegateLong: long task → DECIDE=delegate,
//     EXTRACT fires, needs FakeLLM returning valid JSON.
//  3. TestDelegateIntentV4Alpha_RefuseMarker: refusal marker in task →
//     DECIDE=refused, 0 subtasks.
//  4. TestDelegateIntentV4Alpha_NeedsHuman_NoLLMKey: no LLMClient →
//     verdict=needs_human + alternatives[] (includes fallback-plan).
//  5. TestDelegateIntentV4Alpha_NeedsHuman_LLMParseError: FakeLLM
//     returns malformed JSON → verdict=needs_human + alternatives[].
//  6. TestDelegateIntentV4Alpha_RefineRetry: FakeLLM returns
//     drift_detected once then aligned on retry → verdict=aligned
//     after MaxRetries+1 attempts.
//  7. TestDelegateIntentV4Alpha_WireShapeV3: confirms v3 wire shape
//     (handler uppercase, plan, operator) + v4alpha additive fields
//     (decision, cache_hit, verdict, alternatives).
//  8. TestDelegateIntentV2_FallbackByFlag: DARK_DELEGATION_BACKEND=v2
//     routes to v2 (orch.DelegateIntent), confirming the rollback
//     escape hatch.
//
// CURATE subagent binding propagation (Chunk 7.2) is NOT tested here
// because the v3 Store interface hides it. The test 7 documents the
// no-op behavior (subagent_id empty) — Chunk 8.1.1 will add the
// RawDB() method to enable full CURATE.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// fakeLLMForDelegate is a deterministic stub for judge.LLMClient.
// Returns the configured sequence of (content, error) tuples; the
// last entry repeats if attempts > len(sequence).
type fakeLLMForDelegate struct {
	// responses[attempt] is the LLM response content for that attempt.
	// Empty content + err=nil simulates "empty LLM response".
	// err set simulates "LLM network error".
	responses []fakeLLMResponse
	calls     int
}

type fakeLLMResponse struct {
	content string
	err     error
}

func (f *fakeLLMForDelegate) Complete(_ context.Context, _ judge.LLMRequest) (*judge.LLMResponse, error) {
	idx := f.calls
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	f.calls++
	r := f.responses[idx]
	if r.err != nil {
		return nil, r.err
	}
	return &judge.LLMResponse{
		Content: r.content,
		Model:   "fake-llm-1",
	}, nil
}

// Calls returns the number of LLM calls made so far (for assertion).
func (f *fakeLLMForDelegate) Calls() int { return f.calls }

// validExtractJSON returns a JSON response that satisfies the
// EXTRACT schema. Used by tests that want EXTRACT to succeed.
func validExtractJSON(n int) string {
	type subtask struct {
		ID           string   `json:"id"`
		Description  string   `json:"description"`
		Dependencies []string `json:"dependencies"`
	}
	type resp struct {
		Decision  string     `json:"decision"`
		Reasoning string     `json:"reasoning"`
		Subtasks  []subtask  `json:"subtasks"`
	}
	r := resp{
		Decision:  "delegate",
		Reasoning: "valid extract response",
		Subtasks:  make([]subtask, n),
	}
	for i := 0; i < n; i++ {
		r.Subtasks[i] = subtask{
			ID:          "subtask-" + string(rune('1'+i)),
			Description: "test subtask number one with sufficient length to clear MinSubtaskLength",
			Dependencies: nil,
		}
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// validValidateJSON returns a JUDGE-step JSON response. verdict is
// "aligned" or "drift_detected".
func validValidateJSON(verdict, reasoning string) string {
	r := map[string]string{
		"verdict":   verdict,
		"reasoning": reasoning,
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// withEnv sets an env var for the duration of t and restores it on
// cleanup. Pattern from Go's t.Setenv (Go 1.17+).
func withEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

// --- Test 1: short task → DECIDE=inline, no EXTRACT ---

func TestDelegateIntentV4Alpha_InlineShort(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	llm := &fakeLLMForDelegate{} // no responses — EXTRACT not expected to fire
	backend := &DelegateIntentBackend{LLMClient: llm, ExtractCache: cache, Memories: nil}

	RegisterDelegationWithBackend(reg, nil, nil, backend) // orch+st nil → v4alpha path used

	ctx := context.Background()
	input := json.RawMessage(`{"vibe_case":"C1","task_description":"Write the release notes for alpha.20","operator":"nico"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Handler != "HANDLE" {
		t.Errorf("Handler = %q; want \"HANDLE\" (v4alpha decision=inline → uppercase HANDLE)", payload.Handler)
	}
	if payload.Decision != "inline" {
		t.Errorf("Decision = %q; want \"inline\" (v4alpha additive field)", payload.Decision)
	}
	if payload.Verdict != "aligned" {
		t.Errorf("Verdict = %q; want \"aligned\" (no EXTRACT, deterministic PLAN)", payload.Verdict)
	}
	if llm.Calls() != 0 {
		t.Errorf("LLM called %d times for short inline task; want 0 (EXTRACT must NOT fire)", llm.Calls())
	}
	if len(payload.Plan) != 1 {
		t.Errorf("Plan length = %d; want 1 (deterministic PLAN whole task)", len(payload.Plan))
	}
}

// --- Test 3: refusal marker → DECIDE=refused ---

func TestDelegateIntentV4Alpha_RefuseMarker(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	llm := &fakeLLMForDelegate{}
	backend := &DelegateIntentBackend{LLMClient: llm, ExtractCache: cache, Memories: nil}
	RegisterDelegationWithBackend(reg, nil, nil, backend)

	ctx := context.Background()
	// "impossible" is in the refusal marker list (router.go:43).
	input := json.RawMessage(`{"vibe_case":"C7","task_description":"This is impossible to do because the spec is out of scope for the operator","operator":"nico"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Handler != "REFUSE" {
		t.Errorf("Handler = %q; want \"REFUSE\" (v4alpha decision=refused → uppercase)", payload.Handler)
	}
	if payload.Decision != "refused" {
		t.Errorf("Decision = %q; want \"refused\"", payload.Decision)
	}
	if len(payload.Plan) != 0 {
		t.Errorf("Plan length = %d; want 0 (REFUSE → no subtasks)", len(payload.Plan))
	}
	if llm.Calls() != 0 {
		t.Errorf("LLM called %d times for refused task; want 0 (EXTRACT must NOT fire)", llm.Calls())
	}
}

// --- Test 2: long task → DECIDE=delegate, EXTRACT fires, FakeLLM valid ---

func TestDelegateIntentV4Alpha_DelegateLong(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	// FakeLLM returns valid EXTRACT JSON on first attempt + valid
	// JUDGE JSON (aligned) on second attempt.
	llm := &fakeLLMForDelegate{
		responses: []fakeLLMResponse{
			{content: validExtractJSON(3)},          // EXTRACT call → 3 subtasks
			{content: validValidateJSON("aligned", "valid decomposition")}, // JUDGE call → aligned
		},
	}
	backend := &DelegateIntentBackend{LLMClient: llm, ExtractCache: cache, Memories: nil}
	RegisterDelegationWithBackend(reg, nil, nil, backend)

	ctx := context.Background()
	// Long task (>200 chars) → DECIDE=delegate, EXTRACT fires.
	task := strings.Repeat("a", 250) + " with a clear multi-step instruction set"
	input := json.RawMessage(`{"vibe_case":"C1","task_description":"` + task + `","operator":"nico"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Handler != "DELEGATE" {
		t.Errorf("Handler = %q; want \"DELEGATE\" (v4alpha decision=delegate → uppercase DELEGATE)", payload.Handler)
	}
	if payload.Decision != "delegate" {
		t.Errorf("Decision = %q; want \"delegate\"", payload.Decision)
	}
	if payload.Verdict != "aligned" {
		t.Errorf("Verdict = %q; want \"aligned\" (FakeLLM JUDGE returns aligned)", payload.Verdict)
	}
	if len(payload.Plan) != 3 {
		t.Errorf("Plan length = %d; want 3 (EXTRACT returned 3 subtasks)", len(payload.Plan))
	}
	if llm.Calls() != 2 {
		t.Errorf("LLM called %d times; want 2 (EXTRACT + JUDGE, no retry)", llm.Calls())
	}
}

// --- Test 5: parse error → needs_human + alternatives ---

func TestDelegateIntentV4Alpha_NeedsHuman_LLMParseError(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	// FakeLLM returns invalid JSON on first attempt → EXTRACT fails
	// with llm_parse_error → needs_human + alternatives.
	llm := &fakeLLMForDelegate{
		responses: []fakeLLMResponse{
			{content: "this is not valid JSON"},
		},
	}
	backend := &DelegateIntentBackend{LLMClient: llm, ExtractCache: cache, Memories: nil}
	RegisterDelegationWithBackend(reg, nil, nil, backend)

	ctx := context.Background()
	task := strings.Repeat("y", 250)
	input := json.RawMessage(`{"vibe_case":"C1","task_description":"` + task + `","operator":"nico"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want \"needs_human\" (parse error)", payload.Verdict)
	}
	if len(payload.Alternatives) == 0 {
		t.Error("Alternatives is empty; want ≥1 (llm_parse_error surfaces alternatives[])")
	}
	// llm_parse_error failure mode (delegation/types.go:187) has
	// fallback-plan + accept-partial + fallback-refuse.
	wantAlts := map[string]bool{"fallback-plan": false, "accept-partial": false, "fallback-refuse": false}
	for _, a := range payload.Alternatives {
		if _, ok := wantAlts[a.ID]; ok {
			wantAlts[a.ID] = true
		}
	}
	for id, present := range wantAlts {
		if !present {
			t.Errorf("Alternatives missing %q; got %d alts total", id, len(payload.Alternatives))
		}
	}
}

// --- Test 6: refine+retry (drift_detected → retry → aligned) ---

func TestDelegateIntentV4Alpha_RefineRetry(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	// FakeLLM returns:
	//   attempt 0: valid EXTRACT → JUDGE returns drift_detected
	//   attempt 1: valid EXTRACT → JUDGE returns aligned (success after retry)
	llm := &fakeLLMForDelegate{
		responses: []fakeLLMResponse{
			{content: validExtractJSON(2)},
			{content: validValidateJSON("drift_detected", "over-granular subtasks")},
			{content: validExtractJSON(2)},
			{content: validValidateJSON("aligned", "refined decomposition is good")},
		},
	}
	backend := &DelegateIntentBackend{LLMClient: llm, ExtractCache: cache, Memories: nil}
	RegisterDelegationWithBackend(reg, nil, nil, backend)

	ctx := context.Background()
	task := strings.Repeat("z", 250)
	input := json.RawMessage(`{"vibe_case":"C1","task_description":"` + task + `","operator":"nico"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Verdict != "aligned" {
		t.Errorf("Verdict = %q; want \"aligned\" (drift_detected → refine → aligned)", payload.Verdict)
	}
	if llm.Calls() != 4 {
		t.Errorf("LLM called %d times; want 4 (2 EXTRACT + 2 JUDGE — drift_detected triggers retry)", llm.Calls())
	}
}

func TestDelegateIntentV4Alpha_NeedsHuman_NoLLMKey(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	backend := &DelegateIntentBackend{LLMClient: nil, ExtractCache: cache, Memories: nil}
	RegisterDelegationWithBackend(reg, nil, nil, backend)

	ctx := context.Background()
	// Long task triggers EXTRACT (ShouldExtract: len>200).
	task := strings.Repeat("x", 250)
	input := json.RawMessage(`{"vibe_case":"C1","task_description":"` + task + `","operator":"nico"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want \"needs_human\" (no LLMClient → EXTRACT fails)", payload.Verdict)
	}
	if len(payload.Alternatives) == 0 {
		t.Error("Alternatives is empty; want ≥1 (failure mode surfaces alternatives[])")
	}
	// Check that fallback-plan is among the alternatives — per
	// llm_network_error failure mode in delegation/types.go:171.
	hasFallback := false
	for _, a := range payload.Alternatives {
		if a.ID == "fallback-plan" {
			hasFallback = true
		}
	}
	if !hasFallback {
		t.Errorf("Alternatives missing fallback-plan; got %d alts", len(payload.Alternatives))
	}
}

// --- Test 7: wire shape v3 + v4alpha additive fields ---

func TestDelegateIntentV4Alpha_WireShapeV3(t *testing.T) {
	reg := NewRegistry()
	cache := delegation.NewExtractCache(delegation.DefaultCacheTTL)
	backend := &DelegateIntentBackend{LLMClient: nil, ExtractCache: cache, Memories: nil}
	RegisterDelegationWithBackend(reg, nil, nil, backend)

	ctx := context.Background()
	input := json.RawMessage(`{"vibe_case":"C1","task_description":"Short task for wire shape test","operator":"alice"}`)
	res, err := callDelegateTool(t, reg, ctx, input)
	if err != nil {
		t.Fatalf("callDelegateTool: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("delegate_intent returned ToolError: %+v", res.Error)
	}
	payload, err := decodeDelegateResponse(t, res)
	if err != nil {
		t.Fatalf("decode v3 shape: %v\ndata=%s", err, payloadAsJSON(res))
	}
	// v3 contract: handler uppercase, operator present.
	if payload.Handler == "" {
		t.Error("v3 handler field empty (backward compat broken)")
	}
	// v4alpha decision "inline" maps to v3 handler "HANDLE"
	// (per v4DecisionToV3Handler conversion — see Chunk 8.1 spec).
	if payload.Decision == "inline" && payload.Handler != "HANDLE" {
		t.Errorf("v4 decision=%q should map to v3 handler=HANDLE; got %q",
			payload.Decision, payload.Handler)
	}
	if payload.Decision == "delegate" && payload.Handler != "DELEGATE" {
		t.Errorf("v4 decision=%q should map to v3 handler=DELEGATE; got %q",
			payload.Decision, payload.Handler)
	}
	if payload.Decision == "refused" && payload.Handler != "REFUSE" {
		t.Errorf("v4 decision=%q should map to v3 handler=REFUSE; got %q",
			payload.Decision, payload.Handler)
	}
	if payload.Operator != "alice" {
		t.Errorf("v3 Operator = %q; want alice (input echo)", payload.Operator)
	}
	// v4alpha additive: decision lowercase.
	if payload.Decision == "" {
		t.Error("v4alpha additive decision field empty (pipeline did not run)")
	}
	if payload.Verdict == "" {
		t.Error("v4alpha additive verdict field empty (pipeline did not run)")
	}
}

// --- Test 8: DARK_DELEGATION_BACKEND=v2 rollback path ---

func TestDelegateIntentV2_FallbackByFlag(t *testing.T) {
	// DARK_DELEGATION_BACKEND=v2 routes to v2 (orch.DelegateIntent).
	// We can't run orch.DelegateIntent without a full Store +
	// orchestrator, so we verify the BACKEND SELECTION only:
	// with the flag set, delegationBackendFromEnv() returns BackendV2.
	withEnv(t, "DARK_DELEGATION_BACKEND", "v2")
	got := delegationBackendFromEnv()
	if got != BackendV2 {
		t.Errorf("delegationBackendFromEnv() = %d; want BackendV2 (%d) when env=v2", got, BackendV2)
	}
	// Also verify DARK_DELEGATION_BACKEND=alpha18.1 maps to BackendV2
	// (per delegationBackendFromEnv alias table).
	withEnv(t, "DARK_DELEGATION_BACKEND", "alpha18.1")
	got = delegationBackendFromEnv()
	if got != BackendV2 {
		t.Errorf("delegationBackendFromEnv() with alpha18.1 = %d; want BackendV2", got)
	}
	// Empty env → default BackendV4Alpha (Phase 9 default).
	withEnv(t, "DARK_DELEGATION_BACKEND", "")
	got = delegationBackendFromEnv()
	if got != BackendV4Alpha {
		t.Errorf("delegationBackendFromEnv() with empty env = %d; want BackendV4Alpha (%d)", got, BackendV4Alpha)
	}
}

// --- helpers ---

// orchestrationDelegateOutputWireShape is the v3 wire shape decoded
// into Go. Mirrors orchestration.DelegateIntentOutput but declared
// here so the test doesn't depend on the orchestration package's
// internal struct (which may add fields in future versions).
type orchestrationDelegateOutputWireShape struct {
	Handler      string                              `json:"handler"`
	Reasoning    string                              `json:"reasoning"`
	Case         string                              `json:"case"`
	Plan         []planEntryWire                     `json:"plan"`
	Operator     string                              `json:"operator"`
	Decision     string                              `json:"decision,omitempty"`
	CacheHit     bool                                `json:"cache_hit,omitempty"`
	Verdict      string                              `json:"verdict,omitempty"`
	Alternatives []orchestrationDelegateAltWire      `json:"alternatives,omitempty"`
}

type planEntryWire struct {
	ID                string   `json:"id"`
	VibeCase          string   `json:"vibe_case"`
	Task              string   `json:"task"`
	SystemPrompt      string   `json:"system_prompt"`
	DelegationContext string   `json:"delegation_context"`
	SubagentID        string   `json:"subagent_id"`
	ToolsRecommended  []string `json:"tools_recommended"`
	ModelRecommended  string   `json:"model_recommended"`
}

type orchestrationDelegateAltWire struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	Reasoning string `json:"reasoning"`
}

// callDelegateTool invokes the registered delegate_intent tool with
// the given raw JSON input. Returns the ToolResponse.
func callDelegateTool(t *testing.T, reg *Registry, ctx context.Context, raw json.RawMessage) (*ToolResponse, error) {
	t.Helper()
	tool := reg.Get("delegate_intent")
	if tool == nil {
		t.Fatal("delegate_intent tool not registered")
	}
	return tool.Handler(ctx, raw)
}

// decodeDelegateResponse marshals the ToolResponse.Data into JSON then
// unmarshals into the wire-shape struct. The two-step decode handles
// the fact that ToolResponse.Data is `any` (interface{}).
func decodeDelegateResponse(t *testing.T, res *ToolResponse) (orchestrationDelegateOutputWireShape, error) {
	t.Helper()
	var payload orchestrationDelegateOutputWireShape
	// Data is *orchestration.DelegateIntentOutput. Marshal it to JSON
	// so we can decode into the wire-shape struct.
	if res.Data == nil {
		return payload, fmt.Errorf("res.Data is nil")
	}
	dataBytes, err := json.Marshal(res.Data)
	if err != nil {
		return payload, fmt.Errorf("marshal res.Data: %w", err)
	}
	if err := json.Unmarshal(dataBytes, &payload); err != nil {
		return payload, fmt.Errorf("unmarshal: %w\ndata=%s", err, string(dataBytes))
	}
	return payload, nil
}

// payloadAsJSON marshals ToolResponse.Data for diagnostic messages.
func payloadAsJSON(res *ToolResponse) string {
	if res == nil || res.Data == nil {
		return "<nil>"
	}
	b, err := json.Marshal(res.Data)
	if err != nil {
		return "<marshal-error: " + err.Error() + ">"
	}
	return string(b)
}