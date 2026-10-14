// Package tools — vibe_compile_test.go
//
// Tests for dark_memory_vibe_compile (Phase 22 alpha.32 B2 Session 5bis,
// spec_id 1983, T6). Eight tests cover:
//   (a) RegisteredAndGranted  — canonicalNamespaces.VIBE + DefaultToolGrants
//   (b) TemplateRendering     — basic template render contains expected markers
//   (c) TemplateEscaping      — %q escaping for shell-metacharacter strings
//   (d) PhaseFunctions        — all 6 phase functions present in output
//   (e) ApprovalGate          — __dark_mem_pause_for_approval shim present
//   (f) ResolvesSpec          — SpecProvider closure feeds spec into render
//   (g) MissingSpec           — spec_id=0 or nil spec → ErrInvalidArgument
//   (h) OptionsFormat         — format=ps1/json, include_osint/include_artifact_template
//
// All tests are pure (no I/O, no DB). The SpecProvider closure pattern lets
// us avoid the 50-method Store fake.
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// (a) TestVibeCompile_RegisteredAndGranted — vibe_compile is in VIBE namespace
// AND in DefaultToolGrants. Without both, the gate refuses the call.
func TestVibeCompile_RegisteredAndGranted(t *testing.T) {
	// Check canonicalNamespaces.VIBE contains vibe_compile.
	found := false
	for _, ns := range canonicalNamespaces {
		if ns.Name == "VIBE" {
			for _, tn := range ns.Tools {
				if tn == "vibe_compile" {
					found = true
					break
				}
			}
			break
		}
	}
	if !found {
		t.Fatalf("vibe_compile not found in canonicalNamespaces.VIBE")
	}

	// Check DefaultToolGrants contains vibe_compile.
	grants := strings.Split(recall.DefaultToolGrants, ",")
	foundInGrants := false
	for _, g := range grants {
		if strings.TrimSpace(g) == "vibe_compile" {
			foundInGrants = true
			break
		}
	}
	if !foundInGrants {
		t.Fatalf("vibe_compile not found in DefaultToolGrants")
	}
}

// (b) TestVibeCompile_TemplateRendering — render the bash template with
// minimal data and verify the output contains the expected markers
// (shebang, spec_id echo, 6 phase functions, approval gate).
func TestVibeCompile_TemplateRendering(t *testing.T) {
	script, err := renderVibeBashTemplate(vibeBashData{
		SpecID:                  1983,
		VibeCase:                "C1",
		Constitution:            "",
		Spec:                    "Sesión 5bis smoke",
		IncludeOSINT:            true,
		IncludeArtifactTemplate: true,
	})
	if err != nil {
		t.Fatalf("renderVibeBashTemplate: %v", err)
	}
	wantMarkers := []string{
		"#!/usr/bin/env bash",
		"set -euo pipefail",
		"SPEC_ID=1983",
		"VIBE_CASE=",
		"phase_osint",
		"phase_spec",
		"phase_artifact",
		"phase_drift_judge",
		"phase_resolve_drift",
		"phase_atomic_mirror",
		"__dark_mem_pause_for_approval",
		"main",
	}
	for _, m := range wantMarkers {
		if !strings.Contains(script, m) {
			t.Errorf("missing marker %q in rendered script", m)
		}
	}
}

// (c) TestVibeCompile_TemplateEscaping — shell metacharacters in spec
// fields must be safely escaped in the rendered bash output. The
// template uses {{bashEscape .X}} (POSIX single-quote escaping) for
// user-controlled strings. We verify:
//   (1) bashQuote produces safe single-quoted output for the basic
//       cases (no chars, single quotes, $(), backticks, semicolons)
//   (2) The template source uses {{bashEscape ...}} for the user-
//       controlled fields (so future edits can't silently remove
//       escaping without this test failing)
//   (3) The rendered output for a malicious input contains the
//       dangerous substring inside a single-quoted region (bash treats
//       it as literal data, no expansion).
func TestVibeCompile_TemplateEscaping(t *testing.T) {
	// (1) bashQuote unit cases.
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "''"},
		{"plain", "hello", "'hello'"},
		{"single_quote", "ab'cd", `'ab'\''cd'`},
		{"two_quotes", "'a'", `''\''a'\'''`},
		{"dollar_subst", "$(id)", "'$(id)'"},
		{"backtick", "`whoami`", "'`whoami`'"},
		{"all_metachars", "'; rm -rf /; `whoami`; $(id) #", `''\''; rm -rf /; ` + "`whoami`" + `; $(id) #'`},
	}
	for _, tc := range cases {
		t.Run("bashQuote/"+tc.name, func(t *testing.T) {
			got := bashQuote(tc.in)
			if got != tc.want {
				t.Errorf("bashQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Every output must start with ' and end with '.
			if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
				t.Errorf("bashQuote(%q) = %q not wrapped in single quotes", tc.in, got)
			}
		})
	}

	// (2) Template source uses bashEscape for VibeCase/Constitution/Spec.
	for _, marker := range []string{
		`{{bashEscape .Constitution}}`,
		`{{bashEscape .Spec}}`,
		`{{bashEscape .VibeCase}}`,
	} {
		if !strings.Contains(vibeLoopBashTemplate, marker) {
			t.Errorf("template missing bashEscape directive %q; user-controlled fields would be unescaped in bash output", marker)
		}
	}

	// (3) End-to-end render with malicious content — verify the rendered
	//     script never has the malicious MARKER in a raw, unquoted
	//     context (i.e., NOT preceded by `'` which would mean it's
	//     inside single quotes — bash treats that as literal data).
	//     Use a unique marker (XYZZY_INJECT_MARKER) to make sure we're
	//     matching the rendered content, not the template's own
	//     documentation comments (which mention "bash-safe", "single-
	//     quoted", etc.).
	malicious := "XYZZY_INJECT_MARKER'; rm -rf /; `whoami`; $(id) #"
	script, err := renderVibeBashTemplate(vibeBashData{
		SpecID:       42,
		VibeCase:     "C1",
		Constitution: malicious,
		Spec:         malicious,
	})
	if err != nil {
		t.Fatalf("renderVibeBashTemplate: %v", err)
	}
	// Look for the dangerous marker — it MUST be inside single quotes.
	idx := strings.Index(script, "XYZZY_INJECT_MARKER")
	if idx < 0 {
		t.Fatalf("malicious marker not found in rendered output; cannot verify escaping")
	}
	prev := byte(0)
	if idx > 0 {
		prev = script[idx-1]
	}
	if prev != '\'' {
		t.Errorf("dangerous marker at index %d is NOT inside a single-quoted region (prev byte = %q); "+
			"template escaping failed; the marker was rendered as a raw token",
		idx, prev)
	}
}

// (d) TestVibeCompile_PhaseFunctions — the 6 protocol phases must each
// have a top-level function definition (not just a comment reference).
func TestVibeCompile_PhaseFunctions(t *testing.T) {
	script, err := renderVibeBashTemplate(vibeBashData{
		SpecID: 1, VibeCase: "C1", IncludeOSINT: true, IncludeArtifactTemplate: true,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, fn := range []string{"phase_osint()", "phase_spec()", "phase_artifact()", "phase_drift_judge()", "phase_resolve_drift()", "phase_atomic_mirror()"} {
		if !strings.Contains(script, fn) {
			t.Errorf("missing function definition for %s", fn)
		}
	}
}

// (e) TestVibeCompile_ApprovalGate — the __dark_mem_pause_for_approval
// shim function must be defined AND called between phases.
func TestVibeCompile_ApprovalGate(t *testing.T) {
	script, err := renderVibeBashTemplate(vibeBashData{
		SpecID: 1, VibeCase: "C1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Definition: shim function.
	if !strings.Contains(script, "__dark_mem_pause_for_approval() {") {
		t.Errorf("missing approval gate function definition")
	}
	// Behavior: y/n/revise/abort choices.
	for _, choice := range []string{"y|yes|Y", "n|N)", "revise)", "exit 1", "exit 2"} {
		if !strings.Contains(script, choice) {
			t.Errorf("missing approval gate branch %q", choice)
		}
	}
	// Usage: called 5 times between the 6 phases (after phase 1..5).
	count := strings.Count(script, "__dark_mem_pause_for_approval ")
	if count < 5 {
		t.Errorf("approval gate should be called at least 5 times (between phases); got %d", count)
	}
}

// (f) TestVibeCompile_ResolvesSpec — SpecProvider closure feeds the spec
// into the render. Verify: compileVibe reads the spec, populates the
// loop summary, returns a stable SHA-256 of the rendered script.
func TestVibeCompile_ResolvesSpec(t *testing.T) {
	// Mock spec — use the canonical Spec struct shape.
	mockSpec := &vibeflow.Spec{
		ID:        1983,
		VibeCase:  "C1",
		Spec:      "Sesión 5bis smoke",
		Tasks:     `[{"id":"T1","description":"compiler foundation"}]`,
		CreatedAt: "2026-10-14T00:00:00Z",
	}
	provider := func(ctx context.Context, id int64) (*vibeflow.Spec, error) {
		if id != 1983 {
			t.Errorf("provider called with id=%d, want 1983", id)
		}
		return mockSpec, nil
	}
	result, err := compileVibe(context.Background(), provider, CompileInput{
		SpecID: 1983,
	})
	if err != nil {
		t.Fatalf("compileVibe: %v", err)
	}
	if result.LoopSummary.SpecID != 1983 {
		t.Errorf("LoopSummary.SpecID = %d, want 1983", result.LoopSummary.SpecID)
	}
	if result.LoopSummary.VibeCase != "C1" {
		t.Errorf("LoopSummary.VibeCase = %q, want C1", result.LoopSummary.VibeCase)
	}
	if result.LoopSummary.TaskCount != 1 {
		t.Errorf("LoopSummary.TaskCount = %d, want 1 (parsed from mock Tasks)", result.LoopSummary.TaskCount)
	}
	if result.LoopSummary.PhaseCount != 6 {
		t.Errorf("LoopSummary.PhaseCount = %d, want 6", result.LoopSummary.PhaseCount)
	}
	if result.ContentType != "text/x-shellscript" {
		t.Errorf("ContentType = %q, want text/x-shellscript", result.ContentType)
	}
	// SHA-256 should match sha256(script).
	sum := sha256.Sum256([]byte(result.Script))
	want := hex.EncodeToString(sum[:])
	if result.SHA256 != want {
		t.Errorf("SHA256 = %q, want %q", result.SHA256, want)
	}
	if !strings.Contains(result.Script, "phase_atomic_mirror") {
		t.Errorf("script missing phase_atomic_mirror; spec was not used")
	}
}

// (g) TestVibeCompile_MissingSpec — three sub-cases for "no spec":
//   - spec_id = 0 → ErrInvalidArgument
//   - spec_id = -1 → ErrInvalidArgument
//   - provider returns (nil, nil) → ErrInvalidArgument
func TestVibeCompile_MissingSpec(t *testing.T) {
	cases := []struct {
		name     string
		specID   int64
		provider SpecProvider
	}{
		{
			name:   "zero_spec_id",
			specID: 0,
			provider: func(ctx context.Context, id int64) (*vibeflow.Spec, error) {
				t.Errorf("provider should not be called for spec_id=0")
				return nil, nil
			},
		},
		{
			name:   "negative_spec_id",
			specID: -1,
			provider: func(ctx context.Context, id int64) (*vibeflow.Spec, error) {
				t.Errorf("provider should not be called for spec_id=-1")
				return nil, nil
			},
		},
		{
			name:   "nil_spec_returned",
			specID: 9999,
			provider: func(ctx context.Context, id int64) (*vibeflow.Spec, error) {
				return nil, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileVibe(context.Background(), tc.provider, CompileInput{
				SpecID: tc.specID,
			})
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !errors.Is(err, store.ErrInvalidArgument) {
				t.Errorf("expected ErrInvalidArgument, got %v", err)
			}
		})
	}
}

// (h) TestVibeCompile_OptionsFormat — combinations of Options.Format
// (default, ps1, json) and the boolean toggles IncludeOSINT /
// IncludeArtifactTemplate. Also: provider error surfaces as wrapped error.
func TestVibeCompile_OptionsFormat(t *testing.T) {
	provider := func(ctx context.Context, id int64) (*vibeflow.Spec, error) {
		return &vibeflow.Spec{ID: 1, VibeCase: "C1"}, nil
	}

	t.Run("default_format_is_bash", func(t *testing.T) {
		r, err := compileVibe(context.Background(), provider, CompileInput{SpecID: 1})
		if err != nil {
			t.Fatalf("compileVibe: %v", err)
		}
		if r.ContentType != "text/x-shellscript" {
			t.Errorf("default format ContentType = %q, want text/x-shellscript", r.ContentType)
		}
	})

	t.Run("format_ps1_unsupported", func(t *testing.T) {
		_, err := compileVibe(context.Background(), provider, CompileInput{
			SpecID:  1,
			Options: &CompileOptions{Format: "ps1"},
		})
		if err == nil {
			t.Fatal("expected error for format=ps1, got nil")
		}
		if !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("expected ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("format_json_unsupported", func(t *testing.T) {
		_, err := compileVibe(context.Background(), provider, CompileInput{
			SpecID:  1,
			Options: &CompileOptions{Format: "json"},
		})
		if err == nil {
			t.Fatal("expected error for format=json, got nil")
		}
		if !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("expected ErrInvalidArgument, got %v", err)
		}
	})

	t.Run("include_osint_false_disables_phase1", func(t *testing.T) {
		f := false
		r, err := compileVibe(context.Background(), provider, CompileInput{
			SpecID:  1,
			Options: &CompileOptions{IncludeOSINT: &f},
		})
		if err != nil {
			t.Fatalf("compileVibe: %v", err)
		}
		if !strings.Contains(r.Script, "OSINT disabled via options.include_osint=false") {
			t.Errorf("phase_osint should show disabled marker when include_osint=false")
		}
	})

	t.Run("include_artifact_template_false_disables_phase3_skeleton", func(t *testing.T) {
		f := false
		r, err := compileVibe(context.Background(), provider, CompileInput{
			SpecID:  1,
			Options: &CompileOptions{IncludeArtifactTemplate: &f},
		})
		if err != nil {
			t.Fatalf("compileVibe: %v", err)
		}
		if !strings.Contains(r.Script, "artifact template disabled") {
			t.Errorf("phase_artifact should show disabled marker when include_artifact_template=false")
		}
	})

	t.Run("provider_error_is_wrapped", func(t *testing.T) {
		errProvider := func(ctx context.Context, id int64) (*vibeflow.Spec, error) {
			return nil, errors.New("db connection refused")
		}
		_, err := compileVibe(context.Background(), errProvider, CompileInput{SpecID: 1})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "db connection refused") {
			t.Errorf("provider error not surfaced; got %v", err)
		}
	})

	// Verify deterministic output: same spec + same options → same SHA-256.
	t.Run("deterministic_output", func(t *testing.T) {
		r1, _ := compileVibe(context.Background(), provider, CompileInput{SpecID: 1})
		r2, _ := compileVibe(context.Background(), provider, CompileInput{SpecID: 1})
		if r1.SHA256 != r2.SHA256 {
			t.Errorf("non-deterministic SHA256: %s vs %s", r1.SHA256, r2.SHA256)
		}
	})
}

// Helper: silence unused import in case orchestration types are pruned.
// (orchestration import removed — not needed for these pure tests.)