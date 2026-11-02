// Package orchestration — llm_credential_test.go
//
// The credential never lives in the project config: the config holds the
// provider_id and the secret is resolved from the OS keyring at CALL
// time.
//
// These tests pin that contract:
//
//   - ensureLLMJudge resolves the credential from the keyring when the
//     stored binding carries none, and FAILS loudly when there is none;
//   - buildNLIPrimary (the "chat-*" NLI path) does the same;
//   - a legacy config token still wins (compatibility).
package orchestration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// judgeCredentialStore installs an in-memory key store for the test
// and returns it. The seam keeps the OS keyring (and the network) out
// of the unit test.
func judgeCredentialStore(t *testing.T, entries map[string]string) *llmkeystore.MemoryStore {
	t.Helper()
	mem := llmkeystore.NewMemoryStore()
	for id, key := range entries {
		if err := mem.Set(id, key); err != nil {
			t.Fatalf("seed keyring %q: %v", id, err)
		}
	}
	prev := llmCredentialKeyStore
	llmCredentialKeyStore = mem
	t.Cleanup(func() { llmCredentialKeyStore = prev })
	return mem
}

// judgeCaptureServer is an httptest server that records the
// Authorization header of the last request and answers with a valid
// verdict body.
func judgeCaptureServer(t *testing.T, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"aligned\",\"confidence\":0.9,\"reasoning\":\"matches spec\"}"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// judgeProjectWithConfig returns a project whose primary binding uses
// providerID and endpoint, with NO auth token in the config (the
// sealed posture: the secret lives in the keyring).
func judgeProjectWithConfig(providerID, endpoint string) *project.Project {
	return &project.Project{
		ProjectID:   "default",
		DisplayName: "default",
		NLIConfig: &project.NLIConfig{
			Enabled: true,
			Primary: project.NLIPrimary{
				ProviderID: providerID,
				Endpoint:   endpoint,
				// AuthToken deliberately EMPTY: the credential must be
				// resolved from the keyring at call time.
				TimeoutMS: 5000,
				ModelRev:  "MiniMax-M3",
			},
			LatencyBudgetMS:    10000,
			MaxPremiseBytes:    65536,
			MaxHypothesisBytes: 8192,
			MaxCacheEntries:    0,
			CacheTTLSeconds:    86400,
		},
	}
}

// judgeTestOrchestrator builds an orchestrator over a real sqlite
// store with the given project as the active one.
func judgeTestOrchestrator(t *testing.T, proj *project.Project) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         dir + "/judge.db",
		WALMode:     true,
		ForeignKeys: true,
	}
	st, err := testStoreOpen(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetActiveProject(context.Background(), proj.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(context.Background(), proj); err != nil {
		t.Fatal(err)
	}
	return newDriftJudgeTestOrchestratorWithStore(t, st)
}

// TestEnsureLLMJudge_ResolvesCredentialFromKeyring is the core test:
// the stored binding carries NO token, the keyring has one under the
// CATALOG id ("minimax"), and the judge must authenticate with it.
//
// The endpoint is a local httptest server (NOT a catalog candidate), so
// the credential-scoped endpoint resolution does not probe the
// network — and the Authorization header the server sees proves the
// token came from the keyring.
func TestEnsureLLMJudge_ResolvesCredentialFromKeyring(t *testing.T) {
	var gotAuth string
	srv := judgeCaptureServer(t, &gotAuth)

	judgeCredentialStore(t, map[string]string{"minimax": "sk-from-keyring"})

	o := judgeTestOrchestrator(t, judgeProjectWithConfig("judge-minimax", srv.URL+"/v1/chat/completions"))

	judge, err := o.ensureLLMJudge(context.Background())
	if err != nil {
		t.Fatalf("ensureLLMJudge: %v", err)
	}
	if judge == nil {
		t.Fatal("expected a judge")
	}
	if _, err := judge.Judge(context.Background(), "a spec intent long enough to pass", "artifact body"); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if gotAuth != "Bearer sk-from-keyring" {
		t.Errorf("Authorization = %q, want the keyring credential", gotAuth)
	}
}

// TestEnsureLLMJudge_NoCredentialFails: a "judge-*" binding with no
// token in the config and no credential in the keyring must NOT build a
// token-less judge — it must fail with an explicit error (the caller
// records it in the Error Observatory).
func TestEnsureLLMJudge_NoCredentialFails(t *testing.T) {
	judgeCredentialStore(t, nil)

	o := judgeTestOrchestrator(t, judgeProjectWithConfig("judge-minimax", "https://example.test/v1/chat/completions"))

	judge, err := o.ensureLLMJudge(context.Background())
	if err == nil {
		t.Fatal("expected an error when no credential can be resolved")
	}
	if judge != nil {
		t.Error("expected no judge when the credential is missing")
	}
	if !strings.Contains(err.Error(), "llm_key_add") {
		t.Errorf("error = %q, want it to point at llm_key_add", err.Error())
	}
}

// TestEnsureLLMJudge_LegacyConfigTokenWins: a row that still carries
// the token in the config (legacy) is honoured verbatim — the keyring
// is NOT consulted.
func TestEnsureLLMJudge_LegacyConfigTokenWins(t *testing.T) {
	var gotAuth string
	srv := judgeCaptureServer(t, &gotAuth)

	judgeCredentialStore(t, map[string]string{"minimax": "sk-from-keyring"})

	proj := judgeProjectWithConfig("judge-minimax", srv.URL+"/v1/chat/completions")
	proj.NLIConfig.Primary.AuthToken = "sk-legacy-config"

	o := judgeTestOrchestrator(t, proj)

	judge, err := o.ensureLLMJudge(context.Background())
	if err != nil {
		t.Fatalf("ensureLLMJudge: %v", err)
	}
	if _, err := judge.Judge(context.Background(), "a spec intent long enough to pass", "artifact body"); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if gotAuth != "Bearer sk-legacy-config" {
		t.Errorf("Authorization = %q, want the legacy config token", gotAuth)
	}
}

// TestEnsureLLMJudge_ResolvesExactIDCredential: a credential stored
// under the EXACT provider id (how llm_provider_bind stores a provider
// that names no catalog provider) is found by the judge too.
func TestEnsureLLMJudge_ResolvesExactIDCredential(t *testing.T) {
	var gotAuth string
	srv := judgeCaptureServer(t, &gotAuth)

	judgeCredentialStore(t, map[string]string{"judge-my-self-hosted": "sk-exact-id"})

	o := judgeTestOrchestrator(t, judgeProjectWithConfig("judge-my-self-hosted", srv.URL+"/v1/chat/completions"))

	judge, err := o.ensureLLMJudge(context.Background())
	if err != nil {
		t.Fatalf("ensureLLMJudge: %v", err)
	}
	if _, err := judge.Judge(context.Background(), "a spec intent long enough to pass", "artifact body"); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if gotAuth != "Bearer sk-exact-id" {
		t.Errorf("Authorization = %q, want the exact-id credential", gotAuth)
	}
}

// TestEnsureLLMJudge_StoredConfigHasNoToken: the persisted binding
// must carry no secret. This is the invariant the whole change exists
// for: projects.nli_config_json holds the provider_id, never the key.
func TestEnsureLLMJudge_StoredConfigHasNoToken(t *testing.T) {
	judgeCredentialStore(t, map[string]string{"minimax": "sk-from-keyring"})

	proj := judgeProjectWithConfig("judge-minimax", "https://example.test/v1/chat/completions")
	o := judgeTestOrchestrator(t, proj)

	if _, err := o.ensureLLMJudge(context.Background()); err != nil {
		t.Fatalf("ensureLLMJudge: %v", err)
	}

	raw, err := json.Marshal(proj.NLIConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-from-keyring") {
		t.Errorf("the credential leaked into the project config: %s", raw)
	}
	if strings.Contains(string(raw), "auth_token") {
		t.Errorf("the project config still carries an auth_token field: %s", raw)
	}
}

// TestBuildNLIPrimary_ResolvesCredentialFromKeyring: the NLI chat
// path resolves the credential the same way.
func TestBuildNLIPrimary_ResolvesCredentialFromKeyring(t *testing.T) {
	judgeCredentialStore(t, map[string]string{"minimax": "sk-nli-keyring"})

	hc := &http.Client{Transport: &countingTransport{}}
	provider, err := buildNLIPrimary(context.Background(), project.NLIPrimary{
		ProviderID: "chat-minimax",
		Endpoint:   "http://x/chat",
		TimeoutMS:  5000,
	}, hc, 1024, 1024)
	if err != nil {
		t.Fatalf("buildNLIPrimary: %v", err)
	}
	if provider == nil {
		t.Fatal("expected a provider")
	}
	if got := provider.ID(); got != "chat-minimax" {
		t.Errorf("ID = %q, want chat-minimax", got)
	}
}

// TestBuildNLIPrimary_NoCredentialFails: same contract on the NLI
// path — an explicit error, not a provider that would 401 forever.
func TestBuildNLIPrimary_NoCredentialFails(t *testing.T) {
	judgeCredentialStore(t, nil)

	hc := &http.Client{Transport: &countingTransport{}}
	_, err := buildNLIPrimary(context.Background(), project.NLIPrimary{
		ProviderID: "chat-minimax",
		Endpoint:   "http://x/chat",
		TimeoutMS:  5000,
	}, hc, 1024, 1024)
	if err == nil {
		t.Fatal("expected an error when no credential can be resolved")
	}
	if !strings.Contains(err.Error(), "llm_key_add") {
		t.Errorf("error = %q, want it to point at llm_key_add", err.Error())
	}
}

// TestBuildNLIPrimary_UnknownProviderStillUnmapped: a provider id that
// names no catalog provider (self-hosted) keeps working WITHOUT a
// credential — the mapping gate protects those deployments.
func TestBuildNLIPrimary_UnknownProviderStillUnmapped(t *testing.T) {
	judgeCredentialStore(t, nil)

	hc := &http.Client{Transport: &countingTransport{}}
	provider, err := buildNLIPrimary(context.Background(), project.NLIPrimary{
		ProviderID: "chat-my-self-hosted-vllm",
		Endpoint:   "http://x/chat",
		TimeoutMS:  5000,
	}, hc, 1024, 1024)
	if err != nil {
		t.Fatalf("buildNLIPrimary: %v", err)
	}
	if provider == nil {
		t.Fatal("expected a provider")
	}
}

// TestResolveBindingCredential_LegacyConfigToken: the helper resolves
// the credential from the keyring for a catalog provider.
func TestResolveBindingCredential_LegacyConfigToken(t *testing.T) {
	judgeCredentialStore(t, map[string]string{"minimax": "sk-keyring"})

	token, found, err := resolveBindingCredential("chat-minimax")
	if err != nil {
		t.Fatalf("resolveBindingCredential: %v", err)
	}
	if !found {
		t.Fatal("resolveBindingCredential: found = false, want true")
	}
	if token != "sk-keyring" {
		t.Errorf("token = %q, want the keyring credential", token)
	}
}

// TestResolveBindingCredential_UnmappedIsNotAnError: a provider id
// that names no catalog provider and has no credential is NOT an
// error — it is a self-hosted deployment that needs no bearer.
func TestResolveBindingCredential_UnmappedIsNotAnError(t *testing.T) {
	judgeCredentialStore(t, nil)

	token, found, err := resolveBindingCredential("chat-my-self-hosted")
	if err != nil {
		t.Fatalf("resolveBindingCredential: %v", err)
	}
	if found {
		t.Errorf("found = true (token %q), want false", token)
	}
}

// TestResolveBindingCredential_MappedMissingIsAnError: a catalog
// provider WITHOUT a credential IS an error — the operator must store
// the key first.
func TestResolveBindingCredential_MappedMissingIsAnError(t *testing.T) {
	judgeCredentialStore(t, nil)

	_, _, err := resolveBindingCredential("chat-minimax")
	if err == nil {
		t.Fatal("expected an error when the keyring has no credential")
	}
	if !strings.Contains(err.Error(), "llm_key_add") {
		t.Errorf("error = %q, want it to point at llm_key_add", err.Error())
	}
}

// TestResolveBindingCredential_ExactIDIsFound: a credential stored
// under the EXACT provider id (how llm_provider_bind stores a provider
// that names no catalog provider) is found too.
func TestResolveBindingCredential_ExactIDIsFound(t *testing.T) {
	judgeCredentialStore(t, map[string]string{"judge-my-self-hosted": "sk-exact"})

	token, found, err := resolveBindingCredential("judge-my-self-hosted")
	if err != nil {
		t.Fatalf("resolveBindingCredential: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if token != "sk-exact" {
		t.Errorf("token = %q, want the exact-id credential", token)
	}
}
