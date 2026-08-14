# Persona Registry — Operator Guide

This document explains how to register and override the personas that
the LLM-as-judge pipeline uses. See the spec at
`internal/orchestration/judge_personas_*.go` for the runtime code.

## What is a persona?

A persona is a specialized system prompt the judge LLM uses for a
specific `eval_type`. Each persona encodes:

- **Role**: 1-2 sentences describing what the judge does
- **Lens**: 1-2 paragraphs of the evaluation lens (what to look for)
- **Rubric**: atomic, independently verifiable checks (DAGMetric-style)
- **Constraints**: persona-specific anti-hallucination anchors + behavioral rules
- **Voice**: response style (cite file:line, fail-closed, etc.)

The 8 default personas ship compiled in (defined in
`judge_personas_default.go`):

| Persona | Default for eval_type(s) |
|---|---|
| `judge-logical` | `drift_judge`, `spec_test_alignment` |
| `judge-visual` | `brand_match` |
| `judge-security` | `pii_detect`, `prompt_injection_scan`, `security_coverage` |
| `judge-compositional` | `mindset_compose`, `mindset_quality` |
| `judge-mutation` | `mutation_score_check` |
| `judge-resilience` | `resilience_check` |
| `judge-evidential` | `grounding_check` |
| `judge-coverage` | (none — explicit only via `persona_id`) |

## Override via Markdown files

To override a compiled persona without recompiling, set the
`DARK_JUDGE_PERSONAS_DIR` environment variable to a directory of
`*.md` files. Each file overrides one persona by `id`.

### File format

```markdown
---
id: judge-security
eval_types: [security_coverage, prompt_injection_scan]
default: false
---

# Role

Custom security reviewer role — emphasizes our specific threat model.

## Lens

Our lens is the OWASP LLM Top 10 plus our internal threat model:
  - LLM01: Prompt Injection
  - LLM02: Insecure Output Handling
  ...

## Rubric

- Our secrets are never logged, even in debug mode
- All API calls go through the gateway
- ...

## Constraints

- Custom: Tailwind classes must be hashed before logging
- Custom: All timestamps are TZ=UTC

## Voice

Threat-modeling, NEVER OK without an explicit threat model, cite file:line.
```

### Merge semantics

The loader is **field-level** (per spec 1155 v14 §5). Each field
comes from the Markdown file if present, otherwise from the compiled
default:

- `id`: required from Markdown. Must match a compiled persona's id
  (otherwise the loader errors). This prevents accidental shadowing
  of unrelated personas.
- `eval_types`: Markdown if non-empty, else compiled default.
- `default`: Markdown value (true or false). If omitted, the compiled
  default's value is preserved.
- `name`, `role`, `lens`, `rubric`, `constraints`, `voice`: Markdown
  if non-empty, else compiled default.

### Field-level merge example

If the compiled `judge-security` has:
- `name: "Security Reviewer"`
- `role: "You evaluate artifacts for security risks..."`
- `lens: "OWASP LLM Top 10..."`

And your Markdown file has:
- `role: "Custom security role for our threat model."`
- `rubric: ["- our threat model rule 1", "- our threat model rule 2"]`

The merged persona has:
- `name: "Security Reviewer"` (from compiled)
- `role: "Custom security role for our threat model."` (from Markdown)
- `lens: "OWASP LLM Top 10..."` (from compiled)
- `rubric: ["- our threat model rule 1", "- our threat model rule 2"]` (from Markdown)

## The persona_id parameter

When calling `judge` or `consensus`, pass `persona_id` to select a
specific persona:

```json
{
  "tool": "judge",
  "params": {
    "eval_type": "security_coverage",
    "content": "<verbatim artifact>",
    "persona_id": "judge-security",
    "spec_intent": "spec 1150 v2.16.0: judge evidence contract"
  }
}
```

If `persona_id` is empty, the registry resolves by `eval_type` default
(lex-smallest ID wins per spec 1155 v14 §7). If `eval_type` has no
default, the registry falls back to `judge-logical`.

## List registered personas

The `judge_list_personas` MCP tool returns the registered personas
(after Markdown overrides are applied):

```json
{
  "tool": "judge_list_personas",
  "params": {}
}

{
  "result": {
    "personas": [
      {"id": "judge-coverage", "eval_types": [], "default": false, "source": "compiled"},
      {"id": "judge-compositional", "eval_types": ["mindset_compose", "mindset_quality"], "default": true, "source": "compiled"},
      {"id": "judge-evidential", "eval_types": ["grounding_check"], "default": true, "source": "compiled"},
      ...
    ],
    "count": 8
  }
}
```

## Audit trail

Every `SDDEvaluation` row records the resolved `PersonaID` (v2.17.0).
You can audit which persona was applied to each historical evaluation:

```sql
SELECT id, eval_type, persona_id, confidence, created_at
FROM ssd_evaluations
WHERE eval_type = 'security_coverage'
ORDER BY created_at DESC
LIMIT 10;
```

## Adding a new persona (not yet overridable)

Currently, only **existing** compiled personas can be overridden via
Markdown. To add a new persona, you must:

1. Add the persona to `internal/orchestration/judge_personas_default.go`
2. Add it to `CompiledDefaultPersonas()` and provide a builder
   function like `personaJudgeXxx()`
3. Rebuild the binary

This is by design (per spec 1155 v14 §5): "To register a new persona,
compile it in `personas_default.go`, not via Markdown." Markdown
overrides are for tuning, not for new personas.

## Future work

- **agent_memory override**: Spec 1155 v14 §2.2 mentions agent_memory
  as the third loader (after compiled defaults and Markdown files).
  This is deferred to a future spec.
- **Hot reload**: Currently the registry is loaded once at startup.
  Hot reload of Markdown overrides would require a reload mechanism.
