// Package tools - session_envduration_test.go: covers envDuration,
// the env-var duration parser in session.go. The baseline mutation
// run (gremlins v0.6.0) reported LIVED CONDITIONALS_NEGATION at
// session.go:51 (`secs > 0` → `>= 0`) and session.go:54 (`d > 0` →
// `>= 0`) because NO test exercised the parser directly. The
// boundary rows below distinguish `<` from `<=` on both the numeric
// and Go-duration forms.
package tools

import (
	"testing"
	"time"
)

func TestEnvDuration(t *testing.T) {
	cases := []struct {
		name string
		env  string
		def  time.Duration
		want time.Duration
	}{
		{"empty → default", "", 30 * time.Minute, 30 * time.Minute},
		{"numeric seconds", "300", 30 * time.Minute, 300 * time.Second},
		{"numeric zero → default (strict > 0)", "0", 30 * time.Minute, 30 * time.Minute},
		{"numeric one", "1", 30 * time.Minute, 1 * time.Second},
		{"numeric negative → default", "-5", 30 * time.Minute, 30 * time.Minute},
		{"numeric garbage → default", "abc", 30 * time.Minute, 30 * time.Minute},
		{"go duration", "5m", 30 * time.Minute, 5 * time.Minute},
		{"go duration zero → default (strict > 0)", "0s", 30 * time.Minute, 30 * time.Minute},
		{"go duration negative → default", "-30s", 30 * time.Minute, 30 * time.Minute},
		{"go duration garbage → default", "nonsense", 30 * time.Minute, 30 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DARK_TEST_ENV_DURATION", c.env)
			got := envDuration("DARK_TEST_ENV_DURATION", c.def)
			if got != c.want {
				t.Errorf("envDuration(%q, %v) = %v, want %v", c.env, c.def, got, c.want)
			}
		})
	}
}
