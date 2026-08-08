# JUDGE_RUBRICS.md — Vibe-case-aware judging (spec 878)

Status: **IMPLEMENTED** (2026-08-08)
Spec: 878 (C1), sess-1916208b5c96b85a
Author: dark-agent
Source: research synthesis (G-Eval, self-consistency, JudgeBench, arxiv 2508.14419)

---

## 1. Problem

Two judge defects found 2026-08-08 (reproduced live):

1. **Contradiction verdict↔reasoning.** The LLM produced `verdict=drift_detected`
   while its own reasoning said "the disclosure is explicitly present... no
   drift is detected". Cause: `defaultSystemForEval` ("Reply ONLY with JSON")
   lets the model decide verdict and reasoning as INDEPENDENT JSON fields →
   they can contradict.

2. **No technical evaluation when the artifact is code.** The judge received
   only `eval_type` + `content`; it never knew the vibe_case
   (C1=code, C2=text, ...). A code artifact was judged with the same generic
   prompt as marketing copy.

## 2. Research basis

| Source | Finding applied |
|---|---|
| G-Eval (deepeval; qaskills 2026; G-Eval 2.0) | CoT steps derived from the rubric BEFORE the score. The verdict is the **conclusion of the checklist**, not an independent decision. |
| Self-consistency (Wang ICLR 2023; CISC ACL 2025) | Majority-vote over N reasoning paths mitigates judge instability; CISC reduces samples 40% by prioritizing high-confidence paths. |
| Sage (arxiv 2512.16041) | Measuring local/global logical consistency of the judge without human annotation. |
| JudgeBench; arxiv 2508.14419 | Technical criteria must be OBJECTIVELY verifiable (compiles, injection surface, hardcoded secrets, missing auth). 89.8% of issues in LLM code are beyond security. |
| MetaPromptQuality (existing in repo, mindset_meta_prompts.go) | The checklist + `criteria_failed` pattern already worked for mindset_quality — replicate it for drift_judge. |

## 3. Design

### A. Agent-layer: `vibe-judge.py` (primary, robust)

`~/.config/dark-agent/judge-delegation/vibe-judge.py`

- **Rubrics per vibe_case** (C1 code: CORRECTNESS/SECURITY/MAINTAINABILITY/SPEC_CONFORMANCE;
  C2 text: COHERENCE/RELEVANCE/FLUENCY/BRAND_ALIGNMENT; C3-C7 fallback).
- **G-Eval procedure**: evaluate EACH criterion with quoted evidence, then
  verdict = logical conclusion. Output includes `criteria[]`, `criteria_failed[]`.
- **Consistency post-check** (`detect_contradiction`): if reasoning contradicts
  verdict → override to `needs_human`. Heuristic negation-aware (6/6 unit tests).
- **Self-consistency mode** (`--consensus N`): majority-vote over N samples,
  <60% agreement → `needs_human` (same floor as dark-memory consensus).

Usage:
```bash
ANTHROPIC_API_KEY=... python vibe-judge.py --file auth.go --vibe-case C1 --eval-type drift_judge
ANTHROPIC_API_KEY=... python vibe-judge.py --file copy.md --vibe-case C2 --eval-type brand_match --consensus 3
```

### B. Go layer: `vibe_case` on judge/consensus (retrocompat)

- `JudgeInput.VibeCase` + `JudgeRequest.VibeCase` (empty = legacy).
- `rubricPromptFor(vibeCase, evalType)` appends the G-Eval rubric to
  `defaultSystemForEval` when vibe_case is set. Empty vibe_case returns "" —
  exact pre-v2.12.0 prompt (retrocompat contract, tested).
- Schema: `vibe_case` enum C1..C7 on both `dark_memory_judge` and
  `dark_memory_consensus`.

## 4. Verification

### vibe-judge.py (real MiniMax-M3)

| Test | Result |
|---|---|
| C1 code artifact with SQL injection + password log + stub token verify | `drift_detected` 0.97, all 4 criteria fail with quoted lines |
| Same C1, `--consensus 3` | 3/3 `drift_detected`, agree 1.0, conf 0.977 |
| C2 filler content | `drift_detected`, 4 criteria fail with evidence |
| `detect_contradiction` unit tests | 6/6 PASS (incl. the exact bug case + no false positives) |

### Go

- `go build ./...` clean.
- `rubricPromptFor` tests 4/4 PASS (C1 technical, C2 text, C7 fallback, empty→"").
- All existing judge/consensus/publish_vibe tests PASS (retrocompat).
- Pre-existing failures (`TestDelegateIntent_*` need real API key in env) unchanged.

## 5. Files

| File | Change |
|---|---|
| `internal/orchestration/judge.go` | `JudgeInput.VibeCase` + propagate |
| `internal/orchestration/llm_client.go` | `JudgeRequest.VibeCase`, `rubricPromptFor()` |
| `internal/orchestration/judge_consensus.go` | `JudgeConsensusInput.VibeCase` + forward |
| `internal/tools/judge.go` | schema: `vibe_case` enum on judge + consensus |
| `CHANGELOG.md` | [Unreleased] entry |
| `~/.config/dark-agent/judge-delegation/vibe-judge.py` | new: rubrics + post-check + consensus |
| `vibe-flow/main/JUDGE_RUBRICS.md` | this doc |

## 6. Operational note

- The Go `vibe_case` ships in the next binary build (v2.12.0-rubrics already
  compiled at `bin/dark-mem-mcp-v2.12.0-rubrics.exe`). Swap into
  `opencode.jsonc` + restart opencode to use `dark_memory_judge(vibe_case="C1")`.
- The agent-layer `vibe-judge.py` works TODAY without restart (reads env fresh).
- Default workflow: use `vibe-judge.py` when a contradiction is plausible
  (large content, technical artifact); `dark_memory_judge(vibe_case=...)` after
  the binary swap for MCP-native judging.
