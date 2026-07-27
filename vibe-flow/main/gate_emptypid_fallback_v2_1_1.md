# vibe-spec: GateMiddleware empty project_id fallback (v2.1.1)

**Owner**: Nico
**Trigger**: After shipping v2.1.0 (agent_memory), `agent_memory_save` and
`session_status` calls return `ErrFrameStaleTooFar` despite a valid
active session in the DB. Root cause: `GateMiddleware.buildGateInput`
calls `ActiveSessionResolver.ActiveSessionID(ctx, pid)` where `pid`
is `args.project_id` (empty for tools that don't take project_id
explicitly). `StoreBackedActiveSessionResolver.ActiveSessionID` short-
circuits on `projectID == ""` and returns `""` without consulting
the store. The gate then constructs a GateInput with `SessionID=""`
and `ProjectID=""` — both are treated as "no session" by PreCheck,
which refuses the call.

This is a **v2.0.2 regression** that the existing test suite didn't
catch because:
  - All session-required tools in v2.0.2's tests pass `project_id`
    explicitly in args (e.g. `vibe_publish`, `vibe_spec`).
  - The new `agent_memory_*` tools (v2.1.0) intentionally omit
    `project_id` from args — they derive it from the active project
    internally (INV-7 enforces it at the Store layer).

## Symptom (reproduced)

1. Operator restarts opencode, MCP loads v2.1.0 binary cleanly
   (`health_ping` → `server.version=2.1.0`, `schema_version=18`).
2. Operator calls `session_start(project_id=default)` → success,
   DB shows `projects.active_session_id=sess-13a249d368965661`,
   `sessions.status=open`.
3. Operator calls `session_status()` (no project_id in args) →
   `ErrFrameStaleTooFar`. Same for `agent_memory_save`,
   `agent_memory_list`, `agent_memory_get`, `agent_memory_update`,
   `agent_memory_archive`, `session_close`.
4. Read-only tools without session requirement (`health_ping`,
   `memory_state`, `active_policy`, `admin_schema_status`) work
   fine — gate allowlist bypasses session requirement for those.

## Root cause

Two files collaborate to break the flow:

**`internal/server/middleware.go:194-200`** — `buildGateInput`:
```go
if id, ok := in.Args["session_id"].(string); ok && id != "" {
    in.SessionID = id
} else if m.ActiveSession != nil {
    pid, _ := in.Args["project_id"].(string)
    in.SessionID = m.ActiveSession.ActiveSessionID(ctx, pid)  // pid=""
}

if pid, ok := in.Args["project_id"].(string); ok {
    in.ProjectID = pid  // "" if not in args
}
```

**`internal/server/active_session_resolver.go:150-153`** — resolver:
```go
func (r *StoreBackedActiveSessionResolver) ActiveSessionID(ctx context.Context, projectID string) string {
    if projectID == "" {
        return ""  // short-circuit; no DB lookup
    }
    ...
}
```

The middleware passes `""` to the resolver; the resolver returns `""`
without consulting `Store.GetActiveSession(ctx, "")` (which would
have looked up the active project's session anyway). Result:
`SessionID=""`, `ProjectID=""`, gate refuses as "no session".

## Goal

Make `buildGateInput` fall back to the **active project** when args
don't carry `project_id`. The active project is exposed by the
Store (`Store.ActiveProject()`); the middleware needs a way to
read it without taking a direct Store dependency.

Smallest change that fixes the bug without leaking the Store into
the gate layer:

  1. Add `ActiveProject func() string` field to `GateMiddleware`
     (mirrors the existing `ActiveConstitution func() (id, ver string)`
     pattern).
  2. In `buildGateInput`, when args has no `project_id`, call
     `m.ActiveProject()` to get the active project_id. Use it as
     the resolver's `projectID` argument AND as `in.ProjectID`.
  3. Wire `ActiveProject: bootState.Store.ActiveProject` in
     `cmd/dark-mem-mcp/main.go`.
  4. The resolver's `projectID == ""` short-circuit now only fires
     when there is genuinely no active project (e.g. very first
     boot before any session_start) — which is the original intent.

## Why not change the resolver

The resolver's `projectID == ""` short-circuit was added (correctly)
to avoid a useless "no project" DB lookup for `session_start` itself,
which has no project context yet. Removing the short-circuit would
break the bootstrap case. Patching the middleware keeps the
resolver's semantics clean and localizes the fix.

## Non-goals

- Don't change the resolver's cache strategy (5s TTL).
- Don't change the GateInput contract — just fix how it's populated.
- Don't add new public APIs on the resolver.

## Acceptance criteria

1. `agent_memory_save` succeeds after `session_start(default)` is
   called with no `project_id` argument. End-to-end verified via
   `dark_memory_agent_memory_save` MCP call returning a non-error
   response with an `id` field.
2. `session_status()` (no project_id) succeeds after
   `session_start(default)`.
3. Existing tests still pass: `go test ./internal/server/...`.
4. New regression test: `TestGateMiddleware_BuildGateInput_NoProjectIDInArgs_FallsBackToActiveProject`
   exercises the exact bug path (StaticSessionResolver returning
   a fixed session_id, ActiveProject getter returning "default",
   args has no project_id → GateInput.SessionID == "sess-test",
   GateInput.ProjectID == "default").

## Files touched

- `internal/server/middleware.go` — add `ActiveProject` field +
  fallback logic (~12 LOC).
- `cmd/dark-mem-mcp/main.go` — wire `ActiveProject` (1 LOC).
- `internal/server/middleware_test.go` — regression test (~25 LOC).
- `CHANGELOG.md` — entry under `[2.1.1]`.
