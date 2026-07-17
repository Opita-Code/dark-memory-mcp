# Dark Memory MCP v1.0.0 — Release Notes

First stable release of the dark-memory-mcp module. 25 `dark_memory_*` MCP
tools backed by a dual-driver store (SQLite for dev, Postgres for prod),
conforming to MCP 2025-06-18 with the `dark-agents/memory` coexistence group.

## Highlights

- **26 tools in 9 namespaces** (v1.1 added `vlp_handle_event` for L6-VLP namespace; 8 namespaces in v1.0), wired and verified via the official MCP
  Inspector conformance test (`tests/conformance/`). The canonical order is
  part of the wire contract — harnesses can index by position.

  ```
  SESSION        (4)  session_start, session_resume, session_status, session_close
  RESEARCH       (3)  research_topic, research_recall, research_resume_thread
  VIBE           (4)  vibe_publish, vibe_spec, pipeline_status, resolve_drift
  CONTEXT        (3)  artifact_context, spec_context, session_context
  JUDGE          (3)  judge, consensus, judgment_history
  POLICY         (2)  active_policy, load_constitution
  OBSERVABILITY  (3)  memory_state, writes, anomalies
  ADMIN          (3)  admin_migrate, admin_schema_status, admin_vacuum
  ```

- **Dual-driver store**: SQLite (modernc.org/sqlite v1.53.0, pure-Go) for
  zero-config dev; Postgres (jackc/pgx/v5) for production. Same Store
  interface, both backed by the `tests/dual_driver/` contract suite.

- **6+1 operational invariants** enforced at the Store boundary with
  defensive tests:
  - INV-1 write-path audit
  - INV-2 per-session scoping
  - INV-3 canary in writes (payloads containing the active canary token are rejected)
  - INV-4 constitution audit + SHA256 watchdog on every Open
  - INV-5 cache re-hash on every Get
  - INV-6 mod content sanitization (injection-marker regex)
  - INV-7 multi-tenancy (SetActiveProject required for any read/write)

- **Bridge conformance**: coexistence_group declared in the `initialize`
  response, canonical tool order enforced, panic recovery middleware on
  the MCP server, MCP Inspector conformance tests.

- **3 binaries**:
  - `dark-mem-mcp` — MCP server (stdio transport)
  - `dark-mem-cli` — operator-side admin: `migrate`, `vacuum`,
    `schema-status`, `set-driver`
  - `dark-mem-inspect` — read-only diagnostic (safe to run against prod)

- **6 runbooks** in `docs/`: RUNBOOK, INVARIANTS, CONTEXT_OBJECTS,
  PERFORMANCE, MIGRATION, COEXISTENCE.

## What's not in v1.0

Deferred per the master plan (`vibe-flow/PLAN.md`):

- **Real `dark_ssd_drift_judge` verdicts** — currently uses R9 self-judge
  because the [drift-judge-daemon] daemon (sub-spec 180) is not running on this
  host. The integration is wired; starting the daemon in a future session
  unlocks real LLM-as-judge verdicts.
- **`dark-recall` v2.3 plugin** — sibling opencode plugin; lives outside
  this repo. Detects dark-memory-mcp presence and prefers `dark_memory_*`
  when both servers are present.
- **`dark-research-mcp` deprecation shim** — sibling OSINT router; emits
  `{deprecated: true, successor: 'dark-memory-mcp'}` from each `dark_mem_*`
  handler.
- **Migration to `modelcontextprotocol/go-sdk` v1.6.1** — the official MCP
  Go SDK exists (maintained with Google) as an alternative to mark3labs/mcp-go;
  migration is an architectural decision for a future RFC.

## Installation

```bash
# Clone
git clone https://github.com/Opita-Code/dark-memory-mcp
cd dark-memory-mcp

# Build the 3 binaries
go build -o bin/dark-mem-mcp ./cmd/dark-mem-mcp
go build -o bin/dark-mem-cli ./cmd/dark-mem-cli
go build -o bin/dark-mem-inspect ./cmd/dark-mem-inspect

# Wire into opencode.jsonc
{
  "mcp": {
    "dark-memory": {
      "type": "local",
      "command": ["/path/to/bin/dark-mem-mcp.exe"],
      "enabled": true
    }
  }
}

# Apply migrations (first time)
./bin/dark-mem-cli migrate

# Verify
./bin/dark-mem-inspect --json
```

## Upgrade guide

This is v1.0 — no upgrade path from earlier versions.

## Verification

- 9/9 test suites green with `-count=1` cold rebuild (~11 min total)
- 73+ orchestrator tests
- 6 e2e tests including `TestE2E_1000MixedCallsNoDeadlock` (RFC §12 #4)
- 4 bridge.7 conformance tests via real mcp-go client
- 11 CLI tests + 2 new canary_present regression tests

## Contributors

Built by the dark-agents-v2 team. See
[`CONTRIBUTING.md`](https://github.com/Opita-Code/dark-memory-mcp/blob/main/CONTRIBUTING.md)
to get involved.

## License

MIT. See [`LICENSE`](https://github.com/Opita-Code/dark-memory-mcp/blob/main/LICENSE).