package event

// DelegationProgressEmitter — Phase 12 T-103c: auto-emit progress events
// from the DECIDE→EXTRACT→MIND→CURATE pipeline (delegate_intent).
//
// The v4alpha delegate_intent pipeline runs 4 sequential phases:
//
//	1. DECIDE  (deterministic, ~ms)
//	2. EXTRACT (LLM call, ~5-30s when ShouldExtract fires)
//	3. MIND    (composeSystemPrompt per subtask, ~ms per subtask)
//	4. CURATE  (subagent bindings via agent_memory.Save, ~5-50ms each)
//
// This helper emits 1 root + up to 4 child progress events through the
// pipeline:
//
//	EmitDecide       → root, phase=started,        progress_pct=0
//	EmitExtract      → child of root, phase=running, progress_pct=25
//	EmitMind         → child of root, phase=running, progress_pct=50
//	EmitCurate       → child of root, phase=running, progress_pct=75
//	EmitCompleted    → child of root, phase=completed, progress_pct=100
//	EmitFailed       → child of root, phase=failed,    error_msg=...
//
// Design notes (per SPEC §4.3 T-103c + LangFuse §3.3 tree semantics):
//   - EmitDecide is SYNC so we capture the root event_id; the other
//     5 emits reference it as parent_event_id (LangFuse tree).
//   - root_event_id == parent_event_id (one-level tree — the
//     delegate_intent call is the root, its phases are children).
//   - process_id = "delegate-<taskID>" — stable across all events so
//     observers can correlate the full pipeline even without
//     parent_event_id.
//   - target_table="delegation_trees" so ListEventsByTable filters
//     can pull only the delegation subtree.

import (
	"context"
	"fmt"
)

// DelegationProgressEmitter wraps a *Writer and emits a tree of
// progress events for one delegate_intent call. Each call must use
// a fresh emitter (the rootEventID is set on EmitDecide and reused
// by the 4 subsequent emits).
//
// Safe for concurrent use IF you only call EmitDecide once (the
// rootEventID field is mutated on EmitDecide and read by the 5
// downstream emits; concurrent EmitDecide would race). The intended
// pattern is: one emitter per delegate_intent call.
type DelegationProgressEmitter struct {
	writer      *Writer
	projectID   string
	actor       string
	rootEventID int64 // 0 until EmitDecide succeeds
	rootSet     bool
}

// NewDelegationProgressEmitter constructs the emitter. writer must
// be non-nil; the emitter will panic otherwise (programmer error).
// projectID + actor are the events-table bookkeeping fields; pass
// "" for either and the emitter uses safe defaults.
func NewDelegationProgressEmitter(w *Writer, projectID, actor string) *DelegationProgressEmitter {
	if w == nil {
		panic("event.NewDelegationProgressEmitter: writer must be non-nil (programmer error)")
	}
	if projectID == "" {
		projectID = "default"
	}
	if actor == "" {
		actor = "orchestrator_delegate"
	}
	return &DelegationProgressEmitter{writer: w, projectID: projectID, actor: actor}
}

// processID returns the canonical async-work identifier for a
// delegate_intent call. Stable across all 5+ events so observers
// can correlate.
func (e *DelegationProgressEmitter) processID(taskID string) string {
	if taskID == "" {
		taskID = "unknown"
	}
	return fmt.Sprintf("delegate-%s", taskID)
}

// RootEventID returns the root event id (set by EmitDecide). 0 if
// EmitDecide has not yet been called or failed.
func (e *DelegationProgressEmitter) RootEventID() int64 {
	return e.rootEventID
}

// EmitDecide emits the ROOT progress event for the delegation tree.
// SYNC (returns int64 so the emitter captures it as parent_event_id
// for downstream emits). progress_pct=0, phase=started.
// Returns the assigned event id on success; 0 + error on failure
// (caller decides whether to continue — typically yes, since the
// remaining emits reference parent_event_id=0 which still produces
// observable events).
func (e *DelegationProgressEmitter) EmitDecide(ctx context.Context, taskID, decision, reason string) (int64, error) {
	id, err := e.writer.Emit(ctx, Event{
		Kind:        KindProgress,
		ProjectID:   e.projectID,
		Actor:       e.actor,
		ProcessID:   e.processID(taskID),
		TargetTable: "delegation_trees",
		Phase:       PhaseStarted,
		ProgressPct: pctPtr(0),
		Message:     fmt.Sprintf("DECIDE: %s (%s)", decision, truncateReason(reason)),
		Source:      "v4alpha/transport/mcp/wire.go:RunDelegateIntentCore",
	})
	if err == nil && id > 0 {
		e.rootEventID = id
		e.rootSet = true
	}
	return id, err
}

// EmitExtract emits phase=running for the EXTRACT step. ASYNC.
// progress_pct=25 (1/4 of pipeline). verdict = "aligned" | "drift_detected"
// | "needs_human" | "cached" | "errored" — whichever the EXTRACT step
// returned. subtaskCount = len(extractResult.Subtasks).
func (e *DelegationProgressEmitter) EmitExtract(ctx context.Context, taskID, verdict string, subtaskCount int) {
	e.writer.EmitAsync(ctx, Event{
		Kind:          KindProgress,
		ProjectID:     e.projectID,
		Actor:         e.actor,
		ProcessID:     e.processID(taskID),
		ParentEventID: int64Ptr(e.rootEventID),
		RootEventID:   int64Ptr(e.rootEventID),
		TargetTable:   "delegation_trees",
		Phase:         PhaseRunning,
		ProgressPct:   pctPtr(25),
		JudgeVerdict:  verdict,
		Message:       fmt.Sprintf("EXTRACT: verdict=%s subtasks=%d", verdict, subtaskCount),
		Source:        "v4alpha/transport/mcp/wire.go:RunDelegateIntentCore",
	})
}

// EmitMind emits phase=running for the MIND step (composeSystemPrompt
// per subtask). ASYNC. progress_pct=50.
func (e *DelegationProgressEmitter) EmitMind(ctx context.Context, taskID string, subtaskCount int) {
	e.writer.EmitAsync(ctx, Event{
		Kind:          KindProgress,
		ProjectID:     e.projectID,
		Actor:         e.actor,
		ProcessID:     e.processID(taskID),
		ParentEventID: int64Ptr(e.rootEventID),
		RootEventID:   int64Ptr(e.rootEventID),
		TargetTable:   "delegation_trees",
		Phase:         PhaseRunning,
		ProgressPct:   pctPtr(50),
		Message:       fmt.Sprintf("MIND: composed system_prompt for %d subtasks", subtaskCount),
		Source:        "v4alpha/transport/mcp/wire.go:RunDelegateIntentCore",
	})
}

// EmitCurate emits phase=running for the CURATE step (subagent
// bindings via agent_memory.Save). ASYNC. progress_pct=75.
func (e *DelegationProgressEmitter) EmitCurate(ctx context.Context, taskID string, subagentCount int) {
	e.writer.EmitAsync(ctx, Event{
		Kind:          KindProgress,
		ProjectID:     e.projectID,
		Actor:         e.actor,
		ProcessID:     e.processID(taskID),
		ParentEventID: int64Ptr(e.rootEventID),
		RootEventID:   int64Ptr(e.rootEventID),
		TargetTable:   "delegation_trees",
		Phase:         PhaseRunning,
		ProgressPct:   pctPtr(75),
		Message:       fmt.Sprintf("CURATE: registered %d subagent bindings", subagentCount),
		Source:        "v4alpha/transport/mcp/wire.go:RunDelegateIntentCore",
	})
}

// EmitCompleted emits phase=completed, progress_pct=100, with the
// final verdict + subtask count + duration_ms. SYNC (the pipeline
// is done; we want to block until the events row lands so observers
// polling process_id see the final state).
func (e *DelegationProgressEmitter) EmitCompleted(ctx context.Context, taskID, verdict string, subtaskCount int, durationMs int64) {
	_, _ = e.writer.Emit(ctx, Event{
		Kind:           KindProgress,
		ProjectID:      e.projectID,
		Actor:          e.actor,
		ProcessID:      e.processID(taskID),
		ParentEventID:  int64Ptr(e.rootEventID),
		RootEventID:    int64Ptr(e.rootEventID),
		TargetTable:    "delegation_trees",
		Phase:          PhaseCompleted,
		ProgressPct:    pctPtr(100),
		DurationMs:     int64Ptr(durationMs),
		JudgeVerdict:   verdict,
		JudgeReasoning: fmt.Sprintf("delegate_intent completed in %dms", durationMs),
		Message:        fmt.Sprintf("delegate_intent completed: verdict=%s subtasks=%d", verdict, subtaskCount),
		Source:         "v4alpha/transport/mcp/wire.go:RunDelegateIntentCore",
	})
}

// EmitFailed emits phase=failed with the failing phase + error.
// SYNC (the pipeline failed; we want the failure event landed before
// the caller returns so observers see the failure).
func (e *DelegationProgressEmitter) EmitFailed(ctx context.Context, taskID, failPhase, errMsg string, durationMs int64) {
	_, _ = e.writer.Emit(ctx, Event{
		Kind:          KindProgress,
		ProjectID:     e.projectID,
		Actor:         e.actor,
		ProcessID:     e.processID(taskID),
		ParentEventID: int64Ptr(e.rootEventID),
		RootEventID:   int64Ptr(e.rootEventID),
		TargetTable:   "delegation_trees",
		Phase:         PhaseFailed,
		ProgressPct:   pctPtr(0), // we don't know how far it got before the failure
		DurationMs:    int64Ptr(durationMs),
		ErrorMsg:      errMsg,
		Message:       fmt.Sprintf("delegate_intent failed in %s after %dms: %s", failPhase, durationMs, errMsg),
		Source:        "v4alpha/transport/mcp/wire.go:RunDelegateIntentCore",
	})
}

// truncateReason keeps the reason field under 200 chars for the
// events.Message column. The full reasoning is still available in
// delegate_intent's wire response.
func truncateReason(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:197] + "..."
}