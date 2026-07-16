# Contributing to Dark Memory MCP

Thanks for your interest in dark-memory-mcp. This project is part of the
[dark-agents-v2](https://github.com/Opita-Code) ecosystem — a standalone Go
module that provides persistent memory + workflow orchestration for MCP-native
harnesses.

## Code of conduct

This project follows the [Contributor Covenant v2.1](CODE_OF_CONDUCT.md). By
participating you agree to its terms.

## What we accept

- **Bug fixes** that have a clear repro and a minimal change.
- **Performance improvements** with before/after measurements.
- **Documentation fixes** (typos, broken links, unclear examples).
- **New orchestrators** that follow the RFC §D-9 contract and ship with tests.
- **New context projections** that follow the design rules in
  [`docs/CONTEXT_OBJECTS.md`](docs/CONTEXT_OBJECTS.md).

## What we don't accept (yet)

- New binary distribution formats (deb, rpm, brew, scoop). v1.0 is `go install`
  + `go build`. Packaging is v1.1 work.
- New storage drivers beyond SQLite + Postgres. MySQL/etcd are explicit
  non-goals per the constitution.
- Replacement of the canonical 25-tool order. The order is a wire contract —
  adding a tool is fine; renumbering breaks harnesses.

## Development setup

```bash
# Clone
git clone https://github.com/Opita-Code/dark-memory-mcp
cd dark-memory-mcp

# Requires Go 1.25+
go version

# Run all tests (~11 min with -count=1 cold rebuild)
go test -count=1 ./...

# Run just the contract tests (~7s, sqlite only)
go test ./tests/dual_driver/...

# Run the MCP Inspector conformance test (~14s, requires Go toolchain)
go test ./tests/conformance/...

# Run the invariant tests (~6s)
go test ./tests/invariants/...

# Build the 3 binaries
go build -o bin/dark-mem-mcp ./cmd/dark-mem-mcp
go build -o bin/dark-mem-cli ./cmd/dark-mem-cli
go build -o bin/dark-mem-inspect ./cmd/dark-mem-inspect
```

### Postgres testing (optional, requires local Postgres)

```bash
export DARK_TEST_POSTGRES_DSN="postgres://user:pass@localhost:5432/darkdb?sslmode=disable"
go test ./tests/dual_driver/...
```

The CI runner exercises both drivers. Local dev box can skip Postgres if you
only care about the sqlite path.

## Project layout

```
dark-memory-mcp/
├── cmd/                  # 3 binaries (mcp / cli / inspect) — each with its own go.mod
├── internal/
│   ├── audit/            # write_audit event types (INV-1)
│   ├── constitution/     # INV-4 watchdog
│   ├── context/          # 8 context projections
│   ├── economy/          # Atlan 2026 token economy pipeline
│   ├── llm/              # LLM client + cache (INV-5)
│   ├── migrate/          # Schema migrations v1..v7
│   ├── mods/             # Mod loader (INV-6)
│   ├── orchestration/    # 9 typed orchestrators (RFC §D-6)
│   ├── project/          # INV-7 multi-tenancy
│   ├── research/         # OSINT run/item/link types
│   ├── safety/           # Canary + injection markers (INV-3, INV-6)
│   ├── server/           # MCP server bootstrap + lifecycle
│   ├── session/          # Session lifecycle types
│   ├── ssd/              # LLM-as-judge verdict types
│   ├── store/            # Store interface + sqlite/postgres impls
│   ├── tools/            # 26 tool handlers + canonical registry
│   └── vibeflow/         # Spec/artifact/drift types
├── tests/                # 9 test suites (cli, conformance, context, dual_driver, e2e, ...)
├── docs/                 # 6 operator-facing runbooks
├── vibe-flow/            # RFC + BRIDGE + constitution + master plan
└── README.md
```

## Testing policy

Every PR must pass all 9 test suites locally:

```bash
go test -count=1 ./...
```

CI runs the same on Linux + macOS runners. The 1 flake we know about
(review-w4-b01) is `go test -race` on Windows without TDM-GCC — this is a
tooling gap, not a code issue. Don't worry about it unless you're adding
concurrent code paths.

## Pull request process

1. **Open an issue first** for non-trivial changes (anything beyond a typo or
   small bug fix). We use issues to discuss design before code lands.
2. **Branch from `main`**. Use the convention `topic/short-description`
   (e.g. `fix/canary-bypass`, `feat/new-context-projection`).
3. **Keep commits atomic**. One logical change per commit. Use the prefixes:
   - `feat:` for new functionality
   - `fix:` for bug fixes
   - `docs:` for documentation only
   - `test:` for adding tests
   - `refactor:` for restructuring without behavior change
   - `chore:` for tooling/build/docs maintenance
4. **Write a spec for any non-trivial change**. Use `dark_research_spec_create`
   (C1 case for code, C2 for text/image/video artifacts). The spec is the
   source of truth; the implementation is checked against it via
   `dark_ssd_drift_judge`.
5. **Add or update tests** for any behavior change. The 9 test suites are the
   contract — every new tool, every new orchestrator, every new invariant must
   be exercised by at least one test.
6. **Update docs** if your change affects operator workflow (CLI flags, env
   vars, runbook, README).
7. **One approver required** from a maintainer. Complex changes (new
   orchestrators, schema migrations, security-sensitive code) need 2.

## Coding style

- **Go formatting**: `gofmt -s -w .` (or `goimports -w .` for import ordering)
- **Comments**: all exported types/functions MUST have godoc comments starting
  with the name. No inline comments unless necessary.
- **Errors**: return sentinel errors (`ErrFoo`), wrap with `fmt.Errorf("context:
  %w", err)`, use `errors.Is` for checking.
- **Context**: always accept `context.Context` as the first parameter in
  handlers and long-running functions.
- **Thread safety**: use `sync.Mutex` for shared state; document thread-safety
  requirements in comments.
- **JSON**: use `json` tags with `omitempty` for optional fields; use
  `json.RawMessage` for flexible/deferred parsing.

## Release process

We follow [SemVer 2.0.0](https://semver.org/). Breaking changes bump the major
version; the wire-format tool order is a breaking change for harnesses that
index by position, so renumbering requires a major bump.

Releases are cut from `main` via annotated git tags. The current tag is
[`v1.0.0`](https://github.com/Opita-Code/dark-memory-mcp/releases/tag/v1.0.0).

## Questions?

Open an issue. We read all of them.