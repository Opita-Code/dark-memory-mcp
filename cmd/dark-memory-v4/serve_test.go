// Tests for dark-memory-v4 serve. The skeleton doesn't expose
// a JSON-RPC transport yet (BUG-7), so the tests verify:
//   - boot opens the dark-db with INV-16 pragmas
//   - boot applies every v4alpha CreateSchema
//   - boot exits 0 when ctx is cancelled
//   - boot exits 1 when the DB is unopenable
//
// We redirect serve's stdout/stderr to *os.File (matching the
// v3 dark-mem-cli pattern) so the runServe signature stays
// uniform with run/runMigrate/runSchemaStatus. The tests poll
// the file for the "ready" marker, then cancel the ctx to mimic
// SIGINT.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// startServe runs runServe in a goroutine with isolated
// stdout/stderr files. Returns the exit-code channel, a poll
// function for stdout, the stderr path, and a cancel func.
//
// Note: do NOT defer outW.Close() in startServe. The goroutine
// owns the file lifecycle and closes it after runServe returns.
// A premature close in the parent turns every fmt.Fprintf in
// runServe into a "file already closed" write error — and the
// test reads an empty file.
func startServe(t *testing.T, args []string) (code <-chan int, stdoutPath, stderrPath string, cancel func()) {
	t.Helper()
	outF := filepath.Join(t.TempDir(), "out.txt")
	errF := filepath.Join(t.TempDir(), "err.txt")
	outW, err := os.Create(outF)
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	errW, err := os.Create(errF)
	if err != nil {
		t.Fatalf("create stderr: %v", err)
	}

	ctx, cancelFn := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- runServe(ctx, args, outW, errW)
		_ = outW.Close()
		_ = errW.Close()
		close(codeCh)
	}()

	cancel = func() { cancelFn() }
	return codeCh, outF, errF, cancel
}

// pollUntil reads path repeatedly until it contains marker or the
// deadline elapses. Returns the final file contents.
func pollUntil(t *testing.T, path, marker string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			last = string(b)
			if strings.Contains(last, marker) {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// TestServe_BootAndShutdown exercises the full skeleton path.
func TestServe_BootAndShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in -short mode")
	}
	dsn := filepath.Join(t.TempDir(), "serve.db")
	codeCh, outF, errF, cancel := startServe(t, []string{"--dsn=" + dsn})

	out := pollUntil(t, outF, "ready", 5*time.Second)
	if !strings.Contains(out, "ready") {
		cancel()
		t.Fatalf("serve didn't reach ready; stdout=%q", out)
	}

	cancel()
	select {
	case c := <-codeCh:
		if c != exitOK {
			t.Errorf("exit code = %d; want %d", c, exitOK)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}

	// After shutdown, the DB file must exist and the version row
	// must be stamped.
	db, err := store.OpenSQLite(context.Background(), dsn)
	if err != nil {
		t.Fatalf("reopen after shutdown: %v", err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT version FROM schema_migrations LIMIT 1`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("schema_version = %q; want %q", version, schemaVersion)
	}

	// stderr should report shutdown duration.
	errOut, err := os.ReadFile(errF)
	if err != nil {
		t.Fatalf("read stderr file: %v", err)
	}
	if !strings.Contains(string(errOut), "shutdown") {
		t.Errorf("stderr should report shutdown: %q", string(errOut))
	}
}

// TestServe_BadDSN exits 1 when the DSN path cannot be opened.
// (Directory doesn't exist; store.OpenSQLite can't create the
// file if the parent dir is missing.)
func TestServe_BadDSN(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "no-such-dir", "serve.db")
	code, _, errOut := runCaptured(t, "serve", "--dsn="+dsn)
	if code != exitRuntimeErr {
		t.Fatalf("exit %d; want %d (runtime); stderr=%q",
			code, exitRuntimeErr, errOut)
	}
	if !strings.Contains(errOut, "open") {
		t.Errorf("stderr should mention open: %q", errOut)
	}
}

// TestServe_JSONBootReport confirms the --json flag emits a
// JSON object with the expected fields. Polls the stdout file
// for the `"notes"` key.
func TestServe_JSONBootReport(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in -short")
	}
	dsn := filepath.Join(t.TempDir(), "serve-json.db")
	codeCh, outF, _, cancel := startServe(t, []string{"--dsn=" + dsn, "--json"})

	out := pollUntil(t, outF, `"notes"`, 5*time.Second)
	if !strings.Contains(out, `"notes"`) {
		cancel()
		t.Fatalf("serve didn't emit JSON: %q", out)
	}

	cancel()
	<-codeCh

	if !strings.Contains(out, `"schema_version": "`+schemaVersion+`"`) {
		t.Errorf("missing schema_version in JSON: %s", out)
	}
	if !strings.Contains(out, "BUG-6 skeleton") {
		t.Errorf("missing BUG-6 skeleton marker in JSON: %s", out)
	}
	if !strings.Contains(out, `"server_version"`) {
		t.Errorf("missing server_version field: %s", out)
	}
}
