# dark-memory-mcp MCPB (MCP Bundles) — one-click install for Claude Desktop

> **v2.5.2+.** Anthropic's MCPB format (formerly DXT — Desktop Extensions) for one-click local MCP server installation in Claude for macOS and Windows. Just double-click the `.mcpb` file → Claude Desktop installs.

## What is MCPB?

**MCPB** (MCP Bundles, formerly DXT — Desktop Extensions) is Anthropic's standard format for packaging local MCP servers as single-file install bundles. Anthropic donated the spec to the [modelcontextprotocol org](https://github.com/modelcontextprotocol/mcpb) and renamed it from DXT to MCPB in 2026. Currently supported by:

- **Claude for macOS** (1.0+)
- **Claude for Windows** (1.0+)
- (Linux: not yet officially supported by Claude Desktop, but the format is open — other MCP clients may add support)

A `.mcpb` file is a zip archive containing:

```
dark-memory-mcp-darwin.mcpb (ZIP)
├── manifest.json       # server metadata + run config + compatibility
├── server/
│   ├── dark-mem-mcp-amd64      # macOS x64 binary
│   └── dark-mem-mcp-arm64      # macOS arm64 (Apple Silicon)
└── (optional) icon.png
```

Claude Desktop reads the manifest, validates compatibility, extracts the bundle to a managed directory, and adds the MCP server to your config without any JSON editing.

## Compatibility matrix

Each `.mcpb` file targets one OS family and bundles both architectures:

| Bundle file | OS | Arch | Size | Claude Desktop |
|---|---|---|---|---|
| `dark-memory-mcp-darwin.mcpb` | macOS | x64 + arm64 | ~50 MB | ≥ 1.0.0 |
| `dark-memory-mcp-linux.mcpb` | Linux | x64 + arm64 | ~50 MB | not yet (format is open) |
| `dark-memory-mcp-win32.mcpb` | Windows | x64 + arm64 | ~50 MB | ≥ 1.0.0 |

Download from the [GitHub Release](https://github.com/Opita-Code/dark-memory-mcp/releases/tag/v2.5.2) → expand **Assets** → pick the file for your OS.

## Install (3 steps)

1. **Download** the right `.mcpb` file for your OS from the GitHub Release.
2. **Double-click** the file. Claude Desktop opens an install confirmation dialog showing:
   - Server name: `dark-memory-mcp`
   - Tools: 49 canonical MCP tools
   - Optional config: `DARK_DB` file path (leave blank for default location in user data dir)
3. Click **Install**. Restart Claude Desktop. The `dark-memory-mcp` server is now wired into your chat with all 49 tools available.

## Uninstall

Claude Desktop → **Settings** → **Extensions** → click `dark-memory-mcp` → **Uninstall**. The bundle directory is removed. Your npm wrapper install (if you also use that) is untouched.

## Why we use MCPB

| Install path | Steps | What user has to know |
|---|---|---|
| **npm wrapper** (`npx -y @opitacode/dark-memory-mcp`) | 1 | Paste 5-line JSON snippet into MCP host config |
| **MCPB** (`.mcpb` double-click) | 1 (double-click) | Nothing — Claude Desktop handles everything |
| **Direct binary** (`.exe` from GitHub Releases) | 5 | Download → verify SHA-256 → edit mcp.json paths |

For Claude Desktop users, MCPB is the lowest-friction path. The user doesn't see any code, any JSON, any config files. They just double-click and approve.

For non-Claude-Desktop hosts (opencode, Cursor, VS Code, Claude Code), use the npm wrapper — MCPB only works in Claude Desktop today.

## What's in the manifest

The manifest declares `server.type: binary` (perfect for our single-file Go binary), lists all 49 canonical tools, declares compatibility with macOS/Linux/Windows, and references a `DARK_DB` user-config option (file path for the SQLite database).

For the full spec, see https://github.com/modelcontextprotocol/mcpb/blob/main/MANIFEST.md

## Build

The `build-mcpb.yml` GitHub Actions workflow:
1. Cross-compiles all 6 platforms (darwin/linux/windows × amd64/arm64) using the SAME ldflags + `-trimpath` as `publish-npm.yml`
2. Zips binaries + manifest into 3 platform-specific `.mcpb` archives
3. Attaches both `.mcpb` archives AND raw binaries to the GitHub Release

The same CI build produces both the npm packages and the `.mcpb` bundles, eliminating the cross-publish drift that v2.5.0 had.

## Difference from the npm wrapper

| | npm wrapper | MCPB bundle |
|---|---|---|
| **Hosts** | opencode, Cursor, Claude Code, Claude Desktop, anything that runs Node | Claude Desktop only (as of 2026-07) |
| **Install** | 5-line JSON snippet in MCP host config | Double-click |
| **Update** | `npm update -g` | Claude Desktop auto-updates (when v2.5.x → v2.5.x+ lands) |
| **Trust model** | npm 2FA + GitHub OAuth + SLSA provenance via `--provenance` | Same (SLSA via GitHub Release provenance) |
| **Bundle size** | ~5 KB wrapper + ~25 MB binary (downloaded on first install) | ~50 MB (downloaded on first install) |

Both install paths use the **same Go binary** built from the **same git commit** with the **same ldflags**. The only difference is packaging.
