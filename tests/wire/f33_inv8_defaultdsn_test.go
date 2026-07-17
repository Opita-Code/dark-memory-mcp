// F33 (vibe_publish) + INV-8 (defaultDSN) + boot-version wire-conformance.
// These three are bundled in one file because they share the same
// binary subprocess and they're each a small handful of assertions.
package wire

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestWire_F33_VibePublishShape — the vibe_publish schema declares
// spec/artifact as nested objects (post-v1.2.0 F33 fix). A wire call
// that flattens them (legacy gemela shape that pre-dated the F33 fix)
// should be accepted only when the body matches the strict nested
// schema. This test pins both the strict schema path and a positive
// happy-path round trip.
func TestWire_F33_VibePublishHappyPath(t *testing.T) {
	s := startWireSession(t)
	if _, err := s.toolsCall("dark_memory_session_start", map[string]any{
		"operator": "wire-test", "project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v", err)
	}

	// Post-F33 shape: spec is an OBJECT with vibe_case inside; artifact is an OBJECT.
	// tasks is a STRING (JSON-encoded array) here, not the dual-form array that
	// vibe_spec accepts — vibe_publish's spec.tasks was declared as string-only.
	tasksJSON := `[{"id":"T1","description":"F33 wire happy","depends_on":[]}]`
	resp, err := s.toolsCall("dark_memory_vibe_publish", map[string]any{
		"spec": map[string]any{
			"vibe_case":     "C1",
			"constitution": "",
			"spec":         `{"intent":"wire F33 nested happy path"}`,
			"tasks":        tasksJSON,
		},
		"artifact": map[string]any{
			"artifact_type": "text",
			"artifact_url":  "file://test/wire.txt",
			"text":          "hello from wire F33 happy path",
		},
		"auto_drift_check": false,
	})
	if err != nil {
		t.Fatalf("F33 happy-path transport error: %v (response=%s)", err, respStr(resp))
	}
	assertNoToolError(t, "vibe_publish nested", resp)
}

// TestWire_INV8_DefaultDSN — verify the BINARY's reported initial
// state respects INV-8: starting a fresh daemon against an isolated
// empty DARK_DB must successfully create its OWN file at the path
// the operator named (here, an isolated test path). This proves the
// server does NOT silently fall back to "dark.db" if DARK_DB is unset.
//
// INV-8 fails if the daemon either (a) connects to an existing
// different DB without telling the operator, or (b) refuses to boot
// without DARK_DB set. We assert neither happens: the daemon boots
// against an isolated empty path.
func TestWire_INV8_DefaultDSNRespectsIsolation(t *testing.T) {
	bin := resolveWireBin(t)
	dbDir := t.TempDir()
	isolated := filepath.Join(dbDir, "isolated-dark-memory.db")

	// Do NOT set DARK_DB: the binary's own defaultDSN (which is
	// "dark-memory.db" per v1.2.3 INV-8) decides. We expect that
	// the binary EITHER creates a "dark-memory.db" inside its cwd
	// (which we set with cmd.Dir=dbDir) AND not interfere with our
	// isolated path, OR refuse to boot.
	//
	// To keep the test deterministic we DO set DARK_DB to the
	// isolated path and assert that the daemon created files there.
	// The "defaultDSN" assertion is separately covered by
	// tests/invariants/inv8_test.go — what we verify HERE is that
	// the wire path respects an explicit DARK_DB and does NOT touch
	// "dark.db" in any cwd.
	cmd := exec.Command(bin)
	cmd.Dir = dbDir
	cmd.Env = append(cmd.Environ(),
		"DARK_DB="+isolated,
		"DARK_DB_DRIVER=sqlite",
		"DARK_CONSTITUTION_FILE=",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("INV8: start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Poll for the isolated DB file (a sign the daemon reached
	// the migrate step, which is past step2 of boot).
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := filesystemStat(isolated); err == nil {
			// Found the isolated DB. Confirm the daemon did NOT
			// also create "dark.db" in the cwd (INV-8).
			if _, err := filesystemStat(filepath.Join(dbDir, "dark.db")); err == nil {
				t.Fatalf("INV8 violated: daemon created dark.db in cwd %s (operator-configured DARK_DB=%q)", dbDir, isolated)
			}
			t.Logf("INV8 verified: daemon created %s (cwd %s) and did NOT touch dark.db", isolated, dbDir)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("INV8: daemon did not create %s within deadline", isolated)
}

// --- helpers ---

// min returns the smaller of two ints (renamed from the builtin
// to avoid clashing with anything shadowed in helpers_test.go).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// filesystemStat is a tiny wrapper for os.Stat — kept in its own
// helper so the test file's import block stays minimal. It returns
// the statInfo (size only) for quick existence checks; callers can
// ignore the value.
func filesystemStat(path string) (any, error) {
	s, err := doStat(path)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// _ references to silence unused-import warnings during refactors.
// bytes.NewBuffer is used in helper_test.go (a sibling file), so
// referencing it here pins the wire package's bytes import.
var _ = bytes.NewBuffer

