// Phase 4 Chunk 4.3 — judge.Store.ConfidencesByProjectProviderTarget
// + SaveEvaluation project_id roundtrip tests.
//
// Three tests cover the new project-scoped calibration path:
//
//  1. TestConfidencesByProjectProviderTarget_FiltersCorrectly —
//     insert 3 evaluations (2 in proj-huila, 1 in proj-default),
//     query by proj-huila → only the 2 from that project.
//  2. TestConfidencesByProjectProviderTarget_EmptyProjectIDFailsFast —
//     empty projectID rejected (contract: project-scoped queries
//     must carry the scope).
//  3. TestSaveEvaluation_ProjectIDRoundtrip — Evaluation.ProjectID
//     roundtrips through the persistence layer.
package judge

import (
	"context"
	"math"
	"testing"
)

// TestConfidencesByProjectProviderTarget_FiltersCorrectly inserts
// 3 evaluations across 2 projects; the query must return only the
// 2 from the requested project.
func TestConfidencesByProjectProviderTarget_FiltersCorrectly(t *testing.T) {
	store, _, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// 2 evaluations in proj-huila.
	for i, c := range []float64{0.7, 0.8} {
		e := &Evaluation{
			EvalType:    "drift_judge",
			TargetType:  "file",
			TargetID:    "test://huila-" + string(rune('A'+i)) + ".go",
			ProjectID:   "proj-huila",
			VerdictJSON: `{"verdict":"aligned","confidence":0.5}`,
			Confidence:  c,
		}
		if _, err := store.SaveEvaluation(ctx, &Audit{
			Actor:     "operator-test",
			ProjectID: "proj-huila",
		}, e); err != nil {
			t.Fatalf("SaveEvaluation huila[%d]: %v", i, err)
		}
	}

	// 1 evaluation in proj-default.
	if _, err := store.SaveEvaluation(ctx, &Audit{
		Actor:     "operator-test",
		ProjectID: "proj-default",
	}, &Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "file",
		TargetID:    "test://default.go",
		ProjectID:   "proj-default",
		VerdictJSON: `{"verdict":"aligned","confidence":0.5}`,
		Confidence:  0.95,
	}); err != nil {
		t.Fatalf("SaveEvaluation default: %v", err)
	}

	// Query project-scoped.
	confidences, err := store.ConfidencesByProjectProviderTarget(
		ctx, "proj-huila", "", "file", "drift_judge", 100,
	)
	if err != nil {
		t.Fatalf("ConfidencesByProjectProviderTarget: %v", err)
	}
	if len(confidences) != 2 {
		t.Fatalf("got %d confidences; want 2 (proj-huila only)", len(confidences))
	}

	// Sorted DESC by id, so 0.8 first, 0.7 second.
	if !floatNear(confidences[0], 0.8, 0.001) || !floatNear(confidences[1], 0.7, 0.001) {
		t.Errorf("confidences = %v; want [0.8, 0.7]", confidences)
	}

	// Compare with the global variant: should return all 3.
	global, err := store.ConfidencesByProviderTarget(ctx, "", "file", "drift_judge", 100)
	if err != nil {
		t.Fatalf("ConfidencesByProviderTarget: %v", err)
	}
	if len(global) != 3 {
		t.Errorf("global confidences = %d; want 3 (both projects)", len(global))
	}
}

func TestConfidencesByProjectProviderTarget_EmptyProjectIDFailsFast(t *testing.T) {
	store, _, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	_, err := store.ConfidencesByProjectProviderTarget(ctx, "", "", "file", "drift_judge", 100)
	if err == nil {
		t.Fatal("empty projectID accepted; want rejection")
	}
}

func TestSaveEvaluation_ProjectIDRoundtrip(t *testing.T) {
	store, _, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	id, err := store.SaveEvaluation(ctx, &Audit{
		Actor:     "operator-test",
		ProjectID: "proj-roundtrip",
	}, &Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "file",
		TargetID:    "test://rt.go",
		ProjectID:   "proj-roundtrip",
		VerdictJSON: `{"verdict":"aligned","confidence":0.9}`,
		Confidence:  0.9,
	})
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	got, err := store.GetEvaluation(ctx, id)
	if err != nil {
		t.Fatalf("GetEvaluation: %v", err)
	}
	if got.ProjectID != "proj-roundtrip" {
		t.Errorf("ProjectID = %q; want proj-roundtrip", got.ProjectID)
	}
}

func floatNear(a, b, tol float64) bool { return math.Abs(a-b) < tol }
