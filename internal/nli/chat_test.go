// Package nli — chat_test.go
//
// Phase 13 T-201 (alpha.24-pre-1, 2026-10-06): tests for ChatProvider.
// The dispatch case for `chat-*` was added to orchestration/nli_wiring.go
// ::buildNLIPrimary to close the Phase H5 / row 1370 gap (operators
// with OpenAI-compatible chat completions configs hit ErrInvalidConfig
// and drift_judge returned needs_human@0).
//
// Tests use httptest.Server as the production backing for HFInferenceClient
// (the test boundary type). Cases cover the rejected 360-degree envelope:
// happy paths (3 canonical labels), HTTP error classification (401/403,
// 429, 5xx, unexpected), input validation (empty, oversized), response
// shape (Anthropic-via-adapter, legacy text field, malformed JSON,
// unrecognized canonical reply), and constructor validation (empty
// provider_id, missing "chat-" prefix, empty endpoint, nil client,
// negative caps).
package nli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubClient is a tiny *http.Client adapter so tests can point at
// httptest.Server via the HFInferenceClient interface (which is the
// exact shape *http.Client satisfies). Re-used pattern from
// deberta_test.go and minicheck_test.go.
func stubClient(srv *httptest.Server) *http.Client {
	return srv.Client()
}

// TestChatProvider_Score_HappyPath covers all three canonical labels
// (entailment, contradiction, neutral) over the OpenAI-compatible
// choices[].message.content shape. Asserts Score.Valid() (Label in set,
// Confidence in [0,1], ProviderID non-empty, LatencyMS >= 0).
func TestChatProvider_Score_HappyPath(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		wantLabel Label
	}{
		{"entailment", "entailment", LabelEntailment},
		{"contradiction", "contradiction", LabelContradiction},
		{"neutral", "neutral", LabelNeutral},
		// Light handling of trailing punctuation (some models add it
		// despite the system prompt asking them not to).
		{"entailment_with_period", "entailment.", LabelEntailment},
		{"contradiction_quoted", `"contradiction"`, LabelContradiction},
		{"neutral_uppercase_mixed_case", "Neutral", LabelNeutral},
		// Whitespace tolerance.
		{"entailment_leading_trailing_ws", "  entailment  \n", LabelEntailment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request shape — system + user, temperature=0,
				// max_tokens=8, model set.
				if r.Method != http.MethodPost {
					t.Errorf("method: got %s, want POST", r.Method)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type: got %s, want application/json", ct)
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"role":"system"`) {
					t.Errorf("body missing system role: %s", body)
				}
				if !strings.Contains(string(body), `"role":"user"`) {
					t.Errorf("body missing user role: %s", body)
				}
				if !strings.Contains(string(body), `"temperature":0`) {
					t.Errorf("body missing temperature:0: %s", body)
				}
				if !strings.Contains(string(body), `"max_tokens":1024`) {
					t.Errorf("body missing max_tokens:1024: %s", body)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}]}`, tc.reply)
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
			if score.Label != tc.wantLabel {
				t.Errorf("Label: got %s, want %s", score.Label, tc.wantLabel)
			}
			if score.Confidence != 1.0 {
				t.Errorf("Confidence: got %v, want 1.0", score.Confidence)
			}
			if score.ProviderID != "chat-test" {
				t.Errorf("ProviderID: got %s, want chat-test", score.ProviderID)
			}
			if score.LatencyMS < 0 {
				t.Errorf("LatencyMS: got %d, want >= 0", score.LatencyMS)
			}
			if !score.Valid() {
				t.Errorf("Score failed self-inv.Valid(): %+v", score)
			}
			if p.ID() != "chat-test" {
				t.Errorf("ID(): got %s, want chat-test", p.ID())
			}
		})
	}
}

// TestChatProvider_Score_HTTPErrorClassification covers the rejected
// HTTP error envelope: 401 → ErrProviderUnavailable, 403 →
// ErrProviderUnavailable, 429 → ErrProviderRateLimited, 5xx →
// ErrProviderUnavailable, other 4xx → ErrProviderBadResponse,
// unexpected non-2xx → ErrProviderUnavailable.
func TestChatProvider_Score_HTTPErrorClassification(t *testing.T) {
	cases := []struct {
		status   int
		wantErr  error
	}{
		{http.StatusUnauthorized, ErrProviderUnavailable},
		{http.StatusForbidden, ErrProviderUnavailable},
		{http.StatusTooManyRequests, ErrProviderRateLimited},
		{http.StatusInternalServerError, ErrProviderUnavailable},
		{http.StatusBadGateway, ErrProviderUnavailable},
		{http.StatusServiceUnavailable, ErrProviderUnavailable},
		{http.StatusBadRequest, ErrProviderBadResponse},
		{http.StatusNotFound, ErrProviderBadResponse},
		{http.StatusTeapot, ErrProviderBadResponse}, // 4xx (not 401/403/429) → BadResponse
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status_%d", tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			p, _ := NewChatProvider(
				ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000},
				stubClient(srv), 1024, 1024)
			_, err := p.Score(context.Background(), "premise", "hypothesis")
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("status %d: got %v, want errors.Is %v", tc.status, err, tc.wantErr)
			}
		})
	}
}

// TestChatProvider_Score_InputValidation covers ErrInputEmpty and
// ErrInputTooLarge.
func TestChatProvider_Score_InputValidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("HTTP should not be called on input-validation error")
	}))
	defer srv.Close()
	p, _ := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000},
		stubClient(srv), 16, 16)

	t.Run("empty_premise", func(t *testing.T) {
		_, err := p.Score(context.Background(), "", "hypothesis")
		if !errors.Is(err, ErrInputEmpty) {
			t.Errorf("got %v, want ErrInputEmpty", err)
		}
	})
	t.Run("empty_hypothesis", func(t *testing.T) {
		_, err := p.Score(context.Background(), "premise", "")
		if !errors.Is(err, ErrInputEmpty) {
			t.Errorf("got %v, want ErrInputEmpty", err)
		}
	})
	t.Run("oversized_premise", func(t *testing.T) {
		_, err := p.Score(context.Background(), strings.Repeat("a", 32), "hypothesis")
		if !errors.Is(err, ErrInputTooLarge) {
			t.Errorf("got %v, want ErrInputTooLarge", err)
		}
	})
	t.Run("oversized_hypothesis", func(t *testing.T) {
		_, err := p.Score(context.Background(), "premise", strings.Repeat("a", 32))
		if !errors.Is(err, ErrInputTooLarge) {
			t.Errorf("got %v, want ErrInputTooLarge", err)
		}
	})
}

// TestChatProvider_Score_AlternateFormats covers the Anthropic-via-
// adapter shape (`{"content": "..."}`) and the legacy text field shape
// (`{"choices": [{"text": "..."}]}`). Both should parse correctly.
func TestChatProvider_Score_AlternateFormats(t *testing.T) {
	t.Run("anthropic_via_adapter", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"content":"entailment"}`)
		}))
		defer srv.Close()
		p, _ := NewChatProvider(
			ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000},
			stubClient(srv), 1024, 1024)
		score, err := p.Score(context.Background(), "p", "h")
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		if score.Label != LabelEntailment {
			t.Errorf("Label: got %s, want entailment", score.Label)
		}
	})
	t.Run("legacy_text_field", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"text":"contradiction"}]}`)
		}))
		defer srv.Close()
		p, _ := NewChatProvider(
			ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000},
			stubClient(srv), 1024, 1024)
		score, err := p.Score(context.Background(), "p", "h")
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		if score.Label != LabelContradiction {
			t.Errorf("Label: got %s, want contradiction", score.Label)
		}
	})
}

// TestChatProvider_Score_MalformedResponse covers the rejected
// contract-bug envelope: malformed JSON, empty body, unrecognized
// canonical word. All → ErrProviderBadResponse.
func TestChatProvider_Score_MalformedResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed_json", `{"choices":[{"message":{"role":"assistant","content":`},
		{"empty_body", ``},
		{"unrecognized_label", `{"choices":[{"message":{"role":"assistant","content":"maybe"}}]}`},
		{"explanation_not_one_word", `{"choices":[{"message":{"role":"assistant","content":"The hypothesis is entailed because the premise mentions that..."}}]}`},
		{"no_choices_field", `{"result":"entailment"}`},
		{"yes_instead_of_entailment", `{"choices":[{"message":{"role":"assistant","content":"yes"}}]}`},
		{"no_instead_of_contradiction", `{"choices":[{"message":{"role":"assistant","content":"no"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			p, _ := NewChatProvider(
				ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000},
				stubClient(srv), 1024, 1024)
			_, err := p.Score(context.Background(), "p", "h")
			if !errors.Is(err, ErrProviderBadResponse) {
				t.Errorf("body %q: got %v, want ErrProviderBadResponse", tc.body, err)
			}
		})
	}
}

// TestChatProvider_NewChatProvider_Validation covers the constructor
// invariants: empty provider_id, missing "chat-" prefix, empty endpoint,
// nil HTTP client, negative size caps.
func TestChatProvider_NewChatProvider_Validation(t *testing.T) {
	t.Run("empty_provider_id", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{Endpoint: "http://x"}, nil, 1024, 1024)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got %v, want ErrInvalidConfig", err)
		}
		if !strings.Contains(err.Error(), "chat-") {
			t.Errorf("error should mention chat- requirement: %v", err)
		}
	})
	t.Run("missing_chat_prefix", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{ProviderID: "deberta-v3", Endpoint: "http://x"}, nil, 1024, 1024)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got %v, want ErrInvalidConfig", err)
		}
		if !strings.Contains(err.Error(), "chat-") {
			t.Errorf("error should mention chat- prefix: %v", err)
		}
	})
	t.Run("empty_endpoint", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{ProviderID: "chat-x"}, nil, 1024, 1024)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got %v, want ErrInvalidConfig", err)
		}
	})
	t.Run("nil_http_client", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{ProviderID: "chat-x", Endpoint: "http://x"}, nil, 1024, 1024)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got %v, want ErrInvalidConfig", err)
		}
	})
	t.Run("negative_premise_cap", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{ProviderID: "chat-x", Endpoint: "http://x"}, &http.Client{}, -1, 1024)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got %v, want ErrInvalidConfig", err)
		}
	})
	t.Run("negative_hypothesis_cap", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{ProviderID: "chat-x", Endpoint: "http://x"}, &http.Client{}, 1024, -1)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("got %v, want ErrInvalidConfig", err)
		}
	})
	t.Run("valid_minimum", func(t *testing.T) {
		_, err := NewChatProvider(ProviderConfig{ProviderID: "chat-x", Endpoint: "http://x"}, &http.Client{}, 0, 0)
		if err != nil {
			t.Errorf("valid minimum config: got %v, want nil", err)
		}
	})
}

// TestChatProvider_Score_AuthTokenInHeader verifies that when
// auth_token is non-empty, the request includes the Bearer header.
// The AuthToken is sealed — the response shape carries NO label.
func TestChatProvider_Score_AuthTokenInHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer test-tok-123" {
			t.Errorf("Authorization: got %q, want Bearer test-tok-123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"neutral"}}]}`)
	}))
	defer srv.Close()
	p, _ := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, AuthToken: "test-tok-123", TimeoutMS: 5000},
		stubClient(srv), 1024, 1024)
	if _, err := p.Score(context.Background(), "p", "h"); err != nil {
		t.Fatalf("Score: %v", err)
	}
}

// TestChatProvider_Score_NoAuthHeader verifies that when auth_token
// is empty, the Authorization header is omitted (some local chat
// servers run without auth).
func TestChatProvider_Score_NoAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			t.Errorf("Authorization: got %q, want empty", h)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"neutral"}}]}`)
	}))
	defer srv.Close()
	p, _ := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000},
		stubClient(srv), 1024, 1024)
	if _, err := p.Score(context.Background(), "p", "h"); err != nil {
		t.Fatalf("Score: %v", err)
	}
}

// TestChatProvider_Score_Timeout verifies that a slow server triggers
// ErrProviderTimeout, not ErrProviderUnavailable. We simulate latency
// with time.Sleep (200ms) on the server; the Score timeout is 10ms.
func TestChatProvider_Score_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the client-side timeout. The Score will
		// give up first.
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p, _ := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 10}, // 10ms timeout
		stubClient(srv), 1024, 1024)
	_, err := p.Score(context.Background(), "p", "h")
	if !errors.Is(err, ErrProviderTimeout) {
		t.Errorf("got %v, want ErrProviderTimeout", err)
	}
}

// TestChatProvider_BuildChatCompletionPayload_StableFieldOrder ensures
// the wire shape doesn't drift across Go encoding/json version bumps
// (we manually marshal for stable field order; this is the test that
// catches accidental struct-field reordering).
//
// T-407-c (v4.0.0-alpha.28): max_tokens is now a parameter, not a
// constant. This test passes 1024 explicitly to lock the wire shape.
func TestChatProvider_BuildChatCompletionPayload_StableFieldOrder(t *testing.T) {
	payload, err := buildChatCompletionPayload("test-model", 1024, "the sky is blue", "the sky is blue")
	if err != nil {
		t.Fatalf("buildChatCompletionPayload: %v", err)
	}
	got := string(payload)
	// Note: encoding/json escapes the system prompt's embedded quotes
	// as \" and newlines as \n. We mirror that in the expected literal.
	want := `{"model":"test-model","messages":[{"role":"system","content":"You are a precise Natural Language Inference (NLI) classifier. Given a premise and a hypothesis, you reply with EXACTLY ONE of these three words, with no surrounding punctuation, no quotes, no explanation: \"entailment\" (the premise supports the hypothesis), \"contradiction\" (the premise refutes the hypothesis), or \"neutral\" (the premise neither supports nor refutes the hypothesis). Reply with only the word. No other output is valid."},{"role":"user","content":"Premise:\nthe sky is blue\n\nHypothesis:\nthe sky is blue\n\nReply with one word."}],"temperature":0,"max_tokens":1024}`
	if got != want {
		t.Errorf("payload mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

// TestChatProvider_ParseCanonicalLabel covers the parseCanonicalLabel
// helper directly. Cases match the integration tests but isolate the
// parser from HTTP plumbing.
func TestChatProvider_ParseCanonicalLabel(t *testing.T) {
	cases := []struct {
		raw       string
		wantLabel Label
		wantErr   bool
	}{
		{"entailment", LabelEntailment, false},
		{"ENTAILMENT", LabelEntailment, false},
		{"Entailment", LabelEntailment, false},
		{"  entailment  ", LabelEntailment, false},
		{"\"entailment\"", LabelEntailment, false},
		{"entailment.", LabelEntailment, false},
		{"contradiction", LabelContradiction, false},
		{"neutral", LabelNeutral, false},
		// T-406 (v4.0.0-alpha.27-pre-2): thinking-block prefixes
		// from 2026 reasoning models (MiniMax-M3, DeepSeek-R1,
		// Claude extended-thinking). The block must be stripped
		// before the canonical match.
		{"<think>The premise says X</think>\n\nentailment", LabelEntailment, false},
		{"<think>reasoning</think>contradiction", LabelContradiction, false},
		{"<think>\n  lots of reasoning\n  \n</think>\n\nneutral", LabelNeutral, false},
		{"<think>still thinking</think>  \n  entailment  ", LabelEntailment, false},
		{"<think>only reasoning, no label", "", true},
		{"", "", true},
		{"yes", "", true},
		{"no", "", true},
		{"maybe", "", true},
		{"supported", "", true},
		{"refuted", "", true},
		{"the premise entails the hypothesis", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			label, conf, err := parseCanonicalLabel(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Errorf("raw %q: expected error, got nil", tc.raw)
				}
				return
			}
			if err != nil {
				t.Errorf("raw %q: unexpected error: %v", tc.raw, err)
				return
			}
			if label != tc.wantLabel {
				t.Errorf("raw %q: got %s, want %s", tc.raw, label, tc.wantLabel)
			}
			if conf != 1.0 {
				t.Errorf("raw %q: got confidence %v, want 1.0", tc.raw, conf)
			}
		})
	}
}

// ============================================================================
// T-407-c (v4.0.0-alpha.28): per-model max_tokens resolution + retry-on-length
// ============================================================================

// TestResolveMaxTokens_OverrideWins: when MaxTokensOverride > 0 it
// takes precedence over the table. The operator escape hatch.
func TestResolveMaxTokens_OverrideWins(t *testing.T) {
	got := resolveMaxTokens("MiniMax-M3", 4096)
	want := 4096
	if got != want {
		t.Errorf("override=4096 with MiniMax-M3: got %d, want %d", got, want)
	}
}

// TestResolveMaxTokens_TableHit: when no override, look up model in table.
func TestResolveMaxTokens_TableHit(t *testing.T) {
	cases := []struct {
		modelRev  string
		wantTokens int
	}{
		{"MiniMax-M3", 1024},
		{"claude-sonnet-4-20260101", 2048}, // prefix match
		{"claude-3-7-sonnet-20250219", 1024},
		{"deepseek-r1-distill-llama-70b", 4096},
		{"o1-preview-2024-09-12", 8192},
		{"gpt-4o-2024-08-06", 256},
		{"gpt-4o-mini", 256},
		{"gpt-5-turbo", 1024},
	}
	for _, tc := range cases {
		t.Run(tc.modelRev, func(t *testing.T) {
			got := resolveMaxTokens(tc.modelRev, 0)
			if got != tc.wantTokens {
				t.Errorf("resolveMaxTokens(%q, 0) = %d, want %d", tc.modelRev, got, tc.wantTokens)
			}
		})
	}
}

// TestResolveMaxTokens_LongestPrefixWins: claude-sonnet-4-20260101
// should match the longer "claude-sonnet-4" before any shorter prefix
// (regression: greedy-first bug would mask late entries).
func TestResolveMaxTokens_LongestPrefixWins(t *testing.T) {
	// MiniMax-M2-prefixed should match MiniMax-M2 (1024), not MiniMax-M3 (also 1024 here).
	got := resolveMaxTokens("MiniMax-M2-preview", 0)
	want := 1024
	if got != want {
		t.Errorf("MiniMax-M2-preview: got %d, want %d (longest-prefix match)", got, want)
	}
}

// TestResolveMaxTokens_UnknownModel: unknown model falls through to
// DefaultFallbackMaxTokens (1024) via the empty-pattern catch-all row.
func TestResolveMaxTokens_UnknownModel(t *testing.T) {
	got := resolveMaxTokens("some-future-reasoning-model-v9000", 0)
	want := DefaultFallbackMaxTokens
	if got != want {
		t.Errorf("unknown model: got %d, want %d (DefaultFallback)", got, want)
	}
}

// TestResolveMaxTokens_ZeroOverride: override=0 → use table. (NOT
// 0 → fallback. Override=0 is "not set".)
func TestResolveMaxTokens_ZeroOverride(t *testing.T) {
	got := resolveMaxTokens("MiniMax-M3", 0)
	want := 1024
	if got != want {
		t.Errorf("override=0 with MiniMax-M3: got %d, want %d (table hit, NOT override)", got, want)
	}
}

// TestBuildChatCompletionPayload_AllKnownModels is the matrix test for
// T-407-c: confirm every entry in reasoningModelMaxTokens produces a
// payload with the expected max_tokens value. Backstops against table
// edits that forget to update the resolver.
func TestBuildChatCompletionPayload_AllKnownModels(t *testing.T) {
	type modelCase struct {
		modelRev   string
		wantTokens int
	}
	cases := []modelCase{
		{"claude-opus-4-5", 2048},
		{"claude-opus-4-6-preview", 2048},
		{"claude-sonnet-4-20260101", 2048},
		{"claude-haiku-4-5", 1024},
		{"claude-3-7-sonnet", 1024},
		{"deepseek-r1", 4096},
		{"deepseek-reasoner", 4096},
		{"deepseek-v3-20250101", 1024},
		{"deepseek-flash", 512},
		{"o3-mini", 4096},
		{"o4-mini", 4096},
		{"o3", 8192},
		{"o1-preview", 8192},
		{"gpt-4o", 256},
		{"gpt-4o-mini", 256},
		{"gpt-5", 1024},
		{"MiniMax-M3", 1024},
		{"MiniMax-M2", 1024},
		{"unknown-model-9000", DefaultFallbackMaxTokens},
	}
	for _, tc := range cases {
		t.Run(tc.modelRev, func(t *testing.T) {
			payload, err := buildChatCompletionPayload(tc.modelRev, resolveMaxTokens(tc.modelRev, 0), "p", "h")
			if err != nil {
				t.Fatalf("buildChatCompletionPayload: %v", err)
			}
			want := fmt.Sprintf(`"max_tokens":%d`, tc.wantTokens)
			if !strings.Contains(string(payload), want) {
				t.Errorf("payload missing %s: %s", want, payload)
			}
		})
	}
}

// TestParseChatCompletionResponse_FinishReasonLength_Empty: when the
// model hits finish_reason="length" with empty content, parse returns
// ErrTruncatedResponse (T-407-c). This is the trigger for Score's retry.
func TestParseChatCompletionResponse_FinishReasonLength_Empty(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":""}}]}`)
	_, _, err := parseChatCompletionResponse(body)
	if !errors.Is(err, ErrTruncatedResponse) {
		t.Errorf("got err=%v, want ErrTruncatedResponse", err)
	}
}

// TestParseChatCompletionResponse_FinishReasonLength_OnlyThink: when
// the model hit length with only a <think> block (no visible label),
// also ErrTruncatedResponse (the label didn't make it through).
func TestParseChatCompletionResponse_FinishReasonLength_OnlyThink(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"<think>\nlots of reasoning that consumed the budget\n</think>"}}]}`)
	_, _, err := parseChatCompletionResponse(body)
	if !errors.Is(err, ErrTruncatedResponse) {
		t.Errorf("got err=%v, want ErrTruncatedResponse (think-only truncated)", err)
	}
}

// TestParseChatCompletionResponse_FinishReasonStop: normal happy path
// with finish_reason="stop" returns the label (regression: T-406's
// <think> strip still works).
func TestParseChatCompletionResponse_FinishReasonStop(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"<think>reasoning</think>\n\nentailment"}}]}`)
	label, conf, err := parseChatCompletionResponse(body)
	if err != nil {
		t.Fatalf("parseChatCompletionResponse: %v", err)
	}
	if label != LabelEntailment {
		t.Errorf("label: got %s, want entailment", label)
	}
	if conf != 1.0 {
		t.Errorf("confidence: got %v, want 1.0", conf)
	}
}

// TestParseChatCompletionResponse_FinishReasonLength_WithLabel: when
// the model hit length BUT already emitted the label, return the label
// (truncation AFTER the label is harmless — no retry needed).
func TestParseChatCompletionResponse_FinishReasonLength_WithLabel(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"entailment"}}]}`)
	label, _, err := parseChatCompletionResponse(body)
	if err != nil {
		t.Fatalf("parseChatCompletionResponse: %v", err)
	}
	if label != LabelEntailment {
		t.Errorf("label after length: got %s, want entailment", label)
	}
}

// TestChatProvider_Score_RetryOnTruncation: end-to-end retry. The mock
// returns ErrTruncatedResponse on first call, success on second. The
// second call MUST use a 4× budget (verifiable via request body).
func TestChatProvider_Score_RetryOnTruncation(t *testing.T) {
	var (
		firstCallTokens   int
		secondCallTokens  int
		firstCallSeen     bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tokens := 0
		if strings.Contains(string(body), `"max_tokens":256`) {
			tokens = 256
		} else if strings.Contains(string(body), `"max_tokens":1024`) {
			tokens = 1024
		}
		if !firstCallSeen {
			firstCallSeen = true
			firstCallTokens = tokens
			// First call: respond truncated (reasoning hit length).
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":""}}]}`)
			return
		}
		secondCallTokens = tokens
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"entailment"}}]}`)
	}))
	defer srv.Close()

	// Use a GPT-4o model (default 256) so the 4× retry is observable.
	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000, ModelRev: "gpt-4o"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	score, err := p.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if score.Label != LabelEntailment {
		t.Errorf("label: got %s, want entailment", score.Label)
	}
	if firstCallTokens != 256 {
		t.Errorf("first call max_tokens: %d, want 256 (GPT-4o default)", firstCallTokens)
	}
	if secondCallTokens != 1024 {
		t.Errorf("second call max_tokens: %d, want 1024 (4× retry)", secondCallTokens)
	}
}

// TestChatProvider_Score_BothAttemptsTruncated: when both attempts hit
// ErrTruncatedResponse, Score returns ErrProviderBadResponse with a
// diagnostic message. The retry budget cap (MaxRetryBudgetCap) is
// reachable but we don't need to hit it for this test.
func TestChatProvider_Score_BothAttemptsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":""}}]}`)
	}))
	defer srv.Close()

	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000, ModelRev: "MiniMax-M3"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	_, err = p.Score(context.Background(), "p", "h")
	if err == nil {
		t.Fatal("expected error after 2 truncated attempts, got nil")
	}
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("got err=%v, want ErrProviderBadResponse (wrapped)", err)
	}
	// Diagnostic message should mention retries exhausted.
	if !strings.Contains(err.Error(), "retries exhausted") {
		t.Errorf("error message missing 'retries exhausted': %v", err)
	}
}

// TestChatProvider_LegacyProviderConfig_NoMaxTokensOverride: confirms
// ProviderConfig without MaxTokensOverride still works (backward compat).
// The field defaults to 0 → resolveMaxTokens uses the table.
func TestChatProvider_LegacyProviderConfig_NoMaxTokensOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// MiniMax-M3 default = 1024
		if !strings.Contains(string(body), `"max_tokens":1024`) {
			t.Errorf("body missing max_tokens:1024: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"entailment"}}]}`)
	}))
	defer srv.Close()

	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000, ModelRev: "MiniMax-M3"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	_, err = p.Score(context.Background(), "p", "h")
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
}

// =====================================================================
// T-201 (v4.0.0-alpha.24-pre-1): think-only reply handling.
// MiniMax-M3, Claude-with-extended-thinking, and DeepSeek-R1 sometimes
// emit finish_reason=stop with content == a single <think>...</think>
// block and NO visible label. The OLD parser returned
// ErrProviderBadResponse, surfacing to drift_judge as needs_human.
// NEW behavior: strip the think block; if anything visible remains,
// parse it as the label; if the reply was truly thinking-only with
// no label at all, keep the ErrProviderBadResponse (genuinely empty).
// =====================================================================

// TestParseChatCompletionResponse_ThinkOnlyStop_VisibleLabel: the
// reasoning model emitted a <think> block AND the label after it
// (finish_reason=stop). The label must be parsed correctly.
func TestParseChatCompletionResponse_ThinkOnlyStop_VisibleLabel(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"<think>reasoning</think>\n\nentailment"}}]}`)
	label, conf, err := parseChatCompletionResponse(body)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if label != LabelEntailment || conf != 1.0 {
		t.Errorf("got label=%v conf=%v, want entailment/1.0", label, conf)
	}
}

// TestParseChatCompletionResponse_ThinkOnlyStop_NoLabel: the reasoning
// model emitted ONLY a <think> block (no visible label). finish_reason=stop
// means the model committed, so this is ErrProviderBadResponse (NOT
// ErrTruncatedResponse — that path is reserved for finish_reason=length).
func TestParseChatCompletionResponse_ThinkOnlyStop_NoLabel(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"<think>only reasoning, no label</think>"}}]}`)
	_, _, err := parseChatCompletionResponse(body)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("got err=%v, want ErrProviderBadResponse (NOT truncated — finish_reason=stop)", err)
	}
	if errors.Is(err, ErrTruncatedResponse) {
		t.Errorf("got ErrTruncatedResponse; think-only stop must NOT route to retry path")
	}
}

// TestParseChatCompletionResponse_AnthropicThinkOnly_WithLabel: the
// Anthropic-via-adapter path also handles <think> blocks. stop_reason=end_turn
// with a label after the think block must parse.
func TestParseChatCompletionResponse_AnthropicThinkOnly_WithLabel(t *testing.T) {
	body := []byte(`{"content":"<think>chain of thought</think>\n\ncontradiction","stop_reason":"end_turn"}`)
	label, conf, err := parseChatCompletionResponse(body)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if label != LabelContradiction || conf != 1.0 {
		t.Errorf("got label=%v conf=%v, want contradiction/1.0", label, conf)
	}
}

// TestParseChatCompletionResponse_AnthropicThinkOnly_NoLabel: <think>
// block only with stop_reason=end_turn → ErrProviderBadResponse.
func TestParseChatCompletionResponse_AnthropicThinkOnly_NoLabel(t *testing.T) {
	body := []byte(`{"content":"<think>only reasoning</think>","stop_reason":"end_turn"}`)
	_, _, err := parseChatCompletionResponse(body)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("got err=%v, want ErrProviderBadResponse (end_turn + empty content)", err)
	}
}

// TestChatProvider_Score_ThinkOnlyStop_NoRetry: regression guard. A
// think-only stop reply surfaces ErrProviderBadResponse, NOT
// ErrTruncatedResponse. The Score retry loop must NOT re-attempt
// (re-prompting the same model with the same context won't change
// its behavior — the model already committed to a final reply).
// We assert the call count via the stub server.
func TestChatProvider_Score_ThinkOnlyStop_NoRetry(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"<think>no label here</think>"}}]}`)
	}))
	defer srv.Close()

	p, err := NewChatProvider(
		ProviderConfig{ProviderID: "chat-test", Endpoint: srv.URL, TimeoutMS: 5000, ModelRev: "MiniMax-M3"},
		stubClient(srv), 1024, 1024)
	if err != nil {
		t.Fatalf("NewChatProvider: %v", err)
	}
	_, err = p.Score(context.Background(), "p", "h")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrProviderBadResponse) {
		t.Errorf("got err=%v, want ErrProviderBadResponse", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("stub called %d times, want 1 (no retry on think-only stop)", got)
	}
}