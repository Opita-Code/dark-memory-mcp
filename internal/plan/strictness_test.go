package plan

import (
	"strings"
	"testing"
)

// The strictness of the plan gate has a contract that is NOT the same
// as the drift one, and the difference is in the fallback:
//
//   - drift: unknown -> off (the check costs an LLM round-trip).
//   - plan:  unknown -> warn (the check is free and the switch exists
//     to MEASURE; a typo must not switch the measurement off).
//
// These tests pin that difference, because it is easy to "fix" it by
// copying the drift parser wholesale and silently break the KPI.
func TestParseStrictness_UnknownNeverBlocks(t *testing.T) {
	warned := false
	got := ParseStrictness("garbage", func(format string, args ...any) { warned = true })

	if got == StrictnessStrict {
		t.Fatal("an unknown value produced strict: a typo must never refuse a call")
	}
	if got != StrictnessWarn {
		t.Errorf("ParseStrictness(unknown) = %v, want %v (warn: the fallback keeps the measurement on)", got, StrictnessWarn)
	}
	if !warned {
		t.Error("an unknown value did not warn: the operator has to learn about the typo")
	}
}

func TestParseStrictness_EnvDefaultIsWarn(t *testing.T) {
	// An EMPTY env var is the default of the operator switch, and the
	// default is warn: the gate is born measuring.
	if got := ParseStrictness("", nil); got != StrictnessWarn {
		t.Errorf("ParseStrictness(\"\") = %v, want %v", got, StrictnessWarn)
	}
}

func TestParseStrictness_AcceptedValues(t *testing.T) {
	cases := []struct {
		in   string
		want Strictness
	}{
		{"off", StrictnessOff},
		{"OFF", StrictnessOff},
		{"  off  ", StrictnessOff},
		{"0", StrictnessOff},
		{"false", StrictnessOff},
		{"no", StrictnessOff},
		{"warn", StrictnessWarn},
		{"WARN", StrictnessWarn},
		{"1", StrictnessWarn},
		{"true", StrictnessWarn},
		{"yes", StrictnessWarn},
		{"strict", StrictnessStrict},
		{"STRICT", StrictnessStrict},
		{"2", StrictnessStrict},
	}
	for _, c := range cases {
		if got := ParseStrictness(c.in, nil); got != c.want {
			t.Errorf("ParseStrictness(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The zero value of the TYPE is off, so a Strictness built without
// asking the environment does not start refusing calls. That is the
// opposite of the env default on purpose: the type's zero value
// belongs to a caller that never opted in, and the env default
// belongs to an operator who installed the switch.
func TestStrictness_ZeroValueIsOff(t *testing.T) {
	var s Strictness
	if s != StrictnessOff {
		t.Errorf("the zero value of Strictness = %v, want %v", s, StrictnessOff)
	}
}

func TestStrictness_String(t *testing.T) {
	cases := map[Strictness]string{
		StrictnessOff:    "off",
		StrictnessWarn:   "warn",
		StrictnessStrict: "strict",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Strictness(%d).String() = %q, want %q", int(s), got, want)
		}
	}
	// An out-of-range value does NOT panic and does NOT return an
	// empty string: an operator reading a log has to be able to see
	// that something was wrong.
	if got := Strictness(99).String(); !strings.HasPrefix(got, "unknown") {
		t.Errorf("Strictness(99).String() = %q, want something starting with \"unknown\"", got)
	}
}
