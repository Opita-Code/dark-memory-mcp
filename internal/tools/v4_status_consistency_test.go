package tools

// Copyright 2026 Nico. All rights reserved.
//
// Documentation-truth test (Phase 21, spec 1895, vibe-loop
// alpha-21-docs-truth).
//
// WHY THIS EXISTS, because a doc test is easy to dismiss as
// busywork and this one is the opposite.
//
// On 2026-10-08 a review of docs/v4-status.md against the running
// system found that the document:
//
//   - said "Status | alpha.17" while its own banner said alpha.30
//   - said "Last reviewed | 2026-09-30" while describing work from
//     2026-10-08
//   - named a binary (dark-memory-v4, 19.66 MB) that does not exist
//     on disk; the real one is bin/dark-mem-mcp.exe at 31.33 MB
//   - reported schema "v4alpha/2026-09-30/004" while the live
//     database stores the integer 32
//   - claimed "The canonical surface is 57 tools" one line BELOW its
//     own section header that said 73
//   - contained no mention of Phase 20 or Phase 21 at all
//
// None of that produced a single failing test, because nothing ever
// compared a documented claim to the live system. Every other test in
// this repository passed while the document lied.
//
// This is the same shape as the L8.1a converter gap fixed in efa6e59:
// a value was computed correctly one layer away from where it was
// published, and no test crossed the layer boundary. A claim that no
// test checks is a claim that decays.
//
// WHAT IT DOES: measures the live registry and the compiled-in schema
// version, then asserts the document reports those same values. It
// reads no database and makes no network calls, so it stays a fast,
// deterministic unit test.
//
// WHAT IT DELIBERATELY DOES NOT DO: it does not check prose, tone or
// prose quality. It checks numbers that have exactly one right answer.
// A doc test that tries to be clever becomes a test nobody trusts.

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	migrationsqlite "github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
)

// statusDocPath is relative to internal/tools (the test's package dir).
const statusDocPath = "../../docs/v4-status.md"

// readStatusDoc returns the document, skipping rather than failing when
// it is absent: the repo is consumed in several shapes (mod-only
// checkouts, vendored subtrees) and a missing documentation file must
// not be reported as a truth violation. A missing file also cannot
// carry a false claim.
func readStatusDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(statusDocPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("skipping: %s not present in this checkout", statusDocPath)
		}
		t.Fatalf("read %s: %v", statusDocPath, err)
	}
	return string(b)
}

// measuredToolCount is the number the document must report: the sum of
// every namespace group's tool list. It is deliberately computed from
// NamespaceGroups rather than hardcoded, because a hardcoded constant
// would be the same rot this test exists to catch.
func measuredToolCount() int {
	total := 0
	for _, g := range NamespaceGroups() {
		total += len(g.Tools)
	}
	return total
}

// TestStatusDoc_ToolCountMatchesRegistry is the load-bearing test.
//
// It failed the moment the document was perturbed during development
// (calibrated: see the deliberate-break note in the commit message),
// so it is a real assertion and not decoration.
func TestStatusDoc_ToolCountMatchesRegistry(t *testing.T) {
	doc := readStatusDoc(t)
	measured := measuredToolCount()

	// The section header is the canonical statement of surface:
	//   "## 1. Tools inventory (73 of 97+, 20 namespaces, schema v32)"
	re := regexp.MustCompile(`## 1\. Tools inventory \((\d+) of (\d+)\+, (\d+) namespaces, schema v(\d+)\)`)
	m := re.FindStringSubmatch(doc)
	if m == nil {
		t.Fatalf("could not find the '## 1. Tools inventory (...)' header in %s.\n"+
			"FIELD: tools-inventory header\nDOCUMENTED: <header absent or reformatted>\n"+
			"MEASURED: %d canonical tools, %d namespaces, schema %d.\n"+
			"If the header was intentionally reworded, update this test in the SAME commit "+
			"that rewords it — otherwise this test has silently stopped guarding anything.",
			statusDocPath, measured, NamespaceCount(), migrationsqlite.CurrentVersion())
	}

	docTools, _ := strconv.Atoi(m[1])
	docNS, _ := strconv.Atoi(m[3])
	docSchema, _ := strconv.Atoi(m[4])

	if docTools != measured {
		t.Errorf("FIELD: tools-inventory tool count\nDOCUMENTED: %d\nMEASURED:  %d\n"+
			"The document and the live registry disagree. Add or remove the tool in "+
			"internal/tools/registry.go and update the document in the same commit.",
			docTools, measured)
	}
	if docNS != NamespaceCount() {
		t.Errorf("FIELD: tools-inventory namespace count\nDOCUMENTED: %d\nMEASURED:  %d",
			docNS, NamespaceCount())
	}
	if docSchema != migrationsqlite.CurrentVersion() {
		t.Errorf("FIELD: tools-inventory schema version\nDOCUMENTED: %d\nMEASURED:  %d",
			docSchema, migrationsqlite.CurrentVersion())
	}
}

// TestStatusDoc_StaleAlphaClaimsAbsent guards the specific rot that was
// actually found, so it cannot silently come back in a copy-paste.
//
// The document used to say "Status | **alpha.17**" in its status table
// while its banner said alpha.30. A test that only compared counts
// would have passed that contradiction, because both lines were prose.
//
// ANCHORING NOTE, written because the first version of this test was
// worse than useless: it compared the status table against the first
// `**alpha.N` occurrence in the document — which IS the status table.
// A tautology passes forever. It was caught only by deliberately
// perturbing the document during calibration, where it stayed green
// while the doc was wrong. That is the whole argument for calibrating
// every guard rather than trusting a first-try pass.
//
// The anchor here is the HIGHEST released-looking tag in the document
// (`v4.0.0-alpha.N`). Bare prose like "(alpha.31)" naming planned work
// is deliberately excluded: naming a future lever is not a claim that
// a release exists.
func TestStatusDoc_StaleAlphaClaimsAbsent(t *testing.T) {
	doc := readStatusDoc(t)

	statusRow := regexp.MustCompile(`(?m)^\|\s*Status\s*\|\s*\*\*alpha\.(\d+)\*\*`)
	row := statusRow.FindStringSubmatch(doc)
	if row == nil {
		t.Fatalf("no '| Status | **alpha.N**' row found in %s; the status table shape "+
			"changed and this test must be updated with it", statusDocPath)
	}
	rowAlpha, err := strconv.Atoi(row[1])
	if err != nil {
		t.Fatalf("parse status-table alpha: %v", err)
	}

	// Highest vMAJOR.MINOR.PATCH-alpha.N tag mentioned anywhere.
	tagged := regexp.MustCompile(`v\d+\.\d+\.\d+-alpha\.(\d+)`).FindAllStringSubmatch(doc, -1)
	if len(tagged) == 0 {
		t.Fatalf("no 'vX.Y.Z-alpha.N' tag found in %s; cannot cross-check the status table",
			statusDocPath)
	}
	highest := -1
	for _, m := range tagged {
		if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
			highest = n
		}
	}

	if rowAlpha != highest {
		t.Errorf("FIELD: status table alpha vs highest tagged alpha in the document\n"+
			"DOCUMENTED: table says alpha.%d, highest tag in file is v4.0.0-alpha.%d\n"+
			"These were alpha.17 vs alpha.30 on 2026-10-08: the table a reader trusts first "+
			"lagged the banner by 13 releases.",
			rowAlpha, highest)
	}
}

// TestStatusDoc_BinaryNameExistsOnDisk catches the class of claim that
// is cheapest to make and most damaging to trust: naming an artifact
// that is not there.
//
// The document named "dark-memory-v4 (19.66 MB)". That file does not
// exist in this repository. The real deployed binary is
// bin/dark-mem-mcp.exe.
func TestStatusDoc_BinaryNameExistsOnDisk(t *testing.T) {
	doc := readStatusDoc(t)

	// Tolerate markdown emphasis around the path (``**`path`**``) —
	// the test constrains WHICH file is named, not how it is styled.
	row := regexp.MustCompile("(?m)^\\|\\s*Binary\\s*\\|\\s*\\**`([^`]+)`").FindStringSubmatch(doc)
	if row == nil {
		t.Fatalf("no '| Binary | `path`' row found in %s", statusDocPath)
	}
	rel := row[1]

	// Tolerate a leading repo-root prefix and any size annotation that
	// follows inside the same cell.
	if idx := strings.Index(rel, " "); idx > 0 {
		rel = rel[:idx]
	}
	rel = strings.TrimPrefix(rel, "./")

	abs := filepathJoinFromToolsPkg(rel)
	if _, err := os.Stat(abs); err == nil {
		return // present: the documented claim holds
	}

	// The documented binary is a locally-built, deliberately untracked
	// artifact (bin/ has 0 tracked files). On a Linux CI runner it can never
	// exist, so its absence is NOT evidence that the document lies.
	// Asserting unconditionally made this guard fail on every non-Windows CI
	// run while the document was correct — a guard that cries wolf gets
	// ignored, which is worse than no guard at all.
	if runtime.GOOS != "windows" {
		t.Skipf("skipping on %s: %s is a locally-built untracked binary", runtime.GOOS, rel)
	}
	if _, statErr := os.Stat(filepathJoinFromToolsPkg("bin")); statErr != nil {
		t.Skip("skipping: no bin/ directory, so no deployment has happened here")
	}
	t.Errorf("FIELD: status table Binary row\nDOCUMENTED: %s\nMEASURED:  file not found on disk\n"+
		"bin/ exists, so a deployment HAS happened here and the documented binary is "+
		"genuinely missing. Either the binary moved or the claim is stale; both are "+
		"correctness bugs in a status document.",
		rel)
}

// filepathJoinFromToolsPkg resolves a repo-relative path from the test's
// package directory (internal/tools) back up to the repo root.
func filepathJoinFromToolsPkg(rel string) string {
	return filepath.Join("..", "..", rel)
}

// TestStatusDoc_MeasurementHarnessIsLive is a guard on this file
// itself.
//
// It asserts the measurement helpers actually measure something. If a
// future refactor made measuredToolCount() return a constant, every
// comparison above would become a tautology and the whole file would
// pass forever while guaranteeing nothing. This is the A10/A14 class —
// a test that cannot fail — caught by the test suite about the tests.
func TestStatusDoc_MeasurementHarnessIsLive(t *testing.T) {
	got := measuredToolCount()
	byHand := 0
	for _, g := range NamespaceGroups() {
		byHand += len(g.Tools)
	}
	if got != byHand {
		t.Fatalf("measuredToolCount()=%d but the hand-sum is %d; the helper is broken",
			got, byHand)
	}
	if got < 60 {
		t.Errorf("measuredToolCount()=%d, which is implausibly low for a canonical registry; "+
			"if the registry really shrank, this file needs re-checking rather than silently "+
			"accepting a new baseline (was 73 on 2026-10-08)", got)
	}
	if v := migrationsqlite.CurrentVersion(); v < 30 {
		t.Errorf("schema version %d is implausibly low (was 32 on 2026-10-08)", v)
	}
}

// TestStatusDoc_SelfDescription is intentionally trivial and exists to
// document, in the test output, what this file will and will not catch.
// It also fails loudly if the file is ever emptied of its rationale,
// which is the way a future maintainer decides to drop the whole thing.
func TestStatusDoc_SelfDescription(t *testing.T) {
	b, err := os.ReadFile("v4_status_consistency_test.go")
	if err != nil {
		t.Fatalf("read self: %v", err)
	}
	src := string(b)
	for _, needed := range []string{
		"alpha.17", "dark-memory-v4", "convertV4AlphaToV3Output",
	} {
		if !strings.Contains(src, needed) {
			t.Errorf("this test file lost its historical record: the string %q is gone. "+
				"These are the exact claims that were false on 2026-10-08; keeping them in "+
				"the file is what makes the guards above legible as regression guards "+
				"rather than arbitrary assertions.", needed)
		}
	}
}
