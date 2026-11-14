package v4judge

import (
	"context"
	"testing"
)

// cuerpoConUso builds an OpenAI-compatible response that carries a usage
// report, the way the real providers do.
func cuerpoConUso(content, finish string, prompt, completion int) string {
	return `{"choices":[{"message":{"role":"assistant","content":` + mustJSON(content) +
		`},"finish_reason":"` + finish + `"}],` +
		`"usage":{"prompt_tokens":` + itoa(prompt) + `,"completion_tokens":` + itoa(completion) +
		`,"total_tokens":` + itoa(prompt+completion) + `}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// TestJudge_UsageIsCarried: the provider reports what the verdict cost,
// and the verdict carries it. Without this the caller cannot say anything
// about the cost of a publication.
func TestJudge_UsageIsCarried(t *testing.T) {
	body := cuerpoConUso(`{"verdict":"aligned","confidence":0.9,"reasoning":"Matches."}`, "stop", 1071, 295)
	stub := &stubHTTPClient{responses: []stubResponse{{status: 200, body: []byte(body)}}}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-minimax", ModelRev: "MiniMax-M3"}, stub)

	v, err := j.Judge(context.Background(), "spec intent", "artifact body")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Usage == nil {
		t.Fatal("the verdict carries no usage, want the provider report")
	}
	if v.Usage.Requests != 1 {
		t.Errorf("Requests = %d, want 1", v.Usage.Requests)
	}
	if v.Usage.Tokens == nil {
		t.Fatal("the tokens are missing, want the provider report")
	}
	if v.Usage.Tokens.TotalTokens != 1366 {
		t.Errorf("TotalTokens = %d, want 1366", v.Usage.Tokens.TotalTokens)
	}
}

// TestJudge_ProviderWithoutUsageKeepsTheRequestCount: a provider that
// says nothing about its tokens does not make the request disappear. The
// request happened and it is billed; only the cost is unknown. A missing
// datum is not a cost of zero.
func TestJudge_ProviderWithoutUsageKeepsTheRequestCount(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":` +
		mustJSON(`{"verdict":"aligned","confidence":0.9,"reasoning":"Matches."}`) +
		`},"finish_reason":"stop"}]}`
	stub := &stubHTTPClient{responses: []stubResponse{{status: 200, body: []byte(body)}}}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-minimax", ModelRev: "MiniMax-M3"}, stub)

	v, err := j.Judge(context.Background(), "spec intent", "artifact body")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Usage == nil {
		t.Fatal("the verdict carries no usage, want the request count")
	}
	if v.Usage.Requests != 1 {
		t.Errorf("Requests = %d, want 1", v.Usage.Requests)
	}
	if v.Usage.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: the provider did not report usage", v.Usage.Tokens)
	}
}

// TestJudge_RetryAddsBothAttempts: the judge reissued the request because
// the model ran out of budget, and the retry is a request that was made.
// The cost of the verdict is the sum of the two.
func TestJudge_RetryAddsBothAttempts(t *testing.T) {
	truncated := cuerpoConUso("<think>still thinking</think>", "length", 1000, 10)
	answer := cuerpoConUso(`{"verdict":"aligned","confidence":0.8,"reasoning":"Matches."}`, "stop", 1100, 20)
	stub := &stubHTTPClient{responses: []stubResponse{
		{status: 200, body: []byte(truncated)},
		{status: 200, body: []byte(answer)},
	}}
	j := makeJudge(t, ProviderConfig{ProviderID: "judge-minimax", ModelRev: "MiniMax-M3"}, stub)

	v, err := j.Judge(context.Background(), "spec intent", "artifact body")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Usage == nil {
		t.Fatal("the verdict carries no usage")
	}
	if v.Usage.Requests != 2 {
		t.Errorf("Requests = %d, want 2 (the retry is a request too)", v.Usage.Requests)
	}
	if v.Usage.Tokens == nil {
		t.Fatal("the tokens are missing, want the sum of the two attempts")
	}
	// 1010 of the first attempt plus 1120 of the second.
	if v.Usage.Tokens.TotalTokens != 2130 {
		t.Errorf("TotalTokens = %d, want 2130", v.Usage.Tokens.TotalTokens)
	}
}

// TestJudge_RetryWithoutUsageMakesTheTokensUnknown: the first attempt
// reported its usage and the retry did not. The requests are known; the
// total is not, and publishing the partial sum would be a wrong number.
func TestJudge_RetryWithoutUsageMakesTheTokensUnknown(t *testing.T) {
	truncated := cuerpoConUso("<think>still thinking</think>", "length", 1000, 10)
	answer := `{"choices":[{"message":{"role":"assistant","content":` +
		mustJSON(`{"verdict":"aligned","confidence":0.8,"reasoning":"Matches."}`) +
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
	if v.Usage == nil {
		t.Fatal("the verdict carries no usage")
	}
	if v.Usage.Requests != 2 {
		t.Errorf("Requests = %d, want 2", v.Usage.Requests)
	}
	if v.Usage.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: one of the two attempts did not report usage", v.Usage.Tokens)
	}
}
