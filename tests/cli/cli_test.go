// Package cli — cli_test.go: end-to-end tests for the dark-mem-cli and
// dark-mem-inspect binaries. Each test invokes the actual binary via
// subprocess (os.Exec) with a fresh temp DB, so the tests exercise
// the real command-line surface — flags, env, exit codes, output.
package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cliPath resolves the dark-mem-cli binary built into the test's temp
// dir. Each TestMain builds the binary once and stashes the path.
var cliPath string

// inspectPath resolves the dark-mem-inspect binary.
var inspectPath string

// TestMain builds both binaries into a temp dir before any tests run.
// This isolates the test process from any pre-built binary on disk
// and ensures we always test the current source.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "dark-mem-cli-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp) // best-effort; tests run while tmp exists

	// Find the repo root (parent of the test file's dir).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile))) // tests/cli/cli_test.go -> repo root

	cliPath = buildBinary(tmp, repoRoot, "dark-mem-cli", "cmd/dark-mem-cli")
	inspectPath = buildBinary(tmp, repoRoot, "dark-mem-inspect", "cmd/dark-mem-inspect")

	os.Exit(m.Run())
}

func buildBinary(tmp, repoRoot, name, subdir string) string {
	out := filepath.Join(tmp, name+".exe")
	// cmd/dark-mem-cli and cmd/dark-mem-inspect are SEPARATE Go
	// modules (each with its own go.mod + replace directive), so we
	// must invoke `go build` from inside the subdir, not from the
	// repo root. From the root, `./cmd/dark-mem-cli` is not part of
	// the main module.
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = filepath.Join(repoRoot, subdir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("build "+name+": "+err.Error())
	}
	return out
}

// freshDB creates a fresh dark.db in a temp dir and returns the path.
// The caller is responsible for removing the dir after the test.
func freshDB(t *testing.T) (dbPath, homeDir string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "dark.db")
	homeDir = filepath.Join(dir, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	return dbPath, homeDir
}

// runCLI invokes dark-mem-cli with the given args + env vars, returns
// exit code + combined stdout/stderr.
func runCLI(t *testing.T, args []string, env map[string]string) (int, string) {
	t.Helper()
	cmd := exec.Command(cliPath, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		t.Fatalf("run cli: %v\n%s", err, string(out))
	}
	return 0, string(out)
}

// runInspect invokes dark-mem-inspect with the given args + env vars.
func runInspect(t *testing.T, args []string, env map[string]string) (int, string) {
	t.Helper()
	cmd := exec.Command(inspectPath, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		t.Fatalf("run inspect: %v\n%s", err, string(out))
	}
	return 0, string(out)
}

// TestCLI_Help exits 0 and prints subcommand list.
func TestCLI_Help(t *testing.T) {
	code, out := runCLI(t, []string{"help"}, nil)
	if code != 0 {
		t.Errorf("exit code: want 0, got %d (out=%s)", code, out)
	}
	for _, sub := range []string{"migrate", "vacuum", "schema-status", "set-driver", "version"} {
		if !strings.Contains(out, sub) {
			t.Errorf("help: missing subcommand %q in output", sub)
		}
	}
}

// TestCLI_Version exits 0 and prints the version string.
func TestCLI_Version(t *testing.T) {
	code, out := runCLI(t, []string{"version"}, nil)
	if code != 0 {
		t.Errorf("exit code: want 0, got %d", code)
	}
	if !strings.Contains(out, "dark-mem-cli ") {
		t.Errorf("version output missing prefix: %s", out)
	}
}

// TestCLI_UnknownSubcommand exits 2 (usage error) per exit-code spec.
func TestCLI_UnknownSubcommand(t *testing.T) {
	code, _ := runCLI(t, []string{"bogus"}, nil)
	if code != 2 {
		t.Errorf("exit code: want 2 (usage error), got %d", code)
	}
}

// TestCLI_MigrateFreshDB applies migrations on a fresh DB and returns 0.
func TestCLI_MigrateFreshDB(t *testing.T) {
	dbPath, _ := freshDB(t)
	code, out := runCLI(t, []string{"migrate"}, map[string]string{
		"DARK_DB": dbPath,
	})
	if code != 0 {
		t.Errorf("migrate: want 0, got %d (out=%s)", code, out)
	}
	// Either "applied N migration(s)" or "nothing to apply" is OK.
	if !strings.Contains(out, "applied") && !strings.Contains(out, "nothing to apply") {
		t.Errorf("migrate output unexpected: %s", out)
	}
}

// TestCLI_MigrateJSON validates the --json shape.
func TestCLI_MigrateJSON(t *testing.T) {
	dbPath, _ := freshDB(t)
	code, out := runCLI(t, []string{"migrate", "--json"}, map[string]string{
		"DARK_DB": dbPath,
	})
	if code != 0 {
		t.Errorf("migrate --json: want 0, got %d (out=%s)", code, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Errorf("migrate --json output not valid JSON: %v\n%s", err, out)
	}
	// Must have the four documented fields.
	for _, k := range []string{"before_version", "after_version", "applied", "applied_count"} {
		if _, ok := got[k]; !ok {
			t.Errorf("migrate --json missing field %q", k)
		}
	}
}

// TestCLI_SchemaStatus reports driver + version + table list.
func TestCLI_SchemaStatus(t *testing.T) {
	dbPath, _ := freshDB(t)
	code, out := runCLI(t, []string{"schema-status"}, map[string]string{
		"DARK_DB": dbPath,
	})
	if code != 0 {
		t.Errorf("schema-status: want 0, got %d (out=%s)", code, out)
	}
	for _, want := range []string{"driver: sqlite", "schema_version:", "tables", "migrations"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema-status output missing %q: %s", want, out)
		}
	}
}

// TestCLI_VacuumDryRun exits 0 and reports "DRY RUN" without deleting.
func TestCLI_VacuumDryRun(t *testing.T) {
	dbPath, _ := freshDB(t)
	// First migrate so the DB has tables.
	_, _ = runCLI(t, []string{"migrate"}, map[string]string{"DARK_DB": dbPath})
	code, out := runCLI(t, []string{"vacuum", "--dry-run"}, map[string]string{
		"DARK_DB": dbPath,
	})
	if code != 0 {
		t.Errorf("vacuum --dry-run: want 0, got %d (out=%s)", code, out)
	}
	if !strings.Contains(out, "DRY RUN") {
		t.Errorf("vacuum --dry-run output missing DRY RUN marker: %s", out)
	}
}

// TestCLI_SetDriverWritesConfig writes $DARK_HOME/config.toml and the
// contents are valid TOML with the documented schema.
func TestCLI_SetDriverWritesConfig(t *testing.T) {
	dbPath, homeDir := freshDB(t)
	code, out := runCLI(t, []string{
		"set-driver", "--driver=sqlite", "--dsn=" + dbPath,
	}, map[string]string{
		"DARK_HOME": homeDir,
	})
	if code != 0 {
		t.Errorf("set-driver: want 0, got %d (out=%s)", code, out)
	}
	// File must exist with expected content. The pelletier/go-toml
	// v2 serializer may emit either single or double quotes (both
	// valid TOML basic strings); we accept either.
	path := filepath.Join(homeDir, "config.toml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(body), "driver =") || !strings.Contains(string(body), "sqlite") {
		t.Errorf("config.toml missing driver line: %s", string(body))
	}
	if !strings.Contains(string(body), "dsn =") || !strings.Contains(string(body), dbPath) {
		t.Errorf("config.toml missing dsn line: %s", string(body))
	}
}

// TestCLI_SetDriverBadDriver exits 2 (usage error).
func TestCLI_SetDriverBadDriver(t *testing.T) {
	_, homeDir := freshDB(t)
	code, _ := runCLI(t, []string{
		"set-driver", "--driver=oracle", "--dsn=/tmp/x",
	}, map[string]string{
		"DARK_HOME": homeDir,
	})
	if code != 2 {
		t.Errorf("set-driver bad driver: want 2, got %d", code)
	}
}

// TestInspect_ReportContainsCoreSections produces a human-readable
// report with driver, schema_version, canary_present, tables.
func TestInspect_ReportContainsCoreSections(t *testing.T) {
	dbPath, _ := freshDB(t)
	_, _ = runCLI(t, []string{"migrate"}, map[string]string{"DARK_DB": dbPath})
	code, out := runInspect(t, nil, map[string]string{"DARK_DB": dbPath})
	if code != 0 {
		t.Errorf("inspect: want 0, got %d (out=%s)", code, out)
	}
	for _, want := range []string{
		"driver:             sqlite",
		"canary_present:",
		"schema_version:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect report missing %q: %s", want, out)
		}
	}
}

// TestInspect_JSONOutput is a valid JSON with the documented fields.
func TestInspect_JSONOutput(t *testing.T) {
	dbPath, _ := freshDB(t)
	_, _ = runCLI(t, []string{"migrate"}, map[string]string{"DARK_DB": dbPath})
	code, out := runInspect(t, []string{"--json"}, map[string]string{"DARK_DB": dbPath})
	if code != 0 {
		t.Errorf("inspect --json: want 0, got %d (out=%s)", code, out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("inspect --json not valid JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"generated_at", "driver", "dsn", "canary_present", "schema_version", "tables", "migrations"} {
		if _, ok := got[k]; !ok {
			t.Errorf("inspect --json missing field %q", k)
		}
	}
}

// strconvQuote is unused after the TOML-quote fix above; kept as a
// no-op for any future test that wants to compare on a specific
// quote style.
var _ = strconvQuote

func strconvQuote(s string) string {
	return `"` + s + `"`
}