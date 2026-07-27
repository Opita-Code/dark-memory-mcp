# vibe-spec: splitStatements SQL-aware upgrade (v2.1.0)

**Owner**: Nico
**Trigger**: v2.1.0 agent_memory needs FTS5 sync triggers (multi-statement
BEGIN..END bodies). The current splitStatements at
`internal/migrate/migrate.go:204` is a naive `strings.Split(body, ";")`
that breaks at every `;` regardless of SQL context. It can't handle:
  - Single-quoted string literals with embedded `;`
  - `--` line comments with embedded `;`
  - `BEGIN..END` blocks (any trigger body, PL/pgSQL function bodies,
    Postgres `DO $$ ... $$` blocks, etc.)
  - Dollar-quoted strings (`$tag$ ... $tag$`, Postgres)

This is a hard prerequisite for the agent_memory v18 migration which
contains 4 FTS5 sync triggers. Without the upgrade, the migrations
have to be split into v18-v22 (5 separate migrations for what should
be one logical change) — that pollutes the version namespace and
makes future rollbacks confusing.

## Goal

Upgrade `splitStatements` to a small SQL-aware splitter that handles:
  1. Line comments (`--` to end of line)
  2. Single-quoted string literals with `''` escape
  3. BEGIN..END blocks (with full nesting — a trigger inside a
     function, etc.)
  4. Dollar-quoted strings (`$tag$...$tag$`) — Postgres, but cheap
     to support so we may as well
  5. Block comments (`/* ... */`) — SQLite/Postgres both support,
     cheap to add

Out of scope (NOT required for v2.1.0):
  - Full SQL lexer with proper identifier handling
  - Operator-aware tokenization (e.g., `<>` vs `<` `>`)
  - Backslash continuations (`\` at end of line)

## Non-goals

- Don't try to validate SQL syntax. The splitter just needs to identify
  statement boundaries; correctness is SQLite/Postgres's job.
- Don't change the public Migration struct (Up stays `string`).
- Don't add new migrations or change existing ones' *content* — only
  the splitter upgrade.

## Acceptance criteria

A new test file `internal/migrate/split_test.go` with table-driven
tests that cover at minimum:

  1. Empty string → `[]string{}`
  2. Single statement no `;` → 1 stmt (trailing recovery)
  3. Two statements with `;` between → 2 stmts
  4. String literal with `;` inside → 1 stmt (literal preserved)
  5. Escaped quote (`''`) inside literal → 1 stmt
  6. Line comment with `;` inside → `;` after comment is a real split
  7. Trigger body (BEGIN INSERT; END) → 1 stmt
  8. Nested BEGIN..END (rare but possible) → 1 stmt
  9. Two triggers separated by `;` → 2 stmts
  10. Dollar-quoted string with `;` inside (`$tag$...;...$tag$`) →
      1 stmt
  11. Block comment `/* ... ; ... */` → splits around the comment
  12. Multi-line trigger with line break before END → 1 stmt
  13. Idempotency: `v18`-style body with table+indexes+triggers all
      in one migration → expected split count
  14. Backward compat: every existing migration v1..v17 produces the
      same statement split as before (no test regressions)

Plus:
  - All existing migration tests pass (`go test ./internal/migrate/...`)
  - All tools tests pass (the v18 failure goes away)
  - The agent_memory v18-v22 migration set can be consolidated back
    to a single v18 with all 4 triggers in one Up string

## Implementation plan

1. **Write tests first** (`internal/migrate/split_test.go`) — TDD.
   This is the drift-check: the impl MUST pass these tests.
2. **Rewrite `splitStatements`** as a small state machine:
   - States: normal / in_line_comment / in_block_comment /
     in_string / in_dollar_quote / in_begin_end_block
   - The split point is `;` ONLY in normal state.
   - Track BEGIN..END nesting (push on BEGIN, pop on END).
   - Track dollar-quote tag matching (`$tag$...$tag$`).
3. **Run the test suite** — every case in the table must pass.
4. **Verify existing migrations** — split-counts unchanged for v1-v17.
5. **Consolidate v18-v22 → v18** in `internal/migrate/sqlite/ddl.go`
   (and mirror in postgres).
6. **Run full test suite** — must be green.

## Drift-check (manual)

Run these commands. If any fails, the impl has drift:

```bash
# 1. Splitter unit tests pass
go test ./internal/migrate/... -run TestSplitStatements -v

# 2. Existing migration tests still pass (no regressions)
go test ./internal/migrate/...

# 3. The v18 boot flow works end-to-end (this was failing before)
go test ./internal/tools/... -run TestVLPHandleEventTool_BootToComplete

# 4. Full build clean
go build ./...

# 5. Existing integration tests pass
go test ./tests/dual_driver/...
```

If all 5 pass, the upgrade is aligned with the spec. If any fails,
fix and re-run.

## Risks

- **Risk**: A new migration in the future uses a SQL construct we
  didn't think of. **Mitigation**: tests cover the cases we know
  about; future migrations should add tests for new constructs.
- **Risk**: Postgres `$tag$` handling breaks if a tag starts with a
  digit. **Mitigation**: dollar-quote tags follow Postgres identifier
  rules (must start with letter or underscore); tests cover the
  canonical `$tag$` form.
- **Risk**: Backward compat — a subtle change in split counts breaks
  an existing migration. **Mitigation**: every existing migration
  must produce the same stmt count after the upgrade. The test for
  v17 body is the regression guard.

## Out of scope for this spec

- Migrating to a real SQL parser library (e.g., xwb1989/sqlparser).
  That's an order of magnitude more work; not justified for the
  migrations we have.

## Sign-off

Spec author: Nico
Status: DRAFT (in progress)
Last edited: 2026-07-27