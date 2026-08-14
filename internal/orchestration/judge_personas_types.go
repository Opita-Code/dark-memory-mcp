// Package orchestration — judge_personas_types.go
//
// v2.17.0 (spec 1155): Persona Registry types.
//
// The Persona is the specialized system_prompt the LLM-as-judge
// pipeline uses for a given eval_type. Each persona encodes:
//   - Role: 1-2 sentences describing what the judge does
//   - Lens: 1-2 paragraphs of the evaluation lens (what to look for)
//   - Rubric: atomic, independently verifiable checks (DAGMetric-style)
//   - Constraints: anti-hallucination anchors + behavioral rules
//   - Voice: response style (cite file:line, fail-closed, etc.)
//
// Per spec 1155 v14 §3:
//
//   type Persona struct {
//       ID, Name, Role, Lens, Rubric, Constraints, Voice,
//       EvalTypes []string,
//       Source string, // "compiled" | "file:<path>" | "agent_memory:<id>"
//       Default bool
//   }
//
// SharedConstraints is appended to every persona (per spec 1155 §3):
// the canonical anti-hallucination anchor + verbatim quote + citation
// requirements. BuildAnchor() returns the full string for callers that
// want a single bundle.
package orchestration

// Persona is the registered system_prompt for a single evaluation
// lens. See package doc for the schema.
type Persona struct {
	ID          string   // unique, stable identifier (e.g. "judge-security")
	Name        string   // human-readable label (e.g. "Security Reviewer")
	Role        string   // 1-2 sentences describing what the judge does
	EvalTypes   []string // eval_types this persona handles (e.g. ["security_coverage"])
	Lens        string   // 1-2 paragraphs of evaluation lens
	Rubric      []string // atomic checks (DAGMetric-style), each independently verifiable
	Constraints []string // anti-hallucination anchors + behavioral rules
	Voice       string   // style of response (cite file:line, fail-closed, etc.)
	Source      string   // "compiled" | "file:<path>" | "agent_memory:<id>"
	Default     bool     // whether this persona is the default for any of its eval_types
}

// SharedConstraints is prepended to every persona's Constraints list
// during prompt composition. It encodes the canonical anti-hallucination
// contract: insufficient info → needs_human, verbatim quotes only, file:line
// citation, calibration discipline.
//
// Per spec 1155 §3, this is the canonical list. Personas can extend with
// persona-specific constraints but must not remove these.
var SharedConstraints = []string{
	`If you do not have enough information to evaluate this artifact against the spec, you MUST return verdict="needs_human" with reasoning="insufficient information to ground verdict". Do NOT guess. Do NOT fabricate quotes. Do NOT extrapolate beyond what is visible in the artifact at the cited file:line.`,

	`Your evidence[] MUST contain at least one item with a verbatim quote that EXACTLY matches the artifact at the cited file:line. If you cannot find such a quote, return verdict="needs_human" instead.`,

	`Cite file:line for every evidence claim. Quote the artifact verbatim — no paraphrase, no summary, no abbreviation.`,

	`Confidence is calibrated: 0.5 means coin-flip, 0.95 means near-certain. Confidence above 0.9 requires 3+ evidence items. Below 0.5 escalates to needs_human.`,
}

// PersonaSource values for the Source field.
const (
	PersonaSourceCompiled    = "compiled"
	PersonaSourceFilePrefix  = "file:" // followed by the path
	PersonaSourceAgentMemory = "agent_memory"
)

// EffectiveConstraints returns the persona's Constraints list with
// SharedConstraints prepended. Use this when composing the system
// prompt so every persona gets the canonical anti-hallucination anchor.
func (p *Persona) EffectiveConstraints() []string {
	out := make([]string, 0, len(SharedConstraints)+len(p.Constraints))
	out = append(out, SharedConstraints...)
	out = append(out, p.Constraints...)
	return out
}

// HasEvalType reports whether the persona covers the eval_type.
func (p *Persona) HasEvalType(evalType string) bool {
	for _, et := range p.EvalTypes {
		if et == evalType {
			return true
		}
	}
	return false
}
