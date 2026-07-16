---
name: Bug report
about: Report a defect in dark-memory-mcp
title: '[BUG] '
labels: bug, needs-triage
assignees: ''
---

## Summary

One-sentence description of the bug.

## Environment

- **dark-memory-mcp version**: `git describe --tags` or `bin/dark-mem-mcp --version`
- **Go version**: `go version`
- **OS**: (Windows / macOS / Linux + version)
- **Database driver**: (sqlite / postgres)
- **Database version**: (SQLite via modernc.org/sqlite v1.53.0; Postgres vX.Y)
- **MCP harness**: (opencode 1.18 / Claude Desktop / Cursor / etc.)

## Reproduction

Minimal steps to reproduce the bug:

```bash
# Step 1
# Step 2
# Step 3
```

## Expected behavior

What you expected to happen.

## Actual behavior

What actually happened. Include any error messages verbatim.

```text
<error message here>
```

## Logs / output

Paste relevant log output. Set `DARK_DEBUG=1` env var for verbose logging if
needed.

## INV-* invariant

If the bug is a violation of one of the 6 operational invariants, identify which:

- [ ] INV-1 (write-path audit)
- [ ] INV-2 (per-session scoping)
- [ ] INV-3 (canary in writes)
- [ ] INV-4 (constitution audit)
- [ ] INV-5 (cache integrity)
- [ ] INV-6 (mod content sanitization)
- [ ] INV-7 (multi-tenancy / project isolation)
- [ ] None / not sure

## Suggested fix

If you have one, describe it. We accept PRs against `main`.

## Severity

- [ ] Critical — security vulnerability, data loss, server crash
- [ ] High — broken documented behavior
- [ ] Medium — broken edge case, workaround exists
- [ ] Low — cosmetic, doc typo, perf nit