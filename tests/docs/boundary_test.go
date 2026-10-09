// Package docs — boundary_test.go: the shipped binary's real boundary.
//
// WHY THIS EXISTS. An architecture review measured the repository from
// the parent module and concluded that internal/v4alpha/ was a parallel
// universe barely wired into production. That conclusion was wrong, and
// it was wrong for a structural reason: the binary that actually ships
// is cmd/dark-mem-mcp, which carries its OWN go.mod. A parent-module
// `go list -deps` cannot resolve it at all — the tool reports the
// package as absent rather than reporting the dependency graph. So
// every "what does this build actually contain" question answered from
// the root was answering a question about the wrong module.
//
// The consequence was a document that understates reality: §2 listed 8
// packages where 58 are reachable, and marked three directories NOT
// STARTED that have had production code for releases (adapters is
// internal/embedder, security is internal/artifact/ssrf.go,
// constitutions is internal/constitution). Its deferral annotations
// still read "alpha.2" and "alpha.3" while the product is at
// alpha.31.
//
// The same measurement also surfaced something the docs never
// mentioned: 3 of 17 internal/v4alpha packages, 8,328 LoC, are
// unreachable from the shipped binary. A directory named v4alpha/ at
// alpha.31 is a temporary namespace that outlived its temporary status.
//
// This test makes the boundary an enforced fact rather than a claim.
// It measures reachability from the real binary module and asserts the
// document's own numbers. Adding a new dead package, adding a new
// v4alpha package, or changing the reachable count all fail here
// rather than rotting unnoticed for twenty releases.
//
// IT DOES NOT, deliberately, assert LoC figures. Line counts move with
// comment policy, generated files and refactors that have nothing to do
// with whether code is reachable. The dead SET is the contract; the
// LoC column is a dated measurement for humans.
package docs

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	boundaryBegin = "<!-- boundary:begin"
	boundaryEnd   = "<!-- boundary:end"

	// The module that produces the released binary. Its own go.mod
	// makes it invisible to parent-module tooling, which is the whole
	// reason this file exists.
	binaryModuleDir = "cmd/dark-mem-mcp"

	modulePrefix = "github.com/dark-agents/dark-memory-mcp/internal/"
)

// v4alphaPackages enumerates every PACKAGE directory under
// internal/v4alpha/, keyed in the same relative form the reachability
// probe reports ("v4alpha/judge").
//
// A directory counts only when it holds .go files DIRECTLY. The first
// version of this probe walked recursively and got this wrong:
// internal/v4alpha/transport/ contains no .go files of its own, it is
// a container holding transport/mcp/, so a recursive walk counted it as
// a 17th package and then reported it as dead — inventing both a
// phantom package and a phantom dead-weight figure. Counting only root
// .go files makes the container invisible, which is the truth: it is
// not a package, and nothing is lost by not tracking it.
func v4alphaPackages(t *testing.T) map[string]bool {
	t.Helper()
	base := filepath.Join(repoRoot(t), "internal", "v4alpha")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read internal/v4alpha: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		direct, err := filepath.Glob(filepath.Join(base, e.Name(), "*.go"))
		if err != nil {
			t.Fatalf("glob internal/v4alpha/%s: %v", e.Name(), err)
		}
		if len(direct) > 0 {
			out["v4alpha/"+e.Name()] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("internal/v4alpha contained no Go packages; the probe below would report every package as dead for the wrong reason")
	}
	return out
}

// binaryReachableInternal returns the set of internal/ package paths
// reachable from the shipped binary, e.g. {"v4alpha/judge", "tools",
// "orchestration"}. Measured by running `go list -deps` with Dir set to
// the nested module — the parent module genuinely cannot resolve it.
func binaryReachableInternal(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join(repoRoot(t), filepath.FromSlash(binaryModuleDir))
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		// Failing rather than skipping is deliberate. A boundary guard
		// that quietly disables itself when the toolchain cannot measure
		// is the same defect as the vacuous guard fixed in
		// readme_consistency_test.go: it looks like protection and is
		// not.
		t.Fatalf("%s has no go.mod; the binary module moved and this probe must follow it: %v", binaryModuleDir, err)
	}

	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Dir = dir
	// Keep the module graph resolvable without mutating the repo.
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list -deps in %s failed (the boundary cannot be measured): %v\n%s", binaryModuleDir, err, stderr)
	}

	reach := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, modulePrefix) {
			continue
		}
		reach[strings.TrimPrefix(line, modulePrefix)] = true
	}
	if len(reach) == 0 {
		t.Fatal("go list -deps reported zero internal packages; the probe matched nothing and every conclusion drawn from it would be false")
	}
	return reach
}

var (
	reReachableTotal = regexp.MustCompile(`\*\*(\d+)\*\* internal packages reachable`)
	reV4AlphaLive    = regexp.MustCompile(`\*\*(\d+) of (\d+)\*\*\s*` + "`" + `internal/v4alpha/`)
	reDeadRow        = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|")
)

// boundaryBlock extracts the machine-checked region of §2. If the
// markers disappear the test fails rather than reporting zero claims,
// which is the failure mode that let §2 rot in the first place.
func boundaryBlock(t *testing.T) string {
	t.Helper()
	doc := readRepoFile(t, "docs/v4-status.md")
	i := strings.Index(doc, boundaryBegin)
	if i < 0 {
		t.Fatalf("docs/v4-status.md has no %q marker; the boundary section is no longer machine-checked and can rot again", boundaryBegin)
	}
	rest := doc[i:]
	j := strings.Index(rest, boundaryEnd)
	if j < 0 {
		t.Fatalf("docs/v4-status.md opened %q but never closes it", boundaryBegin)
	}
	return rest[:j]
}

// TestStatusDoc_BoundaryClaimsMatchBinary is the guard. The document
// must state, and the binary must agree on, three things: how many
// internal packages are reachable, how many v4alpha packages are live
// out of how many exist, and exactly which v4alpha packages are dead.
func TestStatusDoc_BoundaryClaimsMatchBinary(t *testing.T) {
	block := boundaryBlock(t)

	reach := binaryReachableInternal(t)
	all := v4alphaPackages(t)

	// Dead = exists on disk, not reachable from the binary.
	dead := map[string]bool{}
	for pkg := range all {
		if !reach[pkg] {
			dead[pkg] = true
		}
	}
	live := len(all) - len(dead)

	// Claim 1: total reachable internal packages.
	m := reReachableTotal.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("boundary block does not state a reachable-package count in the form \"**N** internal packages reachable\"; the guard cannot verify a claim it cannot parse")
	}
	claimedTotal, _ := strconv.Atoi(m[1])
	if claimedTotal != len(reach) {
		t.Errorf("doc claims %d internal packages reachable from the binary, measured %d (%d live v4alpha + %d other). "+
			"Either a package became unreachable or the count is stale",
			claimedTotal, len(reach), live, len(reach)-live)
	}

	// Claim 2: v4alpha live out of total.
	m = reV4AlphaLive.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("boundary block does not state the v4alpha ratio in the form \"**N of M** `internal/v4alpha/`\"; a new v4alpha package must be declared live or dead, so this number has to be checkable")
	}
	claimedLive, _ := strconv.Atoi(m[1])
	claimedAll, _ := strconv.Atoi(m[2])
	if claimedAll != len(all) {
		t.Errorf("doc claims %d packages under internal/v4alpha/, %d exist on disk. "+
			"A new v4alpha package must be declared live or dead in the table below", claimedAll, len(all))
	}
	if claimedLive != live {
		t.Errorf("doc claims %d of %d v4alpha packages reachable, measured %d of %d (dead: %s)",
			claimedLive, claimedAll, live, len(all), strings.Join(sortedKeys(dead), ", "))
	}

	// Claim 3: the dead set, named one per row. Asserted in both
	// directions: a package that became reachable without being
	// removed from the table is a claim that has gone false, and a
	// package that became unreachable without being declared is code
	// that quietly stopped shipping.
	var declared map[string]bool
	for _, line := range strings.Split(block, "\n") {
		row := reDeadRow.FindStringSubmatch(line)
		if row == nil {
			continue
		}
		if declared == nil {
			declared = map[string]bool{}
		}
		declared[row[1]] = true
	}
	if declared == nil {
		t.Fatal("boundary block declares no dead-package table; unreachable code cannot be reviewed if it is not written down")
	}
	for pkg := range dead {
		if !declared[pkg] {
			t.Errorf("%s is unreachable from the shipped binary but is not declared dead in docs/v4-status.md. "+
				"Declare it and give it a verdict, or wire it in", pkg)
		}
	}
	for pkg := range declared {
		if !dead[pkg] {
			t.Errorf("docs/v4-status.md declares %s dead, but it IS reachable from the shipped binary. Remove the row", pkg)
		}
	}

	t.Logf("verified boundary: %d internal packages reachable, %d of %d v4alpha live, %d dead (%s)",
		len(reach), live, len(all), len(dead), strings.Join(sortedKeys(dead), ", "))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
