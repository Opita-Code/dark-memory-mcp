# dark-memory-mcp â€” npm install guide

> Single-command install via `npx`. No manual binary download. No PATH tweaking. No SHA256 verification. The `npx` flow downloads the wrapper + your platform's binary in one shot and caches them under `~/.npm/_npx`.

---

## Quickstart (5 lines, 30 seconds)

Add this to your MCP host config and you're done.

### Claude Code

`~/.claude.json`:

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

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

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

### opencode

`~/.config/opencode/opencode.jsonc`:

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

### Cursor

`~/.cursor/mcp.json`:

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

Restart the host. The first call will trigger `npx` to download the wrapper + the platform binary; subsequent calls hit the cache. Cold-install is ~25 MB download; warm calls are zero.

---

## What happens on first run

```
$ npx -y @opitacode/dark-memory-mcp
```

1. npm creates a temporary cache at `~/.npm/_npx/<hash>/node_modules/`.
2. Downloads `@opitacode/dark-memory-mcp` (the wrapper, ~5 KB).
3. npm resolves `optionalDependencies` and installs only the package matching your platform+arch (e.g. `@opitacode/dark-memory-mcp-darwin-arm64`, ~25 MB).
4. Executes `node_modules/@opitacode/dark-memory-mcp/index.js`.
5. Wrapper detects platform â†’ requires `@opitacode/dark-memory-mcp-darwin-arm64`.
6. Platform package's `index.js` spawns `bin/dark-mem-mcp` (the Go binary).
7. Go binary reads/writes the JSON-RPC stream on stdio â€” same as if you'd downloaded it manually.

Total time on a typical Mac: 2â€“4 seconds cold, <100 ms warm.

---

## Configuration

### Set the SQLite DB path

By default the Go binary writes its SQLite DB to a platform-specific user-data directory. Override with `DARK_DB`:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp"],
      "env": {
        "DARK_DB": "/Users/you/.dark-memory/memoria.db"
      }
    }
  }
}
```

### Configure drift_judge

`dark_memory_judge(eval_type=drift_judge)` calls an external LLM-as-judge. Set one of:

```json
"env": {
  "DARK_DRIFT_JUDGE_DAEMON_URL": "https://your-judge.example.com",
  "ANTHROPIC_API_KEY": "sk-ant-...",
  "OPENAI_API_KEY": "sk-...",
  "GEMINI_API_KEY": "..."
}
```

If no key is set, `drift_judge` returns `verdict=aligned` without contacting an LLM (useful for vibe-coding without API spend). See the [drift_judge docs](../README.md#drift-judge) for the full configuration matrix.

---

## Platform matrix

| OS           | Arch   | npm sub-package                                  |
|--------------|--------|--------------------------------------------------|
| macOS        | x64    | `@opitacode/dark-memory-mcp-darwin-x64`        |
| macOS        | arm64  | `@opitacode/dark-memory-mcp-darwin-arm64`      |
| Linux        | x64    | `@opitacode/dark-memory-mcp-linux-x64`         |
| Linux        | arm64  | `@opitacode/dark-memory-mcp-linux-arm64`       |
| Windows      | x64    | `@opitacode/dark-memory-mcp-win32-x64`         |
| Windows      | arm64  | `@opitacode/dark-memory-mcp-win32-arm64`       |

`optionalDependencies` means npm will install only the matching sub-package. Other platforms' packages won't be downloaded. If your platform isn't in the list, the wrapper errors out cleanly with an actionable message.

---

## Troubleshooting

### Windows: "command not found" or PATH issues

Some MCP hosts on Windows have trouble resolving `npx` directly. Use the `cmd /c` wrapper:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "cmd",
      "args": ["/c", "npx", "-y", "@opitacode/dark-memory-mcp"]
    }
  }
}
```

### "Could not resolve @opitacode/dark-memory-mcp-{platform}-{arch}"

The platform sub-package didn't install. This usually means `optionalDependencies` was filtered out by your npm config. Check:

```bash
npm config get optional
npm config get production
```

If either is `false` (or unset, which means `true` in some npm versions), `optionalDependencies` should still install. If you see `--omit=optional` somewhere, override:

```bash
npm install -g @opitacode/dark-memory-mcp --include=optional
```

Or reinstall without the flag:

```bash
npm uninstall -g @opitacode/dark-memory-mcp
npm install -g @opitacode/dark-memory-mcp
```

### "ENOENT" on the binary

The platform sub-package installed but doesn't contain the actual `dark-mem-mcp[.exe]` binary. This is the CI failure mode â€” the GitHub Actions workflow did not copy the cross-compiled binary into the npm tarball. Open an issue: https://github.com/Opita-Code/dark-memory-mcp/issues.

### Want to install a specific version

Pin in your host config:

```json
{
  "mcpServers": {
    "dark-memory": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-memory-mcp@2.5.2"]
    }
  }
}
```

`npx -y @scope/pkg@version` always runs that exact version. Without `@version`, you get the latest.

### Want to force a reinstall

```bash
npm cache clean --force
rm -rf ~/.npm/_npx
```

Then restart your MCP host.

---

## How this differs from the GitHub Releases install

Until v2.4.x, dark-memory-mcp was distributed as raw `.exe` / ELF / Mach-O binaries in GitHub Releases. The install path was:

1. Open the Releases page.
2. Find your OS + arch.
3. Download the right `.exe`.
4. Compute SHA-256, compare with the value in the release notes.
5. Manually wire the path into your MCP host config.
6. To upgrade: repeat steps 1â€“5.

The npm install path collapses all of this into a single `npx -y @opitacode/dark-memory-mcp`. The trade-off is you now depend on npm + the operator-controlled `@opitacode` npm scope being available. Both are reasonable trade-offs for vibe-coders who don't want to think about SHA-256 sums.

The GitHub Releases download path is still supported for users who can't or won't use npm. Both paths ship the exact same Go binary (built from the same git commit by the same CI).

---

## How the wrapper works (for the curious)

```
â”Œâ”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”
â”‚ npx -y @opitacode/dark-memory-mcp                                         â”‚
â”‚   â””â”€â–¶ node wrapper/index.js                                                â”‚
â”‚        â”‚  - detects process.platform + process.arch                        â”‚
â”‚        â”‚  - require.resolve('@opitacode/dark-memory-mcp-darwin-arm64/...')â”‚
â”‚        â”‚  - spawn node with platform-package/index.js + forwarded args    â”‚
â”‚        â””â”€â–¶ node platform-package/index.js                                  â”‚
â”‚             â”‚  - path.join(__dirname, 'bin', 'dark-mem-mcp')               â”‚
â”‚             â”‚  - spawn binary with stdio: inherit                          â”‚
â”‚             â””â”€â–¶ ./dark-mem-mcp  (Go binary, stdio MCP server)             â”‚
â””â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”˜
```

All three layers inherit stdio. The MCP host writes JSON-RPC to `npx`'s stdin; it flows through Node â†’ Node â†’ Go unchanged. Same protocol, just extra process boundaries.

The pattern is documented at https://github.com/microsoft/mcp/blob/main/eng/npm/wrapperBinariesArchitecture.md (Microsoft uses it for their .NET MCP; we use it for our Go MCP).
