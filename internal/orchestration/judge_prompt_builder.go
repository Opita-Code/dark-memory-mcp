// Package orchestration — judge_prompt_builder.go
//
// v2.17.0 (spec 1155): JudgePromptBuilder composes the system_prompt
// and user_prompt for a judge call. Per spec 1155 v14 §8.
//
// The system_prompt is composed from the persona (Role, Lens, Rubric,
// Constraints, Voice + the canonical AntiHallucinationAnchor). The
// user_prompt is the verbatim artifact (if available) plus the spec
// intent plus the JSON output schema.
//
// The builder is the canonical entry point for the judge pipeline.
//
// Token budget (per spec 1155 v14 §9):
//   - System prompt: 400-600 tokens per persona (target)
//   - User prompt: 650 tokens overhead + artifact content
//   - Combined: 1000-1500 tokens overhead (within Anthropic's 2-4K
//     recommended budget)
package orchestration

import (
	"fmt"
	"strings"
)

// JudgePrompt is the composed prompt for a single judge call.
type JudgePrompt struct {
	// SystemPrompt is the persona-derived system instructions,
	// including the canonical AntiHallucinationAnchor.
	SystemPrompt string
	// UserPrompt is the artifact + spec + output schema, in markdown.
	UserPrompt string
	// PersonaID is the resolved persona id (after the Registry's
	// default resolution). Useful for audit trails.
	PersonaID string
	// EvalType is the eval_type passed to Build (used in the
	// audit trail and per-call error reporting).
	EvalType string
}

// JudgePromptBuilder composes JudgePrompt values from a Persona's
// fields plus an artifact (optional) and a spec intent (optional).
// Hold references to the registry for resolution.
//
// All methods are safe for concurrent use: the registry is read-only
// after construction.
type JudgePromptBuilder struct {
	registry *PersonaRegistry
}

// NewJudgePromptBuilder returns a builder backed by the given registry.
// The registry must not be nil.
func NewJudgePromptBuilder(registry *PersonaRegistry) *JudgePromptBuilder {
	if registry == nil {
		panic("NewJudgePromptBuilder: registry is nil")
	}
	return &JudgePromptBuilder{registry: registry}
}

// Build resolves the persona via the registry and composes the
// JudgePrompt. evalType and personaID follow the spec 1155 v14 §7
// resolution algorithm:
//
//  1. If personaID != "", use that persona.
//  2. Else use the default for evalType.
//  3. Else fall back to judge-logical.
//
// artifact is optional: when nil, the user prompt omits the verbatim
// artifact block (the caller is responsible for passing content via
// the judge pipeline). specIntent is optional: when empty, the
// "Spec intent" section is omitted.
func (b *JudgePromptBuilder) Build(evalType, personaID string, artifact *LoadedArtifact, specIntent string) (*JudgePrompt, error) {
	persona, err := b.registry.Resolve(evalType, personaID)
	if err != nil {
		return nil, fmt.Errorf("JudgePromptBuilder.Build: %w", err)
	}

	return &JudgePrompt{
		SystemPrompt: composePersonaSystemPrompt(persona),
		UserPrompt:   composeUserPrompt(artifact, specIntent),
		PersonaID:    persona.ID,
		EvalType:     evalType,
	}, nil
}

// composePersonaSystemPrompt renders the persona's fields into a
// markdown system prompt. The persona-specific constraints come first,
// then the canonical AntiHallucinationAnchor (via composeAnchorText).
//
// Format:
//
//   # {Name} — {Role}
//
//   ## Lens
//   {lens}
//
//   ## Rubric
//   - [ ] {rubric[0]}
//   - [ ] {rubric[1]}
//   ...
//
//   {composeAnchorText(persona)}
//
//   ## Voice
//   {voice}
func composePersonaSystemPrompt(persona *Persona) string {
	if persona == nil {
		return composeAnchorText(nil)
	}
	var sb strings.Builder

	// Header.
	if persona.Name != "" {
		sb.WriteString("# ")
		sb.WriteString(persona.Name)
		if persona.Role != "" {
			sb.WriteString(" — ")
			sb.WriteString(persona.Role)
		}
		sb.WriteString("\n\n")
	} else if persona.Role != "" {
		sb.WriteString("# ")
		sb.WriteString(persona.Role)
		sb.WriteString("\n\n")
	}

	// Lens.
	if persona.Lens != "" {
		sb.WriteString("## Lens\n\n")
		sb.WriteString(strings.TrimSpace(persona.Lens))
		sb.WriteString("\n\n")
	}

	// Rubric.
	if len(persona.Rubric) > 0 {
		sb.WriteString("## Rubric\n\n")
		for _, item := range persona.Rubric {
			sb.WriteString("- [ ] ")
			sb.WriteString(strings.TrimSpace(item))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Anchors (persona-specific + canonical anti-hallucination).
	sb.WriteString(composeAnchorText(persona))
	sb.WriteString("\n\n")

	// Voice.
	if persona.Voice != "" {
		sb.WriteString("## Voice\n\n")
		sb.WriteString(strings.TrimSpace(persona.Voice))
		sb.WriteString("\n")
	}

	return sb.String()
}

// composeUserPrompt renders the user-side prompt: the artifact
// (verbatim when available) plus the spec intent plus the required
// JSON output schema.
//
// Format:
//
//   You are evaluating this artifact against this spec. Read carefully.
//
//   ## Artifact (verbatim, sha256={sha256}, lines={lineCount})
//   ```
//   {artifact content}
//   ```
//
//   ## Spec intent
//   {spec intent}
//
//   ## Required output schema
//   ```json
//   {JudgeVerdict schema with EvidenceItem}
//   ```
//
// If artifact is nil, the "Artifact" section is omitted. If
// specIntent is empty, the "Spec intent" section is omitted.
func composeUserPrompt(artifact *LoadedArtifact, specIntent string) string {
	var sb strings.Builder
	sb.WriteString("You are evaluating this artifact against this spec. Read carefully.\n\n")

	if artifact != nil {
		sb.WriteString("## Artifact (verbatim, sha256=")
		sb.WriteString(artifact.Sha256Hex)
		sb.WriteString(", lines=")
		sb.WriteString(fmt.Sprintf("%d", artifact.LineCount))
		sb.WriteString(")\n\n")
		sb.WriteString("```\n")
		sb.WriteString(string(artifact.Bytes))
		sb.WriteString("\n```\n\n")
	}

	if strings.TrimSpace(specIntent) != "" {
		sb.WriteString("## Spec intent\n\n")
		sb.WriteString(strings.TrimSpace(specIntent))
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Required output schema\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(judgeVerdictSchemaMarkdown)
	sb.WriteString("\n```\n")

	return sb.String()
}

// judgeVerdictSchemaMarkdown is the verbatim JSON schema the LLM must
// emit. This is the human-readable form of the JudgeVerdict Go struct
// (judge_evidence_types.go) — keeping them in sync is a code review
// responsibility (the schema is the source of truth for the LLM).
const judgeVerdictSchemaMarkdown = `{
  "verdict": "aligned" | "drift_detected" | "needs_human",
  "confidence": 0.0..1.0,
  "evidence": [
    {
      "file": "<relative path>",
      "line": <1-indexed int>,
      "quote": "<verbatim quote from artifact at file:line>",
      "concern": "<one short sentence on what the judge sees>"
    }
  ],
  "reasoning": "<explicit chain of reasoning>",
  "calibration_metrics": {
    "brier": <optional>,
    "ece": <optional>,
    "kuiper": <optional>,
    "n_shot": <1, 3, or 7>
  },
  "persona_id": "<resolved persona id>",
  "eval_type": "<eval_type>",
  "harness_id": "<harness id>",
  "model_used": "<model identifier>",
  "timestamp": "<RFC3339>"
}`
