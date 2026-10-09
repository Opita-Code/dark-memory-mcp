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
	"strconv"
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
// surface claim must match the live sources of truth.
//
// The catalog moved to docs/tools.md on 2026-10-09, so the guard
// follows it there rather than keeping a second copy alive in the
// README. That split is deliberate: the README's job is to get a
// stranger to an install, and an 84-line catalog above the fold was
// serving as evidence rather than as persuasion. Each claim is still
// checked, in the file that now makes it.
//
// Moving a guard's target is exactly where protection disappears
// silently, so the mapping is asserted rather than assumed: if
// docs/tools.md ever goes missing, readRepoFile fails loudly instead
// of this guard passing vacuously on an empty string.
func TestDocs_SurfaceNumbersMatchRuntime(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	catalog := readRepoFile(t, "docs/tools.md")

	wantTools := len(tools.CanonicalOrder())
	wantSchema := sqlite.CurrentVersion()
	wantNamespaces := tools.NamespaceCount()

	checks := []struct {
		label string
		hay   string
		file  string
		want  string
	}{
		{"MCP tools badge", readme, "README.md", fmt.Sprintf("MCP-%d%%20canonical%%20tools", wantTools)},
		{"schema badge", readme, "README.md", fmt.Sprintf("schema-v%d", wantSchema)},
		{"Las N herramientas header", catalog, "docs/tools.md", fmt.Sprintf("Las %d herramientas", wantTools)},
		{"N oficios", catalog, "docs/tools.md", fmt.Sprintf("%d oficios", wantNamespaces)},
	}
	for _, c := range checks {
		if !strings.Contains(c.hay, c.want) {
			t.Errorf("%s missing %q (%s) — docs drifted from the runtime source of truth (want tools=%d, schema=%d, namespaces=%d)",
				c.file, c.want, c.label, wantTools, wantSchema, wantNamespaces)
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

// TestDocs_NamespaceTableCountsDerivable asserts the per-namespace
// "N tools" headings in docs/tools.md match the derived counts.
//
// This used to read the README and it passed vacuously: it looked for
// headings named `### AGENT_MEMORY`, while the document actually
// writes them in Spanish (`### Cuaderno del agente (10 tools, ...)`).
// The loop found nothing, matched nothing, and reported success. A
// guard that checks for a spelling the document never used is a guard
// that protects nothing while looking like it does.
//
// It now walks the document's own headings, matches each one to a
// namespace by looking for that namespace's tool names in the lines
// beneath it, and compares the claimed count to the live count. The
// checked>0 assertion at the end is what makes this trustworthy: a
// guard that has silently stopped matching anything reports itself.
func TestDocs_NamespaceTableCountsDerivable(t *testing.T) {
	catalog := readRepoFile(t, "docs/tools.md")
	groups := tools.NamespaceGroups()

	lines := strings.Split(catalog, "\n")
	checked := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, "### ") {
			continue
		}
		open := strings.Index(line, "(")
		if open < 0 || !strings.Contains(line[open:], "tools") {
			continue
		}
		rest := line[open+1:]
		end := strings.IndexAny(rest, " ,")
		if end < 0 {
			continue
		}
		claimed, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
		if err != nil {
			continue
		}
		heading := strings.TrimSpace(strings.TrimPrefix(line, "### "))

		// The tool names sit in the next few lines; that is the
		// reliable link between a prose heading and a namespace.
		var window strings.Builder
		for j := i + 1; j < len(lines) && j <= i+4; j++ {
			if strings.HasPrefix(lines[j], "### ") {
				break
			}
			window.WriteString(lines[j])
			window.WriteString("\n")
		}

		// Compare WHOLE tokens, not substrings. The document writes
		// wired names ("agent_memory_recall") while the registry holds
		// bare ones ("recall"), so a substring match makes the
		// AGENT_MEMORY heading also "contain" CONTEXT's recall tool
		// and the guard reports a wrong namespace. That bug fired on
		// the first run and named the wrong namespace four times.
		tokens := map[string]bool{}
		for _, tok := range strings.FieldsFunc(window.String(), func(r rune) bool {
			return !(r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		}) {
			tokens[tok] = true
		}

		for _, g := range groups {
			matched := false
			for _, tool := range g.Tools {
				if tokens[tool] || tokens["dark_memory_"+tool] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if claimed != len(g.Tools) {
				t.Errorf("docs/tools.md heading %q claims %d tools, namespace %s actually has %d",
					heading, claimed, g.Name, len(g.Tools))
			}
			checked++
			break
		}
	}
	if checked == 0 {
		t.Errorf("no namespace heading in docs/tools.md could be checked (checked=%d) — "+
			"the guard is verifying nothing. Either the document changed shape or this "+
			"test has gone stale, and a silently-useless drift guard is worse than none "+
			"because it is trusted", checked)
	}
	t.Logf("verified %d namespace tool counts against the live registry", checked)
}
