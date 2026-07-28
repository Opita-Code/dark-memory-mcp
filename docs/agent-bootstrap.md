# dark-memory-mcp — Self-Bootstrapping

> **v2.6.0+.** The server teaches any MCP harness how to operate it via
> embedded resources + 3 self-bootstrap tools. No external docs required.

## What is self-bootstrapping?

"Bootstrapping" in MCP terminology is the process by which an MCP
client (the "harness") learns what tools the server exposes, what they
do, and how to use them well — *without* the user having to point the
LLM at external documentation.

Most MCP servers leave bootstrapping to the harness:
- Read the tool list and descriptions from `tools/list`.
- Hope the harness reads the optional `instructions` field.
- Pray that any "system prompt" extension is honored.

For dark-memory-mcp, three things broke that assumption:

1. **opencode** (issue [#32856](https://github.com/opencode-ai/opencode/issues/32856))
   discards the `instructions` field. Period.
2. **Claude Desktop / Claude Code** honor `instructions` for the
   first session, but subsequent sessions may not re-read it.
3. **Cursor, Cline, Continue** have varying levels of `instructions`
   support and inconsistent behavior across versions.

**Self-bootstrapping** is dark-memory-mcp's answer: instead of relying
on the harness to remember the manual, the server publishes the
manual as **MCP resources** (which every spec-compliant harness MUST
support) and exposes **3 discovery tools** that the LLM can call on
demand to learn how to operate the server.

## The 5 layers (all non-invasive)

```
┌─────────────────────────────────────────────────────────────────────┐
│ Harness discovers the dark-agent system prompt + compatibility      │
│ matrix + install guides via MCP resources (every harness supports)   │
└─────────────────────────────────────────────────────────────────────┘
                                  │
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
   L1  dark-memory://docs/   L3  dark-memory://docs/   L5  dark-memory://docs/
       system-prompt.md          install/{client}.md     companions/{name}.md
       (8.9 KB canonical         (per-harness install     (dark-research,
        operating manual)         steps, x6 harnesses)     dark-copilot docs)

   L2  dark-memory://docs/compatibility-matrix.md (per-harness feature matrix)

   L4  `instructions` field appended to the initialize response with
       cross-feature hints (best-effort; opencode discards it; others honor)
```

| # | Layer | URI | Audience | Why |
|---|---|---|---|---|
| L1 | System-prompt resource | `dark-memory://docs/system-prompt.md` | `assistant` | **Canonical path.** Every spec-compliant harness MUST support resources. The 8.9 KB manual gives the LLM enough context to operate the server without external docs. |
| L2 | Compatibility matrix | `dark-memory://docs/compatibility-matrix.md` | `assistant` | Per-harness feature matrix (instructions support, resources support, web fetch, terminal, etc.). Tells the LLM "if you're opencode, don't try the tools/curl path". |
| L3 | Install URI templates | `dark-memory://docs/install/{client}.md` | `assistant` | One URI template per harness (claude-desktop, claude-code, opencode, cline, cursor, continue). LLM calls `agent_bootstrap` with `target=opencode` to get the install guide for that harness. |
| L4 | `instructions` field | n/a (initialize response) | `assistant` | Best-effort. Appended to the existing coexistence_group string with cross-feature hints. Honored by Claude Desktop/Code (first session); discarded by opencode. |
| L5 | Companion URI templates | `dark-memory://docs/companions/{name}.md` | `assistant` | One URI template per companion MCP (dark-research, dark-copilot). LLM calls `agent_bootstrap` with `target=dark-research` to learn when to install it. |

Every resource is annotated with `audience: [assistant]` and
`priority: 0.9` so harnesses can demote them under tight context
budgets without dropping them entirely.

## The 3 self-bootstrap tools

All in the **`AGENT_BOOTSTRAP`** namespace (added in v2.6.0).
Canonical tool count: 35 → 38.

### `dark_memory_agent_bootstrap`

Read any resource by surface/target. Surfaces:

| Surface | Target | Returns |
|---|---|---|
| `system_prompt` | (ignored) | The canonical operating manual |
| `compatibility_matrix` | (ignored) | Per-harness feature matrix |
| `install_guide` | one of: `claude-desktop`, `claude-code`, `opencode`, `cline`, `cursor`, `continue` | Step-by-step install for that harness |
| `companion` | one of: `dark-research`, `dark-copilot` | Why + how to install this companion MCP |
| `all` | (ignored) | Every surface as a map |

Optional `force_embedded: true` bypasses the `DARK_AGENT_BOOTSTRAP_DIR`
override for this single call (useful for diagnostics).

### `dark_memory_agent_recommend_companions`

Harness-aware companion advice. Reads the global clientInfo store
(captured during `initialize` for legacy 2025-06-18 spec, or via
`_meta.clientInfo` per-request for the new 2026-07-28 spec) and
returns:

- Harness info (name, version, normalized short name, source spec).
- Which companion MCPs are missing (always both: dark-research and
  dark-copilot, since we cannot enumerate peer MCPs in the spec).
- Rationale + install snippet + docs URI for each missing companion.
- Limitations: we cannot detect whether dark-research or dark-copilot
  is *already* installed; we always recommend them.

### `dark_memory_agent_detect_environment`

Harness runtime introspection. Returns:

- `SpecVersionDetected`: "2025-06-18", "2026-07-28", or "unknown".
- `ClientInfoSource`: which path captured the harness identity
  ("initialize", "_meta", or "none").
- `Client`: harness name, version, normalized canonical name.
- `NegotiatedCapabilities`: server's view of harness capabilities
  (resources/tools always true; prompts/logging/sampling/roots false
  until we can introspect them properly).
- `Transport`: always "stdio" (SDK limitation; future enhancement when
  the 2026-07-28 spec lands).
- `Server`: this MCP server's identity (name, version, schema
  version, tools total, resources total).

## Decision tree: which path for which harness

```
LLM sees dark-memory-mcp in its tools list
                  │
                  ▼
        Is `dark_memory_agent_bootstrap` in the tool list?
        ┌─────────┴─────────┐
        YES                NO (very old harness pre-v2.6.0)
        │                  │
        ▼                  ▼
  Call it with         Rely on the `instructions` field
  surface="system_prompt"  baked into the initialize response.
  to load the canonical     (Works for Claude Desktop/Code first
  operating manual.         session; fails for opencode.)
        │
        ▼
  Continue with surface="compatibility_matrix" to learn
  what THIS harness supports (resources? web fetch? terminal?).
        │
        ▼
  When you need to know about companions (dark-research,
  dark-copilot), call `dark_memory_agent_recommend_companions`.
  When you need to know what the harness actually told the
  MCP at connect time, call `dark_memory_agent_detect_environment`.
```

In short: **resources are the canonical cross-harness path**. The
`instructions` field is a best-effort optimization for harnesses that
honor it. The 3 self-bootstrap tools are the programmatic entry point
for LLMs that want to re-read on demand.

## Operator override: `DARK_AGENT_BOOTSTRAP_DIR`

By default, the 10 bootstrap content files (SYSTEM_PROMPT.md,
COMPATIBILITY_MATRIX.md, 6 install guides, 2 companion docs) are
embedded in the Go binary via `//go:embed` and shipped as part of
every install.

Operators who want to customize the content (e.g., add proprietary
harness support, fix a typo before the next release, add a custom
companion doc) can set:

```bash
DARK_AGENT_BOOTSTRAP_DIR=/path/to/custom/dark-agent-bootstrap
```

The directory must contain the 10 expected files at the expected
paths (relative to the dir):

```
SYSTEM_PROMPT.md
COMPATIBILITY_MATRIX.md
install/claude-desktop.md
install/claude-code.md
install/opencode.md
install/cline.md
install/cursor.md
install/continue.md
companions/dark-research.md
companions/dark-copilot.md
```

If any file is missing or the directory is invalid, the server falls
back to the embedded content and emits a one-time warning to stderr.

This is a runtime override — no rebuild required. The harness sees
the operator's content via `resources/read` on the same URIs.

## Adding a new harness (operator-side)

To add an install guide for a new MCP harness without waiting for a
release:

1. Create `install/<client>.md` in your custom dir.
2. Append the client to `InstallClients` in
   `internal/agentbootstrap/resources.go` (this part requires a
   rebuild — operator cannot patch without source).
3. Rebuild + restart the server.

For step 2 to be operator-only (no rebuild), we'd need to move
`InstallClients` to the override dir via a manifest file. That's
deferred to v2.6.2+ unless operator demand is high.

## Why embed.FS and not separate npm-side files?

This question came up during v2.6.1 review. The short answer: **the
binary already contains everything the harness needs**.

- `npx -y @opitacode/dark-memory-mcp@X.Y.Z` downloads the npm wrapper,
  which spawns the Go binary.
- The Go binary has `//go:embed all:data` which compiles all 10 .md
  files into the binary's read-only filesystem at build time.
- The binary exposes those files via MCP resources at runtime.
- **No npm-side file distribution needed.** The wrapper spawns the
  binary, the binary IS the resource server.

Same story for MCPB bundles: `manifest.json` references the binary,
the binary exposes resources via MCP. No additional bundle files
required.

## Spec compatibility

The dual-spec clientInfo capture (legacy `initialize.clientInfo` for
the 2025-06-18 spec + per-request `_meta.clientInfo` for the new
2026-07-28 spec) means self-bootstrapping works for harnesses on
either spec version. Both paths converge into a single
`ClientInfoRecord` store; the bootstrap tools read from there.

Future-proofing: when the 2026-07-28 spec lands widely and the old
handshake is removed, the dual-spec code path can be simplified to
just the new path. The `OnAfterInitialize` hook and `WithMetaPropagator`
both have stable SDK contracts (mark3labs/mcp-go v0.56.0).

## See also

- `README.md` — install + quickstart
- `docs/mcpb-install.md` — one-click Claude Desktop install
- `docs/npm-install.md` — npm wrapper install for opencode / Cursor / Cline
- `internal/agentbootstrap/data/SYSTEM_PROMPT.md` — the canonical manual
  the server publishes
- `internal/agentbootstrap/data/COMPATIBILITY_MATRIX.md` — per-harness
  feature matrix
