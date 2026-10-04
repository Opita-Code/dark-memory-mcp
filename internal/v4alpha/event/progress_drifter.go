package event

// DriftJudgeProgressEmitter — Phase 12 T-103b: auto-emit progress events
// from the async drift_judge background path.
//
// The async path in internal/orchestration/publish_vibe.go
// runAsyncJudgePipeline runs drift_judge for ~120s in a detached
// goroutine. Without progress emission, operators polling
// pipeline_status(artifact_id=X) see "pending" with no insight into
// whether the background judge is alive, how far along it is, or
// where it died on error.
//
// This helper emits 4 progress events through the lifecycle:
//
//	EmitStarted       → kind=progress, phase=started,        progress_pct=0
//	EmitInProgress    → kind=progress, phase=running,        progress_pct=25/50/75
//	EmitCompleted     → kind=progress, phase=completed,      progress_pct=100, duration_ms=N
//	EmitFailed        → kind=progress, phase=failed,         error_msg=...
//
// All 4 emits go through Writer.EmitAsync (Q2 operator decision:
// progress events are always async — never block the producer).
//
// Design notes (per SPEC §4.3 T-103b + dark-testing A1+A8):
//   - Fire-and-forget: never return an error. Failures are logged
//     via Writer.logger. The drift_judge is the primary action;
//     progress emission is for observability.
//   - process_id: identical across the 4 events so observers can
//     correlate "this async drift_judge" across the lifecycle.
//     Format: "drift-<artifactID>" (matches the convention from
//     sqlite events_test.go:140 "gen_ai.operation.name":"drift_judge").
//   - target_table="drift_report" so a ListEventsByTable filter
//     can scope the events table to drift reports only.

import (
	"context"
	"fmt"
)

// DriftJudgeProgressEmitter wraps a *Writer and emits 4 progress
// events for the async drift_judge lifecycle. Safe for concurrent
// use (the underlying Writer is goroutine-safe; this struct holds
// only an immutable reference).
type DriftJudgeProgressEmitter struct {
	writer    *Writer
	projectID string // active project (default = "default")
	actor     string // typically "orchestrator_publish_vibe_async"
}

// NewDriftJudgeProgressEmitter constructs the emitter. writer must
// be non-nil; the emitter will panic otherwise (programmer error,
// not user input). projectID + actor are the events-table bookkeeping
// fields; pass "" for either and NewDriftJudgeProgressEmitter will
// use safe defaults ("default" + "orchestrator").
func NewDriftJudgeProgressEmitter(w *Writer, projectID, actor string) *DriftJudgeProgressEmitter {
	if w == nil {
		panic("event.NewDriftJudgeProgressEmitter: writer must be non-nil (programmer error)")
	}
	if projectID == "" {
		projectID = "default"
	}
	if actor == "" {
		actor = "orchestrator_publish_vibe_async"
	}
	return &DriftJudgeProgressEmitter{writer: w, projectID: projectID, actor: actor}
}

// processID returns the canonical async-work identifier for an
// artifact's drift_judge. Stable across all 4 events in the
// lifecycle so observers can correlate.
func (e *DriftJudgeProgressEmitter) processID(artifactID int64) string {
	return fmt.Sprintf("drift-%d", artifactID)
}

// pctPtr returns &v as a *float64 (helper for ProgressPct).
func pctPtr(v float64) *float64 { return &v }

// int64Ptr returns &v as a *int64 (helper for TargetRowID / DurationMs).
func int64Ptr(v int64) *int64 { return &v }

// EmitStarted emits phase=started, progress_pct=0. Call as the FIRST
// thing in runAsyncJudgePipeline's goroutine, BEFORE invoking
// runJudgePipeline. process_id = "drift-<artifactID>".
func (e *DriftJudgeProgressEmitter) EmitStarted(ctx context.Context, artifactID, specID int64, sessionID string) {
	e.writer.EmitAsync(ctx, Event{
		Kind:        KindProgress,
		ProjectID:   e.projectID,
		Actor:       e.actor,
		SessionID:   sessionID,
		ProcessID:   e.processID(artifactID),
		TargetTable: "drift_report",
		TargetRowID: int64Ptr(artifactID),
		Phase:       PhaseStarted,
		ProgressPct: pctPtr(0),
		Message:     "drift_judge background goroutine started",
		Source:      "orchestration/publish_vibe.go:runAsyncJudgePipeline",
	})
	_ = specID // reserved for future drill-down (spec link)
}

// EmitInProgress emits phase=running with the given progress_pct
// (0..100). Call periodically while runJudgePipeline is running.
// e.g., after the brand_match branch returns, after the
// compliance_check branch returns, after the drift_judge itself
// returns. Clamped to [0, 100].
func (e *DriftJudgeProgressEmitter) EmitInProgress(ctx context.Context, artifactID int64, sessionID string, pct float64) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	e.writer.EmitAsync(ctx, Event{
		Kind:        KindProgress,
		ProjectID:   e.projectID,
		Actor:       e.actor,
		SessionID:   sessionID,
		ProcessID:   e.processID(artifactID),
		TargetTable: "drift_report",
		TargetRowID: int64Ptr(artifactID),
		Phase:       PhaseRunning,
		ProgressPct: pctPtr(pct),
		Message:     fmt.Sprintf("drift_judge in progress (%.0f%%)", pct),
		Source:      "orchestration/publish_vibe.go:runAsyncJudgePipeline",
	})
}

// EmitCompleted emits phase=completed, progress_pct=100, with the
// final verdict + confidence + duration. Call on the happy path
// after the drift report has been updated in place.
func (e *DriftJudgeProgressEmitter) EmitCompleted(ctx context.Context, artifactID int64, sessionID, verdict string, confidence float32, durationMs int64) {
	e.writer.EmitAsync(ctx, Event{
		Kind:           KindProgress,
		ProjectID:      e.projectID,
		Actor:          e.actor,
		SessionID:      sessionID,
		ProcessID:      e.processID(artifactID),
		TargetTable:    "drift_report",
		TargetRowID:    int64Ptr(artifactID),
		Phase:          PhaseCompleted,
		ProgressPct:    pctPtr(100),
		DurationMs:     int64Ptr(durationMs),
		JudgeVerdict:   verdict,
		Confidence:     float64Ptr(float64(confidence)),
		JudgeReasoning: fmt.Sprintf("drift_judge async completed in %dms", durationMs),
		Message:        fmt.Sprintf("drift_judge completed: verdict=%s confidence=%.2f", verdict, confidence),
		Source:         "orchestration/publish_vibe.go:runAsyncJudgePipeline",
	})
}

// EmitFailed emits phase=failed with the error message. Call on
// panic recovery or unrecoverable judge error. durationMs is the
// elapsed time before failure (for forensic time-to-fail analysis).
func (e *DriftJudgeProgressEmitter) EmitFailed(ctx context.Context, artifactID int64, sessionID, errMsg string, durationMs int64) {
	e.writer.EmitAsync(ctx, Event{
		Kind:        KindProgress,
		ProjectID:   e.projectID,
		Actor:       e.actor,
		SessionID:   sessionID,
		ProcessID:   e.processID(artifactID),
		TargetTable: "drift_report",
		TargetRowID: int64Ptr(artifactID),
		Phase:       PhaseFailed,
		ProgressPct: pctPtr(0), // we don't know how far it got
		DurationMs:  int64Ptr(durationMs),
		ErrorMsg:    errMsg,
		Message:     fmt.Sprintf("drift_judge failed after %dms: %s", durationMs, errMsg),
		Source:      "orchestration/publish_vibe.go:runAsyncJudgePipeline",
	})
}

// float64Ptr returns &v as a *float64 (helper for Confidence).
func float64Ptr(v float64) *float64 { return &v }