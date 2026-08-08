// Package recall_test - assemble_pure_test.go: covers the pure
// helper functions in assemble.go (ParseTimestamp, ParseSDDVerdict,
// ParsePersonaFromConstitution, Truncate). The baseline gremlins run
// reported the recall package at ~0.9% coverage — these helpers are
// the cheap-to-test, high-value surface; the Store-bound frame
// compositors (StoreSource.*Frame) need a mock Store in a follow-up.
package recall_test

import (
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/recall"
)

func TestParseTimestamp(t *testing.T) {
	zero := time.Time{}
	cases := []struct {
		name string
		in   string
		want time.Time
	}{
		{"empty → zero", "", zero},
		{"RFC3339Nano", "2026-08-07T12:34:56.123456789Z", time.Date(2026, 8, 7, 12, 34, 56, 123456789, time.UTC)},
		{"RFC3339", "2026-08-07T12:34:56Z", time.Date(2026, 8, 7, 12, 34, 56, 0, time.UTC)},
		{"with offset normalized to UTC", "2026-08-07T14:34:56+02:00", time.Date(2026, 8, 7, 12, 34, 56, 0, time.UTC)},
		{"garbage → zero", "not-a-time", zero},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := recall.ParseTimestamp(c.in)
			if !got.Equal(c.want) {
				t.Errorf("recall.ParseTimestamp(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseSDDVerdict(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantV   string
		wantR   string
		wantErr bool
	}{
		{"empty → nil", "", "", "", false},
		{"aligned", `{"verdict":"aligned","reasoning":"ok"}`, "aligned", "ok", false},
		{"drift_detected", `{"verdict":"drift_detected"}`, "drift_detected", "", false},
		{"needs_human", `{"verdict":"needs_human","reasoning":"review"}`, "needs_human", "review", false},
		{"unknown verdict → nil", `{"verdict":"weird"}`, "", "", false},
		{"malformed JSON → error", `{not json`, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, r, err := recall.ParseSDDVerdict(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("recall.ParseSDDVerdict(%q): want error, got nil", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("recall.ParseSDDVerdict(%q): unexpected error %v", c.in, err)
			}
			if v != c.wantV || r != c.wantR {
				t.Errorf("recall.ParseSDDVerdict(%q) = (%q,%q), want (%q,%q)", c.in, v, r, c.wantV, c.wantR)
			}
		})
	}
}

func TestParsePersonaFromConstitution(t *testing.T) {
	defV, defC, defT := recall.DefaultVoice, recall.DefaultClaimsPolicy, recall.DefaultTone
	cases := []struct {
		name                string
		in                  string
		wantV, wantC, wantT string
	}{
		{"empty → defaults", "", defV, defC, defT},
		{"malformed JSON → defaults", `{bad`, defV, defC, defT},
		{"no persona key → defaults", `{"other":1}`, defV, defC, defT},
		{"full persona override", `{"persona":{"voice":"v","claims_policy":"c","tone":"t"}}`, "v", "c", "t"},
		{"partial persona keeps defaults", `{"persona":{"tone":"t"}}`, defV, defC, "t"},
		{"empty strings keep defaults", `{"persona":{"voice":"","claims_policy":"","tone":""}}`, defV, defC, defT},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, cl, tone := recall.ParsePersonaFromConstitution(c.in)
			if v != c.wantV || cl != c.wantC || tone != c.wantT {
				t.Errorf("recall.ParsePersonaFromConstitution(%q) = (%q,%q,%q), want (%q,%q,%q)",
					c.in, v, cl, tone, c.wantV, c.wantC, c.wantT)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under limit → unchanged", "hello", 10, "hello"},
		{"exactly limit → unchanged", "hello", 5, "hello"},
		{"over limit → ellipsis", "hello world", 8, "hello..."},
		{"max <= 3 → unchanged (no room for ellipsis)", "hello world", 3, "hello world"},
		{"empty string", "", 5, ""},
		{"max 4 → 1 char + ellipsis", "hello", 4, "h..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := recall.Truncate(c.s, c.max)
			if got != c.want {
				t.Errorf("recall.Truncate(%q, %d) = %q, want %q", c.s, c.max, got, c.want)
			}
		})
	}
}

// Ensure time import stays live for the zero-time comparisons.
var _ = time.Time{}
