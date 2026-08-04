# dark-memory-mcp — Harness Compatibility Matrix

> Bootstrap version {{.BootstrapVersion}}. Tied to MCP schema v{{.SchemaVersion}}, dark-memory-mcp v{{.Version}}+.
>
> **What this is:** per-harness capability matrix. Which harnesses read `instructions`? Which expose `webfetch`? Which have a real terminal? Use this to know what you can rely on when running dark-memory-mcp.

---

## Compatibility matrix (canonical harnesses)

| Harness | Reads `instructions` | Resources | Tools | Web fetch | Real terminal | Read SYSTEM_PROMPT.md directly |
|---|---|---|---|---|---|---|
| **Claude Desktop** | ✅ Yes | ✅ Yes | ✅ Yes | ❌ No | ❌ No | ❌ No — use `dark_memory_agent_bootstrap` |
| **Claude Code** | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes (read-only) | ✅ Yes (Bash) | ✅ Via `Read` tool |
| **opencode** | ❌ No (issue #32856) | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes (Bash) | ✅ Via `read` tool |
| **Cline** | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes (terminal) | ✅ Via `read_file` |
| **Cursor** | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes (terminal) | ✅ Via `Read` |
| **Continue** | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes (terminal) | ✅ Via `read` |

**Legend:**
- ✅ Supported / works out of the box
- ❌ Not supported / does not work
- "Real terminal" = can execute arbitrary shell commands, run `git`, `gh`, `brew`, etc.

---

## What this means for your workflow

### If your harness reads `instructions` (most do except opencode)

The MCP server returns a brief `instructions` field in the initialize response. Harnesses that read this field auto-inject it into the LLM's context. This is the **fallback bootstrap path** — works without any action on your part.

### If your harness does NOT read `instructions` (opencode)

You MUST call `dark_memory_agent_bootstrap(surface="system_prompt")` explicitly and ingest the result into your context. Without this, you are operating the MCP without an operating manual — drift is likely.

### If your harness does not have web fetch

You are limited to:
- Whatever is already in dark-memory (recall + agent_memory)
- Whatever the MCP can do offline (drift detection, governance, audits)

You cannot do OSINT. The `dark_memory_agent_recommend_companions()` tool will recommend `dark-research-mcp` (which provides web fetch via its own MCP server) and/or `[FUTURE-MCP-N]-mcp` (real browser for JS-gated pages).

### If your harness has a real terminal

You can install companion MCPs yourself:
```bash
npm install -g @opitacode/dark-research-mcp
npm install -g @opitacode/[FUTURE-MCP-N]-mcp
```

Then add them to your MCP config (see your harness's install guide).

---

## Harness-specific notes

### Claude Desktop
- Consumer-grade; no terminal
- Reads `instructions` reliably
- For OSINT, install `dark-research-mcp` (no terminal needed — runs as MCP)
- For browser-based OSINT, install `[FUTURE-MCP-N]-mcp` (no terminal needed)

### Claude Code
- Developer-grade; full Bash
- Reads `instructions` reliably
- Has a read-only `WebFetch` (sufficient for most OSINT, not for interactive flows)

### opencode
- Developer-grade; full Bash
- Does **NOT** read `instructions` (verified by issue #32856 against MCP SDK)
- Must call `dark_memory_agent_bootstrap` explicitly to bootstrap

### Cline / Cursor / Continue
- Developer-grade; all have terminal
- All read `instructions`
- For per-harness install steps, see `install/{client}.md`

---

## Spec version detection

The MCP server supports two spec versions concurrently:

| Spec version | Released | Where `clientInfo` lives |
|---|---|---|
| `2025-06-18` | June 2025 | `initialize.params.clientInfo` (handshake) |
| `2026-07-28` | July 2026 | `_meta.clientInfo` per-request |

Call `dark_memory_agent_detect_environment()` to see which one your harness negotiated. The MCP works with both; harness detection falls back gracefully if `clientInfo` is missing.
