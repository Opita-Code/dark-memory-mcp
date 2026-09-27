package vibe

import (
	"context"
	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// testHelper is the minimal subset of *testing.T + *rapid.T that
// the test helpers need. Both types implement it (rapid.T has
// Helper, Fatalf, Cleanup since v1.0).
type testHelper interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// newTestDB returns a fresh in-memory SQLite connection with the
// spec + artifact + drift schemas. Audit schema is created via
// audit.CreateSchema (caller's responsibility).
//
// Uses store.OpenSQLite so the pragma set matches production
// (WAL + busy_timeout + bounded pool). The ":memory:" DSN gives
// each *sql.DB its own private database; concurrent tests live in
// store_test.go / cap_store_concurrent_test.go with shared DSN.
func newTestDB(t testHelper) *sql.DB {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := CreateSpecSchema(db); err != nil {
		t.Fatalf("CreateSpecSchema: %v", err)
	}
	if err := CreateArtifactSchema(db); err != nil {
		t.Fatalf("CreateArtifactSchema: %v", err)
	}
	if err := CreateDriftSchema(db); err != nil {
		t.Fatalf("CreateDriftSchema: %v", err)
	}
	return db
}

// insertFakeSpec inserts a minimal vibe_specs row using a real Spec.
// It validates the spec first, then persists it via SpecStore.Insert.
func insertFakeSpec(t testHelper, db *sql.DB) int64 {
	t.Helper()
	spec := &Spec{
		VibeCase: CaseC1,
		Intent:   "test fixture",
		Tasks:    []Task{{ID: "t1", Description: "fixture task"}},
	}
	store := NewSpecStore(db)
	id, err := store.Insert(context.Background(), spec)
	if err != nil {
		t.Fatalf("insert fake spec: %v", err)
	}
	return id
}

// validArtifact returns a minimal Artifact with a fake spec already
// inserted in the DB. Tests then break one invariant at a time.
func validArtifact(t testHelper, db *sql.DB) *Artifact {
	t.Helper()
	specID := insertFakeSpec(t, db)
	return &Artifact{
		SpecID: specID,
		Type:   ArtifactTypeCode,
		URL:    "file:///tmp/foo.go",
	}
}
