# dark-memory-mcp — test suite

This directory contains the integration + contract test suite. Tests
exercise the public Store / Orchestrator / Server surface end-to-end
against both the SQLite and Postgres drivers, and against the full MCP
tool registry.

## IMPORTANT — INFRA-003 / INV-10

**If you are on a corporate Windows host with WDAC + Carbon Black, the
test workflow is NOT `go test ./...`** — it will fail because freshly-built
Go test binaries are quarantined by the endpoint protection before they
can run a single test.

### The constraint

- **Windows Defender Application Control (WDAC)** + **Carbon Black**
  block unsigned, recently-compiled Go test binaries from executing.
- Symptom: `go test ./...` reports `[build failed]` or `[setup failed]`
  even when `go build ./...` succeeds.
- This is **corporate security policy**, not a bug. Do NOT attempt to
  disable WDAC or Carbon Black.

### The canonical workflow on INFRA-003 hosts

```
go build ./...         # must be clean
go vet ./...           # must be clean
go vet ./tests/...     # must be clean (catches test-file parse errors)
# + drift-judge + resolve_drift on the wave artifact
```

CI runs `go test ./...` against the full runtime suite. **CI is the
authoritative runtime signal.**

### Why no `go test` locally

- We tried `GOFLAGS=-tmpdir=...` — does not bypass Carbon Black.
- Code-signing the test binary requires corporate cert access.
- AppLocker policy is empty by design (see `internal/server/bootstrap.go`).
- WSL2 still hits WDAC for Windows-side binaries.

### When INFRA-003 resolves

If the corporate security policy changes (WDAC moves to audit-only,
Carbon Black is replaced, etc.):

1. Update the constitution: bump `dark-memory-mcp-cerebro` to v1.2.0,
   retire INV-10 (or mark it historical), document the policy change.
2. Update `docs/INFRA-003.md`: mark RESOLVED, link to IT announcement.
3. Update the drift-judge prompt: the "needs_human on interface changes"
   guidance can be relaxed since runtime tests are locally available.

Until then, INV-10 stands and the workflow above is the only path.

## See also

- `docs/INFRA-003.md` — full operator-facing explanation
- `vibe-flow/constitution/dark-memory-mcp-cerebro.constitution.toml`
  — operational_rules.10 (constitutional text)

## Layout

- `tests/cli/` — CLI command contract tests
- `tests/conformance/` — MCP bridge 7 conformance
- `tests/context/` — context-related integration
- `tests/dual_driver/` — Store contract tests (sqlite + postgres)
- `tests/e2e/` — end-to-end server tests
- `tests/economy/` — Atlan 5-bucket economy pipeline
- `tests/invariants/` — INV-5, INV-6, INV-8 invariant tests
- `tests/migrate/` — schema migration tests
- `tests/orchestration/` — orchestrator-level tests
- `tests/project/` — INV-7 project namespace tests
- `tests/tools/` — tool-handler tests
- `tests/wire/` — wire-format / envelope tests

All tests are designed to run in CI. The build+vet+drift-judge workflow
on INFRA-003 hosts is the local fallback, with CI as the runtime oracle.