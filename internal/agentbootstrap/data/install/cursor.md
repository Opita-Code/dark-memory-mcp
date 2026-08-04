# Install on Cursor

> Bootstrap version {{.BootstrapVersion}}. For dark-memory-mcp v{{.Version}}+.

## 1. Locate your config

Cursor reads MCP config from `~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (project).

## 2. Install dark-memory-mcp

```bash
npm install -g @opitacode/dark-memory-mcp
```

Add to `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"]
    }
  }
}
```

## 3. (Optional) Install companion MCPs

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

Cursor reads `instructions` automatically. To explicitly fetch the SYSTEM_PROMPT:

> "Use dark_memory_agent_bootstrap to read the system prompt and the install guide for Cursor."

## 5. Verify

In Cursor's MCP panel (Settings → MCP), `dark-memory` should appear as connected.
