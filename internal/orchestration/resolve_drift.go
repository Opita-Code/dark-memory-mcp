// O11: ResolveDrift — the human-gate action. Given a drift_id + a
// decision (accept | reject), updates the artifact's
// validation_status + the drift's reconciled_at. Use this when the
// LLM-as-judge returned drift_detected or needs_human and a human
// has reviewed the artifact.
//
// Decision semantics:
//
//	accept — the artifact IS correct as-is. Update artifact.validation_status
//	         to "passed" and stamp drift.reconciled_at with the
//	         operator's note. Use this when drift_detected was a
//	         false positive.
//	reject — the artifact is wrong. Update artifact.validation_status
//	         to "failed" and stamp drift.reconciled_at. The artifact
//	         stays in the DB (for audit) but is marked failed so
//	         callers filter it out.
//
// Already-resolved drifts (reconciled_at set) cannot be resolved
// again — returns ErrInvalidState. Use case: a tool retries by
// mistake; we surface the error rather than silently overwriting
// the prior decision.
//
// INV-1 (write-path audit): every SaveDriftReport + UpdateArtifact
// emits a write_audit row through the Store layer.
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// ResolveDriftDecision is the operator's call on a drift report.
type ResolveDriftDecision string

const (
	DecisionAccept ResolveDriftDecision = "accept" // artifact correct as-is
	DecisionReject ResolveDriftDecision = "reject" // artifact wrong
)

// ResolveDriftInput is the request to resolve one drift.
type ResolveDriftInput struct {
	DriftID    int64                  `json:"drift_id"`
	Decision   ResolveDriftDecision   `json:"decision"`
	OperatorID string                 `json:"operator_id"` // who is resolving; recorded in drift
	Note       string                 `json:"note,omitempty"`
}

// ResolveDriftResult is the outcome of the resolution.
type ResolveDriftResult struct {
	DriftID         int64  `json:"drift_id"`
	ArtifactID      int64  `json:"artifact_id"`
	PreviousVerdict string `json:"previous_verdict"`
	Decision        string `json:"decision"`
	NewStatus       string `json:"new_status"` // passed (accept) | failed (reject)
	ResolvedAt      string `json:"resolved_at"`
	OperatorID      string `json:"operator_id"`
	Note            string `json:"note,omitempty"`
}

// ResolveDrift updates the drift + artifact to reflect the operator's
// decision. See package doc for semantics.
//
// Returns ErrInvalidArgument if Decision is not in {accept, reject}
// or OperatorID is empty. Returns ErrNotFound if DriftID does not
// exist. Returns ErrInvalidState if the drift was already reconciled.
func (o *Orchestrator) ResolveDrift(ctx context.Context, in ResolveDriftInput) (*ResolveDriftResult, error) {
	// 1. Validate.
	if in.DriftID == 0 {
		return nil, errMissingField("drift_id")
	}
	if in.Decision != DecisionAccept && in.Decision != DecisionReject {
		return nil, fmt.Errorf("%w: decision must be 'accept' or 'reject'", store.ErrInvalidArgument)
	}
	if strings.TrimSpace(in.OperatorID) == "" {
		return nil, errMissingField("operator_id")
	}

	// 2. Fetch the drift referenced by DriftID. We scan
	// ListDriftReports because the Store interface doesn't expose
	// GetDriftByID in v1 (vibeflow.DriftReport.DriftID resolution
	// would benefit from a future Store.GetDriftReport(driftID) —
	// out of scope for this wave).
	drift, err := findDriftByID(ctx, o.Store, in.DriftID)
	if err != nil {
		return nil, fmt.Errorf("resolve_drift: find drift: %w", err)
	}
	if drift == nil {
		return nil, fmt.Errorf("%w: drift_id=%d", store.ErrNotFound, in.DriftID)
	}

	// 3. Reject double-resolve. We check BOTH the original drift
	// (by DriftID) AND the latest drift for the artifact (which
	// catches the case where the operator resolves twice and the
	// second call would otherwise INSERT a new drift row).
	if drift.ReconciledAt != "" {
		return nil, fmt.Errorf("%w: drift %d already reconciled at %s",
			store.ErrInvalidState, in.DriftID, drift.ReconciledAt)
	}
	latest, err := o.Store.LatestDriftForArtifact(ctx, drift.ArtifactID)
	if err == nil && latest != nil && latest.ReconciledAt != "" {
		return nil, fmt.Errorf("%w: artifact %d already has a reconciled drift (%d) at %s",
			store.ErrInvalidState, drift.ArtifactID, latest.ID, latest.ReconciledAt)
	}

	// 4. Compute new artifact validation_status from decision.
	newStatus := "passed"
	if in.Decision == DecisionReject {
		newStatus = "failed"
	}

	// 5. Stamp drift.reconciled_at with operator note. We create a
	// NEW drift row (Store.SaveDriftReport is INSERT-only) carrying
	// the reconciliation stamp. The original drift stays in the
	// table for audit; the latest drift is the one with the
	// decision.
	now := o.now().Format(time.RFC3339Nano)
	resolvedNote := fmt.Sprintf("operator=%s decision=%s note=%s", in.OperatorID, in.Decision, in.Note)
	resolvedDrift := &vibeflow.DriftReport{
		ArtifactID:     drift.ArtifactID,
		SpecID:         drift.SpecID,
		Verdict:        drift.Verdict,
		SpecDiff:       drift.SpecDiff,
		JudgeReasoning: drift.JudgeReasoning + "\n[resolved] " + resolvedNote,
		ReconciledAt:   now,
	}

	wc := store.WriteContext{
		Actor:     "orchestrator_resolve_drift",
		WritePath: "ResolveDrift",
	}

	// 6. Persist the resolved drift (creates a new row).
	newDriftID, err := o.Store.SaveDriftReport(ctx, wc, resolvedDrift)
	if err != nil {
		return nil, fmt.Errorf("resolve_drift: save drift: %w", err)
	}

	// 7. Update artifact validation_status.
	if err := o.Store.SetArtifactValidation(ctx, wc, drift.ArtifactID, newStatus); err != nil {
		return nil, fmt.Errorf("resolve_drift: set artifact validation: %w", err)
	}

	return &ResolveDriftResult{
		DriftID:         newDriftID,
		ArtifactID:      drift.ArtifactID,
		PreviousVerdict: drift.Verdict,
		Decision:        string(in.Decision),
		NewStatus:       newStatus,
		ResolvedAt:      now,
		OperatorID:      in.OperatorID,
		Note:            in.Note,
	}, nil
}

// findDriftByID scans ListDriftReports to find a specific drift.
// O(N) in number of drifts; acceptable for v1 (drift tables are
// small). Future: add Store.GetDriftReport(driftID).
func findDriftByID(ctx context.Context, s store.Store, driftID int64) (*vibeflow.DriftReport, error) {
	all, err := s.ListDriftReports(ctx, 0, "", 10000)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == driftID {
			return &all[i], nil
		}
	}
	return nil, nil
}