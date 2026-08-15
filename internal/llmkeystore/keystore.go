// Package llmkeystore provides secure storage for LLM provider API keys
// (spec 1188 T2).
//
// Primary backend: the operating system's credential vault via
// 99designs/keyring (Windows Credential Manager on Windows, Keychain on
// macOS, Secret Service on Linux). The API key NEVER lives in a config
// file, a log line, or an agent_memory row.
//
// Compatibility backend: process environment variables (the pre-v2.20.0
// mechanism), so CI jobs and harnesses that already inject keys keep
// working with zero changes.
//
// Composite read order: keyring first, then env. Writes always go to
// the keyring (the env is read-only by design — it is owned by the
// harness, not by dark-memory).
//
// Migration: MigrateFromEnv copies env keys into the keyring exactly
// once per missing key. It is idempotent and crash-safe: if the
// keyring write fails, the env var remains the source of truth.
package llmkeystore

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/99designs/keyring"
)

// ErrKeyNotFound is returned by Get when the provider has no key in
// any backend.
var ErrKeyNotFound = errors.New("llm keystore: key not found")

// KeyStore is the provider-key storage interface.
type KeyStore interface {
	// Get returns the key for a provider id, or ErrKeyNotFound.
	Get(providerID string) (string, error)
	// Set stores the key for a provider id.
	Set(providerID, key string) error
	// Delete removes the key for a provider id. Deleting a missing
	// key is a no-op.
	Delete(providerID string) error
	// Has reports whether a key exists without returning it.
	Has(providerID string) bool
	// Source reports where the key lives: "keyring", "env", or "none".
	// Used by llm_key_list (status only — never the value).
	Source(providerID string) string
}

// keyringStore wraps a 99designs/keyring.Keyring with a key prefix.
type keyringStore struct {
	ring   keyring.Keyring
	prefix string
}

// KeyringStore builds a KeyStore on an already-opened keyring.
// prefix is prepended to every item key (namespace separation).
func KeyringStore(ring keyring.Keyring, prefix string) KeyStore {
	return &keyringStore{ring: ring, prefix: prefix}
}

// DefaultKeyring opens the OS credential vault with the canonical
// service name. On Windows this is Credential Manager with target
// prefix "dark-memory/".
func DefaultKeyring() (KeyStore, error) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName:  "dark-memory-mcp",
		WinCredPrefix: "dark-memory/",
	})
	if err != nil {
		return nil, fmt.Errorf("llm keystore: open OS keyring: %w", err)
	}
	return &keyringStore{ring: ring, prefix: "llm/"}, nil
}

func (k *keyringStore) itemKey(providerID string) string {
	return k.prefix + providerID
}

func (k *keyringStore) Get(providerID string) (string, error) {
	item, err := k.ring.Get(k.itemKey(providerID))
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", ErrKeyNotFound
	}
	if err != nil {
		return "", fmt.Errorf("llm keystore: keyring get: %w", err)
	}
	return string(item.Data), nil
}

func (k *keyringStore) Set(providerID, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("llm keystore: refusing to store an empty key")
	}
	if err := k.ring.Set(keyring.Item{
		Key:         k.itemKey(providerID),
		Data:        []byte(key),
		Label:       "dark-memory-mcp LLM key (" + providerID + ")",
		Description: "dark-memory-mcp LLM-as-judge API key for provider " + providerID,
	}); err != nil {
		return fmt.Errorf("llm keystore: keyring set: %w", err)
	}
	return nil
}

func (k *keyringStore) Delete(providerID string) error {
	err := k.ring.Remove(k.itemKey(providerID))
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return nil // idempotent
	}
	if err != nil {
		return fmt.Errorf("llm keystore: keyring remove: %w", err)
	}
	return nil
}

func (k *keyringStore) Has(providerID string) bool {
	_, err := k.ring.Get(k.itemKey(providerID))
	return err == nil
}

func (k *keyringStore) Source(providerID string) string {
	if k.Has(providerID) {
		return "keyring"
	}
	return "none"
}

// envStore reads keys from process environment variables. It is
// read-only: Set/Delete are no-ops (the env is owned by the harness).
type envStore struct {
	// EnvKey resolves a provider id to its env var name (usually
	// llm.EnvKeyForProvider). Nil resolver = identity (provider id IS
	// the env var name — useful for tests).
	EnvKey func(providerID string) string
}

// EnvStore builds a read-only KeyStore over environment variables.
func EnvStore(envKeyResolver func(providerID string) string) KeyStore {
	return &envStore{EnvKey: envKeyResolver}
}

func (e *envStore) keyName(providerID string) string {
	if e.EnvKey != nil {
		return e.EnvKey(providerID)
	}
	return providerID
}

func (e *envStore) Get(providerID string) (string, error) {
	v := os.Getenv(e.keyName(providerID))
	if v == "" {
		return "", ErrKeyNotFound
	}
	return v, nil
}

func (e *envStore) Set(providerID, key string) error {
	return errors.New("llm keystore: env store is read-only (owned by the harness)")
}

func (e *envStore) Delete(providerID string) error { return nil }

func (e *envStore) Has(providerID string) bool {
	return os.Getenv(e.keyName(providerID)) != ""
}

func (e *envStore) Source(providerID string) string {
	if e.Has(providerID) {
		return "env"
	}
	return "none"
}

// Composite chains backends in order: reads consult each until one
// hits; writes go to the first backend only.
type Composite struct {
	backends []KeyStore
}

// NewComposite builds a chain. Read order = argument order.
func NewComposite(backends ...KeyStore) KeyStore {
	return &Composite{backends: backends}
}

func (c *Composite) Get(providerID string) (string, error) {
	var lastErr error = ErrKeyNotFound
	for _, b := range c.backends {
		v, err := b.Get(providerID)
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func (c *Composite) Set(providerID, key string) error {
	if len(c.backends) == 0 {
		return errors.New("llm keystore: composite has no backends")
	}
	return c.backends[0].Set(providerID, key)
}

func (c *Composite) Delete(providerID string) error {
	if len(c.backends) == 0 {
		return nil
	}
	return c.backends[0].Delete(providerID)
}

func (c *Composite) Has(providerID string) bool {
	for _, b := range c.backends {
		if b.Has(providerID) {
			return true
		}
	}
	return false
}

func (c *Composite) Source(providerID string) string {
	for _, b := range c.backends {
		if b.Has(providerID) {
			return b.Source(providerID)
		}
	}
	return "none"
}

// MemoryStore is an in-memory KeyStore for tests and one-shot tool
// invocations. NOT production-safe (no encryption, no persistence).
type MemoryStore struct {
	mu   sync.RWMutex
	keys map[string]string
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{keys: map[string]string{}}
}

func (m *MemoryStore) Get(providerID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.keys[providerID]
	if !ok {
		return "", ErrKeyNotFound
	}
	return v, nil
}

func (m *MemoryStore) Set(providerID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[providerID] = key
	return nil
}

func (m *MemoryStore) Delete(providerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, providerID)
	return nil
}

func (m *MemoryStore) Has(providerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.keys[providerID]
	return ok
}

func (m *MemoryStore) Source(providerID string) string {
	if m.Has(providerID) {
		return "memory"
	}
	return "none"
}

// EnvEntry pairs a provider id with its env var name (migration input).
type EnvEntry struct {
	ProviderID string
	EnvKey     string
}

// MigrateFromEnv copies env keys into the store once per missing key.
// Idempotent: entries already present in the store are skipped. Returns
// the provider ids that were migrated. A failing write aborts the
// remaining entries (env stays the source of truth for those).
func MigrateFromEnv(ks KeyStore, entries []EnvEntry) (migrated []string, err error) {
	for _, e := range entries {
		if ks.Has(e.ProviderID) {
			continue
		}
		v := os.Getenv(e.EnvKey)
		if v == "" {
			continue
		}
		if serr := ks.Set(e.ProviderID, v); serr != nil {
			return migrated, fmt.Errorf("llm keystore: migrate %s: %w", e.ProviderID, serr)
		}
		migrated = append(migrated, e.ProviderID)
	}
	return migrated, nil
}

// EnvEntriesFromCatalog is a convenience used by the llm package: it
// builds the migration entry list from the canonical provider catalog.
// Kept here (not in llm) to avoid an import cycle; the caller passes
// the (id, envKey) pairs.
func EnvEntriesFromCatalog(pairs [][2]string) []EnvEntry {
	out := make([]EnvEntry, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, EnvEntry{ProviderID: p[0], EnvKey: p[1]})
	}
	return out
}
