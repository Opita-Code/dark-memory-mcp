// Package vibecase defines the canonical C1..C7 case taxonomy for the
// vibe-loop. It is the single source of truth for what cases exist and
// what they mean. JSON Schema enums, orchestrator validators, and
// documentation generators MUST derive from this package — never
// hardcode the list elsewhere.
//
// # Taxonomy
//
//	C1 code       — source code artifacts (functions, modules, services).
//	C2 text       — prose, documentation, narrative content.
//	C3 image      — still images, illustrations, generated art.
//	C4 video      — motion pictures, animation, synthetic video
//	                (EU AI Act 2026-08-02 disclosure required).
//	C5 audio      — voice, music, sound effects, synthetic audio
//	                (EU AI Act 2026-08-02 disclosure required).
//	C6 multi-modal — composite artifacts spanning ≥2 of
//	                {code, text, image, video, audio} in ONE output.
//	C7 mixed      — a coordinated bundle of independent artifacts
//	                (e.g. a campaign = image + text + landing-page-code).
//
// # Why a separate package
//
// Before v1.4.1, the C1..C7 list lived only as a JSON Schema enum
// fragment in `internal/tools/vibe.go` for `vibe_publish`, and was
// absent from `vibe_spec` (asymmetry: same field, two schemas). This
// package is the single source of truth; the JSON Schema layer, the
// orchestrator validation layer, and the documentation layer all
// derive from `All()` / `JSONSchemaEnum()` / `Description()`.
//
// # Versioning
//
// Adding a new case (e.g. C8) is a MINOR version bump: case labels
// are persisted as TEXT in the DB, and an existing row with an
// unknown case will be returned by readers as the raw string. Adding
// a case is therefore backward-compatible. Reordering or renaming
// an existing case is a BREAKING change.
package vibecase

import (
	"errors"
	"fmt"
	"strings"
)

// Case is the canonical case identifier. Persisted as a TEXT column
// in the DB (specs.vibe_case, artifacts.vibe_case); validated at every
// orchestrator boundary so callers cannot persist an unknown case.
//
// Case is a typed string (not a numeric enum) so the DB column stays
// human-readable and so the wire schema (JSON `"C1"`) matches the
// in-memory representation without a mapping table.
type Case string

// Canonical case identifiers. The order of these constants is the
// authoritative order returned by `All()` — do not reorder without a
// major version bump.
const (
	CaseCode       Case = "C1" // source code
	CaseText       Case = "C2" // prose / documentation
	CaseImage      Case = "C3" // still images
	CaseVideo      Case = "C4" // motion pictures (EU AI Act disclosure)
	CaseAudio      Case = "C5" // voice / music / SFX (EU AI Act disclosure)
	CaseMultiModal Case = "C6" // composite single-output artifact
	CaseMixed      Case = "C7" // coordinated bundle of independent artifacts
)

// all is the canonical ordered slice. `All()` returns a defensive
// copy so callers cannot mutate the package-level state.
var all = [...]Case{
	CaseCode,
	CaseText,
	CaseImage,
	CaseVideo,
	CaseAudio,
	CaseMultiModal,
	CaseMixed,
}

// descriptions maps each canonical case to a one-line human
// description. Used by `Description()` and by the LLM-facing
// context projections (spec_context, artifact_context).
var descriptions = map[Case]string{
	CaseCode:       "code — source code artifacts (functions, modules, services).",
	CaseText:       "text — prose, documentation, narrative content.",
	CaseImage:      "image — still images, illustrations, generated art.",
	CaseVideo:      "video — motion pictures, animation, synthetic video (EU AI Act disclosure required).",
	CaseAudio:      "audio — voice, music, sound effects, synthetic audio (EU AI Act disclosure required).",
	CaseMultiModal: "multi-modal — composite artifact spanning ≥2 of {code, text, image, video, audio} in one output.",
	CaseMixed:      "mixed — coordinated bundle of independent artifacts (e.g. campaign = image + text + landing-page-code).",
}

// ErrInvalidCase is returned by Parse when the input is not a canonical
// case identifier. Use `errors.Is(err, vibecase.ErrInvalidCase)` to
// branch on this in higher layers.
var ErrInvalidCase = errors.New("vibecase: invalid case identifier")

// Parse validates and canonicalises a case string. Whitespace is
// trimmed; an empty input returns ErrInvalidCase. Unknown values
// (e.g. "C8", "c1", "CODE") also return ErrInvalidCase — the package
// does NOT silently uppercase or coerce.
//
// Parse is the function every orchestrator should call before
// persisting a VibeCase field. The wrapper in `internal/orchestration`
// adds a field-path prefix via `store.NewFieldError`.
func Parse(s string) (Case, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidCase)
	}
	c := Case(trimmed)
	if _, ok := descriptions[c]; !ok {
		return "", fmt.Errorf("%w: %q is not one of %v", ErrInvalidCase, trimmed, JSONSchemaEnum())
	}
	return c, nil
}

// MustParse is the panic-on-error variant of Parse. Use ONLY for
// tests and for hardcoded constants at startup; never for user input.
//
//	func init() {
//	    DefaultCase = vibecase.MustParse("C1")
//	}
func MustParse(s string) Case {
	c, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return c
}

// IsValid reports whether s is a canonical case identifier.
// Equivalent to `Parse(s) == nil` but without the error allocation.
func IsValid(s string) bool {
	_, ok := descriptions[Case(strings.TrimSpace(s))]
	return ok
}

// String returns the canonical string form ("C1", "C2", ...).
func (c Case) String() string { return string(c) }

// IsZero reports whether the case is the empty string. Used by
// callers to distinguish "unset" from "explicitly set to a valid case".
func (c Case) IsZero() bool { return string(c) == "" }

// All returns every canonical case in C1..C7 order. The returned slice
// is a fresh defensive copy on each call; callers may mutate it.
//
// The order is stable and authoritative: do not reorder without a
// major version bump (case labels are persisted as TEXT and downstream
// readers iterate `All()` to render the canonical list).
func All() []Case {
	out := make([]Case, len(all))
	copy(out, all[:])
	return out
}

// JSONSchemaEnum returns the canonical enum slice for JSON Schema
// "enum" constraints. The order matches All() — stable.
//
// Usage in a schema:
//
//	enum: vibecase.JSONSchemaEnum()
//
// This is the canonical way to declare the C1..C7 enum in any JSON
// Schema; do not duplicate the list inline.
func JSONSchemaEnum() []string {
	out := make([]string, len(all))
	for i, c := range all {
		out[i] = string(c)
	}
	return out
}

// Description returns a one-line human description of the case.
// For unknown cases (defensive: should not happen post-Parse),
// returns a placeholder.
func Description(c Case) string {
	if d, ok := descriptions[c]; ok {
		return d
	}
	return fmt.Sprintf("unknown case %q", string(c))
}
