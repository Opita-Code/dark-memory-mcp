// Package orchestration — judge_prompt_builder_tdj5_test.go
//
// TD-J5 closure regression tests (2026-08-18, sess-f28534325d0f2496):
// the JudgePromptBuilder.Build must propagate SpecIntent into the
// user prompt so the drift_judge LLM can compare spec-vs-artifact.
//
// The PUBLISHER side of the fix lives at publish_vibe.go:411
// (`SpecIntent: in.Spec.Spec`, commit 3ce7e9a9). Without that line,
// JudgeInput.SpecIntent was empty, the user prompt lacked the
// "## Spec intent" section, and the judge's anti-hallucination
// anchor (correctly) emitted needs_human "spec text not provided"
// for drifts 1073-1082. After the fix, drift 1090 onward produced
// legitimate aligned verdicts (see SPEC 1242 t1+t2 CERRADO pinned
// row 986).
//
// These tests pin the CONSUMER side: the builder must include the
// spec text verbatim in the user prompt when SpecIntent is set, and
// must omit the "## Spec intent" section entirely when SpecIntent
// is empty (per judge_prompt_builder.go:180 contract).
package orchestration

import (
	"strings"
	"testing"
)

// TestJudgePromptBuilder_Build_SpecIntentIncludedInUserPrompt_DJ5
// verifies TD-J5 closure on the consumer side: when SpecIntent is
// non-empty, the user prompt includes the verbatim spec text under
// the "## Spec intent" section header.
//
// publish_vibe.go:411 is the publisher side; this test is the
// consumer side. Together they form the chain: in.Spec.Spec →
// JudgeInput.SpecIntent → Builder.Build user prompt.
func TestJudgePromptBuilder_Build_SpecIntentIncludedInUserPrompt_DJ5(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	const specMarker = "MARKER_TDJ5_SPEC_42_LANDING_CONVERSION_COPY"
	jp, err := b.Build("drift_judge", "", nil, specMarker)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !strings.Contains(jp.UserPrompt, "## Spec intent") {
		t.Errorf("TD-J5 FAIL: user prompt missing '## Spec intent' section header. "+
			"composeUserPrompt should emit the section when SpecIntent is non-empty. "+
			"User prompt: %s", jp.UserPrompt)
	}
	if !strings.Contains(jp.UserPrompt, specMarker) {
		t.Errorf("TD-J5 REGRESSION: SpecIntent %q not present in user prompt. "+
			"publish_vibe.go:411 must populate JudgeInput.SpecIntent=in.Spec.Spec, "+
			"and JudgePromptBuilder.Build must include the text verbatim. "+
			"User prompt (first 500 chars): %s",
			specMarker, jp.UserPrompt[:min(500, len(jp.UserPrompt))])
	}

	// The spec section must appear AFTER the artifact section (since
	// composeUserPrompt writes Artifact first, then Spec intent).
	// This guards against future refactors that might reorder sections.
	artifactIdx := strings.Index(jp.UserPrompt, "## Artifact")
	specIdx := strings.Index(jp.UserPrompt, "## Spec intent")
	if artifactIdx >= 0 && specIdx >= 0 && specIdx <= artifactIdx {
		t.Errorf("TD-J5 ORDERING: '## Spec intent' (idx %d) must appear AFTER "+
			"'## Artifact' (idx %d) so the judge sees artifact+spec together.",
			specIdx, artifactIdx)
	}
}

// TestJudgePromptBuilder_Build_EmptySpecIntent_OmitsSection_DJ5 is the
// safety pair to TD-J5: when SpecIntent is empty, the prompt must
// NOT contain a placeholder "## Spec intent" section (which would
// make the judge see a stub and (correctly) emit needs_human).
//
// This pins the contract at judge_prompt_builder.go:180 —
// "If specIntent is empty, the 'Spec intent' section is omitted."
func TestJudgePromptBuilder_Build_EmptySpecIntent_OmitsSection_DJ5(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	// SpecIntent deliberately empty (also covers whitespace-only).
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty_string", ""},
		{"whitespace_only", "   \t  \n  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jp, err := b.Build("drift_judge", "", nil, tc.value)
			if err != nil {
				t.Fatalf("Build(%q): %v", tc.value, err)
			}
			if strings.Contains(jp.UserPrompt, "## Spec intent") {
				t.Errorf("TD-J5 SAFETY: empty SpecIntent (%q) must NOT produce "+
					"'## Spec intent' section. composeUserPrompt should skip the "+
					"section when strings.TrimSpace(specIntent) == ''. "+
					"User prompt (first 500 chars): %s",
					tc.value, jp.UserPrompt[:min(500, len(jp.UserPrompt))])
			}
		})
	}
}

// TestJudgePromptBuilder_Build_SpecIntentNotInSystemPrompt is a small
// sanity test that locks the partition: SpecIntent goes into the
// user prompt, NOT the system prompt. The system prompt carries the
// persona+anchor (judge_evidence_anchors_test.go); the user prompt
// carries the per-call artifact+spec. Mixing them is a TD-J5
// regression in another guise (persona corruption).
func TestJudgePromptBuilder_Build_SpecIntentNotInSystemPrompt(t *testing.T) {
	r, _ := NewPersonaRegistry(RegistryOptions{IncludeMarkdownOverrides: false})
	b := NewJudgePromptBuilder(r)

	const specMarker = "MARKER_TDJ5_SYSTEM_PARTITION_CHECK"
	jp, err := b.Build("drift_judge", "", nil, specMarker)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(jp.SystemPrompt, specMarker) {
		t.Errorf("TD-J5 PARTITION: spec marker leaked into SystemPrompt. "+
			"SpecIntent must only reach the user prompt. "+
			"System prompt (first 300 chars): %s",
			jp.SystemPrompt[:min(300, len(jp.SystemPrompt))])
	}
}