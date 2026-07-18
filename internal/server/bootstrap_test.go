// Package server - bootstrap_test.go: Go-level tests for the
// pre-flight validation in LoadConfig. The pre-flight catches
// bad DARK_DB paths at step1 (before any sqlite driver code
// runs) so operators see a clear error message instead of a
// driver-level stack trace.
//
// v1.3.0 (bug-hunt polish): the previous code crashed deep
// inside modernc/sqlite with "unable to open database file"
// when the operator's DARK_DB pointed to a nonexistent dir.
// preflightSQLiteDSN closes that gap.
package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightSQLiteDSN_MissingDir(t *testing.T) {
	// DSN whose parent directory does not exist.
	tmp := t.TempDir()
	dsn := filepath.Join(tmp, "no-such-subdir", "dark.db")
	err := preflightSQLiteDSN(dsn)
	if err == nil {
		t.Fatalf("expected error for nonexistent dir; got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should mention 'does not exist'; got %q", err.Error())
	}
}

func TestPreflightSQLiteDSN_FileNotDir(t *testing.T) {
	// DSN whose parent is a regular file, not a directory.
	tmp := t.TempDir()
	notADir := filepath.Join(tmp, "this-is-a-file")
	if err := os.WriteFile(notADir, []byte("hello"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	dsn := filepath.Join(notADir, "dark.db")
	err := preflightSQLiteDSN(dsn)
	if err == nil {
		t.Fatalf("expected error for file-as-dir; got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should mention 'not a directory'; got %q", err.Error())
	}
}

func TestPreflightSQLiteDSN_BareFilename(t *testing.T) {
	// Bare filename (no directory component). filepath.Dir
	// returns "." which always exists; preflight should pass
	// and defer the actual file creation to sqlite.Open.
	dsn := "dark.db"
	if err := preflightSQLiteDSN(dsn); err != nil {
		t.Errorf("bare filename should pass preflight; got %v", err)
	}
}

func TestPreflightSQLiteDSN_WritableDir(t *testing.T) {
	// Existing writable directory should pass.
	tmp := t.TempDir()
	dsn := filepath.Join(tmp, "dark.db")
	if err := preflightSQLiteDSN(dsn); err != nil {
		t.Errorf("writable dir should pass preflight; got %v", err)
	}
}

func TestPreflightSQLiteDSN_ReadOnlyDir(t *testing.T) {
	// POSIX-only: a read-only directory should fail preflight.
	// Skipped on Windows because the temp dir permission model
	// doesn't map cleanly to POSIX rwx.
	if os.Getenv("GOOS") == "windows" || filepath.Separator == '\\' {
		t.Skip("read-only test is POSIX-only")
	}
	tmp := t.TempDir()
	ro := filepath.Join(tmp, "ro")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Fatalf("setup mkdir ro: %v", err)
	}
	dsn := filepath.Join(ro, "dark.db")
	err := preflightSQLiteDSN(dsn)
	if err == nil {
		t.Fatalf("expected error for read-only dir; got nil")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Errorf("error should mention 'not writable'; got %q", err.Error())
	}
}

func TestLoadConfig_RejectsBadDSN(t *testing.T) {
	// End-to-end: LoadConfig catches a nonexistent dir.
	t.Setenv("DARK_DB_DRIVER", "sqlite")
	t.Setenv("DARK_DB", filepath.Join(t.TempDir(), "no-such-subdir", "dark.db"))
	_, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig should reject bad DSN")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Errorf("error should mention 'preflight'; got %q", err.Error())
	}
}

func TestLoadConfig_AcceptsGoodDSN(t *testing.T) {
	// End-to-end: LoadConfig accepts a valid DSN.
	dir := t.TempDir()
	t.Setenv("DARK_DB_DRIVER", "sqlite")
	t.Setenv("DARK_DB", filepath.Join(dir, "dark.db"))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig should accept valid DSN; got %v", err)
	}
	if cfg.DBDSN == "" {
		t.Errorf("DBDSN empty after LoadConfig")
	}
}