// judge.Store tests (ADR-007 C3).
//
// 12 tests covering the sdd_evaluations persistence layer:
//
//	SCHEMA (3 tests)
//	  1. TestCreateSchema_Idempotent
//	  2. TestCreateSchema_AllIndexesExist
//	  3. TestCreateSchema_NonDeterministicDefaultZero
//
//	SAVE EVALUATION (4 tests)
//	  4. TestSaveEvaluation_HappyPath
//	  5. TestSaveEvaluation_RejectsEmptyAuditActor
//	  6. TestSaveEvaluation_RejectsEmptyEvalType
//	  7. TestSaveEvaluation_AuditLogRowInsertedAtomically
//
//	SAVE CONSENSUS SAMPLES (2 tests)
//	  8. TestSaveConsensusSamples_InsertsNPlus1
//	  9. TestSaveConsensusSamples_ModalRowUsesConsensusEvalType
//
//	READ (3 tests)
//	  10. TestListEvaluations_FilterByEvalType
//	  11. TestLatestEvaluation_ReturnsNewest
//	  12. TestVerdictFromEvaluation_Roundtrips
package judge

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestStore opens an in-memory SQLite with both audit_log and
// sdd_evaluations schemas applied. Returns the Store + the *sql.DB
// (so tests can SELECT directly) + a cleanup.
func newTestStore(t *testing.T) (*Store, *sql.DB, func()) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := CreateSchema(db); err != nil {
		t.Fatalf("judge.CreateSchema: %v", err)
	}
	w := audit.NewWriter(db)
	return NewStore(db, w), db, func() { _ = db.Close() }
}

// fakeVerdict builds a minimal but realistic Verdict for round-trip tests.
func fakeVerdict(verdict string, confidence float64, persona string) *Verdict {
	return &Verdict{
		Verdict:       verdict,
		Confidence:    confidence,
		Reasoning:     "test verdict",
		PersonaID:     persona,
		RubricVersion: "sha256:0000000000000000",
		TemperatureNote: TemperatureNote{
			Provider:      "anthropic",
			Model:         "claude-test",
			Temperature:   0.0,
			Seed:          42,
			MaxTokens:     2048,
			TimeoutMs:     15000,
			TopP:          1.0,
			PersonaID:     persona,
			RubricVersion: "sha256:0000000000000000",
			SchemaVersion: "v4alpha/2026-09-27/001",
		},
	}
}

// --- 1. SCHEMA ---

func TestCreateSchema_Idempotent(t *testing.T) {
	// Calling CreateSchema twice on the same DB must not error.
	db, err := store.OpenSQLite(context.Background(), "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for i := 0; i < 3; i++ {
		if err := CreateSchema(db); err != nil {
			t.Fatalf("CreateSchema call %d: %v", i, err)
		}
	}
}

func TestCreateSchema_AllIndexesExist(t *testing.T) {
	// The 4 indexes promised by CreateSchema must be present.
	s, db, cleanup := newTestStore(t)
	defer cleanup()
	_ = s

	want := []string{
		"idx_sdd_eval_eval_type",
		"idx_sdd_eval_target",
		"idx_sdd_eval_created",
		"idx_sdd_eval_session",
	}
	for _, idx := range want {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?`, idx,
		).Scan(&n); err != nil {
			t.Fatalf("scan %s: %v", idx, err)
		}
		if n != 1 {
			t.Fatalf("index %s not found (n=%d)", idx, n)
		}
	}
}

func TestCreateSchema_NonDeterministicDefaultZero(t *testing.T) {
	// C3 always inserts non_deterministic=0 (C3 doesn't surface the
	// LLMResponse.NonDeterministic flag through Verdict). The column
	// exists for forward compat.
	s, db, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.95, "judge-test")
	e, err := EvaluationFromVerdict("drift_judge", "code", "test-id", v)
	if err != nil {
		t.Fatalf("EvaluationFromVerdict: %v", err)
	}
	auditMeta := &Audit{Actor: "operator-test"}
	id, err := s.SaveEvaluation(context.Background(), auditMeta, e)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}

	var nd int
	if err := db.QueryRow("SELECT non_deterministic FROM sdd_evaluations WHERE id = ?", id).Scan(&nd); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if nd != 0 {
		t.Fatalf("non_deterministic=%d, want 0 (C3 default)", nd)
	}
}

// --- 4. SAVE EVALUATION ---

func TestSaveEvaluation_HappyPath(t *testing.T) {
	s, db, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.95, "judge-logical")
	e, err := EvaluationFromVerdict("drift_judge", "code", "test-id-1", v)
	if err != nil {
		t.Fatalf("EvaluationFromVerdict: %v", err)
	}
	auditMeta := &Audit{Actor: "operator-save", SessionID: "sess-1"}

	id, err := s.SaveEvaluation(context.Background(), auditMeta, e)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}
	if id <= 0 {
		t.Fatalf("SaveEvaluation returned id=%d, want > 0", id)
	}

	// SELECT the row + verify v4 columns populated.
	var (
		evalType, targetType, targetID, provider, model, personaID string
		confidence                                                 float64
	)
	if err := db.QueryRow(
		`SELECT eval_type, target_type, target_id, COALESCE(provider,''), COALESCE(model,''),
		        COALESCE(persona_id,''), confidence
		 FROM sdd_evaluations WHERE id = ?`, id,
	).Scan(&evalType, &targetType, &targetID, &provider, &model, &personaID, &confidence); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if evalType != "drift_judge" {
		t.Fatalf("eval_type=%q", evalType)
	}
	if targetType != "code" {
		t.Fatalf("target_type=%q", targetType)
	}
	if targetID != "test-id-1" {
		t.Fatalf("target_id=%q", targetID)
	}
	if provider != "anthropic" {
		t.Fatalf("provider=%q", provider)
	}
	if model != "claude-test" {
		t.Fatalf("model=%q", model)
	}
	if personaID != "judge-logical" {
		t.Fatalf("persona_id=%q", personaID)
	}
	if confidence != 0.95 {
		t.Fatalf("confidence=%f", confidence)
	}
}

func TestSaveEvaluation_RejectsEmptyAuditActor(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("drift_judge", "code", "id", v)

	cases := []struct {
		name string
		am   *Audit
	}{
		{"nil_audit", nil},
		{"empty_actor_audit", &Audit{Actor: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SaveEvaluation(context.Background(), tc.am, e)
			if !errors.Is(err, ErrEmptyAuditActor) {
				t.Fatalf("SaveEvaluation with %s: got %v, want ErrEmptyAuditActor", tc.name, err)
			}
		})
	}
}

func TestSaveEvaluation_RejectsEmptyEvalType(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("drift_judge", "code", "id", v)
	e.EvalType = "" // break it
	_, err := s.SaveEvaluation(context.Background(), &Audit{Actor: "op"}, e)
	if !errors.Is(err, ErrEmptyEvalType) {
		t.Fatalf("SaveEvaluation with empty eval_type: got %v, want ErrEmptyEvalType", err)
	}
}

func TestSaveEvaluation_AuditLogRowInsertedAtomically(t *testing.T) {
	// The audit_log row carries the INV-1 actor + the event payload.
	s, db, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("drift_judge", "code", "id-atomic", v)
	auditMeta := &Audit{Actor: "operator-atomic", SessionID: "sess-atomic"}

	id, err := s.SaveEvaluation(context.Background(), auditMeta, e)
	if err != nil {
		t.Fatalf("SaveEvaluation: %v", err)
	}

	// audit_log row exists with the actor we passed in.
	var actor, sessionID string
	var payload []byte
	if err := db.QueryRow(
		`SELECT actor, COALESCE(session_id,''), payload FROM audit_log WHERE actor = ?`,
		"operator-atomic",
	).Scan(&actor, &sessionID, &payload); err != nil {
		t.Fatalf("audit_log select: %v", err)
	}
	if actor != "operator-atomic" {
		t.Fatalf("actor=%q", actor)
	}
	if sessionID != "sess-atomic" {
		t.Fatalf("session_id=%q", sessionID)
	}
	if !stringContains(payload, "judge.save") {
		t.Fatalf("audit payload missing event: %s", payload)
	}
	if !stringContains(payload, "\"id\":") {
		t.Fatalf("audit payload missing id field: %s", payload)
	}
	// The id in the payload must match the sdd_evaluations id.
	if !stringContains(payload, intToString(id)) {
		t.Fatalf("audit payload id=%d not in payload: %s", id, payload)
	}
}

// --- 5. SAVE CONSENSUS SAMPLES ---

func TestSaveConsensusSamples_InsertsNPlus1(t *testing.T) {
	// 3 samples + 1 modal = 4 sdd_evaluations rows.
	s, db, cleanup := newTestStore(t)
	defer cleanup()

	auditMeta := &Audit{Actor: "operator-consensus", SessionID: "sess-consensus"}

	samples := []*Evaluation{}
	for i := 0; i < 3; i++ {
		v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
		e, _ := EvaluationFromVerdict("drift_judge", "code", "target-consensus", v)
		samples = append(samples, e)
	}
	modalV := fakeVerdict(VerdictAligned, 0.92, "judge-test")
	modal, _ := EvaluationFromVerdict("drift_judge", "code", "target-consensus", modalV)

	modalID, err := s.SaveConsensusSamples(context.Background(), auditMeta, samples, modal)
	if err != nil {
		t.Fatalf("SaveConsensusSamples: %v", err)
	}
	if modalID <= 0 {
		t.Fatalf("modal id=%d, want > 0", modalID)
	}

	// 4 rows total in sdd_evaluations.
	var total, consensusRows int
	if err := db.QueryRow("SELECT COUNT(*) FROM sdd_evaluations").Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 4 {
		t.Fatalf("sdd_evaluations rows=%d, want 4 (3 samples + 1 modal)", total)
	}

	// 1 row has eval_type=consensus.
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sdd_evaluations WHERE eval_type = ?", EvalConsensus,
	).Scan(&consensusRows); err != nil {
		t.Fatalf("count consensus: %v", err)
	}
	if consensusRows != 1 {
		t.Fatalf("consensus rows=%d, want 1", consensusRows)
	}

	// 4 audit_log rows (1 per sample + 1 for modal).
	var auditRows int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM audit_log WHERE actor = ?", "operator-consensus",
	).Scan(&auditRows); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditRows != 4 {
		t.Fatalf("audit_log rows for consensus=%d, want 4", auditRows)
	}
}

func TestSaveConsensusSamples_ModalRowUsesConsensusEvalType(t *testing.T) {
	// Modal row's eval_type is forced to "consensus" regardless of
	// what the caller passes in (sentinel enforcement).
	s, db, cleanup := newTestStore(t)
	defer cleanup()

	auditMeta := &Audit{Actor: "operator"}

	samples := []*Evaluation{}
	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("drift_judge", "code", "target", v)
	samples = append(samples, e)

	modalV := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	// Caller mistakenly sets EvalType to "drift_judge" on the modal.
	modal, _ := EvaluationFromVerdict("drift_judge", "code", "target", modalV)

	modalID, err := s.SaveConsensusSamples(context.Background(), auditMeta, samples, modal)
	if err != nil {
		t.Fatalf("SaveConsensusSamples: %v", err)
	}

	var evalType string
	if err := db.QueryRow("SELECT eval_type FROM sdd_evaluations WHERE id = ?", modalID).Scan(&evalType); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if evalType != EvalConsensus {
		t.Fatalf("modal eval_type=%q, want %q", evalType, EvalConsensus)
	}
}

// --- 6. READ ---

func TestListEvaluations_FilterByEvalType(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	auditMeta := &Audit{Actor: "operator"}

	// 2 drift_judge rows + 1 brand_match row.
	for i := 0; i < 2; i++ {
		v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
		e, _ := EvaluationFromVerdict("drift_judge", "code", "target", v)
		if _, err := s.SaveEvaluation(context.Background(), auditMeta, e); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
	e, _ := EvaluationFromVerdict("brand_match", "image", "target", v)
	if _, err := s.SaveEvaluation(context.Background(), auditMeta, e); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Filter: drift_judge only.
	rows, err := s.ListEvaluations(context.Background(), ListFilter{EvalType: "drift_judge"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("drift_judge rows=%d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.EvalType != "drift_judge" {
			t.Fatalf("filtered row has eval_type=%q", r.EvalType)
		}
	}
}

func TestLatestEvaluation_ReturnsNewest(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	auditMeta := &Audit{Actor: "operator"}

	// 3 rows, increasing ids.
	for i := 0; i < 3; i++ {
		v := fakeVerdict(VerdictAligned, 0.9, "judge-test")
		e, _ := EvaluationFromVerdict("drift_judge", "code", "latest-target", v)
		if _, err := s.SaveEvaluation(context.Background(), auditMeta, e); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	latest, err := s.LatestEvaluation(context.Background(), "drift_judge", "code", "latest-target")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.ID != 3 {
		t.Fatalf("latest id=%d, want 3", latest.ID)
	}
}

func TestVerdictFromEvaluation_Roundtrips(t *testing.T) {
	// VerdictFromEvaluation(EvaluationFromVerdict(v)) reconstructs
	// a Verdict with the same shape. Spot-check on the fields that
	// are persisted to columns (provider, model, persona_id).
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	v := fakeVerdict(VerdictAligned, 0.93, "judge-cross-modal")
	e, err := EvaluationFromVerdict("drift_judge", "image", "roundtrip-target", v)
	if err != nil {
		t.Fatalf("EvaluationFromVerdict: %v", err)
	}
	auditMeta := &Audit{Actor: "operator"}
	id, err := s.SaveEvaluation(context.Background(), auditMeta, e)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	read, err := s.GetEvaluation(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if read.Provider != "anthropic" {
		t.Fatalf("provider=%q", read.Provider)
	}
	if read.Model != "claude-test" {
		t.Fatalf("model=%q", read.Model)
	}
	if read.PersonaID != "judge-cross-modal" {
		t.Fatalf("persona_id=%q", read.PersonaID)
	}
	if read.Confidence != 0.93 {
		t.Fatalf("confidence=%f", read.Confidence)
	}

	// VerdictFromEvaluation reconstructs the inner Verdict.
	reconstructed, err := VerdictFromEvaluation(read)
	if err != nil {
		t.Fatalf("VerdictFromEvaluation: %v", err)
	}
	if reconstructed.Verdict != VerdictAligned {
		t.Fatalf("reconstructed verdict=%q", reconstructed.Verdict)
	}
	if reconstructed.PersonaID != "judge-cross-modal" {
		t.Fatalf("reconstructed persona_id=%q", reconstructed.PersonaID)
	}
	if reconstructed.TemperatureNote.Model != "claude-test" {
		t.Fatalf("reconstructed model=%q", reconstructed.TemperatureNote.Model)
	}
}

// --- helpers ---

func stringContains(haystack []byte, needle string) bool {
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}

func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
