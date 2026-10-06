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
				if !strings.Contains(string(body), `"max_tokens":8`) {
					t.Errorf("body missing max_tokens:8: %s", body)
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
func TestChatProvider_BuildChatCompletionPayload_StableFieldOrder(t *testing.T) {
	payload, err := buildChatCompletionPayload("test-model", "the sky is blue", "the sky is blue")
	if err != nil {
		t.Fatalf("buildChatCompletionPayload: %v", err)
	}
	got := string(payload)
	// Note: encoding/json escapes the system prompt's embedded quotes
	// as \" and newlines as \n. We mirror that in the expected literal.
	want := `{"model":"test-model","messages":[{"role":"system","content":"You are a precise Natural Language Inference (NLI) classifier. Given a premise and a hypothesis, you reply with EXACTLY ONE of these three words, with no surrounding punctuation, no quotes, no explanation: \"entailment\" (the premise supports the hypothesis), \"contradiction\" (the premise refutes the hypothesis), or \"neutral\" (the premise neither supports nor refutes the hypothesis). Reply with only the word. No other output is valid."},{"role":"user","content":"Premise:\nthe sky is blue\n\nHypothesis:\nthe sky is blue\n\nReply with one word."}],"temperature":0,"max_tokens":8}`
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