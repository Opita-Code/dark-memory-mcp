package llmkeystore

import (
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
