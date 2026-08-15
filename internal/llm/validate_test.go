package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestValidateProvider_OpenAIDialect_200 verifies the OpenAI-compat
// probe path + Bearer header + valid state.
func TestValidateProvider_OpenAIDialect_200(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := &ProviderSpec{ID: "deepseek", BaseURL: srv.URL, ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer}
	res := ValidateProvider(context.Background(), spec, "sk-test")
	if res.State != ProbeValid {
		t.Fatalf("State = %q, want valid (err=%s)", res.State, res.Error)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", gotAuth)
	}
}

// TestValidateProvider_AnthropicDialect_200 verifies the Anthropic
// probe path + x-api-key header.
func TestValidateProvider_AnthropicDialect_200(t *testing.T) {
	var gotPath, gotKey, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := &ProviderSpec{ID: "anthropic", BaseURL: srv.URL, ProbePath: "/v1/models", ProbeAuthMode: ProbeAuthXAPIKey}
	res := ValidateProvider(context.Background(), spec, "sk-ant")
	if res.State != ProbeValid {
		t.Fatalf("State = %q, want valid (err=%s)", res.State, res.Error)
	}
	if gotPath != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", gotPath)
	}
	if gotKey != "sk-ant" {
		t.Errorf("x-api-key = %q, want sk-ant", gotKey)
	}
	if gotVer == "" {
		t.Error("anthropic-version header missing")
	}
}

// TestValidateProvider_ErrorMatrix covers the full classification.
func TestValidateProvider_ErrorMatrix(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantState  ProbeState
		wantClass  FailClass
		wantErrSub string
	}{
		{"401", http.StatusUnauthorized, ProbeAuthError, FailAuth, "401"},
		{"403", http.StatusForbidden, ProbeAuthError, FailAuth, "403"},
		{"429", http.StatusTooManyRequests, ProbeRateLimited, FailRate, "429"},
		{"408", http.StatusRequestTimeout, ProbeUnreachable, FailTimeout, "408"},
		{"500", http.StatusInternalServerError, ProbeUnreachable, FailServer, "500"},
		{"404", http.StatusNotFound, ProbeUnknown, "", "404"},
		{"200", http.StatusOK, ProbeValid, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
			}))
			defer srv.Close()
			spec := &ProviderSpec{ID: "openai", BaseURL: srv.URL, ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer}
			res := ValidateProvider(context.Background(), spec, "k")
			if res.State != c.wantState {
				t.Errorf("State = %q, want %q (err=%s)", res.State, c.wantState, res.Error)
			}
			if c.wantClass != "" && res.Class != c.wantClass {
				t.Errorf("Class = %q, want %q", res.Class, c.wantClass)
			}
			if c.wantErrSub != "" && !contains(res.Error, c.wantErrSub) {
				t.Errorf("Error = %q, want substring %q", res.Error, c.wantErrSub)
			}
		})
	}
}

// TestValidateProvider_Timeout verifies a slow endpoint → unreachable
// with class timeout. The probe client has its own 5s timeout, so we
// bound the caller ctx to 100ms to keep the test fast. The handler
// sleeps longer than the client budget so the client (not the server)
// wins the race.
func TestValidateProvider_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // > ctx deadline (100ms), << probe client timeout (5s)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := &ProviderSpec{ID: "deepseek", BaseURL: srv.URL, ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := ValidateProvider(ctx, spec, "k")
	if res.State != ProbeUnreachable {
		t.Fatalf("State = %q, want unreachable (err=%s)", res.State, res.Error)
	}
	if res.Class != FailTimeout {
		t.Errorf("Class = %q, want timeout", res.Class)
	}
}

// TestValidateProvider_EmptyKey fails fast without HTTP.
func TestValidateProvider_EmptyKey(t *testing.T) {
	spec := &ProviderSpec{ID: "openai", BaseURL: "https://example.com", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer}
	res := ValidateProvider(context.Background(), spec, "")
	if res.State != ProbeUnreachable {
		t.Fatalf("State = %q, want unreachable", res.State)
	}
}

// TestValidateProviderWithBalance verifies the balance bonus endpoint
// is hit and doesn't flip a valid verdict.
func TestValidateProviderWithBalance(t *testing.T) {
	var balanceHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/balance" {
			balanceHits++
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spec := &ProviderSpec{ID: "deepseek", BaseURL: srv.URL, ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer, BalancePath: "/user/balance"}
	res := ValidateProviderWithBalance(context.Background(), spec, "sk-test")
	if res.State != ProbeValid {
		t.Fatalf("State = %q, want valid", res.State)
	}
	if balanceHits != 1 {
		t.Errorf("balance probe hits = %d, want 1", balanceHits)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// keep fmt imported for build when error text changes.
var _ = fmt.Sprintf
