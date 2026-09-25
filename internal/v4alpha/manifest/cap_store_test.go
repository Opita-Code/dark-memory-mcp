package manifest

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// helpers_test.go (private to manifest_test.go siblings) — only the
// helpers this file needs to keep test coupling tight.

const inMemoryDSN = "file:cap_test?mode=memory&cache=shared"

func newTestCapDB(t *testing.T) *sql.DB {
	t.Helper()
	// Use a unique file-based DSN per test to keep tests parallel-safe.
	// ":memory:" would race across goroutines via the shared cache.
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dsn+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := CreateCapSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateCapSchema: %v", err)
	}
	return db
}

func mustCap(t *testing.T, mutators ...func(*CapToken)) *CapToken {
	t.Helper()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	c := &CapToken{
		ID:        "cap-" + t.Name(),
		Operator:  "operator-nico",
		Scopes:    []string{"manifest:read", "manifest:write"},
		GrantedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
		Signature: []byte("signature-bytes-must-be-non-empty"),
		SignedBy:  "admin-opita",
	}
	for _, fn := range mutators {
		fn(c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("mustCap: produced invalid token: %v", err)
	}
	return c
}

// TestExample_CapStore_CreateCapSchemaIdempotent confirms running the
// DDL twice doesn't error (the schema is IF NOT EXISTS).
func TestExample_CapStore_CreateCapSchemaIdempotent(t *testing.T) {
	db := newTestCapDB(t)
	ctx := context.Background()
	if err := CreateCapSchema(ctx, db); err != nil {
		t.Fatalf("second CreateCapSchema: %v", err)
	}
}

// TestExample_CapStore_GrantInsertRoundTrip inserts a token, reads it
// back, and confirms every field survives the round-trip.
func TestExample_CapStore_GrantInsertRoundTrip(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c := mustCap(t)
	if err := store.Grant(ctx, c); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	got, err := store.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Operator != c.Operator {
		t.Errorf("Operator: got %q want %q", got.Operator, c.Operator)
	}
	if got.GrantedAt.Unix() != c.GrantedAt.Unix() {
		t.Errorf("GrantedAt: got %v want %v", got.GrantedAt, c.GrantedAt)
	}
	if got.ExpiresAt.Unix() != c.ExpiresAt.Unix() {
		t.Errorf("ExpiresAt: got %v want %v", got.ExpiresAt, c.ExpiresAt)
	}
	if got.RevokedAt.IsZero() == false {
		t.Errorf("RevokedAt: expected zero, got %v", got.RevokedAt)
	}
	if string(got.Signature) != string(c.Signature) {
		t.Errorf("Signature: got %q want %q", got.Signature, c.Signature)
	}
	if got.SignedBy != c.SignedBy {
		t.Errorf("SignedBy: got %q want %q", got.SignedBy, c.SignedBy)
	}
	if len(got.Scopes) != len(c.Scopes) {
		t.Errorf("Scopes len: got %d want %d", len(got.Scopes), len(c.Scopes))
	}
	for i, s := range c.Scopes {
		if got.Scopes[i] != s {
			t.Errorf("Scopes[%d]: got %q want %q", i, got.Scopes[i], s)
		}
	}
}

// TestExample_CapStore_GrantRejectsInvalidToken confirms Validate is
// enforced before INSERT (defense-in-depth; the CHECK constraints are
// the schema-level backstop but we should never reach them).
func TestExample_CapStore_GrantRejectsInvalidToken(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c := mustCap(t)
	c.ID = "" // invalidate
	if err := store.Grant(ctx, c); err == nil {
		t.Fatal("Grant: expected validation error, got nil")
	}
}

// TestExample_CapStore_GrantRejectsDuplicateID confirms the UNIQUE
// constraint produces ErrCapExists.
func TestExample_CapStore_GrantRejectsDuplicateID(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c1 := mustCap(t)
	c1.ID = "cap-dup"
	if err := store.Grant(ctx, c1); err != nil {
		t.Fatalf("Grant(c1): %v", err)
	}
	c2 := mustCap(t)
	c2.ID = "cap-dup"
	if err := store.Grant(ctx, c2); !errors.Is(err, ErrCapExists) {
		t.Fatalf("Grant(c2): expected ErrCapExists, got %v", err)
	}
}

// TestExample_CapStore_GetMissingReturnsErrCapNotFound.
func TestExample_CapStore_GetMissingReturnsErrCapNotFound(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	_, err := store.Get(context.Background(), "no-such-cap")
	if !errors.Is(err, ErrCapNotFound) {
		t.Fatalf("Get: expected ErrCapNotFound, got %v", err)
	}
}

// TestExample_CapStore_GetEmptyIDReturnsErrCapNotFound — empty string
// is treated the same as "not found" (not a query for the empty row).
func TestExample_CapStore_GetEmptyIDReturnsErrCapNotFound(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	_, err := store.Get(context.Background(), "")
	if !errors.Is(err, ErrCapNotFound) {
		t.Fatalf("Get(\"\"): expected ErrCapNotFound, got %v", err)
	}
}

// TestExample_CapStore_RevokeSetsRevokedAt confirms UPDATE marks the row.
func TestExample_CapStore_RevokeSetsRevokedAt(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c := mustCap(t)
	if err := store.Grant(ctx, c); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	revokeAt := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	if err := store.Revoke(ctx, c.ID, revokeAt); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, err := store.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RevokedAt.Unix() != revokeAt.Unix() {
		t.Errorf("RevokedAt: got %v want %v", got.RevokedAt, revokeAt)
	}
}

// TestExample_CapStore_RevokeAlreadyRevokedReturnsErrCapAlreadyRevoked.
func TestExample_CapStore_RevokeAlreadyRevokedReturnsErrCapAlreadyRevoked(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c := mustCap(t)
	if err := store.Grant(ctx, c); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	revokeAt := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	if err := store.Revoke(ctx, c.ID, revokeAt); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	if err := store.Revoke(ctx, c.ID, revokeAt); !errors.Is(err, ErrCapAlreadyRevoked) {
		t.Fatalf("second Revoke: expected ErrCapAlreadyRevoked, got %v", err)
	}
}

// TestExample_CapStore_RevokeIdempotent is the lenient variant.
func TestExample_CapStore_RevokeIdempotent(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c := mustCap(t)
	if err := store.Grant(ctx, c); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	revokeAt := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	if err := store.RevokeIdempotent(ctx, c.ID, revokeAt); err != nil {
		t.Fatalf("first RevokeIdempotent: %v", err)
	}
	if err := store.RevokeIdempotent(ctx, c.ID, revokeAt); err != nil {
		t.Fatalf("second RevokeIdempotent: expected nil, got %v", err)
	}
}

// TestExample_CapStore_RevokeMissingReturnsErrCapNotFound.
func TestExample_CapStore_RevokeMissingReturnsErrCapNotFound(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	err := store.Revoke(context.Background(), "no-such", time.Now())
	if !errors.Is(err, ErrCapNotFound) {
		t.Fatalf("Revoke(missing): expected ErrCapNotFound, got %v", err)
	}
}

// TestExample_CapStore_RevokeRejectsZeroTime — invariant from row 2018:
// zero revokedAt is a programming error, not a valid "revoke now".
func TestExample_CapStore_RevokeRejectsZeroTime(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	c := mustCap(t)
	if err := store.Grant(context.Background(), c); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := store.Revoke(context.Background(), c.ID, time.Time{}); !errors.Is(err, ErrCapAlreadyRevoked) {
		t.Fatalf("Revoke(zero): expected ErrCapAlreadyRevoked, got %v", err)
	}
}

// TestExample_CapStore_CheckReturnsActiveTokens — only non-revoked,
// not-yet-expired tokens are returned, ordered by granted_at ASC.
func TestExample_CapStore_CheckReturnsActiveTokens(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	// three tokens for "operator-nico"
	c1 := mustCap(t, func(c *CapToken) {
		c.ID = "cap-1"
		c.Operator = "operator-nico"
		c.GrantedAt = base
		c.ExpiresAt = base.Add(24 * time.Hour)
	})
	c2 := mustCap(t, func(c *CapToken) {
		c.ID = "cap-2"
		c.Operator = "operator-nico"
		c.GrantedAt = base.Add(1 * time.Hour)
		c.ExpiresAt = base.Add(25 * time.Hour)
	})
	c3 := mustCap(t, func(c *CapToken) {
		c.ID = "cap-3"
		c.Operator = "operator-nico"
		c.GrantedAt = base.Add(2 * time.Hour)
		c.ExpiresAt = base.Add(48 * time.Hour)
	})
	for _, c := range []*CapToken{c1, c2, c3} {
		if err := store.Grant(ctx, c); err != nil {
			t.Fatalf("Grant %s: %v", c.ID, err)
		}
	}
	// revoke c2
	if err := store.Revoke(ctx, "cap-2", base.Add(12*time.Hour)); err != nil {
		t.Fatalf("Revoke c2: %v", err)
	}

	at := base.Add(20 * time.Hour) // within window for c1 + c3; past c2's expiry too
	tokens, err := store.Check(ctx, "operator-nico", at)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("Check: expected 2 active tokens, got %d", len(tokens))
	}
	if tokens[0].ID != "cap-1" {
		t.Errorf("tokens[0].ID: got %q want %q", tokens[0].ID, "cap-1")
	}
	if tokens[1].ID != "cap-3" {
		t.Errorf("tokens[1].ID: got %q want %q", tokens[1].ID, "cap-3")
	}
}

// TestExample_CapStore_CheckEmptyOperatorReturnsEmptySlice — defensive
// guard so a UI with an empty operator field gets [] instead of error.
func TestExample_CapStore_CheckEmptyOperatorReturnsEmptySlice(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	tokens, err := store.Check(context.Background(), "", time.Now())
	if err != nil {
		t.Fatalf("Check(\"\"): %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected empty slice, got %d tokens", len(tokens))
	}
}

// TestExample_CapStore_CheckAtTimeFiltersExpiry — tokens past ExpiresAt
// are excluded even if not revoked.
func TestExample_CapStore_CheckAtTimeFiltersExpiry(t *testing.T) {
	db := newTestCapDB(t)
	store := NewCapStore(db)
	ctx := context.Background()

	c := mustCap(t, func(c *CapToken) { c.ID = "cap-expiring"; c.ExpiresAt = c.GrantedAt.Add(1 * time.Hour) })
	if err := store.Grant(ctx, c); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// Query at c.ExpiresAt — should be excluded (exclusive end).
	tokens, err := store.Check(ctx, c.Operator, c.ExpiresAt)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens at ExpiresAt, got %d", len(tokens))
	}
	// Query 1s before ExpiresAt — should be included.
	tokens, err = store.Check(ctx, c.Operator, c.ExpiresAt.Add(-1*time.Second))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("expected 1 token before ExpiresAt, got %d", len(tokens))
	}
}
