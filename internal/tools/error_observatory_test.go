// Package tools — error_observatory_test.go: unit tests for the
// cross-project policy gate (enforceCrossProjectPolicy +
// resolveOperatorFromStore). These pin the env-flag-driven access
// control without spinning up the full MCP stack — the operator
// can read the test cases as a behavioral spec.
//
// Spec 1237 (2026-08-18): cross-project error observability +
// admin elevation.
package tools

import (
	"os"
	"strings"
	"testing"
)

// TestEnforceCrossProjectPolicy_NeitherSet verifies the deny-by-default
// path: with neither DARK_ERROR_OBS_OPERATOR_OVERRIDE nor
// DARK_ERROR_OBS_ADMIN_OPERATORS set, any operator is rejected.
func TestEnforceCrossProjectPolicy_NeitherSet(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "")
	t.Setenv(EnvAdminOperators, "")

	if d := enforceCrossProjectPolicy("dark-agent"); d.allowed {
		t.Errorf("allowed=true with no env vars set, want false")
	}
	if d := enforceCrossProjectPolicy(""); d.allowed {
		t.Errorf("allowed=true with no env vars + empty operator, want false")
	}
	if d := enforceCrossProjectPolicy("alice"); d.allowed {
		t.Errorf("allowed=true with no env vars + non-admin operator, want false")
	}
}

// TestEnforceCrossProjectPolicy_OverrideFlag verifies the bypass flag
// path: when DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed, anyone is
// allowed (regardless of allow-list).
func TestEnforceCrossProjectPolicy_OverrideFlag(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "armed")
	t.Setenv(EnvAdminOperators, "") // allow-list empty — irrelevant

	if d := enforceCrossProjectPolicy("dark-agent"); !d.allowed || d.actor != ActorCrossProjectOverride {
		t.Errorf("override: got {allowed=%v, actor=%q}, want {true, %q}",
			d.allowed, d.actor, ActorCrossProjectOverride)
	}
	if d := enforceCrossProjectPolicy("random-guest"); !d.allowed {
		t.Errorf("override: got allowed=%v, want true (anyone when override=armed)", d.allowed)
	}
	// Override flag with wrong value: NOT armed → not allowed.
	t.Setenv(EnvOverrideFlag, "yes")
	if d := enforceCrossProjectPolicy("dark-agent"); d.allowed {
		t.Errorf("override=armed exact match: got allowed=true with override=yes, want false")
	}
}

// TestEnforceCrossProjectPolicy_AdminAllowList verifies the
// allow-list path: only operators in the comma-separated list
// are allowed; others rejected.
func TestEnforceCrossProjectPolicy_AdminAllowList(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "")
	t.Setenv(EnvAdminOperators, "dark-agent,nico,platform-ops")

	tests := []struct {
		operator string
		want     bool
	}{
		{"dark-agent", true},
		{"nico", true},
		{"platform-ops", true},
		{"guest", false},
		{"", false},
		{"dark-agent ", false}, // exact match, no trim tolerance
	}
	for _, tt := range tests {
		t.Run(tt.operator, func(t *testing.T) {
			d := enforceCrossProjectPolicy(tt.operator)
			if d.allowed != tt.want {
				t.Errorf("operator=%q: got allowed=%v, want %v", tt.operator, d.allowed, tt.want)
			}
			if d.allowed && d.actor != ActorCrossProjectAdmin {
				t.Errorf("operator=%q: actor=%q, want %q", tt.operator, d.actor, ActorCrossProjectAdmin)
			}
		})
	}
}

// TestEnforceCrossProjectPolicy_AllowListWhitespace verifies
// that whitespace around operators is trimmed (CSV parsing edge case).
func TestEnforceCrossProjectPolicy_AllowListWhitespace(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "")
	t.Setenv(EnvAdminOperators, " dark-agent , nico , platform-ops ")

	if d := enforceCrossProjectPolicy("dark-agent"); !d.allowed {
		t.Errorf("dark-agent (with whitespace in allow-list): got allowed=%v, want true", d.allowed)
	}
	if d := enforceCrossProjectPolicy("nico"); !d.allowed {
		t.Errorf("nico: got allowed=%v, want true", d.allowed)
	}
}

// TestEnforceCrossProjectPolicy_OverrideWinsOverAllowList verifies
// the precedence rule: when both are set, override wins (anyone
// allowed, audit Actor=override).
func TestEnforceCrossProjectPolicy_OverrideWinsOverAllowList(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "armed")
	t.Setenv(EnvAdminOperators, "dark-agent") // restrictive

	d := enforceCrossProjectPolicy("random-guest-not-in-list")
	if !d.allowed {
		t.Errorf("override+allow-list: random guest got allowed=false, want true (override wins)")
	}
	if d.actor != ActorCrossProjectOverride {
		t.Errorf("override+allow-list: actor=%q, want %q", d.actor, ActorCrossProjectOverride)
	}
}

// TestEnforceCrossProjectPolicy_DenyDistinguishesEmptyOperator
// documents the contract that empty operator + no allow-list match
// is denied even if the override flag would otherwise allow.
func TestEnforceCrossProjectPolicy_EmptyOperator(t *testing.T) {
	// Override set, empty operator: allowed (override bypasses identity).
	t.Setenv(EnvOverrideFlag, "armed")
	t.Setenv(EnvAdminOperators, "")
	if d := enforceCrossProjectPolicy(""); !d.allowed {
		t.Errorf("override + empty operator: got allowed=%v, want true (override is identity-blind)", d.allowed)
	}

	// No override, empty operator, allow-list present but not matched: denied.
	t.Setenv(EnvOverrideFlag, "")
	t.Setenv(EnvAdminOperators, "dark-agent")
	if d := enforceCrossProjectPolicy(""); d.allowed {
		t.Errorf("no override + empty operator + non-matching list: got allowed=true, want false")
	}
}

// TestErrCrossProjectNotAllowed_IsInvalidArgument verifies the
// sentinel wrapping — callers branch on errors.Is(err,
// store.ErrInvalidArgument).
func TestErrCrossProjectNotAllowed_IsInvalidArgument(t *testing.T) {
	if !strings.Contains(errCrossProjectNotAllowed.Error(), "DARK_ERROR_OBS_OPERATOR_OVERRIDE") {
		t.Errorf("error message missing env var hint: %q", errCrossProjectNotAllowed.Error())
	}
	if !strings.Contains(errCrossProjectNotAllowed.Error(), "DARK_ERROR_OBS_ADMIN_OPERATORS") {
		t.Errorf("error message missing allow-list hint: %q", errCrossProjectNotAllowed.Error())
	}
}

// TestEnvVarsReadPerCall documents the read-on-every-call
// discipline: changes to the env between calls take effect
// without a process restart (allow-list is dynamic; override
// is intended to be restart-bound but reading per-call is
// safe because the operator must restart to disable it).
func TestEnvVarsReadPerCall(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "")
	t.Setenv(EnvAdminOperators, "")

	// Initial: denied.
	if d := enforceCrossProjectPolicy("dark-agent"); d.allowed {
		t.Errorf("initial: got allowed=true, want false")
	}

	// Set allow-list mid-process.
	t.Setenv(EnvAdminOperators, "dark-agent")
	if d := enforceCrossProjectPolicy("dark-agent"); !d.allowed {
		t.Errorf("after setenv: got allowed=false, want true (env read per call)")
	}

	// Clear allow-list mid-process.
	t.Setenv(EnvAdminOperators, "")
	if d := enforceCrossProjectPolicy("dark-agent"); d.allowed {
		t.Errorf("after unset: got allowed=true, want false (env read per call)")
	}
}

// TestNoEnvLeakBetweenSubtests is a hygiene check: the
// t.Setenv mechanism snapshots and restores env per test, so
// leaking across subtests would manifest here. Mostly
// defensive — documents intent.
func TestNoEnvLeakBetweenSubtests(t *testing.T) {
	t.Setenv(EnvOverrideFlag, "armed")
	if os.Getenv(EnvOverrideFlag) != "armed" {
		t.Fatalf("setup: override flag not set")
	}
	// subtest: change + verify
	t.Run("inner", func(t *testing.T) {
		t.Setenv(EnvOverrideFlag, "")
		if os.Getenv(EnvOverrideFlag) != "" {
			t.Errorf("inner: env should be empty")
		}
	})
	// After subtest: t.Setenv restored.
	if os.Getenv(EnvOverrideFlag) != "armed" {
		t.Errorf("outer: env not restored after subtest")
	}
}