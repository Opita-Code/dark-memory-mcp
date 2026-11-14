// Package orchestration — drift_judge_usage_test.go
//
// What a verdict cost the PROVIDER. The judge and the NLI chain are both
// provider calls, and the caller (the runtime that publishes a verdict)
// needs to know how many requests were made and how many tokens they
// consumed.
//
// The three things this file pins down:
//
//  1. The verdict_json carries the account, with the numbers the provider
//     reported.
//  2. A provider that does not report usage does NOT make the cost zero:
//     the requests are counted and the tokens are null.
//  3. When the judge falls through to the NLI chain, the account is the
//     sum of both paths. The failed judge requests were made too, and they
//     were billed.
package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/artifact"
	"github.com/dark-agents/dark-memory-mcp/internal/llm/chatwire"
	"github.com/dark-agents/dark-memory-mcp/internal/nli"
)

// cannedAlignedConUso is the judge answer, with the usage the provider
// reports: 1000 tokens in, 10 out.
func cannedAlignedConUso() string {
	return `{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"aligned\",\"confidence\":0.9,\"reasoning\":\"matches spec\"}"}}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010}}`
}

// usageDelVerdictJSON reads the account out of the verdict_json.
func usageDelVerdictJSON(t *testing.T, verdictJSON string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(verdictJSON), &v); err != nil {
		t.Fatalf("the verdict_json is not JSON: %v (%s)", err, verdictJSON)
	}
	u, ok := v["usage"].(map[string]any)
	if !ok {
		t.Fatalf("the verdict_json carries no usage: %s", verdictJSON)
	}
	return u
}

// TestDriftJudge_VerdictJSONCarriesTheUsage: the provider reported what
// the verdict cost, and the verdict says so.
func TestDriftJudge_VerdictJSONCarriesTheUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("a clean artifact"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithLLMJudge(newStubLLMJudge(t, []stubHTTPResponseItem{
		{status: 200, body: cannedAlignedConUso()},
	}))

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "this spec is long enough to pass principle 5 of self critique",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Usage == nil {
		t.Fatal("the verdict carries no usage, want the provider report")
	}
	if out.Usage.Requests != 1 {
		t.Errorf("Requests = %d, want 1", out.Usage.Requests)
	}
	if out.Usage.Tokens == nil || out.Usage.Tokens.TotalTokens != 1010 {
		t.Fatalf("Tokens = %+v, want a total of 1010", out.Usage.Tokens)
	}

	uso := usageDelVerdictJSON(t, out.VerdictJSON)
	if uso["requests"] != float64(1) {
		t.Errorf("the verdict_json says %v requests, want 1", uso["requests"])
	}
	tokens, ok := uso["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("the verdict_json says the tokens are unknown: %s", out.VerdictJSON)
	}
	if tokens["total"] != float64(1010) {
		t.Errorf("the verdict_json says %v total tokens, want 1010", tokens["total"])
	}
}

// TestDriftJudge_VerdictJSONWithoutUsageKeepsTheRequests: a provider that
// says nothing about its tokens does not make the cost zero. The request
// is counted and the tokens are null: a missing datum is not a cost of
// zero.
func TestDriftJudge_VerdictJSONWithoutUsageKeepsTheRequests(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("a clean artifact"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithLLMJudge(newStubLLMJudge(t, []stubHTTPResponseItem{
		{status: 200, body: cannedAligned()},
	}))

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "this spec is long enough to pass principle 5 of self critique",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Usage == nil {
		t.Fatal("the verdict carries no usage, want the request count")
	}
	if out.Usage.Requests != 1 {
		t.Errorf("Requests = %d, want 1", out.Usage.Requests)
	}
	if out.Usage.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: the provider did not report usage", out.Usage.Tokens)
	}

	uso := usageDelVerdictJSON(t, out.VerdictJSON)
	if uso["requests"] != float64(1) {
		t.Errorf("the verdict_json says %v requests, want 1", uso["requests"])
	}
	tokens, ok := uso["tokens"]
	if !ok {
		t.Fatalf("the verdict_json has no tokens field: %s", out.VerdictJSON)
	}
	if tokens != nil {
		t.Errorf("the verdict_json says the tokens are %v, want null", tokens)
	}
}

// TestDriftJudge_FallThroughCombinesBothPaths: the judge failed (two
// requests) and the NLI chain answered (one more). The account is the
// sum of the three: the failed judge requests were made, and they were
// billed.
func TestDriftJudge_FallThroughCombinesBothPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.txt")
	if err := os.WriteFile(path, []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}

	o, _ := newDriftJudgeTestOrchestrator(t)
	o.WithLLMJudge(newStubLLMJudge(t, []stubHTTPResponseItem{
		{status: 200, body: "nope1"},
		{status: 200, body: "nope2"},
	}))

	score := makeNLIScore(nli.LabelEntailment, 0.88)
	score.Usage = &chatwire.ProviderUsage{
		Requests: 1,
		Tokens:   &chatwire.Usage{PromptTokens: 995, CompletionTokens: 5, TotalTokens: 1000},
	}
	o.WithNLIRouter(&controllableProvider{score: score})

	out, err := o.DriftJudge(context.Background(), DriftJudgeInput{
		ArtifactRef: artifact.ArtifactRef{Kind: artifact.KindFile, Path: path},
		SpecIntent:  "this spec is long enough to pass principle 5 of self critique",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.Usage == nil {
		t.Fatal("the verdict carries no usage")
	}
	if out.Usage.Requests != 3 {
		t.Errorf("Requests = %d, want 3 (two judge attempts and the NLI call)", out.Usage.Requests)
	}
	// The judge did not report usage and the NLI did: the total is
	// unknown, and the requests are known.
	if out.Usage.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: the judge attempts did not report usage", out.Usage.Tokens)
	}

	uso := usageDelVerdictJSON(t, out.VerdictJSON)
	if uso["requests"] != float64(3) {
		t.Errorf("the verdict_json says %v requests, want 3", uso["requests"])
	}
}
