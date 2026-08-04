# Install on Continue

> Bootstrap version 3. For dark-memory-mcp v2.11.0+.

## 1. Locate your config

Continue reads MCP config from `~/.continue/config.json` under `mcpServers` (or per-workspace config).

## 2. Install dark-memory-mcp

```bash
npm install -g @opitacode/dark-memory-mcp
```

Add to `~/.continue/config.json`:

```json
{
  "mcpServers": [
    {
      "name": "dark-memory",
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"]
    }
  ]
}
```

(Note: Continue uses an array, not a map.)

## 3. (Optional) Install companion MCPs

```json
{
  "mcpServers": [
    {"name": "dark-memory", "command": "npx", "args": ["-y", "@opitacode/dark-memory-mcp"]},
    {"name": "dark-research", "command": "npx", "args": ["-y", "@opitacode/dark-research-mcp"]},
    {"name": "[FUTURE-MCP-N]", "command": "npx", "args": ["-y", "@opitacode/[FUTURE-MCP-N]-mcp"]}
  ]
}
```

## 4. Bootstrap the operating manual

Continue reads `instructions` automatically. To explicitly fetch the SYSTEM_PROMPT:

> "Use dark_memory_agent_bootstrap to read the system prompt and the install guide for Continue."

## 5. Verify

Restart Continue after editing config. The MCP panel should show `dark-memory` as connected.
