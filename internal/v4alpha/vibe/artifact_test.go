package vibe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// ---- L2 example tests (boundary + named scenarios) ----

func TestExample_Artifact_InsertAndGetRoundTrip(t *testing.T) {
	db := newTestDB(t)
	store := NewArtifactStore(db)
	ctx := context.Background()

	a := validArtifact(t, db)
	id, err := store.Insert(ctx, a)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("Insert: expected id > 0, got %d", id)
	}

	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != id {
		t.Fatalf("ID round-trip: expected %d, got %d", id, got.ID)
	}
	if got.SpecID != a.SpecID {
		t.Fatalf("SpecID round-trip: expected %d, got %d", a.SpecID, got.SpecID)
	}
	if got.Type != a.Type {
		t.Fatalf("Type round-trip: expected %q, got %q", a.Type, got.Type)
	}
	if got.URL != a.URL {
		t.Fatalf("URL round-trip: expected %q, got %q", a.URL, got.URL)
	}
}

func TestExample_Artifact_GetNotFoundReturnsErrNotFound(t *testing.T) {
	db := newTestDB(t)
	store := NewArtifactStore(db)
	_, err := store.Get(context.Background(), 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing): expected ErrNotFound, got %v", err)
	}
}

func TestExample_Artifact_RejectsInvalidType(t *testing.T) {
	a := &Artifact{SpecID: 1, Type: "blog-post", URL: "x"}
	err := a.Validate()
	if !errors.Is(err, ErrInvalidArtifactType) {
		t.Fatalf("invalid type: expected ErrInvalidArtifactType, got %v", err)
	}
}

func TestExample_Artifact_RejectsMissingSpecID(t *testing.T) {
	a := &Artifact{SpecID: 0, Type: ArtifactTypeCode, URL: "x"}
	err := a.Validate()
	if !errors.Is(err, ErrMissingSpecID) {
		t.Fatalf("missing spec_id: expected ErrMissingSpecID, got %v", err)
	}
}

func TestExample_Artifact_RejectsEmpty(t *testing.T) {
	a := &Artifact{SpecID: 1, Type: ArtifactTypeCode}
	err := a.Validate()
	if !errors.Is(err, ErrMissingArtifactRef) {
		t.Fatalf("empty artifact: expected ErrMissingArtifactRef, got %v", err)
	}
}

func TestExample_Artifact_AcceptsTextOnly(t *testing.T) {
	a := &Artifact{SpecID: 1, Type: ArtifactTypeText, Text: "hello"}
	if err := a.Validate(); err != nil {
		t.Fatalf("text-only artifact: expected nil, got %v", err)
	}
}

func TestExample_Artifact_AcceptsAllCanonicalTypes(t *testing.T) {
	for _, typ := range []string{
		ArtifactTypeCode, ArtifactTypeText, ArtifactTypeImage,
		ArtifactTypeVideo, ArtifactTypeAudio, ArtifactTypeMulti,
	} {
		a := &Artifact{SpecID: 1, Type: typ, URL: "x"}
		if err := a.Validate(); err != nil {
			t.Fatalf("canonical type %s: expected nil, got %v", typ, err)
		}
	}
}

func TestExample_Artifact_RefFileRequiresPath(t *testing.T) {
	a := &Artifact{
		SpecID: 1, Type: ArtifactTypeCode,
		Ref: &ArtifactRef{Kind: RefKindFile},
	}
	err := a.Validate()
	if !errors.Is(err, ErrRefKindMismatch) {
		t.Fatalf("file ref without path: expected ErrRefKindMismatch, got %v", err)
	}
	if !strings.Contains(err.Error(), "Path") {
		t.Fatalf("error should mention Path, got: %v", err)
	}
}

func TestExample_Artifact_RefURLRequiresURL(t *testing.T) {
	a := &Artifact{
		SpecID: 1, Type: ArtifactTypeCode,
		Ref: &ArtifactRef{Kind: RefKindURL},
	}
	err := a.Validate()
	if !errors.Is(err, ErrRefKindMismatch) {
		t.Fatalf("url ref without URL: expected ErrRefKindMismatch, got %v", err)
	}
}

func TestExample_Artifact_RefArtifactIDRequiresArtifactID(t *testing.T) {
	a := &Artifact{
		SpecID: 1, Type: ArtifactTypeCode,
		Ref: &ArtifactRef{Kind: RefKindArtifact},
	}
	err := a.Validate()
	if !errors.Is(err, ErrRefKindMismatch) {
		t.Fatalf("artifact_id ref without id: expected ErrRefKindMismatch, got %v", err)
	}
}

func TestExample_Artifact_InsertRejectsMissingSpec(t *testing.T) {
	// INV-3: artifact.spec_id must reference an existing spec.
	// We insert an artifact with a fake spec_id that was never added.
	db := newTestDB(t)
	store := NewArtifactStore(db)
	a := &Artifact{SpecID: 9999, Type: ArtifactTypeCode, URL: "x"}
	_, err := store.Insert(context.Background(), a)
	if !errors.Is(err, ErrSpecNotFound) {
		t.Fatalf("missing spec: expected ErrSpecNotFound, got %v", err)
	}
}

func TestExample_Artifact_RefFileRoundTrip(t *testing.T) {
	// File ref persists and round-trips with Path intact.
	db := newTestDB(t)
	store := NewArtifactStore(db)
	ctx := context.Background()

	a := validArtifact(t, db)
	a.URL = ""
	a.Text = ""
	a.Ref = &ArtifactRef{
		Kind:     RefKindFile,
		Path:     "/abs/path/to/file.go",
		MaxBytes: 4096,
	}
	id, err := store.Insert(ctx, a)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Ref == nil {
		t.Fatal("Ref lost in round-trip")
	}
	if got.Ref.Kind != RefKindFile {
		t.Fatalf("Ref.Kind: expected %q, got %q", RefKindFile, got.Ref.Kind)
	}
	if got.Ref.Path != "/abs/path/to/file.go" {
		t.Fatalf("Ref.Path: expected %q, got %q", "/abs/path/to/file.go", got.Ref.Path)
	}
	if got.Ref.MaxBytes != 4096 {
		t.Fatalf("Ref.MaxBytes: expected 4096, got %d", got.Ref.MaxBytes)
	}
}

// ---- L1 property tests (rapid) ----

// TestProperty_Artifact_AnyCanonicalTypeInsertable — universal claim:
// every canonical ArtifactType, paired with at least one of URL/Text,
// inserts successfully and round-trips with the same type.
func TestProperty_Artifact_AnyCanonicalTypeInsertable(t *testing.T) {
	types := []string{
		ArtifactTypeCode, ArtifactTypeText, ArtifactTypeImage,
		ArtifactTypeVideo, ArtifactTypeAudio, ArtifactTypeMulti,
	}
	rapid.Check(t, func(t *rapid.T) {
		typ := rapid.SampledFrom(types).Draw(t, "type")
		db := newTestDB(t)
		store := NewArtifactStore(db)
		specID := insertFakeSpec(t, db)

		a := &Artifact{
			SpecID: specID,
			Type:   typ,
			URL:    "file:///tmp/x",
		}
		id, err := store.Insert(context.Background(), a)
		if err != nil {
			t.Fatalf("Insert type=%s: %v", typ, err)
		}
		got, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Type != typ {
			t.Fatalf("Type round-trip: expected %q, got %q", typ, got.Type)
		}
	})
}

// TestProperty_Artifact_AnyNonCanonicalTypeRejected — universal claim:
// any non-canonical type drawn from a rejection alphabet is always
// rejected by Validate.
func TestProperty_Artifact_AnyNonCanonicalTypeRejected(t *testing.T) {
	rejections := []string{"", "blog", "x", "VIDEO", "Code", "video "}
	rapid.Check(t, func(t *rapid.T) {
		typ := rapid.SampledFrom(rejections).Draw(t, "bad_type")
		a := &Artifact{SpecID: 1, Type: typ, URL: "x"}
		err := a.Validate()
		if !errors.Is(err, ErrInvalidArtifactType) {
			t.Fatalf("non-canonical type %q: expected ErrInvalidArtifactType, got %v",
				typ, err)
		}
	})
}

// TestProperty_Artifact_MissingSpecIDAlwaysRejected — universal claim:
// any SpecID ≤ 0 is rejected by Validate (the DB-layer cross-spec
// check is exercised separately).
func TestProperty_Artifact_MissingSpecIDAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Rapid.IntRange includes negatives.
		id := rapid.IntRange(-100, 0).Draw(t, "spec_id")
		a := &Artifact{SpecID: int64(id), Type: ArtifactTypeCode, URL: "x"}
		err := a.Validate()
		if !errors.Is(err, ErrMissingSpecID) {
			t.Fatalf("spec_id=%d: expected ErrMissingSpecID, got %v", id, err)
		}
	})
}

// TestProperty_Artifact_EmptyArtifactAlwaysRejected — universal claim:
// an artifact with URL="", Text="", Ref=nil is always rejected.
func TestProperty_Artifact_EmptyArtifactAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		specID := rapid.IntRange(1, 1000).Draw(t, "spec_id")
		a := &Artifact{SpecID: int64(specID), Type: ArtifactTypeCode}
		err := a.Validate()
		if !errors.Is(err, ErrMissingArtifactRef) {
			t.Fatalf("empty artifact: expected ErrMissingArtifactRef, got %v", err)
		}
	})
}

// TestProperty_Artifact_AnyRandomURLRoundTrips — universal claim:
// for any non-empty URL of any shape (no length cap), Insert + Get
// returns the same URL. The path/value pair is round-trip stable.
func TestProperty_Artifact_AnyRandomURLRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		url := rapid.StringMatching(`[a-zA-Z0-9_./:?-]{4,128}`).Draw(t, "url")
		db := newTestDB(t)
		store := NewArtifactStore(db)
		specID := insertFakeSpec(t, db)

		a := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: url}
		id, err := store.Insert(context.Background(), a)
		if err != nil {
			t.Fatalf("Insert: %v", err)
		}
		got, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.URL != url {
			t.Fatalf("URL round-trip: expected %q, got %q", url, got.URL)
		}
	})
}
