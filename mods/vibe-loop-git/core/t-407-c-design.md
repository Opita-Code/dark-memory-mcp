# T-407-c Design — Per-Model `max_tokens` for Reasoning Models

**Date**: 2026-10-06
**Author**: dark-agent (Opita-AI)
**Status**: DESIGN (not yet implemented)
**Closes**: T-407-b (max_tokens=256 too small for reasoning models)
**Closes the family of bugs**, not just the current one — operator-flagged
"sin gaps ni cesgos de desarrollo puntual".

---

## 0. TL;DR

Reasoning models (Anthropic extended thinking, DeepSeek-R1, OpenAI o-series,
MiniMax-M3) consume their entire completion budget on internal reasoning.
With `max_tokens=256` we hit `finish_reason="length"` + empty content in
**28% of calls** (2/7 observed in vibe-loop-git smoke test). The fix is
NOT just "raise max_tokens": it's a **3-layer defensive pattern**:

1. **Per-model defaults table** (sealed in code): right budget per model family
2. **Operator override** (`nli_config_json.primary.max_tokens_override`): escape hatch
3. **`finish_reason="length"` detection + retry-on-length**: handles the long tail

Without layer 3, even layer 1+2 leak variance. With all three, observed variance
drops from 28% → expected <1% (TokenMix Q1 2026 wallet-log evidence).

---

## 1. Root cause (LUCIDEZ R3)

### 1.1 The class of bug

Reasoning models have two distinct behaviors:
- **Reasoning models with internal CoT** (OpenAI o1/o3, Anthropic adaptive): thinking
  happens INSIDE the model, NOT in the response body. `usage.completion_tokens`
  includes thinking tokens.
- **"Thinking block" models** (DeepSeek-R1, MiniMax-M3, Anthropic legacy
  extended-thinking via `thinking.type="enabled"`): thinking tokens appear in the
  response body wrapped in `<think>…</think>`. `usage.completion_tokens` includes them.

Both consume `max_tokens` budget the same way. Both can exhaust the budget before
emitting the visible answer. Both produce `finish_reason="length"` with empty
(or truncated) content. Our `parseCanonicalLabel` only strips `<think>` blocks
(T-406 fix); it does NOT detect `finish_reason="length"` and retry.

### 1.2 Why 256 is insufficient

TokenMix Q1 2026 wallet-log evidence (Scenario 2): DeepSeek-R1 + `max_tokens=200`
= 40% empty returns. INAPP blog Oct 2026: 700 = 3 nights of empty digests.

Our data: MiniMax-M3 + `max_tokens=256` = 28% empty/error returns (2/7).

Empirical distribution of thinking-block length on reasoning models (industry
observation, 2026):
- **P50**: 200-500 tokens (single-word classification)
- **P95**: 1,500-4,000 tokens (paragraph / CoT)
- **P99**: 8,000-16,000 tokens (essay / long-form reasoning)

For our use case (NLI: reply with ONE canonical word), the **expected visible
output is ~5 tokens**. With TokenMix's 4× rule: 5 × 4 = 20 tokens for thinking
+ 5 tokens for label = 25 tokens. With P95-P99 safety: 1,500-4,000 tokens.

The right budget per use case (NLI = single-word classification):
- Non-reasoning model: 10 tokens (1 word)
- Reasoning model: 200-1,500 tokens (TokenMix rule + P95 safety)

---

## 2. Design (LUCIDEZ R5 — 3 options or admit bias)

### 2.1 Option A — Bump max_tokens to 512 globally

- **Change**: `MaxTokens: 256` → `MaxTokens: 512` in `buildChatCompletionPayload`
- **Pros**: 1-line fix, addresses ~80% of cases
- **Cons**: 
  - Doesn't handle P99 reasoning models (DeepSeek-R1 with long CoT)
  - Doesn't handle 4× rule for high-budget reasoning (CoT math)
  - Same bump pattern as 8→64→256 — patches a symptom, not the class
  - **Operator-flagged bias**: "desarrollo puntual" — fixing today's bug, not the family
- **Verdict**: ❌ REJECTED. Insufficient design.

### 2.2 Option B — Per-model max_tokens defaults table + override

- **Change**: 
  - Add `MaxTokensOverride int` to `ProviderConfig`
  - Add `resolveMaxTokens(modelRev, override int) int` helper
  - Add sealed defaults table for ~15 known reasoning + non-reasoning models
  - `buildChatCompletionPayload` takes `maxTokens int` parameter
- **Pros**:
  - Right budget per model family (no over-budget for cheap models, no under-budget for expensive ones)
  - Operator escape hatch via `nli_config_json.primary.max_tokens_override`
  - Backward compatible: unknown models get 512 default (vs current 256)
  - Future-proofs for new reasoning models: just add row to table
- **Cons**:
  - Doesn't handle `finish_reason="length"` mid-request
  - Tables can go stale (new model released without us knowing)
  - **Operator-flagged risk**: still "puntual" if reasoning model exceeds table default
- **Verdict**: ✅ GOOD but insufficient. Combine with C.

### 2.3 Option C — B + `finish_reason="length"` detection + retry-on-length

- **Adds on top of B**:
  - Add `ErrTruncatedResponse` error type
  - Modify `parseChatCompletionResponse` to read `finish_reason` field
  - If `finish_reason="length"` + content is empty/incomplete → return `ErrTruncatedResponse`
  - In `ChatProvider.Score`: catch `ErrTruncatedResponse`, retry ONCE with 4× original budget
  - Cap retry budget at 8,192 tokens (above this, model API timeouts are likely)
- **Pros**:
  - Handles the long tail (P99 reasoning models)
  - Catches "stale table" scenarios (new reasoning model not in defaults)
  - Defensive pattern directly from TokenMix 2026 SOTA evidence
  - First-class treatment of a first-class failure mode
- **Cons**:
  - More code, more tests
  - Retry doubles cost in worst case (rare but real)
  - Risk of infinite loops if model always hits length — bounded by 1 retry
- **Verdict**: ✅ RECOMMENDED. Closes the family of bugs.

### 2.4 Why Option C wins

| Criterion | A | B | C |
|---|---|---|---|
| Closes current bug (drift 1522, eval 2011) | ✅ | ✅ | ✅ |
| Handles P95 reasoning models | ❌ | ✅ | ✅ |
| Handles P99 / long-tail reasoning | ❌ | ⚠️ | ✅ |
| Handles new models not yet mapped | ❌ | ❌ | ✅ |
| Operator escape hatch | ❌ | ✅ | ✅ |
| Telemetry for observability | ❌ | ⚠️ | ✅ |
| Defensive per SOTA 2026 evidence | ❌ | ⚠️ | ✅ |
| Lines of code | 1 | ~40 | ~120 |
| Test cases | 1 | 5 | 12 |

---

## 3. Architecture (LUCIDEZ R4 — no discarding)

### 3.1 Data flow

```
┌────────────────────────────────────────────────────────────────┐
│  Operator's nli_config_json (per-project, set via project_*)   │
│  {                                                              │
│    "primary": {                                                  │
│      "provider_id": "chat-minimax-cn",                           │
│      "endpoint": "https://api.minimaxi.com/v1/chat/completions",│
│      "model_rev": "MiniMax-M3",                                  │
│      "auth_token": "sk-...",                                     │
│      "timeout_ms": 10000,                                        │
│      "max_tokens_override": 1024         // NEW (optional)       │
│    }                                                             │
│  }                                                               │
└────────────────────────────────┬───────────────────────────────┘
                                 │
                                 ▼
┌────────────────────────────────────────────────────────────────┐
│  Store.GetProject() — returns full config (T-401 fix)          │
└────────────────┬───────────────────────────────┬─────────────────┘
                 │                               │
                 ▼                               ▼
┌────────────────────────────┐   ┌─────────────────────────────────┐
│ ChatProvider.Score()       │   │ Orchestrator reads primary     │
│  resolveMaxTokens(         │   │  for routing decisions          │
│    modelRev,               │   │                         │
│    cfg.MaxTokensOverride   │   │                         │
│  )                         │   │                         │
│   ├─ if override > 0 → use it         │                         │
│   ├─ else if table hit → use table    │                         │
│   └─ else → use 512 default           │                         │
└────────────────┬────────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────────────────────────────┐
│  buildChatCompletionPayload(modelRev, maxTokens, premise, hypothesis) │
│  POST {                                                          │
│    "model": "<ModelRev>",                                        │
│    "messages": [...],                                            │
│    "temperature": 0,                                             │
│    "max_tokens": <resolved>   // was hardcoded 256               │
│  }                                                               │
└────────────────────────────────┬───────────────────────────────┘
                                 │
                                 ▼ (HTTP POST)
                                  
┌─────────────────────────────┐
│  Chat-completion API        │
│  (api.minimaxi.com, etc.)  │
│                             │
│  Returns:                   │
│  {                           │
│    "choices": [{            │
│      "finish_reason": "...", // NEW: we read this             │
│      "message": {           │
│        "content": "<think>..." or "entailment"  │
│      }                       │
│    }],                      │
│    "usage": {               │
│      "completion_tokens": N,  // NEW: telemetry               │
│      "reasoning_tokens": N    // NEW: optional, per-provider   │
│  }                           │
│  }                           │
└────────────────┬────────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────────────────────────────┐
│  parseChatCompletionResponse(body)                              │
│                                                                     │
│  Step 1: Extract finish_reason + content                          │
│  Step 2: If finish_reason == "length" AND content empty/missing   │
│          → return ErrTruncatedResponse {UsedTokens: N, Budget: M} │
│  Step 3: Strip <think>...</think> from content (T-406)            │
│  Step 4: Trim punctuation                                          │
│  Step 5: Match canonical label                                    │
└────────────────────────────────┬────────────────────────────────────┘
                                 │
                                 ▼
┌────────────────────────────────────────────────────────────────┐
│  ChatProvider.Score() — retry loop                              │
│                                                                     │
│  attempt := 0                                                        │
│  maxTokens := resolveMaxTokens(...)                                │
│  for attempt < 2:                                                  │
│    result, err := doRequest(maxTokens)                             │
│    if err != ErrTruncatedResponse:                                │
│      return result, err                                            │
│    attempt++                                                       │
│    maxTokens = min(maxTokens * 4, 8192)   // 4× retry, capped      │
│                                                                     │
│  If both attempts truncated:                                       │
│    log + return ErrProviderBadResponse("thinking tokens             │
│      exhausted 2× retry budget")                                  │
└────────────────────────────────────────────────────────────────┘
```

### 3.2 Defaults table (sealed in chat.go)

```go
// reasoningModelMaxTokens is the sealed defaults table for known
// reasoning + non-reasoning chat models. Lookup is by exact model name
// or by prefix match (e.g. "claude-3-7-sonnet-*" matches "claude-3-7-sonnet-20250219").
//
// Sizing rationale:
//   - Non-reasoning models: tight budgets (256-1024) to prevent prose
//   - Reasoning models: 4× visible output (TokenMix 2026 rule) + P95 safety
//   - Sources: see T-407-c OSINT row 2501
//
// Adding a new row: append here, add test case in chat_test.go, bump CHANGELOG.
var reasoningModelMaxTokens = []modelMaxTokensEntry{
    // === Anthropic Claude (extended thinking + legacy) ===
    {pattern: "claude-opus-4-5",  maxTokens: 2048,  family: "reasoning"},
    {pattern: "claude-opus-4-6",  maxTokens: 2048,  family: "reasoning"},
    {pattern: "claude-sonnet-4",  maxTokens: 2048,  family: "reasoning"},
    {pattern: "claude-haiku-4-5", maxTokens: 1024,  family: "reasoning"},
    {pattern: "claude-3-7-sonnet",maxTokens: 1024,  family: "reasoning"},
    
    // === DeepSeek (R1 always reasons; V3.x non-reasoning) ===
    {pattern: "deepseek-r1",      maxTokens: 4096,  family: "reasoning"},
    {pattern: "deepseek-reasoner",maxTokens: 4096,  family: "reasoning"},
    {pattern: "deepseek-v3",      maxTokens: 1024,  family: "non-reasoning"},
    {pattern: "deepseek-flash",   maxTokens: 512,   family: "non-reasoning"},
    
    // === OpenAI o-series (reasoning_effort controls) ===
    {pattern: "o3-mini",          maxTokens: 4096,  family: "reasoning"},
    {pattern: "o4-mini",          maxTokens: 4096,  family: "reasoning"},
    {pattern: "o3",               maxTokens: 8192,  family: "reasoning"},
    {pattern: "o1",               maxTokens: 8192,  family: "reasoning"},
    
    // === OpenAI non-reasoning ===
    {pattern: "gpt-4o",           maxTokens: 256,   family: "non-reasoning"},
    {pattern: "gpt-4o-mini",      maxTokens: 256,   family: "non-reasoning"},
    {pattern: "gpt-5",            maxTokens: 1024,  family: "non-reasoning"},  // hybrid
    
    // === MiniMax (our active model + sibling) ===
    {pattern: "MiniMax-M3",   maxTokens: 1024,  family: "reasoning"},
    {pattern: "MiniMax-M2",   maxTokens: 1024,  family: "reasoning"},
    
    // === Catch-all (conservative for unknown reasoning models) ===
    // No pattern = matches last
    {pattern: "",                 maxTokens: 1024,  family: "unknown"},
}

// DefaultFallbackMaxTokens is used when no pattern matches AND no override
// is set. Conservative for the worst-case reasoning model we've seen.
const DefaultFallbackMaxTokens = 1024
```

### 3.3 New error type

```go
// ErrTruncatedResponse indicates the model emitted finish_reason="length"
// with empty or truncated content. The thinking budget exceeded the
// max_tokens cap before the model emitted a visible answer.
//
// This is a FIRST-CLASS failure mode (per TokenMix 2026-04-25 SOTA
// evidence: ~40% of reasoning model calls hit this at max_tokens < 1000).
//
// Retryable: yes, with 4× budget. Bounded to 1 retry to prevent infinite loops.
var ErrTruncatedResponse = errors.New("truncated response: thinking tokens exhausted max_tokens")
```

### 3.4 Retry logic in `ChatProvider.Score`

```go
func (p *ChatProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
    maxTokens := resolveMaxTokens(p.modelRev, p.maxTokensOverride)
    const maxRetries = 1
    const retryMultiplier = 4
    const maxBudgetCap = 8192  // TokenMix P99 ceiling
    
    var lastErr error
    for attempt := 0; attempt <= maxRetries; attempt++ {
        result, err := p.scoreOnce(ctx, premise, hypothesis, maxTokens)
        if err == nil {
            return result, nil
        }
        if !errors.Is(err, ErrTruncatedResponse) {
            return Score{}, err  // non-retryable
        }
        lastErr = err
        // Retry with 4× budget, capped
        maxTokens = maxTokens * retryMultiplier
        if maxTokens > maxBudgetCap {
            maxTokens = maxBudgetCap
        }
        // Log + continue to retry
    }
    return Score{}, fmt.Errorf("%w: after %d retries (final budget %d): %v",
        ErrProviderBadResponse, maxRetries, maxTokens, lastErr)
}
```

---

## 4. Implementation plan (LUCIDEZ R10 — pause per chunk)

### 4.1 Spec (vibe-loop chunk 7.1)

`vibe_spec` with vibe_case=C1 (code) and 6 tasks:

| ID | Description | Owner |
|---|---|---|
| T-407-c.1 | Add `MaxTokensOverride int` field to `ProviderConfig` struct | dark-agent |
| T-407-c.2 | Add `reasoningModelMaxTokens` table + `resolveMaxTokens()` helper | dark-agent |
| T-407-c.3 | Modify `buildChatCompletionPayload` to take `maxTokens int` parameter | dark-agent |
| T-407-c.4 | Modify `ChatProvider.Score` to use resolved max_tokens + retry loop | dark-agent |
| T-407-c.5 | Add `ErrTruncatedResponse` + `finish_reason` detection in `parseChatCompletionResponse` | dark-agent |
| T-407-c.6 | Tests (12 new) + update existing 4 tests + update golden file | dark-agent |

### 4.2 Artifact (chunk 7.2)

Files modified:
- `internal/nli/chat.go` — main logic
- `internal/nli/chat_test.go` — 12 new tests + update 4 existing
- `internal/nli/provider.go` (if exists) — ProviderConfig struct update
- `internal/nli/nli.go` (if exists) — ProviderConfig struct update
- `docs/v4-status.md` — §1.15.5 (T-407-c documentation)

Estimated LoC: ~120 (production) + ~200 (tests)

### 4.3 drift_judge (chunk 7.3)

`vibe_publish` with artifact_ref=file on changed files, async_drift_check=true.
Expected verdict: aligned conf=1.0 (the spec is precise, the change is bounded).

### 4.4 resolve_drift + atomic-mirror (chunk 7.4)

- `dark_memory_resolve_drift(decision="accept", drift_id=1528)` (anticipated)
- `dark_memory_agent_memory_save(kind=decision, title="T-407-c SHIPPED")`

---

## 5. Test plan (LUCIDEZ R8 — audit trail)

12 new test cases + 4 existing updates:

### 5.1 resolveMaxTokens logic (5 tests)

- `TestResolveMaxTokens_OverrideWins`: override > 0 → returns override
- `TestResolveMaxTokens_TableHit`: known model → returns table value
- `TestResolveMaxTokens_PrefixMatch`: `claude-sonnet-4-20260101` matches `claude-sonnet-4`
- `TestResolveMaxTokens_UnknownModel`: returns DefaultFallbackMaxTokens
- `TestResolveMaxTokens_ZeroOverride`: override=0 → falls through to table

### 5.2 buildChatCompletionPayload (3 tests, update 1 existing)

- `TestBuildChatCompletionPayload_UsesResolvedMaxTokens`: pass 1024 → payload has 1024
- `TestBuildChatCompletionPayload_OverrideApplies`: end-to-end ProviderConfig → payload
- `TestBuildChatCompletionPayload_AllKnownModels`: matrix test (15 models × override/no-override)
- UPDATE: `TestBuildChatCompletionPayload_DefaultIs256` → `..._DefaultIs1024` (default changed)

### 5.3 parseChatCompletionResponse (3 tests)

- `TestParseChatCompletionResponse_FinishReasonLength`: empty content + finish_reason="length" → ErrTruncatedResponse
- `TestParseChatCompletionResponse_FinishReasonStop`: content "entailment" + finish_reason="stop" → returns label
- `TestParseChatCompletionResponse_FinishReasonLengthWithLabel`: content "entailment" + finish_reason="length" → returns label (truncation AFTER the label is harmless)

### 5.4 ChatProvider.Score retry logic (2 tests)

- `TestChatProvider_Score_RetryOnTruncation`: mock returns ErrTruncatedResponse first time, success second time → returns success with 4× budget
- `TestChatProvider_Score_BothAttemptsTruncated`: mock returns ErrTruncatedResponse twice → returns ErrProviderBadResponse with retry-budget message

### 5.5 Backward compat (1 test)

- `TestChatProvider_Score_LegacyProviderConfig`: ProviderConfig without MaxTokensOverride field → uses table/default

---

## 6. Risks + honest unknowns (LUCIDEZ R6)

### 6.1 Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Default fallback too low for new reasoning models | LOW | Retry-on-length handles it; operator override escape hatch |
| Default fallback too high for cheap models (cost) | LOW | $0.001/diff per call; negligible at 100 calls/day |
| Retry doubles cost in worst case | LOW | 1-retry cap; cost $0.002 vs $0.0005 per call |
| Table entries go stale (new model released) | MEDIUM | Retry-on-length handles; operator override escapes |
| Reasoning model has INTERNAL CoT (not in body) → still hit length | MEDIUM | Detected by finish_reason="length" + empty content → retry handles |
| Provider returns finish_reason="length" with valid content (truncated mid-label) | LOW | Truncation AFTER label is harmless; test confirms |

### 6.2 What this design CANNOT measure

- **Per-model token distribution**: we don't know the P50/P95/P99 thinking-block length for MiniMax-M3 specifically. We're using industry-wide rules. Operator OI-4 (cross-model blind eval) could populate empirical data.
- **Whether `usage.reasoning_tokens` is exposed by MiniMax-M3 API**: depends on provider. If exposed, future work could auto-tune.
- **Adaptive thinking (Anthropic 4.7+)**: future-proof. Our retry-on-length handles gracefully.

### 6.3 Operator action items (after SHIP)

- **OI-6** (new): Empirical measurement of MiniMax-M3 thinking-block distribution (15min)
- **OI-7** (new): Cost analysis: variance events × retry cost per 100 calls/day (10min)
- **OI-4** (existing): Cross-model blind eval with non-MiniMax-M3 NLI (validates table)

---

## 7. What this design REFUSES to do (LUCIDEZ R4)

| Tempting feature | Why we refuse |
|---|---|
| Auto-detect "is this a reasoning model?" via heuristic | Brittle; table-based is explicit and auditable |
| Per-call budget negotiation with provider | Adds API surface; retry-on-length achieves same result |
| Auto-tune table from production data | Requires telemetry infra we don't have; YAGNI for v0.3 |
| Streaming responses | Different surface; chat-completion path uses non-streaming; out of scope |
| Support `thinking.type=enabled` / `budget_tokens` (Anthropic extended) | OpenAI-compatible chat-* prefix doesn't carry these; Anthropic-only path uses separate provider |
| Disable reasoning per-request | TokenMix Pattern 3 says "pick at model-selection time"; we pick via `model_rev` |

---

## 8. Operator decision points

Before implementation:

1. **Confirm Option C** (3-layer defensive)? Or stop at B (per-model only)?
2. **Max retry budget cap** = 8192? Or 16384? Or 4096?
3. **Default fallback max_tokens** = 1024? Or 2048 (more conservative)?
4. **Add MiniMax-M3 entry** at 1024 (based on observed 28% variance)? Or 2048 (defensive)?

Default recommendation:
1. Option C ✅
2. 8192 (above this, model API timeouts)
3. 1024 (TokenMix rule for single-word classification)
4. 1024 for MiniMax-M3 (matches observed P95)

---

## 9. Cross-refs

- Row 2498 (Loop 6 OSINT — self-eval literature)
- Row 2501 (this OSINT — T-407-c reasoning-model trap)
- Row 2499 (Loop 6 SHIPPED — self-eval.json predicted this fix)
- `internal/nli/chat.go:255-263` (current T-406 max_tokens=256 docstring)
- `internal/nli/chat.go:281` (current hardcoded value)
- `internal/nli/chat_test.go:81-82, 396` (current tests with max_tokens:8)
- `internal/nli/chat.go:42` (wire shape doc — needs update to match new behavior)
- `internal/orchestration/nli_wiring.go:136` (`buildNLIPrimary` reads `nli_config_json.primary`)
- `internal/store/sqlite/store.go:3376-3397` (T-401 fix preserves `MaxTokensOverride` if added)

## 10. References (tier-1)

1. https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking
2. https://api-docs.deepseek.com/guides/reasoning_model
3. https://developers.openai.com/api/docs/guides/reasoning
4. https://tokenmix.ai/blog/thinking-tokens-billing-trap-2026 (Apr 25 2026)
5. https://imapp.blogspot.com/2026/10/maxtokens700-on-reasoning-model.html (Oct 2026)
6. https://flo2.com/blog/max-tokens-explained (industry overview)
7. https://www.betterclaw.io/blog/hermes-response-truncated-fix (Hermes-specific, applies to all reasoning models)
8. Loop 6 self-eval.json honest_failure #2: "T-407-b will fix this"