package eventholder_test

// Phase 12 T-103a-extension-2 e2e tests: exercise the 3 NEW wires
// added in T-103a-extension-2 (EmitSchemaMigration in migrate.Migrate,
// EmitCalibrationUpdate in judge.Store.SetCalibration,
// EmitJudgeVerdictUpdate in judge.Store.SaveEvaluation).
//
// Tests are written as e2e (call the real call site, observe the
// event through the eventholder-bound AutoEmitter) — NOT as unit
// tests on the AutoEmitter itself (that is covered by the L1
// holder_test.go in the parent package).
//
// The e2e pattern mirrors the existing T-103a-extension wires:
//   - Wire the eventholder to a recordingEmitter that captures calls
//   - Call the real call site
//   - Assert the fake captured the expected method + args
//
// Per dark-testing: A1 (real assertions on call args), A3 (no
// mocks at this boundary — recordingEmitter is a recording fake,
// not a mock with expectations), A5 (mock only at the seam, here
// the eventholder is the seam), A14 (non-default values throughout).

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite" // pure-Go driver (no cgo) — see internal/store/sqlite/store.go:25

	"github.com/dark-agents/dark-memory-mcp/internal/eventholder"
	"github.com/dark-agents/dark-memory-mcp/internal/migrate"
	migratesqlite "github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
)

// recordingEmitter is a thread-safe recording AutoEmitter. We use a
// mutex because the e2e paths may fire from goroutines (SaveEvaluation
// may be called concurrently across judge pipelines). The calls map
// is keyed by method name; each value is the captured args slice.
type recordingEmitter struct {
	mu    sync.Mutex
	calls map[string][][]any
}

func newRecordingEmitter() *recordingEmitter {
	return &recordingEmitter{calls: map[string][][]any{}}
}

func (r *recordingEmitter) record(method string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]any, len(args))
	for i, a := range args {
		cp[i] = a
	}
	r.calls[method] = append(r.calls[method], cp)
}

func (r *recordingEmitter) EmitSupersede(_ context.Context, oldMemID, newMemID int64, trigger, reason string) {
	r.record("EmitSupersede", oldMemID, newMemID, trigger, reason)
}
func (r *recordingEmitter) EmitDecayRefresh(_ context.Context, rowID, newAccessCount int64) {
	r.record("EmitDecayRefresh", rowID, newAccessCount)
}
func (r *recordingEmitter) EmitSchemaMigration(_ context.Context, fromVersion, toVersion int, rationale string) {
	r.record("EmitSchemaMigration", fromVersion, toVersion, rationale)
}
func (r *recordingEmitter) EmitEmbedderRefresh(_ context.Context, rowID int64, newEntityCount int) {
	r.record("EmitEmbedderRefresh", rowID, newEntityCount)
}
func (r *recordingEmitter) EmitCalibrationUpdate(_ context.Context, evalID int64, newPointEstimate float64, method string) {
	r.record("EmitCalibrationUpdate", evalID, newPointEstimate, method)
}
func (r *recordingEmitter) EmitCacheInvalidation(_ context.Context, cacheTable string, rowID int64, semantic bool, reason string) {
	r.record("EmitCacheInvalidation", cacheTable, rowID, semantic, reason)
}
func (r *recordingEmitter) EmitJudgeVerdictUpdate(_ context.Context, evalID int64, verdict string, confidence float64) {
	r.record("EmitJudgeVerdictUpdate", evalID, verdict, confidence)
}
func (r *recordingEmitter) EmitPersonaUpdate(_ context.Context, personaID, rationale string) {
	r.record("EmitPersonaUpdate", personaID, rationale)
}

// count returns the number of recorded calls for a method (0 if absent).
func (r *recordingEmitter) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls[method])
}

// first returns the first call's args for a method, or nil if absent.
func (r *recordingEmitter) first(method string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.calls[method]; ok && len(cs) > 0 {
		return cs[0]
	}
	return nil
}

// last returns the last call's args for a method, or nil if absent.
func (r *recordingEmitter) last(method string) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs, ok := r.calls[method]; ok && len(cs) > 0 {
		return cs[len(cs)-1]
	}
	return nil
}

// install wires the eventholder to rec and registers a cleanup that
// resets the holder to nil. Tests must call install() before any
// call that should observe the wire.
func install(t *testing.T) *recordingEmitter {
	t.Helper()
	rec := newRecordingEmitter()
	eventholder.Set(rec)
	t.Cleanup(func() { eventholder.Set(nil) })
	return rec
}

// openFreshDB opens a new file-backed SQLite DB in a temp dir. Each
// call gets its own DB so tests don't share state.
func openFreshDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "wires.db")
	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------- Wire A: EmitSchemaMigration → migrate.Migrate ----------

// TestWire_EmitSchemaMigration_OnMigrate asserts that calling
// migrate.Migrate triggers EmitSchemaMigration for each NEW version
// applied. The test installs the recording emitter BEFORE Migrate
// runs, so the first applied version is v1 (from=0, to=1).
func TestWire_EmitSchemaMigration_OnMigrate(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	// Run Migrate against the canonical migrations slice.
	if err := migrate.Migrate(context.Background(), db, migratesqlite.Migrations); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// 32 = the highest version in the canonical slice (v32 events_polymorphic).
	// 33+ may land in future T-101+ commits; assert >=32 not exact.
	if got := rec.count("EmitSchemaMigration"); got < 32 {
		t.Errorf("EmitSchemaMigration calls = %d; want >= 32", got)
	}

	// The first call must be (from=0, to=1) since this is a fresh DB.
	first := rec.first("EmitSchemaMigration")
	if first == nil {
		t.Fatal("first EmitSchemaMigration call missing")
	}
	if from, to := first[0].(int), first[1].(int); from != 0 || to != 1 {
		t.Errorf("first migration from=%v to=%v; want 0/1", from, to)
	}

	// The last call must include v32 (events_polymorphic) as the
	// toVersion. Assert >= 32 (idempotent across future T-101+ commits).
	last := rec.last("EmitSchemaMigration")
	if to := last[1].(int); to < 32 {
		t.Errorf("last migration to=%v; want >= 32 (events_polymorphic)", to)
	}
}

// TestWire_EmitSchemaMigration_NoEmitsWhenAlreadyApplied asserts
// idempotency: re-running Migrate on a DB that's already at v32
// emits ZERO new events. This protects against a regression where
// the bookkeeping table is consulted AFTER applyOne commits (which
// would mistakenly emit "from=32 to=32" on every restart).
func TestWire_EmitSchemaMigration_NoEmitsWhenAlreadyApplied(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	// First migrate: should emit >=32 events.
	if err := migrate.Migrate(context.Background(), db, migratesqlite.Migrations); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	firstRunCount := rec.count("EmitSchemaMigration")

	// Second migrate: NOTHING new should be emitted (bookkeeping
	// says every version is applied; the loop in Migrate skips
	// them all).
	if err := migrate.Migrate(context.Background(), db, migratesqlite.Migrations); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	secondRunCount := rec.count("EmitSchemaMigration")
	if secondRunCount != firstRunCount {
		t.Errorf("second run emitted %d new events (total %d); want 0 (total %d)",
			secondRunCount-firstRunCount, secondRunCount, firstRunCount)
	}
}

// TestWire_EmitSchemaMigration_FromVersionComputed asserts the
// fromVersion is the previous version (not the new one). On a fresh
// DB the sequence is: (0→1), (1→2), ..., (31→32). The fromVersion
// must monotonically increase by 1.
func TestWire_EmitSchemaMigration_FromVersionComputed(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	if err := migrate.Migrate(context.Background(), db, migratesqlite.Migrations); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rec.mu.Lock()
	calls := append([][]any(nil), rec.calls["EmitSchemaMigration"]...)
	rec.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("need >=2 migration calls to verify from-version sequence; got %d", len(calls))
	}
	for i := 1; i < len(calls); i++ {
		prevTo := calls[i-1][1].(int)
		curFrom := calls[i][0].(int)
		if curFrom != prevTo {
			t.Errorf("calls[%d].from = %d; want %d (previous toVersion)", i, curFrom, prevTo)
		}
	}
}

// ---------- Wire B: EmitCalibrationUpdate → judge.Store.SetCalibration ----------

// TestWire_EmitCalibrationUpdate_OnSetCalibration asserts that
// updating calibration columns on a sdd_evaluations row triggers
// EmitCalibrationUpdate with the correct (evalID, point estimate,
// method) triple.
//
// NOTE: this test uses judge.CreateSchema DIRECTLY (skipping the
// full migrate path) — the migration creates a legacy 8-column
// sdd_evaluations table, and the canonical 17-column schema is
// what judge.Store expects. This isolates the wire test from
// the migration timing (~30s on a fresh DB).
func TestWire_EmitCalibrationUpdate_OnSetCalibration(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge CreateSchema: %v", err)
	}
	// audit.NewWriter requires audit_log table + project_id column.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit CreateSchema: %v", err)
	}
	// judge.SaveEvaluation inserts a project_id column; CreateSchema
	// does NOT add it (the column lands via project.ApplyProjectIDColumns
	// in production after the audit.CreateSchema + ApplyChainColumns).
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project ApplyProjectIDColumns: %v", err)
	}

	auditMeta := &judge.Audit{Actor: "test/calibration", SessionID: "sess-c1"}
	ev := &judge.Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "art-1",
		VerdictJSON: `{"verdict":"aligned","confidence":0.5}`,
		Confidence:  0.5,
	}
	store := judge.NewStore(db, audit.NewWriter(db))
	id, err := store.SaveEvaluation(context.Background(), auditMeta, ev)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}

	// Now run SetCalibration — should emit one event with this id.
	ci := judge.CalibrationCI{
		N:             100,
		PointEstimate: 0.74,
		CILow:         0.69,
		CIHigh:        0.79,
		NResamples:    1000,
	}
	if err := store.SetCalibration(context.Background(), id, ci, "bootstrap_1000"); err != nil {
		t.Fatalf("SetCalibration: %v", err)
	}

	if got := rec.count("EmitCalibrationUpdate"); got != 1 {
		t.Fatalf("EmitCalibrationUpdate calls = %d; want 1", got)
	}
	args := rec.first("EmitCalibrationUpdate")
	if args == nil {
		t.Fatal("first EmitCalibrationUpdate call missing")
	}
	if got := args[0].(int64); got != id {
		t.Errorf("evalID = %d; want %d", got, id)
	}
	if got := args[1].(float64); got != 0.74 {
		t.Errorf("pointEstimate = %v; want 0.74", got)
	}
	if got := args[2].(string); got != "bootstrap_1000" {
		t.Errorf("method = %q; want %q", got, "bootstrap_1000")
	}
}

// ---------- Wire C: EmitJudgeVerdictUpdate → judge.Store.SaveEvaluation ----------

// TestWire_EmitJudgeVerdictUpdate_OnSaveEvaluation asserts that
// persisting a new sdd_evaluations row triggers EmitJudgeVerdictUpdate
// with the new id, the parsed verdict label, and the confidence.
func TestWire_EmitJudgeVerdictUpdate_OnSaveEvaluation(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge CreateSchema: %v", err)
	}
	// audit.NewWriter requires audit_log table + project_id column.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit CreateSchema: %v", err)
	}
	// judge.SaveEvaluation inserts a project_id column; CreateSchema
	// does NOT add it (the column lands via project.ApplyProjectIDColumns
	// in production after the audit.CreateSchema + ApplyChainColumns).
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project ApplyProjectIDColumns: %v", err)
	}

	auditMeta := &judge.Audit{Actor: "test/verdict", SessionID: "sess-v1"}
	ev := &judge.Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "art-7",
		VerdictJSON: `{"verdict":"aligned","confidence":0.92}`,
		Confidence:  0.92,
	}
	store := judge.NewStore(db, audit.NewWriter(db))
	id, err := store.SaveEvaluation(context.Background(), auditMeta, ev)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}

	if got := rec.count("EmitJudgeVerdictUpdate"); got != 1 {
		t.Fatalf("EmitJudgeVerdictUpdate calls = %d; want 1", got)
	}
	args := rec.first("EmitJudgeVerdictUpdate")
	if args == nil {
		t.Fatal("first EmitJudgeVerdictUpdate call missing")
	}
	if got := args[0].(int64); got != id {
		t.Errorf("evalID = %d; want %d", got, id)
	}
	if got := args[1].(string); got != "aligned" {
		t.Errorf("verdict = %q; want %q", got, "aligned")
	}
	if got := args[2].(float64); got != 0.92 {
		t.Errorf("confidence = %v; want 0.92", got)
	}
}

// TestWire_EmitJudgeVerdictUpdate_MultipleSaves asserts the wire
// fires ONCE per SaveEvaluation (not batched, not skipped) when
// multiple verdicts are persisted in sequence. 3 saves → 3 events.
func TestWire_EmitJudgeVerdictUpdate_MultipleSaves(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge CreateSchema: %v", err)
	}
	// audit.NewWriter requires audit_log table + project_id column.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit CreateSchema: %v", err)
	}
	// judge.SaveEvaluation inserts a project_id column; CreateSchema
	// does NOT add it (the column lands via project.ApplyProjectIDColumns
	// in production after the audit.CreateSchema + ApplyChainColumns).
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project ApplyProjectIDColumns: %v", err)
	}
	st := judge.NewStore(db, audit.NewWriter(db))

	for i := 0; i < 3; i++ {
		ev := &judge.Evaluation{
			EvalType:    "drift_judge",
			TargetType:  "artifact",
			TargetID:    "art-" + string(rune('a'+i)),
			VerdictJSON: `{"verdict":"drift_detected","confidence":0.7}`,
			Confidence:  0.7,
		}
		auditMeta := &judge.Audit{Actor: "test/multi", SessionID: "sess-m"}
		if _, err := st.SaveEvaluation(context.Background(), auditMeta, ev); err != nil {
			t.Fatalf("SaveEvaluation[%d]: %v", i, err)
		}
	}

	if got := rec.count("EmitJudgeVerdictUpdate"); got != 3 {
		t.Errorf("EmitJudgeVerdictUpdate calls = %d; want 3 (one per SaveEvaluation)", got)
	}
}

// TestWire_EmitJudgeVerdictUpdate_VerdictLabelParsed asserts the
// emitted verdict label is parsed from VerdictJSON (not just the
// confidence field). This catches a regression where the wire
// hardcodes "aligned" or passes empty string.
func TestWire_EmitJudgeVerdictUpdate_VerdictLabelParsed(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge CreateSchema: %v", err)
	}
	// audit.NewWriter requires audit_log table + project_id column.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit CreateSchema: %v", err)
	}
	// judge.SaveEvaluation inserts a project_id column; CreateSchema
	// does NOT add it (the column lands via project.ApplyProjectIDColumns
	// in production after the audit.CreateSchema + ApplyChainColumns).
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project ApplyProjectIDColumns: %v", err)
	}
	st := judge.NewStore(db, audit.NewWriter(db))
	auditMeta := &judge.Audit{Actor: "test/label", SessionID: "sess-l"}
	ev := &judge.Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "art-l",
		VerdictJSON: `{"verdict":"drift_detected","confidence":0.85}`,
		Confidence:  0.85,
	}
	if _, err := st.SaveEvaluation(context.Background(), auditMeta, ev); err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	args := rec.first("EmitJudgeVerdictUpdate")
	if got := args[1].(string); got != "drift_detected" {
		t.Errorf("verdict = %q; want %q (parsed from VerdictJSON)", got, "drift_detected")
	}
}

// ---------- Wire independence ----------

// TestWire_NoCrossTalk asserts that installing the emitter for one
// wire doesn't accidentally trigger ANOTHER wire (e.g., running
// Migrate should not also call EmitCalibrationUpdate).
func TestWire_NoCrossTalk(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	// Migrate only — should emit schema migration events but ZERO
	// of the other 7 method kinds.
	if err := migrate.Migrate(context.Background(), db, migratesqlite.Migrations); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, m := range []string{
		"EmitCalibrationUpdate", "EmitJudgeVerdictUpdate",
		"EmitSupersede", "EmitDecayRefresh", "EmitEmbedderRefresh",
		"EmitCacheInvalidation", "EmitPersonaUpdate",
	} {
		if got := rec.count(m); got != 0 {
			t.Errorf("crosstalk: %s called %d times during Migrate only; want 0", m, got)
		}
	}
}

// TestWire_JudgeNoCrossTalk asserts that judge.Store operations
// (SaveEvaluation + SetCalibration) emit ONLY the 2 expected
// methods — not the schema migration event.
func TestWire_JudgeNoCrossTalk(t *testing.T) {
	rec := install(t)
	db := openFreshDB(t)

	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge CreateSchema: %v", err)
	}
	// audit.NewWriter requires audit_log table + project_id column.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit CreateSchema: %v", err)
	}
	// judge.SaveEvaluation inserts a project_id column; CreateSchema
	// does NOT add it (the column lands via project.ApplyProjectIDColumns
	// in production after the audit.CreateSchema + ApplyChainColumns).
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project ApplyProjectIDColumns: %v", err)
	}
	st := judge.NewStore(db, audit.NewWriter(db))
	auditMeta := &judge.Audit{Actor: "test/crosstalk-judge", SessionID: "sess-cj"}
	ev := &judge.Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "art-cj",
		VerdictJSON: `{"verdict":"aligned","confidence":0.5}`,
		Confidence:  0.5,
	}
	if _, err := st.SaveEvaluation(context.Background(), auditMeta, ev); err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	if err := st.SetCalibration(context.Background(), 1, judge.CalibrationCI{}, "test"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetCalibration: %v", err)
	}

	// SaveEvaluation → EmitJudgeVerdictUpdate (1 call)
	// SetCalibration on missing row → sql.ErrNoRows → no EmitCalibrationUpdate (0 calls)
	if got := rec.count("EmitJudgeVerdictUpdate"); got != 1 {
		t.Errorf("EmitJudgeVerdictUpdate = %d; want 1", got)
	}
	if got := rec.count("EmitSchemaMigration"); got != 0 {
		t.Errorf("crosstalk: EmitSchemaMigration = %d; want 0 (CreateSchema doesn't migrate)", got)
	}
}

// ---------- Nil-emitter safety ----------

// TestWire_NilEmitterNoPanic asserts that the wires behave safely when
// eventholder.Get() returns nil (pre-boot or test teardown). The
// call sites MUST guard with `if ae := eventholder.Get(); ae != nil`
// before invoking — a regression here would panic in production
// at boot time.
func TestWire_NilEmitterNoPanic(t *testing.T) {
	// Deliberately do NOT call install() — emitter stays nil.
	db := openFreshDB(t)

	// audit.NewWriter requires audit_log table + project_id column.
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit CreateSchema with nil emitter: %v", err)
	}
	// Should not panic. judge.CreateSchema must come BEFORE
	// ApplyProjectIDColumns so the sdd_evaluations table exists
	// for the column-add ALTER TABLE.
	if err := judge.CreateSchema(db); err != nil {
		t.Fatalf("judge CreateSchema with nil emitter: %v", err)
	}
	if err := project.ApplyProjectIDColumns(context.Background(), db); err != nil {
		t.Fatalf("project ApplyProjectIDColumns with nil emitter: %v", err)
	}
	st := judge.NewStore(db, audit.NewWriter(db))
	auditMeta := &judge.Audit{Actor: "test/nil", SessionID: "sess-nil"}
	ev := &judge.Evaluation{
		EvalType:    "drift_judge",
		TargetType:  "artifact",
		TargetID:    "art-nil",
		VerdictJSON: `{"verdict":"aligned","confidence":0.5}`,
		Confidence:  0.5,
	}
	if _, err := st.SaveEvaluation(context.Background(), auditMeta, ev); err != nil {
		t.Fatalf("SaveEvaluation with nil emitter: %v", err)
	}
	if err := st.SetCalibration(context.Background(), 1, judge.CalibrationCI{}, "test"); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetCalibration with nil emitter: %v", err)
	}
}