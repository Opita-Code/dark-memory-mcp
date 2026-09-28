// Package agent_memory — unit tests for the v4alpha
// implementation. Mirrors the v3.0 agent_memory spec (note /
// observation / decision / finding / todo / link / context)
// with the v4 simplification: no embedding, no Mem0 three-class
// taxonomy. Those land in BUG-9.
//
// C3 (ADR-007): every Save/Update/Archive now emits one
// audit_log row inside the same Tx. Existing tests still
// cover the data path; new audit-specific tests live in
// agent_memory_audit_test.go.
package agent_memory

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestDB returns a fresh *sql.DB with CreateSchema applied
// and an audit.Writer wired to it (ADR-007 C3: INV-1 closure).
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
	if err := audit.CreateSchema(d); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	w := audit.NewWriter(d)
	return func() { _ = d.Close() }, NewStore(d, w)
}

// testAudit returns the canonical test audit metadata. ADR-007 C3
// made Audit a mandatory argument; tests that don't care about
// the audit specifics can use this default.
func testAudit() *Audit { return &Audit{Actor: "nico"} }

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
	id, err := s.Save(context.Background(), testAudit(), "nico", KindDecision,
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
	_, err := s.Save(context.Background(), testAudit(), "", KindNote, "", "body", "", false)
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
	_, err := s.Save(context.Background(), testAudit(), "nico", KindNote, "t", "", "", false)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestSave_RejectsInvalidKind(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	_, err := s.Save(context.Background(), testAudit(), "nico", "bogus", "t", "c", "", false)
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
		_, err := s.Save(context.Background(), testAudit(), "nico", k, "t", "c-"+k, "", false)
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
	id, err := s.Save(context.Background(), testAudit(), "nico", KindDecision,
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
	if _, err := s.Save(context.Background(), testAudit(), "alice", KindNote, "", "red apple", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testAudit(), "bob", KindNote, "", "blue apple", "", false); err != nil {
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
	if _, err := s.Save(context.Background(), testAudit(), "nico", KindNote, "t1", "first", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testAudit(), "nico", KindNote, "t2", "second-pinned", "", true); err != nil {
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
	if _, err := s.Save(context.Background(), testAudit(), "alice", KindNote, "t", "alice's note", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(context.Background(), testAudit(), "bob", KindNote, "t", "bob's note", "", false); err != nil {
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
	id, err := s.Save(context.Background(), testAudit(), "nico", KindNote, "t", "to be archived", "", false)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Archive(context.Background(), testAudit(), id); err != nil {
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
	if err := s.Archive(context.Background(), testAudit(), 99999); err == nil {
		t.Fatal("expected ErrNotFound for missing id")
	}
}

// --- Update ---

func TestUpdate_MutatesFieldsAndReSyncsFTS(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id, err := s.Save(ctx, testAudit(), "nico", KindNote, "original", "the busy_timeout was set wrong", "BUG-5", false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Update content + tags + pinned. Title left untouched (nil).
	pin := true
	newContent := "the busy_timeout pragma is the right fix"
	newTags := "BUG-5,SQLite,fix"
	if err := s.Update(ctx, testAudit(), id, nil, &newContent, &newTags, &pin); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != newContent {
		t.Errorf("content = %q; want %q", got.Content, newContent)
	}
	if got.Tags != newTags {
		t.Errorf("tags = %q; want %q", got.Tags, newTags)
	}
	if !got.Pinned {
		t.Errorf("pinned = false; want true")
	}
	if got.Title != "original" {
		t.Errorf("title = %q; want %q (nil should leave it alone)", got.Title, "original")
	}

	// FTS5 sync: the NEW content must be searchable.
	rows, err := s.Recall(ctx, "nico", "pragma", 10)
	if err != nil {
		t.Fatalf("Recall after update: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("FTS5 sync broken: recall count = %d; want 1", len(rows))
	}
	// The OLD content keyword must NOT match anymore.
	rows, err = s.Recall(ctx, "nico", "set_wrong OR wrong", 10)
	if err != nil {
		t.Fatalf("Recall for old content: %v", err)
	}
	for _, r := range rows {
		if r.ID == id && strings.Contains(r.Content, "set wrong") {
			t.Errorf("FTS5 stale entry: id=%d still has old content", r.ID)
		}
	}
}

func TestUpdate_RejectsEmptyContent(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id, _ := s.Save(ctx, testAudit(), "nico", KindNote, "", "original", "", false)
	empty := ""
	if err := s.Update(ctx, testAudit(), id, nil, &empty, nil, nil); err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestUpdate_NotFound(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	c := "x"
	if err := s.Update(context.Background(), testAudit(), 99999, nil, &c, nil, nil); err == nil {
		t.Fatal("expected ErrNotFound for missing id")
	}
}

func TestUpdate_NoFieldsIsNoOp(t *testing.T) {
	cleanup, s := newTestDB(t)
	defer cleanup()
	ctx := context.Background()
	id, _ := s.Save(ctx, testAudit(), "nico", KindNote, "", "hello world", "tag", false)
	if err := s.Update(ctx, testAudit(), id, nil, nil, nil, nil); err != nil {
		t.Fatalf("Update with no fields: %v", err)
	}
	got, _ := s.Get(ctx, id)
	if got.Content != "hello world" {
		t.Errorf("content changed: got %q", got.Content)
	}
}

// Suppress unused-import warnings if a refactor removes one of
// the tests above (sql is referenced through store.OpenSQLite
// internally; left here so the test file compiles cleanly
// without an unused-imports error).

// --- PRE-1 C1: RecallFiltered / ListFiltered tests ---

// TestRecallFiltered_TagPrefix proves the new TagPrefix filter
// matches any tag in the CSV that starts with the given prefix.
// This is the loadout protocol's "give me all docs_index rows"
// query: tag_prefix="doc-index:".
func TestRecallFiltered_TagPrefix(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	am := testAudit()

	mustSaveC1(t, s, am, "nico", KindLink, "RUNBOOK v1",
		"primary operator manual for dark-memory-mcp", "doc-index:v1,doc:RUNBOOK", true)
	mustSaveC1(t, s, am, "nico", KindLink, "INVARIANTS v1",
		"the eight operational rules", "doc-index:v1,doc:INVARIANTS", true)
	mustSaveC1(t, s, am, "nico", KindLink, "RUNBOOK v0",
		"old version of the runbook", "doc-index:v0,doc:RUNBOOK", true)
	mustSaveC1(t, s, am, "nico", KindNote, "random note",
		"this has nothing to do with docs", "personal,misc", false)

	// Query with tag_prefix="doc-index:" should hit all 3 doc-index rows.
	rows, err := s.RecallFiltered(ctx, "nico", "runbook OR rules",
		RecallFilter{TagPrefix: "doc-index:"}, 10)
	if err != nil {
		t.Fatalf("RecallFiltered: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("RecallFiltered returned %d rows, want 3 (the 3 doc-index rows)", len(rows))
	}
	for _, r := range rows {
		if !strings.Contains(r.Tags, "doc-index:") {
			t.Errorf("row %d has tags %q, want one with doc-index: prefix", r.ID, r.Tags)
		}
	}
}

// TestRecallFiltered_TagPrefix_NoFalsePositive proves the
// tag_prefix filter does NOT match partial tags (e.g. "doc"
// should NOT match a row tagged "doc-index:v1" because we
// require the comma boundary).
func TestRecallFiltered_TagPrefix_NoFalsePositive(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	am := testAudit()
	// Tag "doc" with surrounding commas is a different tag from
	// "doc-index:v1" which starts with "doc-".
	mustSaveC1(t, s, am, "nico", KindNote, "doc-only",
		"keyword alpha content", "doc", false)
	mustSaveC1(t, s, am, "nico", KindNote, "doc-index",
		"keyword bravo content", "doc-index:v1", false)

	// tag_prefix="doc-index:" should match only the second row.
	rows, err := s.RecallFiltered(ctx, "nico", "keyword",
		RecallFilter{TagPrefix: "doc-index:"}, 10)
	if err != nil {
		t.Fatalf("RecallFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (only the doc-index:v1 row)", len(rows))
	}
	if rows[0].Title != "doc-index" {
		t.Errorf("got title %q, want 'doc-index'", rows[0].Title)
	}
}

// TestRecallFiltered_KindFilter proves the Kind filter narrows
// the result to one canonical kind.
func TestRecallFiltered_KindFilter(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	am := testAudit()
	mustSaveC1(t, s, am, "nico", KindNote, "shared content",
		"this content appears in both note and link", "shared", false)
	mustSaveC1(t, s, am, "nico", KindLink, "shared content",
		"this content appears in both note and link", "shared", false)

	rows, err := s.RecallFiltered(ctx, "nico", "shared",
		RecallFilter{Kind: KindLink}, 10)
	if err != nil {
		t.Fatalf("RecallFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Kind != KindLink {
		t.Errorf("row kind = %q, want %q", rows[0].Kind, KindLink)
	}
}

// TestListFiltered_KindFilter proves the kind filter on List.
func TestListFiltered_KindFilter(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	am := testAudit()
	mustSaveC1(t, s, am, "nico", KindNote, "n1", "a note", "t", false)
	mustSaveC1(t, s, am, "nico", KindLink, "l1", "a link", "t", false)
	mustSaveC1(t, s, am, "nico", KindLink, "l2", "another link", "t", true)

	rows, err := s.ListFiltered(ctx, "nico", ListFilter{Kind: KindLink}, 10)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("got %d rows, want 2 (the 2 links)", len(rows))
	}
	for _, r := range rows {
		if r.Kind != KindLink {
			t.Errorf("row %d kind = %q, want link", r.ID, r.Kind)
		}
	}
}

// TestListFiltered_PinnedOnly proves the Pinned pointer filter
// distinguishes true (only pinned) from false (only unpinned).
func TestListFiltered_PinnedOnly(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	am := testAudit()
	mustSaveC1(t, s, am, "nico", KindNote, "pinned", "x", "t", true)
	mustSaveC1(t, s, am, "nico", KindNote, "unpinned", "x", "t", false)
	mustSaveC1(t, s, am, "nico", KindNote, "unpinned2", "x", "t", false)

	pinned := true
	rows, err := s.ListFiltered(ctx, "nico", ListFilter{Pinned: &pinned}, 10)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("pinned=true got %d, want 1", len(rows))
	}
	if !rows[0].Pinned {
		t.Errorf("pinned=true returned unpinned row")
	}

	unpinned := false
	rows, err = s.ListFiltered(ctx, "nico", ListFilter{Pinned: &unpinned}, 10)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("pinned=false got %d, want 2", len(rows))
	}
}

// TestListFiltered_TagExact proves the exact tag filter
// matches a single tag in the CSV.
func TestListFiltered_TagExact(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	am := testAudit()
	mustSaveC1(t, s, am, "nico", KindNote, "with-tag", "content", "alpha,beta,gamma", false)
	mustSaveC1(t, s, am, "nico", KindNote, "no-tag", "content", "delta,epsilon", false)
	mustSaveC1(t, s, am, "nico", KindNote, "with-prefix", "content", "alpha-extra,zeta", false)

	rows, err := s.ListFiltered(ctx, "nico", ListFilter{Tag: "alpha"}, 10)
	if err != nil {
		t.Fatalf("ListFiltered: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("got %d, want 1 (only the exact 'alpha' tag)", len(rows))
	}
	if len(rows) >= 1 && rows[0].Title != "with-tag" {
		t.Errorf("got title %q, want 'with-tag'", rows[0].Title)
	}
}

// TestEscapeLike_EscapesWildcards proves the LIKE escape helper
// handles the 3 wildcards (% _ and the escape char itself).
func TestEscapeLike_EscapesWildcards(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"with%percent", `with\%percent`},
		{"with_underscore", `with\_underscore`},
		{`with\backslash`, `with\\backslash`},
		{"a%b_c\\d", `a\%b\_c\\d`},
		{"", ""},
	}
	for _, c := range cases {
		got := escapeLike(c.in)
		if got != c.want {
			t.Errorf("escapeLike(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestListFiltered_InvalidKind proves the kind validation
// (rejects unknown kinds).
func TestListFiltered_InvalidKind(t *testing.T) {
	ctx := context.Background()
	cleanup, s := newTestDB(t)
	defer cleanup()
	_, err := s.ListFiltered(ctx, "nico", ListFilter{Kind: "bogus"}, 10)
	if err == nil {
		t.Fatal("ListFiltered with bogus kind returned nil error")
	}
	if !errors.Is(err, ErrInvalidKind) {
		t.Errorf("got %v, want ErrInvalidKind", err)
	}
}

// mustSaveC1 is a test helper that fails the test on error.
// Named with the C1 suffix to avoid colliding with any
// potential helper added in a later chunk.
func mustSaveC1(t *testing.T, s *Store, am *Audit, op, kind, title, content, tags string, pinned bool) {
	t.Helper()
	if _, err := s.Save(context.Background(), am, op, kind, title, content, tags, pinned); err != nil {
		t.Fatalf("Save: %v", err)
	}
}
var _ sql.IsolationLevel
