package v4judge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/llm/chatwire"
)

// TestJudge_ReasoningModelThinkBlock: a reasoning model answers as
// "<think>...</think>{verdict}". The block is invisible output: it must
// not reach the verdict, and the JSON extractor must not latch onto a
// brace the thinking happened to quote (the thinking usually repeats
// the expected JSON shape).
func TestJudge_ReasoningModelThinkBlock(t *testing.T) {
	think := "<think>The body is a 200 MB ELF binary. It does not look like a clean installation. " +
		"The expected shape is {\"verdict\":\"aligned\"|\"drift_detected\"}. I will answer drift_detected.</think>"
	content := `{"verdict":"drift_detected","confidence":0.91,"reasoning":"The artifact is a raw binary, not a clean installation."}`
	canned := `{"choices":[{"message":{"role":"assistant","content":` + mustJSON(think+content) + `},"finish_reason":"stop"}]}`
	stub := &stubHTTPClient{responses: []stubResponse{{status: 200, body: []byte(canned)}}}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-minimax", ModelRev: "MiniMax-M3"}, stub)

	v, err := j.Judge(context.Background(), "a clean installation", "ELF binary")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Verdict != "drift_detected" {
		t.Errorf("Verdict = %q, want drift_detected", v.Verdict)
	}
	if v.Confidence != 0.91 {
		t.Errorf("Confidence = %f, want 0.91", v.Confidence)
	}
	if strings.Contains(v.Reasoning, "<think>") {
		t.Errorf("Reasoning still carries the reasoning block: %q", v.Reasoning)
	}
}

// TestJudge_TruncatedThinkingRetriesWithBiggerBudget: when the model
// exhausts its budget while thinking (finish_reason="length"), the
// judge retries ONCE with a bigger budget and gets the verdict.
//
// Before this, the judge sent a hardcoded max_tokens of 512, which no
// reasoning model can satisfy: every verdict degraded to the NLI
// fallback (or to needs_human when there was none).
func TestJudge_TruncatedThinkingRetriesWithBiggerBudget(t *testing.T) {
	truncated := `{"choices":[{"message":{"role":"assistant","content":"<think>still thinking</think>"},` +
		`"finish_reason":"length"}]}`
	answer := `{"choices":[{"message":{"role":"assistant","content":` +
		mustJSON(`{"verdict":"aligned","confidence":0.8,"reasoning":"Matches the spec intent."}`) +
		`},"finish_reason":"stop"}]}`
	stub := &stubHTTPClient{responses: []stubResponse{
		{status: 200, body: []byte(truncated)},
		{status: 200, body: []byte(answer)},
	}}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-minimax", ModelRev: "MiniMax-M3"}, stub)

	v, err := j.Judge(context.Background(), "spec intent", "artifact body")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Verdict != "aligned" {
		t.Errorf("Verdict = %q, want aligned", v.Verdict)
	}
	if stub.calls != 2 {
		t.Fatalf("calls = %d, want 2 (the truncated answer must be retried)", stub.calls)
	}
	// The retry must carry a BIGGER budget than the first attempt.
	first := maxTokensOf(t, stub.bodies[0])
	second := maxTokensOf(t, stub.bodies[1])
	if second <= first {
		t.Errorf("retry max_tokens = %d, want > %d (the retry must give the model room to answer)", second, first)
	}
	if second > chatwire.MaxRetryBudgetCap {
		t.Errorf("retry max_tokens = %d, want <= %d", second, chatwire.MaxRetryBudgetCap)
	}
}

// TestJudge_BudgetComesFromTheModelTable: the budget is a property of
// the model, not of the client.
func TestJudge_BudgetComesFromTheModelTable(t *testing.T) {
	canned := `{"choices":[{"message":{"role":"assistant","content":` +
		mustJSON(`{"verdict":"aligned","confidence":0.5,"reasoning":"ok"}`) + `},"finish_reason":"stop"}]}`
	stub := &stubHTTPClient{responses: []stubResponse{{status: 200, body: []byte(canned)}}}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-minimax", ModelRev: "MiniMax-M3"}, stub)

	if _, err := j.Judge(context.Background(), "spec intent", "artifact body"); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	got := maxTokensOf(t, stub.bodies[0])
	want := chatwire.ResolveMaxTokens("MiniMax-M3", 0)
	if got != want {
		t.Errorf("max_tokens = %d, want %d (the per-model table)", got, want)
	}
	if got <= 512 {
		t.Errorf("max_tokens = %d: the old hardcoded 512 is below every reasoning model's floor", got)
	}
}

// TestParseVerdictResponse_ThinkOnlyIsAnError: a body that is nothing
// but a reasoning block is a contract failure, not a verdict.
func TestParseVerdictResponse_ThinkOnlyIsAnError(t *testing.T) {
	if _, err := parseVerdictResponse([]byte(`<think>thinking only</think>`)); err == nil {
		t.Fatal("expected an error when the answer is only a reasoning block")
	}
}

// TestChatwire_StripThinkBlocks: the shared helper removes the block
// and keeps the visible answer.
func TestChatwire_StripThinkBlocks(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"<think>a</think>{\"verdict\":\"aligned\"}", `{"verdict":"aligned"}`},
		{"plain answer", "plain answer"},
		{"<think>a</think><think>b</think>x", "x"},
		{"<think>unterminated", ""},
	}
	for _, c := range cases {
		if got := chatwire.StripThinkBlocks(c.in); got != c.want {
			t.Errorf("StripThinkBlocks(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// maxTokensOf reads the max_tokens field out of a captured request
// body.
func maxTokensOf(t *testing.T, body []byte) int {
	t.Helper()
	var payload struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return payload.MaxTokens
}

// mustJSON encodes a string as a JSON literal.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
