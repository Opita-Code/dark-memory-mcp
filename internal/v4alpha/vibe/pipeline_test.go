package vibe

import (
	"context"
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// validPipelineArtifactForDB returns a minimal Artifact with the
// spec_id slot to be filled by the caller (after insertFakeSpec).
// Kept as documentation for the spec/artifact coupling; tests build
// the Artifact inline since each test wants a different URL.
func validPipelineArtifactForDB() *Artifact {
	return &Artifact{
		Type: ArtifactTypeCode,
		URL:  "file:///tmp/foo.go",
	}
}

// ---- L2 example tests ----

func TestExample_Pipeline_PublishAligned(t *testing.T) {
	db := newTestDB(t)
	p, _, j := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}

	drift, err := p.Publish(ctx, art, "aligned fixture intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if drift.Verdict != judge.VerdictAligned {
		t.Fatalf("verdict: expected aligned, got %q", drift.Verdict)
	}
	if drift.ID <= 0 {
		t.Fatalf("drift.ID: expected > 0, got %d", drift.ID)
	}
	if drift.ArtifactID <= 0 {
		t.Fatalf("drift.ArtifactID: expected > 0, got %d", drift.ArtifactID)
	}

	// Judge was called exactly once with the spec intent we passed.
	if len(j.Calls) != 1 {
		t.Fatalf("FakeJudge.Calls: expected 1, got %d", len(j.Calls))
	}
	if j.Calls[0].EvalType != "drift_judge" {
		t.Fatalf("FakeJudge.EvalType: expected drift_judge, got %q", j.Calls[0].EvalType)
	}
	if j.Calls[0].SpecIntent != "aligned fixture intent" {
		t.Fatalf("FakeJudge.SpecIntent: expected %q, got %q",
			"aligned fixture intent", j.Calls[0].SpecIntent)
	}
}

func TestExample_Pipeline_PublishDriftDetected(t *testing.T) {
	db := newTestDB(t)
	p, _, j := newTestPipeline(t, db)
	ctx := context.Background()

	j.NextVerdict = &judge.Verdict{
		Verdict: judge.VerdictDriftDetected, Confidence: 0.92, Reasoning: "fake drift",
	}
	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}

	drift, err := p.Publish(ctx, art, "drift fixture intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if drift.Verdict != judge.VerdictDriftDetected {
		t.Fatalf("verdict: expected drift_detected, got %q", drift.Verdict)
	}
	if drift.Reasoning != "fake drift" {
		t.Fatalf("reasoning: expected %q, got %q", "fake drift", drift.Reasoning)
	}
}

func TestExample_Pipeline_PublishNeedsHuman(t *testing.T) {
	db := newTestDB(t)
	p, _, j := newTestPipeline(t, db)
	ctx := context.Background()

	j.NextVerdict = &judge.Verdict{
		Verdict: judge.VerdictNeedsHuman, Confidence: 0.62, Reasoning: "needs human review",
	}
	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}

	drift, err := p.Publish(ctx, art, "needs-human fixture intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if drift.Verdict != judge.VerdictNeedsHuman {
		t.Fatalf("verdict: expected needs_human, got %q", drift.Verdict)
	}
}

func TestExample_Pipeline_PublishJudgeErrorPropagates(t *testing.T) {
	db := newTestDB(t)
	p, _, j := newTestPipeline(t, db)
	ctx := context.Background()

	j.NextErr = errors.New("network down")
	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}

	_, err := p.Publish(ctx, art, "intent")
	if err == nil {
		t.Fatal("Publish: expected error when judge fails, got nil")
	}
}

func TestExample_Pipeline_PublishRejectsInvalidArtifact(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	ctx := context.Background()

	art := &Artifact{SpecID: 9999, Type: ArtifactTypeCode, URL: "x"}
	_, err := p.Publish(ctx, art, "intent")
	if !errors.Is(err, ErrSpecNotFound) {
		t.Fatalf("missing spec: expected ErrSpecNotFound, got %v", err)
	}
}

func TestExample_Pipeline_StatusReturnsLatestDrift(t *testing.T) {
	db := newTestDB(t)
	p, _, j := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}

	// First publish: aligned.
	if _, err := p.Publish(ctx, art, "first"); err != nil {
		t.Fatalf("Publish 1: %v", err)
	}
	// Second publish: drift_detected (different artifact row, but same intent).
	j.NextVerdict = &judge.Verdict{
		Verdict: judge.VerdictDriftDetected, Confidence: 0.85, Reasoning: "later",
	}
	if _, err := p.Publish(ctx, art, "second"); err != nil {
		t.Fatalf("Publish 2: %v", err)
	}

	// Status(artID of 2nd publish) returns drift_detected.
	artID := art.ID
	got, err := p.Status(ctx, artID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.Verdict != judge.VerdictDriftDetected {
		t.Fatalf("Status verdict: expected drift_detected, got %q", got.Verdict)
	}
}

func TestExample_Pipeline_StatusReturnsErrNotFoundWhenEmpty(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	_, err := p.Status(context.Background(), 99999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status on missing artifact: expected ErrNotFound, got %v", err)
	}
}

func TestExample_Pipeline_PublishEmitsAuditRow(t *testing.T) {
	db := newTestDB(t)
	p, w, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	if _, err := p.Publish(ctx, art, "audit intent"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got := w.LastID(); got != 1 {
		t.Fatalf("audit.LastID: expected 1 (one Publish emits one audit row), got %d", got)
	}
}

// ---- L1 property tests ----

// TestProperty_Pipeline_PublishAnyCanonicalVerdict — universal claim:
// for any canonical verdict from the judge, Publish persists it
// unchanged in the drift row and Status returns it.
func TestProperty_Pipeline_PublishAnyCanonicalVerdict(t *testing.T) {
	verdicts := []string{
		judge.VerdictAligned, judge.VerdictDriftDetected, judge.VerdictNeedsHuman,
	}
	rapid.Check(t, func(t *rapid.T) {
		v := rapid.SampledFrom(verdicts).Draw(t, "verdict")
		c := rapid.Float64Range(0.0, 1.0).Draw(t, "confidence")

		db := newTestDB(t)
		p, _, j := newTestPipeline(t, db)
		j.NextVerdict = &judge.Verdict{Verdict: v, Confidence: c, Reasoning: "prop"}
		ctx := context.Background()

		specID := insertFakeSpec(t, db)
		art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
		drift, err := p.Publish(ctx, art, "prop intent")
		if err != nil {
			t.Fatalf("Publish verdict=%s conf=%f: %v", v, c, err)
		}
		if drift.Verdict != v {
			t.Fatalf("drift.Verdict: expected %q, got %q", v, drift.Verdict)
		}

		got, err := p.Status(ctx, drift.ArtifactID)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got.Verdict != v {
			t.Fatalf("Status.Verdict: expected %q, got %q", v, got.Verdict)
		}
	})
}

// TestProperty_Pipeline_PublishAlwaysEmitsOneAuditRow — universal
// claim: every successful Publish emits exactly one INV-1 audit row,
// so the audit.LastID counter increments by 1 per publish.
func TestProperty_Pipeline_PublishAlwaysEmitsOneAuditRow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "n")
		db := newTestDB(t)
		p, w, _ := newTestPipeline(t, db)
		ctx := context.Background()
		specID := insertFakeSpec(t, db)

		for i := 0; i < n; i++ {
			art := &Artifact{
				SpecID: specID, Type: ArtifactTypeCode, URL: "x",
			}
			if _, err := p.Publish(ctx, art, "audit prop intent"); err != nil {
				t.Fatalf("Publish iter %d: %v", i, err)
			}
		}
		if got := w.LastID(); got != int64(n) {
			t.Fatalf("audit.LastID: expected %d, got %d", n, got)
		}
	})
}

// TestProperty_Pipeline_AnyArbitraryURLPublishes — universal claim:
// for any non-empty URL, Publish succeeds (Pipeline doesn't care
// about URL semantics — it just persists it).
func TestProperty_Pipeline_AnyArbitraryURLPublishes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		url := rapid.StringMatching(`[a-zA-Z0-9_./:?-]{4,128}`).Draw(t, "url")
		db := newTestDB(t)
		p, _, _ := newTestPipeline(t, db)
		ctx := context.Background()
		specID := insertFakeSpec(t, db)

		art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: url}
		drift, err := p.Publish(ctx, art, "url prop")
		if err != nil {
			t.Fatalf("Publish url=%q: %v", url, err)
		}
		if drift.ID <= 0 {
			t.Fatalf("drift.ID: expected > 0, got %d", drift.ID)
		}
	})
}

// ---- Cycle 5: Resolve tests ----

func TestExample_Pipeline_ResolveAcceptMarksDrift(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	drift, err := p.Publish(ctx, art, "resolve intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := p.Resolve(ctx, drift.ID, DecisionAccept, "looks good", "operator-nico"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, err := p.Status(ctx, art.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !got.Resolved {
		t.Fatal("after Resolve, drift should be marked Resolved")
	}
	if got.Resolution != DecisionAccept {
		t.Fatalf("Resolution: expected %q, got %q", DecisionAccept, got.Resolution)
	}
	if got.OperatorID != "operator-nico" {
		t.Fatalf("OperatorID: expected %q, got %q", "operator-nico", got.OperatorID)
	}
	if got.Note != "looks good" {
		t.Fatalf("Note: expected %q, got %q", "looks good", got.Note)
	}
}

func TestExample_Pipeline_ResolveRejectMarksDrift(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	drift, err := p.Publish(ctx, art, "resolve intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := p.Resolve(ctx, drift.ID, DecisionReject, "wrong artifact", "operator-maria"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, _ := p.Status(ctx, art.ID)
	if got.Resolution != DecisionReject {
		t.Fatalf("Resolution: expected %q, got %q", DecisionReject, got.Resolution)
	}
}

func TestExample_Pipeline_ResolveIdempotent(t *testing.T) {
	// Calling Resolve twice with the same args must not error and
	// must not change the drift state.
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	drift, err := p.Publish(ctx, art, "idempotent intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := p.Resolve(ctx, drift.ID, DecisionAccept, "first", "operator-nico"); err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	if err := p.Resolve(ctx, drift.ID, DecisionAccept, "second", "operator-nico"); err != nil {
		t.Fatalf("Resolve 2 (idempotent): expected nil, got %v", err)
	}

	got, _ := p.Status(ctx, art.ID)
	if got.Note != "first" {
		t.Fatalf("Note: expected unchanged %q, got %q", "first", got.Note)
	}
}

func TestExample_Pipeline_ResolveRejectsInvalidDecision(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	drift, err := p.Publish(ctx, art, "intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	err = p.Resolve(ctx, drift.ID, "approve", "note", "operator-nico")
	if !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("invalid decision: expected ErrInvalidDecision, got %v", err)
	}
}

func TestExample_Pipeline_ResolveRejectsEmptyOperator(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	drift, err := p.Publish(ctx, art, "intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	err = p.Resolve(ctx, drift.ID, DecisionAccept, "note", "")
	if !errors.Is(err, ErrEmptyOperator) {
		t.Fatalf("empty operator: expected ErrEmptyOperator, got %v", err)
	}
}

func TestExample_Pipeline_ResolveDriftNotFound(t *testing.T) {
	db := newTestDB(t)
	p, _, _ := newTestPipeline(t, db)
	err := p.Resolve(context.Background(), 99999, DecisionAccept, "n", "operator-nico")
	if !errors.Is(err, ErrDriftNotFound) {
		t.Fatalf("missing drift: expected ErrDriftNotFound, got %v", err)
	}
}

func TestExample_Pipeline_ResolveEmitsAuditRow(t *testing.T) {
	db := newTestDB(t)
	p, w, _ := newTestPipeline(t, db)
	ctx := context.Background()

	specID := insertFakeSpec(t, db)
	art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
	drift, err := p.Publish(ctx, art, "audit-resolve intent")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := w.LastID(); got != 1 {
		t.Fatalf("audit.LastID after Publish: expected 1, got %d", got)
	}

	if err := p.Resolve(ctx, drift.ID, DecisionAccept, "looks good", "operator-nico"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := w.LastID(); got != 2 {
		t.Fatalf("audit.LastID after Resolve: expected 2, got %d", got)
	}
}

// TestProperty_Pipeline_ResolveAnyValidDecisionAccepted — universal
// claim: for any decision ∈ {accept, reject} and any non-empty
// operator, Resolve persists the decision and Status returns it.
func TestProperty_Pipeline_ResolveAnyValidDecisionAccepted(t *testing.T) {
	decisions := []string{DecisionAccept, DecisionReject}
	rapid.Check(t, func(t *rapid.T) {
		decision := rapid.SampledFrom(decisions).Draw(t, "decision")
		operator := rapid.StringMatching(`[a-z][a-z0-9_-]{3,31}`).Draw(t, "operator")
		note := rapid.StringMatching(`[A-Za-z0-9 .,!?]{0,64}`).Draw(t, "note")

		db := newTestDB(t)
		p, _, _ := newTestPipeline(t, db)
		ctx := context.Background()

		specID := insertFakeSpec(t, db)
		art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
		drift, err := p.Publish(ctx, art, "prop resolve")
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if err := p.Resolve(ctx, drift.ID, decision, note, operator); err != nil {
			t.Fatalf("Resolve decision=%s operator=%s: %v", decision, operator, err)
		}
		got, err := p.Status(ctx, art.ID)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if got.Resolution != decision {
			t.Fatalf("Resolution: expected %q, got %q", decision, got.Resolution)
		}
		if got.OperatorID != operator {
			t.Fatalf("OperatorID: expected %q, got %q", operator, got.OperatorID)
		}
	})
}

// TestProperty_Pipeline_ResolveRejectsEmptyOperator — universal claim:
// empty operator is always rejected (defense-in-depth on INV-1).
func TestProperty_Pipeline_ResolveRejectsEmptyOperator(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		db := newTestDB(t)
		p, _, _ := newTestPipeline(t, db)
		ctx := context.Background()

		specID := insertFakeSpec(t, db)
		art := &Artifact{SpecID: specID, Type: ArtifactTypeCode, URL: "x"}
		drift, err := p.Publish(ctx, art, "prop empty-operator")
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		err = p.Resolve(ctx, drift.ID, DecisionAccept, "n", "")
		if !errors.Is(err, ErrEmptyOperator) {
			t.Fatalf("empty operator: expected ErrEmptyOperator, got %v", err)
		}
	})
}
