// Package vibecase_test — taxonomy_test.go covers the canonical
// C1..C7 taxonomy. The package is the single source of truth for
// what cases exist; these tests pin the public contract:
//
//   - The seven canonical constants are stable (renames = breaking).
//   - All() returns them in order.
//   - JSONSchemaEnum() returns the canonical enum slice.
//   - Parse rejects empty + unknown + mixed-case.
//   - MustParse panics on bad input.
//   - Description round-trips for known cases and falls back gracefully
//     for unknown ones.
package vibecase_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// TestAll_StableOrder pins the canonical order. Reordering would
// change JSON Schema enum serialisation, which is observable on
// the wire — a breaking change.
func TestAll_StableOrder(t *testing.T) {
	want := []vibecase.Case{
		vibecase.CaseCode,
		vibecase.CaseText,
		vibecase.CaseImage,
		vibecase.CaseVideo,
		vibecase.CaseAudio,
		vibecase.CaseMultiModal,
		vibecase.CaseMixed,
	}
	got := vibecase.All()
	if len(got) != len(want) {
		t.Fatalf("All() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("All()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestAll_DefensiveCopy ensures callers cannot mutate the package's
// internal state by writing through All()'s return value.
func TestAll_DefensiveCopy(t *testing.T) {
	a := vibecase.All()
	a[0] = "ZZZ"
	b := vibecase.All()
	if b[0] == "ZZZ" {
		t.Fatalf("All() returned a shared slice; mutation leaked into package state")
	}
}

// TestJSONSchemaEnum_MatchesAll pins the invariant that JSONSchemaEnum
// is the stringified form of All() in the same order. The two
// functions exist separately only because JSON Schema expects []string
// and the in-memory API uses []Case.
func TestJSONSchemaEnum_MatchesAll(t *testing.T) {
	got := vibecase.JSONSchemaEnum()
	want := vibecase.All()
	if len(got) != len(want) {
		t.Fatalf("JSONSchemaEnum() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != string(want[i]) {
			t.Errorf("JSONSchemaEnum()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParse_HappyPath covers all seven canonical cases.
func TestParse_HappyPath(t *testing.T) {
	cases := []struct {
		in   string
		want vibecase.Case
	}{
		{"C1", vibecase.CaseCode},
		{"C2", vibecase.CaseText},
		{"C3", vibecase.CaseImage},
		{"C4", vibecase.CaseVideo},
		{"C5", vibecase.CaseAudio},
		{"C6", vibecase.CaseMultiModal},
		{"C7", vibecase.CaseMixed},
	}
	for _, tc := range cases {
		got, err := vibecase.Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParse_TrimsWhitespace is required because callers in the
// dark-research gemela sometimes pass values with surrounding
// whitespace from YAML/JSON pipelines.
func TestParse_TrimsWhitespace(t *testing.T) {
	for _, in := range []string{"  C1", "C1 ", " C1 ", "\tC1\n"} {
		got, err := vibecase.Parse(in)
		if err != nil {
			t.Errorf("Parse(%q) error = %v", in, err)
			continue
		}
		if got != vibecase.CaseCode {
			t.Errorf("Parse(%q) = %q, want C1", in, got)
		}
	}
}

// TestParse_RejectsEmpty confirms Parse returns ErrInvalidCase for
// zero values. This catches "vibe_case was forgotten" before it
// reaches the DB.
func TestParse_RejectsEmpty(t *testing.T) {
	for _, in := range []string{"", " ", "\t", "\n"} {
		_, err := vibecase.Parse(in)
		if !errors.Is(err, vibecase.ErrInvalidCase) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidCase", in, err)
		}
	}
}

// TestParse_RejectsUnknown documents the strict parsing policy:
// no silent uppercase, no silent fallback to a default, no trimming
// of non-ASCII, no accepting of partial prefixes like "C".
func TestParse_RejectsUnknown(t *testing.T) {
	for _, in := range []string{"C0", "C8", "c1", "code", "CODE", "C-1", "C1.0", "ё"} {
		_, err := vibecase.Parse(in)
		if !errors.Is(err, vibecase.ErrInvalidCase) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidCase", in, err)
		}
	}
}

// TestParse_ErrorMentionsAllowedValues verifies the error message
// contains the canonical enum list so the operator can see what
// IS valid without consulting docs. This is the message surfaced
// through `dark_memory_vibe_spec` to the calling agent.
func TestParse_ErrorMentionsAllowedValues(t *testing.T) {
	_, err := vibecase.Parse("C0")
	if err == nil {
		t.Fatalf("expected error")
	}
	for _, want := range []string{"C1", "C2", "C7"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

// TestMustParse_PanicsOnInvalid pins the panic contract.
func TestMustParse_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("MustParse(C0) should panic")
		}
	}()
	_ = vibecase.MustParse("C0")
}

// TestMustParse_OK ensures the happy path of MustParse returns
// the canonical Case.
func TestMustParse_OK(t *testing.T) {
	got := vibecase.MustParse("C4")
	if got != vibecase.CaseVideo {
		t.Fatalf("MustParse(C4) = %q, want C4", got)
	}
}

// TestIsValid is the boolean shortcut: must agree with Parse.
func TestIsValid(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"C1", true},
		{"C7", true},
		{"C8", false},
		{"c1", false},
		{"", false},
		{"  C1  ", true}, // IsValid also trims, mirrors Parse
	}
	for _, tc := range cases {
		if got := vibecase.IsValid(tc.in); got != tc.want {
			t.Errorf("IsValid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestString_RoundTrip confirms String() returns the canonical
// wire form for every canonical case.
func TestString_RoundTrip(t *testing.T) {
	for _, c := range vibecase.All() {
		s := c.String()
		got, err := vibecase.Parse(s)
		if err != nil {
			t.Errorf("String() -> Parse round-trip failed for %q: %v", c, err)
			continue
		}
		if got != c {
			t.Errorf("String() -> Parse round-trip: got %q, want %q", got, c)
		}
	}
}

// TestIsZero pins the empty-state discriminator.
func TestIsZero(t *testing.T) {
	if !vibecase.Case("").IsZero() {
		t.Errorf(`Case("").IsZero() should be true`)
	}
	if vibecase.CaseCode.IsZero() {
		t.Errorf(`CaseCode.IsZero() should be false`)
	}
}

// TestDescription_KnownCases confirms every canonical case has a
// non-empty description. (A regression that mapped a case to ""
// would silently break the LLM-facing context projection.)
//
// Description format: "<human label> — <body>." e.g.
//   "code — source code artifacts (functions, modules, services)."
// The canonical "C1" label is NOT in the description itself — it is
// a human-facing string, not a machine-readable one. Machine code
// uses Case.String(). Description() is for LLM-facing context only.
func TestDescription_KnownCases(t *testing.T) {
	for _, c := range vibecase.All() {
		d := vibecase.Description(c)
		if d == "" {
			t.Errorf("Description(%q) is empty", c)
		}
		if !strings.Contains(d, " — ") {
			t.Errorf("Description(%q) = %q, should contain an em-dash separator", c, d)
		}
		if !strings.HasSuffix(d, ".") {
			t.Errorf("Description(%q) = %q, should end with a period", c, d)
		}
	}
}

// TestDescription_UnknownFallback is a defensive guard: an unknown
// case (shouldn't happen post-Parse) gets a placeholder, not a panic
// or empty string.
func TestDescription_UnknownFallback(t *testing.T) {
	d := vibecase.Description(vibecase.Case("ZZZ"))
	if d == "" {
		t.Fatalf("Description(ZZZ) is empty")
	}
	if !strings.Contains(d, "ZZZ") {
		t.Errorf("Description(ZZZ) = %q, should mention the unknown label", d)
	}
}

// TestCardinality protects against accidental additions / removals.
// The taxonomy is exactly seven cases today. Adding C8 is a MINOR bump
// and should update this test alongside `all`.
func TestCardinality(t *testing.T) {
	if got := len(vibecase.All()); got != 7 {
		t.Errorf("len(All()) = %d, want 7 (C1..C7); adding C8 requires updating this test", got)
	}
	if got := len(vibecase.JSONSchemaEnum()); got != 7 {
		t.Errorf("len(JSONSchemaEnum()) = %d, want 7", got)
	}
}
