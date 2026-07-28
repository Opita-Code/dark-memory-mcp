#!/usr/bin/env node
'use strict';

/**
 * dark-memory-mcp platform package (linux-arm64).
 *
 * Spawns the Go binary in bin/dark-mem-mcp with inherited stdio so
 * the MCP host's JSON-RPC stream passes through unchanged.
 */

const path = require('path');
const { spawn } = require('child_process');

const BINARY_NAME = 'dark-mem-mcp';
const BINARY_PATH = path.join(__dirname, 'bin', BINARY_NAME);

function spawnBinary() {
  const args = process.argv.slice(2);

  const child = spawn(BINARY_PATH, args, {
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
    console.error(`dark-memory-mcp: failed to start ${BINARY_NAME}: ${err.message}`);
    console.error(`Binary path: ${BINARY_PATH}`);
    console.error(`This usually means the npm publish step did not include the binary.`);
    console.error(`Report: https://github.com/Opita-Code/dark-memory-mcp/issues`);
    process.exit(1);
  });
}

spawnBinary();
