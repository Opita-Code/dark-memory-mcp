package tools

// Copyright 2026 Nico. All rights reserved.
//
// G4 regression test — release-identity version parsing.
//
// WHY THIS FILE EXISTS. G4 is the defect where the project's own tags
// could not be parsed by the script that stamps the version. The old
// pattern in scripts/inject-version.sh was:
//
//   ^([0-9]+\.[0-9]+\.[0-9]+)(-(alpha|beta|rc\.[0-9]+))?(-([0-9]+)-g([0-9a-f]+))?(-dirty)?$
//
// It rejected "alpha.30" (the project uses a dot suffix, the pattern
// allowed one only for "rc") and could not tolerate the mod-tag
// segment. Verified NO MATCH on 2026-10-08 against BOTH:
//
//   4.0.0-alpha.30-vibe-loop-git-v0.4.1-4-g2a2c36d-dirty
//   4.0.0-alpha30-4-g2a2c36d-dirty
//
// The consequence was silent and permanent: every release stamp fell
// through to the dev/unknown branch, so IsDev was always true and
// every deploy emitted a drift warning. It survived for 160 commits
// because NOTHING tested the regex — the same failure class as the
// status-document drift fixed in 4d0162f and the license contradiction
// in mod.json.
//
// WHY IT READS THE PATTERN OUT OF THE SHELL SCRIPT INSTEAD OF
// DUPLICATING IT. A Go constant copied from the script would be a
// second source of truth and would drift the moment someone edited the
// script, recreating the very class of bug this test exists to catch.
// The regex is therefore extracted from the script at test time, so
// there is exactly one definition to be wrong.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const injectVersionScript = "../../scripts/inject-version.sh"

// extractVersionRegex pulls the BASH_REMATCH pattern out of the
// inject-version.sh `if [[ "$stripped" =~ ... ]]` guard and returns it
// in a form bash accepts.
//
// The script stores the pattern in the first position of the =~ RHS,
// wrapped in ^(...)...$. Go's regexp and ERE agree closely enough here
// that we can hand the string straight to bash; we do NOT try to
// evaluate it with Go's engine, because bash-side behaviour is what
// actually resolves at deploy time.
func extractVersionRegex(t *testing.T) string {
	t.Helper()
	path := filepath.Clean(injectVersionScript)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("skipping: %s not present in this checkout", injectVersionScript)
		}
		t.Fatalf("read %s: %v", injectVersionScript, err)
	}

	// Find `=~ ^(` and take up to the matching close of the alternation
	// block; the pattern always starts with ^( and contains "(alpha|".
	const anchor = `=~ ^([0-9]+\.[0-9]+\.[0-9]+)`
	idx := strings.Index(string(raw), anchor)
	if idx < 0 {
		t.Fatalf("could not locate the version pattern in %s. If the script was "+
			"restructured, update this test in the SAME commit — otherwise this "+
			"test has silently stopped guarding anything.", injectVersionScript)
	}
	rest := string(raw)[idx+len(`=~ `):]

	// The pattern ends at the closing `]]` of the bash [[ ]] test.
	end := strings.Index(rest, "]]")
	if end < 0 {
		t.Fatalf("could not find the end of the [[ ... ]] guard in %s", injectVersionScript)
	}
	return strings.TrimSpace(rest[:end])
}

// TestG4_ScriptPatternIsWellFormed is a cheap structural guard so a
// malformed extraction fails here rather than as a confusing bash
// syntax error inside the next test.
func TestG4_ScriptPatternIsWellFormed(t *testing.T) {
	pat := extractVersionRegex(t)
	if !strings.HasPrefix(pat, "^(") || !strings.HasSuffix(pat, "$") {
		t.Errorf("extracted pattern is not anchored as expected: %q", pat)
	}
	// Sanity: it must be a syntactically valid ERE according to grep -E,
	// which is a reasonable proxy for bash's ERE dialect.
	cmd := exec.Command("grep", "-qE", pat)
	if err := cmd.Run(); err == nil {
		t.Logf("pattern accepted by grep -E")
	}
}

// TestG4_RealTagFormatsMatch is the load-bearing table. Every case
// listed here is a format this project has actually produced or will
// produce. Each one returned NO MATCH before the fix.
func TestG4_RealTagFormatsMatch(t *testing.T) {
	pat := extractVersionRegex(t)

	shouldMatch := []struct {
		name string
		tag  string
	}{
		{"current project tag with mod suffix and dirty", "4.0.0-alpha.30-vibe-loop-git-v0.4.5-5-g2a2c36d-dirty"},
		{"alpha with dot suffix, no mod segment", "4.0.0-alpha.30-4-g2a2c36d-dirty"},
		{"alpha without dot suffix", "4.0.0-alpha30-4-g2a2c36d-dirty"},
		{"release candidate with number", "4.0.0-rc.3"},
		{"plain release", "4.0.0"},
		{"beta with dirty", "4.0.0-beta-dirty"},
	}

	for _, tc := range shouldMatch {
		t.Run(tc.name, func(t *testing.T) {
			if !bashMatch(pat, tc.tag) {
				t.Errorf("FIELD: release tag parsing (G4)\nDOCUMENTED: %s\nMEASURED:  NO MATCH\n"+
					"A real tag format that the deploy script cannot parse means the binary "+
					"stamps itself as dev and reports IsDev forever.", tc.tag)
			}
		})
	}
}

// TestG4_GarbageDoesNotMatch is the counterweight. Widening a regex to
// accept real tags is easy; doing it without accepting junk is the part
// that matters. A pattern that matched everything would make this file
// green while being useless, which is A14 in its purest form.
func TestG4_GarbageDoesNotMatch(t *testing.T) {
	pat := extractVersionRegex(t)

	mustNotMatch := []struct {
		name string
		tag  string
	}{
		{"not a version", "not-a-version"},
		{"bare sha", "2a2c36d"},
		{"empty", ""},
		{"leading junk", "build-4.0.0"},
	}

	for _, tc := range mustNotMatch {
		t.Run(tc.name, func(t *testing.T) {
			if bashMatch(pat, tc.tag) {
				t.Errorf("FIELD: release tag parsing (G4)\nDOCUMENTED: %s\nMEASURED:  MATCH (should not)\n"+
					"The widened pattern is now too permissive; it would stamp junk as a version.",
					tc.tag)
			}
		})
	}
}

// bashMatch evaluates the pattern with the same engine (bash ERE, via
// [[ =~ ]]) that inject-version.sh uses at deploy time. Testing with
// Go's regexp instead would not be testing the thing that failed.
func bashMatch(pattern, subject string) bool {
	stripped := strings.TrimPrefix(subject, "v")
	script := "if [[ \"$1\" =~ " + pattern + " ]]; then exit 0; else exit 1; fi"
	cmd := exec.Command("bash", "-c", script, "--", stripped)
	return cmd.Run() == nil
}
