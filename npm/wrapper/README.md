# @opita-code/dark-memory-mcp

> Cross-platform npm wrapper for the dark-memory-mcp MCP server.

## What is this?

This package is a **shim**. It does not contain the dark-memory-mcp binary itself. Instead, it ships a tiny Node.js script that detects your OS + CPU architecture, then loads the matching platform-specific sub-package (which **does** contain the real Go binary).

This pattern is the same one used by the official Microsoft MCP server and by `npm install` itself for native binaries. It lets you install one package name everywhere and let npm pick the right binary for your platform.

## Install

For **Claude Code / Cursor / VS Code / opencode** etc., add this to your MCP host config (`~/.config/opencode/opencode.jsonc`, `~/Library/Application Support/Claude/claude_desktop_config.json`, etc.):

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opita-code/dark-memory-mcp"]
    }
  }
}
```

That's it. The first invocation downloads the wrapper + your platform's binary; subsequent invocations use the npm cache.

## How it works

```
npx -y @opita-code/dark-memory-mcp
  |
  v
node wrapper/index.js   <-- this package
  |   (detects platform + resolves @opita-code/dark-memory-mcp-{platform}-{arch})
  v
spawn node platform-package/index.js   <-- sub-package
  |   (locates bin/dark-mem-mcp[.exe])
  v
spawn dark-mem-mcp[.exe]   <-- real Go MCP server, stdio MCP protocol
```

All three layers inherit stdio so the JSON-RPC stream from your AI client reaches the Go binary unchanged.

## Supported platforms

| OS       | Arch   | Package                                       |
|----------|--------|-----------------------------------------------|
| macOS    | x64    | `@opita-code/dark-memory-mcp-darwin-x64`     |
| macOS    | arm64  | `@opita-code/dark-memory-mcp-darwin-arm64`   |
| Linux    | x64    | `@opita-code/dark-memory-mcp-linux-x64`      |
| Linux    | arm64  | `@opita-code/dark-memory-mcp-linux-arm64`    |
| Windows  | x64    | `@opita-code/dark-memory-mcp-win32-x64`      |
| Windows  | arm64  | `@opita-code/dark-memory-mcp-win32-arm64`    |

## Windows note

On Windows, some MCP hosts have trouble invoking `npx` directly. If you see "command not found" or PATH issues, use the `cmd /c` wrapper:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "cmd",
      "args": ["/c", "npx", "-y", "@opita-code/dark-memory-mcp"]
    }
  }
}
```

## Environment variables

The wrapper passes through all CLI args to the underlying binary. You can also set environment variables:

| Variable                     | Default                  | Purpose                                  |
|------------------------------|--------------------------|------------------------------------------|
| `DARK_DB`                    | platform user data dir   | Where the SQLite DB lives                |
| `DARK_DRIFT_JUDGE_DAEMON_URL`| (LLM-as-judge daemon)    | Drift judge endpoint                     |
| `ANTHROPIC_API_KEY`          | (empty)                  | Anthropic API key (for drift_judge)      |
| `OPENAI_API_KEY`             | (empty)                  | OpenAI API key (for drift_judge)         |
| `GEMINI_API_KEY`             | (empty)                  | Gemini API key (for drift_judge)         |

Set them in your MCP host config under `env`:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opita-code/dark-memory-mcp"],
      "env": {
        "DARK_DB": "C:\\Users\\you\\dark-memory.db",
        "ANTHROPIC_API_KEY": "sk-ant-..."
      }
    }
  }
}
```

## License

MIT — same as dark-memory-mcp itself. See [LICENSE](https://github.com/Opita-Code/dark-memory-mcp/blob/main/LICENSE).

## Source

- Repository: https://github.com/Opita-Code/dark-memory-mcp
- npm wrapper source: https://github.com/Opita-Code/dark-memory-mcp/tree/main/npm/wrapper
- Issue tracker: https://github.com/Opita-Code/dark-memory-mcp/issues
- Official MCP Registry entry: `io.github.opita-code/dark-memory-mcp`
