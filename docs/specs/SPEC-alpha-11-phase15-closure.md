# SPEC-alpha-11-phase15-closure — close the 3 deferrals from Phase 14 §1.13.5

**Status:** DRAFT
**Phase:** 15 (house-keeping closure)
**Pre-tag:** `v4.0.0-alpha.26-pre-1` → final `v4.0.0-alpha.26`
**Owner:** dark-agent + nico
**Created:** 2026-10-06
**Closes:** row 2464 §1.13.5 deferral list (3 actionable items)

---

## TL;DR

Phase 15 closes the 3 deferrals left over from Phase 14 §1.13.5:

| # | Title | Source deferral |
|---|---|---|
| **T-401** | Postgres parity — 3 `notImpl` methods (`ListAgentMemoryByAnyEntity`, `MarkSupersededAgentMemory`, `RecallAtTime`) | row 2464 §1.13.5 |
| **T-402** | AutoEmitter `EmitCacheInvalidation` wire-up (the **8th / 8th** orphan) | row 2464 §1.13.5 |
| **T-403** | NLI `CachedProvider.ConcurrentGet_RaceFree` flaky fix (single-flight dedup) | row 2464 §1.13.5 |
| **T-404** | Docs sweep — `CHANGELOG [4.0.0-alpha.26]` + `docs/v4-status.md` §1.14 | house-keeping |

**Out of scope (documented constraints, NOT deferrals):**
- 95% Chunk 8.6 coverage ceiling (accept 91.9% — structural — row 2464 §1.13.5).
- DirectML EP selection (blocked by upstream `yalue/onnxruntime_go` API surface).
- AutoEmitter wire-up count prior to T-402: **7 of 8 wired**; after T-402: **8 of 8 wired**.

**Cross-version lockstep hash pin** stays UNCHANGED through Phase 15 (no canonical tool changes).

---

## Background — what Phase 14 left open

Phase 14 SHIPPED (`v4.0.0-alpha.25`, 2026-10-06, row 2464) added LLM-as-judge
+ Connect flow. The §1.13.5 deferral list documented 3 actionable items
that were out of scope for the "ship the truth" push but are now ready
to close:

1. **Postgres parity** — the PG `Store` returns `notImpl("…")` for 3 methods
   that the SQLite `Store` already implements. PG is the production path
   for multi-agent load (MVCC, row-level locks) but operators get a clear
   error if they hit these methods. Close the gap.

2. **AutoEmitter cache invalidation orphan** — 7 of 8 emitter helpers are
   wired to actual event sites (`EmitSupersede` at bitemporal.go:202,
   `EmitDecayRefresh` at decay.go:118, `EmitSchemaMigration` at
   migrate.go:94, `EmitEmbedderRefresh` at c2_text.go:232, `EmitCalibrationUpdate`
   at judge/store.go:675, `EmitJudgeVerdictUpdate` at judge/store.go:419,
   `EmitPersonaUpdate` at personas_v4.go:166). The 8th
   (`EmitCacheInvalidation`) is defined in `auto_emit.go:256` but never
   called from any cache eviction site.

3. **NLI CachedProvider race** — `TestCachedProvider_ConcurrentGet_RaceFree`
   (`internal/nli/cached_provider_test.go:203`) expects exactly 1 inner
   call when 100 goroutines hit the cache with identical
   `(premise, hypothesis)`. Today it flakes because there is no
   single-flight dedup — every miss-then-call goroutine invokes `inner`
   independently. The race detector flags the data race on the inner
   counter, and the count assertion is non-deterministic.

---

## T-401 — Postgres parity for 3 notImpls

### Goal

Replace 3 PG stubs (PG full implementations of methods already in SQLite):

- `internal/store/postgres/store.go:422` — `ListAgentMemoryByAnyEntity`
- `internal/store/postgres/store.go:444` — `MarkSupersededAgentMemory`
- `internal/store/postgres/store.go:462` — `RecallAtTime`

Each gets a real pgxpool implementation mirroring the SQLite version.

### Files to change

| File | Action |
|---|---|
| `internal/store/postgres/store.go` | Replace 3 `notImpl` returns with real impls |
| `internal/store/postgres/bitemporal_pg_test.go` | NEW — tests for `MarkSupersededAgentMemory` + `RecallAtTime` |
| `internal/store/postgres/entity_pg_test.go` | NEW — tests for `ListAgentMemoryByAnyEntity` |

### Implementation references

- `ListAgentMemoryByAnyEntity` → mirror `internal/store/sqlite/entity.go:201-273` with pgx syntax (`$1`, `$2`, `ANY($1::text[])` for chunked IN)
- `MarkSupersededAgentMemory` → mirror `internal/store/sqlite/bitemporal.go:69-185` with pgx tx + `recordWriteTx`
- `RecallAtTime` → mirror `internal/store/sqlite/bitemporal.go:232-290` with pgx syntax

### Behavior contracts (must match SQLite EXACTLY)

1. Cross-project isolation: `WHERE project_id = $N` on every SELECT/UPDATE.
2. Pre-flight validation before tx (matches sqlite pattern at bitemporal.go:97-124).
3. Tx wraps (a) the data UPDATE/INSERT, (b) the audit row via `recordWriteTx`, (c) the decision_transitions row (for `MarkSuperseded` only).
4. INV-1 audit row in the SAME tx as the data write.
5. `EmitSupersede` fires AFTER tx commit (mirror sqlite bitemporal.go:181-183).
6. Empty-input OR no-match → `(nil, nil)` (NOT an error).
7. `RecallAtTime(t)` with `t.IsZero()` → `store.ErrInvalidArgument`.
8. `MarkSupersededAgentMemory` returns `ErrInvalidSupersession` (wrapped) for: self-supersede, invalid row id, missing row, non-decision kind, cross-project, no-rows-affected race.
9. ListAgentMemoryByAnyEntity: lowercase + dedup + sort + chunk at 200 (matches sqlite MAX_VARIABLE_NUMBER safety).

### Test gates

All 3 PG tests gated by `DARK_TEST_POSTGRES_DSN` env var (CI doesn't have PG).
Operators run locally with `DARK_TEST_POSTGRES_DSN=postgres://… go test ./internal/store/postgres/`.

### TDD 4-layer

- L1 (rapid, 20 iter): empty input, single chunk, multi-chunk, lowercase + dedup, cross-project isolation, tx rollback on error.
- L2 (stdlib boundaries): INV-1 audit row emitted in same tx; `EmitSupersede` only fires on success.
- L3 (mutation ≥0.80): `go-mutesting` against the 3 methods.
- L4 (drift_judge ≥0.85): none (this is a backend parity task, no public surface change).

### Verification target

- 12-15 new tests PASS in ≤1.5s when `DARK_TEST_POSTGRES_DSN` is set.
- Operators running with `DARK_TEST_POSTGRES_DSN=postgres://… go test ./internal/store/postgres/` see all green.
- SQLite impl unchanged (parity = same behavior, not same impl).

---

## T-402 — AutoEmitter `EmitCacheInvalidation` wire-up

### Goal

The 8th / 8th emitter helper (`EmitCacheInvalidation`) is currently
defined in `internal/v4alpha/event/auto_emit.go:256` but has zero callers.
After this task: every LRU eviction in `internal/nli/cache.go` (Put-over-cap
AND Get-TTL-expiry paths) fires `EmitCacheInvalidation`.

### Files to change

| File | Action |
|---|---|
| `internal/nli/cache.go` | Add `AutoEmitter` field to `InMemoryLRU` + `SetAutoEmitter(ae)` setter + emit on Put-eviction + Get-expiry |
| `internal/nli/cache_test.go` | NEW tests verifying EmitCacheInvalidation is called on Put-eviction + Get-expiry |
| `internal/v4alpha/event/auto_emit.go` | Confirm `EmitCacheInvalidation` signature matches new callers (no change expected) |
| `internal/eventholder/holder.go` | Confirm `AutoEmitter` interface already includes `EmitCacheInvalidation` (already does at line 53) |
| `cmd/main.go` (or equivalent boot path) | Wire `cache.SetAutoEmitter(eventholder.Get())` once at startup |

### Wire shape

`EmitCacheInvalidation(ctx, cacheTable, rowID, semantic, reason)`:
- `cacheTable` — `"nli_lru"` for the NLI cache (extensible later for other caches).
- `rowID` — `0` (LRU evicts per-key, not per-row-id; the event is a coarse aggregate).
- `semantic` — `false` (LRU evicts entries, not semantic-relationship nodes).
- `reason` — `"lru_cap"` (over-cap) or `"ttl_expired"` (under-cap TTL hit).

### Test gates

Tests use `eventholder.Set(...)` per-test to install a mock that records emissions.
Tests verify:
- `TestInMemoryLRU_PutEviction_FiresEmitCacheInvalidation` — fill cache to cap, insert one more, expect 1 emit with `reason="lru_cap"`.
- `TestInMemoryLRU_GetTTLExpiry_FiresEmitCacheInvalidation` — insert with TTL=2ms, sleep 5ms, Get, expect 1 emit with `reason="ttl_expired"`.
- `TestInMemoryLRU_NoEmissionWhenNotEvicted` — Put a single entry that doesn't overflow, expect 0 emits.

### Behavior contract

- Emit is **fire-and-forget** (matches existing 7 emit helpers — failures logged, never propagated).
- Emit happens INSIDE the cache `mu.Lock()` critical section (the events table is separate from the cache so this is safe — same posture as `bitemporal.go:194-202`).
- `SetAutoEmitter(nil)` is safe (no-op; cache still works).
- When no AutoEmitter is wired (legacy harnesses), the cache behaves as today (callers see no difference).

### TDD 4-layer

- L1: 3-5 tests as listed above.
- L2: stdlib boundaries — emission never blocks the cache path (capture emit in a buffered channel).
- L3: mutation ≥0.80 on the cache helpers.
- L4: none (no public surface change; events table audit only).

### Verification target

- 3-5 new tests PASS.
- Operators with `eventholder.Set` see the 8th emit firing in their event audit logs.

---

## T-403 — NLI `CachedProvider.ConcurrentGet_RaceFree` flaky fix

### Goal

`TestCachedProvider_ConcurrentGet_RaceFree` (`internal/nli/cached_provider_test.go:203`)
spins up 100 goroutines that all call `Score(premise, hypothesis)` with
identical args. Expects `stub.calls.Load() == 1` (cache should make the
inner call happen exactly once).

Today it flakes because:
1. 100 goroutines all call `cache.Get(key)` → all return `ok=false` (cold).
2. All 100 then call `c.inner.Score(ctx, …)` independently → `stub.calls.Load() == 100`.
3. The race detector flags the data race on the inner counter (`atomic.Int32.Add` itself is race-free, but the **read** of the counter at line 222 happens after `wg.Wait()` so the count is deterministic at 100, not 1).

The fix is **single-flight dedup**: when a goroutine sees a miss AND another goroutine is already fetching the same key, the new goroutine waits for the in-flight caller's result instead of starting its own.

### Files to change

| File | Action |
|---|---|
| `internal/nli/cached_provider.go` | Add `inflight map[Key]*inflightCall` (sync.Map, no global mutex) + `getOrStartInflight` helper + on-call-remove invalidate the in-flight entry |
| `internal/nli/cached_provider_test.go` | Existing `TestCachedProvider_ConcurrentGet_RaceFree` should now PASS deterministically. Add `TestCachedProvider_ConcurrentGet_1000xNoFlake` stress test (1000 iterations of the same scenario). |

### Algorithm

```go
type inflightCall struct {
    done chan struct{}
    score Score
    err  error
}

func (c *CachedProvider) getOrStartInflight(key Key, start func() (Score, error)) (Score, error) {
    if existing, ok := c.inflight.Load(key); ok {
        call := existing.(*inflightCall)
        <-call.done
        return call.score, call.err
    }
    call := &inflightCall{done: make(chan struct{})}
    if _, loaded := c.inflight.LoadOrStore(key, call); loaded {
        // someone else won the race — wait for them
        existing := c.inflight.Load(key).(*inflightCall)
        <-existing.done
        return existing.score, existing.err
    }
    // we won the race — start the inner call
    call.score, call.err = start()
    close(call.done)
    c.inflight.Delete(key)
    return call.score, call.err
}
```

Pattern matches `golang.org/x/sync/singleflight` semantics without adding the dependency.

### Behavior contracts

1. **Identical-key dedup**: N concurrent goroutines with the same `(premise, hypothesis)` → exactly 1 inner call.
2. **Different-key parallel**: N concurrent goroutines with distinct keys → N inner calls (no false sharing).
3. **Errors are NOT shared**: if the in-flight call fails, callers get the same error (matches existing `errors are NEVER cached` invariant — they're still not cached, just shared within the in-flight window).
5. **Context propagation**: callers with `ctx` that's canceled do NOT unblock the in-flight winner; they unblock when the winner completes (the winner uses its OWN ctx). Pattern matches `singleflight.DoChan`.
6. **Cache write-through unchanged**: the winner still does `cache.Put` after success (existing line 86).

### TDD 4-layer

- L1: existing `TestCachedProvider_ConcurrentGet_RaceFree` (must PASS deterministically now). NEW `TestCachedProvider_ConcurrentGet_1000xNoFlake` (1000 iterations of 100 goroutines, count must be `1000*1=1000` inner calls not `1000*100=100000`). NEW `TestCachedProvider_DifferentKeys_ParallelInner` (10 goroutines, 10 different keys, 10 inner calls).
- L2: stdlib boundaries — race detector MUST be clean (`go test -race`).
- L3: mutation ≥0.80 on the single-flight helper.
- L4: none (no public surface change).

### Verification target

- All 3 new tests PASS in ≤2s.
- `go test -race -count=10` PASS (no flakes).
- Existing 8 `TestCachedProvider_*` tests unchanged.

---

## T-404 — Docs sweep

### Goal

`docs/v4-status.md` §1.14 + `CHANGELOG.md` `[4.0.0-alpha.26]` section.
Frozen test stays `TestCanonicalOrder_Frozen_73_20_28` (no tool count change).

### Files to change

| File | Action |
|---|---|
| `CHANGELOG.md` | Append `[4.0.0-alpha.26] — 2026-10-06 — Phase 15: closure of §1.13.5 deferrals` with 4 subsections (T-401 / T-402 / T-403 / T-404) + summary |
| `docs/v4-status.md` | Append §1.14 (1.14.1..1.14.4) matching Phase 14 §1.13 structure. Update top-level inventory + phase banner |
| `docs/specs/SPEC-alpha-11-phase15-closure.md` | This file (already created) |

### LUCIDEZ gate

R1 file:line grounding: every claim links to file + line.
R2 no shallow: each subsection covers root cause + fix + tests + verification.
R3 root cause over symptom: §1.13.5 deferral explanations cite row 2464.
R4 effort never discards truth: T-403 explicitly states the race was a single-flight dedup gap, not a sync.Map bug.
R6 honest cost: PG tests are env-gated, not in CI (matches existing PG tests — `DARK_TEST_POSTGRES_DSN` is required).
R7 audit trail before resolution: each task has its own decision row (saved via `agent_memory_save` at commit time).
R8 unknowns declared: T-401 PG tests depend on operator's local PG instance (not CI).
R10 pause-and-summarize: T-404 CHANGELOG appendix recapitulates operator-facing CLI changes (`VERIFY_PHASE14=1` lives, no new CLI needed).

---

## Non-goals (explicit)

- **No new tools.** 73/20 frozen.
- **No schema changes.** v32 stays v32.
- **No new namespaces.**
- **No `vibe_publish`/`vibe_spec` changes.**
- **No constitutional changes.** Phase 14 §1.13 invariants preserved.

---

## Success criteria

- [ ] 3 PG methods no longer return `notImpl`. Operators running with PG see full parity.
- [ ] 8 of 8 AutoEmitter helpers wired (was 7 of 8).
- [ ] `TestCachedProvider_ConcurrentGet_RaceFree` PASSes deterministically under `-race -count=10`.
- [ ] `CHANGELOG [4.0.0-alpha.26]` published.
- [ ] `docs/v4-status.md` §1.14 published.
- [ ] 5 commits + 1 pre-tag + 1 final tag (`v4.0.0-alpha.26`).
- [ ] Atomic mirror row + sidecar (auto via `agent_memory_save`).
- [ ] Cross-version lockstep hash pin UNCHANGED.

---

## Risk register

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| PG tx ordering diverges from SQLite (INV-1 audit row not in same tx) | low | high | Mirror `bitemporal.go` SQLite pattern; use `recordWriteTx` (existing helper at `store.go:885`). |
| PG-specific SQL syntax error (e.g. `$N` placeholders, `TIMESTAMPTZ` cast) | medium | medium | L1 tests iterate 20x; L3 mutation catches typo-level bugs. |
| `EmitCacheInvalidation` blocks the cache path under load | low | medium | Fire-and-forget (matches other 7 emitters); cache mutex already held so emit is O(1) channel send. |
| Single-flight dedup introduces deadlock on context cancel | low | high | In-flight winner uses its own ctx; waiters block on `chan struct{}` not `ctx.Done()`. |
| Race detector catches new race in single-flight helper | medium | medium | Run `go test -race` 10x. L2 stdlib boundary test ensures channel close happens once. |

---

## Phase 15 timeline

| Date | Action | Commit | Tag |
|---|---|---|---|
| 2026-10-06 | SPEC-alpha-11-phase15-closure.md | (this commit) | — |
| 2026-10-06 | T-401 PG parity | `feat(postgres): T-401` | `v4.0.0-alpha.26-pre-1` |
| 2026-10-06 | T-402 EmitCacheInvalidation wire | `feat(emitter): T-402` | `v4.0.0-alpha.26-pre-2` |
| 2026-10-06 | T-403 CachedProvider single-flight | `fix(nli): T-403` | `v4.0.0-alpha.26-pre-3` |
| 2026-10-06 | T-404 docs sweep | `docs(phase-15): T-404` | `v4.0.0-alpha.26` (final) |

---

## Operator runbook (post-SHIP)

```bash
# T-401 (PG parity — only if you have a local PG)
DARK_TEST_POSTGRES_DSN=postgres://user:pass@localhost:5432/dark_mem go test ./internal/store/postgres/

# T-402 (no operator CLI — verify with auto_emitter_wires_test.go which already covers this)
go test -race ./internal/v4alpha/event/...

# T-403 (flaky fix — verify with the existing test + new stress)
go test -race -count=10 ./internal/nli/

# Phase 15 SHIP verification (cross-cutting)
go vet ./...
go build ./...
go test -race -count=1 -p 1 -short ./...
```