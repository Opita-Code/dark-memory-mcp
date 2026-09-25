// Package manifest provides Tier-1 evidence primitives for dark-memory
// v4.0: capability tokens (cap_token) and a manifest registry.
//
// The cap_token layer answers "is this operator allowed to do X right now?".
// The manifest layer answers "which artifacts have we ever committed to,
// and who signed for each one?". Together they make artifact publication
// (from internal/v4alpha/vibe) auditable and replay-safe.
//
// This file defines the CapToken type and its invariants. The persistence
// layer lives in cap_store.go (cycle 2) and signing/verification lives in
// sign.go (cycle 4).
package manifest

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for CapToken.Validate. All callers should use errors.Is.
var (
	// ErrEmptyCapID is returned when CapToken.ID is the empty string.
	ErrEmptyCapID = errors.New("manifest: cap token id is empty")

	// ErrEmptyCapOperator is returned when CapToken.Operator is empty.
	ErrEmptyCapOperator = errors.New("manifest: cap token operator is empty")

	// ErrEmptyCapScopes is returned when CapToken.Scopes is empty or nil.
	ErrEmptyCapScopes = errors.New("manifest: cap token has no scopes")

	// ErrBadCapExpiry is returned when ExpiresAt is not strictly after GrantedAt.
	ErrBadCapExpiry = errors.New("manifest: cap token expires_at must be after granted_at")

	// ErrEmptyCapSignature is returned when Signature bytes are empty.
	ErrEmptyCapSignature = errors.New("manifest: cap token signature is empty")
)

// CapToken is the canonical Tier-1 cap token. It is granted by an operator
// to another operator (or to itself) for a bounded set of scopes and a
// bounded time window. Once granted it is immutable; revocation creates a
// new audit row but does not delete the token.
//
// INV-12: every cap_token emitted MUST carry an Ed25519 signature
// (Signature field). The SignedBy field identifies which operator's
// private key produced the signature; this may differ from the operator
// who will use the token.
//
// INV-13: revocation does NOT delete the token. Revoked tokens stay in
// the store so audit can prove when a capability was withdrawn.
//
// Scopes follow the pattern "<resource>:<action>", e.g.
// "manifest:read", "drift:resolve", "agent_memory:write". The package
// does not interpret scope strings — callers decide what each scope
// means. IsValidScopeName in this file enforces the syntactic shape.
type CapToken struct {
	// ID is the unique token id (uuid, ulid, or any collision-free string).
	ID string

	// Operator is the human or agent identity that will use the token.
	Operator string

	// Scopes is the set of capability strings granted to Operator.
	// Order does not matter for HasScope; duplicate entries are allowed
	// but discouraged (HasScope returns true either way).
	Scopes []string

	// GrantedAt is the moment the token became (or will become) active.
	GrantedAt time.Time

	// ExpiresAt is the moment the token stops being valid. Must be
	// strictly after GrantedAt per Validate.
	ExpiresAt time.Time

	// RevokedAt is the moment the token was withdrawn. The zero value
	// means "not revoked".
	RevokedAt time.Time

	// Signature is the Ed25519 signature over the token's canonical form.
	// CanonicalBytes in sign.go covers Entry, not CapToken; the CapToken
	// canonical form is a future slice. Verify separately via CapStore
	// + Verify once the form lands.
	Signature []byte

	// SignedBy is the operator identity whose private key produced
	// Signature. May differ from Operator (e.g. admin grants to a sub-agent).
	SignedBy string
}

// Validate enforces the structural invariants of a CapToken. It does NOT
// verify the Ed25519 signature (no CapToken verifier exists yet; Entry
// verification lives in Verify/VerifyContent in sign.go) and it does NOT
// check whether the token is currently active (use CapStore.Check).
//
// Returns nil if every field is well-formed, otherwise an error wrapping
// the matching sentinel. Callers should use errors.Is to discriminate.
func (c *CapToken) Validate() error {
	if c == nil {
		return fmt.Errorf("manifest: %w", ErrEmptyCapID)
	}
	if c.ID == "" {
		return fmt.Errorf("manifest: %w", ErrEmptyCapID)
	}
	if c.Operator == "" {
		return fmt.Errorf("manifest: %w", ErrEmptyCapOperator)
	}
	if len(c.Scopes) == 0 {
		return fmt.Errorf("manifest: %w", ErrEmptyCapScopes)
	}
	for i, s := range c.Scopes {
		if !IsValidScopeName(s) {
			return fmt.Errorf("manifest: scope[%d]=%q invalid: %w", i, s, ErrEmptyCapScopes)
		}
	}
	if !c.ExpiresAt.After(c.GrantedAt) {
		return fmt.Errorf("manifest: %w", ErrBadCapExpiry)
	}
	if len(c.Signature) == 0 {
		return fmt.Errorf("manifest: %w", ErrEmptyCapSignature)
	}
	return nil
}

// HasScope reports whether the token grants the named scope. Linear scan
// over Scopes — fine because real cap tokens have a small handful of scopes
// (typically 1-5). Returns false if c is nil or Scopes is empty.
func (c *CapToken) HasScope(scope string) bool {
	if c == nil || scope == "" {
		return false
	}
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// IsActive reports whether the token is currently active at the given
// moment: not revoked, not expired. The caller passes the "now" time so
// this method is deterministic and testable.
//
// A token with ExpiresAt exactly equal to t is considered NOT active
// (ExpiresAt is the exclusive end of the validity window).
func (c *CapToken) IsActive(now time.Time) bool {
	if c == nil {
		return false
	}
	if !c.RevokedAt.IsZero() && !c.RevokedAt.After(now) {
		return false
	}
	if !c.ExpiresAt.After(now) {
		return false
	}
	return true
}

// IsValidScopeName checks the syntactic shape of a scope string.
// Format: "<resource>:<action>" where resource and action are lowercase
// alphanumeric + underscore, 1-32 chars each. Examples:
//   - "manifest:read"        ok
//   - "drift:resolve"        ok
//   - "agent_memory:write"   ok
//   - "Manifest:Read"        bad (uppercase)
//   - ":read"                bad (empty resource)
//   - "manifest:"            bad (empty action)
//   - "manifest:read:more"   bad (extra colon)
//
// This function is intentionally permissive about resource names so new
// resources can be added without code changes. It rejects obviously
// malformed strings so callers can trust the shape.
func IsValidScopeName(s string) bool {
	if len(s) < 3 || len(s) > 66 {
		return false
	}
	colon := -1
	for i, r := range s {
		if r == ':' {
			if colon != -1 {
				return false
			}
			colon = i
			continue
		}
		if colon == -1 {
			if !isScopeRune(r) {
				return false
			}
			if i >= 32 {
				return false
			}
		} else {
			if !isScopeRune(r) {
				return false
			}
			if i-colon-1 >= 32 {
				return false
			}
		}
	}
	if colon <= 0 || colon == len(s)-1 {
		return false
	}
	return true
}

func isScopeRune(r rune) bool {
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	if r == '_' {
		return true
	}
	return false
}
