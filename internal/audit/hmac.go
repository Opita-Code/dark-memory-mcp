// Package audit — hmac.go: HMAC-SHA256 chain primitives for the audit
// JSONL stream (ADR-016 transparency log + ADR-018 audit verify).
//
// Design (LUCIDEZ R3 — see docs/v4-alpha-11-plan §8.5):
//
//  1. Each JSONL row carries two extra fields beyond the WriteEvent
//     payload: hmac_prev (the previous row's hmac_self, "" for genesis)
//     and hmac_self (the HMAC-SHA256 of the current row's canonical
//     bytes, keyed on hmac_prev).
//
//  2. canonical_bytes is the deterministic encoding/json marshaled
//     bytes of the WriteEvent struct with hmac_prev/hmac_self/canary_present
//     zeroed — the HMAC must NOT cover itself. Go's encoding/json
//     marshals in struct field order, which is fixed for our struct
//     definition; operators on both ends use the same struct, so the
//     bytes match.
//
//  3. HMAC-SHA256(key, prev_hmac || canonical_bytes) — the previous
//     HMAC is prefixed so any tampering with prior rows invalidates
//     every subsequent row's HMAC (chain integrity).
//
//  4. hmac_key_id labels which secret was active when the HMAC was
//     computed. The verifier holds a keychain (id → secret) and looks
//     up by hmac_key_id. This is the rotation handle: a new key id is
//     added to the chain when ops rotates; the verifier tries the
//     declared id and falls back to known keys if needed.
//
//  5. The HMAC key source is an env var DARK_AUDIT_HMAC_KEY (with a
//     fixed-format value like "v1:<hex-secret>") so the verifier and
//     exporter agree without sharing via DB columns. In dev/test the
//     caller may inject a fixed key (WithKey option) for hermetic tests.
package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// HMACKeyIDLength is the canonical format length for the version prefix
// of an HMAC secret (e.g. "v1:<64-hex-char>").
const HMACKeyIDLength = 2 // "v1", "v2", etc.

// MaxHMACHexLen is the canonical encoded length of an HMAC-SHA256 in hex.
const MaxHMACHexLen = sha256.Size * 2 // 64 hex chars

// Keyring holds the active HMAC secrets. Multiple keys may coexist for
// rotation: a key id added on v1 is the canonical "primary", v2 is a
// newer key that future rows will use. The verifier tries the row's
// declared hmac_key_id first, then walks the rest as fallback.
type Keyring struct {
	keys map[string][]byte // id -> raw secret bytes
}

// NewKeyring constructs a Keyring from a list of "id:hexsecret" pairs.
// Use KeyringFromEnv() for the env-var canonical path.
func NewKeyring(pairs ...string) (*Keyring, error) {
	k := &Keyring{keys: map[string][]byte{}}
	for _, p := range pairs {
		id, secret, err := parseKeyPair(p)
		if err != nil {
			return nil, fmt.Errorf("audit: NewKeyring: %w", err)
		}
		k.keys[id] = secret
	}
	if len(k.keys) == 0 {
		return nil, fmt.Errorf("audit: NewKeyring: no keys provided")
	}
	return k, nil
}

// KeyringFromEnv reads DARK_AUDIT_HMAC_KEY. Format is a comma-separated
// list of "id:hexsecret" pairs (the same as NewKeyring). When the env
// var is unset, returns an empty keyring (operator can still inject one
// for tests via WithKey).
func KeyringFromEnv(env string) (*Keyring, error) {
	raw := strings.TrimSpace(env)
	if raw == "" {
		return &Keyring{keys: map[string][]byte{}}, nil
	}
	return NewKeyring(strings.Split(raw, ",")...)
}

// Add installs (or replaces) a key by id. Useful for tests + rotation.
func (k *Keyring) Add(id string, secret []byte) {
	k.keys[id] = secret
}

// Primary returns the first-iterated key id. The order is not guaranteed
// (Go map iteration); callers needing stable order should pass the
// desired id explicitly via Get(id).
func (k *Keyring) Primary() (string, []byte, bool) {
	for id, secret := range k.keys {
		return id, secret, true
	}
	return "", nil, false
}

// Get looks up a key by id.
func (k *Keyring) Get(id string) ([]byte, bool) {
	secret, ok := k.keys[id]
	return secret, ok
}

// IDs returns all known key ids (unsorted — Go map iteration).
func (k *Keyring) IDs() []string {
	out := make([]string, 0, len(k.keys))
	for id := range k.keys {
		out = append(out, id)
	}
	return out
}

// CanonicalBytes returns the deterministic JSON encoding of ev with the
// hmac_prev/hmac_self fields cleared. This is the byte string that gets
// HMAC'd. Two encodings (exporter + verifier) of the same logical row
// MUST produce identical bytes.
func CanonicalBytes(ev WriteEvent) ([]byte, error) {
	// Zero the chain fields so they're not covered by the HMAC.
	ev.ChainPrev = ""
	ev.ChainSelf = ""
	ev.ChainKeyID = ""
	return json.Marshal(ev)
}

// ComputeChain returns (chainPrev, chainSelf) for one row given the
// previous row's chainSelf. For genesis (the very first row in a chain),
// pass chainPrevPrev = "".
//
// chainSelf = HMAC-SHA256(secret, chainPrev || canonical_bytes)
func ComputeChain(secret []byte, chainPrevPrev string, ev WriteEvent) (chainPrev string, chainSelf string, err error) {
	if len(secret) == 0 {
		return "", "", errors.New("audit: ComputeChain: empty secret")
	}
	canonical, err := CanonicalBytes(ev)
	if err != nil {
		return "", "", fmt.Errorf("audit: ComputeChain: marshal: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(chainPrevPrev))
	mac.Write(canonical)
	sum := mac.Sum(nil)
	return chainPrevPrev, hex.EncodeToString(sum), nil
}

// VerifyChain recomputes the HMAC of ev given the previous row's
// chainSelf. Returns (ok bool, err error). When err is non-nil, ok is
// always false and the operator should treat the chain as broken.
//
// failClosed is true for production (return error on any mismatch);
// false for tests that want to compare without aborting.
func VerifyChain(secret []byte, prevChainSelf string, ev WriteEvent) (bool, error) {
	if len(secret) == 0 {
		return false, errors.New("audit: VerifyChain: empty secret")
	}
	canonical, err := CanonicalBytes(ev)
	if err != nil {
		return false, fmt.Errorf("audit: VerifyChain: marshal: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(prevChainSelf))
	mac.Write(canonical)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(ev.ChainSelf)) {
		return false, nil
	}
	return true, nil
}

// parseKeyPair parses "id:hexsecret" into (id, secret_bytes). The id is
// the version label (e.g. "v1"); secret_bytes is the hex-decoded secret.
func parseKeyPair(s string) (string, []byte, error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", nil, fmt.Errorf("missing ':' separator in %q", redactKey(s))
	}
	id := strings.TrimSpace(s[:idx])
	hexPart := strings.TrimSpace(s[idx+1:])
	if id == "" {
		return "", nil, fmt.Errorf("empty key id in %q", redactKey(s))
	}
	secret, err := hex.DecodeString(hexPart)
	if err != nil {
		return "", nil, fmt.Errorf("hex decode failed for key id %q: %w", id, err)
	}
	if len(secret) < 16 {
		return "", nil, fmt.Errorf("key id %q too short (%d bytes, min 16)", id, len(secret))
	}
	return id, secret, nil
}

// redactKey masks the secret portion of a "id:hexsecret" string for
// safe logging. Id is preserved (so operators can see WHICH key failed)
// but the hex is replaced with "***".
func redactKey(s string) string {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "***"
	}
	return s[:idx+1] + "***"
}

// FormatKey returns the canonical "id:hexsecret" encoding of a secret
// for use in env vars + persisted logs.
func FormatKey(id string, secret []byte) string {
	return id + ":" + hex.EncodeToString(secret)
}

// FormatKeyFromInt is a convenience for test/dev: builds "v1:<hex>"
// from a stable seed integer (so tests can construct deterministic
// keys without rng).
func FormatKeyFromInt(seed int) string {
	id := "v" + strconv.Itoa(1)
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte((seed + i) % 256)
	}
	return FormatKey(id, secret)
}