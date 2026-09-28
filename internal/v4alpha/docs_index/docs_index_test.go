package docs_index

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// newTestStore returns a fresh in-memory agent_memory.Store
// for one test. Uses t.TempDir() so each test has its own DB
// and there's no cross-test pollution.
func newTestStore(t *testing.T) (*agent_memory.Store, func()) {
	t.Helper()
	dsn := t.TempDir() + "/docs_index_test.db"
	db, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := agent_memory.CreateSchema(db); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("create audit schema: %v", err)
	}
	w := audit.NewWriter(db)
	return agent_memory.NewStore(db, w), func() { _ = db.Close() }
}

// TestIndex_EmptyDB_InsertsAllFive is the happy path: a
// fresh install gets all 5 indexed docs on first run.
func TestIndex_EmptyDB_InsertsAllFive(t *testing.T) {
	ctx := context.Background()
	s, cleanup := newTestStore(t)
	defer cleanup()

	res, err := Index(ctx, s)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if res.Inserted != 5 {
		t.Errorf("Inserted = %d, want 5", res.Inserted)
	}
	if res.Updated != 0 {
		t.Errorf("Updated = %d, want 0", res.Updated)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", res.Skipped)
	}
	if res.Indexed != 5 {
		t.Errorf("Indexed = %d, want 5", res.Indexed)
	}
	if res.Version != IndexVersion {
		t.Errorf("Version = %q, want %q", res.Version, IndexVersion)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %d, want 0: %v", len(res.Errors), res.Errors)
	}

	// Verify each row exists with the right shape.
	rows, err := s.List(ctx, SystemOperator, 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("system rows = %d, want 5", len(rows))
	}
	for _, r := range rows {
		if r.Kind != agent_memory.KindLink {
			t.Errorf("row %d kind = %q, want %q", r.ID, r.Kind, agent_memory.KindLink)
		}
		if !r.Pinned {
			t.Errorf("row %d not pinned", r.ID)
		}
		if !strings.Contains(r.Tags, IndexTag) {
			t.Errorf("row %d missing index tag %q (tags=%q)", r.ID, IndexTag, r.Tags)
		}
		if !strings.Contains(r.Tags, "doc:") {
			t.Errorf("row %d missing doc: tag (tags=%q)", r.ID, r.Tags)
		}
	}
}

// TestIndex_AlreadyIndexed_Noop proves idempotency: a
// second Index() with the same version is a clean no-op.
func TestIndex_AlreadyIndexed_Noop(t *testing.T) {
	ctx := context.Background()
	s, cleanup := newTestStore(t)
	defer cleanup()

	// First run.
	if _, err := Index(ctx, s); err != nil {
		t.Fatalf("first Index: %v", err)
	}
	firstCount, err := s.List(ctx, SystemOperator, 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Second run — should be all skipped.
	res, err := Index(ctx, s)
	if err != nil {
		t.Fatalf("second Index: %v", err)
	}
	if res.Inserted != 0 || res.Updated != 0 {
		t.Errorf("second run mutated: inserted=%d updated=%d", res.Inserted, res.Updated)
	}
	if res.Skipped != 5 {
		t.Errorf("Skipped = %d, want 5", res.Skipped)
	}

	// Row count unchanged.
	secondCount, err := s.List(ctx, SystemOperator, 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(firstCount) != len(secondCount) {
		t.Errorf("row count drift: first=%d second=%d", len(firstCount), len(secondCount))
	}
}

// TestIndex_StaleVersion_Updates proves that bumping
// IndexVersion (or having a row with an old version tag)
// triggers an Update, not a duplicate insert.
func TestIndex_StaleVersion_Updates(t *testing.T) {
	ctx := context.Background()
	s, cleanup := newTestStore(t)
	defer cleanup()

	// First run at v1.
	if _, err := Index(ctx, s); err != nil {
		t.Fatalf("first Index: %v", err)
	}

	// Simulate an operator who manually inserted a row at v0
	// (or a previous install of an older version). We
	// archive the v1 row and insert a v0 row with the same
	// name so the indexer sees a stale row.
	existing, err := s.List(ctx, SystemOperator, 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	auditMeta := &agent_memory.Audit{Actor: SystemOperator}
	// Find the RUNBOOK row.
	var runbookID int64
	for _, r := range existing {
		if strings.Contains(r.Tags, "doc:RUNBOOK") {
			runbookID = r.ID
		}
	}
	if runbookID == 0 {
		t.Fatal("RUNBOOK row not found")
	}
	// Archive the v1 RUNBOOK row.
	if err := s.Archive(ctx, auditMeta, runbookID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	// Insert a fake v0 row.
	staleTags := "doc-index:v0,doc:RUNBOOK,operator"
	if _, err := s.Save(ctx, auditMeta, SystemOperator,
		agent_memory.KindLink, "stale RUNBOOK", "stale content", staleTags, true); err != nil {
		t.Fatalf("Save stale: %v", err)
	}

	// Now run Index again. It should:
	//   - skip the v1 rows (4 of them, untouched)
	//   - find the v0 RUNBOOK row and UPDATE it (not insert)
	//   - end up with 5 rows total
	res, err := Index(ctx, s)
	if err != nil {
		t.Fatalf("second Index: %v", err)
	}
	if res.Updated != 1 {
		t.Errorf("Updated = %d, want 1 (the stale v0 RUNBOOK)", res.Updated)
	}
	if res.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4 (the 4 still-v1 rows)", res.Skipped)
	}
	if res.Inserted != 0 {
		t.Errorf("Inserted = %d, want 0 (would be a duplicate)", res.Inserted)
	}

	// Verify only 5 system rows exist.
	all, err := s.List(ctx, SystemOperator, 200)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("system rows = %d, want 5", len(all))
	}
	// And RUNBOOK is now at v1.
	var runbookRowCount int
	for _, r := range all {
		if strings.Contains(r.Tags, "doc:RUNBOOK") {
			runbookRowCount++
			if !strings.Contains(r.Tags, IndexTag) {
				t.Errorf("RUNBOOK row not at v1: tags=%q", r.Tags)
			}
		}
	}
	if runbookRowCount != 1 {
		t.Errorf("RUNBOOK row count = %d, want 1", runbookRowCount)
	}
}

// TestIndex_RecallFindsByContent proves the loadout
// protocol works: a natural-language query hits an
// indexed doc via FTS5.
func TestIndex_RecallFindsByContent(t *testing.T) {
	ctx := context.Background()
	s, cleanup := newTestStore(t)
	defer cleanup()

	if _, err := Index(ctx, s); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Operator asks: "how do I do operator decisions?" — should
	// hit RUNBOOK which has "operator decision logs" in content.
	rows, err := s.Recall(ctx, SystemOperator, "operator decision", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("Recall returned 0 rows for 'operator decision'")
	}
	foundRUNBOOK := false
	for _, r := range rows {
		if strings.Contains(r.Tags, "doc:RUNBOOK") {
			foundRUNBOOK = true
		}
	}
	if !foundRUNBOOK {
		t.Errorf("Recall did not return RUNBOOK row; got %d rows", len(rows))
	}
}

// TestIndex_RecallFindsByTagToken proves that the harness
// can recall by tag token (e.g. "doc:INVARIANTS" or
// "v4alpha") — useful when the operator wants to find
// specific docs by id.
func TestIndex_RecallFindsByTagToken(t *testing.T) {
	ctx := context.Background()
	s, cleanup := newTestStore(t)
	defer cleanup()

	if _, err := Index(ctx, s); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Search for the tag token "v4alpha" — should hit
	// the V4_STATUS doc (which has v4alpha in its tags).
	rows, err := s.Recall(ctx, SystemOperator, "v4alpha", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("Recall returned 0 rows for 'v4alpha'")
	}
	foundV4 := false
	for _, r := range rows {
		if strings.Contains(r.Tags, "doc:v4-status") {
			foundV4 = true
		}
	}
	if !foundV4 {
		t.Errorf("Recall did not return v4-status row; got %d rows", len(rows))
	}
}

// TestIndex_NilStore_Errors proves the defensive guard.
func TestIndex_NilStore_Errors(t *testing.T) {
	_, err := Index(context.Background(), nil)
	if err == nil {
		t.Fatal("Index with nil store returned nil error")
	}
	if !strings.Contains(err.Error(), "store is nil") {
		t.Errorf("error message = %q, want substring 'store is nil'", err.Error())
	}
}

// TestBuildTags_IncludesVersionAndName proves the tag
// format is correct.
func TestBuildTags_IncludesVersionAndName(t *testing.T) {
	d := DocMeta{Name: "RUNBOOK", Tags: "operator,runbook,ops"}
	got := buildTags(d)
	for _, want := range []string{IndexTag, "doc:RUNBOOK", "operator", "runbook", "ops"} {
		if !strings.Contains(got, want) {
			t.Errorf("buildTags(%q) = %q, missing %q", d.Name, got, want)
		}
	}
}

// TestExtractIndexVersion_HandlesAllCases covers the
// version parser: present, missing, mixed, edge.
func TestExtractIndexVersion_HandlesAllCases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"doc-index:v1,doc:RUNBOOK,operator", "v1"},
		{"doc-index:v2,doc:FOO", "v2"},
		{"operator,doc:RUNBOOK,doc-index:v10", "v10"},
		{"", ""},
		{"doc:RUNBOOK,operator", ""}, // no index tag
		{"doc-index:v1", "v1"},
		{" ,doc-index:v3,", "v3"},
	}
	for _, c := range cases {
		got := extractIndexVersion(c.in)
		if got != c.want {
			t.Errorf("extractIndexVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestAllIndexedIDs_Stable proves the canonical id list
// is non-empty and stable.
func TestAllIndexedIDs_Stable(t *testing.T) {
	ids := AllIndexedIDs()
	if len(ids) != len(DocsToIndex) {
		t.Errorf("AllIndexedIDs returned %d, want %d", len(ids), len(DocsToIndex))
	}
	// All ids should be non-empty and unique.
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Error("empty id in AllIndexedIDs")
		}
		if seen[id] {
			t.Errorf("duplicate id %q in AllIndexedIDs", id)
		}
		seen[id] = true
	}
}
