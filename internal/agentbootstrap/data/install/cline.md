# Install on Cline

> Bootstrap version 1. For dark-memory-mcp v2.6.0+.

## 1. Locate your config

Cline reads MCP config from VS Code settings (`settings.json`) under `cline.mcpServers` or `mcp.servers`.

## 2. Install dark-memory-mcp

The fastest way is via the Cline UI:
1. Open Cline panel
2. Click "MCP Servers" icon
3. Click "Installed" → "Configure MCP Servers"
4. Add:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"],
      "disabled": false
    }
  }
}
```

Or install via npm first:

```bash
npm install -g @opitacode/dark-memory-mcp
```

## 3. (Optional) Install companion MCPs

Same pattern:

```json
{
  "mcpServers": {
    "dark-memory": {"command": "npx", "args": ["-y", "@opitacode/dark-memory-mcp"]},
    "dark-research": {"command": "npx", "args": ["-y", "@opitacode/dark-research-mcp"]},
    "[FUTURE-MCP-N]": {"command": "npx", "args": ["-y", "@opitacode/[FUTURE-MCP-N]-mcp"]}
  }
}
```

## 4. Bootstrap the operating manual

Cline reads `instructions` automatically. To explicitly fetch the SYSTEM_PROMPT:

> "Use dark_memory_agent_bootstrap to read the system prompt and the install guide for Cline."

## 5. Verify

Cline shows MCP server status in the panel. `dark-memory` should appear with a green dot.
