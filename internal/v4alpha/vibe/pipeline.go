package vibe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// Pipeline orchestrates the vibe-loop:
//   Publish(artifact, specIntent) → insert artifact → judge → insert drift → audit
//   Status(artifactID)            → latest drift
//   Resolve(...)                  → operator gate (cycle 5)
//
// The Pipeline owns the three stores (spec, artifact, drift) and
// composes them with the configured Judge implementation. All
// persistence is to a single *sql.DB shared with audit.Writer.
type Pipeline struct {
	db        *sql.DB
	audit     *audit.Writer
	specs     *SpecStore
	artifacts *ArtifactStore
	drifts    *DriftStore
	judge     judge.Judge
}

// NewPipeline wires the dependencies. The caller MUST have run
// CreateSpecSchema, CreateArtifactSchema, CreateDriftSchema, and
// CreateSchema (for audit_log) before calling Publish.
func NewPipeline(db *sql.DB, w *audit.Writer, j judge.Judge) *Pipeline {
	return &Pipeline{
		db:        db,
		audit:     w,
		specs:     NewSpecStore(db),
		artifacts: NewArtifactStore(db),
		drifts:    NewDriftStore(db),
		judge:     j,
	}
}

// Specs returns the underlying SpecStore so callers can look up
// a spec's intent before publishing. The transport/mcp package
// uses this to derive spec_intent when the publish tool doesn't
// pass one explicitly.
func (p *Pipeline) Specs() *SpecStore { return p.specs }

// Artifacts returns the underlying ArtifactStore.
func (p *Pipeline) Artifacts() *ArtifactStore { return p.artifacts }

// Drifts returns the underlying DriftStore.
func (p *Pipeline) Drifts() *DriftStore { return p.drifts }

// Publish inserts the artifact, judges it, and inserts the drift
// row. Returns the new DriftReport (with ID + EvaluatedAt populated).
//
// specIntent is the operator's one-paragraph "what should this
// artifact be" hypothesis (forwarded to the Judge). It is NOT
// persisted in v4-alpha (it lives in the spec row, accessed via
// artifact.SpecID → spec.Intent in a later slice).
//
// Phase 4 Chunk 4.3: art.ProjectID is propagated to the audit_log
// row via audit.Writer.WriteWithProject (when non-empty). Empty
// falls back to audit.Writer.Write (audit row gets project_id=
// 'default' via column DEFAULT). Same pattern as audit_log +
// session.Start hard isolation.
//
// Lifecycle:
//   1. Validate artifact (defense-in-depth; caller may have skipped).
//   2. Insert artifact → artID (artifact row's project_id column
//      is also set, via ArtifactStore.Insert).
//   3. Judge.Evaluate → verdict.
//   4. Insert drift with verdict → driftID.
//   5. Emit one INV-1 audit row tagged with "pipeline" and
//      art.ProjectID (when set).
func (p *Pipeline) Publish(ctx context.Context, art *Artifact, specIntent string) (*DriftReport, error) {
	if err := art.Validate(); err != nil {
		return nil, fmt.Errorf("pipeline Publish: %w", err)
	}

	artID, err := p.artifacts.Insert(ctx, art)
	if err != nil {
		return nil, fmt.Errorf("pipeline Publish artifact: %w", err)
	}

	verdict, err := p.judge.Evaluate(ctx, "drift_judge", specIntent,
		artifactRefToJudge(art.Ref))
	if err != nil {
		return nil, fmt.Errorf("pipeline Publish judge: %w", err)
	}
	if err := verdict.Validate(); err != nil {
		return nil, fmt.Errorf("pipeline Publish verdict: %w", err)
	}

	drift := &DriftReport{
		ArtifactID: artID,
		Verdict:    verdict.Verdict,
		Confidence: verdict.Confidence,
		Reasoning:  verdict.Reasoning,
	}
	driftID, err := p.drifts.Insert(ctx, drift)
	if err != nil {
		return nil, fmt.Errorf("pipeline Publish drift: %w", err)
	}

	payload := []byte(fmt.Sprintf(
		`{"event":"vibe.publish","artifact_id":%d,"drift_id":%d,"verdict":%q,"confidence":%f}`,
		artID, driftID, verdict.Verdict, verdict.Confidence,
	))
	if err := p.writeAuditWithProject(ctx, art.ProjectID, payload); err != nil {
		return nil, fmt.Errorf("pipeline Publish audit: %w", err)
	}

	return drift, nil
}

// writeAuditWithProject emits one audit_log row with the project_id
// stamped (Phase 4 Chunk 4.3). Empty projectID falls back to
// audit.Write (the row gets project_id='default' via the column
// DEFAULT — pre-Phase-4 contract preserved for callers that don't
// set ProjectID).
func (p *Pipeline) writeAuditWithProject(ctx context.Context, projectID string, payload []byte) error {
	if projectID != "" {
		_, err := p.audit.WriteWithProject(ctx, "pipeline", "", projectID, payload)
		return err
	}
	_, err := p.audit.Write(ctx, "pipeline", "", payload)
	return err
}

// Status returns the latest drift report for an artifact, or
// ErrNotFound if no drift has been recorded.
func (p *Pipeline) Status(ctx context.Context, artifactID int64) (*DriftReport, error) {
	return p.drifts.Status(ctx, artifactID)
}

// Resolve gates a drift with an operator decision (accept or reject).
//
// Lifecycle:
//   1. Load the drift via DriftStore.Get (returns ErrDriftNotFound
//      when the id does not exist; the package-level ErrNotFound
//      sentinel is mapped here for API precision).
//   2. If already resolved, return nil (idempotent: re-Resolve is OK).
//   3. Validate decision ∈ {accept, reject}; operator ≠ "".
//   4. Apply UPDATE via DriftStore.Resolve.
//   5. Emit one INV-1 audit row tagged with the operator.
//
// INV-1 enforcement: the audit row's actor is the operator id, so
// every resolve is traceable to a human/agent identity.
func (p *Pipeline) Resolve(ctx context.Context, driftID int64, decision, note, operator string) error {
	d, err := p.drifts.Get(ctx, driftID)
	if errors.Is(err, ErrNotFound) {
		return ErrDriftNotFound
	}
	if err != nil {
		return fmt.Errorf("pipeline Resolve lookup: %w", err)
	}
	if d.Resolved {
		// Idempotent: already resolved. No-op, no audit row.
		return nil
	}
	if !IsValidDecision(decision) {
		return fmt.Errorf("%w: got %q", ErrInvalidDecision, decision)
	}
	if operator == "" {
		return ErrEmptyOperator
	}
	if operator == "" {
		return ErrEmptyOperator
	}

	if err := p.drifts.Resolve(ctx, driftID, decision, note, operator); err != nil {
		return fmt.Errorf("pipeline Resolve update: %w", err)
	}

	if _, err := p.audit.Write(ctx, operator, "",
		[]byte(fmt.Sprintf(
			`{"event":"vibe.resolve","drift_id":%d,"decision":%q,"artifact_id":%d}`,
			driftID, decision, d.ArtifactID,
		))); err != nil {
		return fmt.Errorf("pipeline Resolve audit: %w", err)
	}
	return nil
}

// artifactRefToJudge converts a vibe.ArtifactRef to a judge.Ref so
// the Judge interface stays decoupled from the persistence layer.
func artifactRefToJudge(ref *ArtifactRef) *judge.Ref {
	if ref == nil {
		return nil
	}
	return &judge.Ref{
		Kind:       ref.Kind,
		Path:       ref.Path,
		GitSHA:     ref.GitSHA,
		GitRepo:    ref.GitRepo,
		URL:        ref.URL,
		SpecID:     ref.SpecID,
		ArtifactID: ref.ArtifactID,
		MaxBytes:   ref.MaxBytes,
	}
}
