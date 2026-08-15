package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
)

// fakeJudge is a deterministic JudgeClient for failover tests.
type fakeJudge struct {
	name       string
	failCount  int // number of calls that return an error before succeeding
	errcode    int // HTTP-like status for the error
	calls      int
	lastResp   *JudgeResponse
}

func (f *fakeJudge) Name() string { return f.name }

func (f *fakeJudge) Judge(ctx context.Context, req JudgeRequest) (*JudgeResponse, error) {
	f.calls++
	if f.calls <= f.failCount {
		return nil, &testStatusError{status: f.errcode}
	}
	f.lastResp = &JudgeResponse{VerdictJSON: `{"verdict":"aligned"}`, Confidence: 0.9, Model: "test", Provider: f.name}
	return f.lastResp, nil
}

// testStatusError mimics orchestration's judgeHTTPStatusError shape.
type testStatusError struct{ status int }

func (e *testStatusError) Error() string { return fmt.Sprintf("judge: HTTP %d", e.status) }

// failoverTestSetup builds a FailoverClient with two fake providers.
func failoverTestSetup(t *testing.T, pin string, health *HealthRegistry, firstFail, secondFail int, firstCode, secondCode int) *FailoverClient {
	t.Helper()
	ks := llmkeystore.NewMemoryStore()
	_ = ks.Set("deepseek", "sk-ds")
	_ = ks.Set("openai", "sk-oa")

	specs := []*ProviderSpec{
		{ID: "deepseek", BaseURL: "https://api.deepseek.com", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer, DefaultModel: "deepseek-v4-flash"},
		{ID: "openai", BaseURL: "https://api.openai.com/v1", ProbePath: "/models", ProbeAuthMode: ProbeAuthBearer, DefaultModel: "gpt-5"},
	}
	factory := func(spec *ProviderSpec, key string) (JudgeClient, error) {
		switch spec.ID {
		case "deepseek":
			return &fakeJudge{name: "self_harness_deepseek", failCount: firstFail, errcode: firstCode}, nil
		case "openai":
			return &fakeJudge{name: "self_harness_openai", failCount: secondFail, errcode: secondCode}, nil
		}
		return nil, fmt.Errorf("unknown spec %s", spec.ID)
	}
	fc, err := NewFailoverClient(FailoverOptions{
		Specs:   specs,
		KS:      ks,
		Health:  health,
		Pin:     pin,
		Factory: factory,
		// Map 401 → FailAuth exactly like orchestration's
		// classifyJudgeError (the production wiring).
		Classify: func(err error) FailClass {
			var se *testStatusError
			if errors.As(err, &se) {
				if se.status == 401 || se.status == 403 {
					return FailAuth
				}
				if se.status == 429 {
					return FailRate
				}
				if se.status == 408 {
					return FailTimeout
				}
				if se.status >= 500 {
					return FailServer
				}
			}
			return ""
		},
		Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewFailoverClient: %v", err)
	}
	return fc
}

// TestFailoverClient_FirstProviderWins verifies the happy path.
func TestFailoverClient_FirstProviderWins(t *testing.T) {
	fc := failoverTestSetup(t, "", nil, 0, 0, 0, 0)
	resp, err := fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if resp.Provider != "self_harness_deepseek" {
		t.Errorf("Provider = %q, want self_harness_deepseek", resp.Provider)
	}
	if fc.LastProviderID() != "deepseek" {
		t.Errorf("LastProviderID = %q, want deepseek", fc.LastProviderID())
	}
}

// TestFailoverClient_FailoverToSecond verifies the core behavior: the
// first provider fails, the second answers.
func TestFailoverClient_FailoverToSecond(t *testing.T) {
	// deepseek fails 1st call (401), openai succeeds.
	fc := failoverTestSetup(t, "", nil, 1, 0, 401, 0)
	resp, err := fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if resp.Provider != "self_harness_openai" {
		t.Errorf("Provider = %q, want self_harness_openai (failover)", resp.Provider)
	}
	if fc.LastProviderID() != "openai" {
		t.Errorf("LastProviderID = %q, want openai", fc.LastProviderID())
	}
}

// TestFailoverClient_PinMovesToFront verifies DARK_JUDGE_PROVIDER
// preference: the pinned provider goes first even though it is second
// in the catalog.
func TestFailoverClient_PinMovesToFront(t *testing.T) {
	fc := failoverTestSetup(t, "openai", nil, 0, 0, 0, 0)
	resp, err := fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if resp.Provider != "self_harness_openai" {
		t.Errorf("Provider = %q, want self_harness_openai (pinned)", resp.Provider)
	}
}

// TestFailoverClient_CooldownSkipsProvider verifies a provider in
// cooldown is skipped even if it is first.
func TestFailoverClient_CooldownSkipsProvider(t *testing.T) {
	health := NewHealthRegistry(HealthOptions{})
	health.RecordFailure("deepseek", FailAuth) // deepseek in cooldown
	fc := failoverTestSetup(t, "", health, 0, 0, 0, 0)
	resp, err := fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if resp.Provider != "self_harness_openai" {
		t.Errorf("Provider = %q, want self_harness_openai (deepseek in cooldown)", resp.Provider)
	}
}

// TestFailoverClient_RecordsFailuresToHealth verifies judge failures
// are recorded into the health registry (shared counters — LiteLLM).
func TestFailoverClient_RecordsFailuresToHealth(t *testing.T) {
	health := NewHealthRegistry(HealthOptions{})
	fc := failoverTestSetup(t, "", health, 1, 0, 401, 0)
	_, err := fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	// deepseek failed with 401 → cooldown (policy auth→0).
	if !health.InCooldown("deepseek") {
		t.Error("deepseek not in cooldown after 401 judge failure")
	}
	// openai succeeded → healthy.
	if health.InCooldown("openai") {
		t.Error("openai in cooldown after success")
	}
}

// TestFailoverClient_AllFail verifies the aggregated error.
func TestFailoverClient_AllFail(t *testing.T) {
	fc := failoverTestSetup(t, "", nil, 1, 1, 401, 500)
	_, err := fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !errors.Is(err, ErrNoLLMAvailable) {
		t.Errorf("err should wrap ErrNoLLMAvailable, got %v", err)
	}
}

// TestFailoverClient_NoKeys verifies ErrNoLLMAvailable when nothing
// has a key.
func TestFailoverClient_NoKeys(t *testing.T) {
	ks := llmkeystore.NewMemoryStore()
	factory := func(spec *ProviderSpec, key string) (JudgeClient, error) {
		return &fakeJudge{name: "x"}, nil
	}
	fc, err := NewFailoverClient(FailoverOptions{Specs: []*ProviderSpec{{ID: "deepseek"}}, KS: ks, Factory: factory, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("NewFailoverClient: %v", err)
	}
	_, err = fc.Judge(context.Background(), JudgeRequest{EvalType: "drift_judge", Content: "x"})
	if !errors.Is(err, ErrNoLLMAvailable) {
		t.Fatalf("err = %v, want ErrNoLLMAvailable", err)
	}
}

// TestFailoverClient_Status reports key source + cooldown.
func TestFailoverClient_Status(t *testing.T) {
	health := NewHealthRegistry(HealthOptions{})
	health.RecordFailure("deepseek", FailAuth)
	fc := failoverTestSetup(t, "deepseek", health, 0, 0, 0, 0)
	st := fc.Status()
	if len(st) != 2 {
		t.Fatalf("Status len = %d, want 2", len(st))
	}
	if !st[0].Pinned {
		t.Errorf("deepseek should be pinned")
	}
	if !st[0].InCooldown {
		t.Errorf("deepseek should be in cooldown")
	}
	if st[1].KeySource != "memory" {
		t.Errorf("openai KeySource = %q, want memory", st[1].KeySource)
	}
}
