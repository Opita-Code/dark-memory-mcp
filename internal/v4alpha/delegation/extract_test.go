// Package delegation — tests for validate.go + extract.go.
//
// Strategy:
//   - validate.go: pure-function tests (no LLM, no I/O)
//   - extract.go: mock LLM client (FakeLLM in this file); 7 scenarios
//   - cache.go: covered by cache_test.go (separate file)
//
// Drift_judge validation is wired through validateSubtasks which uses
// the same FakeLLM. We provide canned responses for both EXTRACT and
// JUDGE prompts by switching on response_format's schema field.
package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// ---------- FakeLLM ----------

// FakeLLM is a deterministic mock LLM client for tests. Returns canned
// responses based on the request's UserPrompt (substring match). Lets
// tests assert behavior without HTTP roundtrips.
type FakeLLM struct {
	mu          sync.Mutex
	Calls       []judge.LLMRequest
	DefaultResp string
	RespByMatch map[string]string // substring of UserPrompt → response JSON
	Err         error
}

func NewFakeLLM() *FakeLLM {
	return &FakeLLM{
		RespByMatch: make(map[string]string),
	}
}

// Complete satisfies judge.LLMClient. Picks response based on UserPrompt
// substring matching; falls back to DefaultResp; returns Err if set.
func (f *FakeLLM) Complete(ctx context.Context, req judge.LLMRequest) (*judge.LLMResponse, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	// Match against user prompt.
	for marker, resp := range f.RespByMatch {
		if strings.Contains(req.UserPrompt, marker) {
			return &judge.LLMResponse{
				Content: resp,
				Provider: "fake",
				Model:    "fake-1",
			}, nil
		}
	}
	if f.DefaultResp != "" {
		return &judge.LLMResponse{
			Content: f.DefaultResp,
			Provider: "fake",
			Model:    "fake-1",
		}, nil
	}
	return &judge.LLMResponse{Content: `{"decision":"inline","reasoning":"default","subtasks":[]}`, Provider: "fake"}, nil
}

// ---------- Test helpers ----------

func newTestExtractor(fake *FakeLLM) *Extractor {
	return &Extractor{
		LLM:       fake,
		Cache:     NewExtractCache(DefaultCacheTTL),
		Operator:  "test",
		ProjectID: "test-project",
	}
}

// validExtractJSON returns a valid EXTRACT JSON for the given subtasks.
func validExtractJSON(subtasks ...string) string {
	type st struct {
		ID, Description string
		Dependencies    []string
	}
	type resp struct {
		Decision, Reasoning string
		Subtasks            []st
	}
	out := resp{Decision: "delegate", Reasoning: "test extract"}
	for i, desc := range subtasks {
		out.Subtasks = append(out.Subtasks, st{ID: "subtask-" + itoa(i+1), Description: desc})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// validValidateJSON returns a valid JUDGE JSON.
func validValidateJSON(verdict, reasoning string) string {
	b, _ := json.Marshal(map[string]string{"verdict": verdict, "reasoning": reasoning})
	return string(b)
}

// itoa is a tiny helper (avoid pulling strconv just for one usage).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if negative {
		return "-" + digits
	}
	return digits
}

// ---------- validate.go tests ----------

// TestValidateSubtasks_DropsShortContent checks that subtasks with
// description < MinSubtaskLength are dropped with a warning.
func TestValidateSubtasks_DropsShortContent(t *testing.T) {
	raw := []Subtask{
		{ID: "s1", Description: "this is a valid long description here"},
		{ID: "s2", Description: "short"}, // < MinSubtaskLength (10)
		{ID: "s3", Description: "another valid subtask description here"},
	}
	res := ValidateSubtasks(raw)
	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}
	if len(res.Subtasks) != 2 {
		t.Fatalf("Subtasks = %d; want 2 (short dropped)", len(res.Subtasks))
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("Warnings = %d; want 1", len(res.Warnings))
	}
	if res.Warnings[0].ID != "drop_short_subtask" {
		t.Errorf("Warning[0].ID = %q; want drop_short_subtask", res.Warnings[0].ID)
	}
}

// TestValidateSubtasks_TruncatesOverflow checks that >MaxSubtasks is truncated.
func TestValidateSubtasks_TruncatesOverflow(t *testing.T) {
	raw := make([]Subtask, 0, 12)
	for i := 0; i < 12; i++ {
		raw = append(raw, Subtask{
			ID:          "s" + itoa(i+1),
			Description: "this is subtask number " + itoa(i+1) + " description",
		})
	}
	res := ValidateSubtasks(raw)
	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}
	if len(res.Subtasks) != MaxSubtasks {
		t.Errorf("Subtasks = %d; want %d (truncated)", len(res.Subtasks), MaxSubtasks)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("Warnings = %d; want 1", len(res.Warnings))
	}
	if res.Warnings[0].ID != "truncate_overflow" {
		t.Errorf("Warning[0].ID = %q; want truncate_overflow", res.Warnings[0].ID)
	}
}

// TestValidateSubtasks_NormalizesIDs checks that empty IDs are renamed
// to subtask-1, subtask-2, ...
func TestValidateSubtasks_NormalizesIDs(t *testing.T) {
	raw := []Subtask{
		{ID: "", Description: "first valid subtask description here"},
		{ID: "weird-id-xyz", Description: "second valid subtask description here"},
	}
	res := ValidateSubtasks(raw)
	if res.Err != nil {
		t.Fatalf("unexpected err: %v", res.Err)
	}
	if len(res.Subtasks) != 2 {
		t.Fatalf("Subtasks = %d; want 2", len(res.Subtasks))
	}
	if res.Subtasks[0].ID != "subtask-1" {
		t.Errorf("Subtasks[0].ID = %q; want subtask-1", res.Subtasks[0].ID)
	}
	if res.Subtasks[1].ID != "subtask-2" {
		t.Errorf("Subtasks[1].ID = %q; want subtask-2", res.Subtasks[1].ID)
	}
	if len(res.Warnings) < 1 {
		t.Errorf("Warnings should have at least one entry (rename_missing_id)")
	}
}

// TestValidateSubtasks_DetectsCycle checks that a 2-node cycle is caught.
func TestValidateSubtasks_DetectsCycle(t *testing.T) {
	raw := []Subtask{
		{ID: "subtask-1", Description: "first subtask description here", Dependencies: []string{"subtask-2"}},
		{ID: "subtask-2", Description: "second subtask description here", Dependencies: []string{"subtask-1"}},
	}
	res := ValidateSubtasks(raw)
	if res.Err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(res.Err.Error(), "cycle") {
		t.Errorf("Err = %v; want cycle mention", res.Err)
	}
}

// TestValidateSubtasks_DropsSelfDep checks that a self-dependency is
// silently dropped (not flagged as a cycle).
func TestValidateSubtasks_DropsSelfDep(t *testing.T) {
	raw := []Subtask{
		{ID: "subtask-1", Description: "first subtask description here", Dependencies: []string{"subtask-1"}},
	}
	res := ValidateSubtasks(raw)
	if res.Err != nil {
		t.Fatalf("self-dep should NOT be a cycle; got err: %v", res.Err)
	}
	if len(res.Subtasks) != 1 {
		t.Fatalf("Subtasks = %d; want 1", len(res.Subtasks))
	}
	if len(res.Subtasks[0].Dependencies) != 0 {
		t.Errorf("self-dep should be dropped; got %v", res.Subtasks[0].Dependencies)
	}
}

// TestTopoSort_StableOrder verifies Kahn's algorithm preserves original
// order for zero-indegree nodes.
func TestTopoSort_StableOrder(t *testing.T) {
	subs := []Subtask{
		{ID: "subtask-1", Description: "alpha description here"},
		{ID: "subtask-2", Description: "beta description here"},
		{ID: "subtask-3", Description: "gamma description here", Dependencies: []string{"subtask-1"}},
	}
	sorted := TopoSort(subs)
	if len(sorted) != 3 {
		t.Fatalf("len = %d; want 3", len(sorted))
	}
	if sorted[0].ID != "subtask-1" {
		t.Errorf("sorted[0] = %q; want subtask-1", sorted[0].ID)
	}
	if sorted[2].ID != "subtask-3" {
		t.Errorf("sorted[2] = %q; want subtask-3 (last, depends on subtask-1)", sorted[2].ID)
	}
}

// ---------- extract.go tests ----------

// TestExtract_HappyPathAligned verifies the EXTRACT happy path:
// LLM produces valid subtasks, judge says aligned.
func TestExtract_HappyPathAligned(t *testing.T) {
	fake := NewFakeLLM()
	// EXTRACT call returns 2 subtasks. JUDGE call says aligned.
	extractJSON := validExtractJSON(
		"deploy the new memory subsystem to staging",
		"monitor latency p95 for one hour after deploy",
	)
	fake.RespByMatch["Decompose into atomic subtasks"] = extractJSON
	fake.DefaultResp = validValidateJSON("aligned", "good coverage, no overlap")

	ex := newTestExtractor(fake)
	res, err := ex.Extract(context.Background(), "C7", "deploy and monitor memory subsystem")
	if err != nil {
		t.Fatalf("Extract err: %v", err)
	}
	if res.Decision != "delegate" {
		t.Errorf("Decision = %q; want delegate", res.Decision)
	}
	if len(res.Subtasks) != 2 {
		t.Errorf("Subtasks = %d; want 2", len(res.Subtasks))
	}
	if res.Verdict != "aligned" {
		t.Errorf("Verdict = %q; want aligned", res.Verdict)
	}
	if len(res.Alternatives) != 0 {
		t.Errorf("Alternatives = %d; want 0 (aligned)", len(res.Alternatives))
	}
	if res.CacheHit {
		t.Error("CacheHit = true; want false (first call)")
	}
}

// TestExtract_CacheHit verifies the second call returns cached result.
func TestExtract_CacheHit(t *testing.T) {
	fake := NewFakeLLM()
	extractJSON := validExtractJSON("do the thing here now properly")
	fake.RespByMatch["Decompose into atomic subtasks"] = extractJSON
	fake.DefaultResp = validValidateJSON("aligned", "ok")

	task := "do the thing here now properly"
	vibe := "C7"

	ex := newTestExtractor(fake)
	res1, _ := ex.Extract(context.Background(), vibe, task)
	if res1.CacheHit {
		t.Fatal("first call should not be cache hit")
	}

	res2, _ := ex.Extract(context.Background(), vibe, task)
	if !res2.CacheHit {
		t.Error("second call should be cache hit")
	}
	if res2.Verdict != "cached" {
		t.Errorf("Verdict on cache hit = %q; want cached", res2.Verdict)
	}
	if len(fake.Calls) != 2 {
		t.Errorf("LLM call count = %d; want 2 (one extract + one validate per first call, zero on hit)", len(fake.Calls))
	}
}

// TestExtract_LLMNetworkError verifies that a transport-level error
// surfaces as needs_human with the right alternatives.
func TestExtract_LLMNetworkError(t *testing.T) {
	fake := NewFakeLLM()
	fake.Err = errors.New("connection refused")

	ex := newTestExtractor(fake)
	res, _ := ex.Extract(context.Background(), "C7",
		"some task that requires decomposition into multiple parts")
	if res.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want needs_human", res.Verdict)
	}
	if len(res.Alternatives) == 0 {
		t.Fatal("Alternatives = 0; want >=1")
	}
	// First alternative should be fallback-plan.
	if res.Alternatives[0].ID != "fallback-plan" {
		t.Errorf("Alternatives[0].ID = %q; want fallback-plan", res.Alternatives[0].ID)
	}
}

// TestExtract_LLMParseError verifies that an unparseable response
// surfaces as needs_human with llm_parse_error alternatives.
func TestExtract_LLMParseError(t *testing.T) {
	fake := NewFakeLLM()
	fake.DefaultResp = `this is not json at all`

	ex := newTestExtractor(fake)
	res, _ := ex.Extract(context.Background(), "C7",
		"task description that should trigger an LLM extraction attempt")
	if res.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want needs_human", res.Verdict)
	}
	foundParseError := false
	for _, a := range res.Alternatives {
		if a.ID == "fallback-plan" || a.ID == "accept-partial" {
			foundParseError = true
		}
	}
	if !foundParseError {
		t.Errorf("Alternatives = %v; want fallback-plan or accept-partial", res.Alternatives)
	}
}

// TestExtract_JudgeDriftExhausted verifies that drift_detected on all
// retries surfaces as needs_human with judge_drift_exhausted alternatives.
func TestExtract_JudgeDriftExhausted(t *testing.T) {
	fake := NewFakeLLM()
	extractJSON := validExtractJSON("subtask one description here", "subtask two description here")
	fake.RespByMatch["Decompose into atomic subtasks"] = extractJSON
	fake.DefaultResp = validValidateJSON("drift_detected", "always drift")

	ex := newTestExtractor(fake)
	res, _ := ex.Extract(context.Background(), "C7", "do the compound task here please")
	if res.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want needs_human", res.Verdict)
	}
	foundExhausted := false
	for _, a := range res.Alternatives {
		if a.ID == "accept-as-is" || a.ID == "fallback-plan" || a.ID == "custom-instructions" {
			foundExhausted = true
		}
	}
	if !foundExhausted {
		t.Errorf("Alternatives = %v; want accept-as-is / fallback-plan / custom-instructions", res.Alternatives)
	}
}

// TestExtract_JudgeNeedsHuman verifies that judge returning needs_human
// surfaces immediately (no retry).
func TestExtract_JudgeNeedsHuman(t *testing.T) {
	fake := NewFakeLLM()
	extractJSON := validExtractJSON("subtask one description here")
	fake.RespByMatch["Decompose into atomic subtasks"] = extractJSON
	fake.DefaultResp = validValidateJSON("needs_human", "can't decide quality")

	ex := newTestExtractor(fake)
	res, _ := ex.Extract(context.Background(), "C7", "do the compound task here please")
	if res.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want needs_human", res.Verdict)
	}
	// Should be exactly 1 LLM call (extract + judge call).
	if len(fake.Calls) != 2 {
		t.Errorf("LLM call count = %d; want 2 (extract + judge, no retry on needs_human)", len(fake.Calls))
	}
}

// TestExtract_DependencyCycle verifies that a cycle in extracted
// dependencies surfaces as needs_human with topo_cycle alternatives.
func TestExtract_DependencyCycle(t *testing.T) {
	fake := NewFakeLLM()
	cycleJSON := `{
		"decision": "delegate",
		"reasoning": "compound",
		"subtasks": [
			{"id": "subtask-1", "description": "first task description here", "dependencies": ["subtask-2"]},
			{"id": "subtask-2", "description": "second task description here", "dependencies": ["subtask-1"]}
		]
	}`
	fake.RespByMatch["Decompose into atomic subtasks"] = cycleJSON
	fake.DefaultResp = validValidateJSON("aligned", "ok")

	ex := newTestExtractor(fake)
	res, _ := ex.Extract(context.Background(), "C7", "do the compound task here please")
	if res.Verdict != "needs_human" {
		t.Errorf("Verdict = %q; want needs_human (cycle)", res.Verdict)
	}
	foundCycle := false
	for _, a := range res.Alternatives {
		if a.ID == "drop-deps" || a.ID == "fallback-plan" {
			foundCycle = true
		}
	}
	if !foundCycle {
		t.Errorf("Alternatives = %v; want drop-deps / fallback-plan", res.Alternatives)
	}
}

// ---------- ShouldExtract tests ----------

// TestShouldExtract verifies the gating rule.
func TestShouldExtract(t *testing.T) {
	cases := []struct {
		decision, vibeCase, task string
		want                      bool
	}{
		{"inline", "C7", "any task", false},
		{"refused", "C7", "any task", false},
		{"delegate", "C1", "short", false},                                    // short + non-C7
		{"delegate", "C7", "short task description here", true},               // C7 triggers
		{"delegate", "C1", strings.Repeat("a", 250), true},                    // length>200 triggers
		{"delegate", "C7", strings.Repeat("a", 50), true},                     // C7 even short
	}
	for _, tc := range cases {
		got := ShouldExtract(tc.decision, tc.vibeCase, tc.task)
		if got != tc.want {
			t.Errorf("ShouldExtract(decision=%q, vibe=%q, len=%d) = %v; want %v",
				tc.decision, tc.vibeCase, len(tc.task), got, tc.want)
		}
	}
}