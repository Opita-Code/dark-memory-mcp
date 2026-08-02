# Install on Claude Desktop

> Bootstrap version 1. For dark-memory-mcp v2.6.0+.

## 1. Locate your config

Claude Desktop reads MCP config from:
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

## 2. Install dark-memory-mcp

The fastest way is via the npm wrapper:

```bash
npm install -g @opitacode/dark-memory-mcp
```

Then add to `claude_desktop_config.json`:

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

Claude Desktop has no terminal. To do OSINT or browser-based research, install the companion MCPs the same way:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"]
    },
    "dark-research": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-research-mcp"]
    },
    "[FUTURE-MCP-N]": {
      "command": "npx",
      "args": ["-y", "@opitacode/[FUTURE-MCP-N]-mcp"]
    }
  }
}
```

Restart Claude Desktop after editing the config.

## 4. Bootstrap the operating manual

Claude Desktop reads `instructions` automatically. To explicitly fetch the SYSTEM_PROMPT:

> "Use dark_memory_agent_bootstrap to read the system prompt and the install guide for Claude Desktop."

The MCP will return this document as markdown.

## 5. Verify

In any chat, ask:

> "Use dark_memory_health_ping to check the MCP is healthy."

You should see `{ok: true, server_version: "2.6.0", schema_version: 20}`.
