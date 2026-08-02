# Install on Claude Code

> Bootstrap version 1. For dark-memory-mcp v2.6.0+.

## 1. Locate your config

Claude Code reads MCP config from `~/.claude.json` (or `.mcp.json` in project root).

## 2. Install dark-memory-mcp

```bash
claude mcp add dark-memory -- npx -y @opitacode/dark-memory-mcp
```

## 3. (Optional) Install companion MCPs

```bash
claude mcp add dark-research -- npx -y @opitacode/dark-research-mcp
claude mcp add [FUTURE-MCP-N] -- npx -y @opitacode/[FUTURE-MCP-N]-mcp
```

Claude Code has a Bash tool, so you can also install directly:

```bash
npm install -g @opitacode/dark-research-mcp
npm install -g @opitacode/[FUTURE-MCP-N]-mcp
```

## 4. Bootstrap the operating manual

Claude Code reads `instructions` automatically. To explicitly fetch the SYSTEM_PROMPT, ask:

> "Use dark_memory_agent_bootstrap to read the system prompt and the install guide for Claude Code."

You can also use the `Read` tool to fetch the resource directly:

```
Read dark-memory://docs/system-prompt.md
```

(Note: `Read` may not handle MCP URIs directly; use the tool if `Read` fails.)

## 5. Verify

```bash
claude mcp list
```

Should show `dark-memory` (and optionally `dark-research`, `[FUTURE-MCP-N]`) as connected.
