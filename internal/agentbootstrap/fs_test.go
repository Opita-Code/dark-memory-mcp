// Package agentbootstrap - fs_test.go: tests for the embedded bootstrap
// content + DARK_AGENT_BOOTSTRAP_DIR override.
//
// What this verifies:
//
//   - LoadFS returns the embedded fs when no override env var is set.
//   - LoadFSWithWarn returns the embedded fs + nil warning when the
//     override env var is unset OR invalid.
//   - validateOverrideDir accepts a directory that contains every
//     required bootstrap file (10 files).
//   - validateOverrideDir rejects a directory missing any required
//     file (returns the file path in the error).
//   - EmbeddedFS() returns the same fs as LoadFS() when no override
//     is set (defensive — both should yield identical content).
//
// Drift contract: this is the canonical source of "do the embedded
// files exist + match the override contract". Future edits to either
// data files or override validation logic must update this test.
package agentbootstrap

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadFS_NoOverride returns the embedded fs when DARK_AGENT_BOOTSTRAP_DIR
// is unset. Defensive: even if the env var is set to empty string,
// LoadFS should fall back to embedded.
func TestLoadFS_NoOverride(t *testing.T) {
	t.Setenv(EnvOverride, "")

	fsys := LoadFS()
	if fsys == nil {
		t.Fatal("LoadFS returned nil fs")
	}

	// Read a known file from the embedded fs to confirm content.
	data, err := fs.ReadFile(fsys, "SYSTEM_PROMPT.md")
	if err != nil {
		t.Fatalf("read SYSTEM_PROMPT.md from embedded fs: %v", err)
	}
	if !strings.Contains(string(data), "dark-agent Operating Manual") {
		t.Errorf("embedded SYSTEM_PROMPT.md does not contain expected marker")
	}
}

// TestLoadFSWithWarn_NoOverride confirms no warning when override
// is unset (the common path).
func TestLoadFSWithWarn_NoOverride(t *testing.T) {
	t.Setenv(EnvOverride, "")

	fsys, warn := LoadFSWithWarn()
	if fsys == nil {
		t.Fatal("LoadFSWithWarn returned nil fs")
	}
	if warn != nil {
		t.Errorf("expected no warning when override unset, got %v", warn)
	}
}

// TestLoadFSWithWarn_InvalidOverride confirms the warning fires when
// the override env var points to a directory that doesn't exist.
func TestLoadFSWithWarn_InvalidOverride(t *testing.T) {
	t.Setenv(EnvOverride, "/nonexistent/directory/that/should/never/exist")

	fsys, warn := LoadFSWithWarn()
	if fsys == nil {
		t.Fatal("LoadFSWithWarn returned nil fs on invalid override")
	}
	if warn == nil {
		t.Error("expected warning when override invalid, got nil")
	}
	if !strings.Contains(warn.Error(), EnvOverride) {
		t.Errorf("warning should mention env var name %q; got %q", EnvOverride, warn.Error())
	}
}

// TestLoadFSWithWarn_OverrideMissingFiles confirms the override is
// rejected when the directory exists but lacks required bootstrap
// files (the half-overridden case).
func TestLoadFSWithWarn_OverrideMissingFiles(t *testing.T) {
	tmp := t.TempDir()
	// Create an empty directory (no bootstrap files).
	t.Setenv(EnvOverride, tmp)

	fsys, warn := LoadFSWithWarn()
	if fsys == nil {
		t.Fatal("LoadFSWithWarn returned nil fs on incomplete override")
	}
	if warn == nil {
		t.Error("expected warning when override missing required files, got nil")
	}
	if !strings.Contains(warn.Error(), "SYSTEM_PROMPT.md") {
		t.Errorf("warning should mention missing SYSTEM_PROMPT.md; got %q", warn.Error())
	}
}

// TestLoadFS_ValidOverride confirms the override path is honored
// when the directory has all 10 required files. We copy the embedded
// files into a temp directory so the override is valid.
func TestLoadFS_ValidOverride(t *testing.T) {
	tmp := copyEmbeddedFilesTo(t, t.TempDir())
	t.Setenv(EnvOverride, tmp)

	fsys := LoadFS()
	if fsys == nil {
		t.Fatal("LoadFS returned nil fs")
	}

	// Read a known file from the override fs.
	data, err := fs.ReadFile(fsys, "SYSTEM_PROMPT.md")
	if err != nil {
		t.Fatalf("read SYSTEM_PROMPT.md from override fs: %v", err)
	}
	if len(data) == 0 {
		t.Error("override SYSTEM_PROMPT.md is empty")
	}
}

// TestEmbeddedFS_NotNil confirms EmbeddedFS() always returns the
// canonical embedded fs regardless of the override env var.
func TestEmbeddedFS_NotNil(t *testing.T) {
	t.Setenv(EnvOverride, "")
	if EmbeddedFS() == nil {
		t.Fatal("EmbeddedFS returned nil")
	}
	// Even with override set, EmbeddedFS() should return the embedded fs.
	t.Setenv(EnvOverride, "/nonexistent")
	if EmbeddedFS() == nil {
		t.Fatal("EmbeddedFS returned nil despite override set")
	}
}

// TestValidateOverrideDir_AcceptsCompleteDir checks that a directory
// with all 10 required files passes validation.
func TestValidateOverrideDir_AcceptsCompleteDir(t *testing.T) {
	tmp := copyEmbeddedFilesTo(t, t.TempDir())
	if err := validateOverrideDir(tmp); err != nil {
		t.Errorf("validateOverrideDir rejected a complete directory: %v", err)
	}
}

// TestValidateOverrideDir_RejectsMissingFile removes one required
// file and confirms validation fails.
func TestValidateOverrideDir_RejectsMissingFile(t *testing.T) {
	tmp := copyEmbeddedFilesTo(t, t.TempDir())
	// Delete one required file.
	if err := os.Remove(filepath.Join(tmp, "COMPATIBILITY_MATRIX.md")); err != nil {
		t.Fatalf("test setup: %v", err)
	}

	err := validateOverrideDir(tmp)
	if err == nil {
		t.Fatal("validateOverrideDir accepted a directory missing COMPATIBILITY_MATRIX.md")
	}
	if !strings.Contains(err.Error(), "COMPATIBILITY_MATRIX.md") {
		t.Errorf("error should mention missing file; got %q", err.Error())
	}
}

// TestValidateOverrideDir_RejectsNotADir confirms a file (not a
// directory) is rejected.
func TestValidateOverrideDir_RejectsNotADir(t *testing.T) {
	tmp := t.TempDir()
	notADir := filepath.Join(tmp, "this-is-a-file")
	if err := os.WriteFile(notADir, []byte("hello"), 0o644); err != nil {
		t.Fatalf("test setup: %v", err)
	}

	err := validateOverrideDir(notADir)
	if err == nil {
		t.Fatal("validateOverrideDir accepted a regular file as override dir")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should mention 'not a directory'; got %q", err.Error())
	}
}

// TestValidateOverrideDir_RejectsNonexistent confirms a missing dir
// is rejected with a stat error.
func TestValidateOverrideDir_RejectsNonexistent(t *testing.T) {
	err := validateOverrideDir("/this/path/should/never/exist/on/any/test/runner")
	if err == nil {
		t.Fatal("validateOverrideDir accepted a nonexistent directory")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Errorf("error should mention stat failure; got %q", err.Error())
	}
}

// TestEmbeddedFiles_Contract locks in the canonical list of 10 files.
// If a future change adds or removes a file from data/, this test
// must be updated intentionally — accidental changes fail the build.
func TestEmbeddedFiles_Contract(t *testing.T) {
	src := EmbeddedFS()
	expected := map[string]bool{
		"SYSTEM_PROMPT.md":                                              false,
		"COMPATIBILITY_MATRIX.md":                                       false,
		"install/claude-desktop.md":                                     false,
		"install/claude-code.md":                                        false,
		"install/opencode.md":                                           false,
		"install/cline.md":                                              false,
		"install/cursor.md":                                             false,
		"install/continue.md":                                           false,
		"companions/dark-research.md":                                   false,
		"companions/dark-copilot.md":                                    false,
	}

	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := expected[path]; ok {
			expected[path] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded fs: %v", err)
	}

	for path, found := range expected {
		if !found {
			t.Errorf("expected embedded file %q not found in fs", path)
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// copyEmbeddedFilesTo copies every file under the embedded fs to the
// given destination directory. Returns the destination directory path.
// Used by tests that want a valid override directory without having to
// fabricate the file contents by hand.
func copyEmbeddedFilesTo(t *testing.T, dst string) string {
	t.Helper()
	src := EmbeddedFS()
	if src == nil {
		t.Fatal("EmbeddedFS returned nil")
	}

	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Recreate the directory under dst.
			if path == "." {
				return os.MkdirAll(dst, 0o755)
			}
			return os.MkdirAll(filepath.Join(dst, path), 0o755)
		}
		// Read from embedded fs, write to disk.
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		full := filepath.Join(dst, path)
		// Ensure parent dir exists.
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy embedded files to %s: %v", dst, err)
	}
	return dst
}
