// Package server - bootstrap_constitution_test.go: regression tests
// for the v2.1.3 fix that auto-loads the canonical dark-mem constitution
// at boot when DARK_CONSTITUTION_FILE is unset. The bug surfaced as
// "identity unavailable for this session" because the watchdog short-
// circuited (ConstitutionFile="" → no INSERT into constitutions table)
// and StoreSource.IdentityFrame refused to compose a frame without
// a non-empty ConstitutionID.
//
// These tests pin the auto-load contract:
//   - env var wins (operator override always takes precedence)
//   - exe-dir / cwd search paths cover the typical layouts
//   - malformed TOML or empty [meta] fields degrade gracefully
//     (no crash, no partial cfg mutation, log + skip)
//   - duplicate search paths are deduplicated so the auto-loader
//     doesn't stat the same file twice
package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConstitution = `[meta]
id = "test/constitution"
version = "9.9.9"
label = "test only"
`

const sampleConstitutionEmptyMeta = `[meta]
id = ""
version = ""
`

const sampleConstitutionMissingMeta = `[authority]
body = "no meta section"
`

const malformedConstitution = `[meta
not valid TOML at all
`

// writeConstitutionFile creates the file at <root>/vibe-flow/constitution/dark-mem.constitution.toml
// with the given body. Returns the absolute path of the written file.
func writeConstitutionFile(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, "vibe-flow", "constitution")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	p := filepath.Join(dir, builtinConstitutionName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	return p
}

// TestResolveBuiltinConstitution_AppliesFromExeRelative verifies the
// primary search path: <exe-dir>/../vibe-flow/constitution/. We mimic
// this by chdir'ing to a temp dir and laying out the constitution
// under ./vibe-flow/constitution/ — cwd is the second search path so
// the same coverage applies. The test asserts cfg gets populated
// from the TOML's [meta] section.
func TestResolveBuiltinConstitution_AppliesFromCwd(t *testing.T) {
	tmp := t.TempDir()
	writeConstitutionFile(t, tmp, sampleConstitution)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg := &Config{}
	if applied := resolveBuiltinConstitution(cfg); !applied {
		t.Fatalf("expected auto-load to apply; got false")
	}

	want := filepath.Join(tmp, "vibe-flow", "constitution", builtinConstitutionName)
	if cfg.ConstitutionFile != want {
		t.Errorf("ConstitutionFile = %q, want %q", cfg.ConstitutionFile, want)
	}
	if cfg.ConstitutionID != "test/constitution" {
		t.Errorf("ConstitutionID = %q, want %q", cfg.ConstitutionID, "test/constitution")
	}
	if cfg.ConstitutionVer != "9.9.9" {
		t.Errorf("ConstitutionVer = %q, want %q", cfg.ConstitutionVer, "9.9.9")
	}
}

// TestResolveBuiltinConstitution_EnvVarWins verifies the precedence
// rule: an operator-set DARK_CONSTITUTION_FILE wins even when a
// canonical builtin TOML is present in the search paths.
func TestResolveBuiltinConstitution_EnvVarWins(t *testing.T) {
	tmp := t.TempDir()
	writeConstitutionFile(t, tmp, sampleConstitution)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	explicitPath := filepath.Join(tmp, "explicit-constitution.toml")
	if err := os.WriteFile(explicitPath, []byte(sampleConstitution), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg := &Config{ConstitutionFile: explicitPath}
	if applied := resolveBuiltinConstitution(cfg); applied {
		t.Fatalf("auto-load must not fire when env var is set")
	}
	if cfg.ConstitutionFile != explicitPath {
		t.Errorf("ConstitutionFile mutated: got %q, want %q", cfg.ConstitutionFile, explicitPath)
	}
	if cfg.ConstitutionID != "" {
		t.Errorf("ConstitutionID should remain empty; got %q", cfg.ConstitutionID)
	}
}

// TestResolveBuiltinConstitution_NoBuiltin_EmbeddedFallback verifies
// the embedded fallback: when no filesystem constitution is found in
// any search path, the build-time-embedded TOML is materialised and
// applied. The cfg.ConstitutionFile should point at the materialised
// temp file path; the ID/Ver come from the embedded [meta].
//
// This replaced the original TestResolveBuiltinConstitution_NoBuiltin
// after the embedded fallback was added (the old assertion "auto-load
// must not fire when no builtin exists" became wrong by design — the
// embedded fallback ALWAYS fires, even on an empty filesystem).
func TestResolveBuiltinConstitution_NoBuiltin_EmbeddedFallback(t *testing.T) {
	tmp := t.TempDir() // empty — no constitution dir at all

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg := &Config{}
	if !resolveBuiltinConstitution(cfg) {
		t.Fatalf("auto-load must fire via embedded fallback when filesystem has no match")
	}
	if cfg.ConstitutionFile == "" {
		t.Errorf("ConstitutionFile empty after embedded fallback")
	}
	if cfg.ConstitutionID != "dark-agents/dark-mem" {
		t.Errorf("ConstitutionID = %q, want dark-agents/dark-mem", cfg.ConstitutionID)
	}
	if cfg.ConstitutionVer != "1.0.0" {
		t.Errorf("ConstitutionVer = %q, want 1.0.0", cfg.ConstitutionVer)
	}
	// The materialised file should exist (we just wrote it).
	if _, err := os.Stat(cfg.ConstitutionFile); err != nil {
		t.Errorf("materialised file not found at %s: %v", cfg.ConstitutionFile, err)
	}
	// Cleanup the temp file the test produced.
	t.Cleanup(func() { _ = os.Remove(cfg.ConstitutionFile) })
}

// TestResolveBuiltinConstitution_BadTOML verifies graceful degradation:
// a malformed TOML is logged + skipped, not a panic, not a partial
// write. This is the "operator copied a corrupt file" path — boot
// should still complete (sans constitution) so other tools work.
func TestResolveBuiltinConstitution_BadTOML(t *testing.T) {
	tmp := t.TempDir()
	writeConstitutionFile(t, tmp, malformedConstitution)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg := &Config{}
	if applied := resolveBuiltinConstitution(cfg); applied {
		t.Fatalf("auto-load must not apply a malformed TOML")
	}
	if cfg.ConstitutionFile != "" || cfg.ConstitutionID != "" || cfg.ConstitutionVer != "" {
		t.Errorf("cfg mutated despite malformed TOML: %+v", cfg)
	}
}

// TestResolveBuiltinConstitution_EmptyMeta verifies the empty-[meta]
// path: a TOML that parses cleanly but has empty id/version is
// treated as a malformed file (the watchdog needs non-empty id and
// version to write the INSERT).
func TestResolveBuiltinConstitution_EmptyMeta(t *testing.T) {
	tmp := t.TempDir()
	writeConstitutionFile(t, tmp, sampleConstitutionEmptyMeta)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg := &Config{}
	if applied := resolveBuiltinConstitution(cfg); applied {
		t.Fatalf("auto-load must not apply a TOML with empty [meta] fields")
	}
	if cfg.ConstitutionFile != "" {
		t.Errorf("cfg mutated despite empty meta: %+v", cfg)
	}
}

// TestResolveBuiltinConstitution_MissingMeta verifies a TOML without
// a [meta] section at all: same as empty meta — skip.
func TestResolveBuiltinConstitution_MissingMeta(t *testing.T) {
	tmp := t.TempDir()
	writeConstitutionFile(t, tmp, sampleConstitutionMissingMeta)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg := &Config{}
	if applied := resolveBuiltinConstitution(cfg); applied {
		t.Fatalf("auto-load must not apply a TOML without [meta]")
	}
}

// TestBuiltinConstitutionSearchPaths_Deduplicates verifies that the
// search-path builder doesn't return the same canonical path twice
// when exe-dir == cwd (e.g. when running `go test` from the repo
// root with a build that uses the source tree).
func TestBuiltinConstitutionSearchPaths_Deduplicates(t *testing.T) {
	// Make cwd != exe-dir so the canonical paths would otherwise
	// differ; the test asserts that within a single root they are
	// unique. Cross-root dedup is harder to verify in a unit test
	// without stubbing os.Executable; the bare call returns a valid
	// list and dedup-by-map handles equality correctly.
	paths := builtinConstitutionSearchPaths()
	if len(paths) == 0 {
		t.Fatalf("search paths must not be empty")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate search path: %q", p)
		}
		seen[p] = true
	}
}

// TestFindBuiltinConstitutionFile_FirstMatchWins verifies the helper
// returns the first existing candidate across an arbitrary search
// path list, in the order supplied.
func TestFindBuiltinConstitutionFile_FirstMatchWins(t *testing.T) {
	tmp := t.TempDir()
	// Create two candidates; first search path wins.
	first := writeConstitutionFile(t, filepath.Join(tmp, "first"), sampleConstitution)
	writeConstitutionFile(t, filepath.Join(tmp, "second"), sampleConstitution)

	paths := []string{
		filepath.Join(tmp, "first", "vibe-flow", "constitution"),
		filepath.Join(tmp, "second", "vibe-flow", "constitution"),
	}
	got := findBuiltinConstitutionFile(paths)
	if got != first {
		t.Errorf("first match wins: got %q, want %q", got, first)
	}
}

func TestFindBuiltinConstitutionFile_NoneFound(t *testing.T) {
	tmp := t.TempDir() // empty
	got := findBuiltinConstitutionFile([]string{
		filepath.Join(tmp, "a", "vibe-flow", "constitution"),
		filepath.Join(tmp, "b", "vibe-flow", "constitution"),
	})
	if got != "" {
		t.Errorf("expected empty for no matches; got %q", got)
	}
}

// TestParseBuiltinConstitutionMeta_HappyPath sanity-checks the TOML
// projection: [meta] id/version/label flow into the struct fields.
func TestParseBuiltinConstitutionMeta_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "c.toml")
	if err := os.WriteFile(p, []byte(sampleConstitution), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	c, err := parseBuiltinConstitutionMeta(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.ID != "test/constitution" || c.Version != "9.9.9" || c.Label != "test only" {
		t.Errorf("parsed fields wrong: %+v", c)
	}
	if c.FilePath != p {
		t.Errorf("FilePath = %q, want %q", c.FilePath, p)
	}
}

// TestParseBuiltinConstitutionMeta_MissingFile verifies a missing
// file yields a non-nil error (not a panic, not empty struct).
func TestParseBuiltinConstitutionMeta_MissingFile(t *testing.T) {
	_, err := parseBuiltinConstitutionMeta(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error should mention 'read'; got %q", err.Error())
	}
}
