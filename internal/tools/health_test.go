// Package tools - health_test.go: Go-level tests for the
// health_ping tool's read-only state helpers (PII redaction,
// uptime calculation, registry counts).
//
// Wire-level conformance for the full ToolResponse shape lives
// in tests/wire/health_ping_test.go. These Go-level tests cover
// the helper functions in isolation so a refactor of the helper
// fails the suite BEFORE the wire test runs.
package tools

import "testing"

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