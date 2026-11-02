// Package tools — llm_bind_keyring_test.go
//
// The credential NEVER reaches the project config: it lives in the OS
// keyring and the config keeps only the provider id.
//
// These tests pin:
//
//   - a bind WITHOUT auth_token succeeds when the keyring already holds
//     the credential (mapped or exact), and persists the binding with
//     an EMPTY auth_token;
//   - a bind WITHOUT auth_token fails loudly when the keyring has no
//     credential (the operator must call llm_key_add first);
//   - a bind WITH auth_token stores the credential in the keyring and
//     leaves the persisted config free of the secret;
//   - the result never echoes the token.
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
)

// TestLLMProviderBind_Keyring_NoAuthToken_UsesExistingCredential:
// the keyring already holds the credential under the CATALOG id
// ("minimax"), so a bind of "judge-minimax" without a token succeeds
// and persists the binding WITHOUT the secret.
func TestLLMProviderBind_Keyring_NoAuthToken_UsesExistingCredential(t *testing.T) {
	stub := bindTestSetup(t)
	ks := bindTestKeyStore(t)
	if err := ks.Set("minimax", "sk-catalog-key"); err != nil {
		t.Fatal(err)
	}

	in := LLMProviderBindInput{
		ProviderID: "judge-minimax",
		Endpoint:   "https://api.minimax.io/v1/chat/completions",
		TimeoutMS:  10000,
	}
	resp, err := runLLMProviderBind(context.Background(), in)
	if err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}
	result, ok := resp.Data.(LLMProviderBindResult)
	if !ok {
		t.Fatalf("expected LLMProviderBindResult, got %T", resp.Data)
	}
	if !result.AuthPresent {
		t.Error("AuthPresent: got false, want true (a credential is bound)")
	}

	persisted, err := stub.GetProjectRaw(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if persisted.NLIConfig == nil {
		t.Fatal("NLIConfig is nil after bind")
	}
	if persisted.NLIConfig.Primary.ProviderID != "judge-minimax" {
		t.Errorf("Primary.ProviderID: got %q, want judge-minimax", persisted.NLIConfig.Primary.ProviderID)
	}
	if persisted.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("Primary.AuthToken: got %q, want empty", persisted.NLIConfig.Primary.AuthToken)
	}
	// The whole NLIConfig must serialize WITHOUT the secret.
	raw, err := json.Marshal(persisted.NLIConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-catalog-key") {
		t.Errorf("the credential leaked into the project config: %s", raw)
	}
	if strings.Contains(string(raw), "auth_token") {
		t.Errorf("the project config still carries an auth_token field: %s", raw)
	}
}

// TestLLMProviderBind_Keyring_NoAuthToken_RequiresCredential: with no
// credential in the keyring the bind fails loudly — it must not
// persist a binding that could never authenticate.
func TestLLMProviderBind_Keyring_NoAuthToken_RequiresCredential(t *testing.T) {
	stub := bindTestSetup(t)
	bindTestKeyStore(t)

	in := LLMProviderBindInput{
		ProviderID: "judge-minimax",
		Endpoint:   "https://api.minimax.io/v1/chat/completions",
	}
	_, err := runLLMProviderBind(context.Background(), in)
	if err == nil {
		t.Fatal("expected an error when the keyring has no credential")
	}
	if !strings.Contains(err.Error(), "llm_key_add") {
		t.Errorf("error: got %q, want it to point at llm_key_add", err.Error())
	}

	// Nothing must have been persisted.
	persisted, err := stub.GetProjectRaw(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if persisted.NLIConfig != nil {
		t.Errorf("the binding was persisted despite the failure: %+v", persisted.NLIConfig)
	}
}

// TestLLMProviderBind_Keyring_WithAuthToken_StoresInKeyring: a bind
// WITH a token stores the credential in the keyring (under the
// catalog id) and leaves the project config free of the secret.
//
// "judge-openai" is used on purpose: the provider has a SINGLE
// endpoint candidate, so the credential-scoped endpoint resolution
// short-circuits (no network probe in a unit test).
func TestLLMProviderBind_Keyring_WithAuthToken_StoresInKeyring(t *testing.T) {
	stub := bindTestSetup(t)
	ks := bindTestKeyStore(t)

	in := LLMProviderBindInput{
		ProviderID: "judge-openai",
		Endpoint:   "https://api.openai.com/v1/chat/completions",
		AuthToken:  "sk-brand-new-key",
	}
	resp, err := runLLMProviderBind(context.Background(), in)
	if err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}

	// The credential must be in the keyring under the CATALOG id, so
	// both the "judge-" and the "chat-" dispatch paths find it.
	stored, err := ks.Get("openai")
	if err != nil {
		t.Fatalf("keyring Get(openai): %v", err)
	}
	if stored != "sk-brand-new-key" {
		t.Errorf("keyring: got %q, want the bound credential", stored)
	}

	// ... and NOT in the project config.
	persisted, err := stub.GetProjectRaw(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if persisted.NLIConfig == nil {
		t.Fatal("NLIConfig is nil after bind")
	}
	if persisted.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("Primary.AuthToken: got %q, want empty", persisted.NLIConfig.Primary.AuthToken)
	}
	raw, err := json.Marshal(persisted.NLIConfig)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-brand-new-key") {
		t.Errorf("the credential leaked into the project config: %s", raw)
	}

	// The result must not echo the token either.
	out, _ := json.Marshal(resp.Data)
	if strings.Contains(string(out), "sk-brand-new-key") {
		t.Errorf("the credential leaked into the tool result: %s", out)
	}
}

// TestLLMProviderBind_Keyring_MiniMaxStoredUnderCatalogID: the
// MiniMax binding stores its credential under the CATALOG id
// ("minimax"), so ONE key serves both the "judge-" and the "chat-"
// dispatch paths of that provider.
//
// The operator override pins the endpoint (no network probe in a unit
// test); the credential-scoped resolution honours it.
func TestLLMProviderBind_Keyring_MiniMaxStoredUnderCatalogID(t *testing.T) {
	t.Setenv("DARK_LLM_BASE_URL_MINIMAX", "https://api.minimax.io/v1")
	bindTestSetup(t)
	ks := bindTestKeyStore(t)

	in := LLMProviderBindInput{
		ProviderID: "judge-minimax",
		Endpoint:   "https://api.minimax.io/v1/chat/completions",
		AuthToken:  "sk-minimax-key",
	}
	if _, err := runLLMProviderBind(context.Background(), in); err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}

	// Under the CATALOG id, not under the dispatch-prefixed id.
	stored, err := ks.Get("minimax")
	if err != nil {
		t.Fatalf("keyring Get(minimax): %v", err)
	}
	if stored != "sk-minimax-key" {
		t.Errorf("keyring: got %q, want the bound credential", stored)
	}
	if _, err := ks.Get("judge-minimax"); err == nil {
		t.Error("the credential must be stored under the catalog id, not the dispatch id")
	}
}

// TestLLMProviderBind_Keyring_UnmappedProvider_StoredUnderFullID:
// a provider id that names no catalog provider is stored under its
// FULL id (the keyring accepts any id).
func TestLLMProviderBind_Keyring_UnmappedProvider_StoredUnderFullID(t *testing.T) {
	bindTestSetup(t)
	ks := bindTestKeyStore(t)

	in := LLMProviderBindInput{
		ProviderID: "judge-my-self-hosted",
		Endpoint:   "https://llm.internal/v1/chat/completions",
		AuthToken:  "sk-self-hosted-key",
	}
	if _, err := runLLMProviderBind(context.Background(), in); err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}
	stored, err := ks.Get("judge-my-self-hosted")
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if stored != "sk-self-hosted-key" {
		t.Errorf("keyring: got %q, want the bound credential", stored)
	}
}

// TestLLMProviderBind_Keyring_NoAuthToken_ExactID: the credential may
// live under the EXACT binding id (not the catalog id) — the bind
// accepts it too.
func TestLLMProviderBind_Keyring_NoAuthToken_ExactID(t *testing.T) {
	bindTestSetup(t)
	ks := bindTestKeyStore(t)
	if err := ks.Set("judge-minimax", "sk-exact-id-key"); err != nil {
		t.Fatal(err)
	}

	in := LLMProviderBindInput{
		ProviderID: "judge-minimax",
		Endpoint:   "https://api.minimax.io/v1/chat/completions",
	}
	if _, err := runLLMProviderBind(context.Background(), in); err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}
}

// TestLLMProviderBind_Keyring_LegacyConfigTokenKept: a project that
// already carries a token in its config keeps working — the bind
// replaces the primary but the fallback binding (and its token) is
// preserved untouched.
func TestLLMProviderBind_Keyring_LegacyConfigTokenKept(t *testing.T) {
	stub := bindTestSetup(t)
	bindTestKeyStore(t)

	p := seedDefaultProject(stub)
	p.NLIConfig = &project.NLIConfig{
		Enabled: true,
		Primary: project.NLIPrimary{
			ProviderID: "judge-legacy",
			Endpoint:   "https://legacy.test/v1/chat/completions",
			AuthToken:  "sk-legacy",
			TimeoutMS:  5000,
		},
		LatencyBudgetMS:    10000,
		MaxPremiseBytes:    65536,
		MaxHypothesisBytes: 8192,
		MaxCacheEntries:    1000,
		CacheTTLSeconds:    300,
	}
	if err := stub.CreateProject(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	in := LLMProviderBindInput{
		ProviderID: "judge-openai",
		Endpoint:   "https://api.openai.com/v1/chat/completions",
		AuthToken:  "sk-new-key",
	}
	if _, err := runLLMProviderBind(context.Background(), in); err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}

	persisted, err := stub.GetProjectRaw(context.Background(), "default")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if persisted.NLIConfig.Primary.AuthToken != "" {
		t.Errorf("Primary.AuthToken: got %q, want empty", persisted.NLIConfig.Primary.AuthToken)
	}
}

// TestLLMProviderBind_Keyring_StoredCredentialCarriesEndpoint: the
// stored credential record carries the endpoint that was resolved for
// it, so the judge can pick the right deployment at call time.
func TestLLMProviderBind_Keyring_StoredCredentialCarriesEndpoint(t *testing.T) {
	bindTestSetup(t)
	ks := bindTestKeyStore(t)

	in := LLMProviderBindInput{
		ProviderID: "judge-openai",
		Endpoint:   "https://api.openai.com/v1/chat/completions",
		AuthToken:  "sk-openai-key",
	}
	if _, err := runLLMProviderBind(context.Background(), in); err != nil {
		t.Fatalf("runLLMProviderBind: %v", err)
	}
	cred, ok := ks.LoadCredential("openai")
	if !ok {
		t.Fatal("no credential stored under the catalog id")
	}
	if cred.Key != "sk-openai-key" {
		t.Errorf("credential key: got %q, want the bound credential", cred.Key)
	}
	if cred.EndpointID == "" {
		t.Error("the stored credential carries no endpoint id")
	}
}
