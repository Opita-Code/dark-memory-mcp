// Package tools — vibe_compile.go: the dark_memory_vibe_compile tool.
//
// Per Phase 22 alpha.32 B2 spec (atomic mirror row 2707): compiles a
// persisted *vibeflow.Spec into an executable bash script that drives the
// 6-phase vibe-loop-git protocol with operator-approval gates between
// phases. Bash is the artifact (auditable, replay-able, deterministic)
// — NOT a new binary. This honors SKILL.md:59 ("every loop requires
// operator approval") and mod.json:56 ("instrument → measure → decide →
// flip, not wiring").
//
// Wire shape:
//
//	dark_memory_vibe_compile(spec_id, options)
//
//	  spec_id: int64   — vibe_specs.id to compile
//	  options.format:   "bash" (default) | "ps1" | "json"
//	  options.include_osint:              bool (default true)
//	  options.include_artifact_template:  bool (default true)
//
// Returns: { script: string, content_type: string, sha256: string,
//           loop_summary: { vibe_case, task_count, phases: [...] } }
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

//go:embed templates/vibe_loop.sh.tmpl
var vibeLoopBashTemplate string

// SpecProvider is the minimal accessor compileVibe needs. Production
// wires it from store.Store.GetSpec; tests inject a closure backed by
// a fixture. Decoupling from the full Store interface keeps the test
// surface small (no 50-method fake required) and the handler honest
// about its only dependency.
type SpecProvider func(ctx context.Context, id int64) (*vibeflow.Spec, error)

// CompileInput is the input for dark_memory_vibe_compile.
type CompileInput struct {
	// SpecID is the vibe_specs.id to compile. Required.
	SpecID int64 `json:"spec_id"`
	// Options controls output shape. All fields optional.
	Options *CompileOptions `json:"options,omitempty"`
}

// CompileOptions tunes the compiled artifact.
type CompileOptions struct {
	// Format selects the output format. Default "bash".
	// Other values: "ps1" (reserved — returns ErrInvalidArgument),
	// "json" (reserved — returns ErrInvalidArgument).
	Format string `json:"format,omitempty"`
	// IncludeOSINT controls whether phase 1 (OSINT) is rendered. Default true.
	// When false, phase 1 becomes a no-op echo.
	IncludeOSINT *bool `json:"include_osint,omitempty"`
	// IncludeArtifactTemplate controls whether phase 3 (ARTIFACT) prints
	// a TODO skeleton for the artifact body. Default true.
	IncludeArtifactTemplate *bool `json:"include_artifact_template,omitempty"`
}

// CompileResult is the output of dark_memory_vibe_compile.
type CompileResult struct {
	// Script is the generated bash source (or empty for unsupported formats).
	Script string `json:"script"`
	// ContentType is "text/x-shellscript" for bash, or "" for unsupported.
	ContentType string `json:"content_type"`
	// SHA256 is the hex SHA-256 of Script. Stable across calls when the
	// spec + options are stable (the compiler is deterministic).
	SHA256 string `json:"sha256"`
	// LoopSummary is a short description of the loop the script will drive.
	LoopSummary CompileLoopSummary `json:"loop_summary"`
}

// CompileLoopSummary describes the loop the compiled script will drive.
type CompileLoopSummary struct {
	// SpecID echoes the input so the operator can cross-check.
	SpecID int64 `json:"spec_id"`
	// VibeCase is the spec's vibe_case (C1..C8).
	VibeCase string `json:"vibe_case"`
	// TaskCount is len(Spec.Tasks parsed).
	TaskCount int `json:"task_count"`
	// PhaseCount is the number of protocol phases the script will drive.
	// Always 6 today; reserved for future Loop 7+ variants.
	PhaseCount int `json:"phase_count"`
}

// RegisterVibeCompile wires dark_memory_vibe_compile into the VIBE namespace.
// Called from RegisterVibe (vibe.go) — keeps all VIBE tools in one place.
func RegisterVibeCompile(reg *Registry, st store.Store) {
	reg.Add(BindStore("vibe_compile",
		"Compile a persisted vibe_spec into an executable bash script that drives the 6-phase vibe-loop-git protocol (OSINT → SPEC → ARTIFACT → DRIFT_JUDGE → RESOLVE_DRIFT → ATOMIC_MIRROR) with operator-approval gates between phases. Bash is the artifact — auditable, replay-able, deterministic. Honors 'every loop requires operator approval' (SKILL.md:59).",
		MustJSONSchema(map[string]any{
			"type":                 "object",
			"required":             []string{"spec_id"},
			"additionalProperties": false,
			"properties": map[string]any{
				"spec_id": map[string]any{
					"type":        "integer",
					"description": "vibe_specs.id to compile.",
				},
				"options": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"format": map[string]any{
							"type":        "string",
							"enum":        []string{"bash", "ps1", "json"},
							"default":     "bash",
							"description": "Output format. Only 'bash' implemented in 22.B; ps1+json return ErrInvalidArgument.",
						},
						"include_osint": map[string]any{
							"type":        "boolean",
							"default":     true,
							"description": "Whether to render phase 1 (OSINT). When false, phase 1 becomes a no-op.",
						},
						"include_artifact_template": map[string]any{
							"type":        "boolean",
							"default":     true,
							"description": "Whether phase 3 (ARTIFACT) prints a TODO skeleton for the artifact body.",
						},
					},
				},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in CompileInput) (*CompileResult, error) {
			return compileVibe(ctx, s.GetSpec, in)
		}))
}

// compileVibe is the handler body. Split out for testability.
func compileVibe(ctx context.Context, getSpec SpecProvider, in CompileInput) (*CompileResult, error) {
	// Resolve options with defaults.
	opts := CompileOptions{}
	if in.Options != nil {
		opts = *in.Options
	}
	format := strings.TrimSpace(opts.Format)
	if format == "" {
		format = "bash"
	}
	includeOSINT := true
	if opts.IncludeOSINT != nil {
		includeOSINT = *opts.IncludeOSINT
	}
	includeArtifactTemplate := true
	if opts.IncludeArtifactTemplate != nil {
		includeArtifactTemplate = *opts.IncludeArtifactTemplate
	}

	// Only bash is implemented in 22.B. ps1/json return a clean
	// ErrInvalidArgument so the operator learns fast.
	if format != "bash" {
		return nil, fmt.Errorf("vibe_compile: format=%q not implemented yet (only \"bash\" shipped in 22.B; ps1/json reserved for 22.C follow-up): %w", format, store.ErrInvalidArgument)
	}

	// Load the spec from the store.
	if in.SpecID <= 0 {
		return nil, fmt.Errorf("vibe_compile: spec_id must be > 0: %w", store.ErrInvalidArgument)
	}
	sp, err := getSpec(ctx, in.SpecID)
	if err != nil {
		return nil, fmt.Errorf("vibe_compile: get spec_id=%d: %w", in.SpecID, err)
	}
	if sp == nil {
		return nil, fmt.Errorf("vibe_compile: spec_id=%d not found in vibe_specs: %w", in.SpecID, store.ErrInvalidArgument)
	}

	// Parse tasks for the summary. We do NOT use them to drive phase bodies
	// (phases are protocol-driven); we surface the count and ids only.
	taskCount := 0
	if strings.TrimSpace(sp.Tasks) != "" {
		var tasks []orchestration.VibeSpecTask
		if err := json.Unmarshal([]byte(sp.Tasks), &tasks); err == nil {
			taskCount = len(tasks)
		}
		// Non-fatal: malformed tasks JSON does not block compilation.
		// The operator can still run the script with task_count=0.
	}

	// Render the bash template.
	script, err := renderVibeBashTemplate(vibeBashData{
		SpecID:                  sp.ID,
		VibeCase:                sp.VibeCase,
		Constitution:             sp.Constitution,
		Spec:                    sp.Spec,
		IncludeOSINT:            includeOSINT,
		IncludeArtifactTemplate: includeArtifactTemplate,
	})
	if err != nil {
		return nil, fmt.Errorf("vibe_compile: render template: %w", err)
	}

	// SHA-256 the rendered script. Deterministic — same spec+options yields
	// the same hash, so the operator can cache compiled scripts by hash.
	sum := sha256.Sum256([]byte(script))
	hexsum := hex.EncodeToString(sum[:])

	return &CompileResult{
		Script:      script,
		ContentType: "text/x-shellscript",
		SHA256:      hexsum,
		LoopSummary: CompileLoopSummary{
			SpecID:     sp.ID,
			VibeCase:   sp.VibeCase,
			TaskCount:  taskCount,
			PhaseCount: 6,
		},
	}, nil
}

// vibeBashData is the template context for the embedded bash template.
// Fields are passed through %q escaping to be shell-safe.
type vibeBashData struct {
	SpecID                  int64
	VibeCase                string
	Constitution            string
	Spec                    string
	IncludeOSINT            bool
	IncludeArtifactTemplate bool
}

// renderVibeBashTemplate parses the embedded bash template and renders
// it with data. The template uses Go text/template — {{bashEscape .X}}
// is the canonical bash-safe escape (single-quoted, no $() or backtick
// expansion). Parsing happens per call so a template syntax error
// surfaces at the first compile request, not at boot (the template is
// small enough that the cost is negligible).
func renderVibeBashTemplate(data vibeBashData) (string, error) {
	funcs := template.FuncMap{
		// bashEscape wraps a string in single quotes with safe
		// escaping for embedded single quotes (POSIX shell rule:
		// you can't escape a single quote inside single quotes —
		// close, escape, reopen). Result: no $(), ${}, backtick,
		// or \\ expansion possible; the value is literal data.
		"bashEscape": func(s string) string {
			return bashQuote(s)
		},
	}
	t, err := template.New("vibe_loop").Funcs(funcs).Parse(vibeLoopBashTemplate)
	if err != nil {
		return "", fmt.Errorf("parse bash template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute bash template: %w", err)
	}
	return buf.String(), nil
}

// bashQuote produces a POSIX-shell single-quoted form of s. Single
// quotes inside the input are escaped via the standard "close, escape,
// reopen" idiom (''\''). The result is safe to embed in any bash
// context — no command substitution, no variable expansion, no path
// expansion can occur.
func bashQuote(s string) string {
	// Fast path: no single quotes — wrap and return.
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	// Slow path: replace each ' with '\'' (close + escaped quote + reopen).
	var b strings.Builder
	b.Grow(len(s) + 8)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b.WriteString("'\\''")
		} else {
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// errInvalidArgumentTool removed — use store.ErrInvalidArgument directly.
// F35 wire-propagation: errors.Is(err, store.ErrInvalidArgument) is
// translated by the mcp-go adapter (server.go) into a
// ToolError{Code:"ErrInvalidArgument"} instead of the default
// ErrInternal. See orchestration/vibe_spec.go for the canonical
// wrapping pattern (NewFieldError(store.ErrInvalidArgument, ...)).