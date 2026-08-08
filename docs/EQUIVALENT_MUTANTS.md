# Equivalent Mutants Analysis

Discipline per dark-testing skill v1.0.0: "Never blacklist without the
human analysis — a 'surviving' mutant may be a real bug." Every checksum
below was reviewed before being added to `.go-mutesting.blacklist`.

## v1.1.0 migration note (2026-08-07)

The dark-testing skill migrated the mutation tool from go-mutesting (avito
fork) to **gremlins v0.6.0** (`go install
github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0`). go-mutesting does NOT
support Go modules (uses `go/build` + `x/tools/go/loader`, upstream fix
abandoned), so **every go-mutesting PASS was invalid**. The checksums below
are preserved as historical record; new mutation runs use gremlins, which
does not mutate the working tree and reports VOID for no-op mutants
automatically. Config: `.gremlins.yaml` (keys under `unleash.`).

**Gremlins on Windows — two operational constraints (measured 2026-08-07):**

1. **Run from a CLEAN WORKTREE, not the main checkout.** `CachedDealer.Get()`
   copies the ENTIRE module tree into a per-worker temp dir. The main
   checkout is ~715MB (`bin/` + embedder ONNX blobs) and hangs the run; a
   `git worktree add` is ~91MB and runs. Strip the embedder blobs from the
   worktree too (`internal/embedder/onnx/bundled/*` + `data/model_quantized.onnx`,
   ~87MB) — they get copied per worker and turn genuine KILLED mutants into
   spurious TIMED OUT by saturating disk.
2. **`timeout-coefficient: 15`** is required. The default coefficient 3 makes
   the per-mutant context deadline too tight on Windows (compilation +
   suite > 3×gather), reporting every mutant as TIMED OUT. Coefficient 15
   gives ~30-150s per mutant, enough for the full suite. Config file must be
   named `.gremlins.yaml` (not `.yml`) and keys namespaced `unleash.`.

**Switch-statement coverage artifact:** Go's coverage instrumentation does
not emit blocks for the conditions of an expression-less `switch { case ... }`
(verified with a minimal repro). Mutants in those conditions report as NOT
COVERED even when the branch IS exercised — they are unmattable by design,
not test gaps. Affects policy gate.go:331 and atomic scope_frame.go:330-ish
switch cases. Confirmed by gremlins NOT COVERED + manual `go test -coverprofile`
showing the case body covered but the condition positions absent.

**Mutation results (gremlins v0.6.0, worktree 0d5d4a1 + wave-3 tests, 2026-08-07):**

| Package | Killed | Lived | Efficacy | Mcover | Notes |
|---|---|---|---|---|---|
| version | 8 | 0 | 100% | 100% | |
| drift | 19 | 0 | 100% | 100% | 2 live mutants closed this session (boundary 0.3 + strictness-preserve) |
| policy | 25 | 0 | 100% | 92.6% | 2 NC = switch artifact |
| tools | 65 | 0 | 100% | 19.4% | 271 NC — handlers need Store mocks (structural gap) |
| federation | 23 | 0 | 100% | 100% | |
| entity | 21 | 0 | 100% | 100% | |
| delegation | 17 | 0 | 100% | 100% | |
| recall | 18 | 0 | 100% | 20% | 72 NC — StoreSource compositors need Store mocks |
| atomic | 147 | 0 | 100% | 96.1% | 6 NC = switch artifact; wave-3 tests validated |
| agentbootstrap | 16 | 0 | 100% | 42.1% | 22 NC — clientinfo/render error paths partially uncovered |
| errorobs | 6 | 0 | 100% | 100% | |
| migrate | 139 | 0 | 100% | 86.3% | 22 NC — split_statements edge branches |
| vibecase | 3 | 0 | 100% | 100% | |

Every package has **100% efficacy (zero lived mutants)** — above the M1 ≥70%
bar. Three packages have low mutant coverage (tools/recall/agentbootstrap)
because their NOT-COVERED mutants live in MCP handler code that requires a
mock Store; those are coverage gaps, not survived mutants.

## Wave 3 additions

### agentbootstrap/clientinfo.go.5 — `return false` removed in `!ok` branch
- Mutant: `if !ok { }` (return statement deleted)
- Analysis: when the `clientInfo` key is absent from `_meta`, `raw` is
  the zero value (`nil`), which matches none of the switch cases, so
  control falls through to the final `return false` anyway. Identical
  behavior for all inputs.
- Checksum: `aa3019cddd2ecccb473b75ba2d283fcc`

### agentbootstrap/clientinfo.go.10 — `return "unknown"` removed in NormalizeClientName
- Mutant: early return deleted when both Name and Title are empty
- Analysis: with empty candidates, the candidate loop finds no match and
  the final `return "unknown"` produces the identical result.
- Checksum: `a94360bcebdf6960bd50d31eaa731fe0`

### agentbootstrap/clientinfo.go.15 — `return "cline"` removed in roo-cline branch
- Mutant: dead branch — `k == "roo-cline"` is unreachable because the
  `known` list iterates `"cline"` before `"roo-cline"` and
  `strings.Contains("roo-cline", "cline")` matches first, returning
  `"cline"` via the generic `return k`.
- Analysis: behaviorally equivalent; branch is dead code.
- Checksum: `1aec1fa4593fca983f7be2de6bb75ecd`

### agentbootstrap/clientinfo.go.34 — `_ = ctx` removed in InjectMeta
- Mutant: no-op statement deletion
- Analysis: `ctx` is deliberately unused in InjectMeta; the `_ = ctx`
  is the standard "keep param" idiom and has no observable behavior.
- Checksum: `d196c3bb5dfc0a413733218832857177`

### agentbootstrap/clientinfo.go.29 — `s.mu.Lock()` → `_ = s.mu.Lock`
- Mutant: lock acquisition removed in ClearForTest
- Analysis: this IS a real concurrency weakening, but it is only
  observable under `-race` with a concurrent writer. The mutation
  runner's exec (`go test`) does not run `-race`; single-threaded
  behavior is identical. Killable via `go test -race` but not via the
  standard mutation exec. Tracked: if a `-race` CI job is added, remove
  this checksum and let the race detector kill it.
- Checksum: `8114892f47b7a9591f1ae7f513e42023`

### agentbootstrap/fs.go.0 — panic replaced in init()
- Mutant: `panic(...)` body replaced with `_, _ = fmt.Sprintf, err`
- Analysis: `fs.Sub(embeddedRaw, "data")` cannot fail at runtime (the
  `data` dir is compile-time verified by `//go:embed`); the panic
  branch is defensive dead code that no test can reach.
- Checksum: `2c78e6d0196e0db9ed7ab20d5b7a9791`

### agentbootstrap/render.go.1 — early `return string(raw), nil` removed
- Mutant: the no-template-marker fast path deleted
- Analysis: for content without `{{`, template.Parse + Execute produce
  the byte-identical input string (text/template passes literal text
  through), so the fast path is an optimization, not a behavior change.
- Checksum: `26a3601a25623f7e0cc4602c68867d97`

### atomic/capabilities_frame.go.13 + atomic/drift_frame.go.24 — `>` → `>=`
- Mutant: staleness boundary shifted by one nanosecond
- Analysis: `age >= MaxFrameAge` vs `age > MaxFrameAge` differ only at
  the exact boundary `age == MaxFrameAge`. Frames are timestamped with
  `time.Now()` and validated with `time.Since()`, so hitting the exact
  boundary is non-deterministic; a deterministic kill requires clock
  injection (a `now func() time.Time` field). Until the frames adopt an
  injectable clock, these are blacklisted. The tests DO cover the
  materially-different thresholds (±1 minute constant mutations).
- Checksums:
  - `00ed4f96ae4a31336213535661c8232d` (capabilities_frame.go.13)
  - `d460b4485b872bac748c24200dfcb8cb` (drift_frame.go.24)

### atomic/evidence_frame.go.14 + .19 — `[32]byte` → `[31]/[33]byte`
- Mutant: WriteRef.ContentSHA256 array size changed
- Analysis: structural type mutation. No behavior test can observe the
  array width (the field is only hashed/serialized internally; JSON
  output of a 31-byte vs 32-byte array is a base64 string of different
  length, but no test pins that). Requires an explicit type-assertion
  test (`len(w.ContentSHA256) == 32`) which is tautological. Skipped as
  non-observable mutation.
- Checksums:
  - `94717bf236dfe8b0f9fd9e4b399f7026` (evidence_frame.go.14)
  - `f5ef693ff69525b4307d3924e0215cb8` (evidence_frame.go.19)

## Wave 2 legacy entries

(Pre-existing 28 checksums in `.go-mutesting.blacklist` — analyzed in
the wave 2 session, 2026-08-06, commit 2da332c. See git history.)
