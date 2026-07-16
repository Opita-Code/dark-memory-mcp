# Contributing to dark-agents projects

This document is the **constitution of conventions** for any
`dark-*` MCP server. Every rule here comes from a real bug we fixed
in production. Apply them mechanically in PR reviews; the
[Hard rules](#hard-rules) at the bottom are non-negotiable.

---

## Why this document exists

Between v1.2.0 and v1.2.4, dark-memory-mcp shipped four releases
(F36, F37, F38, F39, F40) that fixed bugs caught only by running the
**actual binary against a real JSON-RPC wire** — not Go-level
orchestrator tests. Every one of them taught us a lesson. This
document bakes those lessons into rules so the next dark-*
server (sibling dark-* future projects under the dark-agents org)
doesn't re-learn them.

## Hard rules

These rules are **inviolable**. A PR that violates one is rejected
at review; exceptions require written justification in the PR
description.

### H-1. Each MCP owns its own database file.

Per **INV-8** in [`docs/INVARIANTS.md`](docs/INVARIANTS.md).

* Each project's `defaultDSN()` must return a project-specific
  filename (`<project>.db`, never just `dark.db`).
* CI lint `scripts/lint-no-private-projects.sh` verifies the default
  doesn't collide with another known server's default.
* Migration bookkeeping tables must be project-namespaced
  (e.g. `<project>_schema_migrations`, not the bare `schema_migrations`)
  to avoid v1-vN name collisions across servers sharing a directory.

### H-2. Array/object parameters MUST accept string fallback.

Per **F36 lesson** in CHANGELOG v1.2.1.

The harness (opencode + Vercel AI SDK + the LLM) decides the JSON
shape. We don't pick — the LLM does. Therefore:

```go
"tasks": map[string]any{
    "anyOf": []any{
        // Form A: JSON array (preferred).
        map[string]any{
            "type":     "array",
            "minItems": 1,
            "items":    someItemSchema,
        },
        // Form B: JSON-encoded string (legacy gemela compat).
        map[string]any{"type": "string", "minLength": 2},
    },
},
```

The orchestrator's parser dispatches on the payload's leading byte:
`[` → unmarshal as array; `"` → unmarshal as string then re-parse;
anything else → structured `store.FieldError` with `Field="tasks"`.

> **Why this matters:** the `dark_research_spec_create` gemela in
> dark-research-mcp persists `tasks` as a string. LLMs trained
> against that history emit string form against `dark_memory_vibe_spec`
> at random. Without `anyOf` we get a generic ErrInvalidArgument
> at the wire.

### H-3. JSON-RPC wire tests are MANDATORY for every fix.

Per **wire-conformance lesson** in this release.

Go-level orchestrator tests prove the function works. They do
**not** prove:
- The schema the harness sees matches what we wrote.
- The harness+LLM can actually produce a valid payload.
- The server's error envelope propagates `Field`/`ExpectedType`.
- Migration runner survives half-migrated DBs in production.

Every fix MUST ship with at least one test in `tests/wire/`. The
test boots the actual binary (`dark-{name}.exe`) via `os/exec`,
sends newline-delimited JSON-RPC frames on stdin, and asserts
against parsed responses on stdout.

**v1.3.0 addendum — race-detector on POSIX**: on Linux/macOS dev
hosts (where `gcc` is available), the wire suite should ALSO be
run with `-race` before publishing, because the harness is the
canonical concurrency exerciser:

```bash
CGO_ENABLED=1 CC=gcc go test -race ./tests/wire/... -count=1 -timeout 120s
```

On Windows hosts (no C compiler by default; see
`docs/PRODUCTION_CHECKLIST.md` §Race detector availability), `-race`
is unavailable locally. CI on POSIX runners handles it.

### H-4. Never mention private project names in public artifacts.

Per operator mandate, hard rule, see
[`scripts/lint-no-private-projects.sh`](scripts/lint-no-private-projects.sh).

The BLOCKLIST contains private dark-* siblings and operator-side
project names. Any public file (markdown, code comment, test name,
config key) MUST NOT contain them. Replace with placeholders:
- `[FUTURE-MCP-N]` for unnamed future dark-* servers
- `[drift-judge-daemon]` for the LLM inference backend
- `[NEIGHBORING-SCRAPER]` for scan-only tools
- `[prior-evaluation-loadout]` for legacy test rigs (e.g. older
  dark-research eval drivers)

Run the lint before every `git commit` and on every CI push.
The `.ps1` variant covers Windows-CI runners.

---

## Conventions

These conventions are **defaults** — deviate with justification,
document the deviation in the PR, and update this section.

### C-1. Directory layout

Every dark-* server follows:
```
dark-{name}/
├── cmd/{name}/main.go              # ONE binary, subcommand-dispatched
├── internal/
│   ├── domain/                     # SSOT types — zero external deps
│   ├── plugin/                     # interface contracts only
│   ├── sources/                    # one folder per external resource
│   ├── pipeline/                   # chan-based, composable stages
│   ├── transport/http/              # net/http ServeMux + otelhttp
│   ├── observability/              # slog + prometheus + audit
│   ├── store/                      # the project-specific DB
│   ├── ratelimit/, circuit/        # shared infra
├── migrations/                     # numbered, go-migrate-friendly
├── configs/{name}.example.toml
├── deploy/{name}.service, Dockerfile, cross-build.sh
├── tests/{unit,integration,e2e,wire}/
├── docs/{ARCHITECTURE,SOURCES,OPERATIONS}.md
└── go.mod, README.md, CONTRIBUTING.md, CHANGELOG.md
```

### C-2. Storage: modernc.org/sqlite, NEVER cgo

Prefer pure-Go SQLite via `modernc.org/sqlite`. Add
`_ "modernc.org/sqlite"` in package main + use it in tests. The CGO
build chain breaks cross-compilation and slows CI.

### C-3. Plugin registration: init() + embed.FS

Auto-register every plugin via `init()` calls; generate the canonical
order via `//go:embed` + a codegen step. NO manual wire.go in the
main binary — that's the source of every "I forgot to register the
redis source" bug in v1.x.

### C-4. Error taxonomy: 7 sentinels + structured FieldError

```go
// sentinels (in store package):
var (
    ErrSessionRequired
    ErrInvalidArgument
    ErrNotFound
    ErrAlreadyExists
    ErrCanaryInPayload
    ErrConstitutionDrift
    ErrInvalidState
)
// structured carrier:
type FieldError struct{ Store error; Field string }
```

Orchestrators always wrap via `store.NewFieldError(store.ErrInvalidArgument, "tasks")`,
NEVER `fmt.Errorf("%w: ... invalid")`. The latter drops the field
name and forces the harness to render a generic error message.

### C-5. Tests layout

| Layer | Purpose | Speed budget |
|---|---|---|
| `tests/unit` | in-process Go function tests | < 1s total |
| `tests/orchestration` | Orchestrator + Store integration, in-memory | < 30s |
| `tests/integrations` | SQLite on disk, real migrations | < 60s |
| `tests/e2e` | HTTP server up, real curl | < 90s |
| `tests/wire` | `os/exec` + JSON-RPC against actual binary | < 30s per test |

Wire tests are **separate** from the rest so they can run in CI
without booting subprocesses for unit work. They live under
`./tests/wire/`.

### C-6. Schemas: strict, with `additionalProperties: false`

Every MCP tool's input schema must:
- declare `"type": "object"`
- declare `"required": [...]` explicitly
- declare `"additionalProperties": false`
- for arrays: declare `"minItems": 1` (rejects accidental empty)
- for typed strings: declare `"minLength": N` where N>1

opencode's harness will FORCE `additionalProperties: false`
anyway (see `opencode/packages/opencode/src/mcp/catalog.ts`,
`convertTool`), but declaring it server-side keeps the schema
self-documenting.

### C-7. Versions: Keep a Changelog + semver

Every release bumps `DARK_SERVER_VERSION` in the
`defaultDSN()`-adjacent config struct, and the CHANGELOG.md must
have a dated entry with the **Five-W's**:

```markdown
## [1.2.3] — 2026-07-16

### Fixed
- **F-something — short title.** Why: root cause. Tests: which
  tests catch the regression. Operator notes: rollback plan if
  the fix breaks.
```

The operator-facing F-codes (`F35`, `F36`, etc.) are **immutable**
once published. If you need to reference a fix in a later release,
keep the code (`F36: tasks dual-form`); never re-purpose a code.

---

## Adding a new MCP server

1. **Repository setup.** Copy this CONTRIBUTING.md and the directory
   layout from C-1. Pick a new module path
   (`github.com/<org>/<project>`).
2. **Default DSN.** Pick a project-specific SQLite filename, NOT
   `dark.db`. Add `INV-{N}` to your INVARIANTS doc that codifies
   "<project> never reads/writes another server's file".
3. **Schemas.** Apply C-6 to every tool. Every `array`/`object`
   parameter gets the `anyOf:[array,string]` fallback (H-2).
4. **Wire tests first.** Before writing the orchestrator, write
   `tests/wire/{tool}_test.go` that asserts each tool accepts both
   forms via a subprocess. THEN implement the tool to make the
   test pass. The harness-shape problem always surfaces at the wire.
5. **Field-level errors.** Every error path uses `store.NewFieldError`
   with the offending field's name. Tests assert `errors.As` against
   `store.FieldError` and verify Field.
6. **Private-name lint.** Run `scripts/lint-no-private-projects.{sh,ps1}`
   in CI. Any leak fails the build.
7. **CHANGELOG + version.** Bump `DARK_SERVER_VERSION`, add a
   Keep-a-Changelog entry.

---

## Review checklist (paste at PR bottom)

- [ ] No private project names in any file (`scripts/lint-no-private-projects.ps1`)
- [ ] Every new tool has at least one `tests/wire/` test
- [ ] Every `array`/`object` parameter has `anyOf:[array,string]`
- [ ] Every error returns `store.NewFieldError`, not `fmt.Errorf`
- [ ] No committed `.exe`, `.db`, or `.patch` files (gitignore enforced)
- [ ] Version bumped; CHANGELOG dated entry added; tests pass
- [ ] Cross-build matrix tested (`deploy/cross-build.sh`)

These checks are mechanical. If a PR fails any of them, the
reviewer should reject with a single line: "Block on check {N}".
