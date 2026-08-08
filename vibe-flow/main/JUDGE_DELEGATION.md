# JUDGE_DELEGATION.md — Intelligent judge delegation (spec 874)

Status: **IMPLEMENTED** (2026-08-08)
Spec: 874 (C1), sess-673acacc4e1c69c0 / sess-8d7c9bea1856fde9
Author: dark-agent
Source: research synthesis (LLM×MapReduce ACL 2025, f22labs PoC, MT-Bench/G-Eval, MiniMax M3 + Claude thinking docs)

---

## 1. Problem

Two compounding issues made `judge` / `consensus` unreliable for large content:

1. **End-to-end API config bug**: `SDD_LLM_BASE_URL` pointed at `api.minimaxi.com`
   (wrong domain) instead of `api.minimax.io`. Every judge call returned
   `HTTP 401 invalid api key` even though the key was valid. Fixed in
   `opencode.jsonc` (2026-08-08).

2. **Single-shot only, no token awareness**: dark-memory's native `judge`
   passes the entire content in one LLM call. There is no token counting, no
   chunking, and no delegation. Beyond ~8K tokens, quality degrades because
   LLM attention thins on long contexts.

## 2. Design decision (see agent_memory row 491)

The delegation system lives at the **agent layer** (dark-agent CLIs), not
inside dark-memory Go. Rationale:

- LLM×MapReduce is an orchestration *pattern*, not a single function.
- When/how to chunk + aggregate is **policy**: depends on eval type, model
  context window, cost budget, operator intent.
- Hardcoding in Go means a release per new model/content type. Agent-layer
  CLIs update independently.

dark-memory keeps its 3 JUDGE tools unchanged (judge, consensus,
judgment_history). The agent composes them via the new CLIs.

## 3. The pipeline (4 CLIs)

Location: `~/.config/dark-agent/judge-delegation/`

| Script | Role |
|---|---|
| `count-tokens.py` | chars/4 heuristic + per-model context window table. Exit 0 = single-shot, 2 = delegate. |
| `chunk-content.py` | Semantic chunking (paragraphs, markdown sections, protected code blocks). `is_split` flag for oversize paragraphs. |
| `aggregate-verdicts.py` | Position-weighted aggregation (intro/conclusion 1.5x), disagreement detection, human-review trigger (<60% agreement or confidence <0.5). |
| `delegate-judge.sh` | Orchestrator: count → decide → single-shot OR chunk → parallel LLM → aggregate. curl to `SDD_LLM_BASE_URL`. |

### Decision rule (count-tokens.py)
```
delegate = (token_count > threshold) OR (token_count > context_window / 2)
threshold default = 8000 tokens
```

### Verification (MiniMax-M3 real, 31.5K-token artifact with injected drift)
- 10 chunks, parallelism 4, ~31.5K tokens.
- Run 1: 8/10 drift_detected → **drift_detected** (conf 0.963) ✅ detected the
  injected drift.
- Run 2: 5 aligned / 4 drift / 1 needs_human → **needs_human** (50% agreement
  < 60% floor) ✅ correct: the system refuses to fabricate a verdict when
  chunks disagree.

## 4. Windows gotchas (learned the hard way, all fixed)

| Gotcha | Fix |
|---|---|
| `Argument list too long` (32K limit) | Never pass large content as `--text` or argv; use `--file` / stdin. |
| MSYS `/tmp/...` paths invisible to native Python | `cygpath -w` before handing paths to python. |
| `verdicts.jsonl` is JSONL, aggregate expects an array | Convert to `verdicts_array.json` before aggregating. |

## 5. Technical debt in dark-memory llm_client.go (follow-up)

- `llm_client.go:399` hardcodes `model = "MiniMax-M3"` as default.
- Judge does not pass `reasoning_split` (MiniMax) or `thinking` (Claude).
- No internal token counting; effective single-shot ceiling ≈8K tokens.
- `extractConfidence` (line 563) `json.Unmarshal`s the full text — fails
  silently (confidence → 0.7) when `<think>` tags are embedded.

**Phase 2 (optional)**: move the pipeline into dark-memory as a new
`judge_with_delegation` tool, using the agent-layer CLIs as the reference
implementation. Not started — agent-layer covers the need today.

## 6. Usage

```bash
# Single-shot (small content)
ANTHROPIC_API_KEY=... bash delegate-judge.sh \
  --text "short content" --eval-type drift_judge

# Delegated (large content)
ANTHROPIC_API_KEY=... bash delegate-judge.sh \
  --file large-artifact.md --eval-type drift_judge \
  --max-chunk-tokens 4000 --parallel 4 --json

# Count-only decision
python count-tokens.py --file artifact.md --model MiniMax-M3 --json-only
```
