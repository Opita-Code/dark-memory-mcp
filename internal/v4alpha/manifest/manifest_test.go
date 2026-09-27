package manifest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

func newTestManifestDB(t *testing.T) *sql.DB {
	t.Helper()
	// store.OpenSQLite applies the BUG-5 pragma set so manifest tests
	// exercise the same connection pool and journal_mode the production
	// binary will use.
	dsn := filepath.Join(t.TempDir(), "manifest_test.db")
	db, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := CreateManifestSchema(context.Background(), db); err != nil {
		t.Fatalf("CreateManifestSchema: %v", err)
	}
	return db
}

// validManifestEntry constructs an Entry baseline. Tests that want a
// well-formed entry call this with no mutators; tests that want to
// introduce an invalid state call this with mutators and then call
// e.Validate() themselves to assert the rejection. The helper does NOT
// auto-validate because some tests intentionally mutate one field into
// an invalid state.
//
// Usage:
//
//	e := validManifestEntry()                         // baseline
//	e := validManifestEntry(func(x *Entry){ x.ID = 1 }) // mutated
//	if err := e.Validate(); err != nil { ... }        // assert rejection
func validManifestEntry(muts ...func(*Entry)) *Entry {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	h := sha256.Sum256([]byte("canonical artifact bytes"))
	e := &Entry{
		ID:         0,
		ArtifactID: 42,
		Kind:       "code",
		SHA256:     hex.EncodeToString(h[:]),
		SignedBy:   "operator-nico",
		SignedAt:   now,
		Signature:  []byte("ed25519-signature-bytes-mock"),
	}
	for _, fn := range muts {
		fn(e)
	}
	return e
}

// validManifestEntryChecked is the strict variant that panics on
// invalid baseline. Use it in tests that need a guaranteed-valid entry
// (e.g. round-trip tests).
func validManifestEntryChecked(t *testing.T, muts ...func(*Entry)) *Entry {
	t.Helper()
	e := validManifestEntry(muts...)
	if err := e.Validate(); err != nil {
		t.Fatalf("validManifestEntryChecked: produced invalid entry: %v", err)
	}
	return e
}

// TestExample_Entry_ValidateAcceptsValid confirms a well-formed entry
// passes Validate.
func TestExample_Entry_ValidateAcceptsValid(t *testing.T) {
	e := validManifestEntryChecked(t)
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestExample_Entry_ValidateRejectsZeroArtifactID.
func TestExample_Entry_ValidateRejectsZeroArtifactID(t *testing.T) {
	e := validManifestEntry(func(x *Entry) { x.ArtifactID = 0 })
	if !errors.Is(e.Validate(), ErrBadArtifactID) {
		t.Fatalf("expected ErrBadArtifactID, got %v", e.Validate())
	}
	e = validManifestEntry(func(x *Entry) { x.ArtifactID = -1 })
	if !errors.Is(e.Validate(), ErrBadArtifactID) {
		t.Fatalf("expected ErrBadArtifactID for negative, got %v", e.Validate())
	}
}

// TestExample_Entry_ValidateRejectsNonCanonicalKind.
func TestExample_Entry_ValidateRejectsNonCanonicalKind(t *testing.T) {
	for _, kind := range []string{"", "binary", "PDF", "zip", "manifest", "spec"} {
		t.Run(kind, func(t *testing.T) {
			e := validManifestEntry(func(x *Entry) { x.Kind = kind })
			if !errors.Is(e.Validate(), ErrEmptyKind) {
				t.Fatalf("kind=%q: expected ErrEmptyKind, got %v", kind, e.Validate())
			}
		})
	}
}

// TestExample_Entry_ValidateRejectsBadSHA256.
func TestExample_Entry_ValidateRejectsBadSHA256(t *testing.T) {
	cases := []struct {
		name string
		sha  string
	}{
		{"empty", ""},
		{"too short", "abc123"},
		{"too long", strings.Repeat("a", 65)},
		{"uppercase", strings.Repeat("A", 64)},
		{"non-hex char", strings.Repeat("z", 64)},
		{"63 chars", strings.Repeat("a", 63)},
		{"65 chars", strings.Repeat("a", 65)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validManifestEntry(func(x *Entry) { x.SHA256 = tc.sha })
			if !errors.Is(e.Validate(), ErrBadSHA256) {
				t.Fatalf("sha=%q: expected ErrBadSHA256, got %v", tc.sha, e.Validate())
			}
		})
	}
}

// TestExample_Entry_ValidateRejectsEmptySignedBy.
func TestExample_Entry_ValidateRejectsEmptySignedBy(t *testing.T) {
	e := validManifestEntry(func(x *Entry) { x.SignedBy = "" })
	if !errors.Is(e.Validate(), ErrEmptyManifestSignedBy) {
		t.Fatalf("expected ErrEmptyManifestSignedBy, got %v", e.Validate())
	}
}

// TestExample_Entry_ValidateRejectsEmptySignature.
func TestExample_Entry_ValidateRejectsEmptySignature(t *testing.T) {
	e := validManifestEntry(func(x *Entry) { x.Signature = nil })
	if !errors.Is(e.Validate(), ErrEmptyManifestSignature) {
		t.Fatalf("expected ErrEmptyManifestSignature, got %v", e.Validate())
	}
	e = validManifestEntry(func(x *Entry) { x.Signature = []byte{} })
	if !errors.Is(e.Validate(), ErrEmptyManifestSignature) {
		t.Fatalf("expected ErrEmptyManifestSignature, got %v", e.Validate())
	}
}

// TestExample_Entry_ValidateAcceptsAllCanonicalKinds.
func TestExample_Entry_ValidateAcceptsAllCanonicalKinds(t *testing.T) {
	for _, kind := range CanonicalKinds {
		t.Run(kind, func(t *testing.T) {
			e := validManifestEntry(func(x *Entry) { x.Kind = kind })
			if err := e.Validate(); err != nil {
				t.Fatalf("kind=%q: %v", kind, err)
			}
		})
	}
}

// TestExample_HashBytesDeterministic — same input always produces the
// same hash (the whole point of SHA-256).
func TestExample_HashBytesDeterministic(t *testing.T) {
	a := HashBytes([]byte("hello"))
	b := HashBytes([]byte("hello"))
	if a != b {
		t.Errorf("HashBytes not deterministic: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("HashBytes len: expected 64, got %d", len(a))
	}
	// hex chars only
	for _, r := range a {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			t.Errorf("HashBytes produced non-hex char %q in %q", r, a)
		}
	}
}

// TestExample_HashBytesKnownVector — SHA-256("abc") = ba7816bf...
func TestExample_HashBytesKnownVector(t *testing.T) {
	got := HashBytes([]byte("abc"))
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("HashBytes(abc): got %q want %q", got, want)
	}
}

// TestExample_IsValidKindAcceptsCanonical.
func TestExample_IsValidKindAcceptsCanonical(t *testing.T) {
	for _, k := range CanonicalKinds {
		if !IsValidKind(k) {
			t.Errorf("IsValidKind(%q): expected true", k)
		}
	}
}

// TestExample_IsValidKindRejectsNonCanonical.
func TestExample_IsValidKindRejectsNonCanonical(t *testing.T) {
	for _, k := range []string{"", "binary", "json", "yaml", "spec", "manifest", "CODE"} {
		if IsValidKind(k) {
			t.Errorf("IsValidKind(%q): expected false", k)
		}
	}
}

// TestExample_ManifestStore_InsertAssignsID confirms Insert returns a
// positive id and sets e.ID.
func TestExample_ManifestStore_InsertAssignsID(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	e := validManifestEntryChecked(t)
	id, err := store.Insert(context.Background(), e)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id <= 0 {
		t.Errorf("Insert returned id=%d, expected positive", id)
	}
	if e.ID != id {
		t.Errorf("e.ID: got %d want %d", e.ID, id)
	}
}

// TestExample_ManifestStore_InsertRejectsInvalid confirms Validate runs
// before INSERT.
func TestExample_ManifestStore_InsertRejectsInvalid(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	e := validManifestEntry(func(x *Entry) { x.ArtifactID = 0 })
	_, err := store.Insert(context.Background(), e)
	if !errors.Is(err, ErrBadArtifactID) {
		t.Fatalf("Insert: expected ErrBadArtifactID, got %v", err)
	}
}

// TestExample_ManifestStore_GetByIDRoundTrip.
func TestExample_ManifestStore_GetByIDRoundTrip(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	ctx := context.Background()

	e := validManifestEntryChecked(t)
	id, err := store.Insert(ctx, e)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := store.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ArtifactID != e.ArtifactID {
		t.Errorf("ArtifactID: got %d want %d", got.ArtifactID, e.ArtifactID)
	}
	if got.Kind != e.Kind {
		t.Errorf("Kind: got %q want %q", got.Kind, e.Kind)
	}
	if got.SHA256 != e.SHA256 {
		t.Errorf("SHA256: got %q want %q", got.SHA256, e.SHA256)
	}
	if got.SignedBy != e.SignedBy {
		t.Errorf("SignedBy: got %q want %q", got.SignedBy, e.SignedBy)
	}
	if got.SignedAt.Unix() != e.SignedAt.Unix() {
		t.Errorf("SignedAt: got %v want %v", got.SignedAt, e.SignedAt)
	}
	if string(got.Signature) != string(e.Signature) {
		t.Errorf("Signature: got %q want %q", got.Signature, e.Signature)
	}
}

// TestExample_ManifestStore_GetByIDMissingReturnsErrManifestNotFound.
func TestExample_ManifestStore_GetByIDMissingReturnsErrManifestNotFound(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	_, err := store.GetByID(context.Background(), 99999)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("GetByID(missing): expected ErrManifestNotFound, got %v", err)
	}
	_, err = store.GetByID(context.Background(), 0)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("GetByID(0): expected ErrManifestNotFound, got %v", err)
	}
}

// TestExample_ManifestStore_ListByArtifactChronological — multiple
// entries for one artifact return ordered by signed_at ASC.
func TestExample_ManifestStore_ListByArtifactChronological(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		e := validManifestEntry(func(x *Entry) {
			x.ArtifactID = 100
			x.SignedAt = base.Add(time.Duration(i) * time.Hour)
		})
		if _, err := store.Insert(ctx, e); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	list, err := store.ListByArtifact(ctx, 100)
	if err != nil {
		t.Fatalf("ListByArtifact: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	for i := 0; i < 3; i++ {
		want := base.Add(time.Duration(i) * time.Hour)
		if list[i].SignedAt.Unix() != want.Unix() {
			t.Errorf("list[%d].SignedAt: got %v want %v", i, list[i].SignedAt, want)
		}
	}
}

// TestExample_ManifestStore_ListByArtifactEmpty returns an empty (not
// nil) slice for an artifact with no entries.
func TestExample_ManifestStore_ListByArtifactEmpty(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	list, err := store.ListByArtifact(context.Background(), 12345)
	if err != nil {
		t.Fatalf("ListByArtifact: %v", err)
	}
	if list == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(list) != 0 {
		t.Errorf("expected 0 entries, got %d", len(list))
	}
}

// TestExample_ManifestStore_ListByArtifactFiltersOtherIDs confirms
// entries for artifact A don't appear when querying artifact B.
func TestExample_ManifestStore_ListByArtifactFiltersOtherIDs(t *testing.T) {
	db := newTestManifestDB(t)
	store := NewManifestStore(db)
	ctx := context.Background()

	for _, artID := range []int64{1, 2, 3} {
		e := validManifestEntry(func(x *Entry) { x.ArtifactID = artID })
		if _, err := store.Insert(ctx, e); err != nil {
			t.Fatalf("Insert art=%d: %v", artID, err)
		}
	}
	list, err := store.ListByArtifact(ctx, 2)
	if err != nil {
		t.Fatalf("ListByArtifact: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry for artifact 2, got %d", len(list))
	}
	if list[0].ArtifactID != 2 {
		t.Errorf("entry.ArtifactID: got %d want 2", list[0].ArtifactID)
	}
}

// TestExample_ManifestStore_CreateManifestSchemaIdempotent.
func TestExample_ManifestStore_CreateManifestSchemaIdempotent(t *testing.T) {
	db := newTestManifestDB(t)
	ctx := context.Background()
	if err := CreateManifestSchema(ctx, db); err != nil {
		t.Fatalf("second CreateManifestSchema: %v", err)
	}
}
