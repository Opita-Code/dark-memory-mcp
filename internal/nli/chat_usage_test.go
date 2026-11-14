package nli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChatProvider_Score_CarriesTheProviderUsage: the NLI anchor is a
// provider call too, and it costs like one. The score carries what it
// cost, so the verdict can say what the whole publication cost.
func TestChatProvider_Score_CarriesTheProviderUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"entailment"}}],`+
			`"usage":{"prompt_tokens":995,"completion_tokens":5,"total_tokens":1000}}`)
	}))
	defer srv.Close()

	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, AuthToken: "tok", TimeoutMS: 5000, ModelRev: "test-model"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	score, err := p.Score(context.Background(), "the sky is blue", "sky is blue")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Usage == nil {
		t.Fatal("the score carries no usage, want the provider report")
	}
	if score.Usage.Requests != 1 {
		t.Errorf("Requests = %d, want 1", score.Usage.Requests)
	}
	if score.Usage.Tokens == nil {
		t.Fatal("the tokens are missing, want the provider report")
	}
	if score.Usage.Tokens.TotalTokens != 1000 {
		t.Errorf("TotalTokens = %d, want 1000", score.Usage.Tokens.TotalTokens)
	}
}

// TestChatProvider_Score_WithoutUsageKeepsTheRequestCount: a provider that
// does not report usage does not make the request disappear. The request
// happened; only the cost is unknown.
func TestChatProvider_Score_WithoutUsageKeepsTheRequestCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"entailment"}}]}`)
	}))
	defer srv.Close()

	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, AuthToken: "tok", TimeoutMS: 5000, ModelRev: "test-model"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	score, err := p.Score(context.Background(), "the sky is blue", "sky is blue")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Usage == nil {
		t.Fatal("the score carries no usage, want the request count")
	}
	if score.Usage.Requests != 1 {
		t.Errorf("Requests = %d, want 1", score.Usage.Requests)
	}
	if score.Usage.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil: the provider did not report usage", score.Usage.Tokens)
	}
}

// TestChatProvider_Score_RetryAddsBothAttempts: a truncated answer is
// retried with a bigger budget, and the retry is a request that was made.
// The cost of the score is the sum of the two.
func TestChatProvider_Score_RetryAddsBothAttempts(t *testing.T) {
	peticiones := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peticiones++
		w.Header().Set("Content-Type", "application/json")
		if peticiones == 1 {
			// The model ran out of budget: no label came through.
			fmt.Fprint(w, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":""}}],`+
				`"usage":{"prompt_tokens":500,"completion_tokens":1,"total_tokens":501}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"entailment"}}],`+
			`"usage":{"prompt_tokens":600,"completion_tokens":1,"total_tokens":601}}`)
	}))
	defer srv.Close()

	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, AuthToken: "tok", TimeoutMS: 5000, ModelRev: "test-model"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	score, err := p.Score(context.Background(), "the sky is blue", "sky is blue")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if peticiones != 2 {
		t.Fatalf("requests = %d, want 2 (the truncated answer must be retried)", peticiones)
	}
	if score.Usage == nil {
		t.Fatal("the score carries no usage")
	}
	if score.Usage.Requests != 2 {
		t.Errorf("Requests = %d, want 2 (the retry is a request too)", score.Usage.Requests)
	}
	if score.Usage.Tokens == nil {
		t.Fatal("the tokens are missing, want the sum of the two attempts")
	}
	if score.Usage.Tokens.TotalTokens != 1102 {
		t.Errorf("TotalTokens = %d, want 1102", score.Usage.Tokens.TotalTokens)
	}
}
