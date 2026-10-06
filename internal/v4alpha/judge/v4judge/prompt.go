package v4judge

// judgeSystemPrompt is the system message sent to the chat model.
// The phrasing is the canonical drift-judge prompt pattern (Anthropic
// claude-as-judge + OpenAI structured outputs), adapted for dark-memory's
// verdict schema.
//
// Hard invariants the system prompt enforces:
//   - Reply with ONLY the JSON shape (no markdown, no comments, no
//     explanation). The parser (parseVerdictResponse) tolerates JSON
//     inside markdown fences but the system prompt forbids them at
//     source, making parseVerdictResponse the fallback (not the
//     primary path).
//   - Do NOT reference the artifact in the system prompt. The
//     artifact is USER content only. This is the prompt-injection
//     boundary: an attacker who injects text into the artifact cannot
//     override the system prompt because the system prompt has zero
//     knowledge of the artifact's specific content.
//   - The three canonical verdicts are spelled out with one-line criteria.
//     "needs_human" is named as the catch-all for ambiguous cases so
//     the LLM uses it instead of guessing aligned / drift_detected.
//
// Why this is the OPPOSITE of internal/nli/chat.go::nliChatSystemPrompt:
// the NLI prompt assigns the model the role of "NLI classifier" and
// commits to one canonical word. The drift-judge prompt assigns the
// model the role of "drift judge" and commits to a structured JSON
// verdict with reasoning. The LLM is being asked the question the
// operator wants answered, not a Stanford-2018 task.
const judgeSystemPrompt = `You are a drift judge for a software development workflow. ` +
	`The USER message contains a SPEC INTENT (one paragraph describing ` +
	`what the artifact SHOULD be) and an ARTIFACT BODY (the artifact ` +
	`produced). Reply with ONLY this JSON shape (no markdown, no ` +
	`explanation, no comments): ` +
		`{"verdict":"aligned"|"drift_detected"|"needs_human", ` +
		`"confidence":0.0-1.0, ` +
		`"reasoning":"<one paragraph explaining the call>"}. ` +
		`Use "aligned" if the artifact meets the spec intent. ` +
		`Use "drift_detected" if the artifact materially diverges from ` +
		`the spec intent. ` +
		`Use "needs_human" if you cannot decide (the spec is ambiguous, ` +
		`the artifact is missing required context, or the divergence is ` +
		`a judgement call). ` +
		`Do NOT reference the artifact in the system prompt. The artifact ` +
		`is USER content. Reply ONLY with the JSON object.`

// buildJudgeUserPrompt composes the user-role payload sent to the chat
// model. Exposed (non-private) for tests that need to assert prompt shape.
//
// Using JSON-style delimiters so the model can clearly see where spec_intent
// ends and artifact_body begins. The double newline between sections is
// intentional — most chat models treat \n\n as a stronger section break
// than \n.
func buildJudgeUserPrompt(specIntent, artifactBody string) string {
	return "SPEC INTENT:\n" + specIntent +
		"\n\nARTIFACT BODY:\n" + artifactBody +
		"\n\nReply with ONLY the JSON verdict."
}