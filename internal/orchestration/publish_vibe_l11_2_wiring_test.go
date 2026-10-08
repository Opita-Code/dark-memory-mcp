package orchestration

// Phase 21 L11.2 — WIRING TESTS.
//
// These exist because the library tests in internal/vibeflow prove the
// grading rules are correct but say NOTHING about whether the
// production path calls them. Phase 20 shipped nine correct, tested
// primitives that nothing invoked. A green library suite is exactly
// the evidence that failed to catch that.
//
// So every test here drives the real PublishVibe entry point and
// asserts on the value the MCP layer actually returns to a harness.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

func setupOrchForGrade(t *testing.T) *Orchestrator {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "l11-2-grade.db")
	st, err := runtime.Open(context.Background(), store.Config{
		Driver:      store.DriverSQLite,
		DSN:         dsn,
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SetActiveProject(context.Background(), "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	return New(st, &safety.Holder{})
}

// THE WIRING PROOF. auto_drift_check=false takes the production
// "skipped" path, which is also the path where the bug is most
// dangerous: confidence 0 from a judge that never ran.
func TestWiring_L11_2_SkippedPathCarriesGrade(t *testing.T) {
	o := setupOrchForGrade(t)
	res, err := o.PublishVibe(context.Background(), PublishVibeInput{
		Spec:           PublishSpecInput{VibeCase: "C1"},
		Artifact:       PublishArtifactInput{ArtifactType: "text", ArtifactURL: "memory://w/1", Text: "x"},
		AutoDriftCheck: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Verdict != "skipped" {
		t.Fatalf("expected skipped, got %q", res.Verdict)
	}
	if res.ConfidenceGrade == "" {
		t.Fatal("WIRING BROKEN: confidence_grade empty on the skipped path — " +
			"the production path is not calling the grader")
	}
	if res.ConfidenceGrade != "unverified" {
		t.Fatalf("skipped must grade unverified, got %q", res.ConfidenceGrade)
	}
	if res.ConfidenceCaveat == "" {
		t.Fatal("caveat must never be empty")
	}
	if !strings.Contains(res.ConfidenceCaveat, "no judge ran") {
		t.Errorf("caveat must say no judge ran, got %q", res.ConfidenceCaveat)
	}
}

// The same guarantee, checked on the JSON the harness actually parses.
// A Go struct field that is never tagged or never set can look correct
// in a unit test and still be absent from the tool output.
func TestWiring_L11_2_FieldsReachJSON(t *testing.T) {
	o := setupOrchForGrade(t)
	res, err := o.PublishVibe(context.Background(), PublishVibeInput{
		Spec:           PublishSpecInput{VibeCase: "C1"},
		Artifact:       PublishArtifactInput{ArtifactType: "text", ArtifactURL: "memory://w/2", Text: "x"},
		AutoDriftCheck: ptrBool(false),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"confidence_grade", "confidence_caveat"} {
		v, ok := got[key]
		if !ok {
			t.Fatalf("WIRING BROKEN: %q absent from the MCP JSON payload: %s", key, raw)
		}
		if s, _ := v.(string); s == "" {
			t.Fatalf("%q present but empty in the MCP JSON payload", key)
		}
	}
	if got["confidence_grade"] != "unverified" {
		t.Fatalf("confidence_grade in JSON = %v, want unverified", got["confidence_grade"])
	}
}

// Every verdict the production path can return must arrive graded.
// This is the structural guard against the failure Loop 11.1 hit: a
// new return path added later and never graded would ship a bare
// number again.
func TestWiring_L11_2_AllVerdictsGradeToKnownRung(t *testing.T) {
	known := map[string]bool{
		"unverified": true, "estimated": true, "modeled": true, "measured": true,
	}
	cases := []struct {
		name  string
		input PublishVibeInput
	}{
		{"skipped", PublishVibeInput{
			Spec:           PublishSpecInput{VibeCase: "C1"},
			Artifact:       PublishArtifactInput{ArtifactType: "text", ArtifactURL: "memory://w/3", Text: "x"},
			AutoDriftCheck: ptrBool(false),
		}},
		{"no-text-skipped", PublishVibeInput{
			Spec:           PublishSpecInput{VibeCase: "C1"},
			Artifact:       PublishArtifactInput{ArtifactType: "text", ArtifactURL: "memory://w/4"},
			AutoDriftCheck: ptrBool(true),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := setupOrchForGrade(t)
			res, err := o.PublishVibe(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			if !known[res.ConfidenceGrade] {
				t.Fatalf("verdict %q produced grade %q, which is not one of the four L11.1 rungs",
					res.Verdict, res.ConfidenceGrade)
			}
			if res.ConfidenceCaveat == "" {
				t.Fatalf("verdict %q produced an empty caveat", res.Verdict)
			}
		})
	}
}

// The async path returns pending before any judge runs. It must be
// graded unverified, never measured — and it must be graded even
// though the background goroutine will later set a real verdict.
func TestWiring_L11_2_AsyncPendingIsUnverified(t *testing.T) {
	o := setupOrchForGrade(t)
	res, err := o.PublishVibe(context.Background(), PublishVibeInput{
		Spec:            PublishSpecInput{VibeCase: "C1"},
		Artifact:        PublishArtifactInput{ArtifactType: "text", ArtifactURL: "memory://w/5", Text: "x"},
		AsyncDriftCheck: true,
		AutoDriftCheck:  ptrBool(false),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.Verdict != "pending" {
		t.Fatalf("expected pending, got %q", res.Verdict)
	}
	if res.ConfidenceGrade != "unverified" {
		t.Fatalf("pending must be unverified, got %q", res.ConfidenceGrade)
	}
	if !strings.Contains(res.ConfidenceCaveat, "still running") {
		t.Errorf("caveat must say the check is still running, got %q", res.ConfidenceCaveat)
	}
}

// The one derivation point. If a second place ever grades confidence,
// this test will not catch it — so the invariant is documented where a
// reader of applyConfidenceGrade will see it.
func TestWiring_L11_2_ApplyConfidenceGradeIsIdempotent(t *testing.T) {
	r := &PublishResult{Verdict: "aligned", Confidence: 0.9}
	r.applyConfidenceGrade()
	firstGrade, firstCaveat := r.ConfidenceGrade, r.ConfidenceCaveat
	r.applyConfidenceGrade()
	if r.ConfidenceGrade != firstGrade || r.ConfidenceCaveat != firstCaveat {
		t.Fatal("applyConfidenceGrade is not deterministic — the grade must not drift between calls")
	}
}
