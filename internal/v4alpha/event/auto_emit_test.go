package event_test

// L1 tests for AutoEmitter (Phase 12 T-103a). 8 tests, one per
// modification site helper. All tests are hermetic: in-memory SQLite
// via the production Open() path (no mocks — per dark-testing A3 no
// mystery guests + A5 mock only at boundaries).
//
// Coverage (one test per Emit* helper per SPEC §4.3 T-103a):
//
//   1. TestEmitSupersede                  — mark_superseded
//   2. TestEmitDecayRefresh               — refresh_on_access
//   3. TestEmitSchemaMigration            — schema_migration
//   4. TestEmitEmbedderRefresh            — embedder_refresh
//   5. TestEmitCalibrationUpdate          — calibration_update
//   6. TestEmitCacheInvalidation_Semantic — cache (semantic=true emits)
//   7. TestEmitCacheInvalidation_LRUNoEmit — cache (semantic=false no-emits, Q1)
//   8. TestEmitJudgeVerdictUpdate         — judge verdict
//   9. TestEmitPersonaUpdate              — persona update
//
// Per dark-testing: A1 tests are evidence (real assertions against
// events table); A3 no mystery guests (real SQLite); A8 no sleep
// (sync ops complete on in-memory DBs immediately); A14 non-default
// values (every rationale, actor, classification is non-empty).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	eventpkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
)

// ---------- 1. mark_superseded ----------

// TestEmitSupersede: AutoEmitter.EmitSupersede writes one modification
// event with classification=supersede, source=store/sqlite/bitemporal.go.
func TestEmitSupersede(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	oldID := int64(42)
	newID := int64(43)
	ae.EmitSupersede(context.Background(), oldID, newID,
		"operator_action", "Phase 11 audit canonical decision supersession")

	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("events count = %d; want 1", len(all))
	}
	ev := all[0]
	if ev.Kind != eventpkg.KindModification {
		t.Errorf("Kind = %q; want modification", ev.Kind)
	}
	if ev.Classification.String != eventpkg.ClassificationSupersede {
		t.Errorf("Classification = %q; want %q", ev.Classification.String, eventpkg.ClassificationSupersede)
	}
	if ev.Source.String != "store/sqlite/bitemporal.go" {
		t.Errorf("Source = %q; want store/sqlite/bitemporal.go", ev.Source.String)
	}
	if ev.RationaleKind.String != eventpkg.RationaleBitemporalTransition {
		t.Errorf("RationaleKind = %q; want %q", ev.RationaleKind.String, eventpkg.RationaleBitemporalTransition)
	}
	if !strings.Contains(ev.Rationale.String, "supersession") {
		t.Errorf("Rationale = %q; want contains 'supersession'", ev.Rationale.String)
	}
	if ev.TargetRowID.Int64 != oldID {
		t.Errorf("TargetRowID = %d; want %d", ev.TargetRowID.Int64, oldID)
	}
}

// ---------- 2. refresh_on_access (Q2 cosmetic = ASYNC) ----------

// TestEmitDecayRefresh: AutoEmitter.EmitDecayRefresh is fire-and-forget.
// Verify the event lands in the table within a polling window.
func TestEmitDecayRefresh(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	rowID := int64(7)
	ae.EmitDecayRefresh(context.Background(), rowID, 42)

	got := waitForEvent(t, env, eventpkg.ClassificationDecay, 2*time.Second)
	if got == nil {
		t.Fatal("no event after 2s")
	}
	if got.Classification.String != eventpkg.ClassificationDecay {
		t.Errorf("Classification = %q; want %q", got.Classification.String, eventpkg.ClassificationDecay)
	}
	if got.RationaleKind.String != eventpkg.RationaleDecayFunction {
		t.Errorf("RationaleKind = %q; want %q", got.RationaleKind.String, eventpkg.RationaleDecayFunction)
	}
	if !strings.Contains(got.Rationale.String, "42") {
		t.Errorf("Rationale = %q; want contains '42'", got.Rationale.String)
	}
}

// ---------- 3. schema_migration ----------

// TestEmitSchemaMigration: emits a schema migration event with
// from_version → to_version in payload_before/after.
func TestEmitSchemaMigration(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	ae.EmitSchemaMigration(context.Background(), 31, 32,
		"Phase 12 T-101: polymorphic events table for modification + progress")

	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("events count = %d; want 1", len(all))
	}
	ev := all[0]
	if ev.Kind != eventpkg.KindModification {
		t.Errorf("Kind = %q; want modification", ev.Kind)
	}
	if ev.Classification.String != eventpkg.ClassificationSchema {
		t.Errorf("Classification = %q; want %q", ev.Classification.String, eventpkg.ClassificationSchema)
	}
	if ev.RationaleKind.String != eventpkg.RationaleSchemaChange {
		t.Errorf("RationaleKind = %q; want %q", ev.RationaleKind.String, eventpkg.RationaleSchemaChange)
	}
	if !strings.Contains(ev.PayloadAfter.String, "32") {
		t.Errorf("PayloadAfter = %q; want contains '32'", ev.PayloadAfter.String)
	}
}

// ---------- 4. embedder_refresh (Q2 cosmetic = ASYNC) ----------

// TestEmitEmbedderRefresh: emits an embedder refresh event with the
// new entity count in the rationale.
func TestEmitEmbedderRefresh(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	rowID := int64(99)
	ae.EmitEmbedderRefresh(context.Background(), rowID, 137)

	got := waitForEvent(t, env, eventpkg.ClassificationEmbedder, 2*time.Second)
	if got == nil {
		t.Fatal("no event after 2s")
	}
	if !strings.Contains(got.Rationale.String, "137") {
		t.Errorf("Rationale = %q; want contains '137'", got.Rationale.String)
	}
}

// ---------- 5. calibration_update (Q2 cosmetic = ASYNC) ----------

// TestEmitCalibrationUpdate: emits a calibration update event with
// the new point estimate in the rationale.
func TestEmitCalibrationUpdate(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	ae.EmitCalibrationUpdate(context.Background(), 555, 0.9234, "bootstrap_ci")

	got := waitForEvent(t, env, eventpkg.ClassificationCalibration, 2*time.Second)
	if got == nil {
		t.Fatal("no event after 2s")
	}
	if !strings.Contains(got.Rationale.String, "0.9234") {
		t.Errorf("Rationale = %q; want contains '0.9234'", got.Rationale.String)
	}
	if !strings.Contains(got.Rationale.String, "bootstrap_ci") {
		t.Errorf("Rationale = %q; want contains 'bootstrap_ci'", got.Rationale.String)
	}
}

// ---------- 6. cache_invalidate SEMANTIC (Q1 selectivo) ----------

// TestEmitCacheInvalidation_Semantic: when semantic=true, an event IS
// emitted (Q1 selectivo: semantic-affecting invalidations emit).
func TestEmitCacheInvalidation_Semantic(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	ae.EmitCacheInvalidation(context.Background(), "recall_cache", 1, true,
		"Phase 12 schema migration invalidates FTS5 recall cache")

	got := waitForEvent(t, env, eventpkg.ClassificationCache, 2*time.Second)
	if got == nil {
		t.Fatal("expected an event for semantic invalidation; got none after 2s")
	}
	if got.RationaleKind.String != eventpkg.RationaleCacheTTLExpired {
		t.Errorf("RationaleKind = %q; want %q", got.RationaleKind.String, eventpkg.RationaleCacheTTLExpired)
	}
}

// ---------- 7. cache_invalidate NON-SEMANTIC (Q1 selectivo: NO event) ----------

// TestEmitCacheInvalidation_LRUNoEmit: when semantic=false, NO event is
// emitted. This is Q1 selectivo: pure LRU evictions do NOT clutter the
// event log.
func TestEmitCacheInvalidation_LRUNoEmit(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	ae.EmitCacheInvalidation(context.Background(), "recall_cache", 1, false,
		"LRU eviction — purely capacity management")

	// Poll briefly to confirm NO event was emitted.
	// EmitAsync may still be running; wait 200ms and assert empty.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		if len(all) > 0 {
			t.Errorf("events count = %d; want 0 (Q1 selectivo: non-semantic cache invalidation does NOT emit)", len(all))
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Empty — perfect.
}

// ---------- 8. judge_verdict_update (Q2 cosmetic = ASYNC) ----------

// TestEmitJudgeVerdictUpdate: emits a judge verdict update event with
// verdict + confidence in the rationale + confidence column.
func TestEmitJudgeVerdictUpdate(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	ae.EmitJudgeVerdictUpdate(context.Background(), 777, "drift_detected", 0.42)

	got := waitForEvent(t, env, eventpkg.ClassificationJudge, 2*time.Second)
	if got == nil {
		t.Fatal("no event after 2s")
	}
	if !strings.Contains(got.Rationale.String, "drift_detected") {
		t.Errorf("Rationale = %q; want contains 'drift_detected'", got.Rationale.String)
	}
	if !strings.Contains(got.Rationale.String, "0.4200") {
		t.Errorf("Rationale = %q; want contains '0.4200'", got.Rationale.String)
	}
	if got.Confidence.Float64 != 0.42 {
		t.Errorf("Confidence = %f; want 0.42", got.Confidence.Float64)
	}
	if got.RationaleKind.String != eventpkg.RationaleJudgeVerdict {
		t.Errorf("RationaleKind = %q; want %q", got.RationaleKind.String, eventpkg.RationaleJudgeVerdict)
	}
}

// ---------- 9. persona_update ----------

// TestEmitPersonaUpdate: emits a persona update event with the persona
// id in payload_json. Rationale describes the change.
func TestEmitPersonaUpdate(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)
	ae := eventpkg.NewAutoEmitter(w)

	ae.EmitPersonaUpdate(context.Background(), "judge-modifications",
		"Phase 12 T-104: new persona for modification_audit eval_type")

	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("events count = %d; want 1", len(all))
	}
	ev := all[0]
	if !strings.Contains(ev.PayloadJSON.String, "judge-modifications") {
		t.Errorf("PayloadJSON = %q; want contains 'judge-modifications'", ev.PayloadJSON.String)
	}
	if ev.RationaleKind.String != eventpkg.RationaleCacheTTLExpired {
		t.Errorf("RationaleKind = %q; want %q", ev.RationaleKind.String, eventpkg.RationaleCacheTTLExpired)
	}
}

// ---------- helpers ----------

// waitForEvent polls the events table until an event with the given
// classification appears, or timeout elapses. Returns nil on timeout.
func waitForEvent(t *testing.T, env *testEnv, classification string, timeout time.Duration) *sqlite.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		for i := range all {
			if all[i].Classification.String == classification {
				return all[i]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}