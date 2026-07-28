#!/usr/bin/env node
'use strict';

/**
 * dark-memory-mcp npm wrapper (Microsoft pattern).
 *
 * Architecture:
 *   npx -y @opita-code/dark-memory-mcp
 *     -> main wrapper index.js (this file)
 *        -> platform-package index.js (spawn node, stdio: inherit)
 *           -> dark-mem-mcp[.exe] (spawn, stdio: inherit)
 *
 * The MCP host (Claude Code, Cursor, VS Code) communicates with the
 * innermost Go binary via stdio. All three layers inherit stdio so
 * the JSON-RPC stream passes through unchanged.
 *
 * Source: https://github.com/microsoft/mcp/blob/main/eng/npm/wrapperBinariesArchitecture.md
 */

const path = require('path');
const { spawn } = require('child_process');

/**
 * Map of supported Node process.platform+arch to npm package name.
 * Keep in sync with the `optionalDependencies` block of package.json.
 */
const PLATFORM_MAP = Object.freeze({
  'darwin-x64':    '@opita-code/dark-memory-mcp-darwin-x64',
  'darwin-arm64':  '@opita-code/dark-memory-mcp-darwin-arm64',
  'linux-x64':     '@opita-code/dark-memory-mcp-linux-x64',
  'linux-arm64':   '@opita-code/dark-memory-mcp-linux-arm64',
  'win32-x64':     '@opita-code/dark-memory-mcp-win32-x64',
  'win32-arm64':   '@opita-code/dark-memory-mcp-win32-arm64',
});

function detectPlatform() {
  const key = `${process.platform}-${process.arch}`;
  const pkg = PLATFORM_MAP[key];
  if (!pkg) {
    console.error(`dark-memory-mcp: unsupported platform ${key}`);
    console.error(`Supported: ${Object.keys(PLATFORM_MAP).join(', ')}`);
    process.exit(1);
  }
  return pkg;
}

function loadPlatformEntry(platformPkg) {
  try {
    return require.resolve(`${platformPkg}/index.js`);
  } catch (err) {
    console.error(`dark-memory-mcp: could not resolve ${platformPkg}/index.js`);
    console.error(`This usually means the platform-specific optional dependency failed to install.`);
    console.error(`Try: npm install -g @opita-code/dark-memory-mcp`);
    console.error(`Underlying error: ${err.message}`);
    process.exit(1);
  }
}

function main() {
  const platformPkg = detectPlatform();
  const platformEntry = loadPlatformEntry(platformPkg);

  const args = process.argv.slice(2);

  const child = spawn(process.execPath, [platformEntry, ...args], {
    stdio: 'inherit',
  });

  child.on('exit', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
    } else {
      process.exit(code || 0);
    }
  });

  child.on('error', (err) => {
    console.error(`dark-memory-mcp: failed to launch platform package: ${err.message}`);
    process.exit(1);
  });
}

main();
