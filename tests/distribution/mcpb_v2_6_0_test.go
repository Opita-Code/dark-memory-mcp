// Package distribution: v2.6.0 reproducible-builds fix.
//
// Background: v2.5.2 binaries published via publish-npm.yml and
// build-mcpb.yml produced different SHA256 hashes because:
//
//  1. The two workflows ran on different GitHub-hosted runners with
//     potentially different Go patch versions resolved from the
//     actions/setup-go@v5 `go-version: "1.25"` spec (which takes the
//     latest 1.25.x.y at the time of install). Different Go patches
//     produce different binaries even with identical source + ldflags.
//  2. Neither workflow set SOURCE_DATE_EPOCH, so each build embedded
//     the current wall clock time into the PE/Mach-O/ELF header.
//
// The fix pins Go to an exact patch (1.25.12) and pins
// SOURCE_DATE_EPOCH to the commit timestamp. The structural tests
// below ensure both workflows keep this fix in place going forward
// (any regression that removes the pin will fail CI before reaching
// the network-dependent SHA comparison in mcpb_v2_5_2_test.go).
package distribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestV260_ReproducibleBuildsSetup_NPMWorkflow verifies that
// publish-npm.yml has the v2.6.0 reproducible-builds fix:
//
//  1. Go version is pinned to an exact patch (1.25.x.y format).
//  2. SOURCE_DATE_EPOCH env var is set on the cross-compile step.
//  3. The EPOCH value is sourced from `git log -1 --format=%ct` (the
//     commit timestamp, deterministic per tag).
func TestV260_ReproducibleBuildsSetup_NPMWorkflow(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "publish-npm.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read publish-npm.yml: %v", err)
	}
	content := string(raw)

	// 1. Go patch pin: must contain a 1.25.X version (not just 1.25).
	if !strings.Contains(content, `go-version: "1.25.`) {
		t.Errorf("publish-npm.yml must pin Go to an exact 1.25.x.y patch version " +
			"(setup-go's bare \"1.25\" resolves to different patches on different runners, causing cross-publish SHA drift)")
	}

	// 2. SOURCE_DATE_EPOCH env var must be set on the cross-compile step.
	if !strings.Contains(content, "SOURCE_DATE_EPOCH:") {
		t.Errorf("publish-npm.yml must set SOURCE_DATE_EPOCH env var on the cross-compile step " +
			"(pins PE/Mach-O/ELF header timestamp to deterministic value; see https://reproducible-builds.org/docs/source-date-epoch/)")
	}

	// 3. EPOCH value sourced from git commit timestamp (deterministic per tag).
	if !strings.Contains(content, "git log -1 --format=%ct") {
		t.Errorf("publish-npm.yml must source EPOCH from `git log -1 --format=%%ct` " +
			"(the commit timestamp, deterministic per git tag)")
	}

	// 4. Cross-compile step must still run `go build` with the original
	// flags (regression guard: this test would pass even if someone
	// accidentally removes the build step entirely).
	if !strings.Contains(content, "go build") || !strings.Contains(content, "ldflags") {
		t.Errorf("publish-npm.yml must still run `go build` with ldflags for version injection")
	}
}

// TestV260_ReproducibleBuildsSetup_BuildMCPBWorkflow is the same check
// applied to build-mcpb.yml. Both workflows MUST use the same Go patch
// pin + SOURCE_DATE_EPOCH; otherwise the binaries they produce will
// drift again, defeating the whole fix.
func TestV260_ReproducibleBuildsSetup_BuildMCPBWorkflow(t *testing.T) {
	path := filepath.Join(repoRoot(t), ".github", "workflows", "build-mcpb.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read build-mcpb.yml: %v", err)
	}
	content := string(raw)

	// Same 4 checks as for publish-npm.yml.
	if !strings.Contains(content, `go-version: "1.25.`) {
		t.Errorf("build-mcpb.yml must pin Go to an exact 1.25.x.y patch version")
	}
	if !strings.Contains(content, "SOURCE_DATE_EPOCH:") {
		t.Errorf("build-mcpb.yml must set SOURCE_DATE_EPOCH env var on the cross-compile step")
	}
	if !strings.Contains(content, "git log -1 --format=%ct") {
		t.Errorf("build-mcpb.yml must source EPOCH from `git log -1 --format=%%ct`")
	}
	if !strings.Contains(content, "go build") || !strings.Contains(content, "ldflags") {
		t.Errorf("build-mcpb.yml must still run `go build` with ldflags for version injection")
	}
}

// TestV260_ReproducibleBuildsSetup_GoVersionsMatch is the critical
// cross-workflow check: both workflows must pin to the EXACT SAME Go
// patch version. If publish-npm.yml pins 1.25.12 and build-mcpb.yml
// pins 1.25.11 (or vice versa), the binaries will drift. This test
// fails fast on any such mismatch — much cheaper than waiting for the
// network-dependent SHA comparison to discover it.
func TestV260_ReproducibleBuildsSetup_GoVersionsMatch(t *testing.T) {
	npmContent := mustReadWorkflow(t, "publish-npm.yml")
	mcpbContent := mustReadWorkflow(t, "build-mcpb.yml")

	npmVer := extractGoVersion(npmContent)
	mcpbVer := extractGoVersion(mcpbContent)

	if npmVer == "" {
		t.Fatal("publish-npm.yml: could not extract go-version value")
	}
	if mcpbVer == "" {
		t.Fatal("build-mcpb.yml: could not extract go-version value")
	}

	if npmVer != mcpbVer {
		t.Errorf("Go version mismatch between publish-npm.yml (%q) and build-mcpb.yml (%q) — "+
			"both workflows must pin to the EXACT same patch version or the cross-publish SHA drift will return",
			npmVer, mcpbVer)
	}

	// Sanity: both must be a 1.25.x.y form (3-part, not 2-part).
	if !strings.Contains(npmVer, ".") || countDots(npmVer) < 2 {
		t.Errorf("publish-npm.yml go-version=%q must be a fully-qualified 1.25.X.Y patch version", npmVer)
	}
}

// mustReadWorkflow is a small helper that reads a workflow file by name
// and fails the test on any read error.
func mustReadWorkflow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), ".github", "workflows", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// extractGoVersion returns the value of `go-version: "..."` from a
// workflow file. Returns "" if not found. We accept any quoted form
// (single or double quotes) to be permissive about syntax variations.
func extractGoVersion(content string) string {
	// Find the go-version: line and extract the quoted value.
	// Pattern: go-version: "<value>" (or 'value').
	const prefix = `go-version:`
	idx := strings.Index(content, prefix)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(prefix):]
	// Find first quote.
	q1 := strings.IndexAny(rest, `"'`)
	if q1 < 0 {
		return ""
	}
	q := rest[q1]
	rest = rest[q1+1:]
	q2 := strings.Index(rest, string(q))
	if q2 < 0 {
		return ""
	}
	return rest[:q2]
}

// countDots counts '.' characters in s. Used to verify the Go version
// is a fully-qualified 3-part version (1.25.12), not a 2-part major.minor.
func countDots(s string) int {
	n := 0
	for _, c := range s {
		if c == '.' {
			n++
		}
	}
	return n
}
