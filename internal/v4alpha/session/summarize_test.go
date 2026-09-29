package session_test

// PRE-1 C4 tests for summarize + skill_loaded.
//
// Three tests cover the contract:
//   1. TestSummarize_EmptySession — minimal session returns
//      a Summary with empty lists (not an error).
//   2. TestSummarize_PopulatedSession — pinned rows + todos
//      + audit_log rows surface in the summary.
//   3. TestSkillLoad_RoundTrip — write a skill_loaded row,
//      summarize, find it via tag_prefix.
//
// Tests live in session_test (external package) so they
// exercise the public API exactly as the MCP transport
// does. Internal helpers (parseSkillLoad, queryAuditRows)
// are covered indirectly via these public tests.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	am "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/session"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// summarizeEnv bundles the wired environment for summarize
// tests. All three stores share one *sql.DB.
type summarizeEnv struct {
	DB           *sql.DB
	SessionStore *session.Store
	AmStore      *am.Store
}

// newTestSummarizeEnv wires up a full environment for
// summarize tests: session store + agent_memory store +
// shared *sql.DB. Cleanup is registered.
func newTestSummarizeEnv(t *testing.T) *summarizeEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := session.CreateSchema(db); err != nil {
		t.Fatalf("session.CreateSchema: %v", err)
	}
	if err := am.CreateSchema(db); err != nil {
		t.Fatalf("am.CreateSchema: %v", err)
	}

	w := audit.NewWriter(db)
	return &summarizeEnv{
		DB:           db,
		SessionStore: session.NewStore(db, w),
		AmStore:      am.NewStore(db, w),
	}
}

// TestSummarize_EmptySession — a fresh session with no
// writes, no pinned rows, no todos, no skills returns a
// Summary with empty lists. NOT an error. The Markdown
// output is valid and explains what's missing.
func TestSummarize_EmptySession(t *testing.T) {
	env := newTestSummarizeEnv(t)
	ctx := context.Background()

	sess, err := env.SessionStore.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	summary, err := session.Summarize(ctx, sess.ID, env.SessionStore, env.AmStore, env.DB)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if summary.Session == nil || summary.Session.ID != sess.ID {
		t.Errorf("Summary.Session.ID: expected %q, got %v", sess.ID, summary.Session)
	}
	if len(summary.PinnedRows) != 0 {
		t.Errorf("PinnedRows: expected 0, got %d", len(summary.PinnedRows))
	}
	if len(summary.OpenTodos) != 0 {
		t.Errorf("OpenTodos: expected 0, got %d", len(summary.OpenTodos))
	}
	// Start emits exactly 1 audit_log row (session.start event).
	// An "empty" session still has 1 audit row per INV-1.
	if len(summary.AuditRows) != 1 {
		t.Errorf("AuditRows: expected 1 (start event), got %d", len(summary.AuditRows))
	}
	if len(summary.SkillsLoaded) != 0 {
		t.Errorf("SkillsLoaded: expected 0, got %d", len(summary.SkillsLoaded))
	}

	md := summary.Markdown()
	if !strings.Contains(md, "# Session summary") {
		t.Error("Markdown missing header")
	}
	if !strings.Contains(md, "_No pinned rows for this operator._") {
		t.Error("Markdown missing 'no pinned rows' message")
	}
	if !strings.Contains(md, "_No skill_loaded rows for this session._") {
		t.Error("Markdown missing 'no skill loaded' message")
	}
	if !strings.Contains(md, "What's NOT in this summary") {
		t.Error("Markdown missing 'What's NOT' footer")
	}
}

// TestSummarize_PopulatedSession — pinned rows, todos,
// audit rows surface in the summary. Skill rows are tested
// in TestSkillLoad_RoundTrip.
func TestSummarize_PopulatedSession(t *testing.T) {
	env := newTestSummarizeEnv(t)
	ctx := context.Background()

	sess, err := env.SessionStore.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Add 1 pinned row.
	_, err = env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
		"operator-nico", "link", "Pinned spec doc",
		"This is the pinned content.", "doc:spec,v4alpha", true)
	if err != nil {
		t.Fatalf("am.Save pinned: %v", err)
	}
	// Add 1 todo.
	_, err = env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
		"operator-nico", "todo", "Review ADR-009",
		"Add provider allow-list expansion", "adr:009,review", false)
	if err != nil {
		t.Fatalf("am.Save todo: %v", err)
	}
	// Heartbeat triggers 1 audit row.
	if err := env.SessionStore.Heartbeat(ctx, sess.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	summary, err := session.Summarize(ctx, sess.ID, env.SessionStore, env.AmStore, env.DB)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if len(summary.PinnedRows) != 1 {
		t.Errorf("PinnedRows: expected 1, got %d", len(summary.PinnedRows))
	}
	if summary.PinnedRows[0].Title != "Pinned spec doc" {
		t.Errorf("PinnedRows[0].Title: expected 'Pinned spec doc', got %q", summary.PinnedRows[0].Title)
	}
	if len(summary.OpenTodos) != 1 {
		t.Errorf("OpenTodos: expected 1, got %d", len(summary.OpenTodos))
	}
	if summary.OpenTodos[0].Title != "Review ADR-009" {
		t.Errorf("OpenTodos[0].Title: expected 'Review ADR-009', got %q", summary.OpenTodos[0].Title)
	}
	// audit_log should have at least the Start + Heartbeat rows
	// (2 rows minimum). v1 includes everything in the session.
	if len(summary.AuditRows) < 2 {
		t.Errorf("AuditRows: expected >= 2 (start + heartbeat), got %d", len(summary.AuditRows))
	}

	md := summary.Markdown()
	if !strings.Contains(md, "Pinned spec doc") {
		t.Error("Markdown missing pinned title")
	}
	if !strings.Contains(md, "Review ADR-009") {
		t.Error("Markdown missing todo title")
	}
	if !strings.Contains(md, "session.heartbeat") && !strings.Contains(md, "session.start") {
		t.Error("Markdown missing audit row payload")
	}
}

// TestSkillLoad_RoundTrip — write 2 skill_loaded events,
// summarize, both surface with name/version/source parsed
// from tags.
func TestSkillLoad_RoundTrip(t *testing.T) {
	env := newTestSummarizeEnv(t)
	ctx := context.Background()

	sess, err := env.SessionStore.Start(ctx, "operator-nico", "proj-huila")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Write 2 skill_loaded events.
	auditMeta := &am.Audit{Actor: "operator-nico", SessionID: sess.ID}

	id1, err := session.RecordSkillLoad(ctx, env.AmStore, auditMeta,
		"operator-nico", "dark-memory", "v3.0.0-docfix", "opencode-system")
	if err != nil {
		t.Fatalf("RecordSkillLoad #1: %v", err)
	}
	if id1 <= 0 {
		t.Errorf("RecordSkillLoad #1 id: expected > 0, got %d", id1)
	}

	_, err = session.RecordSkillLoad(ctx, env.AmStore, auditMeta,
		"operator-nico", "fresh-osint", "v1", "agent-auto")
	if err != nil {
		t.Fatalf("RecordSkillLoad #2: %v", err)
	}

	summary, err := session.Summarize(ctx, sess.ID, env.SessionStore, env.AmStore, env.DB)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if len(summary.SkillsLoaded) != 2 {
		t.Fatalf("SkillsLoaded: expected 2, got %d", len(summary.SkillsLoaded))
	}

	// Find dark-memory.
	var dm, fos *session.SkillLoad
	for i := range summary.SkillsLoaded {
		switch summary.SkillsLoaded[i].SkillName {
		case "dark-memory":
			dm = &summary.SkillsLoaded[i]
		case "fresh-osint":
			fos = &summary.SkillsLoaded[i]
		}
	}
	if dm == nil {
		t.Fatal("dark-memory skill not found in summary")
	}
	if dm.Version != "v3.0.0-docfix" {
		t.Errorf("dark-memory Version: expected v3.0.0-docfix, got %q", dm.Version)
	}
	if dm.Source != "opencode-system" {
		t.Errorf("dark-memory Source: expected opencode-system, got %q", dm.Source)
	}
	if dm.RowID != id1 {
		t.Errorf("dark-memory RowID: expected %d, got %d", id1, dm.RowID)
	}
	if fos == nil {
		t.Fatal("fresh-osint skill not found in summary")
	}
	if fos.Version != "v1" {
		t.Errorf("fresh-osint Version: expected v1, got %q", fos.Version)
	}

	md := summary.Markdown()
	if !strings.Contains(md, "**dark-memory**") {
		t.Error("Markdown missing dark-memory")
	}
	if !strings.Contains(md, "v3.0.0-docfix") {
		t.Error("Markdown missing version")
	}
	if !strings.Contains(md, "opencode-system") {
		t.Error("Markdown missing source")
	}
	if !strings.Contains(md, "**fresh-osint**") {
		t.Error("Markdown missing fresh-osint")
	}
	if !strings.Contains(md, "v1 from agent-auto") {
		t.Error("Markdown missing fresh-osint version+source")
	}
	t.Logf("Markdown output:\n%s", md)
}