// Package oauth — keystore.go: cross-platform OS keyring wrapper for
// OAuth tokens. Uses github.com/99designs/keyring v1.2.2 (already in
// go.mod). On keyring failure, falls back to env vars for headless
// deployments.
//
// Storage layout:
//
//   keyring item name : "oauth:<provider>:<kind>"
//     kind = "access"  → TokenSet.AccessToken
//     kind = "refresh" → TokenSet.RefreshToken
//     kind = "meta"    → JSON-encoded {expires_at, scopes, account_email}
//
// Env-var fallback (only when keyring returns ErrNoAvailImpl):
//
//   DARK_OAUTH_<PROVIDER>_ACCESS_TOKEN  → TokenSet.AccessToken
//   DARK_OAUTH_<PROVIDER>_REFRESH_TOKEN → TokenSet.RefreshToken
//   DARK_OAUTH_<PROVIDER>_EXPIRES_AT    → RFC3339 timestamp
//
// Operators MUST NOT set env vars with secrets in plaintext for
// production deployments. The fallback exists for CI runners and
// containers without a keyring service.
package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/99designs/keyring"
)

// keyringService is the service name used for all OAuth entries.
// Keep stable — changing it invalidates existing entries on disk.
const keyringService = "dark-memory-mcp/oauth"

// Keystore persists OAuth tokens securely. Thread-safe.
type Keystore struct {
	mu     sync.RWMutex
	ring   keyring.Keyring
	config KeyringConfig
}

// KeyringConfig tunes Keystore behavior.
type KeyringConfig struct {
	// Service overrides the default service name. Useful for tests
	// that want isolation between runs.
	Service string
	// AllowedBackends restricts which keyring backends Keystore will
	// use. Empty = OS default (keyring tries each available backend
	// in order until one works). Tests typically pass []BackendType{
	// keyring.SecretServiceBackend } or similar to force a specific one.
	AllowedBackends []keyring.BackendType
	// FileDir forces the "file" backend at the given directory. Used
	// by tests for an in-memory equivalent; production should leave
	// this empty so the OS default backend is selected.
	FileDir string
}

// OpenKeystore creates a Keystore backed by the OS keyring. Returns a
// usable Keystore (possibly in env-var fallback mode) or an error
// only if the backend cannot be initialized at all.
func OpenKeystore(cfg KeyringConfig) (*Keystore, error) {
	if cfg.Service == "" {
		cfg.Service = keyringService
	}
	keyringCfg := keyring.Config{
		ServiceName:     cfg.Service,
		AllowedBackends: cfg.AllowedBackends,
		FileDir:         cfg.FileDir,
	}
	ring, err := keyring.Open(keyringCfg)
	if err != nil {
		// Don't fail hard — return a keystore that operates in
		// env-var fallback mode. This keeps dark-memory usable in
		// headless containers where the keyring is unavailable.
		return &Keystore{config: cfg}, nil
	}
	return &Keystore{ring: ring, config: cfg}, nil
}

// FileKeystore creates an in-process Keystore backed by a JSON file.
// Used by tests; production callers should use OpenKeystore which
// dispatches to the OS keyring.
func FileKeystore(dir string) (*Keystore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("oauth: create file keystore dir: %w", err)
	}
	ring, err := keyring.Open(keyring.Config{
		ServiceName: keyringService,
		AllowedBackends: []keyring.BackendType{
			"file", // plain-text JSON file, only for tests
		},
		FileDir: dir,
	})
	if err != nil {
		return nil, fmt.Errorf("oauth: open file keystore: %w", err)
	}
	return &Keystore{ring: ring, config: KeyringConfig{Service: keyringService, FileDir: dir}}, nil
}

// ArrayKeystore creates an in-memory Keystore backed by an in-process
// array. Used by tests that need full isolation (no disk I/O).
func ArrayKeystore() *Keystore {
	ring := keyring.NewArrayKeyring(nil)
	return &Keystore{ring: ring, config: KeyringConfig{Service: keyringService}}
}

// keyName returns the canonical keyring item name for a (provider, kind).
func (k *Keystore) keyName(provider Provider, kind string) string {
	return fmt.Sprintf("oauth:%s:%s", strings.ToLower(string(provider)), kind)
}

// SaveToken persists a TokenSet for the given provider. AccessToken,
// RefreshToken (if non-empty), and metadata (expires_at, scopes,
// account_email) are stored as separate keyring items so refresh can
// happen without rewriting the full set.
func (k *Keystore) SaveToken(provider Provider, t TokenSet) error {
	if provider == ProviderNone {
		return errors.New("oauth: SaveToken: provider required")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ring == nil {
		// env-var fallback — best effort. We don't write env vars
		// (read-only), so this is a no-op in fallback mode. Production
		// users running in headless should configure env vars before
		// boot.
		return nil
	}
	accessKey := keyring.Item{
		Key:         k.keyName(provider, "access"),
		Data:        []byte(t.AccessToken),
		Description: fmt.Sprintf("dark-memory OAuth access token for %s", provider),
	}
	if err := k.ring.Set(accessKey); err != nil {
		return fmt.Errorf("oauth: save access token: %w", err)
	}
	if t.RefreshToken != "" {
		refreshKey := keyring.Item{
			Key:         k.keyName(provider, "refresh"),
			Data:        []byte(t.RefreshToken),
			Description: fmt.Sprintf("dark-memory OAuth refresh token for %s", provider),
		}
		if err := k.ring.Set(refreshKey); err != nil {
			return fmt.Errorf("oauth: save refresh token: %w", err)
		}
	}
	metaJSON, err := json.Marshal(struct {
		ExpiresAt    time.Time `json:"expires_at"`
		Scopes       []string  `json:"scopes,omitempty"`
		AccountEmail string    `json:"account_email,omitempty"`
	}{
		ExpiresAt:    t.ExpiresAt,
		Scopes:       t.Scopes,
		AccountEmail: t.AccountEmail,
	})
	if err != nil {
		return fmt.Errorf("oauth: marshal token meta: %w", err)
	}
	metaKey := keyring.Item{
		Key:         k.keyName(provider, "meta"),
		Data:        metaJSON,
		Description: fmt.Sprintf("dark-memory OAuth metadata for %s", provider),
	}
	if err := k.ring.Set(metaKey); err != nil {
		return fmt.Errorf("oauth: save token meta: %w", err)
	}
	return nil
}

// LoadToken reads a TokenSet for the given provider. Returns
// (TokenSet{}, false, nil) if no tokens are stored. Falls back to
// env vars when the keyring is unavailable.
func (k *Keystore) LoadToken(provider Provider) (TokenSet, bool, error) {
	if provider == ProviderNone {
		return TokenSet{}, false, errors.New("oauth: LoadToken: provider required")
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.ring == nil {
		return k.loadFromEnv(provider)
	}
	access, err := k.ring.Get(k.keyName(provider, "access"))
	if err != nil {
		if errors.Is(err, keyring.ErrKeyNotFound) {
			return k.loadFromEnv(provider)
		}
		return TokenSet{}, false, fmt.Errorf("oauth: load access token: %w", err)
	}
	ts := TokenSet{AccessToken: string(access.Data)}
	if refresh, err := k.ring.Get(k.keyName(provider, "refresh")); err == nil {
		ts.RefreshToken = string(refresh.Data)
	}
	if meta, err := k.ring.Get(k.keyName(provider, "meta")); err == nil {
		var m struct {
			ExpiresAt    time.Time `json:"expires_at"`
			Scopes       []string  `json:"scopes,omitempty"`
			AccountEmail string    `json:"account_email,omitempty"`
		}
		if err := json.Unmarshal(meta.Data, &m); err == nil {
			ts.ExpiresAt = m.ExpiresAt
			ts.Scopes = m.Scopes
			ts.AccountEmail = m.AccountEmail
		}
	}
	return ts, true, nil
}

// DeleteToken removes all keyring entries for the given provider.
func (k *Keystore) DeleteToken(provider Provider) error {
	if provider == ProviderNone {
		return errors.New("oauth: DeleteToken: provider required")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ring == nil {
		return nil
	}
	for _, kind := range []string{"access", "refresh", "meta"} {
		if err := k.ring.Remove(k.keyName(provider, kind)); err != nil {
			if errors.Is(err, keyring.ErrKeyNotFound) {
				continue
			}
			return fmt.Errorf("oauth: delete %s: %w", kind, err)
		}
	}
	return nil
}

// ListProviders returns every provider that has tokens stored.
func (k *Keystore) ListProviders() ([]Provider, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.ring == nil {
		// Fallback: env vars only.
		var providers []Provider
		for _, p := range AllProviders() {
			prefix := "DARK_OAUTH_" + strings.ToUpper(strings.ReplaceAll(string(p), "-", "_"))
			if os.Getenv(prefix+"_ACCESS_TOKEN") != "" {
				providers = append(providers, p)
			}
		}
		return providers, nil
	}
	keys, err := k.ring.Keys()
	if err != nil {
		return nil, fmt.Errorf("oauth: list keys: %w", err)
	}
	seen := map[Provider]bool{}
	var providers []Provider
	for _, k := range keys {
		// k format: "oauth:<provider>:access" — extract middle segment.
		parts := strings.Split(k, ":")
		if len(parts) >= 3 && parts[0] == "oauth" {
			p := Provider(strings.ToLower(parts[1]))
			if !seen[p] {
				seen[p] = true
				providers = append(providers, p)
			}
		}
	}
	return providers, nil
}

// loadFromEnv reads OAuth tokens from environment variables. Used
// when the OS keyring is unavailable (containers, SSH sessions).
func (k *Keystore) loadFromEnv(provider Provider) (TokenSet, bool, error) {
	prefix := "DARK_OAUTH_" + strings.ToUpper(strings.ReplaceAll(string(provider), "-", "_"))
	access := os.Getenv(prefix + "_ACCESS_TOKEN")
	if access == "" {
		return TokenSet{}, false, nil
	}
	ts := TokenSet{AccessToken: access}
	if r := os.Getenv(prefix + "_REFRESH_TOKEN"); r != "" {
		ts.RefreshToken = r
	}
	if e := os.Getenv(prefix + "_EXPIRES_AT"); e != "" {
		if t, err := time.Parse(time.RFC3339, e); err == nil {
			ts.ExpiresAt = t
		}
	}
	return ts, true, nil
}