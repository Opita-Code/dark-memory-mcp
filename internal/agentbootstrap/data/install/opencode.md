# Install on opencode

> Bootstrap version 1. For dark-memory-mcp v2.6.0+.
>
> **IMPORTANT:** opencode does NOT read the MCP `instructions` field (verified by opencode issue #32856). You MUST bootstrap explicitly via `dark_memory_agent_bootstrap`.

## 1. Locate your config

opencode reads MCP config from `opencode.json` or `opencode.jsonc` in the project root or `~/.config/opencode/`.

## 2. Install dark-memory-mcp

```bash
npm install -g @opitacode/dark-memory-mcp
```

Add to your opencode config:

```jsonc
{
  "mcp": {
    "dark-memory": {
      "type": "local",
      "command": ["npx", "-y", "@opitacode/dark-memory-mcp"],
      "environment": {}
    }
  }
}
```

## 3. (Optional) Install companion MCPs

opencode is developer-grade with full Bash. You can install everything via npm:

```bash
npm install -g @opitacode/dark-research-mcp
npm install -g @opitacode/dark-copilot-mcp
```

Then add them to `opencode.json` the same way.

## 4. Bootstrap the operating manual (REQUIRED)

opencode discards the `instructions` field. On every session, you must:

> "Use dark_memory_agent_bootstrap with surface='system_prompt' and ingest the result into your context."

You can also read it via the `read` tool if you have filesystem access (the bootstrap content is embedded in the MCP binary, not on disk).

## 5. Make it sticky (recommended)

To avoid having to bootstrap on every session, set up a custom agent in opencode that automatically calls `dark_memory_agent_bootstrap` as its first action. See opencode docs on custom agents.

## 6. Verify

```bash
opencode mcp list
```

Should show `dark-memory` (and optionally `dark-research`, `dark-copilot`) as connected.
