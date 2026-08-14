// Package orchestration — judge_evidence_anchors.go
//
// v2.16.0: T3 — Anti-Hallucination Anchor (generic).
// v2.17.0: spec 1155 v14 §10 — persona-specific anchor composition.
//
// Per Anthropic's "Demystifying evals for AI agents" (Jan 9 2026):
//
//   "To avoid hallucinations, give the LLM a way out, like providing
//    an instruction to return 'Unknown' when it doesn't have enough
//    information."
//
// Spec 1155 v14 §10 makes the anchor PERSONA-SPECIFIC. The generic
// anchor constant (AntiHallucinationAnchor) is still emitted by the
// v2.17.0 persona-aware composeAnchorText(persona) function, but the
// legacy InjectAnchor(x) is deprecated. v2.16.0 callers continue to
// work via InjectAnchor (which compiles with a deprecation warning).
//
// Deprecation table (per spec 1155 v14 §10):
//
//   Symbol                          v2.17.0 action
//   -----                           --------------
//   AntiHallucinationAnchor const   KEPT (used by composeAnchorText)
//   InjectAnchor(string)            KEPT (deprecated — use composeAnchorText(*Persona))
//   BuildAnchor()                   KEPT (deprecated — use composeAnchorText(*Persona))
//   composeAnchorText(*Persona)     NEW (canonical)
//
// The v2.16.0 callers of InjectAnchor work via the renamed/aliased
// path; we keep the old function with a deprecation marker so external
// callers compile with a warning but still work. Internal callers in
// this package are updated to composeAnchorText(persona) atomically in
// the v2.17.0 release.
package orchestration

import "strings"

// AntiHallucinationAnchor is the canonical anti-hallucination anchor
// for the GENERIC case. Embedded into composeAnchorText(persona) below.
// External callers should use composeAnchorText(*Persona) instead.
const AntiHallucinationAnchor = `ANTI-HALLUCINATION ANCHOR (mandatory, non-removable):

If you do not have enough information to evaluate this artifact against the
spec, you MUST return verdict="needs_human" with reasoning="insufficient
information to ground verdict". Do NOT guess. Do NOT fabricate quotes. Do
NOT extrapolate beyond what is visible in the artifact at the cited
file:line.

Your evidence[] MUST contain at least one item with a verbatim quote that
EXACTLY matches the artifact at the cited file:line. If you cannot find
such a quote, return verdict="needs_human" instead.

This is the Anthropic anti-hallucination pattern (per
anthropic.com/engineering/demystifying-evals-for-ai-agents, Jan 2026):
"give the LLM a way out, like providing an instruction to return
'Unknown' when it doesn't have enough information."`

// BuildAnchor returns the canonical anchor string. Identical to the
// const; provided as a function so callers can mock it in tests.
//
// Deprecated: use composeAnchorText(*Persona) for persona-specific
// anchors. Kept for backward compatibility with v2.16.0 callers.
// TODO(remove-on-v3.0.0): remove this function when v2.16.0 callers
// have migrated.
func BuildAnchor() string {
	return AntiHallucinationAnchor
}

//
// Deprecated: use composeAnchorText(*Persona) for persona-specific
// anchors. Kept for backward compatibility with v2.16.0 callers.
// TODO(remove-on-v3.0.0): remove this function when v2.16.0 callers
// have migrated.
//nolint:unused // used by tests; production callers use composeAnchorText
func InjectAnchor(systemPrompt string) string {
	if systemPrompt == "" {
		return AntiHallucinationAnchor
	}
	return systemPrompt + "\n\n---\n\n" + AntiHallucinationAnchor
}

// composeAnchorText returns the persona-specific anchor text. It is the
// canonical implementation per spec 1155 v14 §10. The anchor is
// rendered as:
//
//   [persona-specific constraints]
//   --- ANTI-HALLUCINATION ANCHOR (mandatory) ---
//   [AntiHallucinationAnchor constant]
//
// The anchor is appended LAST so no later content can override it.
//
// If persona is nil, falls back to the generic anchor (equivalent to
// InjectAnchor("")).
func composeAnchorText(persona *Persona) string {
	parts := []string{}

	if persona != nil {
		// Persona-specific constraints (the persona's own constraints).
		// These are intentionally rendered ABOVE the generic anchor so
		// the persona can refine the anti-hallucination discipline for
		// its specific eval_type (e.g., a security persona can add
		// "fail-closed" prompting in addition to the generic
		// "insufficient information" escape hatch).
		if len(persona.Constraints) > 0 {
			parts = append(parts, "PERSONA-SPECIFIC CONSTRAINTS:")
			for _, c := range persona.Constraints {
				parts = append(parts, "  - "+strings.TrimSpace(c))
			}
			parts = append(parts, "")
		}
	}

	parts = append(parts, "--- ANTI-HALLUCINATION ANCHOR (mandatory, non-removable) ---")
	parts = append(parts, "")
	parts = append(parts, AntiHallucinationAnchor)

	return strings.Join(parts, "\n")
}
