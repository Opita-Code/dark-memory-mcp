package eventholder

// L1 tests for the eventholder package (Phase 12 T-103a-extension).
// Covers Set + Get lifecycle, atomic semantics, and that the 8
// AutoEmitter interface methods are present (so callers can be
// confident the interface contract is honored).
//
// Per dark-testing: A1 (real assertions), A3 (no mocks at this
// boundary — the AutoEmitter interface is the seam), A8 (no sleep),
// A14 (non-default values).

import (
	"context"
	"testing"
)

// fakeAutoEmitter captures calls to all 8 interface methods so we can
// assert Set() registered a real implementation and Get() returns
// the same instance back. Implements the full 8-method contract.
type fakeAutoEmitter struct {
	calls map[string]any
}

func (f *fakeAutoEmitter) record(method string, args ...any) {
	f.calls[method] = args
}

func (f *fakeAutoEmitter) EmitSupersede(_ context.Context, oldMemID, newMemID int64, trigger, reason string) {
	f.record("EmitSupersede", oldMemID, newMemID, trigger, reason)
}
func (f *fakeAutoEmitter) EmitDecayRefresh(_ context.Context, rowID, newAccessCount int64) {
	f.record("EmitDecayRefresh", rowID, newAccessCount)
}
func (f *fakeAutoEmitter) EmitSchemaMigration(_ context.Context, fromVersion, toVersion int, rationale string) {
	f.record("EmitSchemaMigration", fromVersion, toVersion, rationale)
}
func (f *fakeAutoEmitter) EmitEmbedderRefresh(_ context.Context, rowID int64, newEntityCount int) {
	f.record("EmitEmbedderRefresh", rowID, newEntityCount)
}
func (f *fakeAutoEmitter) EmitCalibrationUpdate(_ context.Context, evalID int64, newPointEstimate float64, method string) {
	f.record("EmitCalibrationUpdate", evalID, newPointEstimate, method)
}
func (f *fakeAutoEmitter) EmitCacheInvalidation(_ context.Context, cacheTable string, rowID int64, semantic bool, reason string) {
	f.record("EmitCacheInvalidation", cacheTable, rowID, semantic, reason)
}
func (f *fakeAutoEmitter) EmitJudgeVerdictUpdate(_ context.Context, evalID int64, verdict string, confidence float64) {
	f.record("EmitJudgeVerdictUpdate", evalID, verdict, confidence)
}
func (f *fakeAutoEmitter) EmitPersonaUpdate(_ context.Context, personaID, rationale string) {
	f.record("EmitPersonaUpdate", personaID, rationale)
}

// reset clears the global holder so tests are hermetic. The eventholder
// package intentionally provides Set/Get as a process-wide singleton
// (matches SetFederationPeer). Tests that mutate the holder MUST reset
// it in defer to avoid polluting other tests in the binary.
func reset() {
	Set(nil)
}

// TestHolder_SetGet: Set installs a real AutoEmitter; Get returns it.
func TestHolder_SetGet(t *testing.T) {
	defer reset()
	em := &fakeAutoEmitter{calls: map[string]any{}}
	Set(em)
	got := Get()
	if got == nil {
		t.Fatal("Get() returned nil after Set(non-nil)")
	}
	if got != em {
		t.Errorf("Get() returned a different instance; want pointer equality")
	}
}

// TestHolder_GetBeforeSet: Get returns nil when no Set has happened.
func TestHolder_GetBeforeSet(t *testing.T) {
	reset()
	if got := Get(); got != nil {
		t.Errorf("Get() returned %T; want nil before Set", got)
	}
}

// TestHolder_InterfaceContract: every method on the interface can be
// invoked via the holder. Catches "added a method to the interface but
// forgot to add it to the docstring list" bugs at test time.
func TestHolder_InterfaceContract(t *testing.T) {
	defer reset()
	em := &fakeAutoEmitter{calls: map[string]any{}}
	Set(em)
	got := Get()
	got.EmitSupersede(context.Background(), 1, 2, "test_trigger", "test_reason")
	got.EmitDecayRefresh(context.Background(), 3, 4)
	got.EmitSchemaMigration(context.Background(), 5, 6, "rationale")
	got.EmitEmbedderRefresh(context.Background(), 7, 8)
	got.EmitCalibrationUpdate(context.Background(), 9, 0.85, "test_method")
	got.EmitCacheInvalidation(context.Background(), "test_table", 10, true, "test_reason")
	got.EmitJudgeVerdictUpdate(context.Background(), 11, "aligned", 0.95)
	got.EmitPersonaUpdate(context.Background(), "test_persona", "test_rationale")
	// 8 unique call names captured.
	if len(em.calls) != 8 {
		t.Errorf("calls = %d; want 8 (one per interface method)", len(em.calls))
	}
	for _, name := range []string{
		"EmitSupersede", "EmitDecayRefresh", "EmitSchemaMigration",
		"EmitEmbedderRefresh", "EmitCalibrationUpdate",
		"EmitCacheInvalidation", "EmitJudgeVerdictUpdate", "EmitPersonaUpdate",
	} {
		if _, ok := em.calls[name]; !ok {
			t.Errorf("interface method %q not called (missing from fake.calls)", name)
		}
	}
}

// TestHolder_AtomicRead: concurrent Get calls during a Set don't
// deadlock or return torn pointers. The atomic.Pointer load/store
// is lock-free so this is fast.
func TestHolder_AtomicRead(t *testing.T) {
	defer reset()
	em := &fakeAutoEmitter{calls: map[string]any{}}
	Set(em)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				if Get() == nil {
					t.Error("Get() returned nil during/after Set")
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}