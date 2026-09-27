// Package judge — extended persona content for the 3 v4-new personas.
//
// Commit 1 (rubric.go) registered 11 personas (8 legacy + 3 v4 new per
// ADR-007 §3) with thin 1-line descriptions. Commit 2 wires the v4
// pipeline to use richer persona content: a system-prompt template, an
// evaluation lens, explicit bias controls, and a list of required
// evidence types. This file is the SINGLE source of truth for that
// rich content; the LLMAdapter (see llm.go) consults LookupPersonaContent
// at prompt-build time and injects the content into the system prompt.
//
// Backwards compat: 8 legacy personas have no PersonaContent; the
// LLMAdapter falls back to the generic system prompt template (see
// buildSystemPrompt). The 3 v4-new personas (judge-cross-modal,
// judge-pipeline, judge-opinion) MUST have content (compile-time
// invariant at the bottom of this file).
//
// Source of truth: ADR-007 §3 (persona registry) + §5 (anti-injection
// L3 = persona-specific prompt templates) + §6 (persona-aware bias
// controls).
package judge

import (
	"strings"
	"sync"
)

// ---------- PersonaContent ----------

// PersonaContent is the rich per-persona prompt + bias configuration.
// The LLMAdapter composes its SystemPrompt from these fields:
//
//   1. PromptTemplate (rendered first — the persona's "voice")
//   2. EvaluationLens (concatenated next — the persona's scoring focus)
//   3. BiasControls (appended as a bulleted list — anti-bias instructions)
//   4. RequiredEvidence (appended last — what evidence the persona needs)
//
// Empty fields are skipped silently (the 8 legacy personas have no
// content and the adapter falls back to the generic template).
type PersonaContent struct {
	// PromptTemplate is the persona's "voice" — a 2-4 sentence
	// system-prompt paragraph that frames the judge identity. E.g.
	// "You are a cross-modal judge. You specialize in evaluating
	// artifacts that combine text + image + audio...". The LLM
	// reads this BEFORE the generic drift_judge instructions.
	PromptTemplate string

	// EvaluationLens is the persona's scoring focus — 1 paragraph
	// naming the specific criteria the persona weights heaviest.
	// For judge-cross-modal: "weight subject consistency over
	// composition because the artifact's continuity is what the
	// user remembers". For judge-pipeline: "weight idempotence
	// over observability because a non-idempotent pipeline can
	// cause data corruption that observability cannot recover from".
	EvaluationLens string

	// BiasControls are explicit anti-bias instructions. Each entry
	// is a single sentence rendered as a bullet in the prompt.
	// Example: "Do not favor the artifact because it cites your
	// own model family (self-reference bias)".
	BiasControls []string

	// RequiredEvidence lists the evidence kinds this persona needs
	// to make a confident verdict. Used by EC-014 (evidence_missing)
	// to add a persona-specific bias note when artifacts lack them.
	// Example: ["subject_match file:line", "audio_sync timestamp"].
	RequiredEvidence []string
}

// Validate enforces the minimum invariants for a v4-new persona's
// content. Called at registry-init time. Returns an error for
// missing PromptTemplate or empty BiasControls.
func (c *PersonaContent) Validate() error {
	if strings.TrimSpace(c.PromptTemplate) == "" {
		return errPersonaContentEmpty("PromptTemplate")
	}
	if len(c.BiasControls) == 0 {
		return errPersonaContentEmpty("BiasControls")
	}
	return nil
}

// errPersonaContentEmpty builds the error for empty required fields.
func errPersonaContentEmpty(field string) error {
	return &personaContentError{Field: field}
}

// personaContentError is the typed error returned by Validate.
type personaContentError struct{ Field string }

func (e *personaContentError) Error() string {
	return "judge: persona content field " + e.Field + " is required"
}

// ---------- Registry ----------

// personaContentRegistry is the in-memory store for v4-new persona
// content. Concurrent-safe via sync.RWMutex. The 3 v4-new personas
// MUST have entries; the 8 legacy personas have none (they use the
// generic fallback).
type personaContentRegistry struct {
	mu   sync.RWMutex
	byID map[string]*PersonaContent
}

// defaultPersonaContentRegistry is the canonical registry, populated
// at package init from defaultPersonaContents (below).
var defaultPersonaContentRegistry = func() *personaContentRegistry {
	r := &personaContentRegistry{byID: make(map[string]*PersonaContent)}
	for id, c := range defaultPersonaContents {
		r.byID[id] = c
	}
	return r
}()

// LookupPersonaContent returns the rich content for a persona_id, or
// nil when the persona has no registered content (legacy fallback).
func LookupPersonaContent(personaID string) *PersonaContent {
	if personaID == "" {
		return nil
	}
	defaultPersonaContentRegistry.mu.RLock()
	defer defaultPersonaContentRegistry.mu.RUnlock()
	return defaultPersonaContentRegistry.byID[personaID]
}

// RegisterPersonaContent adds or replaces a persona's content.
// Used by tests + future Markdown override (spec 1155 v14).
// Returns an error when content.Validate() fails.
func RegisterPersonaContent(personaID string, c *PersonaContent) error {
	if personaID == "" {
		return &personaContentError{Field: "personaID"}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	defaultPersonaContentRegistry.mu.Lock()
	defer defaultPersonaContentRegistry.mu.Unlock()
	defaultPersonaContentRegistry.byID[personaID] = c
	return nil
}

// UnregisterPersonaContent removes a persona's content. Used by tests.
func UnregisterPersonaContent(personaID string) {
	defaultPersonaContentRegistry.mu.Lock()
	defer defaultPersonaContentRegistry.mu.Unlock()
	delete(defaultPersonaContentRegistry.byID, personaID)
}

// ---------- Default content for the 3 v4-new personas ----------

// defaultPersonaContents holds the rich content for the 3 v4-new
// personas (ADR-007 §3). The 8 legacy personas have no entry.
//
// Content sources:
//   - judge-cross-modal: agent_memory row 2047 §3 + ADR-007 §5 L3
//   - judge-pipeline:    agent_memory row 2047 §3 + ADR-007 §6
//   - judge-opinion:     agent_memory row 2047 §3 (reserved for
//     future use; content is a placeholder)
var defaultPersonaContents = map[string]*PersonaContent{
	"judge-cross-modal": {
		PromptTemplate: "You are a cross-modal judge. " +
			"You specialize in evaluating artifacts that combine multiple modalities — image, " +
			"video, audio — alongside text. Your primary lens is SUBJECT CONTINUITY: when the " +
			"same entity appears across frames or modalities, you verify it remains the same " +
			"entity. When the modalities tell different stories, you call it drift.",
		EvaluationLens: "Weight subject_consistency and scene_transitions over composition " +
			"and color_palette. A well-composed still that does not match the prompt's subject " +
			"is drift; a flatly-composed video that matches the subject across every frame is " +
			"aligned. Audio_sync is a hard requirement (out-of-sync audio is drift regardless " +
			"of how good the video looks).",
		BiasControls: []string{
			"Do not favor an artifact because its visual style matches the prompt — only because " +
				"the SUBJECT matches.",
			"When the prompt is ambiguous (no specific subject), score the artifact against " +
				"the SPEC INTENT, not the surface wording.",
			"Cite at least one evidence snippet per criterion. Empty evidence = needs_human.",
			"If the artifact lacks a timestamp or frame number you can cite, return " +
				"needs_human rather than guess.",
		},
		RequiredEvidence: []string{
			"subject identifier (named or described)",
			"timestamp or frame number for time-based artifacts",
			"audio_sync reference when audio is present",
		},
	},

	"judge-pipeline": {
		PromptTemplate: "You are a pipeline judge. " +
			"You evaluate CI/CD, deploy, migration, and orchestration artifacts. Your primary " +
			"lens is OPERATIONAL SAFETY: a pipeline that is hard to roll back, that runs " +
			"multiple times producing different results, or whose blast radius exceeds its " +
			"observability is unsafe — even if it ships fast.",
		EvaluationLens: "Weight idempotence and rollback over blast_radius and observability. " +
			"A pipeline with great observability but no rollback is more dangerous than one " +
			"with weak observability and a tested rollback. Security is the floor — a pipeline " +
			"that exfiltrates secrets is drift regardless of how well it scores on other axes.",
		BiasControls: []string{
			"Do not reward pipelines for being clever. Reward pipelines for being SAFE TO " +
				"OPERATE UNDER PRESSURE (3am incident, on-call engineer, partial outage).",
			"Quote the rollback command or step when you score rollback. A pipeline that " +
				"'has rollback' but doesn't cite the command is needs_human.",
			"Reject pipelines that mix data-plane and control-plane concerns. Cite the " +
				"specific lines that violate this when you score blast_radius low.",
		},
		RequiredEvidence: []string{
			"rollback command or step",
			"idempotence key or hash",
			"blast_radius estimate (resource count or service count)",
		},
	},

	"judge-opinion": {
		PromptTemplate: "You are an opinion judge. " +
			"You evaluate subjective artifacts — design choices, editorial framing, " +
			"aesthetic judgments — where multiple valid framings coexist. Your primary " +
			"lens is DELIBERATE COHERENCE: the artifact should reflect ONE coherent " +
			"stance, not a committee compromise.",
		EvaluationLens: "ADR-007 §3 reserves judge-opinion for future use. For commit 2, " +
			"score opinion artifacts as needs_human with reasoning 'judge-opinion is reserved " +
			"— operators must review subjective artifacts manually'. The placeholder " +
			"content lets the pipeline resolve the persona without falling into EC-005.",
		BiasControls: []string{
			"Until judge-opinion is fully specified, return needs_human on every opinion " +
				"artifact. Reasoning MUST cite 'judge-opinion reserved — manual review'.",
		},
		RequiredEvidence: []string{
			"the artifact's stated stance (verbatim quote)",
		},
	},
}

// ---------- Compile-time invariants ----------

// _ ensures the 3 v4-new personas each have valid rich content.
// Catches "added a persona to rubric.go defaultPersonas but forgot
// to add it here" bugs at compile time.
var _ = func() error {
	for _, id := range []string{"judge-cross-modal", "judge-pipeline", "judge-opinion"} {
		c := LookupPersonaContent(id)
		if c == nil {
			return &personaContentError{Field: "registry missing entry for " + id}
		}
		if err := c.Validate(); err != nil {
			return err
		}
	}
	return nil
}()
