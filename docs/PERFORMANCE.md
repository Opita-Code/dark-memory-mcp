# Performance — P50 / P99 Targets

> **Audience**: operator sizing capacity + contributor optimizing hot
> paths. Targets are measured on `go test ./tests/dual_driver/...` and
> `tests/orchestration/...`. Numbers below are **goals**, not
> contractual SLAs.

## Why performance matters here

A multi-agent system that retrieves 50 items per recall wastes tokens
on dupes, low-signal items, and verbose content. Latency directly
translates to per-task cost (LLM bound) and operator-visible hang
(memory bound). Both budgets must be tracked.

---

## Targets (per RFC §6)

| Operation | Driver | Row count | P50 | P99 |
|---|---|---|---|---|
| `SaveRun` | sqlite | 10k | < 5ms | < 20ms |
| `SaveRun` | postgres | 100k | < 10ms | < 50ms |
| `SaveSpec` / `SaveArtifact` | sqlite | 10k | < 5ms | < 20ms |
| `SaveDriftReport` | sqlite | 10k | < 8ms | < 30ms |
| `Recall` (default) | sqlite | 10k | < 10ms | < 50ms |
| `Recall` (default) | postgres | 100k | < 30ms | < 150ms |
| `PublishVibe` (end-to-end) | both | — | < 1500ms (LLM-bounded) |
| 1000-call stress test | both | — | no deadlock, no panic |

`PublishVibe` is dominated by the LLM judge call (3 judges in worst
case × ~500ms per call). The DB portion is < 50ms.

---

## Where the budget goes

### Hot path: `Save*` (~5-10ms P50)

```
WriteContext build          ~1µs (struct literal)
INV-3 canary check           ~5µs (substring match)
INSERT into data table       ~1-3ms (SQLite fsync / Postgres roundtrip)
INSERT into write_audit       ~1-3ms (atomic; same TX)
RETURN id                   ~100µs (last_insert_rowid)
Total                       ~3-8ms P50
```

The two INSERTs are the dominant cost. INV-1 forces them to be in the
same transaction — that's correctness, not optional. If you need
faster writes, the right lever is **batching**, not skipping the audit.

### Hot path: `Recall` (~10-30ms P50)

```
Query parse                  ~500µs
SELECT items                 ~3-10ms (indexed on session_id)
Economy Atlan pipeline:
  dedup                      ~500µs (hash + map)
  filter_confidence >= 0.5   ~100µs
  truncate per item to 500c  ~500µs
  compress (title + summary) ~1ms (struct allocation)
  cap to 10 items            ~10µs
Total                       ~5-15ms P50
```

The Atlan pipeline is the budget we accept in exchange for LLM token
savings downstream.

---

## Operational metrics (what to graph)

In production, expose these on a dashboard:

| Metric | Source | Alert if |
|---|---|---|
| `Save*` P50 / P99 | `write_audit` `created_at` diff | P99 > 50ms (sqlite) / 100ms (postgres) |
| `Recall` P50 / P99 | `dark-research-mcp` access log | P99 > 100ms (sqlite) / 200ms (postgres) |
| `write_audit` rows/min | `COUNT(*)` over 1m window | < expected (Store may be wedged) |
| Active canary present | inspect `canary_present` field | `true` (means a payload tripped INV-3) |
| INV-4 drift | inspect `constitution_drift` field | `true` (server refuses to boot) |
| Sessions active | inspect `counts.sessions_active` | > 100 (likely leaked sessions) |

---

## Profiling recipes

```bash
# CPU profile (10s, 100Hz → 1000 samples)
go test -cpuprofile=/tmp/cpu.prof -bench=BenchmarkSaveRun ./tests/dual_driver/...
go tool pprof -top /tmp/cpu.prof

# Memory profile
go test -memprofile=/tmp/mem.prof -bench=BenchmarkSaveRun ./tests/dual_driver/...
go tool pprof -top /tmp/mem.prof

# Trace (goroutine + syscall + heap events)
go test -trace=/tmp/trace.out -bench=BenchmarkRecall ./tests/dual_driver/...
go tool trace /tmp/trace.out
```

For dark-mem-mcp in production:

```bash
# Enable pprof endpoint (server-side, not exposed today — Wave 4+):
DARK_PPROF=:6060 ./dark-mem-mcp
curl http://127.0.0.1:6060/debug/pprof/profile?seconds=30 > /tmp/cpu.prof
```

---

## Anti-patterns (do not do these)

1. **Skipping the audit insert** "to save 5ms" → breaks INV-1, audit
   trail is gone, post-mortem impossible.
2. **Concurrent goroutines inside an orchestrator** → race on
   write_audit row_id; SQLite single-writer mutex is the ceiling.
3. **`SELECT *` in `Recall`** → 50-row payload the Atlan pipeline
   must then truncate. Always project to the columns the context
   needs.
4. **Long-lived connections to Postgres without prepared
   statements** → each query re-parses the plan; 5× slower than
   prepared. The pgx pool already does prepared automatically.
5. **`VACUUM` on hot path** → Postgres `autovacuum` is fine;
   `VACUUM FULL` rewrites the table and locks it. Never run on a
   live DB.

---

*See also: [RUNBOOK.md](./RUNBOOK.md) · [INVARIANTS.md](./INVARIANTS.md) · [MIGRATION.md](./MIGRATION.md)*