package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// newDriftChainTestStore opens a fresh SQLite Store with the
// "default" project active, suitable for drift chain tests. Caller
// must call cleanup when done.
func newDriftChainTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "chain-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("runtime.Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	cleanup := func() { _ = st.Close() }
	return st, cleanup
}

// saveDriftAndArtifact inserts a minimal artifact (so the drift
// report's artifact_id FK is satisfied) and a drift report with the
// given content. Returns the drift report's id.
func saveDriftAndArtifact(t *testing.T, st store.Store, verdict, specDiff, reasoning, createdAt string) int64 {
	t.Helper()
	ctx := context.Background()
	wc := store.WriteContext{
		Actor:        "test",
		SessionID:    "test-sess",
		WritePath:    "test",
		ConstitutionID: "test-const",
	}
	// 1. Minimal artifact.
	art := &vibeflow.Artifact{
		SessionID:        "test-sess",
		VibeCase:         "C1",
		ArtifactURL:      "file:///tmp/test.txt",
		ArtifactType:     "code",
		ValidationStatus: "pending",
	}
	artID, err := st.SaveArtifact(ctx, wc, art)
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	// 2. Drift report.
	drift := &vibeflow.DriftReport{
		ArtifactID:     artID,
		Verdict:        verdict,
		SpecDiff:       specDiff,
		JudgeReasoning: reasoning,
		CreatedAt:      createdAt,
	}
	id, err := st.SaveDriftReport(ctx, wc, drift)
	if err != nil {
		t.Fatalf("SaveDriftReport: %v", err)
	}
	return id
}

// ----- Integration tests for v2.20.0 T04 (spec 1276) -----

func TestSaveDriftReport_ComputesMerkleRoot(t *testing.T) {
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	_ = store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"} //nolint

	id := saveDriftAndArtifact(t, st, "aligned", "{}", "ok", "2026-08-19T00:00:00Z")

	// Read the row directly to confirm merkle_root was set.
	sqliteStore, ok := st.(*Store)
	if !ok {
		t.Fatalf("expected *sqlite.Store, got %T", st)
	}
	var root string
	err := sqliteStore.db.QueryRowContext(ctx,
		`SELECT merkle_root FROM vibe_drift_reports WHERE id = ?`, id).Scan(&root)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(root) != 64 {
		t.Errorf("merkle_root length = %d, want 64", len(root))
	}
}

func TestVerifyDriftChain_Sequential_Verifies(t *testing.T) {
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"}

	for i := 0; i < 5; i++ {
		ts := fmt.Sprintf("2026-08-19T00:00:%02dZ", i)
		saveDriftAndArtifact(t, st, "aligned", "{}", "ok", ts)
	}
	res, err := st.VerifyDriftChain(ctx, wc)
	if err != nil {
		t.Fatalf("VerifyDriftChain: %v", err)
	}
	if !res.OK {
		t.Errorf("sequential chain should verify, got %+v", res)
	}
	if res.RowsChecked != 5 {
		t.Errorf("RowsChecked = %d, want 5", res.RowsChecked)
	}
	if len(res.ChainHead) != 64 {
		t.Errorf("ChainHead length = %d, want 64", len(res.ChainHead))
	}
}

func TestVerifyDriftChain_Empty_Verifies(t *testing.T) {
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"}
	res, err := st.VerifyDriftChain(ctx, wc)
	if err != nil {
		t.Fatalf("VerifyDriftChain: %v", err)
	}
	if !res.OK {
		t.Errorf("empty chain should pass, got %+v", res)
	}
	if res.RowsChecked != 0 {
		t.Errorf("RowsChecked = %d, want 0", res.RowsChecked)
	}
}

func TestVerifyDriftChain_AfterDirectTamper_Detected(t *testing.T) {
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"}

	for i := 0; i < 3; i++ {
		ts := fmt.Sprintf("2026-08-19T00:00:%02dZ", i)
		saveDriftAndArtifact(t, st, "aligned", "{}", "ok", ts)
	}
	// Direct tamper: change row 2's verdict in the DB (bypassing
	// UpdateDriftReportVerdict, simulating an attacker with raw SQL
	// access). The merkle_root stays at the original value.
	sqliteStore, ok := st.(*Store)
	if !ok {
		t.Fatalf("expected *sqlite.Store, got %T", st)
	}
	if _, err := sqliteStore.db.ExecContext(ctx,
		`UPDATE vibe_drift_reports SET verdict = 'drift_detected' WHERE id = 2`); err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}
	res, err := st.VerifyDriftChain(ctx, wc)
	if err != nil {
		t.Fatalf("VerifyDriftChain: %v", err)
	}
	if res.OK {
		t.Error("tampered chain should fail verification")
	}
	if res.FirstBadID != 2 {
		t.Errorf("FirstBadID = %d, want 2", res.FirstBadID)
	}
}

func TestVerifyDriftChain_AfterDelete_Detected(t *testing.T) {
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"}

	for i := 0; i < 3; i++ {
		ts := fmt.Sprintf("2026-08-19T00:00:%02dZ", i)
		saveDriftAndArtifact(t, st, "aligned", "{}", "ok", ts)
	}
	sqliteStore, ok := st.(*Store)
	if !ok {
		t.Fatalf("expected *sqlite.Store, got %T", st)
	}
	// Direct delete: row 2 disappears. Row 3's prev becomes row 1.
	if _, err := sqliteStore.db.ExecContext(ctx,
		`DELETE FROM vibe_drift_reports WHERE id = 2`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, err := st.VerifyDriftChain(ctx, wc)
	if err != nil {
		t.Fatalf("VerifyDriftChain: %v", err)
	}
	if res.OK {
		t.Error("deleted-middle should fail verification")
	}
	if res.FirstBadID != 3 {
		t.Errorf("FirstBadID = %d, want 3", res.FirstBadID)
	}
}

func TestVerifyDriftChain_ProjectScoped(t *testing.T) {
	// INV-7: rows from other projects are not in the active chain.
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"}

	// Write a row to "default" project.
	saveDriftAndArtifact(t, st, "aligned", "{}", "ok", "2026-08-19T00:00:00Z")

	// Create + activate a different project.
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "other", DisplayName: "Other"}); err != nil {
		t.Fatalf("CreateProject other: %v", err)
	}
	if err := st.SetActiveProject(ctx, "other"); err != nil {
		t.Fatalf("SetActiveProject other: %v", err)
	}
	// Default-project row not visible; empty chain for "other".
	res, err := st.VerifyDriftChain(ctx, wc)
	if err != nil {
		t.Fatalf("VerifyDriftChain: %v", err)
	}
	if !res.OK {
		t.Errorf("other-project empty chain should pass, got %+v", res)
	}
	if res.RowsChecked != 0 {
		t.Errorf("RowsChecked = %d, want 0", res.RowsChecked)
	}
}

func TestSaveDriftReport_Concurrent_AllValid(t *testing.T) {
	// 100 concurrent SaveDriftReport calls (different artifacts each,
	// since artifact_id has a UNIQUE-ish relationship). Verify the
	// resulting chain after all writes complete.
	st, cleanup := newDriftChainTestStore(t)
	defer cleanup()
	ctx := context.Background()
	wc := store.WriteContext{Actor: "test", SessionID: "s", WritePath: "p", ConstitutionID: "c"}

	const n = 50
	ids := make([]int64, n)
	errs := make([]error, n)
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			ts := fmt.Sprintf("2026-08-19T00:%02d:%02dZ", i/60, i%60)
			ids[i], errs[i] = func() (int64, error) {
				// Insert artifact + drift.
				art := &vibeflow.Artifact{
					SessionID:        "s",
					VibeCase:         "C1",
					ArtifactURL:      fmt.Sprintf("file:///tmp/test-%d.txt", i),
					ArtifactType:     "code",
					ValidationStatus: "pending",
				}
				artID, err := st.SaveArtifact(ctx, wc, art)
				if err != nil {
					return 0, err
				}
				drift := &vibeflow.DriftReport{
					ArtifactID:     artID,
					Verdict:        "aligned",
					SpecDiff:       "{}",
					JudgeReasoning: "ok",
					CreatedAt:      ts,
				}
				return st.SaveDriftReport(ctx, wc, drift)
			}()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
	}
	res, err := st.VerifyDriftChain(ctx, wc)
	if err != nil {
		t.Fatalf("VerifyDriftChain: %v", err)
	}
	if !res.OK {
		t.Errorf("concurrent chain should verify, got %+v", res)
	}
	if res.RowsChecked != n {
		t.Errorf("RowsChecked = %d, want %d", res.RowsChecked, n)
	}
}

// touchWriteContext is a placeholder to silence the linter about
// unused wc in some tests; not actually used directly but kept to
// avoid losing future per-test wc customizations.
var _ = audit.WriteEvent{}