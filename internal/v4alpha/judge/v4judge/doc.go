// Package v4judge implements the LLM-as-judge drift verdict path
// (Phase 14 T-301, SPEC-alpha-11-phase14-llm-as-judge).
//
// # Why this exists
//
// The drift_judge path in internal/orchestration/drift_judge.go:190
// wraps the LLM in a Natural Language Inference (NLI) classification
// task. The LLM is asked to reply with one of three canonical words
// ("entailment", "contradiction", "neutral"), which is then mapped to
// a drift verdict ("aligned", "drift_detected", "needs_human"). Per
// the operator philosophy (agent_memory row 2456, 2026-10-06):
// "LLM judge es la única verdad, NLI = prompt injection o
// allucination."
//
// This package implements the LLM-as-judge path: the LLM is asked the
// drift-judge task DIRECTLY (returning JSON
// {"verdict","confidence","reasoning"}), not the SNLI-2018 task. The
// verdict IS what the LLM says, with confidence and reasoning as
// first-class outputs.
//
// # NLI is preserved
//
// This package does NOT modify internal/nli/. The NLI chain
// (DeBERTa + MiniCheck + ChatProvider) continues to work for projects
// that have not bound a `judge-*` provider. v4judge.LLMJudge is the
// PRIMARY path when bound; NLI is the SECONDARY fallback when no
// judge-* provider is bound. See drift_judge.go:5 (Phase 14 T-302
// refactor) for the selector.
//
// # ProviderID prefix convention
//
// v4judge.LLMJudge is wired when projects.nli_config_json.primary.
//provider_id starts with "judge-" (e.g. "judge-minimax-cn"). The "chat-"
//prefix continues to route to internal/nli/chat.go::ChatProvider
// (the NLI chain). The prefix-boundary is enforced at NewLLMJudge
// (returns ErrProviderIDNotJudge if the prefix is not "judge-").
//
// # Wire shape
//
// POST <endpoint>
// Content-Type: application/json
// Authorization: Bearer <auth_token>
//
// Request body (OpenAI-compatible /v1/chat/completions):
//   {
//     "model": "<ModelRev>",
//     "temperature": 0,
//     "max_tokens": 512,
//     "response_format": {"type": "json_object"},
//     "messages": [
//       {"role": "system", "content": <judgeSystemPrompt>},
//       {"role": "user",   "content": <judgeUserPrompt>}
//     ]
//   }
//
// Response body (success):
//   {
//     "choices": [
//       {"message": {"role": "assistant", "content": "<JSON verdict>"}}
//     ]
//   }
//
// The JSON verdict shape (see parser.go):
//   {"verdict":"aligned"|"drift_detected"|"needs_human",
//    "confidence":0.0-1.0,
//    "reasoning":"<one paragraph>"}
//
// # Hard invariants (sealed)
//
//   - LLMJudge.ID() returns cfg.ProviderID verbatim (e.g.
//     "judge-minimax-cn"). Provenance flows through the audit chain.
//   - LLMJudge.Judge NEVER echoes auth_token, spec_intent, or
//     artifact_body in errors. The verdict is the only caller-visible
//     artefact.
//   - max_artifact_bytes from the project's NLIConfig enforce size
//     caps BEFORE the HTTP call (defense in depth; the artifact
//     pipeline already enforces its own caps via artifact.Resolver).
//   - Timeout is read from cfg.TimeoutMS; default 30s
//     (v4judge.DefaultTimeoutMS — longer than NLI's 10s because
//     drift-judge prompts are larger and reasoning takes longer).
//
// # SOTA grounding
//
//   - [Anthropic — claude-as-judge pattern](https://docs.anthropic.com/en/docs/build-with-claude/develop-tests)
//     — references the system+user channel separation that bounds
//     prompt injection. T-301 enforces this by forbidding the LLM from
//     referencing the artifact in the system prompt.
//   - [OpenAI — structured outputs](https://platform.openai.com/docs/guides/structured-outputs)
//     — references the response_format={"type":"json_object"} wire
//     constraint that makes parsing deterministic.
//   - [Constitutional AI critique](https://arxiv.org/abs/2212.08073)
//     — references the after-judge self-critique invariant preserved
//     in drift_judge.go:206 (constitution_after_judge).
//
// # Operator follow-up
//
// Phase 14 to ship the Connect flow (T-303) with `llm_provider_bind` and
// `llm_provider_probe` tools. Operators bind a judge-* provider to
// the active project via the keyring (no raw auth tokens in
// operator-facing input).
package v4judge