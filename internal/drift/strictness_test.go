// Package drift — strictness_test.go: covers ParseStrictness
// env-string mapping + StrictnessFromEnv (the boot-time wrapper).
package drift

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseStrictness_DefaultOff(t *testing.T) {
	cases := []struct {
		env  string
		want Strictness
	}{
		// Off family.
		{"", StrictnessOff},
		{"off", StrictnessOff},
		{"OFF", StrictnessOff},
		{"  off  ", StrictnessOff},
		{"0", StrictnessOff},
		{"false", StrictnessOff},
		{"no", StrictnessOff},

		// Warn family.
		{"warn", StrictnessWarn},
		{"WARN", StrictnessWarn},
		{"  warn  ", StrictnessWarn},
		{"1", StrictnessWarn},
		{"true", StrictnessWarn},
		{"yes", StrictnessWarn},

		// Strict family.
		{"strict", StrictnessStrict},
		{"STRICT", StrictnessStrict},
		{"  strict  ", StrictnessStrict},
		{"2", StrictnessStrict},
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			got := ParseStrictness(c.env, nil)
			if got != c.want {
				t.Errorf("ParseStrictness(%q) = %v, want %v", c.env, got, c.want)
			}
		})
	}
}

func TestParseStrictness_InvalidFallsBackToOff(t *testing.T) {
	// Capture the warning message via the warnf callback so the
	// defensive fallback is observable.
	var warnings []string
	warnf := func(format string, args ...any) {
		// Format the args into a single message so the test can match
		// against the resolved value, not the raw %q format placeholder.
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	cases := []string{
		"draconian",     // unknown string
		"3",             // unknown numeric
		"on",            // ambiguous boolean (NOT in canonical set)
		"strict-ish",    // malformed strict
	}
	for _, c := range cases {
		t.Run("env="+c, func(t *testing.T) {
			warnings = warnings[:0]
			got := ParseStrictness(c, warnf)
			if got != StrictnessOff {
				t.Errorf("ParseStrictness(%q) = %v, want StrictnessOff (defensive fallback)", c, got)
			}
			if len(warnings) != 1 {
				t.Errorf("expected exactly 1 warning for invalid env=%q, got %d", c, len(warnings))
			} else if !strings.Contains(warnings[0], c) {
				t.Errorf("warning should mention the invalid value %q; got %q", c, warnings[0])
			}
		})
	}
}

func TestStrictnessFromEnv_ReadsEnv(t *testing.T) {
	cases := []struct {
		env  string
		want Strictness
	}{
		{"off", StrictnessOff},
		{"warn", StrictnessWarn},
		{"strict", StrictnessStrict},
		{"", StrictnessOff},
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv("DARK_DRIFT_STRICTNESS", c.env)
			got := StrictnessFromEnv()
			if got != c.want {
				t.Errorf("StrictnessFromEnv (DARK_DRIFT_STRICTNESS=%q) = %v, want %v",
					c.env, got, c.want)
			}
		})
	}
}

func TestStrictness_String(t *testing.T) {
	cases := []struct {
		s    Strictness
		want string
	}{
		{StrictnessOff, "off"},
		{StrictnessWarn, "warn"},
		{StrictnessStrict, "strict"},
		{Strictness(99), "unknown(99)"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := c.s.String()
			if got != c.want {
				t.Errorf("Strictness(%d).String() = %q, want %q", int(c.s), got, c.want)
			}
		})
	}
}

// TestResolveStrictness_PerProjectOverride (Wave 5X.3): exercises
// the resolution chain. Empty / "default" / invalid → use env.
// Valid override → use override.
func TestResolveStrictness_PerProjectOverride(t *testing.T) {
	cases := []struct {
		name           string
		projectValue   string
		envValue       Strictness
		want           Strictness
		expectWarning  bool
	}{
		// Empty / "default" → use env.
		{"empty + env=off", "", StrictnessOff, StrictnessOff, false},
		{"default + env=off", "default", StrictnessOff, StrictnessOff, false},
		{"empty + env=warn", "", StrictnessWarn, StrictnessWarn, false},
		{"empty + env=strict", "", StrictnessStrict, StrictnessStrict, false},

		// Valid override → use override (env ignored).
		{"override=off + env=strict", "off", StrictnessStrict, StrictnessOff, false},
		{"override=warn + env=off", "warn", StrictnessOff, StrictnessWarn, false},
		{"override=strict + env=warn", "strict", StrictnessWarn, StrictnessStrict, false},

		// Case-insensitive.
		{"override=STRICT + env=off", "STRICT", StrictnessOff, StrictnessStrict, false},
		{"override=Off + env=strict", "Off", StrictnessStrict, StrictnessOff, false},

		// Invalid → fallback to env + warning.
		{"override=garbage + env=warn", "garbage", StrictnessWarn, StrictnessWarn, true},
		{"override=on + env=strict", "on", StrictnessStrict, StrictnessStrict, true}, // "on" is ambiguous; warn + fallback
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var warnings []string
			warnf := func(format string, args ...any) {
				warnings = append(warnings, format)
			}
			got := ResolveStrictness(c.projectValue, c.envValue, warnf)
			if got != c.want {
				t.Errorf("ResolveStrictness(project=%q env=%v) = %v, want %v",
					c.projectValue, c.envValue, got, c.want)
			}
			if c.expectWarning && len(warnings) == 0 {
				t.Errorf("expected warning for project=%q, got none", c.projectValue)
			}
			if !c.expectWarning && len(warnings) > 0 {
				t.Errorf("did not expect warning for project=%q, got %d", c.projectValue, len(warnings))
			}
		})
	}
}

// TestResolveStrictness_NilWarnf_DoesNotPanic: the callback is
// optional. Defensive path — nil callback means no warning even
// on invalid input.
func TestResolveStrictness_NilWarnf_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResolveStrictness panicked with nil warnf: %v", r)
		}
	}()
	got := ResolveStrictness("garbage", StrictnessOff, nil)
	if got != StrictnessOff {
		t.Errorf("ResolveStrictness(garbage, off, nil) = %v, want off (fallback)", got)
	}
}