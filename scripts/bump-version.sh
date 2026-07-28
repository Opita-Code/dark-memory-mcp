#!/usr/bin/env bash
# Bump versions in 8 files: wrapper + 6 platforms + server.json (2 fields)
set -e
FILES="npm/wrapper/package.json npm/platform-darwin-arm64/package.json npm/platform-darwin-x64/package.json npm/platform-linux-arm64/package.json npm/platform-linux-x64/package.json npm/platform-win32-arm64/package.json npm/platform-win32-x64/package.json server.json"
for f in $FILES; do
  # Top-level "version": "2.6.2"
  sed -i 's/"version": "2.6.2"/"version": "2.7.0-alpha"/g' "$f"
  # optionalDependencies keys (wrapper/package.json only)
  sed -i 's/"@opitacode\/dark-memory-mcp-darwin-x64": "2.6.2"/"@opitacode\/dark-memory-mcp-darwin-x64": "2.7.0-alpha"/g' "$f"
  sed -i 's/"@opitacode\/dark-memory-mcp-darwin-arm64": "2.6.2"/"@opitacode\/dark-memory-mcp-darwin-arm64": "2.7.0-alpha"/g' "$f"
  sed -i 's/"@opitacode\/dark-memory-mcp-linux-x64": "2.6.2"/"@opitacode\/dark-memory-mcp-linux-x64": "2.7.0-alpha"/g' "$f"
  sed -i 's/"@opitacode\/dark-memory-mcp-linux-arm64": "2.6.2"/"@opitacode\/dark-memory-mcp-linux-arm64": "2.7.0-alpha"/g' "$f"
  sed -i 's/"@opitacode\/dark-memory-mcp-win32-x64": "2.6.2"/"@opitacode\/dark-memory-mcp-win32-x64": "2.7.0-alpha"/g' "$f"
  sed -i 's/"@opitacode\/dark-memory-mcp-win32-arm64": "2.6.2"/"@opitacode\/dark-memory-mcp-win32-arm64": "2.7.0-alpha"/g' "$f"
done
echo "Done"