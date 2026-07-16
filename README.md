┌──────────────────────────────────────────────────────────────────────────────┐
│                                                                              │
│  ██████╗  ██████╗██████╗ ███╗   ███╗    ███╗   ███╗ ██████╗██████╗           │
│ ██╔═══██╗██╔════╝██╔══██╗████╗ ████║    ████╗ ████║██╔════╝██╔══██╗          │
│ ██║   ██║██║     ██║  ██║██╔████╔██║    ██╔████╔██║██║     ██████╔╝          │
│ ██║   ██║██║     ██║  ██║██║╚██╔╝██║    ██║╚██╔╝██║██║     ██╔═══╝           │
│ ╚██████╔╝╚██████╗██████╔╝██║ ╚═╝ ██║    ██║ ╚═╝ ██║╚██████╗██║               │
│  ╚═════╝  ╚═════╝╚═════╝ ╚═╝     ╚═╝    ╚═╝     ╚═╝ ╚═════╝╚═╝               │
│                                                                              │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│                       Opita Code Dark Memory MCP                             │
│                                                                              │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│          Research • Threat Intelligence • Automation • MCP Server            │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘

**Persistent memory + workflow orchestration for [dark-agents-v2](https://github.com/dark-agents).**

25 `dark_memory_*` MCP tools backed by a dual-driver store (SQLite for dev, Postgres for prod). Standalone Go module — installs alongside `dark-research-mcp` via `opencode.jsonc`. Conforms to MCP 2025-06-18 with the `dark-agents/memory` coexistence group.

---

## Quick start (30 seconds)

```bash
# 1. Build all 3 binaries
go build -o bin/dark-mem-mcp ./cmd/dark-mem-mcp
go build -o bin/dark-mem-cli ./cmd/dark-mem-cli
go build -o bin/dark-mem-inspect ./cmd/dark-mem-inspect

# 2. Tell opencode to use dark-memory-mcp as an MCP server
# (add this to ~/.config/opencode/opencode.jsonc):
{
  "mcp": {
    "dark-memory": {
      "type": "local",
      "command": ["/path/to/bin/dark-mem-mcp.exe"],
      "enabled": true
    }
  }
}

# 3. Verify it works
./bin/dark-mem-inspect --json
# → { "driver": "sqlite", "schema_version": 7, "canary_present": false, ... }

# 4. Apply migrations (only needed first time)
./bin/dark-mem-cli migrate
```

That's it. The server auto-bootstraps on first run (migrations + watchdog + default project seed).

---

## What's in the box

| Binary | Purpose | Audience |
|---|---|---|
| `dark-mem-mcp` | MCP server. Exposes 25 tools to any MCP-native harness (opencode, Claude Desktop, Cursor, VS Code). | LLMs |
| `dark-mem-cli` | Operator-side admin: `migrate`, `vacuum`, `schema-status`, `set-driver`. | You, in a terminal |
| `dark-mem-inspect` | Read-only diagnostic. No migrations, no vacuum, no writes. Safe to run against prod anytime. | You, debugging |

---

## The 25 tools

Eight namespaces. Wire format uses the `dark_memory_` prefix.

| Namespace | Count | Tools |
|---|---|---|
| SESSION | 4 | `session_start`, `session_resume`, `session_status`, `session_close` |
| RESEARCH | 3 | `research_topic`, `research_recall`, `research_resume_thread` |
| VIBE | 4 | `vibe_publish`, `vibe_spec`, `pipeline_status`, `resolve_drift` |
| CONTEXT | 3 | `artifact_context`, `spec_context`, `session_context` |
| JUDGE | 3 | `judge`, `consensus`, `judgment_history` |
| POLICY | 2 | `active_policy`, `load_constitution` |
| OBSERVABILITY | 3 | `memory_state`, `writes`, `anomalies` |
| ADMIN | 3 | `admin_migrate`, `admin_schema_status`, `admin_vacuum` |

Total: **25 tools in a fixed canonical order** (RFC D-9). The order is part of the wire contract — harnesses can index by position.

---

## Storage

- **Default**: SQLite at `./dark.db` (no setup, zero config)
- **Production**: Postgres via `DARK_DB_DRIVER=postgres` + `DARK_DB=postgres://user:pass@host:5432/darkdb`
- Switch drivers live via `dark-mem-cli set-driver` (writes `$DARK_HOME/config.toml`)
- Migrations are auto-applied on open if `ConstitutionFile` is not set; with one set, the watchdog verifies the SHA256 first (INV-4)

For the migration playbook, see [`docs/MIGRATION.md`](docs/MIGRATION.md).

---

## Six invariants

Every Store operation must respect:

| # | Rule | Defended by |
|---|---|---|
| INV-1 | Every `Save*` emits a `write_audit` row atomically | `Store.RecordWrite` called inside the same transaction |
| INV-2 | `Recall()` defaults to cross-session; workflow tools always carry `session_id` | `research.RecallOptions.SessionScope` |
| INV-3 | Payloads containing the canary are rejected | `Store.canary.ValidatePayload` at the top of every `Save*` |
| INV-4 | Every write_audit row carries `constitution_id@version`; watchdog verifies file SHA | `Store.runWatchdog` on Open |
| INV-5 | Cache re-hashes stored text on every Get; mismatch = cache miss + anomaly | `internal/llm/cache.go` |
| INV-6 | Mod content runs through injection-marker regex; refused unless risk-class whitelisted | `internal/safety/safety.go` |
| INV-7 | `Store.SetActiveProject` required before any read or write | `Store.requireProject` |

Full table mapping every Store method to its invariant set: [`docs/INVARIANTS.md`](docs/INVARIANTS.md).

---

## Coexistence with dark-research-mcp

Dark Memory MCP is the **sibling** of `dark-research-mcp`. Both can install in `opencode.jsonc` simultaneously. They share `dark.db` (different tables). `dark-recall` plugin v2.3 (when installed) prefers `dark_memory_*` calls when both servers are present.

| MCP | Namespace | Count | Purpose |
|---|---|---|---|
| `dark-memory-mcp` | `dark_memory_*` | 25 | Persistent memory + workflow orchestration (this repo) |
| `dark-research-mcp` | `dark_research_*` | ~13 + multi + router | OSINT acquisition |
| (deprecated) | `dark_mem_*` | legacy | Migrating to `dark_memory_*` |

Full coexistence architecture: [`docs/COEXISTENCE.md`](docs/COEXISTENCE.md) and [`vibe-flow/main/BRIDGE_AND_COEXISTENCE.md`](vibe-flow/main/BRIDGE_AND_COEXISTENCE.md).

---

## Documentation

| Doc | Audience | Size |
|---|---|---|
| [`docs/RUNBOOK.md`](docs/RUNBOOK.md) | Operator how-to (install, driver switch, vacuum, troubleshooting, env vars) | ~5.5K |
| [`docs/INVARIANTS.md`](docs/INVARIANTS.md) | All 6 INV-* contracts + quick-reference table | ~7K |
| [`docs/CONTEXT_OBJECTS.md`](docs/CONTEXT_OBJECTS.md) | 8 context projections + design rules | ~6.5K |
| [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) | P50/P99 targets + budget breakdown + 5 anti-patterns | ~7K |
| [`docs/MIGRATION.md`](docs/MIGRATION.md) | SQLite → Postgres in 4 phases | ~10.5K |
| [`docs/COEXISTENCE.md`](docs/COEXISTENCE.md) | Architecture + migration path + schema ownership | ~5K |
| [`vibe-flow/main/DARK_MEMORY_MCP_RFC.md`](vibe-flow/main/DARK_MEMORY_MCP_RFC.md) | The RFC (source of truth for the design) | — |
| [`vibe-flow/main/BRIDGE_AND_COEXISTENCE.md`](vibe-flow/main/BRIDGE_AND_COEXISTENCE.md) | The coexistence contract (normative) | — |
| [`vibe-flow/constitution/dark-memory-mcp.constitution.toml`](vibe-flow/constitution/dark-memory-mcp.constitution.toml) | Operating ruleset (system-prompt + INV-* declarations) | — |

---

## Development

```bash
# Run the full test suite (~11 min with -count=1 cold rebuild)
go test -count=1 ./...

# Just the contract tests (sqlite-only, ~7s)
go test ./tests/dual_driver/...

# Just the conformance test (MCP Inspector simulation, ~14s)
go test ./tests/conformance/...

# Just the invariants (~6s)
go test ./tests/invariants/...
```

**Backlog** (not in v1.0.0):
- `go test -race` needs TDM-GCC on Windows (Linux CI covers it)
- Migration to `modelcontextprotocol/go-sdk` v1.6.1 official SDK — architectural, new spec
- `dark-recall` v2.3 plugin (opencode-side glue) — sibling repo
- `dark-research-mcp` deprecation shim — sibling repo

---

## License

MIT. See [`LICENSE`](LICENSE).

---

*Dark Memory MCP is a unit, not a tool. It is invoked by name, by session, and by intent.*
