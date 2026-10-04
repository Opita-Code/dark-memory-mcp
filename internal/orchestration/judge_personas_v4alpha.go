// Package orchestration — judge_personas_v4alpha.go
//
// Phase 9 alpha.20 Chunk 8.2: bridges the v4alpha/judge PersonaContent
// registry (Phase 7 alpha.19 Chunks 7.1, 6 v4-new personas with rich
// PromptTemplate + EvaluationLens + BiasControls + RequiredEvidence)
// into the v3 orchestration.PersonaRegistry.
//
// The 6 v4-new personas are: judge-cross-modal, judge-pipeline,
// judge-opinion, judge-decision, judge-research, judge-delegator.
// Per v4alpha/judge/personas_v4.go:336 they MUST have valid rich
// content (compile-time invariant). This file converts each
// PersonaContent into an orchestration.Persona so v3's
// dark_memory_judge_list_personas returns 14 personas (8 v2 + 6 v4-new)
// instead of 8 only — closes Phase 8 e2e critical finding #2 (row 2323
// — T3).
//
// # Field mapping (v4alpha PersonaContent → orchestration.Persona)
//
//	v4alpha ID              → Persona.ID
//	v4alpha PromptTemplate  → Persona.Role (first sentence) + Persona.Voice (full template)
//	v4alpha EvaluationLens  → Persona.Lens (verbatim)
//	v4alpha BiasControls    → Persona.Constraints (each entry becomes one)
//	v4alpha RequiredEvidence→ Persona.Rubric (each entry becomes one rubric check)
//	(none in v4alpha)       → Persona.EvalTypes (nil — v4alpha lenses are persona-level, NOT bound to specific eval_types)
//	(none in v4alpha)       → Persona.Name (derived from ID with case fixup)
//	(constant)              → Persona.Source = PersonaSourceV4Alpha
//	(constant)              → Persona.Default = false (v4alpha personas are explicit-only; v2 personas retain their defaults)
//
// # Why Default=false
//
// v4alpha personas do not bind to specific eval_types (their lenses
// are persona-level: a judge-cross-modal works on any artifact that
// crosses modalities, regardless of which eval_type triggered the
// call). Marking them as Default would corrupt v2's deterministic
// eval_type → persona mapping. v4alpha personas are reachable via
// explicit persona_id="judge-cross-modal" (etc.) or via the v4alpha
// pipeline's own persona resolution.
package orchestration

import (
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/judge"
)

// v4alphaPersonaIDs is the canonical list of v4-new persona IDs that
// have rich PersonaContent in v4alpha/judge. Order matches
// v4alpha/judge/personas_v4.go:336 (compile-time invariant).
//
// Phase 12 T-104 (alpha.23, 2026-10-04) added judge-modifications +
// judge-progress for audit-quality of the events table (schema v32).
// Total v4-new personas: 8 (was 6 in alpha.22). Total registry surface:
// 16 (8 v2 + 8 v4-new). Closes Phase 12 OD7 invariant (q): audit-quality
// eval_types for the events table.
var v4alphaPersonaIDs = []string{
	"judge-cross-modal",
	"judge-pipeline",
	"judge-opinion",
	"judge-decision",
	"judge-research",
	"judge-delegator",
	"judge-modifications", // Phase 12 T-104
	"judge-progress",      // Phase 12 T-104
}

// v4alphaPersonas converts the v4alpha PersonaContent registry into
// orchestration.Persona structs. Returns one Persona per v4-new
// persona ID. The conversion is pure (no I/O) and deterministic.
//
// When a persona is missing from the v4alpha registry (e.g., a
// future Markdown override removed it), it is silently skipped —
// NOT added to the result. Operators who need to enforce strict
// presence can call it via the Source discriminator after the
// registry is built.
//
// The conversion is one-way: PersonaContent → Persona. Going
// from Persona back to PersonaContent would lose information
// (Persona.Lens is just the EvaluationLens verbatim; Persona.Role
// is the first sentence of PromptTemplate, not the full template).
// v4alpha's LLMAdapter uses PersonaContent directly, not via the
// orchestration.PersonaRegistry, so the one-way conversion is
// safe — the v4alpha pipeline never needs to roundtrip.
func v4alphaPersonas() []*Persona {
	out := make([]*Persona, 0, len(v4alphaPersonaIDs))
	for _, id := range v4alphaPersonaIDs {
		c := judge.LookupPersonaContent(id)
		if c == nil {
			// Persona missing from v4alpha registry — skip silently.
			// The compile-time invariant in personas_v4.go:336 ensures
			// this never happens in production builds; tests that
			// UnregisterPersonaContent may surface this path.
			continue
		}
		out = append(out, v4alphaContentToPersona(id, c))
	}
	return out
}

// v4alphaContentToPersona is the per-persona field mapper. Documented
// at the package level above. Pure function — no I/O.
func v4alphaContentToPersona(id string, c *judge.PersonaContent) *Persona {
	return &Persona{
		ID:        id,
		Name:      v4alphaPersonaName(id),
		Role:      firstSentence(c.PromptTemplate),
		EvalTypes: nil, // v4alpha lenses are persona-level, NOT eval_type-bound
		Lens:      c.EvaluationLens,
		Rubric:    append([]string(nil), c.RequiredEvidence...), // defensive copy
		Constraints: append([]string(nil), c.BiasControls...), // defensive copy
		Voice:     c.PromptTemplate,
		Source:    PersonaSourceV4Alpha,
		Default:   false, // explicit-only; v2 personas retain their defaults
	}
}

// v4alphaPersonaName derives a human-readable Name from the persona
// ID. Pure function. Examples:
//
//	"judge-cross-modal" → "Cross-Modal Judge"
//	"judge-pipeline"    → "Pipeline Judge"
//	"judge-opinion"     → "Opinion Judge"
//	"judge-decision"    → "Decision Judge"
//	"judge-research"    → "Research Judge"
//	"judge-delegator"   → "Delegator Judge"
//
// The "judge-" prefix is stripped; the remaining dash-separated words
// are title-cased with the first word followed by "Judge".
func v4alphaPersonaName(id string) string {
	// Strip "judge-" prefix.
	if len(id) > 6 && id[:6] == "judge-" {
		id = id[6:]
	}
	// Title-case the remaining words separated by dashes, then
	// append "Judge".
	const suffix = " Judge"
	// Simple allocs; this is called only at boot once per persona.
	out := ""
	for i, r := range id {
		if r == '-' {
			r = ' '
		}
		if i == 0 || id[i-1] == '-' {
			if r >= 'a' && r <= 'z' {
				r -= 0x20 // uppercase
			}
		}
		out += string(r)
	}
	return out + suffix
}

// firstSentence returns the text up to the first sentence terminator
// ('.', '!', '?' followed by space-or-end). Empty input → empty
// output. Used to derive Persona.Role from the first sentence of
// PersonaContent.PromptTemplate.
func firstSentence(s string) string {
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' {
			// Check if next is space or end-of-string.
			if i == len(s)-1 || s[i+1] == ' ' || s[i+1] == '\n' {
				return s[:i]
			}
		}
	}
	return s
}