# v1.2.0 — Schema correctness (F33) + structured error reporting (F35) + project_create tool (F33)

> **Summary.** This release closes the bootstrap loop for INV-7 multi-tenancy (operators can now create projects from inside the MCP surface) and fixes the JSON Schema ↔ Go struct drift that made `vibe_publish` and `vibe_spec` fail at unmarshal time for every harness call. Tool errors now surface the offending field path instead of a free-form message.

## What's in this release

### 1. `dark_memory_project_create` — closes the INV-7 bootstrap loop (F33 / Bug C)

Before v1.2.0, provisioning a non-`default` project required inserting into the `projects` table out of band (`psql`/`sqlite3` CLI). That forced the operator out of the MCP surface to do tenant setup, and forced `dark_memory_session_start` to fail with `ErrSessionRequired` whenever a caller passed a non-existent `project_id`.

After v1.2.0, the loop closes:

```jsonc
// 1. Create the tenant
{"project_id": "acme-2026", "display_name": "ACME 2026",
 "description": "ACME tenant for FY2026 OSINT research."}
  → {"project_id": "acme-2026", "display_name": "ACME 2026",
     "created_at": "2026-07-16T07:42:18Z",
     "idempotent_replay": false}

// 2. Bind a session to it
{"operator": "nico", "project_id": "acme-2026"}
  → {"session_id": "sess-...", "project_id": "acme-2026", ...}

// 3. Use the surface as usual
{"vibe_case": "C2", "tasks": [{"id": "T1", "description": "..."}]}
```

Properties:
- Idempotent on `project_id` — re-creating returns the existing row with `idempotent_replay: true` and the original `created_at`.
- Kebab-case enforced (`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`).
- Placed at canonical index 0 — operators that iterate `tools/list` discover it before `session_start`, matching the natural bootstrap flow.

### 2. Schema correctness for `vibe_publish` and `vibe_spec` (F33 / Bug A + Bug B)

The JSON Schema declared fields as flat top-level strings, but the Go orchestrator structs nest them under `Spec` and `Artifact` objects. Result: **every** harness call failed with:

```
cannot unmarshal string into Go struct field PublishVibeInput.spec
of type orchestration.PublishSpecInput
```

This is fixed by declaring the nesting in the schema so the harness serializes the payload with the shape the Go server expects. Before/after payload diff:

```diff
 # vibe_publish (BEFORE — flat shape, schema bug, never worked)
-{"vibe_case": "C2",
- "constitution": "{...}",
- "spec": "{...}",
- "tasks": "[{...}]",
- "artifact_url": "file:///path/to/artifact",
- "artifact_type": "text",
- "text": "..."}

 # vibe_publish (AFTER — nested shape, schema fixed in v1.2.0)
+{"spec": {
+  "vibe_case": "C2",
+  "constitution": "{...}",
+  "spec": "{...}",
+  "tasks": "[{...}]"
+ },
+ "artifact": {
+  "artifact_url": "file:///path/to/artifact",
+  "artifact_type": "text",
+  "text": "..."
+ }}
```

Bonus: `vibe_spec` task items now use a strict schema (`additionalProperties: false` + explicit property list for `id`, `description`, `depends_on`, `owner`). Stops the silent-drop / type-coerce behavior that produced confusing `cannot unmarshal string into depends_on of type []string` errors when callers passed `title`/`status`/`priority`.

### 3. Structured error reporting (F35)

`ToolError` extended with `Field`, `ExpectedType`, `ActualType`, and `SchemaHintURL`. `BindOrchestrator` promotes `*json.UnmarshalTypeError` paths into discrete fields instead of hiding them in `Message`. Before/after:

```jsonc
// BEFORE (F35 / Bug E)
{"code": "ErrInvalidArgument",
 "message": "input JSON does not match expected schema for vibe_spec:
             json: cannot unmarshal string into Go struct field
             VibeSpecTask.tasks.depends_on of type []string",
 "hint": "Inspect the tool's input schema and ensure the payload
          matches the declared fields."}

// AFTER
{"code": "ErrInvalidArgument",
 "message": "input JSON does not match expected schema for vibe_spec:
             json: cannot unmarshal string into Go struct field
             VibeSpecTask.tasks.depends_on of type []string",
 "field": "tasks[2].depends_on",
 "expected_type": "[]string",
 "actual_type": "string",
 "hint": "Field \"tasks[2].depends_on\" must be of type []string,
          got string. Update the input payload to match the tool's
          input schema."}
```

All new fields are `omitempty`, so existing JSON consumers that ignore unknown fields keep working unchanged.

## Tool surface diff

| | v1.1.0 | v1.2.0 |
|---|---|---|
| Canonical count | 26 | **27** |
| Namespaces | 9 | **10** |
| New namespace | — | `PROJECT (1) → project_create` |
| Position of `project_create` | — | index 0 (before `session_start`) |
| `vibe_publish` schema | flat strings (broken) | **nested objects** |
| `vibe_spec` task items | loose | **strict** (`additionalProperties: false`) |
| `ToolError` fields | 3 | **7** (+`Field`, `ExpectedType`, `ActualType`, `SchemaHintURL`) |

## Migration notes

- **No DB migration.** `project_create` writes to the existing `projects` table (migrations/v7) — no schema change.
- **Breaking for `vibe_publish` callers** that built payloads against the old broken flat shape. Those payloads were never valid against the Go struct and would have failed unmarshal at runtime; the new payloads use the nested shape. The before/after diff above is the only change.
- **Backwards compatible for `ToolError` consumers.** New fields are `omitempty`.

## Test coverage

- 7 new sub-tests in `tests/tools/project_tool_test.go`:
  - `TestProjectCreate_Success` — happy path with description round-trip
  - `TestProjectCreate_IdempotentReplay` — re-create returns existing row + original `created_at`
  - `TestProjectCreate_RejectsUppercaseProjectID` — schema `pattern` enforced
  - `TestProjectCreate_RejectsEmptyDisplayName` — `minLength` enforced
  - `TestProjectCreate_MissingFields` — `required` enforced at envelope
  - `TestProjectCreate_RejectsUnknownField` — `additionalProperties: false` enforced
  - `TestBindOrchestrator_TypeMismatchSurfacesFieldPath` — F35 envelope shape
- All existing v1.1.0 tests pass against the updated 27-tool surface.

## Files changed

| File | Change |
|---|---|
| `internal/tools/vibe.go` | Fix `vibe_publish` schema (nested `spec`/`artifact`); extract `vibeSpecTaskSchema` (strict items); `additionalProperties: false` everywhere |
| `internal/tools/wiring.go` | Add `typeMismatchToolError` helper; route JSON unmarshal failures through it |
| `internal/tools/types.go` | Extend `ToolError` with `Field`/`ExpectedType`/`ActualType`/`SchemaHintURL` (omitempty) |
| `internal/tools/project.go` | **NEW** — RegisterProject + ProjectCreateInput/Result + validation |
| `internal/tools/registry.go` | Add `project_create` to canonical order at index 0; bump count 26 → 27 |
| `internal/tools/register.go` | Call `RegisterProject`; bump boot sanity check 26 → 27 |
| `tests/tools/project_tool_test.go` | **NEW** — 7 sub-tests for the new tool + F35 envelope |
| `CHANGELOG.md` | v1.2.0 entry |

## Operator checklist (post-merge)

- [ ] `go build ./...` (no new warnings)
- [ ] `go test ./tests/...` (all pass; 7 new sub-tests)
- [ ] Restart `dark-mem-mcp.exe` to pick up the new schema
- [ ] Verify `dark_memory_project_create` appears at index 0 in `tools/list`
- [ ] Provision a test project and bind a session to it as a smoke test
- [ ] Confirm a pre-existing harness that ignored unknown `ToolError` fields keeps working

## Out of scope (deferred to v1.2.x)

- F35 follow-up: route `json.SyntaxError` and `json.UnmarshalFieldError` through the same structured envelope (right now they fall back to the generic shape — still strictly better than v1.1.0 but not as informative as the type-mismatch path).
- DB schema-level enforcement of the kebab-case `project_id` pattern (currently enforced at the tool layer only; a CHECK constraint would be belt-and-suspenders).
- Per-tool `SchemaHintURL` population — placeholder field is in place but no doc URLs are wired yet.

---

🤖 Generated with assistance from an agentic session running on the `dark-research`/`dark-memory` MCP coexistence harness. All code paths are exercised by the existing + new test suite.