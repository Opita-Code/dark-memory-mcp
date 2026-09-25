package research

import (
	"testing"

	"pgregory.net/rapid"
)

// testHelper mirrors *testing.T and *rapid.T so properties run under
// both runners without duplication.
type testHelper interface {
	Helper()
	Fatalf(string, ...any)
}

func TestProperty_ParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		idx := rapid.IntRange(0, len(canonicalIntents)-1).Draw(t, "idx")
		raw := string(canonicalIntents[idx])
		got, err := ParseIntent(raw)
		if err != nil {
			t.Fatalf("ParseIntent(%q): %v", raw, err)
		}
		if got != canonicalIntents[idx] {
			t.Fatalf("round trip: got %q want %q", got, canonicalIntents[idx])
		}
	})
}

func TestProperty_ValidAlwaysParses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		i := rapid.SampledFrom(canonicalIntents).Draw(t, "intent")
		if !IsValidIntent(i) {
			t.Fatalf("canonical %q not valid", i)
		}
		if _, err := ParseIntent(string(i)); err != nil {
			t.Fatalf("canonical %q does not parse: %v", i, err)
		}
	})
}

func TestProperty_EmptyNeverParses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ws := rapid.StringMatching(`^[ \t\n]*$`).Draw(t, "ws")
		if _, err := ParseIntent(ws); err == nil {
			t.Fatalf("whitespace %q parsed without error", ws)
		}
	})
}

func TestProperty_CaseInsensitiveParse(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		i := rapid.SampledFrom(canonicalIntents).Draw(t, "intent")
		upper := ""
		for _, r := range string(i) {
			if rapid.Bool().Draw(t, "up") && r >= 'a' && r <= 'z' {
				upper += string(r - 'a' + 'A')
			} else {
				upper += string(r)
			}
		}
		got, err := ParseIntent(upper)
		if err != nil {
			t.Fatalf("ParseIntent(%q): %v", upper, err)
		}
		if got != i {
			t.Fatalf("case-insensitive: got %q want %q", got, i)
		}
	})
}
