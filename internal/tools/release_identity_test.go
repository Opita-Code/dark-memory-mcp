package tools

// Copyright 2026 Nico. All rights reserved.
//
// Release-identity and attribution guards (spec 1905, vibe-loop
// alpha-31-guard-drift).
//
// WHY THESE EXIST, because the failure they catch has happened five
// times in one week and always in the same shape: a value was correct
// in one place and nothing compared where it was PUBLISHED to where it
// was computed.
//
//	1. L8.1a advisory computed correctly by RunDelegateIntentCore, then
//	   dropped by the v4alpha->v3 converter. Every test green.
//	2. DefaultToolGrants missing LLM_BIND and EVENTS, so the gate
//	   silently refused four canonical tools in live use.
//	3. The README said 62 tools one line below a header saying 73.
//	4. The GitHub description claimed 29 tools against a measured 73.
//	5. A false "Co-Authored-By: Claude Opus 4.8" landed on main and
//	   stayed visible on the commit page.
//
// In every case the runtime was right and the publication was stale,
// or in the last case the publication was false. A guard that only
// tests the runtime cannot see any of this.
//
// WHAT THESE DO, and deliberately what they do NOT do.
//
// These compare PUBLISHED values against each other and against the
// machine-readable manifest. They do NOT re-implement precheck-version,
// which already owns "the eight stamped package files agree with the
// git tag". Duplicating that here would create a second source of
// truth for one rule, which is the same mistake in miniature.
//
// They do not check prose quality, tone, or whether a sentence is
// kind. They check identifiers that have exactly one right answer.

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// --- guard 1: the product version, stated in two human-facing places ---

// productVersionRE extracts the release from the README STATUS banner,
// which reads "## STATUS: v4-alpha.31, ...". Accepting only the
// v4-alpha.N shape keeps the comparison narrow on purpose: a looser
// pattern would start matching version numbers that belong to the mod
// (0.4.5) or to historical entries, and a guard that matches the wrong
// thing is worse than no guard.
var productVersionRE = regexp.MustCompile(`STATUS:\s*v4-alpha\.(\d+)`)

// statusTableRE extracts the alpha from the "| Status | **alpha.N**"
// row in docs/v4-status.md. Reusing the same shape the existing
// TestStatusDoc_StaleAlphaClaimsAbsent anchors on keeps the two guards
// talking about the same cell instead of drifting apart.
var statusTableRE = regexp.MustCompile(`(?m)^\|\s*Status\s*\|\s*\*\*alpha\.(\d+)\*\*`)

// TestReleaseIdentity_ReadmeAndStatusDocAgree asserts the two documents
// a reader lands on name the same release.
//
// On 2026-10-09 the README STATUS said alpha.31 while the v4-status
// table still said alpha.30, so the two entry points to the project
// disagreed. Nothing caught it because each document was internally
// plausible.
func TestReleaseIdentity_ReadmeAndStatusDocAgree(t *testing.T) {
	readme := readRepoFileOrSkip(t, "../../README.md")
	status := readStatusDoc(t)

	rm := productVersionRE.FindStringSubmatch(readme)
	if rm == nil {
		t.Fatalf("no 'STATUS: v4-alpha.N' banner in README.md; the banner shape " +
			"changed and this guard must be updated with it rather than skipped")
	}
	sm := statusTableRE.FindStringSubmatch(status)
	if sm == nil {
		t.Fatalf("no '| Status | **alpha.N**' row in %s", statusDocPath)
	}
	readmeN, err := strconv.Atoi(rm[1])
	if err != nil {
		t.Fatalf("parse README alpha: %v", err)
	}
	statusN, err := strconv.Atoi(sm[1])
	if err != nil {
		t.Fatalf("parse %s alpha: %v", statusDocPath, err)
	}
	if readmeN != statusN {
		t.Errorf("FIELD: product release, README banner vs %s status table\n"+
			"DOCUMENTED: README says v4-alpha.%d, status table says alpha.%d\n"+
			"These are the first two things a reader sees. When they disagree, "+
			"one of them is describing a release that does not exist.",
			statusDocPath, readmeN, statusN)
	}
}

// --- guard 2: the mod manifest, stated in two places ---

// modManifestPath and modChangelogPath are relative to internal/tools.
const (
	modManifestPath  = "../../mods/vibe-loop-git/mod.json"
	modChangelogPath = "../../mods/vibe-loop-git/CHANGELOG.md"
)

// changelogTopRE extracts the version from the first "## [x.y.z]" heading.
var changelogTopRE = regexp.MustCompile(`(?m)^##\s*\[(\d+\.\d+\.\d+)\]`)

// TestReleaseIdentity_ModManifestMatchesChangelog asserts mod.json's
// version equals the top CHANGELOG entry.
//
// This one was broken from the start of the public release: mod.json
// declared 0.4.5 while the CHANGELOG stopped at 0.4.4, and nobody
// noticed for a full day. It is a small drift, and small drifts are
// what train everyone to ignore the large ones.
//
// NOTE ON VERSION SPACES: the mod's version (0.4.5) and the product's
// version (4.0.0-alpha.31) are deliberately NOT compared. They are
// independent numbers, and a guard that asserted they matched would be
// asserting something false about the next release.
func TestReleaseIdentity_ModManifestMatchesChangelog(t *testing.T) {
	manifest := readRepoFileOrSkip(t, modManifestPath)
	changelog := readRepoFileOrSkip(t, modChangelogPath)

	mv := modManifestVersion(manifest)
	if mv == "" {
		t.Fatalf("no \"version\" field parsed from %s; the manifest shape changed "+
			"and this guard must be updated with it", modManifestPath)
	}
	cv := topChangelogVersion(changelog)
	if cv == "" {
		t.Fatalf("no '## [x.y.z]' heading in %s", modChangelogPath)
	}
	if mv != cv {
		t.Errorf("FIELD: mod version, manifest vs changelog\n"+
			"DOCUMENTED: %s declares %q, %s's newest entry is [%s]\n"+
			"A manifest version with no matching changelog entry is a release "+
			"nobody wrote down.",
			modManifestPath, mv, modChangelogPath, cv)
	}
}

// modManifestVersion pulls the top-level "version" string out of mod.json
// without a JSON round-trip, so this guard keeps working if the manifest
// grows fields that fail to unmarshal. A guard that breaks when
// unrelated manifest fields change is a guard that gets deleted.
func modManifestVersion(manifest string) string {
	re := regexp.MustCompile(`"version"\s*:\s*"([^"]+)"`)
	if m := re.FindStringSubmatch(manifest); m != nil {
		return m[1]
	}
	return ""
}

func topChangelogVersion(changelog string) string {
	if m := changelogTopRE.FindStringSubmatch(changelog); m != nil {
		return m[1]
	}
	return ""
}

// readRepoFileOrSkip reads a repo file, skipping when absent. The mod
// and the docs are consumed in several shapes (mod-only checkouts,
// vendored subtrees), and a file that does not exist cannot be making a
// false claim about itself.
func readRepoFileOrSkip(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("skipping: %s not present in this checkout", path)
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// gitLogBodies returns every reachable commit message and the number of
// commits it saw.
//
// The NUL separator is deliberate. Commit messages contain newlines, so
// splitting the output of "git log" on newlines would shred one message
// into many and make it impossible to tell where a message ends. -z
// gives a record separator that cannot appear inside a message.
func gitLogBodies(repoDir string) ([]string, int, error) {
	cmd := exec.Command("git", "-C", repoDir, "log", "--format=%B%x00")
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, err
	}
	parts := strings.Split(string(out), "\x00")
	msgs := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			msgs = append(msgs, p)
		}
	}
	return msgs, len(msgs), nil
}

// --- guard 3: commit trailers must name real collaborators ---

// allowedCoAuthors is the set of identities that may appear in a
// Co-authored-by trailer. It was built from the repository's actual
// history (git log shows three Co-authored-by lines, all Nico), not
// from who plausibly might contribute. An allowlist seeded from
// imagination invites its first false entry.
//
// Comparison is on the lowercased name only. Matching the email too
// would break the moment a contributor switches address providers, and
// a guard that cries wolf gets ignored.
var allowedCoAuthors = map[string]bool{
	"nico":       true,
	"dark-agent": true,
	"opita-ai":   true,
}

// coAuthorRE captures the display name from a Co-authored-by trailer.
var coAuthorRE = regexp.MustCompile(`(?im)^Co-authored-by:\s*([^<\r\n]+?)\s*(?:<[^>]*>)?\s*$`)

// minHistoryForFullScan is the commit count below which the history is
// treated as shallow. CI checks out with the default fetch-depth of 1,
// so a clone-based scan there sees the tip commit only.
const minHistoryForFullScan = 50

// TestGitHistory_NoForeignCoAuthorTrailers asserts every Co-authored-by
// trailer in the reachable history names someone the maintainer
// authorised.
//
// On 2026-10-09 commit 1c33a7b8 carried "Co-Authored-By: Claude Opus
// 4.8 (1M context) <noreply@anthropic.com>". The maintainer does not
// use Claude and the trailer was false. GitHub's Contributors API did
// not show it, because that address is not linked to any account, so
// the only place the lie was visible was the commit page itself. A
// human reviewer has to open each commit to see it; this test reads
// every message in one pass.
func TestGitHistory_NoForeignCoAuthorTrailers(t *testing.T) {
	if _, err := os.Stat("../../.git"); err != nil {
		t.Skip("skipping: not inside a git checkout")
	}
	messages, count, err := gitLogBodies("../../")
	if err != nil {
		// A source tarball has no git. That is not a trust failure.
		t.Skipf("skipping: cannot read git history (%v)", err)
	}
	if count < minHistoryForFullScan {
		t.Logf("NOTE: only %d commits visible (shallow clone). This guard checked the "+
			"newest commits; a false trailer deeper in history would not be seen. "+
			"Run the suite in a full checkout, or set fetch-depth: 0, for a complete scan.",
			count)
	}

	for _, msg := range messages {
		for _, m := range coAuthorRE.FindAllStringSubmatch(msg, -1) {
			name := strings.ToLower(strings.TrimSpace(m[1]))
			// Strip a parenthetical qualifier such as "(1M context)" so the
			// guard keys on identity, not on formatting.
			if p := strings.Index(name, "("); p >= 0 {
				name = strings.TrimSpace(name[:p])
			}
			if name == "" || allowedCoAuthors[name] {
				continue
			}
			subject := msg
			if i := strings.Index(subject, "\n"); i >= 0 {
				subject = subject[:i]
			}
			t.Errorf("FIELD: commit attribution\n"+
				"UNAUTHORISED: Co-authored-by %q\n"+
				"COMMIT: %s\n"+
				"ALLOWED: %v\n"+
				"A co-author line is a claim about who worked on this. If that is not "+
				"true, the fix is to amend the commit, not to explain the exception.",
				name, strings.TrimSpace(subject), allowedCoAuthors)
		}
	}
}
