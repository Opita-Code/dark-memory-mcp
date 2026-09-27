// Package agent_memory — unit tests for the v4alpha
// implementation. Mirrors the v3.0 agent_memory spec (note /
// observation / decision / finding / todo / link / context)
// with the v4 simplification: no embedding, no Mem0 three-class
// taxonomy. Those land in BUG-9.
package agent_memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestDB returns a fresh *sql.DB with CreateSchema applied.
// Each test gets its own TempDir so concurrent tests don't race
// on the schema_migrations table.
func newTestDB(t *testing.T) (cleanup func(), s *Store) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "agent_memory_test.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := CreateSchema(d); err != nil {
		t.Fatalf("CreateSchema: %v", err)
	}
	return func() { _ = d.Close() }, NewStore(d)
}

// --- CreateSchema ---

func TestCreateSchema_Idempotent(t *testing.T) {
	cleanup, _ := newTestDB(t)
	cleanup() // close the first DB so TempDir cleanup can unlink the file
	// Open a 2nd DB and re-apply CreateSchema — must succeed
	// without "table already exists" errors.
	dsn := filepath.Join(t.TempDir(), "idem.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := 0; i < 3; i++ {
		if err := CreateSchema(d); err != nil {
			t.Fatalf("CreateSchema call %d: %v", i, err)
		}
	}
}

// TestCreateSchema_IndexesExist is a structural regression
// detector: dropping an index silently degrades List/Recall
// without failing assertions. So we read sqlite_schema and
// assert the 3 secondary indexes + the FTS5 virtual table are
// present.
func TestCreateSchema_IndexesExist(t *testing.T) {
	cleanup, _ := newTestDB(t)
	defer cleanup()
	dsn := filepath.Join(t.TempDir(), "schema_inspect.db")
	d, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := CreateSchema(d); err != nil {
		t.Fatal(err)
	}

	rows, err := d.Query(`
		SELECT name, type FROM sqlite_schema
		WHERE name IN (
			'agent_memory',
			'agent_memory_operator_idx',
			'agent_memory_kind_idx',
			'agent_memory_pinned_idx',
			'agent_memory_fts'
		)
		ORDER BY name
	`)
	if err != nil {
		t.Fatalf("sqlite_schema: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n, ty string
		if err := rows.Scan(&n, &ty); err != nil {
			t.Fatal(err)
		}
		got = append(got, n+":"+ty)
	}
	want := []string{
		"agent_memory:table",
		"agent_memory_fts:table",
		"agent_memory_kind_idx:index",
		"agent_memory_operator_idx:index",
		"agent_memory_pinned_idx:index",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("schema objects =\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// --- Save ---

func TestSave_HappyPath(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	id, err := s.Save(context.Background(), "nico", KindDecision,
		"dark-db concurrency architecture",
		"WAL + busy_timeout=5000 + bounded pool — INV-16",
		"sqlite,concurrency,WAL", true)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id = %d; want > 0", id)
	}

	row, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.Operator != "nico" {
		t.Errorf("Operator = %q", row.Operator)
	}
	if row.Kind != KindDecision {
		t.Errorf("Kind = %q", row.Kind)
	}
	if row.Title != "dark-db concurrency architecture" {
		t.Errorf("Title = %q", row.Title)
	}
	if !row.Pinned {
		t.Errorf("Pinned = false; want true")
	}
}

func TestSave_RejectsEmptyOperator(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	_, err := s.Save(context.Background(), "", KindNote, "", "body", "", false)
	if err == nil {
		t.Fatal("expected error for empty operator")
	}
	if !strings.Contains(err.Error(), "operator") {
		t.Errorf("error should mention operator: %v", err)
	}
}

func TestSave_RejectsEmptyContent(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	_, err := s.Save(context.Background(), "nico", KindNote, "t", "", "", false)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestSave_RejectsInvalidKind(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	_, err := s.Save(context.Background(), "nico", "bogus", "t", "c", "", false)
	if err == nil {
		t.Fatal("expected error for invalid kind")
	}
	if !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("error should mention invalid kind: %v", err)
	}
}

func TestSave_AcceptsAllCanonicalKinds(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	for _, k := range []string{
		KindNote, KindObservation, KindDecision, KindFinding,
		KindTodo, KindLink, KindContext,
	} {
		_, err := s.Save(context.Background(), "nico", k, "t", "c-"+k, "", false)
		if err != nil {
			t.Errorf("kind %q rejected: %v", k, err)
		}
	}
}

// --- Recall (FTS5) ---

// TestRecall_FindsSavedRow proves the FTS5 index is wired: the
// saved row's content matches the query.
func TestRecall_FindsSavedRow(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	id, err := s.Save(context.Background(), "nico", KindDecision,
		"BUG-5 design", "wal + busy_timeout=5000 + bounded pool", "", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = id

	rows, err := s.Recall(context.Background(), "nico", "busy_timeout", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("Recall returned 0 rows for 'busy_timeout'")
	}
	found := false
	for _, r := range rows {
		if r.Kind == KindDecision && strings.Contains(r.Content, "busy_timeout") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Recall rows didn't include the saved decision: %+v", rows)
	}
}

// TestRecall_OperatorScope confirms Recall never crosses
// operator boundaries (FTS5 is global by default; the operator
// filter in the JOIN is what enforces scope).
func TestRecall_OperatorScope(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	if _, err := s.Save(context.Background(), "alice", KindNote, "", "red apple", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), "bob", KindNote, "", "blue apple", "", false); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Recall(context.Background(), "alice", "apple", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Operator != "alice" {
			t.Errorf("operator scope leaked: %+v", r)
		}
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Content, "red") {
		t.Errorf("alice rows = %+v; want exactly 1 'red apple'", rows)
	}
}

func TestRecall_EmptyQueryReturnsEmpty(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	rows, err := s.Recall(context.Background(), "nico", "", 10)
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("empty query returned %d rows; want 0", len(rows))
	}
}

// --- List ---

func TestList_OrdersPinnedFirst(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	if _, err := s.Save(context.Background(), "nico", KindNote, "t1", "first", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), "nico", KindNote, "t2", "second-pinned", "", true); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background(), "nico", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("got %d rows; want 2", len(rows))
	}
	if !rows[0].Pinned {
		t.Errorf("first row not pinned: %+v", rows[0])
	}
}

func TestList_FiltersByOperator(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	if _, err := s.Save(context.Background(), "alice", KindNote, "t", "alice's note", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), "bob", KindNote, "t", "bob's note", "", false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background(), "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Operator != "alice" {
			t.Errorf("operator scope leaked: %+v", r)
		}
	}
	if len(rows) != 1 {
		t.Errorf("alice rows = %d; want 1", len(rows))
	}
}

// --- Archive ---

func TestArchive_RemovesFromBaseAndFTS(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	id, err := s.Save(context.Background(), "nico", KindNote, "t", "to be archived", "", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Archive(context.Background(), id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Base table: gone.
	if _, err := s.Get(context.Background(), id); err == nil {
		t.Errorf("Get after archive returned no error; want not found")
	}
	// FTS5 index: gone — Recall shouldn't see it.
	rows, err := s.Recall(context.Background(), "nico", "archived", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Errorf("FTS5 leaked archived row: %+v", r)
		}
	}
}

func TestArchive_NotFoundReturnsError(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	if err := s.Archive(context.Background(), 99999); err == nil {
		t.Fatal("expected ErrNotFound for missing id")
	}
}

// Suppress unused-import warnings if a refactor removes one of
// the tests above (sql is referenced through store.OpenSQLite
// internally; left here so the test file compiles cleanly
// without an unused-imports error).
var _ sql.IsolationLevel
