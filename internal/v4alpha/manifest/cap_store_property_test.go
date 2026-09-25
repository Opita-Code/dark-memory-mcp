package manifest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"pgregory.net/rapid"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// rapidCapDB returns a SHARED SQLite DB across all rapid iterations
// within one property test. Tables are truncated between iterations so
// each iteration starts from a clean slate, but the file is reused.
// This is critical for performance: creating t.TempDir() per rapid
// iteration on Windows is slow (the slow part is directory creation).
func rapidCapDB(t *testing.T, rt *rapid.T) (*sql.DB, func()) {
	dsn := filepath.Join(t.TempDir(), "rapid-cap.db")
	db, err := sql.Open("sqlite", dsn+"?_pragma=foreign_keys(1)")
	if err != nil {
		rt.Fatalf("rapidCapDB open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := CreateCapSchema(context.Background(), db); err != nil {
		rt.Fatalf("rapidCapDB schema: %v", err)
	}
	reset := func() {
		if _, err := db.ExecContext(context.Background(), "DELETE FROM capabilities"); err != nil {
			rt.Fatalf("rapidCapDB reset: %v", err)
		}
	}
	return db, reset
}

// Property: any valid CapToken survives a Grant -> Get round-trip with
// every field equivalent. Catches serialization bugs (lost scopes, time
// truncation, signature corruption).
func TestProperty_CapStore_GrantGetRoundTrip(t *testing.T) {
	db, reset := rapidCapDB(t, &rapid.T{})
	store := NewCapStore(db)
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		reset()
		tok := rapidValidToken().Draw(rt, "tok")
		if err := store.Grant(ctx, tok); err != nil {
			rt.Fatalf("Grant: %v", err)
		}
		got, err := store.Get(ctx, tok.ID)
		if err != nil {
			rt.Fatalf("Get: %v", err)
		}
		if got.ID != tok.ID {
			rt.Fatalf("ID: got %q want %q", got.ID, tok.ID)
		}
		if got.Operator != tok.Operator {
			rt.Fatalf("Operator: got %q want %q", got.Operator, tok.Operator)
		}
		if !reflect.DeepEqual(got.Scopes, tok.Scopes) {
			rt.Fatalf("Scopes: got %v want %v", got.Scopes, tok.Scopes)
		}
		if string(got.Signature) != string(tok.Signature) {
			rt.Fatalf("Signature: got %q want %q", got.Signature, tok.Signature)
		}
		if got.SignedBy != tok.SignedBy {
			rt.Fatalf("SignedBy: got %q want %q", got.SignedBy, tok.SignedBy)
		}
		if got.GrantedAt.Unix() != tok.GrantedAt.Unix() {
			rt.Fatalf("GrantedAt: got %v want %v", got.GrantedAt, tok.GrantedAt)
		}
		if got.ExpiresAt.Unix() != tok.ExpiresAt.Unix() {
			rt.Fatalf("ExpiresAt: got %v want %v", got.ExpiresAt, tok.ExpiresAt)
		}
		if !got.RevokedAt.IsZero() {
			rt.Fatalf("RevokedAt: expected zero, got %v", got.RevokedAt)
		}
	})
}

// Property: granting the same id twice always returns ErrCapExists on
// the second call. Catches regressions where the UNIQUE constraint is
// removed or the error mapping breaks.
func TestProperty_CapStore_DuplicateGrantAlwaysRejected(t *testing.T) {
	db, reset := rapidCapDB(t, &rapid.T{})
	store := NewCapStore(db)
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		reset()
		tok := rapidValidToken().Draw(rt, "tok")
		if err := store.Grant(ctx, tok); err != nil {
			rt.Fatalf("first Grant: %v", err)
		}
		err := store.Grant(ctx, tok)
		if !errors.Is(err, ErrCapExists) {
			rt.Fatalf("second Grant: expected ErrCapExists, got %v", err)
		}
	})
}

// Property: after Revoke, the token never appears in Check for any
// time after revoked_at. Catches the "revoke ignored by Check" bug.
func TestProperty_CapStore_RevokeExcludesFromCheck(t *testing.T) {
	db, reset := rapidCapDB(t, &rapid.T{})
	store := NewCapStore(db)
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		reset()
		tok := rapidValidToken().Draw(rt, "tok")
		if err := store.Grant(ctx, tok); err != nil {
			rt.Fatalf("Grant: %v", err)
		}
		// Revoke between granted_at and expires_at.
		delta := tok.ExpiresAt.Sub(tok.GrantedAt)
		revokeAt := tok.GrantedAt.Add(delta / 2)
		if err := store.Revoke(ctx, tok.ID, revokeAt); err != nil {
			rt.Fatalf("Revoke: %v", err)
		}
		// Check at revokeAt + 1 second — must not appear.
		tokens, err := store.Check(ctx, tok.Operator, revokeAt.Add(time.Second))
		if err != nil {
			rt.Fatalf("Check: %v", err)
		}
		for _, tk := range tokens {
			if tk.ID == tok.ID {
				rt.Fatalf("Check after revoke: token %q still present", tok.ID)
			}
		}
	})
}

// Property: for any operator, granting N active tokens makes Check
// return all N at the moment between the latest granted_at and the
// earliest expires_at. Catches the "lost insert" / "missing scope"
// bug class.
func TestProperty_CapStore_CheckReturnsAllActiveTokens(t *testing.T) {
	db, reset := rapidCapDB(t, &rapid.T{})
	store := NewCapStore(db)
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		reset()
		// Use a fixed operator so all tokens aggregate.
		tok := rapidValidToken().Draw(rt, "tok")
		operator := tok.Operator

		n := rapid.IntRange(1, 8).Draw(rt, "n")
		ids := make(map[string]bool, n)
		for i := 0; i < n; i++ {
			x := rapidValidToken().Draw(rt, fmt.Sprintf("tok-%d", i))
			x.Operator = operator
			x.GrantedAt = tok.GrantedAt.Add(time.Duration(i) * time.Minute)
			x.ExpiresAt = tok.ExpiresAt
			if err := store.Grant(ctx, x); err != nil {
				rt.Fatalf("Grant %s: %v", x.ID, err)
			}
			ids[x.ID] = true
		}
		// Query 1 minute after granted_at — strictly inside the window.
		tokens, err := store.Check(ctx, operator, tok.GrantedAt.Add(time.Minute))
		if err != nil {
			rt.Fatalf("Check: %v", err)
		}
		if len(tokens) != n {
			rt.Fatalf("Check: expected %d tokens, got %d", n, len(tokens))
		}
		for _, tk := range tokens {
			if !ids[tk.ID] {
				rt.Fatalf("Check returned unexpected token %q", tk.ID)
			}
		}
	})
}

// Property: Check returns an empty (not nil) slice when the operator
// has no rows. Defensive guarantee so JSON encoders emit [] not null.
func TestProperty_CapStore_CheckReturnsEmptyNotNil(t *testing.T) {
	db, reset := rapidCapDB(t, &rapid.T{})
	store := NewCapStore(db)
	ctx := context.Background()
	rapid.Check(t, func(rt *rapid.T) {
		reset()
		operator := "operator-" + rapid.StringMatching(`[a-z0-9]{4}`).Draw(rt, "operator")
		tokens, err := store.Check(ctx, operator, time.Now())
		if err != nil {
			rt.Fatalf("Check: %v", err)
		}
		if tokens == nil {
			rt.Fatalf("Check: expected non-nil empty slice")
		}
		if len(tokens) != 0 {
			rt.Fatalf("Check: expected 0 tokens, got %d", len(tokens))
		}
	})
}
