// Package llm — credential_test.go
//
// The credential resolution contract (spec: credential-scoped
// resolution follow-up):
//
//   - CatalogProviderID strips the DISPATCH prefixes ("judge-", "chat-")
//     and validates what is left against the catalog.
//   - CredentialForProvider resolves the key for a BINDING provider id:
//     exact id first, then the catalog id, then the env var of the
//     catalog spec.
//
// The key itself must NEVER appear in a log line or in an error.
package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/llmkeystore"
)

// fakeKeyring is a KeyStore that only answers for ONE provider id.
// It models the real keyring, where the credential of "minimax" is
// stored under the CATALOG id, not under the dispatch-prefixed id.
type fakeKeyring struct {
	// id is the ONLY provider id this store answers for.
	id string
	// key is the credential returned for that id.
	key string
	// calls records every id the store was asked for.
	calls []string
}

func (f *fakeKeyring) Get(providerID string) (string, error) {
	f.calls = append(f.calls, providerID)
	if providerID != f.id {
		return "", llmkeystore.ErrKeyNotFound
	}
	return f.key, nil
}

func (f *fakeKeyring) Set(providerID, key string) error { return nil }
func (f *fakeKeyring) Delete(providerID string) error   { return nil }
func (f *fakeKeyring) Has(providerID string) bool {
	return providerID == f.id
}
func (f *fakeKeyring) Source(providerID string) string {
	if providerID == f.id {
		return "keyring"
	}
	return "none"
}

// TestCatalogProviderID_StripsDispatchPrefixes pins the mapping from
// a binding provider id to the catalog provider it talks to.
func TestCatalogProviderID_StripsDispatchPrefixes(t *testing.T) {
	cases := []struct {
		providerID string
		wantID     string
		wantOK     bool
	}{
		{"judge-minimax", "minimax", true},
		{"chat-minimax", "minimax", true},
		{"minimax", "minimax", true},
		{"desconocido", "", false},
		{"judge-desconocido", "", false},
		{"chat-desconocido", "", false},
		{"", "", false},
		{"judge-", "", false},
		// A sibling catalog id keeps its own identity.
		{"judge-minimax-cn", "minimax-cn", true},
		{"chat-minimax-cn", "minimax-cn", true},
		// Legacy alias ids resolve to their canonical catalog id.
		{"judge-z-ai", "zhipu", true},
	}
	for _, c := range cases {
		got, ok := CatalogProviderID(c.providerID)
		if ok != c.wantOK {
			t.Errorf("CatalogProviderID(%q) ok = %v, want %v", c.providerID, ok, c.wantOK)
			continue
		}
		if got != c.wantID {
			t.Errorf("CatalogProviderID(%q) = %q, want %q", c.providerID, got, c.wantID)
		}
	}
}

// TestCredentialForProvider_FindsCatalogIDKey: the keyring holds the
// credential under the CATALOG id ("minimax"); a binding that says
// "judge-minimax" must still find it (via the catalog id).
func TestCredentialForProvider_FindsCatalogIDKey(t *testing.T) {
	ks := &fakeKeyring{id: "minimax", key: "sk-catalog-key"}

	for _, providerID := range []string{"judge-minimax", "chat-minimax", "minimax"} {
		key, ok := CredentialForProvider(ks, providerID)
		if !ok {
			t.Fatalf("CredentialForProvider(%q): not found, want the catalog key", providerID)
		}
		if key != "sk-catalog-key" {
			t.Errorf("CredentialForProvider(%q) = %q, want the catalog key", providerID, key)
		}
	}
}

// TestCredentialForProvider_ExactIDFirst: a credential stored under the
// EXACT binding id ("judge-minimax") wins over the catalog id.
func TestCredentialForProvider_ExactIDFirst(t *testing.T) {
	ks := &fakeKeyring{id: "judge-minimax", key: "sk-exact-key"}

	key, ok := CredentialForProvider(ks, "judge-minimax")
	if !ok {
		t.Fatal("CredentialForProvider: not found, want the exact-id key")
	}
	if key != "sk-exact-key" {
		t.Errorf("CredentialForProvider = %q, want the exact-id key", key)
	}
	// The catalog id is NOT consulted when the exact id hits.
	for _, call := range ks.calls {
		if call == "minimax" {
			t.Error("catalog id was consulted even though the exact id matched")
		}
	}
}

// TestCredentialForProvider_NoCredential: an unknown provider id has
// no credential. The empty string must be returned (never a stray
// key), and the "not found" answer must be a clean false.
func TestCredentialForProvider_NoCredential(t *testing.T) {
	ks := &fakeKeyring{id: "minimax", key: "sk-catalog-key"}

	key, ok := CredentialForProvider(ks, "judge-desconocido")
	if ok {
		t.Fatalf("CredentialForProvider: ok = true (key %q), want false", key)
	}
	if key != "" {
		t.Errorf("CredentialForProvider returned a key on failure: %q", key)
	}
}

// TestCredentialForProvider_EnvFallback: the env var of the catalog
// spec is the compatibility backend — used when the keyring has no
// entry for the provider.
func TestCredentialForProvider_EnvFallback(t *testing.T) {
	ks := &fakeKeyring{id: "otra-cosa", key: "sk-otra"}
	t.Setenv("MINIMAX_API_KEY", "sk-from-env")

	key, ok := CredentialForProvider(ks, "judge-minimax")
	if !ok {
		t.Fatal("CredentialForProvider: not found, want the env key")
	}
	if key != "sk-from-env" {
		t.Errorf("CredentialForProvider = %q, want the env key", key)
	}
}

// TestCredentialForProvider_KeyringWinsOverEnv: the keyring is the
// source of truth. The env var must not shadow a stored key.
func TestCredentialForProvider_KeyringWinsOverEnv(t *testing.T) {
	ks := &fakeKeyring{id: "minimax", key: "sk-from-keyring"}
	t.Setenv("MINIMAX_API_KEY", "sk-from-env")

	key, ok := CredentialForProvider(ks, "judge-minimax")
	if !ok {
		t.Fatal("CredentialForProvider: not found, want the keyring key")
	}
	if key != "sk-from-keyring" {
		t.Errorf("CredentialForProvider = %q, want the keyring key (env must not shadow it)", key)
	}
}

// TestCredentialForProvider_NilKeyStore: a nil key store must not
// panic — it just means "no credential".
func TestCredentialForProvider_NilKeyStore(t *testing.T) {
	key, ok := CredentialForProvider(nil, "judge-minimax")
	if ok || key != "" {
		t.Errorf("CredentialForProvider(nil, ...) = (%q, %v), want (\"\", false)", key, ok)
	}
}

// TestCredentialForProvider_EmptyKeyIsNoCredential: a stored but EMPTY
// key is not a credential (it would authenticate as nobody).
func TestCredentialForProvider_EmptyKeyIsNoCredential(t *testing.T) {
	ks := &fakeKeyring{id: "minimax", key: "   "}

	key, ok := CredentialForProvider(ks, "judge-minimax")
	if ok {
		t.Errorf("CredentialForProvider: ok = true (key %q), want false for a blank key", key)
	}
	if key != "" {
		t.Errorf("CredentialForProvider returned %q, want an empty key", key)
	}
}

// TestCredentialForProvider_UnknownIDHasNoEnvFallback: an id that
// names no catalog provider has no env var either — the env fallback
// is tied to the catalog spec.
func TestCredentialForProvider_UnknownIDHasNoEnvFallback(t *testing.T) {
	ks := &fakeKeyring{id: "otra-cosa", key: "sk-otra"}
	t.Setenv("MINIMAX_API_KEY", "sk-from-env")

	key, ok := CredentialForProvider(ks, "judge-desconocido")
	if ok {
		t.Errorf("CredentialForProvider: ok = true (key %q), want false", key)
	}
}

// TestCredentialForProvider_MemoryStore: the shipped in-memory store
// behaves like the keyring for the catalog-id lookup.
func TestCredentialForProvider_MemoryStore(t *testing.T) {
	mem := llmkeystore.NewMemoryStore()
	if err := mem.Set("minimax", "sk-mem"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	key, ok := CredentialForProvider(mem, "judge-minimax")
	if !ok {
		t.Fatal("CredentialForProvider: not found, want the memory-store key")
	}
	if key != "sk-mem" {
		t.Errorf("CredentialForProvider = %q, want %q", key, "sk-mem")
	}
}

// TestCredentialForProvider_NeverLeaksKeyInErrors: the helper returns
// the key as a VALUE — never through an error. This pins that the
// contract has no error path that could carry the secret.
func TestCredentialForProvider_NeverLeaksKeyInErrors(t *testing.T) {
	ks := &fakeKeyring{id: "minimax", key: "sk-super-secret"}
	key, ok := CredentialForProvider(ks, "judge-minimax")
	if !ok {
		t.Fatal("CredentialForProvider: not found")
	}
	if strings.Contains(key, " ") {
		t.Errorf("unexpected key shape: %q", key)
	}
	// The store's own error path carries no key either.
	_, err := ks.Get("nope")
	if err == nil {
		t.Fatal("expected an error for an unknown id")
	}
	if !errors.Is(err, llmkeystore.ErrKeyNotFound) {
		t.Errorf("err = %v, want ErrKeyNotFound", err)
	}
	if strings.Contains(err.Error(), "sk-super-secret") {
		t.Error("the key leaked through an error message")
	}
}
