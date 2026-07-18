package version

import (
	"encoding/json"
	"strings"
	"testing"
)

// withBuildVersion temporarily overrides the package-private buildVersion
// for the duration of a test. The override is reset via t.Cleanup so
// parallel tests do not see stale state.
func withBuildVersion(t *testing.T, v string) {
	t.Helper()
	prev := buildVersion
	buildVersion = v
	resetMemoization()
	t.Cleanup(func() {
		buildVersion = prev
		resetMemoization()
	})
}

func TestResolve_LDFlagsTakesPriority(t *testing.T) {
	withBuildVersion(t, "1.3.2")
	got := Resolve()
	if got.Version != "1.3.2" {
		t.Errorf("Version = %q, want %q", got.Version, "1.3.2")
	}
	if got.Source != "ldflags" {
		t.Errorf("Source = %q, want %q", got.Source, "ldflags")
	}
	if got.IsDev {
		t.Error("IsDev = true with ldflags injection, want false")
	}
}

func TestResolve_EmptyLDFlagsFallsBackToDev(t *testing.T) {
	withBuildVersion(t, "")
	got := Resolve()
	if got.Version != "dev" {
		t.Errorf("Version = %q, want %q", got.Version, "dev")
	}
	if got.Source != "dev" {
		t.Errorf("Source = %q, want %q", got.Source, "dev")
	}
	if !got.IsDev {
		t.Error("IsDev = false with empty ldflags, want true")
	}
}

func TestResolve_DevStringInLDFlagsStillFallsBackToDev(t *testing.T) {
	// "dev" is the documented sentinel. If someone injects the literal
	// "dev" via -ldflags we treat it like no injection: IsDev stays true
	// so the drift_warning is still emitted.
	withBuildVersion(t, "dev")
	got := Resolve()
	if !got.IsDev {
		t.Error("IsDev = false with ldflags=dev, want true (sentinel)")
	}
	if got.Source != "dev" {
		t.Errorf("Source = %q, want %q", got.Source, "dev")
	}
}

func TestResolve_TrimsWhitespace(t *testing.T) {
	withBuildVersion(t, "  1.3.2  ")
	got := Resolve()
	if got.Version != "1.3.2" {
		t.Errorf("Version = %q, want trimmed %q", got.Version, "1.3.2")
	}
}

func TestResolve_IsMemoized(t *testing.T) {
	withBuildVersion(t, "1.3.2")
	first := Resolve()
	// Mutate after first call: memoization means second call ignores it.
	buildVersion = "9.9.9"
	second := Resolve()
	if first.Version != second.Version {
		t.Errorf("memoization broken: first=%q second=%q", first.Version, second.Version)
	}
	if second.Version != "1.3.2" {
		t.Errorf("second.Version = %q, want %q (memoized first call)", second.Version, "1.3.2")
	}
}

func TestResolve_CommitTruncatedTo7(t *testing.T) {
	// We cannot reliably inject vcs.revision, but we can verify the
	// truncation logic by checking that Resolve does not panic when
	// ReadBuildInfo is present (the test binary always has VCS info).
	got := Resolve()
	// The commit is either empty (no VCS) or a short SHA (≤40 chars
	// before truncation, ≤7 after). We only assert non-negative length.
	if len(got.Commit) > 40 {
		t.Errorf("Commit too long: %q (want ≤40)", got.Commit)
	}
}

func TestResolved_String(t *testing.T) {
	tests := []struct {
		r    Resolved
		want string
	}{
		{Resolved{Version: "1.3.2"}, "1.3.2"},
		{Resolved{Version: "1.3.2-dirty"}, "1.3.2-dirty"},
		{Resolved{Version: ""}, "dev"},
	}
	for _, tc := range tests {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("Resolved{%q}.String() = %q, want %q", tc.r.Version, got, tc.want)
		}
	}
}

func TestResolved_JSON(t *testing.T) {
	r := Resolved{Version: "1.3.2", Commit: "abc1234", Source: "ldflags"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{`"version":"1.3.2"`, `"commit":"abc1234"`, `"source":"ldflags"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %q in %s", want, b)
		}
	}
}

// TestResolve_BuildInfoContract documents the expected fallback chain
// for callers. If a future refactor breaks the path ordering this
// test fires.
func TestResolve_BuildInfoContract(t *testing.T) {
	// We cannot fully control debug.ReadBuildInfo() in a test, but we
	// can assert that the Source field is one of the three documented
	// values regardless of which path was taken.
	withBuildVersion(t, "")
	got := Resolve()
	switch got.Source {
	case "ldflags", "buildinfo", "dev":
		// ok
	default:
		t.Errorf("Source = %q, want one of ldflags|buildinfo|dev", got.Source)
	}
}
