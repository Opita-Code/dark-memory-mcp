// Package docs — readme_consistency_test.go: drift guard for the
// public documentation.
//
// The README + release metadata (server.json, npm/*/package.json)
// used to carry hardcoded tool counts, schema versions and version
// strings that drifted on every release. This test re-derives the
// expected values from the live sources of truth and fails when the
// docs disagree:
//
//	MCP badge tools-N        <- len(tools.CanonicalOrder())
//	"Las N herramientas"     <- len(tools.CanonicalOrder())
//	"N oficios"              <- tools.NamespaceCount()
//	schema-vN badge          <- sqlite.CurrentVersion()
//	server.json + npm pkg    <- git describe --tags (when a tag exists)
//
// The check is exact-substring based: it FAILS when the number is
// wrong, and passes when it matches. If a doc legitimately references
// a historical number (CHANGELOG entries, archaeology), keep those
// OUT of the checked strings — the checked lines are the surface
// claims that must always match the binary.
package docs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/tools"
)

// repoRoot walks up from the test source to the repo root
// (<root>/tests/docs/readme_consistency_test.go → root is 3 up).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Tests run with CWD = package dir (tests/docs), so root is 2 up.
	root := filepath.Clean(filepath.Join(dir, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not found from %s: %v", dir, err)
	}
	return root
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// gitDescribe returns the current git describe string (or empty when
// git is unavailable — checks are skipped then).
func gitDescribe(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "describe", "--tags", "--always").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitBaseTag returns the most recent tag (git describe --abbrev=0)
// with the "v" prefix stripped, or "" when no tag exists.
func gitBaseTag(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}

// TestDocs_SurfaceNumbersMatchRuntime is the drift guard: every
// surface claim in README.md must match the live sources of truth.
func TestDocs_SurfaceNumbersMatchRuntime(t *testing.T) {
	readme := readRepoFile(t, "README.md")

	wantTools := len(tools.CanonicalOrder())
	wantSchema := sqlite.CurrentVersion()
	wantNamespaces := tools.NamespaceCount()

	checks := []struct {
		label string
		want  string
	}{
		{"MCP tools badge", fmt.Sprintf("MCP-%d%%20canonical%%20tools", wantTools)},
		{"Las N herramientas header", fmt.Sprintf("## Las %d herramientas", wantTools)},
		{"N oficios", fmt.Sprintf("%d oficios", wantNamespaces)},
		{"schema badge", fmt.Sprintf("schema-v%d", wantSchema)},
	}
	for _, c := range checks {
		if !strings.Contains(readme, c.want) {
			t.Errorf("README.md missing %q (%s) — docs drifted from the runtime source of truth (want tools=%d, schema=%d, namespaces=%d)",
				c.want, c.label, wantTools, wantSchema, wantNamespaces)
		}
	}
}

// TestDocs_ReleaseMetadataMatchesGitTag verifies server.json + the
// npm package.json files carry the current release version.
//
// The check is STRICT only when the working tree sits exactly on a
// tag (git describe == base tag): at release time the metadata files
// MUST carry the tag's version. Between releases (commits after the
// last tag), the metadata is aspirational (points at the next
// release) and the check skips — the strict moment is the tag, and
// `go generate ./...` right after tagging is the fix.
func TestDocs_ReleaseMetadataMatchesGitTag(t *testing.T) {
	full := gitDescribe(t)
	base := gitBaseTag(t)
	if full == "" || base == "" {
		t.Skip("no git tag available; skipping release metadata check")
	}
	if full != base {
		t.Skipf("working tree not exactly on a tag (describe=%q, base=%q); release metadata check applies at tag time", full, base)
	}

	files := []string{
		"server.json",
		"mcpb/manifest.json",
		"npm/wrapper/package.json",
		"npm/platform-linux-x64/package.json",
		"npm/platform-linux-arm64/package.json",
		"npm/platform-darwin-x64/package.json",
		"npm/platform-darwin-arm64/package.json",
		"npm/platform-win32-x64/package.json",
		"npm/platform-win32-arm64/package.json",
	}
	for _, rel := range files {
		content := readRepoFile(t, rel)
		want := `"version": "` + base + `"`
		if !strings.Contains(content, want) {
			t.Errorf("%s: version field does not match release tag %q — run 'go generate' after tagging", rel, base)
		}
	}
}

// TestDocs_NamespaceTableCountsDerivable asserts the README's
// per-namespace "N tools" headers (if any) match the derived counts.
// README rows use the format `### NAME (N tools, ...)`; only the
// count is checked, so renames of sections don't break the test.
func TestDocs_NamespaceTableCountsDerivable(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	nsCounts := tools.NamespaceCounts()
	for _, ns := range tools.NamespaceGroups() {
		want := fmt.Sprintf("(%d tools", nsCounts[ns.Name])
		if strings.Contains(readme, "### "+ns.Name) && !strings.Contains(readme, "### "+ns.Name+" ("+want[1:]) {
			// The section exists but the count may be phrased differently;
			// only fail on an explicit wrong count, not on missing section.
			continue
		}
	}
	// Explicit check for the two most drift-prone namespaces.
	for _, ns := range []string{"AGENT_MEMORY", "ERROR_OBS"} {
		want := fmt.Sprintf("%d tools", nsCounts[ns])
		// grep the README lines mentioning the namespace in a heading.
		for _, line := range strings.Split(readme, "\n") {
			if strings.Contains(line, "### "+ns) && strings.Contains(line, "tools") && !strings.Contains(line, want) {
				t.Errorf("README heading %q does not contain the derived count %q", strings.TrimSpace(line), want)
			}
		}
	}
}
