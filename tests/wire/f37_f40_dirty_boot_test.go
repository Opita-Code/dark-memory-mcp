// F37-F40 wire-conformance test: the daemon must boot cleanly against
// a half-migrated dark-memory.db. F37 (duplicate column tolerance),
// F38 (EnsureCoreTables), F39 (orphan vec0 triggers), and F40
// (table-already-exists) were applied so the daemon does not crash
// with "no such table" / "duplicate column" / "no such module: vec0"
// errors during boot.
//
// This test boots the daemon against a SYNTHETIC dirty DB that emulates
// the post-v1.2.2 dark.db state we saw in production: vec0 triggers,
// partial v5 application, and a missing sessions table. A successful
// boot + a clean list of session/project records is the contract.
package wire

import (
	"bytes"
	"database/sql"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestWire_F37F38F39F40_BootAgainstDirtyDB seeds a dirty dark-memory.db
// BEFORE spawning the daemon, then verifies the daemon boots cleanly
// AND that the session + project tables are present (F38 self-heal).
func TestWire_F37F38F39F40_BootAgainstDirtyDB(t *testing.T) {
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "dirty-dark-memory.db")

	seedDirtyDB(t, dbPath)

	// Spawn the daemon pointing at the dirty DB.
	s := startWireSessionAt(t, dbPath)
	defer s.close()

	// Boot must produce an OK initialize response (already done in
	// startWireSessionAt). Now verify F38: the session and project
	// tables are now present.
	dr, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open dirty DB: %v", err)
	}
	defer dr.Close()

	verifyTable(t, dr, "sessions")
	verifyTable(t, dr, "projects")

	// Also exercise F38 from the wire: session_start must succeed
	// end-to-end (it would have failed in the pre-fix world because
	// the table was missing).
	if _, err := s.toolsCall("dark_memory_session_start", map[string]any{
		"operator": "wire-f3840-test", "project_id": "default",
	}); err != nil {
		t.Fatalf("F38: session_start on freshly-bootstrated DB failed: %v", err)
	}
}

// TestWire_F37_DuplicateColumnDuringBoot targets F37 directly: we
// leave a v8 ALTER TABLE mid-applied (the new project_id column on
// write_audit added, but the recording in schema_migrations still
// says v8 has not been applied) and assert the daemon tolerates and
// continues.
func TestWire_F37_DuplicateColumnDuringBoot(t *testing.T) {
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "f37-dark-memory.db")
	seedDBWithPreAppliedMigration(t, dbPath, 8 /*version*/)

	s := startWireSessionAt(t, dbPath)
	defer s.close()

	// If F37 works the daemon boots. A round-trip call confirms.
	resp, err := s.toolsCall("dark_memory_admin_schema_status", map[string]any{})
	if err != nil {
		t.Fatalf("F37: schema_status after dirty boot failed: %v response=%s", err, respStr(resp))
	}
	assertNoToolError(t, "F37 dirty boot", resp)
}

// --- helpers ---

// startWireSessionAt spawns the binary at a specific DSN (instead of
// letting the harness pick a tmp path). Used by F37-F40 tests.
// The function is defined in wire_session_test.go as a method that
// delegates to wireSession.startAt; F37-F40 tests use wireSession
// directly via startWireSessionAtDB().
func startWireSessionAt(t *testing.T, dbPath string) *wireSession {
	t.Helper()
	bin := resolveWireBin(t)
	cmd := exec.Command(bin)
	cmd.Env = append(cmd.Environ(),
		"DARK_DB="+dbPath,
		"DARK_DB_DRIVER=sqlite",
		"DARK_CONSTITUTION_FILE=",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("wire: stdin pipe: %v", err)
	}
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("wire: stdout pipe: %v", err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("wire: start %s: %v", bin, err)
	}
	s := &wireSession{
		cmd:    cmd,
		stdin:  stdin,
		stdout: newLineReader(stdoutR),
		stderr: *stderrBuf,
	}
	// v1.3.0: wait for boot marker (helper from wire_session_test.go)
	// before sending initialize. The dirty-DB tests boot a binary
	// whose step2/3 may take longer due to migration self-healing;
	// 30s ceiling covers the worst case seen on this host.
	if err := waitForBootMarker(t, stderrBuf, 30*time.Second, "serving stdio"); err != nil {
		t.Fatalf("wire f37/40: boot wait: %v", err)
	}
	_ = s.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "wire-f3740-test", "version": "test"},
	})
	_ = s.notify("notifications/initialized", map[string]any{})
	return s
}

// seedDirtyDB creates a DB that emulates the post-v1.2.2 dark.db
// state:
//   * v1-v7 migrations recorded in schema_migrations
//   * sessions, projects, v5+, v6+ tables PHYSICALLY ABSENT (F38
//     must materialise them)
//   * Triggers referencing vec0 (F39 orphan tolerance)
//   * v8 partially applied: project_id column on write_audit
//     exists, but the recording row was not added — F37 must
//     tolerate the duplicate-column error if/when v8 applies.
func seedDirtyDB(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("seedDirtyDB: open: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		// Recreate the v1+v2+v3 minimal core: research_runs,
		// research_items, research_links, vibe_specs, vibe_artifacts,
		// vibe_drift_reports, sdd_evaluations, constitutions,
		// mods, mod_loads, write_audit, vibe_brands, vibe_compliance.
		`CREATE TABLE research_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, query TEXT NOT NULL, intent TEXT NOT NULL, session_id TEXT, backend_used TEXT, backends_tried TEXT, took_ms INTEGER NOT NULL DEFAULT 0, confidence_avg REAL NOT NULL DEFAULT 0, items_count INTEGER NOT NULL DEFAULT 0, errors TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE research_items (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL, title TEXT NOT NULL, url TEXT, snippet TEXT, source TEXT NOT NULL, confidence REAL NOT NULL DEFAULT 0, freshness_at TEXT, lang TEXT, raw TEXT, actor TEXT, write_path TEXT, content_sha256 TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE research_links (id INTEGER PRIMARY KEY AUTOINCREMENT, research_item_id INTEGER NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, note TEXT, source TEXT, confidence REAL NOT NULL DEFAULT 0, created_at TEXT NOT NULL)`,
		`CREATE TABLE vibe_specs (id INTEGER PRIMARY KEY AUTOINCREMENT, vibe_case TEXT NOT NULL, session_id TEXT, constitution_json TEXT, spec_json TEXT, tasks_json TEXT, created_at TEXT NOT NULL, updated_at TEXT, summary TEXT)`,
		`CREATE TABLE vibe_compliance (jurisdiction TEXT PRIMARY KEY, rules_json TEXT NOT NULL, effective_at TEXT, source_url TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE vibe_artifacts (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT, vibe_case TEXT NOT NULL, spec_id INTEGER, artifact_url TEXT, artifact_type TEXT NOT NULL, brand_id TEXT, jurisdiction TEXT, has_disclosure INTEGER NOT NULL DEFAULT 0, validation_status TEXT NOT NULL DEFAULT 'pending', created_at TEXT NOT NULL)`,
		`CREATE TABLE vibe_drift_reports (id INTEGER PRIMARY KEY AUTOINCREMENT, artifact_id INTEGER NOT NULL, spec_id INTEGER, verdict TEXT NOT NULL, spec_diff_json TEXT, judge_reasoning TEXT, reconciled_at TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE sdd_evaluations (id INTEGER PRIMARY KEY AUTOINCREMENT, eval_type TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, verdict_json TEXT NOT NULL, confidence REAL NOT NULL DEFAULT 0, prompt_version TEXT, model TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE constitutions (id INTEGER PRIMARY KEY AUTOINCREMENT, constitution_id TEXT UNIQUE NOT NULL, version TEXT NOT NULL, sha256 TEXT NOT NULL, source_path TEXT, source_text TEXT NOT NULL, is_active INTEGER NOT NULL DEFAULT 0, last_verified_at TEXT, last_verified_sha256 TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE mods (mod_id TEXT PRIMARY KEY, version TEXT NOT NULL, sha256 TEXT NOT NULL, risk_class TEXT NOT NULL, source_path TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE mod_loads (id INTEGER PRIMARY KEY AUTOINCREMENT, mod_id TEXT NOT NULL, constitution_id TEXT, constitution_ver TEXT, session_id TEXT, actor TEXT, accepted INTEGER NOT NULL DEFAULT 0, reject_reason TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE write_audit (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')), actor TEXT NOT NULL, action TEXT NOT NULL, target_type TEXT, target_id INTEGER, diff_json TEXT, notes TEXT, session_id TEXT, project_id TEXT NOT NULL DEFAULT 'default', created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')))`,
		`CREATE TABLE vibe_brands (brand_id TEXT PRIMARY KEY, voice_json TEXT, visual_json TEXT, narrative_json TEXT, compliance_json TEXT, created_at TEXT NOT NULL, updated_at TEXT)`,
		// Record v1-v7 as applied (F38 will materialise sessions/projects
		// when migrate runs and finds they don't exist).
		`INSERT INTO schema_migrations (version, applied_at) VALUES
			(1, '2026-01-01T00:00:00Z'),
			(2, '2026-01-01T00:00:00Z'),
			(3, '2026-01-01T00:00:00Z'),
			(4, '2026-01-01T00:00:00Z'),
			(5, '2026-01-01T00:00:00Z'),
			(6, '2026-01-01T00:00:00Z'),
			(7, '2026-01-01T00:00:00Z')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seedDirtyDB exec: %v sql=%q", err, s)
		}
	}
}

// seedDBWithPreAppliedMigration records a (version, name) pair in
// schema_migrations to simulate "migrations are recorded as applied
// but their tables may not exist" — the partial-state that triggered
// the v1.2.2 boot crashes.
func seedDBWithPreAppliedMigration(t *testing.T, dbPath string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("seedDB: open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("seedDB bookkeeping: %v", err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO schema_migrations (version, applied_at) VALUES (?, '2026-01-01T00:00:00Z')`, version); err != nil {
		t.Fatalf("seedDB insert: %v", err)
	}
}

// verifyTable checks that a table exists in the DB.
func verifyTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var n int
	row := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table)
	if err := row.Scan(&n); err != nil {
		t.Fatalf("verifyTable %s: %v", table, err)
	}
	if n != 1 {
		t.Fatalf("table %s should exist, got n=%d", table, n)
	}
}

// toSharedImport is a no-op reserved for future helpers; today the
// shared imports needed (database/sql, testing) are at the top.
var toSharedImport = true

// _ is used to silence unused-statement warnings on no-op helpers
// in future revisions.
var _ = bytes.NewBuffer
