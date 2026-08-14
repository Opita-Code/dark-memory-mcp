// Package orchestration — judge_evidence_anchors.go
//
// v2.16.0: T3 — Anti-Hallucination Anchors.
//
// Per Anthropic's "Demystifying evals for AI agents" (Jan 9 2026):
//
//   "To avoid hallucinations, give the LLM a way out, like providing
//    an instruction to return 'Unknown' when it doesn't have enough
//    information."
//
// Every persona system prompt MUST include the AntiHallucinationAnchor
// so the LLM has a safe escape hatch. Without the anchor, the LLM
// tends to invent quotes (the "recap problem" root cause).
//
// The anchor is mandatory and non-removable — BuildAnchor() returns
// the canonical string. Persona authors concat it to their SPs.
//
// This is a pure constant + helper. No LLM interaction.
package orchestration

// AntiHallucinationAnchor is the canonical anchor that MUST be
// appended to every persona system prompt. The judge MUST return
// verdict="needs_human" when it cannot ground a quote verbatim.
//
// Tier-1 source: Anthropic Engineering Blog, "Demystifying evals for
// AI agents" (Jan 9 2026), section "How evals fit with other methods".
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
func BuildAnchor() string {
	return AntiHallucinationAnchor
}

// InjectAnchor appends the anti-hallucination anchor to a persona
// system prompt. The anchor is appended LAST so it cannot be
// accidentally overridden by an earlier instruction.
//
// If systemPrompt is empty, returns just the anchor.
func InjectAnchor(systemPrompt string) string {
	if systemPrompt == "" {
		return AntiHallucinationAnchor
	}
	return systemPrompt + "\n\n---\n\n" + AntiHallucinationAnchor
}
