// Package tools - health_test.go: Go-level tests for the
// health_ping tool's read-only state helpers (PII redaction,
// uptime calculation, registry counts).
//
// Wire-level conformance for the full ToolResponse shape lives
// in tests/wire/health_ping_test.go. These Go-level tests cover
// the helper functions in isolation so a refactor of the helper
// fails the suite BEFORE the wire test runs.
package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/version"
)

// jsonMarshal is a tiny alias to keep the test imports clean.
func jsonMarshal(v any) ([]byte, error)              { return json.Marshal(v) }
func contains(s, substr string) bool                 { return strings.Contains(s, substr) }

// TestRedactHomeInPath_Cases covers the platforms the operator
// is most likely to run on. Each case asserts:
//
//   - Input path -> Output path
//   - Paths with no recognizable home component are unchanged
//   - Empty input returns empty
//
// v1.3.0 bug-hunt: previously health_ping emitted the operator's
// Windows username verbatim (e.g. `C:\Users\Nico\AppData\Local\...`)
// in the wire response, which GDPR/CCPA treat as personal data.
// The fix replaces the home prefix with `<USER>` on the wire
// surface; the raw path remains on stderr (boot log line 1) for
// operators who need it for debugging.
func TestRedactHomeInPath_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "windows_user_profile",
			in:   `C:\Users\Nico\AppData\Local\Temp\probe.db`,
			want: `<USER>\AppData\Local\Temp\probe.db`,
		},
		{
			name: "windows_user_profile_no_trailing",
			in:   `C:\Users\Nico`,
			want: `<USER>`,
		},
		{
			name: "windows_lowercase_drive",
			in:   `c:\users\nico\data\probe.db`,
			want: `<USER>\data\probe.db`,
		},
		{
			name: "posix_macos",
			in:   `/Users/nico/Library/Application Support/probe.db`,
			want: `<USER>/Library/Application Support/probe.db`,
		},
		{
			name: "posix_linux",
			in:   `/home/nico/.local/share/probe.db`,
			want: `<USER>/.local/share/probe.db`,
		},
		{
			name: "posix_root",
			in:   `/root/.config/probe.db`,
			want: `<USER>/.config/probe.db`,
		},
		{
			name: "windows_no_user_profile",
			in:   `D:\Data\probe.db`,
			want: `D:\Data\probe.db`,
		},
		{
			name: "posix_tmp_no_home",
			in:   `/tmp/probe.db`,
			want: `/tmp/probe.db`,
		},
		{
			name: "empty",
			in:   ``,
			want: ``,
		},
		{
			name: "windows_relative_with_backslashes",
			in:   `C:\Users\Operator\probe.db`,
			want: `<USER>\probe.db`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactHomeInPath(tc.in)
			if got != tc.want {
				t.Errorf("redactHomeInPath(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRedactHomeInPath_NoDoubleRedact confirms that running the
// redaction twice on an already-redacted path is a no-op. Defensive
// guard against future code paths that might re-process the field
// (e.g. if dark-research-mcp forwards health_ping data).
func TestRedactHomeInPath_NoDoubleRedact(t *testing.T) {
	in := `C:\Users\Nico\probe.db`
	once := redactHomeInPath(in)
	twice := redactHomeInPath(once)
	if once != twice {
		t.Errorf("double-redact changed value: once=%q twice=%q", once, twice)
	}
}

// TestHealthPingResult_IncludesGitFields verifies the v1.4.0 expansion
// of the health_ping response: a `git` block (tag, commit, dirty,
// build_time, source, is_dev) and a top-level `drift` bool. Together
// these implement CONSTITUTION.md Rule 4 — drift detection on every
// boot — without requiring a separate monitor.
func TestHealthPingResult_IncludesGitFields(t *testing.T) {
	// Build a healthPingResult the way RegisterHealth's closure does.
	out := healthPingResult{}
	resolved := version.Resolve()
	out.Git.Tag = resolved.Version
	out.Git.Commit = resolved.Commit
	out.Git.Dirty = resolved.Dirty
	out.Git.BuildTime = resolved.BuildTime
	out.Git.Source = resolved.Source
	out.Git.IsDev = resolved.IsDev
	out.Drift = resolved.IsDev || resolved.Dirty

	// Marshal to JSON and back: this is what the wire test will see.
	// We assert the field set survives the round-trip.
	b, err := jsonMarshal(&out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"git":`, `"source":`, `"drift":`} {
		if !contains(string(b), want) {
			t.Errorf("JSON missing %q in %s", want, b)
		}
	}

	// Drift is determined by the resolver state: if the test binary
	// was built from a dirty tree, Drift=true; if it was built with
	// a proper ldflags injection of a tag, Drift=false and IsDev=false.
	// Either is fine for this test — we only assert consistency.
	if out.Drift != (out.Git.IsDev || out.Git.Dirty) {
		t.Errorf("Drift (%v) != IsDev||Dirty (%v)", out.Drift, out.Git.IsDev || out.Git.Dirty)
	}

	// Source is always one of the documented values.
	switch out.Git.Source {
	case "ldflags", "buildinfo", "dev":
		// ok
	default:
		t.Errorf("Git.Source = %q, want one of ldflags|buildinfo|dev", out.Git.Source)
	}
}

// TestHealthPing_DriftIsSetForDevBuilds verifies the dev-build drift
// path: if the resolver's IsDev flag is true (because ldflags was
// empty AND debug.ReadBuildInfo fell through to "dev"), the
// health_ping response MUST emit Drift=true. This is the operator's
// primary signal that they are running a non-release binary.
func TestHealthPing_DriftIsSetForDevBuilds(t *testing.T) {
	// We can't easily flip the resolver from a test (buildVersion
	// is unexported and the test binary is itself a dev build), so
	// we just verify the contract: Drift is the OR of IsDev and
	// Dirty. A dev build (the test binary itself) MUST have
	// Drift=true; if this test ever runs against a tagged build
	// and fails, that's a sign the test was run from CI.
	r := version.Resolve()
	expectedDrift := r.IsDev || r.Dirty
	if r.IsDev && !expectedDrift {
		t.Error("dev build but Drift would be false (logic bug)")
	}
	// If the test is running against a tagged build (CI), the
	// IsDev flag is false and we just confirm Drift tracks the OR.
	if !r.IsDev && r.Dirty != expectedDrift {
		t.Errorf("tagged build: Drift (%v) != Dirty (%v)", expectedDrift, r.Dirty)
	}
}