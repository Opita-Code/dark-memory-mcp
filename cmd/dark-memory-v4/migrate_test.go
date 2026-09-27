// Tests for dark-memory-v4 migrate.
package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrate_HumanReadable creates a fresh DB, runs migrate,
// asserts the version is stamped and every step reports ok.
func TestMigrate_HumanReadable(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "migrate-human.db")
	code, out, errOut := runCaptured(t, "migrate", "--dsn="+dsn)
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "ok  audit") {
		t.Errorf("missing audit step: %q", out)
	}
	if !strings.Contains(out, "ok  session") {
		t.Errorf("missing session step: %q", out)
	}
	if !strings.Contains(out, "ok  manifest/cap") {
		t.Errorf("missing manifest/cap step: %q", out)
	}
	if !strings.Contains(out, "ok  manifest/meta") {
		t.Errorf("missing manifest/meta step: %q", out)
	}
	if !strings.Contains(out, "ok  vibe/spec") {
		t.Errorf("missing vibe/spec step: %q", out)
	}
	if !strings.Contains(out, "ok  vibe/artifact") {
		t.Errorf("missing vibe/artifact step: %q", out)
	}
	if !strings.Contains(out, "ok  vibe/drift") {
		t.Errorf("missing vibe/drift step: %q", out)
	}
	if !strings.Contains(out, schemaVersion) {
		t.Errorf("missing schema version banner: %q", out)
	}
}

// TestMigrate_Idempotent runs migrate twice on the same DB.
// The second run MUST NOT fail (every CreateSchema is IF NOT
// EXISTS) and MUST report the same schema version.
func TestMigrate_Idempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "migrate-idem.db")
	for i, arg := range []string{"migrate", "migrate"} {
		_ = i
		code, out, errOut := runCaptured(t, arg, "--dsn="+dsn)
		if code != exitOK {
			t.Fatalf("run %d: exit %d; stderr=%q", i, code, errOut)
		}
		if !strings.Contains(out, schemaVersion) {
			t.Fatalf("run %d: missing schema version in output: %q", i, out)
		}
	}
}

// TestMigrate_JSON emits a single JSON object. This is what
// automation consumes — assert the shape so schema-status can
// consume it.
func TestMigrate_JSON(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "migrate-json.db")
	code, out, errOut := runCaptured(t, "migrate", "--dsn="+dsn, "--json")
	if code != exitOK {
		t.Fatalf("exit %d; stderr=%q", code, errOut)
	}
	var got struct {
		DSN           string   `json:"dsn"`
		SchemaVersion string   `json:"schema_version"`
		Steps         []string `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json parse: %v\noutput: %q", err, out)
	}
	if !strings.HasSuffix(got.DSN, "migrate-json.db") {
		t.Errorf("dsn = %q; want suffix migrate-json.db", got.DSN)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schema_version = %q; want %q", got.SchemaVersion, schemaVersion)
	}
	if len(got.Steps) != 8 {
		t.Errorf("steps count = %d; want 8 (audit, session, agent_memory, manifest/cap, manifest/meta, vibe/spec, vibe/artifact, vibe/drift)", len(got.Steps))
	}
}

// TestMigrate_UnknownFlag exits 2 (usage error) — RFC D-1 §6.
func TestMigrate_UnknownFlag(t *testing.T) {
	code, _, errOut := runCaptured(t, "migrate", "--bogus")
	if code != exitUsageErr {
		t.Fatalf("exit %d; want %d (usage error)", code, exitUsageErr)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Errorf("stderr missing 'unknown flag': %q", errOut)
	}
}

// TestMigrate_BadDSNDir exits 1 (runtime error) when the
// directory in the DSN doesn't exist. This is the "operator
// fat-fingered the path" path.
func TestMigrate_BadDSNDir(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "nope", "does-not-exist.db")
	code, _, errOut := runCaptured(t, "migrate", "--dsn="+dsn)
	if code != exitRuntimeErr {
		t.Fatalf("exit %d; want %d (runtime error); stderr=%q",
			code, exitRuntimeErr, errOut)
	}
	if !strings.Contains(errOut, "open") {
		t.Errorf("stderr should mention open: %q", errOut)
	}
}

// TestMigrate_UnknownFlag exercises the --bogus path through
// parseCommonFlags — actually see TestParseCommonFlags for the
// unit-level coverage; here we check the subcommand wraps with
// "migrate: " prefix.
