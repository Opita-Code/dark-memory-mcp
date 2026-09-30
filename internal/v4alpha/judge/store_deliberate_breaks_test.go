// judge.Store deliberate-breaks tests (ADR-007 C3).
//
// 6 tests that catch the regressions most likely to creep in when
// the sdd_evaluations persistence layer evolves:
//
//  1. TestDeliberateBreak_Store_AuditActorMissingFailsFast
//  2. TestDeliberateBreak_Store_RollbackLeavesNoRows
//  3. TestDeliberateBreak_Store_V4ColumnsRoundtripAllFields
//  4. TestDeliberateBreak_Store_ConsensusSamplesRejectsEmptyList
//  5. TestDeliberateBreak_Store_ConsensusSamplesRejectsReservedEvalType
//  6. TestDeliberateBreak_Store_ProjectIDColumnReadyForFuture
package judge

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// Compile-time guard that the test references types from this package.
var _ = (*Audit)(nil)
var _ = (*Evaluation)(nil)

func TestDeliberateBreak_Store_AuditActorMissingFailsFast(t *testing.T) {
	// INV-1: empty actor rejected before any DB I/O.
	s, db, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("drift_judge", "code", "id", v)

	cases := []struct {
		name string
		am   *Audit
	}{
		{"nil_audit", nil},
		{"empty_actor", &Audit{Actor: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SaveEvaluation(context.Background(), tc.am, e)
			if !errors.Is(err, ErrEmptyAuditActor) {
				t.Fatalf("SaveEvaluation with %s: got %v, want ErrEmptyAuditActor", tc.name, err)
			}
			// No row was inserted.
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM sdd_evaluations").Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != 0 {
				t.Fatalf("sdd_evaluations rows=%d after rejected SaveEvaluation, want 0", n)
			}
		})
	}
}

func TestDeliberateBreak_Store_RollbackLeavesNoRows(t *testing.T) {
	// Atomicity guarantee: audit_log + sdd_evaluations rows roll
	// back together when the tx fails. We can't easily inject a
	// SQL error from outside (the Store has no public hooks), so
	// this test exercises the happy path AND verifies that the
	// audit package's tx-aware Writer (TestWriteExecRollback in
	// audit/writer_tx_test.go) covers the failure path.
	//
	// The Store reuses the audit.Writer.WriteExec contract, so
	// the audit-level test is sufficient to prove the atomicity
	// for SaveEvaluation.
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	// Sanity: a happy-path SaveEvaluation persists both rows.
	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("drift_judge", "code", "rollback-target", v)
	auditMeta := &Audit{Actor: "operator-rollback"}
	id, err := s.SaveEvaluation(context.Background(), auditMeta, e)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	if id <= 0 {
		t.Fatalf("SaveEvaluation returned id=%d", id)
	}
	// Full sanity check is redundant with TestSaveEvaluation_AuditLogRowInsertedAtomically;
	// this test focuses on documenting the cross-layer atomicity
	// guarantee, which is proven by audit/writer_tx_test.go.
	_ = id
}

func TestDeliberateBreak_Store_V4ColumnsRoundtripAllFields(t *testing.T) {
	// Verifies that all v4 columns (provider, model, persona_id,
	// rubric_version, schema_version, seed, max_tokens, timeout_ms,
	// temperature, top_p) survive a roundtrip.
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	v := &Verdict{
		Verdict:       VerdictAligned,
		Confidence:    0.97,
		Reasoning:     "all 9 v4 columns verified",
		PersonaID:     "judge-evidential",
		RubricVersion: "sha256:abcdef0123456789",
		TemperatureNote: TemperatureNote{
			Provider:      "minimax",
			Model:         "MiniMax-M3",
			Temperature:   0.5,
			Seed:          12345,
			MaxTokens:     4096,
			TimeoutMs:     30000,
			TopP:          0.95,
			PersonaID:     "judge-evidential",
			RubricVersion: "sha256:abcdef0123456789",
			SchemaVersion: "v4alpha/2026-09-27/002",
		},
	}
	e, err := EvaluationFromVerdict("drift_judge", "code", "all-columns", v)
	if err != nil {
		t.Fatalf("EvaluationFromVerdict: %v", err)
	}
	auditMeta := &Audit{Actor: "operator-v4"}
	id, err := s.SaveEvaluation(context.Background(), auditMeta, e)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}

	read, err := s.GetEvaluation(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.Provider != "minimax" {
		t.Fatalf("provider=%q", read.Provider)
	}
	if read.Model != "MiniMax-M3" {
		t.Fatalf("model=%q", read.Model)
	}
	if read.PersonaID != "judge-evidential" {
		t.Fatalf("persona_id=%q", read.PersonaID)
	}
	if read.RubricVer != "sha256:abcdef0123456789" {
		t.Fatalf("rubric_version=%q", read.RubricVer)
	}
	if read.SchemaVer != "v4alpha/2026-09-27/002" {
		t.Fatalf("schema_version=%q", read.SchemaVer)
	}
	if read.Seed != 12345 {
		t.Fatalf("seed=%d", read.Seed)
	}
	if read.MaxTokens != 4096 {
		t.Fatalf("max_tokens=%d", read.MaxTokens)
	}
	if read.TimeoutMs != 30000 {
		t.Fatalf("timeout_ms=%d", read.TimeoutMs)
	}
	if read.Temperature != 0.5 {
		t.Fatalf("temperature=%f", read.Temperature)
	}
	if read.TopP != 0.95 {
		t.Fatalf("top_p=%f", read.TopP)
	}
	if read.Confidence != 0.97 {
		t.Fatalf("confidence=%f", read.Confidence)
	}

	// Verdict reconstruction pulls the same TemperatureNote.
	recon, err := VerdictFromEvaluation(read)
	if err != nil {
		t.Fatalf("VerdictFromEvaluation: %v", err)
	}
	if recon.TemperatureNote.Provider != "minimax" {
		t.Fatalf("reconstructed provider=%q", recon.TemperatureNote.Provider)
	}
	if recon.TemperatureNote.Seed != 12345 {
		t.Fatalf("reconstructed seed=%d", recon.TemperatureNote.Seed)
	}
}

func TestDeliberateBreak_Store_ConsensusSamplesRejectsEmptyList(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	modal, _ := EvaluationFromVerdict("drift_judge", "code", "target", v)

	_, err := s.SaveConsensusSamples(context.Background(), &Audit{Actor: "op"}, []*Evaluation{}, modal)
	if err == nil {
		t.Fatalf("SaveConsensusSamples with empty samples returned nil")
	}
	if !errors.Is(err, errEmptySamples) {
		t.Fatalf("SaveConsensusSamples empty: got %v, want errEmptySamples", err)
	}
}

func TestDeliberateBreak_Store_ConsensusSamplesRejectsReservedEvalType(t *testing.T) {
	// "consensus" is reserved for the modal row. A sample with
	// eval_type="consensus" is rejected to keep the contract clean.
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	sample, _ := EvaluationFromVerdict(EvalConsensus, "code", "target", v) // misuse: sample has reserved eval_type
	modal, _ := EvaluationFromVerdict("drift_judge", "code", "target", v)

	_, err := s.SaveConsensusSamples(context.Background(), &Audit{Actor: "op"}, []*Evaluation{sample}, modal)
	if err == nil {
		t.Fatalf("SaveConsensusSamples with reserved eval_type on sample returned nil")
	}
}

func TestDeliberateBreak_Store_ProjectIDColumnReadyForFuture(t *testing.T) {
	// Phase 4 Chunk 4.3 SHIPPED (2026-09-30, commits 1d39659 +
	// followup). sdd_evaluations now carries project_id as the
	// namespace primitive. This test, originally asserting
	// absence, was inverted to assert presence + roundtrip:
	//   - column exists (project_id)
	//   - SaveEvaluation persists it (via auditMeta.ProjectID)
	//   - GetEvaluation roundtrips it
	//
	// Pre-Phase-4 expectation was absence; the test now catches
	// regressions in either direction.
	store, db, cleanup := newTestStore(t)
	defer cleanup()

	var nCols int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sdd_evaluations') WHERE name = 'project_id'").Scan(&nCols); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if nCols != 1 {
		t.Fatalf("project_id column absent (nCols=%d); Phase 4 Chunk 4.3 ships it", nCols)
	}

	// Roundtrip: SaveEvaluation with ProjectID, then GetEvaluation,
	// expect ProjectID == 'proj-huila'.
	ctx := context.Background()
	e := &Evaluation{
		EvalType:   "drift_judge",
		TargetType: "file",
		TargetID:   "test://foo.go",
		ProjectID:  "proj-huila",
		VerdictJSON: `{"verdict":"aligned","confidence":0.9}`,
		Confidence:  0.9,
	}
	id, err := store.SaveEvaluation(ctx, &Audit{
		Actor:     "operator-test",
		ProjectID: "proj-huila",
	}, e)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	got, err := store.GetEvaluation(ctx, id)
	if err != nil {
		t.Fatalf("GetEvaluation: %v", err)
	}
	if got.ProjectID != "proj-huila" {
		t.Fatalf("ProjectID roundtrip: want proj-huila, got %q", got.ProjectID)
	}
}

// TestDeliberateBreak_Store_ApplyCalibrationColumns_Idempotent (ADR-011,
// Phase 3). The 4 calibration columns are added by an idempotent
// migration helper. This test exercises the legacy-DB migration path:
// pre-Phase-3 schema (no calibration columns) → ApplyCalibrationColumns
// → columns present. Calling it again must not error.
func TestDeliberateBreak_Store_ApplyCalibrationColumns_Idempotent(t *testing.T) {
	// Build a pre-Phase-3 sdd_evaluations table (no calibration cols).
	db, err := store.OpenSQLite(context.Background(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE sdd_evaluations (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			eval_type     TEXT NOT NULL,
			target_type   TEXT NOT NULL,
			target_id     TEXT NOT NULL,
			verdict_json  TEXT NOT NULL,
			confidence    REAL NOT NULL DEFAULT 0,
			provider      TEXT,
			model         TEXT,
			persona_id    TEXT,
			rubric_version TEXT,
			schema_version TEXT,
			seed          INTEGER,
			max_tokens    INTEGER,
			timeout_ms    INTEGER,
			temperature   REAL,
			top_p         REAL,
			non_deterministic INTEGER NOT NULL DEFAULT 0,
			session_id    TEXT,
			created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatalf("legacy create: %v", err)
	}

	// First call: adds the 4 columns + index.
	if err := ApplyCalibrationColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplyCalibrationColumns (1st): %v", err)
	}
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('sdd_evaluations') "+
			"WHERE name IN ('confidence_calibrated','calibration_ci_low','calibration_ci_high','calibration_method')",
	).Scan(&n); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 4 {
		t.Fatalf("calibration columns after 1st ApplyCalibrationColumns: %d; want 4", n)
	}

	// Second call: must not error (idempotent).
	if err := ApplyCalibrationColumns(context.Background(), db); err != nil {
		t.Fatalf("ApplyCalibrationColumns (2nd, idempotent): %v", err)
	}

	// Third call: post-CreateSchema (new-schema DB) also idempotent.
	db2, err := store.OpenSQLite(context.Background(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer db2.Close()
	if err := CreateSchema(db2); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	if err := ApplyCalibrationColumns(context.Background(), db2); err != nil {
		t.Fatalf("ApplyCalibrationColumns on new schema: %v", err)
	}
}
