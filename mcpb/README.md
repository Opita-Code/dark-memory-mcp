# dark-memory-mcp MCPB (MCP Bundles) — one-click install for Claude Desktop

> **Sprint 3** of the install-friction reduction roadmap. Adds `.mcpb` bundles (the new name for DXT — Desktop Extensions) so Claude Desktop users can install dark-memory-mcp with a single double-click.

## What is MCPB?

MCPB (MCP Bundles) is the renamed and standardized format for **D**esktop e**X**tension **T**ool files (`.dxt`). Anthropic donated the spec to the modelcontextprotocol org, renamed it to **MCPB** (`.mcpb` extension), and made it the standard for one-click local MCP server installation in desktop apps like Claude for macOS/Windows.

A `.mcpb` file is a zip archive containing:
- `manifest.json` — server metadata, run config, compatibility matrix
- `server/` — the actual server files (binary in our case)
- `icon.png` — optional icon

Claude Desktop reads the manifest, validates compatibility, extracts the bundle to a managed directory, and adds the MCP server to the user's config without manual JSON editing.

Source: https://github.com/modelcontextprotocol/mcpb (formerly `anthropics/dxt`)

## What we ship

For each OS family (darwin, linux, win32), we ship a platform-specific `.mcpb` bundle attached to the GitHub Release. Each bundle contains:

```
dark-memory-mcp-darwin.mcpb (ZIP, ~50 MB)
├── manifest.json
└── server/
    ├── dark-mem-mcp-amd64   (darwin x64 binary)
    └── dark-mem-mcp-arm64   (darwin arm64 binary)

dark-memory-mcp-linux.mcpb (ZIP, ~50 MB)
├── manifest.json
└── server/
    ├── dark-mem-mcp-amd64
    └── dark-mem-mcp-arm64

dark-memory-mcp-win32.mcpb (ZIP, ~50 MB)
├── manifest.json
└── server/
    ├── dark-mem-mcp-amd64.exe
    └── dark-mem-mcp-arm64.exe
```

The manifest declares compatibility with macOS/Linux/Windows (Claude Desktop picks the right binary for the user's arch at install time).

## Compatibility matrix

| Bundle file | Claude Desktop | macOS | Linux | Windows |
|---|---|---|---|---|
| `dark-memory-mcp-darwin.mcpb` | ≥ 1.0.0 | ✅ x64 + arm64 | ❌ | ❌ |
| `dark-memory-mcp-linux.mcpb` | ≥ 1.0.0 | ❌ | ✅ x64 + arm64 | ❌ |
| `dark-memory-mcp-win32.mcpb` | ≥ 1.0.0 | ❌ | ❌ | ✅ x64 + arm64 |

## Install

1. Download the right `.mcpb` file for your OS from the GitHub Release (links above)
2. Double-click the file → Claude Desktop opens an install confirmation dialog
3. Review the manifest (it asks for `DARK_DB` file path as user_config; leave blank for default)
4. Click "Install" → Claude Desktop extracts the bundle to `~/Library/Application Support/Claude/mcpb/` (or `%APPDATA%\Claude\mcpb\` on Windows)
5. Restart Claude Desktop → `dark-memory-mcp` shows up as an MCP server with all 35 tools

## Uninstall

Claude Desktop → Settings → Extensions → `dark-memory-mcp` → Uninstall. The bundle directory is removed; the npm wrapper install (if you used that) is untouched.

## Build

The `build-mcpb.yml` GitHub Actions workflow cross-compiles 6 platform binaries (one matrix per OS+arch), zips them into 3 platform-specific bundles (darwin, linux, win32), and attaches all 3 to the GitHub Release as `dark-memory-mcp-{os}.mcpb` assets.

To run locally:

```bash
# 1. Cross-compile all 6 platforms
for GOOS in darwin linux windows; do
  for GOARCH in amd64 arm64; do
    EXT=""
    [ "$GOOS" = "windows" ] && EXT=".exe"
    [ "$GOOS" = "linux" ] && GOOS=linux  # no-op, just for clarity
    GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=0 \
      go build -ldflags "-s -w -X github.com/dark-agents/dark-memory-mcp/internal/version.buildVersion=2.5.2" \
        -o "build/dark-mem-mcp-${GOOS}-${GOARCH}${EXT}" ./cmd/dark-mem-mcp
  done
done

# 2. Build the 3 bundles
for OS in darwin linux; do
  mkdir -p build/${OS}/server
  cp build/dark-mem-mcp-${OS}-amd64 build/${OS}/server/dark-mem-mcp-amd64
  cp build/dark-mem-mcp-${OS}-arm64 build/${OS}/server/dark-mem-mcp-arm64
  cp mcpb/manifest.json build/${OS}/manifest.json
  (cd build && zip -r ../dark-memory-mcp-${OS}.mcpb ${OS}/)
done

# Windows needs .exe suffix
mkdir -p build/win32/server
cp build/dark-mem-mcp-windows-amd64.exe build/win32/server/dark-mem-mcp-amd64.exe
cp build/dark-mem-mcp-windows-arm64.exe build/win32/server/dark-mem-mcp-arm64.exe
cp mcpb/manifest.json build/win32/manifest.json
(cd build && zip -r ../dark-memory-mcp-win32.mcpb win32/)
```

## Why this is the same binary as npm

The 6 binaries in the `.mcpb` bundles are byte-identical to the ones in the npm platform packages (darwin-x64, darwin-arm64, etc.). Same git commit, same ldflags, same cross-compile step. Verified by `tests/distribution/npm_wrapper_v2_5_2_test.go`:
`TestV260_NPMBinaryMatchesReleaseBinary` cross-checks SHA-256 between npm-tarball contents and the GitHub Release binaries.

## What this does NOT do

- Does not work outside Claude Desktop (DXT/MCPB only supported there as of 2026-07)
- Does not bundle a Claude Desktop-compatible Python or Node.js runtime (the binary is statically linked, no runtime deps)
- Does not override Claude Desktop's built-in MCP server discovery (you can't load it as a `.mcpb` from a URL — must be local file)

If you need non-Claude-Desktop support, use the npm wrapper: `npx -y @opitacode/dark-memory-mcp`.
