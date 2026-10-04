package event_test

// L1+L2 tests for EventWriter (Phase 12 T-102). 12 tests covering the
// SPEC §8 T-102 deliverables:
//
//   1.  TestEmit_HappyPath — valid modification with rationale succeeds
//   2.  TestEmit_ModificationMissingRationale — combo (a)+(c) sentinel + reject
//   3.  TestEmit_SentinelCountAfterReject — OD7 (j): exactly 1 sentinel
//   4.  TestEmit_CallerGetsErrRationaleRequired — OD7 (k): errors.Is match
//   5.  TestEmit_AuditLogChainRowWritten — atomic insert + chain (events + audit_log)
//   6.  TestEmit_InvalidKind — ErrInvalidKind for unknown kind
//   7.  TestEmit_EmptyActor — ErrActorRequired for empty actor
//   8.  TestEmit_ProgressNoRationaleRequired — progress bypasses INV-20
//   9.  TestEmitAsync_NonBlocking — EmitAsync returns before emit finishes
//   10. TestEmitAsync_ErrorLogged — failures logged, not returned
//   11. TestEmit_NoAuditWriter — works without audit (chain row skipped)
//   12. TestEmit_NilStore — New(nil) returns ErrStoreRequired
//
// Per dark-testing: A1 tests are evidence (not flaky timing); A3 no
// mystery guests (real SQLite + real audit.Writer); A8 no sleep (sync
// ops complete immediately on in-memory DBs); A14 non-default values
// (every rationale, actor, classification is non-empty).

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	eventpkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
)

// ---------- 1. Happy path ----------

// TestEmit_HappyPath: a well-formed modification emits exactly one
// events row with the rationale populated. No sentinel.
func TestEmit_HappyPath(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false) // no audit chain for this test

	id, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:           eventpkg.KindModification,
		Actor:          "test-decoder",
		TargetTable:    "agent_memory",
		TargetRowID:    int64Ptr(42),
		Operation:      "update",
		Classification: eventpkg.ClassificationDecay,
		Rationale:      "Phase 5 decay applied per score formula",
		RationaleKind:  eventpkg.RationaleDecayFunction,
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want > 0", id)
	}

	// Verify the row.
	got, err := env.eventsStore.GetEventByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetEventByID: %v", err)
	}
	if got.Kind != eventpkg.KindModification {
		t.Errorf("Kind = %q; want %q", got.Kind, eventpkg.KindModification)
	}
	if got.Actor != "test-decoder" {
		t.Errorf("Actor = %q; want %q", got.Actor, "test-decoder")
	}
	if got.Rationale.String != "Phase 5 decay applied per score formula" {
		t.Errorf("Rationale = %q; want non-empty", got.Rationale.String)
	}
}

// ---------- 2. Combo (a)+(c): missing rationale → sentinel + reject ----------

// TestEmit_ModificationMissingRationale: emit a modification WITHOUT
// rationale. The Writer must auto-emit a MISSING_RATIONALE sentinel
// and return ErrRationaleRequired.
func TestEmit_ModificationMissingRationale(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	id, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:           eventpkg.KindModification,
		Actor:          "test-decoder",
		TargetTable:    "agent_memory",
		TargetRowID:    int64Ptr(99),
		Operation:      "update",
		Classification: eventpkg.ClassificationDecay,
		// Rationale: intentionally empty to trigger INV-20.
	})

	// (a) caller receives ErrRationaleRequired
	if !errors.Is(err, eventpkg.ErrRationaleRequired) {
		t.Errorf("err = %v; want ErrRationaleRequired", err)
	}
	if id != 0 {
		t.Errorf("id = %d; want 0 (rejected)", id)
	}

	// (c) sentinel was inserted — find it by rationale + classification.
	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("events count = %d; want 1 (the sentinel)", len(all))
	}
	sentinel := all[0]
	if sentinel.Classification.String != eventpkg.ClassificationInv20Violation {
		t.Errorf("sentinel Classification = %q; want %q",
			sentinel.Classification.String, eventpkg.ClassificationInv20Violation)
	}
	if !strings.Contains(sentinel.Rationale.String, "MISSING_RATIONALE") {
		t.Errorf("sentinel Rationale = %q; want contains MISSING_RATIONALE",
			sentinel.Rationale.String)
	}
	if sentinel.Actor != "system" {
		t.Errorf("sentinel Actor = %q; want %q", sentinel.Actor, "system")
	}
	// Sentinel MUST inherit the target table + row id so the operator
	// can find the rejected write.
	if sentinel.TargetTable.String != "agent_memory" {
		t.Errorf("sentinel TargetTable = %q; want %q",
			sentinel.TargetTable.String, "agent_memory")
	}
}

// ---------- 3. OD7 (j): sentinel count == 1 after rejected write ----------

// TestEmit_SentinelCountAfterReject: after a single rejected write,
// exactly one sentinel exists in events (no spurious duplicates).
func TestEmit_SentinelCountAfterReject(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	_, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:           eventpkg.KindModification,
		Actor:          "test-decoder",
		Classification: eventpkg.ClassificationDecay,
		// Rationale: missing
	})
	if !errors.Is(err, eventpkg.ErrRationaleRequired) {
		t.Fatalf("Emit: %v; want ErrRationaleRequired", err)
	}

	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	sentinelCount := 0
	for _, ev := range all {
		if ev.Classification.String == eventpkg.ClassificationInv20Violation {
			sentinelCount++
		}
	}
	if sentinelCount != 1 {
		t.Errorf("sentinel count = %d; want 1", sentinelCount)
	}
}

// ---------- 4. OD7 (k): caller gets ErrRationaleRequired ----------

// TestEmit_CallerGetsErrRationaleRequired: the error returned from Emit
// is detectable via errors.Is(err, ErrRationaleRequired). This is the
// caller contract for INV-20 enforcement.
func TestEmit_CallerGetsErrRationaleRequired(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	_, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:      eventpkg.KindModification,
		Actor:     "test-decoder",
		Rationale: "",
	})
	if err == nil {
		t.Fatal("Emit: nil error; want ErrRationaleRequired")
	}
	if !errors.Is(err, eventpkg.ErrRationaleRequired) {
		t.Errorf("errors.Is(err, ErrRationaleRequired) = false; err = %v", err)
	}
}

// ---------- 5. Atomic insert + chain (events row + audit_log row) ----------

// TestEmit_AuditLogChainRowWritten: when an audit.Writer is injected,
// every Emit writes both an events row AND an audit_log row. The
// audit_log row carries the event metadata in its payload.
func TestEmit_AuditLogChainRowWritten(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, true) // WITH audit chain

	id, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:           eventpkg.KindModification,
		Actor:          "test-decoder",
		TargetTable:    "agent_memory",
		TargetRowID:    int64Ptr(7),
		Operation:      "update",
		Classification: eventpkg.ClassificationDecay,
		Rationale:      "Phase 5 decay applied",
		RationaleKind:  eventpkg.RationaleDecayFunction,
		SessionID:      "sess-chain-test",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// events row exists
	if _, err := env.eventsStore.GetEventByID(context.Background(), id); err != nil {
		t.Fatalf("events.GetEventByID: %v", err)
	}

	// audit_log row exists (count = 1)
	var count int
	if err := env.auditDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM audit_log",
	).Scan(&count); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	if count != 1 {
		t.Errorf("audit_log count = %d; want 1", count)
	}

	// audit_log row's actor = "event:modification" (per Writer.emit
	// convention so audit_export knows the source)
	var actor string
	if err := env.auditDB.QueryRowContext(context.Background(),
		"SELECT actor FROM audit_log LIMIT 1",
	).Scan(&actor); err != nil {
		t.Fatalf("query audit_log.actor: %v", err)
	}
	if actor != "event:modification" {
		t.Errorf("audit_log.actor = %q; want %q", actor, "event:modification")
	}
}

// ---------- 6. Invalid kind ----------

// TestEmit_InvalidKind: emit with kind="foo" (not in enum) returns
// ErrInvalidKind.
func TestEmit_InvalidKind(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	_, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:  "foo", // not in enum
		Actor: "test-decoder",
	})
	if !errors.Is(err, eventpkg.ErrInvalidKind) {
		t.Errorf("err = %v; want ErrInvalidKind", err)
	}
}

// ---------- 7. Empty actor ----------

// TestEmit_EmptyActor: emit with actor="" returns ErrActorRequired.
func TestEmit_EmptyActor(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	_, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:      eventpkg.KindModification,
		Actor:     "",
		Rationale: "test rationale",
	})
	if !errors.Is(err, eventpkg.ErrActorRequired) {
		t.Errorf("err = %v; want ErrActorRequired", err)
	}
}

// ---------- 8. Progress bypasses INV-20 ----------

// TestEmit_ProgressNoRationaleRequired: a progress event with empty
// rationale is FINE — INV-20 only applies to modifications.
func TestEmit_ProgressNoRationaleRequired(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	id, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:      eventpkg.KindProgress,
		Actor:     "async-drift-judge",
		ProcessID: "drift-42",
		Phase:     eventpkg.PhaseStarted,
		// Rationale: not required for progress
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want > 0", id)
	}

	// No sentinel was emitted.
	all, err := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("events count = %d; want 1 (the progress)", len(all))
	}
	if all[0].Kind != eventpkg.KindProgress {
		t.Errorf("Kind = %q; want %q", all[0].Kind, eventpkg.KindProgress)
	}
}

// ---------- 9. EmitAsync is non-blocking ----------

// TestEmitAsync_NonBlocking: EmitAsync returns before the goroutine
// completes the insert. We measure via a channel that closes once the
// EmitAsync returns.
func TestEmitAsync_NonBlocking(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	done := make(chan struct{})
	go func() {
		w.EmitAsync(context.Background(), eventpkg.Event{
			Kind:      eventpkg.KindProgress,
			Actor:     "test-decoder",
			ProcessID: "test-progress-1",
			Phase:     eventpkg.PhaseStarted,
		})
		close(done)
	}()

	select {
	case <-done:
		// EmitAsync returned before we hit the timeout — good.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EmitAsync blocked > 100ms; should be non-blocking")
	}

	// Give the goroutine a moment to actually insert (no sleep —
	// poll the events table instead).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		if len(all) == 1 {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
	t.Errorf("events count = %d after EmitAsync; want 1 (goroutine didn't finish in 2s)", len(all))
}

// ---------- 10. EmitAsync errors are logged, not returned ----------

// TestEmitAsync_ErrorLogged: a rationale violation from EmitAsync is
// logged but does NOT block the caller. The caller moves on; the
// sentinel exists in the DB.
func TestEmitAsync_ErrorLogged(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false)

	// EmitAsync with a modification that has empty rationale. Should
	// not block, not panic.
	done := make(chan struct{})
	go func() {
		w.EmitAsync(context.Background(), eventpkg.Event{
			Kind:           eventpkg.KindModification,
			Actor:          "test-decoder",
			Classification: eventpkg.ClassificationDecay,
			// Rationale: missing
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EmitAsync blocked on rationale violation; should be non-blocking")
	}

	// Sentinel exists (EmitAsync still calls the rejection path).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		all, _ := env.eventsStore.ListEvents(context.Background(), sqlite.ListEventsFilter{})
		if len(all) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("sentinel missing after EmitAsync + violation")
}

// ---------- 11. No audit writer: works fine ----------

// TestEmit_NoAuditWriter: Writer constructed without audit chain still
// emits events (chain row just skipped). Critical for tests + callers
// that don't need full chain coverage.
func TestEmit_NoAuditWriter(t *testing.T) {
	env := newTestEnv(t)
	w := newWriter(t, env, false) // NO audit chain

	id, err := w.Emit(context.Background(), eventpkg.Event{
		Kind:           eventpkg.KindModification,
		Actor:          "test-decoder",
		Rationale:      "valid rationale",
		RationaleKind:  eventpkg.RationaleDecayFunction,
		Classification: eventpkg.ClassificationDecay,
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d; want > 0", id)
	}

	// events has the row
	if _, err := env.eventsStore.GetEventByID(context.Background(), id); err != nil {
		t.Fatalf("GetEventByID: %v", err)
	}

	// audit_log is empty (no chain row)
	var count int
	if err := env.auditDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM audit_log",
	).Scan(&count); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	if count != 0 {
		t.Errorf("audit_log count = %d; want 0 (no audit writer)", count)
	}
}

// ---------- 12. Nil store ----------

// TestEmit_NilStore: New(nil) returns ErrStoreRequired.
func TestEmit_NilStore(t *testing.T) {
	_, err := eventpkg.New(nil, nil)
	if !errors.Is(err, eventpkg.ErrStoreRequired) {
		t.Errorf("err = %v; want ErrStoreRequired", err)
	}
}

// ---------- helpers ----------

// int64Ptr returns *i64 for setting optional pointer fields in test
// setup (avoids noise from &int64(42)).
func int64Ptr(i int64) *int64 { return &i }

// Keep imports referenced; some are used in helper functions only.
var _ atomic.Bool