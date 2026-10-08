// Package tools — llm_bind_test.go
//
// Phase 14 T-303: tests for the LLM_BIND namespace (llm_provider_bind
// + llm_provider_probe). 12 tests cover:
//
//  1-7. TestLLMProviderBind_0N_*: bind tool
//    1. HappyPath — persists and returns (auth_token persisted)
//    2. AuthTokenNotEchoed — security contract
//    3. RejectsUnknownPrefix — sealed boundary
//    4. RejectsEmptyEndpoint
//    5. RequiresActiveProject — ErrSessionRequired
//    6. PreservesFallback — re-bind doesn't wipe existing fallback
//    7. ValidatesNLIConfig — invalid endpoint rejected
//
//  8-12. TestLLMProviderProbe_0N_*: probe tool
//    8. HappyPath — sends pong request, gets 200
//    9. ResolvesFromActiveProject — provider_id resolves endpoint+token
//    10. RejectsMutuallyExclusiveArgs
//    11. UnknownProviderID
//    12. ReportsFailure — 500 surfaces status + reason
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
)

// =====================================================================
// Test seam: stubLLMBindStore
// =====================================================================

// stubLLMBindStore is the test seam for llmBindStore. It records
// reads/writes and returns canned values.
type stubLLMBindStore struct {
	mu              sync.Mutex
	activeProjectID string
	projects        map[string]*project.Project
	getErr          error
	createErr       error
}

func newStubLLMBindStore() *stubLLMBindStore {
	return &stubLLMBindStore{
		activeProjectID: "default",
		projects:        make(map[string]*project.Project),
	}
}

func (s *stubLLMBindStore) ActiveProject() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeProjectID
}

// GetProjectRaw mirrors the sealed store accessor: the stub must hand back
// the credential, exactly as the real store does for these four call sites.
func (s *stubLLMBindStore) GetProjectRaw(ctx context.Context, id string) (*project.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.projects[id], nil
}

func (s *stubLLMBindStore) CreateProject(ctx context.Context, p *project.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	if p == nil {
		return errors.New("nil project")
	}
	s.projects[p.ProjectID] = p
	return nil
}

// bindTestSetup wires the stub into the package-level var and seeds
// a default project. Returns the stub for post-assertions.
func bindTestSetup(t *testing.T) *stubLLMBindStore {
	t.Helper()
	prev := llmBindStoreOverride
	llmBindStoreOverride = nil
	stub := newStubLLMBindStore()
	seedDefaultProject(stub)
	llmBindStoreOverride = stub
	t.Cleanup(func() { llmBindStoreOverride = prev })
	return stub
}

func seedDefaultProject(s *stubLLMBindStore) *project.Project {
	p := &project.Project{
		ProjectID:   "default",
		DisplayName: "Default",
	}
	s.projects[p.ProjectID] = p
	return p
}

// =====================================================================
// BIND TESTS (1-7)
// =====================================================================

// 1. Happy path: bind succeeds, returns result, persists to stub.
func TestLLMProviderBind_01_HappyPath(t *testing.T) {
	stub := bindTestSetup(t)
	in := LLMProviderBindInput{
		ProviderID: "judge-test-1",
		Endpoint:   "https://example.test/v1/chat/completions",
		AuthToken:  "secret-token",
		TimeoutMS:  10000,
		ModelRev:   "test-model",
	}
	resp, err := runLLMProviderBind(context.Background(), in)
	if err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}
	result, ok := resp.Data.(LLMProviderBindResult)
	if !ok {
		t.Fatalf("expected LLMProviderBindResult, got %T", resp.Data)
	}
	if result.ProjectID != "default" {
		t.Errorf("ProjectID: got %q, want default", result.ProjectID)
	}
	if result.ProviderID != "judge-test-1" {
		t.Errorf("ProviderID: got %q, want judge-test-1", result.ProviderID)
	}
	if result.Endpoint != "https://example.test/v1/chat/completions" {
		t.Errorf("Endpoint: got %q, want example.test", result.Endpoint)
	}
	if result.TimeoutMS != 10000 {
		t.Errorf("TimeoutMS: got %d, want 10000", result.TimeoutMS)
	}
	if !result.AuthPresent {
		t.Error("AuthPresent: got false, want true")
	}
	// Persisted to stub
	persisted, err := stub.GetProjectRaw(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if persisted.NLIConfig == nil {
		t.Fatal("NLIConfig is nil after bind")
	}
	if !persisted.NLIConfig.Enabled {
		t.Error("NLIConfig.Enabled: got false, want true")
	}
	if persisted.NLIConfig.Primary.ProviderID != "judge-test-1" {
		t.Errorf("Primary.ProviderID: got %q, want judge-test-1", persisted.NLIConfig.Primary.ProviderID)
	}
	if persisted.NLIConfig.Primary.AuthToken != "secret-token" {
		t.Errorf("AuthToken: got %q, want secret-token (persisted)", persisted.NLIConfig.Primary.AuthToken)
	}
}

// 2. Auth token is NEVER echoed in the result.
func TestLLMProviderBind_02_AuthTokenNotEchoed(t *testing.T) {
	bindTestSetup(t)
	in := LLMProviderBindInput{
		ProviderID: "judge-test-2",
		Endpoint:   "https://example.test/v1/chat/completions",
		AuthToken:  "very-secret-token-DO-NOT-LEAK",
	}
	resp, err := runLLMProviderBind(context.Background(), in)
	if err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}
	raw, _ := json.Marshal(resp.Data)
	if strings.Contains(string(raw), "very-secret-token-DO-NOT-LEAK") {
		t.Fatalf("SECURITY: auth token leaked in response: %s", raw)
	}
	if !strings.Contains(string(raw), `"auth_present":true`) {
		t.Errorf("auth_present: missing or false; raw=%s", raw)
	}
}

// 3. Reject unknown prefix (sealed boundary).
func TestLLMProviderBind_03_RejectsUnknownPrefix(t *testing.T) {
	bindTestSetup(t)
	in := LLMProviderBindInput{
		ProviderID: "gpt-4-turbo", // NOT judge- or chat-
		Endpoint:   "https://example.test/v1/chat/completions",
	}
	_, err := runLLMProviderBind(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for unknown prefix")
	}
	if !strings.Contains(err.Error(), "must start with") {
		t.Errorf("error: got %q, want substring 'must start with'", err.Error())
	}
}

// 4. Reject empty endpoint.
func TestLLMProviderBind_04_RejectsEmptyEndpoint(t *testing.T) {
	bindTestSetup(t)
	in := LLMProviderBindInput{
		ProviderID: "judge-test",
		Endpoint:   "",
	}
	_, err := runLLMProviderBind(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

// 5. Reject missing active project.
func TestLLMProviderBind_05_RequiresActiveProject(t *testing.T) {
	prev := llmBindStoreOverride
	llmBindStoreOverride = nil
	stub := newStubLLMBindStore()
	stub.activeProjectID = "" // no active project
	llmBindStoreOverride = stub
	t.Cleanup(func() { llmBindStoreOverride = prev })

	in := LLMProviderBindInput{
		ProviderID: "judge-test",
		Endpoint:   "https://example.test/v1/chat/completions",
	}
	_, err := runLLMProviderBind(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for missing active project")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("error: got %q, want substring 'session'", err.Error())
	}
}

// 6. Bind preserves existing fallback binding.
func TestLLMProviderBind_06_PreservesFallback(t *testing.T) {
	stub := bindTestSetup(t)
	in1 := LLMProviderBindInput{
		ProviderID: "judge-primary",
		Endpoint:   "https://primary.test/v1/chat/completions",
	}
	if _, err := runLLMProviderBind(context.Background(), in1); err != nil {
		t.Fatalf("bind 1: %v", err)
	}
	// Manually set a fallback on the stub project.
	p, _ := stub.GetProjectRaw(context.Background(), "default")
	p.NLIConfig.Fallback = project.NLIPrimary{
		ProviderID: "chat-fallback",
		Endpoint:   "https://fallback.test/v1/chat/completions",
	}
	if err := stub.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("seed fallback: %v", err)
	}
	in2 := LLMProviderBindInput{
		ProviderID: "judge-new-primary",
		Endpoint:   "https://new-primary.test/v1/chat/completions",
	}
	if _, err := runLLMProviderBind(context.Background(), in2); err != nil {
		t.Fatalf("bind 2: %v", err)
	}
	got, _ := stub.GetProjectRaw(context.Background(), "default")
	if got.NLIConfig.Fallback.ProviderID != "chat-fallback" {
		t.Errorf("Fallback.ProviderID: got %q, want chat-fallback (preserved)", got.NLIConfig.Fallback.ProviderID)
	}
	if got.NLIConfig.Primary.ProviderID != "judge-new-primary" {
		t.Errorf("Primary.ProviderID: got %q, want judge-new-primary", got.NLIConfig.Primary.ProviderID)
	}
}

// 7. NLIConfig.Validate() runs — provider_id starting with valid
//    prefix but with an obviously bad shape (just whitespace) is rejected.
func TestLLMProviderBind_07_ValidatesNLIConfig(t *testing.T) {
	bindTestSetup(t)
	// Whitespace-only endpoint fails the non-empty check (sealed).
	in := LLMProviderBindInput{
		ProviderID: "judge-bad",
		Endpoint:   "   ",
	}
	_, err := runLLMProviderBind(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for whitespace-only endpoint")
	}
}

// =====================================================================
// PROBE TESTS (8-12)
// =====================================================================

// 8. Probe sends 'reply with pong' to a stub HTTP server and
//    reports OK with a body excerpt.
func TestLLMProviderProbe_01_HappyPath_SendsPong(t *testing.T) {
	prev := llmBindStoreOverride
	llmBindStoreOverride = nil
	t.Cleanup(func() { llmBindStoreOverride = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("probe body unmarshal: %v", err)
			w.WriteHeader(500)
			return
		}
		msgs, _ := payload["messages"].([]any)
		if len(msgs) != 2 {
			t.Errorf("probe messages: got %d, want 2", len(msgs))
			w.WriteHeader(500)
			return
		}
		sys, _ := msgs[0].(map[string]any)
		if sys["content"] != "reply with pong" {
			t.Errorf("system content: got %v, want 'reply with pong'", sys["content"])
			w.WriteHeader(500)
			return
		}
		usr, _ := msgs[1].(map[string]any)
		if usr["content"] != "ping" {
			t.Errorf("user content: got %v, want 'ping'", usr["content"])
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer srv.Close()

	result := probeLLMProvider(context.Background(), srv.URL, "test-token", 5000)
	if !result.OK {
		t.Errorf("OK: got false, reason=%q", result.Reason)
	}
	if result.StatusCode != 200 {
		t.Errorf("StatusCode: got %d, want 200", result.StatusCode)
	}
	if result.LatencyMS < 0 {
		t.Errorf("LatencyMS: got %d, want >= 0", result.LatencyMS)
	}
	if result.BodyExcerpt == "" {
		t.Error("BodyExcerpt is empty on success")
	}
}

// 9. Probe resolves endpoint+token from NLIConfig when provider_id given.
func TestLLMProviderProbe_02_ResolvesFromActiveProject(t *testing.T) {
	prev := llmBindStoreOverride
	llmBindStoreOverride = nil
	stub := newStubLLMBindStore()
	seedDefaultProject(stub)
	p, _ := stub.GetProjectRaw(context.Background(), "default")
	p.NLIConfig = &project.NLIConfig{
		Enabled: true,
		Primary: project.NLIPrimary{
			ProviderID: "judge-projected",
			Endpoint:   "https://example.test/v1/chat/completions",
			AuthToken:  "test-token",
			TimeoutMS:  10000,
		},
	}
	llmBindStoreOverride = stub
	t.Cleanup(func() { llmBindStoreOverride = prev })

	in := LLMProviderProbeInput{
		ProviderID: "judge-projected",
	}
	resp, err := runLLMProviderProbe(context.Background(), in)
	if err != nil {
		t.Fatalf("probe should not error from NLIConfig lookup alone, got: %v", err)
	}
	result, ok := resp.Data.(LLMProviderProbeResult)
	if !ok {
		t.Fatalf("expected LLMProviderProbeResult, got %T", resp.Data)
	}
	if result.OK {
		t.Logf("warning: probe unexpectedly succeeded (network may resolve)")
	}
}

// 10. Probe rejects mutually-exclusive args (provider_id + endpoint).
func TestLLMProviderProbe_03_RejectsMutuallyExclusiveArgs(t *testing.T) {
	bindTestSetup(t)
	in := LLMProviderProbeInput{
		ProviderID: "judge-x",
		Endpoint:   "https://other.test/v1/chat/completions",
	}
	_, err := runLLMProviderProbe(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for mutually-exclusive args")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error: got %q, want substring 'mutually exclusive'", err.Error())
	}
}

// 11. Probe rejects unknown provider_id (active project HAS an
//     NLIConfig but the given one isn't there).
func TestLLMProviderProbe_04_UnknownProviderID(t *testing.T) {
	prev := llmBindStoreOverride
	llmBindStoreOverride = nil
	stub := newStubLLMBindStore()
	seedDefaultProject(stub)
	// Seed an NLIConfig so the "no NLIConfig" branch is bypassed.
	p, _ := stub.GetProjectRaw(context.Background(), "default")
	p.NLIConfig = &project.NLIConfig{
		Enabled: true,
		Primary: project.NLIPrimary{
			ProviderID: "judge-known",
			Endpoint:   "https://example.test/v1/chat/completions",
			TimeoutMS:  5000,
		},
	}
	llmBindStoreOverride = stub
	t.Cleanup(func() { llmBindStoreOverride = prev })

	in := LLMProviderProbeInput{ProviderID: "judge-not-bound"}
	_, err := runLLMProviderProbe(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for unknown provider_id")
	}
	if !strings.Contains(err.Error(), "not bound") {
		t.Errorf("error: got %q, want substring 'not bound'", err.Error())
	}
}

// 12. Probe reports failure when HTTP server returns 500.
func TestLLMProviderProbe_05_ReportsFailure(t *testing.T) {
	prev := llmBindStoreOverride
	llmBindStoreOverride = nil
	t.Cleanup(func() { llmBindStoreOverride = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	result := probeLLMProvider(context.Background(), srv.URL, "", 5000)
	if result.OK {
		t.Error("OK: got true, want false")
	}
	if result.StatusCode != 500 {
		t.Errorf("StatusCode: got %d, want 500", result.StatusCode)
	}
	if !strings.Contains(result.Reason, "500") {
		t.Errorf("Reason: got %q, want substring '500'", result.Reason)
	}
}