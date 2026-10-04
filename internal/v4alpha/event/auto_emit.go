// Package event — auto_emit.go: Auto-emit helpers for the 8 system-side
// modification sites listed in SPEC-alpha-11-phase12 §4.3 T-103a.
//
// # Why this file exists
//
// Phase 12 closes the "silent modifications" hole: dark-memory v4-alpha.22
// records agent-side writes (write_audit) but NOT system-side changes
// like decay refreshes, mark_superseded, calibration updates, etc. The
// fix is to emit a modification event at each modification site.
//
// # Pattern
//
// Each system-side modification site calls an AutoEmitter helper AFTER
// its data write succeeds. The helper fills the event's boilerplate
// (source, classification, rationale_kind) and emits via the Writer
// (sync for structural ones, async per Q2 decision for cosmetic ones).
//
// Helpers use EmitAsync for non-blocking semantics — callers don't wait
// on event emission. Rationale is REQUIRED (INV-20 enforced by Writer);
// sentinel is auto-emitted on violation (combo (a)+(c)).
//
// # Q1 selectivo (semantic-affecting-only for cache)
//
// Cache invalidation has two flavors:
//
//   - Pure LRU eviction / TTL expiry: NO EVENT (no semantic change)
//   - Semantic-affecting invalidation: EVENT (e.g., after a schema
//     migration invalidates the entire recall cache)
//
// EmitCacheInvalidation takes a `semantic bool` flag. Callers MUST set
// `semantic=true` only when the invalidation changes recall behavior.
//
// # Q2 INDIAN (sync/async split)
//
// Per the Q2 operator decision (2026-10-04):
//
//   - Structural modifications (mark_superseded, schema_migration,
//     persona_update): SYNC. Emits block caller. Should not fail the
//     underlying write if emit fails (logged).
//   - Cosmetic modifications (decay refresh, calibration update,
//     embedder refresh, judge verdict update): ASYNC. EmitAsync.
//   - Cache invalidation (when semantic): ASYNC.
//
// All helpers below follow this split.
//
// # File map
//
//	auto_emit.go        — AutoEmitter + 8 Emit* helpers (this file)
//	auto_emit_test.go   — 8 tests, one per Emit* helper
package event

import (
	"context"
	"fmt"
)

// AutoEmitter wraps a *Writer and exposes one Emit* helper per
// modification site. Use NewAutoEmitter to construct.
//
// Thread-safe: delegates concurrency to the underlying *Writer's mutex.
type AutoEmitter struct {
	w *Writer
}

// NewAutoEmitter returns an AutoEmitter bound to w. Panics if w is nil
// (programming error).
func NewAutoEmitter(w *Writer) *AutoEmitter {
	if w == nil {
		panic("event: NewAutoEmitter: w is nil")
	}
	return &AutoEmitter{w: w}
}

// emit wraps Writer.Emit with a logged-and-swallowed failure. The
// underlying data write is what matters; the event is for audit. If
// emit fails (e.g., INV-20 violation → ErrRationaleRequired), the
// failure is logged via the Writer's logger (the sentinel row IS in
// the DB, so it's auditable) but the helper returns nil so callers
// don't have to handle the rare emit error.
func (ae *AutoEmitter) emit(ctx context.Context, ev Event) {
	if ae == nil || ae.w == nil {
		return
	}
	if _, err := ae.w.Emit(ctx, ev); err != nil {
		// The sentinel (when applicable) was already emitted by Writer.Emit.
		// Just log via the Writer's logger (best-effort). If the writer's
		// logger is nil, fall back to fmt.Errorf to stderr.
		if ae.w.logger != nil {
			ae.w.logger.Printf("event: auto_emit failed (kind=%s, source=%s): %v",
				ev.Kind, ev.Source, err)
		} else {
			fmt.Printf("event: auto_emit failed (kind=%s, source=%s): %v\n",
				ev.Kind, ev.Source, err)
		}
	}
}

// emitAsync wraps Writer.EmitAsync for cosmetic modifications.
func (ae *AutoEmitter) emitAsync(ctx context.Context, ev Event) {
	if ae == nil || ae.w == nil {
		return
	}
	ae.w.EmitAsync(ctx, ev)
}

// ---------------------------------------------------------------------------
// 1. mark_superseded (T-103a #3)
// ---------------------------------------------------------------------------

// EmitSupersede emits a modification event for a bitemporal supersession.
// Caller passes the reason (already validated non-empty upstream by the store
// layer). Target row = oldMemID (the row being superseded).
//
// Structural per Q2 — SYNC. Rationale = the operator-supplied reason.
func (ae *AutoEmitter) EmitSupersede(
	ctx context.Context,
	oldMemID, newMemID int64,
	trigger, reason string,
) {
	ae.emit(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:mark_superseded",
		TargetTable:    "agent_memory",
		TargetRowID:    &oldMemID,
		Operation:      "supersede",
		Classification: ClassificationSupersede,
		Source:         "store/sqlite/bitemporal.go",
		Rationale:      reason,
		RationaleKind:  RationaleBitemporalTransition,
		PayloadAfter:   []byte(fmt.Sprintf(`{"trigger":%q,"new_mem_id":%d}`, trigger, newMemID)),
	})
}

// ---------------------------------------------------------------------------
// 2. refresh_on_access (T-103a #1)
// ---------------------------------------------------------------------------

// EmitDecayRefresh emits a modification event when RefreshOnAccess
// bumps access_count + last_refreshed_at. Cosmetic per Q2 — ASYNC.
//
// Rationale is auto-generated (the access_count bump is the WHY).
func (ae *AutoEmitter) EmitDecayRefresh(
	ctx context.Context,
	rowID, newAccessCount int64,
) {
	rationale := fmt.Sprintf("RefreshOnAccess: access_count -> %d", newAccessCount)
	ae.emitAsync(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:recall/decay.go",
		TargetTable:    "agent_memory",
		TargetRowID:    &rowID,
		Operation:      "update",
		Classification: ClassificationDecay,
		Source:         "recall/decay.go",
		Rationale:      rationale,
		RationaleKind:  RationaleDecayFunction,
	})
}

// ---------------------------------------------------------------------------
// 3. schema_migration (T-103a schema)
// ---------------------------------------------------------------------------

// EmitSchemaMigration emits a modification event when a schema migration
// runs. Target row = migration version (e.g., v32 for Phase 12 events).
//
// Structural per Q2 — SYNC. Rationale describes what changed.
func (ae *AutoEmitter) EmitSchemaMigration(
	ctx context.Context,
	fromVersion, toVersion int,
	rationale string,
) {
	id := int64(toVersion)
	ae.emit(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:schema_migration",
		TargetTable:    "schema_version",
		TargetRowID:    &id,
		Operation:      "alter",
		Classification: ClassificationSchema,
		Source:         "migrate/sqlite/ddl.go",
		Rationale:      rationale,
		RationaleKind:  RationaleSchemaChange,
		PayloadBefore:  []byte(fmt.Sprintf(`{"from_version":%d}`, fromVersion)),
		PayloadAfter:   []byte(fmt.Sprintf(`{"to_version":%d}`, toVersion)),
	})
}

// ---------------------------------------------------------------------------
// 4. embedder_refresh (T-103a #4)
// ---------------------------------------------------------------------------

// EmitEmbedderRefresh emits a modification event when the embedder
// refreshes an entity graph. Cosmetic per Q2 — ASYNC.
//
// rowID = entity_graph row id; newEntities = cache TTL of entity count.
func (ae *AutoEmitter) EmitEmbedderRefresh(
	ctx context.Context,
	rowID int64,
	newEntityCount int,
) {
	rationale := fmt.Sprintf("EmbedderRefresh: entity_count -> %d", newEntityCount)
	ae.emitAsync(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:embedder/integration.go",
		TargetTable:    "entity_graph",
		TargetRowID:    &rowID,
		Operation:      "refresh",
		Classification: ClassificationEmbedder,
		Source:         "embedder/integration.go",
		Rationale:      rationale,
		RationaleKind:  RationaleEntityGraphUpdate,
	})
}

// ---------------------------------------------------------------------------
// 5. calibration_update (T-103a #5)
// ---------------------------------------------------------------------------

// EmitCalibrationUpdate emits a modification event when a calibration
// row is updated (confidence_calibrated + ci_low + ci_high +
// calibration_method). Cosmetic per Q2 — ASYNC.
//
// evalID is the sdd_evaluations.id that was updated.
func (ae *AutoEmitter) EmitCalibrationUpdate(
	ctx context.Context,
	evalID int64,
	newPointEstimate float64,
	calibrationMethod string,
) {
	rowID := evalID
	rationale := fmt.Sprintf("CalibrationUpdate: point_estimate=%.4f method=%q",
		newPointEstimate, calibrationMethod)
	ae.emitAsync(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:judge/calibration.go",
		TargetTable:    "sdd_evaluations",
		TargetRowID:    &rowID,
		Operation:      "update",
		Classification: ClassificationCalibration,
		Source:         "judge/calibration.go",
		Rationale:      rationale,
		RationaleKind:  RationaleCacheTTLExpired, // closest fit
	})
}

// ---------------------------------------------------------------------------
// 6. cache_invalidate (Q1 selectivo: semantic-affecting only)
// ---------------------------------------------------------------------------

// EmitCacheInvalidation emits a modification event when a semantic-
// affecting cache invalidation happens. Per Q1, PURE LRU eviction / TTL
// expiry does NOT emit (caller passes semantic=false and we no-op).
//
// Cosmetic per Q2 — ASYNC (when emitted).
func (ae *AutoEmitter) EmitCacheInvalidation(
	ctx context.Context,
	cacheTable string,
	rowID int64,
	semantic bool,
	reason string,
) {
	if !semantic {
		// Q1 selectivo: pure LRU / TTL invalidations do NOT emit.
		return
	}
	ae.emitAsync(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:recall/cache.go",
		TargetTable:    cacheTable,
		TargetRowID:    &rowID,
		Operation:      "invalidate",
		Classification: ClassificationCache,
		Source:         "recall/cache.go",
		Rationale:      reason,
		RationaleKind:  RationaleCacheTTLExpired,
	})
}

// ---------------------------------------------------------------------------
// 7. judge_verdict_update (T-103a #7)
// ---------------------------------------------------------------------------

// EmitJudgeVerdictUpdate emits a modification event when a judge's
// verdict is updated (re-run after edge-case hit, calibration flip, etc.).
// Cosmetic per Q2 — ASYNC.
//
// evalID is the sdd_evaluations.id; confidence is the new confidence;
// verdict is one of "aligned" | "drift_detected" | "needs_human".
func (ae *AutoEmitter) EmitJudgeVerdictUpdate(
	ctx context.Context,
	evalID int64,
	verdict string,
	confidence float64,
) {
	rowID := evalID
	c := confidence
	rationale := fmt.Sprintf("JudgeVerdictUpdate: verdict=%q confidence=%.4f",
		verdict, confidence)
	ae.emitAsync(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:judge/pipeline.go",
		TargetTable:    "sdd_evaluations",
		TargetRowID:    &rowID,
		Operation:      "update",
		Classification: ClassificationJudge,
		Source:         "judge/pipeline.go",
		Rationale:      rationale,
		RationaleKind:  RationaleJudgeVerdict,
		Confidence:     &c,
	})
}

// ---------------------------------------------------------------------------
// 8. persona_update (T-103a #8)
// ---------------------------------------------------------------------------

// EmitPersonaUpdate emits a modification event when a judge persona
// definition is updated (rubric change, lens change, etc.). Structural
// per Q2 — SYNC. Rationale describes the change.
func (ae *AutoEmitter) EmitPersonaUpdate(
	ctx context.Context,
	personaID string,
	rationale string,
) {
	// Persona doesn't have a row id in the events sense — use the
	// canonical id encoding (persona_id is unique). We store it in
	// target_table as a synthetic key.
	ae.emit(ctx, Event{
		Kind:           KindModification,
		Actor:          "system:judge/personas_v4.go",
		TargetTable:    "judge_personas",
		TargetRowID:    nil, // personas don't have a stable row_id
		Operation:      "update",
		Classification: ClassificationCalibration, // closest fit (persona config)
		Source:         "judge/personas_v4.go",
		Rationale:      rationale,
		RationaleKind:  RationaleCacheTTLExpired, // closest fit (config change)
		PayloadJSON:    []byte(fmt.Sprintf(`{"persona_id":%q}`, personaID)),
	})
}