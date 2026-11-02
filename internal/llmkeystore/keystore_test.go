package llmkeystore

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/99designs/keyring"
)

// TestEnvStore_GetSetHas verifies the read-only env backend.
func TestEnvStore_GetSetHas(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	s := EnvStore(func(id string) string {
		if id == "deepseek" {
			return "DEEPSEEK_API_KEY"
		}
		return ""
	})
	if !s.Has("deepseek") {
		t.Fatal("Has(deepseek) = false, want true")
	}
	v, err := s.Get("deepseek")
	if err != nil || v != "sk-test" {
		t.Fatalf("Get = (%q, %v), want (sk-test, nil)", v, err)
	}
	if s.Source("deepseek") != "env" {
		t.Errorf("Source = %q, want env", s.Source("deepseek"))
	}
	if err := s.Set("deepseek", "x"); err == nil {
		t.Error("env store Set should be read-only")
	}
	// missing key
	if _, err := s.Get("openai"); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("Get(missing) err = %v, want ErrKeyNotFound", err)
	}
}

// TestComposite_KeyringWinsOverEnv verifies read order: keyring first,
// env second.
func TestComposite_KeyringWinsOverEnv(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	mem := NewMemoryStore()
	_ = mem.Set("deepseek", "ring-key")

	c := NewComposite(mem, EnvStore(func(id string) string { return "DEEPSEEK_API_KEY" }))
	v, err := c.Get("deepseek")
	if err != nil || v != "ring-key" {
		t.Fatalf("Get = (%q, %v), want (ring-key, nil) — keyring must win", v, err)
	}
	if c.Source("deepseek") != "memory" {
		t.Errorf("Source = %q, want memory (first backend)", c.Source("deepseek"))
	}
	// Set goes to the first backend only.
	if err := c.Set("openai", "new-key"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !mem.Has("openai") {
		t.Error("composite Set should write to first backend")
	}
}

// TestComposite_EnvFallback verifies env is used when keyring misses.
func TestComposite_EnvFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-only")
	mem := NewMemoryStore()
	c := NewComposite(mem, EnvStore(func(id string) string { return "OPENAI_API_KEY" }))
	if !c.Has("openai") {
		t.Fatal("Has(openai) = false, want true via env fallback")
	}
	if c.Source("openai") != "env" {
		t.Errorf("Source = %q, want env", c.Source("openai"))
	}
}

// TestMigrateFromEnv_Idempotent verifies env→store migration runs once
// per missing key and skips existing ones.
func TestMigrateFromEnv_Idempotent(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-1")
	t.Setenv("OPENAI_API_KEY", "sk-2")
	mem := NewMemoryStore()
	// pre-seed openai so only deepseek should migrate
	_ = mem.Set("openai", "old")

	entries := EnvEntriesFromCatalog([][2]string{
		{"deepseek", "DEEPSEEK_API_KEY"},
		{"openai", "OPENAI_API_KEY"},
		{"qwen", "DASHSCOPE_API_KEY"}, // env unset → skipped
	})
	migrated, err := MigrateFromEnv(mem, entries)
	if err != nil {
		t.Fatalf("MigrateFromEnv: %v", err)
	}
	if len(migrated) != 1 || migrated[0] != "deepseek" {
		t.Fatalf("migrated = %v, want [deepseek]", migrated)
	}
	if v, _ := mem.Get("openai"); v != "old" {
		t.Errorf("openai key overwritten: %q, want old (existing must be skipped)", v)
	}
	// second run → no-op
	migrated2, err := MigrateFromEnv(mem, entries)
	if err != nil || len(migrated2) != 0 {
		t.Fatalf("second migrate = (%v, %v), want ([], nil)", migrated2, err)
	}
}

// TestMigrateFromEnv_EmptyEnvUnsetEnvVar skipped (no-op).
func TestMigrateFromEnv_EmptyValueSkipped(t *testing.T) {
	t.Setenv("MINIMAX_API_KEY", "")
	mem := NewMemoryStore()
	migrated, err := MigrateFromEnv(mem, EnvEntriesFromCatalog([][2]string{{"minimax", "MINIMAX_API_KEY"}}))
	if err != nil || len(migrated) != 0 {
		t.Fatalf("empty env migrate = (%v, %v), want ([], nil)", migrated, err)
	}
}

// TestMemoryStore_CRUD is a sanity check on the test backend.
func TestMemoryStore_CRUD(t *testing.T) {
	m := NewMemoryStore()
	if m.Has("x") {
		t.Fatal("Has(x) = true on empty store")
	}
	if err := m.Set("x", "v"); err != nil {
		t.Fatal(err)
	}
	if !m.Has("x") {
		t.Fatal("Has(x) = false after Set")
	}
	if err := m.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if m.Has("x") {
		t.Fatal("Has(x) = true after Delete")
	}
}

// TestDefaultKeyring_OpensOnThisOS verifies the OS keyring opens on
// the current platform (best-effort — some CI/headless environments
// have no keyring; we only fail when it opens but misbehaves).
func TestDefaultKeyring_OpensOnThisOS(t *testing.T) {
	ks, err := DefaultKeyring()
	if err != nil {
		t.Logf("OS keyring unavailable on this machine (acceptable in CI): %v", err)
		return
	}
	if ks == nil {
		t.Fatal("DefaultKeyring returned nil store with nil error")
	}
}

// TestKeyringRoundTrip writes + reads + deletes through the keyringStore
// wrapper backed by the real 99designs ArrayKeyring (the in-memory test
// backend the library ships for exactly this purpose).
func TestKeyringRoundTrip(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	ks := KeyringStore(ring, "llm/")
	if err := ks.Set("deepseek", "sk-test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !ks.Has("deepseek") {
		t.Fatal("Has(deepseek) = false after Set")
	}
	if ks.Source("deepseek") != "keyring" {
		t.Errorf("Source = %q, want keyring", ks.Source("deepseek"))
	}
	v, err := ks.Get("deepseek")
	if err != nil || v != "sk-test" {
		t.Fatalf("Get = (%q, %v), want (sk-test, nil)", v, err)
	}
	if err := ks.Delete("deepseek"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ks.Has("deepseek") {
		t.Fatal("Has(deepseek) = true after Delete")
	}
	// Deleting a missing key is a no-op.
	if err := ks.Delete("deepseek"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	// Empty key refused.
	if err := ks.Set("openai", "  "); err == nil {
		t.Error("Set with blank key should be refused")
	}
}

// TestCredentialRoundTrip verifies a credential (key + resolved
// endpoint) survives a write/read cycle through the keyring.
func TestCredentialRoundTrip(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	ks := KeyringStore(ring, "llm/")

	want := Credential{
		Key:        "sk-region-scoped",
		EndpointID: "minimax-cn",
		BaseURL:    "https://api.minimaxi.com",
	}
	if err := ks.StoreCredential("minimax", want); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}

	got, ok := ks.LoadCredential("minimax")
	if !ok {
		t.Fatal("LoadCredential = not found, want found")
	}
	if got != want {
		t.Fatalf("LoadCredential = %+v, want %+v", got, want)
	}

	// The key alone is still reachable through the plain KeyStore API.
	v, err := ks.Get("minimax")
	if err != nil || v != want.Key {
		t.Fatalf("Get = (%q, %v), want (%q, nil)", v, err, want.Key)
	}

	// A missing provider yields (zero credential, false).
	if c, ok := ks.LoadCredential("openai"); ok || c != (Credential{}) {
		t.Errorf("LoadCredential(missing) = (%+v, %v), want (zero, false)", c, ok)
	}
}

// TestCredentialStoredAsJSON verifies the on-disk item is the JSON
// credential record (so the endpoint travels with the key).
func TestCredentialStoredAsJSON(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	ks := KeyringStore(ring, "llm/")

	cred := Credential{Key: "sk-1", EndpointID: "minimax", BaseURL: "https://api.minimax.io"}
	if err := ks.StoreCredential("minimax", cred); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}

	item, err := ring.Get("llm/minimax")
	if err != nil {
		t.Fatalf("ring.Get: %v", err)
	}
	var decoded Credential
	if err := json.Unmarshal(item.Data, &decoded); err != nil {
		t.Fatalf("stored item is not valid credential JSON: %v (raw=%q)", err, string(item.Data))
	}
	if decoded != cred {
		t.Fatalf("stored JSON = %+v, want %+v", decoded, cred)
	}
}

// TestCredentialLegacyRawKey verifies the legacy format (the item
// holds the raw key, not JSON) is still readable: the key comes back,
// the endpoint is empty.
func TestCredentialLegacyRawKey(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	ks := KeyringStore(ring, "llm/")

	// Write the item directly, bypassing StoreCredential, exactly like
	// the pre-credential-record code did.
	if err := ring.Set(keyring.Item{
		Key:  "llm/minimax",
		Data: []byte("sk-legacy-raw-key"),
	}); err != nil {
		t.Fatalf("ring.Set: %v", err)
	}

	got, ok := ks.LoadCredential("minimax")
	if !ok {
		t.Fatal("LoadCredential = not found, want found")
	}
	if got.Key != "sk-legacy-raw-key" {
		t.Errorf("LoadCredential.Key = %q, want sk-legacy-raw-key", got.Key)
	}
	if got.EndpointID != "" || got.BaseURL != "" {
		t.Errorf("legacy credential should have an empty endpoint, got %+v", got)
	}

	// The plain key API keeps working on legacy items too.
	v, err := ks.Get("minimax")
	if err != nil || v != "sk-legacy-raw-key" {
		t.Errorf("Get = (%q, %v), want (sk-legacy-raw-key, nil)", v, err)
	}
}

// TestEnvStore_LoadCredential verifies the env backend reports the key
// with an EMPTY endpoint (the env carries the key only) and that
// StoreCredential is a no-op that reports the key as stored.
func TestEnvStore_LoadCredential(t *testing.T) {
	t.Setenv("MINIMAX_API_KEY", "sk-env")
	s := EnvStore(func(id string) string {
		if id == "minimax" {
			return "MINIMAX_API_KEY"
		}
		return ""
	})

	got, ok := s.LoadCredential("minimax")
	if !ok {
		t.Fatal("LoadCredential = not found, want found")
	}
	if got.Key != "sk-env" {
		t.Errorf("LoadCredential.Key = %q, want sk-env", got.Key)
	}
	if got.EndpointID != "" || got.BaseURL != "" {
		t.Errorf("env credential must have an empty endpoint, got %+v", got)
	}

	// Missing env var → not found.
	if c, ok := s.LoadCredential("openai"); ok || c != (Credential{}) {
		t.Errorf("LoadCredential(missing) = (%+v, %v), want (zero, false)", c, ok)
	}

	// StoreCredential is a documented no-op: it reports success and
	// does not touch the environment.
	if err := s.StoreCredential("minimax", Credential{Key: "sk-other", EndpointID: "x"}); err != nil {
		t.Errorf("StoreCredential on the env backend should be a no-op, got err: %v", err)
	}
	if v, _ := s.Get("minimax"); v != "sk-env" {
		t.Errorf("env value changed by StoreCredential: %q, want sk-env", v)
	}
}

// TestComposite_CredentialDelegation verifies the composite stores the
// credential in the FIRST (writable) backend — the keyring — and
// reads it back from there, with the env as fallback.
func TestComposite_CredentialDelegation(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "env-key")
	ring := keyring.NewArrayKeyring(nil)
	ks := KeyringStore(ring, "llm/")
	env := EnvStore(func(id string) string { return "DEEPSEEK_API_KEY" })
	c := NewComposite(ks, env)

	cred := Credential{Key: "ring-key", EndpointID: "deepseek", BaseURL: "https://api.deepseek.com"}
	if err := c.StoreCredential("deepseek", cred); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}

	// Written to the keyring, NOT to the env backend.
	if _, err := ring.Get("llm/deepseek"); err != nil {
		t.Fatalf("credential not stored in the keyring: %v", err)
	}
	got, ok := c.LoadCredential("deepseek")
	if !ok {
		t.Fatal("LoadCredential = not found, want found")
	}
	if got != cred {
		t.Fatalf("LoadCredential = %+v, want %+v", got, cred)
	}

	// Env fallback: a provider only present in the env yields the key
	// with an empty endpoint.
	envOnly, ok := c.LoadCredential("openai")
	if !ok {
		t.Fatal("LoadCredential(env-only) = not found, want found")
	}
	if envOnly.Key != "env-key" || envOnly.EndpointID != "" {
		t.Errorf("LoadCredential(env-only) = %+v, want key env-key with empty endpoint", envOnly)
	}
}

// TestComposite_CredentialLegacyFallback verifies a LEGACY keyring
// item (raw key) still loads through the composite, with an empty
// endpoint.
func TestComposite_CredentialLegacyFallback(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	if err := ring.Set(keyring.Item{
		Key:  "llm/minimax",
		Data: []byte("sk-legacy"),
	}); err != nil {
		t.Fatalf("ring.Set: %v", err)
	}
	ks := KeyringStore(ring, "llm/")
	c := NewComposite(ks, EnvStore(func(id string) string { return "MINIMAX_API_KEY" }))

	got, ok := c.LoadCredential("minimax")
	if !ok {
		t.Fatal("LoadCredential = not found, want found")
	}
	if got.Key != "sk-legacy" {
		t.Errorf("LoadCredential.Key = %q, want sk-legacy", got.Key)
	}
	if got.EndpointID != "" || got.BaseURL != "" {
		t.Errorf("legacy credential must have an empty endpoint, got %+v", got)
	}
}

// TestCredentialStoreOf verifies a plain KeyStore handle can be
// upgraded to the credential-record extension.
func TestCredentialStoreOf(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	var ks KeyStore = KeyringStore(ring, "llm/")
	cs, ok := CredentialStoreOf(ks)
	if !ok {
		t.Fatal("CredentialStoreOf = not ok, want ok")
	}
	if err := cs.StoreCredential("minimax", Credential{Key: "sk-1", EndpointID: "minimax"}); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	got, ok := cs.LoadCredential("minimax")
	if !ok || got.Key != "sk-1" || got.EndpointID != "minimax" {
		t.Fatalf("LoadCredential = (%+v, %v), want the stored credential", got, ok)
	}
	if _, ok := CredentialStoreOf(nil); ok {
		t.Error("CredentialStoreOf(nil) = ok, want not ok")
	}
}
