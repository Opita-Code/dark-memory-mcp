package vibe

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"pgregory.net/rapid"
)

// validDrift returns a minimal DriftReport pointing at a real
// artifact in the test DB. Tests then break one invariant at a time.
func validDrift(t testHelper, db *sql.DB) *DriftReport {
	t.Helper()
	art := validArtifact(t, db)
	store := NewArtifactStore(db)
	id, err := store.Insert(context.Background(), art)
	if err != nil {
		t.Fatalf("insert validArtifact: %v", err)
	}
	return &DriftReport{
		ArtifactID: id,
		Verdict:    VerdictAligned,
		Confidence: 0.95,
		Reasoning:  "default aligned drift",
	}
}

// ---- L2 example tests ----

func TestExample_Drift_InsertAndGetRoundTrip(t *testing.T) {
	db := newTestDB(t)
	store := NewDriftStore(db)
	ctx := context.Background()

	d := validDrift(t, db)
	id, err := store.Insert(ctx, d)
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
	if got.ArtifactID != d.ArtifactID {
		t.Fatalf("ArtifactID round-trip: expected %d, got %d", d.ArtifactID, got.ArtifactID)
	}
	if got.Verdict != d.Verdict {
		t.Fatalf("Verdict round-trip: expected %q, got %q", d.Verdict, got.Verdict)
	}
	if got.Confidence != d.Confidence {
		t.Fatalf("Confidence round-trip: expected %f, got %f", d.Confidence, got.Confidence)
	}
	if got.Reasoning != d.Reasoning {
		t.Fatalf("Reasoning round-trip: expected %q, got %q", d.Reasoning, got.Reasoning)
	}
	if got.Resolved {
		t.Fatal("fresh drift should not be resolved")
	}
}

func TestExample_Drift_DefaultEvalTypeIsDriftJudge(t *testing.T) {
	db := newTestDB(t)
	store := NewDriftStore(db)
	ctx := context.Background()

	d := validDrift(t, db)
	d.EvalType = "" // explicit empty
	id, err := store.Insert(ctx, d)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.EvalType != "drift_judge" {
		t.Fatalf("default EvalType: expected %q, got %q", "drift_judge", got.EvalType)
	}
}

func TestExample_Drift_StatusReturnsLatest(t *testing.T) {
	db := newTestDB(t)
	store := NewDriftStore(db)
	ctx := context.Background()

	d := validDrift(t, db)
	// Insert 3 drifts: aligned, drift_detected, needs_human.
	for _, v := range []string{
		VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman,
	} {
		d.Verdict = v
		if _, err := store.Insert(ctx, d); err != nil {
			t.Fatalf("Insert %s: %v", v, err)
		}
	}

	got, err := store.Status(ctx, d.ArtifactID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Verdict != VerdictNeedsHuman {
		t.Fatalf("Status: expected latest = needs_human, got %q", got.Verdict)
	}
}

func TestExample_Drift_StatusReturnsNotFoundWhenEmpty(t *testing.T) {
	db := newTestDB(t)
	store := NewDriftStore(db)
	_, err := store.Status(context.Background(), 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status on missing artifact: expected ErrNotFound, got %v", err)
	}
}

func TestExample_Drift_RejectsInvalidVerdict(t *testing.T) {
	d := &DriftReport{ArtifactID: 1, Verdict: "approve", Confidence: 0.9}
	err := d.Validate()
	if !errors.Is(err, ErrInvalidVerdict) {
		t.Fatalf("invalid verdict: expected ErrInvalidVerdict, got %v", err)
	}
}

func TestExample_Drift_RejectsConfidenceAboveOne(t *testing.T) {
	d := &DriftReport{ArtifactID: 1, Verdict: VerdictAligned, Confidence: 1.01}
	err := d.Validate()
	if !errors.Is(err, ErrInvalidConfidence) {
		t.Fatalf("conf > 1: expected ErrInvalidConfidence, got %v", err)
	}
}

func TestExample_Drift_RejectsConfidenceBelowZero(t *testing.T) {
	d := &DriftReport{ArtifactID: 1, Verdict: VerdictAligned, Confidence: -0.01}
	err := d.Validate()
	if !errors.Is(err, ErrInvalidConfidence) {
		t.Fatalf("conf < 0: expected ErrInvalidConfidence, got %v", err)
	}
}

func TestExample_Drift_AcceptsBoundaryConfidence(t *testing.T) {
	// Confidence exactly 0.0 and exactly 1.0 must be accepted.
	for _, c := range []float64{0.0, 1.0} {
		d := &DriftReport{
			ArtifactID: 1, Verdict: VerdictAligned, Confidence: c,
		}
		if err := d.Validate(); err != nil {
			t.Fatalf("confidence %f: expected nil, got %v", c, err)
		}
	}
}

func TestExample_Drift_InsertRejectsMissingArtifact(t *testing.T) {
	db := newTestDB(t)
	store := NewDriftStore(db)
	d := &DriftReport{
		ArtifactID: 9999, Verdict: VerdictAligned, Confidence: 0.9,
	}
	_, err := store.Insert(context.Background(), d)
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("missing artifact: expected ErrArtifactNotFound, got %v", err)
	}
}

// ---- L1 property tests ----

// TestProperty_Drift_AnyValidVerdictAccepted — universal claim:
// every canonical verdict, paired with any confidence in [0,1],
// passes Validate.
func TestProperty_Drift_AnyValidVerdictAccepted(t *testing.T) {
	verdicts := []string{VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman}
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.SampledFrom(verdicts).Draw(t, "verdict")
		c := rapid.Float64Range(0.0, 1.0).Draw(t, "confidence")
		d := &DriftReport{
			ArtifactID: int64(rapid.IntRange(1, 1000).Draw(t, "art_id")),
			Verdict:    v, Confidence: c,
		}
		if err := d.Validate(); err != nil {
			t.Fatalf("verdict=%s conf=%f: expected nil, got %v", v, c, err)
		}
	})
}

// TestProperty_Drift_ConfidenceOutsideRangeAlwaysRejected — universal
// claim: any confidence STRICTLY < 0 or STRICTLY > 1 is rejected.
// (Note: -0.0 and 0.0 are equal per IEEE 754, so -0.0 is in range.)
func TestProperty_Drift_ConfidenceOutsideRangeAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Pick a magnitude guaranteed > 0 so the negation lands
		// strictly below zero. Rapid.Float64Range is inclusive, so
		// we offset by a small epsilon.
		var c float64
		if rapid.Bool().Draw(t, "neg") {
			c = -rapid.Float64Range(1e-9, 1e6).Draw(t, "negMag")
		} else {
			c = 1.0 + rapid.Float64Range(1e-9, 1e6).Draw(t, "posMag")
		}
		d := &DriftReport{
			ArtifactID: 1, Verdict: VerdictAligned, Confidence: c,
		}
		err := d.Validate()
		if !errors.Is(err, ErrInvalidConfidence) {
			t.Fatalf("confidence %f: expected ErrInvalidConfidence, got %v", c, err)
		}
	})
}

// TestProperty_Drift_AnyNonCanonicalVerdictRejected — universal claim:
// any non-canonical verdict is always rejected.
func TestProperty_Drift_AnyNonCanonicalVerdictRejected(t *testing.T) {
	rejections := []string{"", "approve", "ALIGNED", "reject", "drift"}
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.SampledFrom(rejections).Draw(t, "bad_verdict")
		d := &DriftReport{ArtifactID: 1, Verdict: v, Confidence: 0.5}
		err := d.Validate()
		if !errors.Is(err, ErrInvalidVerdict) {
			t.Fatalf("verdict %q: expected ErrInvalidVerdict, got %v", v, err)
		}
	})
}

// TestProperty_Drift_StatusReturnsNewestAfterManyInserts — universal
// claim: after inserting N drifts in chronological order, Status
// returns the last inserted one (highest id).
func TestProperty_Drift_StatusReturnsNewestAfterManyInserts(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 10).Draw(t, "n")
		db := newTestDB(t)
		store := NewDriftStore(db)
		d := validDrift(t, db)

		var lastID int64
		verdicts := []string{VerdictAligned, VerdictDriftDetected, VerdictNeedsHuman}
		for i := 0; i < n; i++ {
			d.Verdict = verdicts[i%len(verdicts)]
			d.Confidence = rapid.Float64Range(0.0, 1.0).Draw(t, "conf")
			id, err := store.Insert(context.Background(), d)
			if err != nil {
				t.Fatalf("Insert iter %d: %v", i, err)
			}
			lastID = id
		}

		got, err := store.Status(context.Background(), d.ArtifactID)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got.ID != lastID {
			t.Fatalf("Status.ID: expected %d (newest), got %d", lastID, got.ID)
		}
	})
}
