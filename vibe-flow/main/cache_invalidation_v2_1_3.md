# v2.1.3 — Resolver cache invalidation on session state change

## Problem

The first tool call after `session_start` (or `session_close`) returned
`ErrFrameStaleTooFar` ("session or project not bound") within the
`StoreBackedActiveSessionResolver`'s 5s TTL window.

Reproduction (deterministic):

1. Opencode restarts. `s.activeProject = ""`, `projects.active_session_id = NULL`.
2. Operator calls `session_start(operator, project_id="default")`.
3. Wire path: `wrapHandler → GateMiddleware.Wrap → buildGateInput → resolver.ActiveSessionID("default")`.
   - Cache miss (fresh process, empty cache).
   - `Lookup(ctx, "default") → Store.GetActiveSession(ctx, "default")` →
     SELECT returns `active_session_id = NULL` → returns `""`.
   - Cache filled with entry `{sessionID: "", expires: now+5s}`.
   - `buildGateInput` returns `GateInput{SessionID: "", ProjectID: "default"}`.
   - **PreCheck refuses**: SessionID is empty → `ReasonFrameStaleTooFar` →
     "session or project not bound".
4. **Wait**, but `session_start` is in the `RequiresActiveSession` allowlist
   (gate.go:88), so this refusal shouldn't fire. Let me re-check.

Actually re-reading gate.go RequiresActiveSession, `session_start` IS in the
allowlist (line 88), so the gate doesn't enforce SessionID for it. The
inner runs anyway, calls `orch.SessionStart`, which does SetActiveProject →
SaveSession → SetActiveSession (writes DB). Then the call returns
successfully.

The cache is now stale: it has `""` for `"default"`, but the DB has the
new session_id.

5. Operator calls `agent_memory_save(...)` immediately (no explicit args).
6. Wire path: `wrapHandler → GateMiddleware.Wrap → buildGateInput`.
   - `args.project_id` is missing → fall through to `m.ActiveProject()` → `"default"`.
   - `args.session_id` is missing → fall through to `m.ActiveSession.ActiveSessionID(ctx, "default")`.
   - **Cache HIT** (still within 5s TTL) → returns `""`.
   - `GateInput{SessionID: "", ProjectID: "default"}`.
   - `agent_memory_save` is NOT in the `RequiresActiveSession` allowlist.
   - **PreCheck refuses**: SessionID empty → `ReasonFrameStaleTooFar` →
     "session or project not bound".

This is what the operator saw after restart, and what the smoke test
just reproduced.

## Why unit tests passed

The unit test `TestGateMiddleware_BuildGateInput_NoProjectIDInArgs_FallsBackToActiveProject`
exercises `buildGateInput` directly with `ActiveSession: StaticSessionResolver{SessionID: "sess-active"}`.
That returns the configured value regardless of DB state — the cache
staleness is invisible to it.

The e2e gate test `TestE2E_Gate_BareNameGrant` wires a `StoreBackedActiveSessionResolver`
with a stub `ActiveSessionLookup` function. The stub returns the same
value regardless of timing — the cache staleness is invisible to it.

The race only manifests in production where:
- The resolver cache starts empty (fresh process).
- `session_start`'s pre-inner `buildGateInput` populates the cache.
- The inner writes the new value to the DB AFTER.

## Fix

Add a synchronous cache-invalidation callback to the `Orchestrator`:

```go
type Orchestrator struct {
    // ...
    OnActiveSessionChanged func(projectID string)
}
```

Call it from the three write sites that change `projects.active_session_id`:

- `session_start.go` — after `SetActiveSession`.
- `session_close.go` — after `ClearActiveSession`.
- `session_resurrect.go` — after `SetActiveSession`.

Wire in `cmd/dark-mem-mcp/main.go`:

```go
bootState.Orchestrator.OnActiveSessionChanged = activeSessionResolver.Invalidate
```

`resolver.Invalidate(projectID)` is exported (line 195 of
`active_session_resolver.go`). It deletes the cache entry for the project;
the next `ActiveSessionID` call does a fresh DB lookup.

## What this fix does NOT change

- The resolver cache itself (5s TTL, same shape).
- The e2e gate tests (they pass either way).
- Any tool wire contract (callers see the same behavior — the only
  change is that the FIRST call after session_start/session_close now
  succeeds).
- session_sweeper's ClearActiveSession — the sweeper runs in a
  background goroutine, doesn't have access to the resolver. The
  sweeper clears idle sessions that the operator wasn't actively using,
  so the cache staleness window (5s) is not user-visible. Left for a
  future cleanup if it ever matters.

## Test plan

1. Unit test: `TestOrchestrator_SessionStart_InvokesOnActiveSessionChanged`.
   Construct an Orchestrator with a `*sqlitestore.Store` + a spy callback.
   Call `SessionStart`. Assert the spy was called with the right project_id.
2. Unit test: `TestOrchestrator_SessionClose_InvokesOnActiveSessionChanged`.
   Same but for SessionClose.
3. Integration test: `TestResolver_InvalidatedAfterSessionStart`.
   Wire a real resolver + spy cache state. Call ActiveSessionID before
   session_start → cache populated with "". Run session_start. Assert
   the cache is now empty for the project_id (next ActiveSessionID
   does a fresh DB lookup).
4. Smoke test: `session_start → agent_memory_save` within 1s.

## Rollout

- v2.1.3 binary on disk, commit + tag `v2.1.3`.
- Restart opencode to pick up the new binary.
- Smoke test in chat: session_start → save (immediate) → save (immediate).
