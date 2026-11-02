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
//
// Credential records: the valid endpoint of a provider depends on the
// CREDENTIAL (a provider may mint region-scoped keys that only
// authenticate against the endpoint they were issued for). The
// keystore therefore can persist the key TOGETHER with the endpoint
// that was resolved for it (see Credential / CredentialStore). The
// endpoint travels with the key; it is never inferred from the key
// alone.
package llmkeystore

import (
	"encoding/json"
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

// Credential is a provider API key together with the endpoint that was
// resolved for it. The endpoint is part of the credential because a
// provider may issue region-scoped keys: the key determines which
// endpoint accepts it.
type Credential struct {
	Key        string `json:"key"`
	EndpointID string `json:"endpoint_id,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
}

// CredentialStore is an OPTIONAL extension of KeyStore: it can also
// persist the endpoint that was resolved for a key. Implementations
// that cannot persist the endpoint (the env backend) must still
// implement it: LoadCredential returns the key with an empty endpoint
// and StoreCredential is a no-op that reports the key as stored.
type CredentialStore interface {
	// StoreCredential persists key+endpoint for a provider id.
	StoreCredential(providerID string, c Credential) error
	// LoadCredential returns the stored credential. The bool is false
	// when no credential exists for that provider.
	LoadCredential(providerID string) (Credential, bool)
}

// Store is the full key store contract: KeyStore plus the optional
// credential-record extension.
//
// Store satisfies KeyStore, so every consumer that only needs
// Get/Set/Delete/Has/Source keeps compiling unchanged.
type Store interface {
	KeyStore
	CredentialStore
}

// CredentialStoreOf returns the credential-record extension of a
// KeyStore when the backing store supports it. Every store shipped
// by this package does, so callers that hold a plain KeyStore (e.g.
// the one returned by the orchestration wiring) can upgrade it
// without changing the wiring.
func CredentialStoreOf(ks KeyStore) (CredentialStore, bool) {
	if ks == nil {
		return nil, false
	}
	cs, ok := ks.(CredentialStore)
	return cs, ok
}

// decodeCredential parses a stored payload as a credential record.
//
// LEGACY tolerance: items written before credential records existed
// hold the RAW key (not JSON). They are still valid and must keep
// working: a payload that is not JSON, or that unmarshals but carries
// no "key" field, is treated as the raw key with an empty endpoint.
func decodeCredential(data []byte) Credential {
	var c Credential
	if err := json.Unmarshal(data, &c); err != nil {
		// Not JSON at all → legacy raw key.
		return Credential{Key: string(data)}
	}
	if c.Key == "" {
		// JSON but no usable key → legacy raw key (e.g. "{}").
		return Credential{Key: string(data)}
	}
	return c
}

// keyringStore wraps a 99designs/keyring.Keyring with a key prefix.
type keyringStore struct {
	ring   keyring.Keyring
	prefix string
}

// Compile-time guarantees: every shipped backend implements the full
// Store contract (KeyStore + credential records).
var (
	_ Store = (*keyringStore)(nil)
	_ Store = (*envStore)(nil)
	_ Store = (*Composite)(nil)
	_ Store = (*MemoryStore)(nil)
)

// KeyringStore builds a KeyStore on an already-opened keyring.
// prefix is prepended to every item key (namespace separation).
func KeyringStore(ring keyring.Keyring, prefix string) Store {
	return &keyringStore{ring: ring, prefix: prefix}
}

// DefaultKeyring opens the OS credential vault with the canonical
// service name. On Windows this is Credential Manager with target
// prefix "dark-memory/".
func DefaultKeyring() (Store, error) {
	ring, err := keyring.Open(keyring.Config{
		ServiceName:   "dark-memory-mcp",
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
	// The item holds either a credential record (JSON, written by
	// StoreCredential) or a legacy raw key. Both decode to the key.
	return decodeCredential(item.Data).Key, nil
}

// Set stores the key for a provider id.
//
// The endpoint that was resolved for the key is preserved when the
// SAME key is re-set (the endpoint belongs to the key, not to the
// write). Storing a DIFFERENT key drops the endpoint: the new key may
// belong to another region and its endpoint is unknown until it is
// resolved again.
func (k *keyringStore) Set(providerID, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("llm keystore: refusing to store an empty key")
	}
	data := []byte(key)
	if cur, ok := k.LoadCredential(providerID); ok && cur.Key == key {
		// Same key → keep the resolved endpoint, store the full record.
		if enc, err := json.Marshal(cur); err == nil {
			data = enc
		}
	}
	if err := k.ring.Set(keyring.Item{
		Key:         k.itemKey(providerID),
		Data:        data,
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

// StoreCredential persists the key together with the endpoint that was
// resolved for it. The item is stored as UTF-8 JSON of Credential
// ({"key":"...","endpoint_id":"...","base_url":"..."}) so the endpoint
// survives a restart and travels with the key it belongs to.
func (k *keyringStore) StoreCredential(providerID string, c Credential) error {
	if strings.TrimSpace(c.Key) == "" {
		return errors.New("llm keystore: refusing to store an empty credential")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("llm keystore: encode credential for %s: %w", providerID, err)
	}
	if err := k.ring.Set(keyring.Item{
		Key:         k.itemKey(providerID),
		Data:        data,
		Label:       "dark-memory-mcp LLM key (" + providerID + ")",
		Description: "dark-memory-mcp LLM-as-judge API key + endpoint for provider " + providerID,
	}); err != nil {
		return fmt.Errorf("llm keystore: keyring set credential: %w", err)
	}
	return nil
}

// LoadCredential returns the stored credential. Legacy items (raw key,
// no JSON) are tolerated: they yield the key with an empty endpoint.
// A missing item (or an unreadable vault) yields (Credential{}, false).
func (k *keyringStore) LoadCredential(providerID string) (Credential, bool) {
	item, err := k.ring.Get(k.itemKey(providerID))
	if err != nil {
		return Credential{}, false
	}
	return decodeCredential(item.Data), true
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
func EnvStore(envKeyResolver func(providerID string) string) Store {
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

// StoreCredential is a no-op by design: the environment is owned by the
// harness, not by dark-memory (same rule as Set, which errors out
// instead of writing). The env carries the KEY only — never the
// endpoint it was minted for — so there is nothing to persist. The
// call reports success (the key IS stored, in the env) and the
// endpoint is resolved at read time by the caller.
//
// The consequence is documented on purpose: an env-backed provider
// always loads with an empty endpoint (see LoadCredential), so the
// endpoint must be re-resolved for that credential.
func (e *envStore) StoreCredential(providerID string, c Credential) error {
	return nil
}

// LoadCredential returns the env-provided key with an EMPTY endpoint:
// the env var carries the key only. The bool is false when the env var
// is unset or empty.
func (e *envStore) LoadCredential(providerID string) (Credential, bool) {
	v := os.Getenv(e.keyName(providerID))
	if v == "" {
		return Credential{}, false
	}
	return Credential{Key: v}, true
}

// readOnlyCredentials marks the env backend as unable to persist a
// credential record (the environment carries the key only).
func (e *envStore) readOnlyCredentials() bool { return true }

// Composite chains backends in order: reads consult each until one
// hits; writes go to the first backend only.
type Composite struct {
	backends []KeyStore
}

// NewComposite builds a chain. Read order = argument order.
func NewComposite(backends ...KeyStore) Store {
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

// readOnlyCredentialStore marks backends whose StoreCredential is a
// no-op (the env backend: the environment is owned by the harness and
// cannot carry an endpoint). The composite skips them when writing so
// a credential lands in the keyring instead of vanishing.
type readOnlyCredentialStore interface {
	readOnlyCredentials() bool
}

// StoreCredential persists the credential in the first backend that can
// actually persist it — same write order as Set (the keyring normally
// comes first, the env is read-only and last). Read-only backends are
// skipped so the credential lands in the keyring.
func (c *Composite) StoreCredential(providerID string, cred Credential) error {
	if len(c.backends) == 0 {
		return errors.New("llm keystore: composite has no backends")
	}
	var fallback CredentialStore
	for _, b := range c.backends {
		cs, ok := b.(CredentialStore)
		if !ok {
			continue
		}
		if fallback == nil {
			fallback = cs
		}
		if ro, ok := b.(readOnlyCredentialStore); ok && ro.readOnlyCredentials() {
			continue
		}
		return cs.StoreCredential(providerID, cred)
	}
	if fallback != nil {
		// Only read-only backends: report the key as stored (the env
		// carries the key; the endpoint is resolved at read time).
		return fallback.StoreCredential(providerID, cred)
	}
	return errors.New("llm keystore: no backend can store a credential record")
}

// LoadCredential reads the credential from the first backend that has
// one: keyring first, env second (same order as Get).
func (c *Composite) LoadCredential(providerID string) (Credential, bool) {
	for _, b := range c.backends {
		cs, ok := b.(CredentialStore)
		if !ok {
			continue
		}
		if cred, found := cs.LoadCredential(providerID); found {
			return cred, true
		}
	}
	return Credential{}, false
}

// MemoryStore is an in-memory KeyStore for tests and one-shot tool
// invocations. NOT production-safe (no encryption, no persistence).
type MemoryStore struct {
	mu    sync.RWMutex
	keys  map[string]string
	creds map[string]Credential
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{keys: map[string]string{}, creds: map[string]Credential{}}
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

// Set stores the key for a provider id. Re-setting the SAME key keeps
// the endpoint that was resolved for it (the endpoint belongs to the
// key); a DIFFERENT key drops it.
func (m *MemoryStore) Set(providerID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[providerID] = key
	if cur, ok := m.creds[providerID]; ok && cur.Key == key {
		return nil // same key → keep the resolved endpoint
	}
	delete(m.creds, providerID)
	return nil
}

func (m *MemoryStore) Delete(providerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, providerID)
	delete(m.creds, providerID)
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

// StoreCredential persists the key+endpoint pair in memory.
func (m *MemoryStore) StoreCredential(providerID string, c Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creds[providerID] = c
	m.keys[providerID] = c.Key
	return nil
}

// LoadCredential returns the stored credential. A key stored without a
// credential record (legacy shape) is returned with an empty endpoint.
func (m *MemoryStore) LoadCredential(providerID string) (Credential, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c, ok := m.creds[providerID]; ok {
		return c, true
	}
	if k, ok := m.keys[providerID]; ok {
		return Credential{Key: k}, true
	}
	return Credential{}, false
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
