// Tests for dark-memory-v4 schema-status. Confirms the
// post-migrate report includes the INV-16 pragmas and the
// v4alpha tables.
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// TestSchemaStatus_HumanReadable migrates a fresh DB, then runs
// schema-status and confirms the INV-16 pragmas appear in the
// report.
func TestSchemaStatus_HumanReadable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ss-human.db")
	// Migrate first.
	code, _, errOut := runCaptured(t, "migrate", "--dsn="+dsn)
	if code != exitOK {
		t.Fatalf("migrate exit %d: %q", code, errOut)
	}
	// Now schema-status.
	code, out, errOut := runCaptured(t, "schema-status", "--dsn="+dsn)
	if code != exitOK {
		t.Fatalf("schema-status exit %d: %q", code, errOut)
	}
	if !strings.Contains(out, schemaVersion) {
		t.Errorf("missing schema_version in output: %q", out)
	}
	if !strings.Contains(out, "busy_timeout") {
		t.Errorf("missing busy_timeout pragma in output: %q", out)
	}
	if !strings.Contains(out, "journal_mode") {
		t.Errorf("missing journal_mode pragma in output: %q", out)
	}
	// Every v4alpha table must be present. Names verified by
	// reading the v4alpha package schemas (audit_log, capabilities,
	// manifest, sessions, vibe_artifacts, vibe_drifts, vibe_specs).
	for _, want := range []string{"audit_log", "capabilities", "manifest", "sessions", "vibe_artifacts", "vibe_drifts", "vibe_specs"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing table %q in output: %q", want, out)
		}
	}
}

// TestSchemaStatus_JSON verifies the JSON output is parseable and
// confirms the INV-16 pragmas have the right values (5000, wal,
// normal).
func TestSchemaStatus_JSON(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ss-json.db")
	if code, _, errOut := runCaptured(t, "migrate", "--dsn="+dsn); code != exitOK {
		t.Fatalf("migrate exit %d: %q", code, errOut)
	}
	code, out, errOut := runCaptured(t, "schema-status", "--dsn="+dsn, "--json")
	if code != exitOK {
		t.Fatalf("schema-status exit %d: %q", code, errOut)
	}
	var got schemaStatus
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json parse: %v\noutput: %q", err, out)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schema_version = %q; want %q", got.SchemaVersion, schemaVersion)
	}
	if got.Pragmas["busy_timeout"] != "5000" {
		t.Errorf("busy_timeout pragma = %q; want 5000 (INV-16)", got.Pragmas["busy_timeout"])
	}
	if got.Pragmas["journal_mode"] != "wal" {
		t.Errorf("journal_mode pragma = %q; want wal (INV-16)", got.Pragmas["journal_mode"])
	}
	if got.Pragmas["synchronous"] != "1" {
		t.Errorf("synchronous pragma = %q; want 1 (NORMAL, INV-16)", got.Pragmas["synchronous"])
	}
	if len(got.Tables) < 7 {
		t.Errorf("table count = %d; want >= 7", len(got.Tables))
	}
}

// TestSchemaStatus_UnmigratedDB exits 0 but reports
// "(not migrated)" for schema_version. The operator's eye-catcher
// before they forget to run migrate.
func TestSchemaStatus_UnmigratedDB(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ss-unmigrated.db")
	// Open the DB (creates an empty file) without running migrate.
	db, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = db.Close()

	code, out, errOut := runCaptured(t, "schema-status", "--dsn="+dsn)
	if code != exitOK {
		t.Fatalf("exit %d: %q", code, errOut)
	}
	if !strings.Contains(out, "(not migrated)") {
		t.Errorf("expected '(not migrated)' marker: %q", out)
	}
}
