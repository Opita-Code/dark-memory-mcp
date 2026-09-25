package manifest

import (
	"errors"
	"testing"
	"time"
)

// helper: build a valid token for tests.
func validCap() *CapToken {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	return &CapToken{
		ID:        "cap-1",
		Operator:  "operator-nico",
		Scopes:    []string{"manifest:read", "manifest:write"},
		GrantedAt: now,
		ExpiresAt: now.Add(1 * time.Hour),
		Signature: []byte("sig-bytes-32-bytes-aaaaaaaaaaa"),
		SignedBy:  "admin-opita",
	}
}

func TestExample_CapToken_ValidateAcceptsValid(t *testing.T) {
	c := validCap()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: expected nil, got %v", err)
	}
}

func TestExample_CapToken_ValidateRejectsEmptyID(t *testing.T) {
	c := validCap()
	c.ID = ""
	err := c.Validate()
	if !errors.Is(err, ErrEmptyCapID) {
		t.Fatalf("Validate: expected ErrEmptyCapID, got %v", err)
	}
}

func TestExample_CapToken_ValidateRejectsEmptyOperator(t *testing.T) {
	c := validCap()
	c.Operator = ""
	err := c.Validate()
	if !errors.Is(err, ErrEmptyCapOperator) {
		t.Fatalf("Validate: expected ErrEmptyCapOperator, got %v", err)
	}
}

func TestExample_CapToken_ValidateRejectsEmptyScopes(t *testing.T) {
	c := validCap()
	c.Scopes = nil
	err := c.Validate()
	if !errors.Is(err, ErrEmptyCapScopes) {
		t.Fatalf("Validate: expected ErrEmptyCapScopes, got %v", err)
	}

	c.Scopes = []string{}
	err = c.Validate()
	if !errors.Is(err, ErrEmptyCapScopes) {
		t.Fatalf("Validate (empty slice): expected ErrEmptyCapScopes, got %v", err)
	}
}

func TestExample_CapToken_ValidateRejectsEqualExpiry(t *testing.T) {
	c := validCap()
	c.ExpiresAt = c.GrantedAt
	err := c.Validate()
	if !errors.Is(err, ErrBadCapExpiry) {
		t.Fatalf("Validate (equal expiry): expected ErrBadCapExpiry, got %v", err)
	}
}

func TestExample_CapToken_ValidateRejectsInvertedExpiry(t *testing.T) {
	c := validCap()
	c.ExpiresAt = c.GrantedAt.Add(-1 * time.Hour)
	err := c.Validate()
	if !errors.Is(err, ErrBadCapExpiry) {
		t.Fatalf("Validate (inverted expiry): expected ErrBadCapExpiry, got %v", err)
	}
}

func TestExample_CapToken_ValidateRejectsEmptySignature(t *testing.T) {
	c := validCap()
	c.Signature = nil
	err := c.Validate()
	if !errors.Is(err, ErrEmptyCapSignature) {
		t.Fatalf("Validate: expected ErrEmptyCapSignature, got %v", err)
	}

	c.Signature = []byte{}
	if !errors.Is(c.Validate(), ErrEmptyCapSignature) {
		t.Fatalf("Validate (empty slice sig): expected ErrEmptyCapSignature, got %v", c.Validate())
	}
}

func TestExample_CapToken_ValidateRejectsMalformedScope(t *testing.T) {
	cases := []struct {
		name  string
		scope string
	}{
		{"uppercase", "Manifest:read"},
		{"empty resource", ":read"},
		{"empty action", "manifest:"},
		{"extra colon", "manifest:read:more"},
		{"space", "manifest: read"},
		{"dash", "manifest-read"},
		{"dot", "manifest.read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCap()
			c.Scopes = []string{tc.scope}
			err := c.Validate()
			if !errors.Is(err, ErrEmptyCapScopes) {
				t.Fatalf("Validate(%q): expected ErrEmptyCapScopes, got %v", tc.scope, err)
			}
		})
	}
}

func TestExample_CapToken_ValidateRejectsNil(t *testing.T) {
	var c *CapToken
	err := c.Validate()
	if !errors.Is(err, ErrEmptyCapID) {
		t.Fatalf("Validate(nil): expected ErrEmptyCapID, got %v", err)
	}
}

func TestExample_CapToken_HasScopeFindsPresentScope(t *testing.T) {
	c := validCap()
	if !c.HasScope("manifest:read") {
		t.Fatal("HasScope(manifest:read): expected true")
	}
	if !c.HasScope("manifest:write") {
		t.Fatal("HasScope(manifest:write): expected true")
	}
}

func TestExample_CapToken_HasScopeRejectsAbsentScope(t *testing.T) {
	c := validCap()
	if c.HasScope("drift:resolve") {
		t.Fatal("HasScope(drift:resolve): expected false")
	}
}

func TestExample_CapToken_HasScopeHandlesEdgeCases(t *testing.T) {
	var c *CapToken
	if c.HasScope("anything") {
		t.Fatal("HasScope(nil): expected false")
	}
	tok := validCap()
	if tok.HasScope("") {
		t.Fatal("HasScope(empty): expected false")
	}
	// duplicates: still true
	tok.Scopes = []string{"manifest:read", "manifest:read"}
	if !tok.HasScope("manifest:read") {
		t.Fatal("HasScope with duplicates: expected true")
	}
}

func TestExample_CapToken_IsActiveBoundaries(t *testing.T) {
	c := validCap()
	now := c.GrantedAt
	if !c.IsActive(now) {
		t.Fatal("IsActive at GrantedAt: expected true")
	}
	if !c.IsActive(now.Add(30 * time.Minute)) {
		t.Fatal("IsActive mid-window: expected true")
	}
	// ExpiresAt is exclusive end.
	if c.IsActive(c.ExpiresAt) {
		t.Fatal("IsActive at ExpiresAt (exclusive end): expected false")
	}
	if c.IsActive(c.ExpiresAt.Add(1 * time.Second)) {
		t.Fatal("IsActive after ExpiresAt: expected false")
	}
}

func TestExample_CapToken_IsActiveRevokedTakesPrecedence(t *testing.T) {
	c := validCap()
	c.RevokedAt = c.GrantedAt.Add(5 * time.Minute)
	// before revoke: active
	if !c.IsActive(c.GrantedAt.Add(1 * time.Minute)) {
		t.Fatal("IsActive before revoke: expected true")
	}
	// at and after revoke: not active
	if c.IsActive(c.RevokedAt) {
		t.Fatal("IsActive at RevokedAt: expected false")
	}
	if c.IsActive(c.RevokedAt.Add(1 * time.Second)) {
		t.Fatal("IsActive after RevokedAt: expected false")
	}
}

func TestExample_CapToken_IsActiveNil(t *testing.T) {
	var c *CapToken
	if c.IsActive(time.Now()) {
		t.Fatal("IsActive(nil): expected false")
	}
}

func TestExample_IsValidScopeName_AcceptsCanonical(t *testing.T) {
	cases := []string{
		"manifest:read",
		"manifest:write",
		"drift:resolve",
		"agent_memory:write",
		"agent_memory:read",
		"research:web",
		"a:b", // minimal length
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if !IsValidScopeName(s) {
				t.Fatalf("IsValidScopeName(%q): expected true", s)
			}
		})
	}
}

func TestExample_IsValidScopeName_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		":",
		"manifest",
		":read",
		"manifest:",
		"manifest:read:more",
		"Manifest:read",
		"manifest:Read",
		"manifest:read ",
		"manifest: read",
		"-:read",
		"manifest:-",
		"a:",                 // resource too short for 1 char + colon + empty action
		"a:b:c:d:e:f:g:h:i", // way too long
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:b", // resource > 32 chars
		"a:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", // action > 32 chars
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			if IsValidScopeName(s) {
				t.Fatalf("IsValidScopeName(%q): expected false", s)
			}
		})
	}
}
