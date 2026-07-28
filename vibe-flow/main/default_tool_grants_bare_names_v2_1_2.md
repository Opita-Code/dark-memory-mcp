# vibe-spec: DefaultToolGrants bare names + agent_memory grants (v2.1.2)

**Owner**: Nico
**Trigger**: After shipping v2.1.1 (GateMiddleware ActiveProject fallback),
end-to-end smoke test on the live MCP revealed that
`dark_memory_agent_memory_save` (and every other session-required tool
that isn't in the `RequiresActiveSession=false` allowlist) refused with
`ErrCapabilityNotGranted: tool "agent_memory_save" not in GrantedTools`.

## Root cause

`internal/recall/assemble.go:65` defines:

```go
const DefaultToolGrants = "dark_memory_active_policy," +
    "dark_memory_session_start," +
    ...
    "dark_memory_admin_inspect"
```

The list has the `dark_memory_` wire prefix on every entry.

But `internal/policy/gate.go:296` checks:

```go
Message: fmt.Sprintf("tool %q not in GrantedTools", in.ToolName),
```

where `in.ToolName` is the BARE tool name (e.g. `"agent_memory_save"`,
not `"dark_memory_agent_memory_save"`). Per `internal/tools/registry.go:37`:
> `Name string // Name is the bare tool name WITHOUT the "dark_memory_"
> prefix. The server prepends the prefix when registering with mcp-go`.

`CapabilitiesFrame.HasGrant` (`internal/atomic/capabilities_frame.go:154`)
performs case-sensitive exact match — `dark_memory_` prefix mismatch means
zero tools are granted.

## Why nobody noticed

**Every test that touches `dark_memory_*` tools bypasses the gate.**

- `tests/e2e/server_test.go:304` — `resp, err := t.Handler(ctx, raw)`
- `tests/dual_driver/agent_memory_test.go` (and all other dual_driver
  files) — invoke `tools.Get(stripWirePrefix(name))` and call
  `Handler(ctx, raw)` directly.
- `internal/server/middleware_test.go` — tests `buildGateInput` /
  `PreCheck` paths with stubbed frames; never calls real tool handlers.

The gate's `HasGrant` check has been broken in production **forever**
since the `dark_memory_` wire prefix convention was introduced. It just
wasn't exercised by any test that landed in CI.

## The fix

1. Strip `dark_memory_` from every entry in `DefaultToolGrants` so the
   bare tool names match what `HasGrant` expects.
2. Add the 5 new agent_memory tools (`agent_memory_save`,
   `agent_memory_list`, `agent_memory_get`, `agent_memory_update`,
   `agent_memory_archive`) — they were missing from the v2.1.0 list.
3. Update `internal/atomic/capabilities_frame_test.go` test that
   uses prefixed names (`dark_memory_recall`, `dark_memory_session_close`)
   to use bare names (`recall`, `session_close`) — matching the new
   convention.
4. Add an end-to-end regression test in `tests/e2e/server_test.go` that
   exercises the gate (calls `Server.Wrap` or invokes through the
   transport, not just `t.Handler`). This test would have caught both
   the v2.1.1 regression AND this v2.1.2 one.

## Acceptance criteria

1. `agent_memory_save` succeeds end-to-end via the live MCP after
   `session_start` is called (no `ErrCapabilityNotGranted`).
2. `agent_memory_list(scope=current)` returns the saved row.
3. `session_status()` succeeds end-to-end.
4. All existing tests still pass — `go test ./internal/... ./tests/...`.
5. New `TestE2E_Gate_BareNameGrant` regression test: starting from a
   fresh session, calls a session-required tool through the gate and
   asserts the call reaches the handler (no gate refusal).

## Files touched

- `internal/recall/assemble.go` — `DefaultToolGrants` constant (strip
  prefix + add 5 agent_memory_*).
- `internal/atomic/capabilities_frame_test.go` — update prefixed names
  to bare.
- `tests/e2e/server_test.go` — add regression test that exercises the
  gate.
- `CHANGELOG.md` — `[2.1.2]` entry.

## Out of scope (deferred)

- Update `opencode.jsonc` to drop `DARK_SCRAPPER_URL` (deprecated
  v2.0.0 rename to `DARK_DRIFT_JUDGE_DAEMON_URL`). Follow-up.
- Make `ActiveConstitution()` fallback to the session's constitution
  when env is unset (UX improvement). Follow-up.

## Why v2.1.2 instead of amending v2.1.1

v2.1.1 was a hotfix for the GateMiddleware empty project_id regression.
v2.1.2 is a separate, pre-existing bug. Bumping the patch version
preserves the audit trail (one regression = one release) and lets
operators who already applied v2.1.1 know there's a second patch
needed. The fixes don't conflict — v2.1.2 stacks cleanly on v2.1.1.
