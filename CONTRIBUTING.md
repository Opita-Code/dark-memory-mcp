# Contributing to `dark-memory-mcp` v4.0

> **TL;DR**: v4.0 ships with five extension points: **tools**,
> **adapters**, **constitutions**, **mod packs**, and **research
> backends**. Every extension passes through the same review
> gate: mutation score ≥ 80%, all 15 invariant tests pass, all 7
> Tier-1 security regression tests pass, and `drift_judge` returns
> `aligned`. This document is the procedural contract.

| Field | Value |
|---|---|
| Audience | contributor, maintainer, security auditor |
| Level | advanced |
| Status | review (pending constitution-maintainers approval) |
| Version | v4.0.0-alpha.1 |
| Last reviewed | 2026-09-24 |
| Branch | `feat/v4-redesign` |
| Companion | [ARCHITECTURE-V4.md](./ARCHITECTURE-V4.md) |

---

## Table of contents

1. [Five extension points](#1-five-extension-points)
2. [Review gate](#2-review-gate)
3. [Adding a tool](#3-adding-a-tool)
4. [Adding an adapter](#4-adding-an-adapter)
5. [Adding a research backend](#5-adding-a-research-backend)
6. [Adding a constitution](#6-adding-a-constitution)
7. [Adding a mod pack](#7-adding-a-mod-pack)
8. [Modifying the workflow](#8-modifying-the-workflow)
9. [Security disclosures](#9-security-disclosures)
10. [Style guide](#10-style-guide)

---

## 1. Five extension points

| Point | Where it lives | Capability declaration | Example |
|---|---|---|---|
| **Tool** | `tools/<name>/` | `Manifest.Capability` | `tools/judge/` |
| **Adapter** | `adapters/<kind>/<name>/` | `manifest.yaml capabilities:` | `adapters/store/sqlite/` |
| **Research backend** | `adapters/research/<intent>/` | `manifest.yaml capabilities: [research.<intent>]` | `adapters/research/cve/` |
| **Constitution** | `constitutions/<name>.yaml` | `id` + `version` | `constitutions/default.yaml` |
| **Mod pack** | `extensions/<name>/` | `manifest.yaml risk_class:` | `extensions/redteam/` |

Every extension declares its capabilities via a manifest. The
engine **discovers** them at boot via `go:embed` over each
directory. There is **no central registration table**.

A manifest without an `Execute` function (for tools) or
implementation file (for adapters) is a **build error**. An
`Execute` function without a manifest is also a **build error**.

---

## 2. Review gate

A PR is mergeable when **all** the following are true:

- [ ] **Mutation score ≥ 80%** on touched packages (use
      `go-mutesting` with the project's
      [EQUIVALENT_MUTANTS.md](./docs/EQUIVALENT_MUTANTS.md)
      blacklist).
- [ ] **All 15 invariant regression tests pass** (`go test
      ./tests/invariants/...`).
- [ ] **All 7 Tier-1 security regression tests pass** (`go test
      ./tests/security/...`).
- [ ] **`drift_judge(spec_intent="evaluate this PR against
      ARCHITECTURE-V4.md")` returns `aligned`** with confidence
      ≥ 0.85.
- [ ] **Two approvals**:
      1. One from a maintainer of the touched package.
      2. One from a security auditor (required for any change
         touching `security/`, `governance/constitution/`,
         `constitutions/*.yaml`, or `tools/security/`).

The drift_judge step uses the artifact of the diff:

```bash
dark-mem judge \
  --spec_intent "evaluate this PR against ARCHITECTURE-V4.md" \
  --artifact_ref "file://path/to/diff.patch"
```

The PR description **must include** the output of `dark-mem
judge` showing the `aligned` verdict.

---

## 3. Adding a tool

### 3.1 When to add a tool vs an adapter

- **Tool**: a discrete MCP-callable operation with typed inputs
  and outputs. The orchestrator invokes it via `tools.call`.
- **Adapter**: a pluggable backend providing a capability
  (storage, LLM, credentials). The engine uses it internally;
  the orchestrator does not call it directly.

If your extension is callable from a workflow transition, it's a
**tool**. If it provides infrastructure the engine queries, it's
an **adapter**.

### 3.2 Step-by-step

```bash
# 1. Create the directory
mkdir -p tools/my_tool

# 2. Write the manifest
cat > tools/my_tool/manifest.yaml <<'EOF'
name: my_tool
version: 0.1.0
description: Does X with input Y, returns Z.
capability: my_domain.my_tool
requires: []
redact_args: true
redact_output: true
quota_hook: my_domain.my_tool
EOF

# 3. Implement
cat > tools/my_tool/tool.go <<'EOF'
// Package my_tool implements the my_tool MCP tool.
//
// See ARCHITECTURE-V4.md §2.3.2 for the Manifest pattern.
package my_tool

import "context"

type MyInput struct {
    Query string `json:"query" jsonschema:"required,minLength=1"`
    Limit int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=100,default=10"`
}

type MyOutput struct {
    Results []Result `json:"results"`
}

type Result struct {
    Title string `json:"title"`
    URL   string `json:"url"`
}

// Manifest is the declarative contract. Discovery is automatic.
// Adding this without updating the central registration table
// is the intended path.
var Manifest = ToolManifest{
    Name:         "my_tool",
    Description:  "Does X with input Y, returns Z.",
    Capability:   "my_domain.my_tool",
    Inputs:       MyInputSchema,
    Outputs:      MyOutputSchema,
    RedactArgs:   true,
    RedactOutput: true,
    QuotaHook:    "my_domain.my_tool",
}

// Execute implements the tool. The framework handles:
//   - JSON-Schema validation of inputs (DisallowUnknownFields)
//   - Redaction of args + output (INV-13)
//   - Quota tracking (SessionQuotaPool)
//   - Audit emission (INV-1)
func Execute(ctx context.Context, in MyInput) (MyOutput, error) {
    // ... your logic ...
    return MyOutput{Results: results}, nil
}
EOF

# 4. Write tests
cat > tools/my_tool/tool_test.go <<'EOF'
package my_tool

import (
    "context"
    "testing"
)

func TestExecute_BasicQuery(t *testing.T) {
    out, err := Execute(context.Background(), MyInput{Query: "test"})
    if err != nil {
        t.Fatalf("Execute: %v", err)
    }
    if len(out.Results) == 0 {
        t.Fatal("expected at least one result")
    }
}

func TestManifest_RequiredFields(t *testing.T) {
    if Manifest.Name == "" {
        t.Fatal("Manifest.Name required")
    }
    if Manifest.Capability == "" {
        t.Fatal("Manifest.Capability required")
    }
}
EOF

# 5. Run the review gate
go test ./tools/my_tool/...
go-mutesting -mutate ./tools/my_tool/...
dark-mem judge --spec_intent "evaluate my_tool against ARCHITECTURE-V4.md §2.3.2"

# 6. PR
git checkout -b feat/my_tool
git add tools/my_tool/
git commit -m "feat(tools): my_tool — does X with Y, returns Z"
git push origin feat/my_tool   # LOCAL ONLY policy: do not actually push
# Open PR with the drift_judge output in the description
```

### 3.3 Manifest schema reference

```yaml
name: string               # required, must match directory name
version: string            # required, semver
description: string        # required, ≤ 200 chars
capability: string         # required, dotted notation (e.g. "research.web")
requires: [string]         # optional, capability prerequisites
redact_args: bool          # default true (INV-13)
redact_output: bool        # default true (INV-13)
quota_hook: string         # optional, capability namespace for SessionQuotaPool
artifact_creating: bool    # default false; if true, drift_judge runs after every call
outputs_are_pii: bool      # default false; if true, dark_ssd_pii_detect runs before returning
rate_limit: string         # optional, e.g. "5/s" or "100/m"
```

---

## 4. Adding an adapter

### 4.1 Adapter kinds

| Kind | Directory | Capability prefix |
|---|---|---|
| Storage | `adapters/store/<name>/` | `storage.*` |
| LLM | `adapters/llm/<name>/` | `llm.*` |
| Credentials | `adapters/credentials/<name>/` | `credentials.*` |
| Audit | `adapters/audit/<name>/` | `audit.*` |
| Federation | `adapters/federation/<name>/` | `federation.*` |
| Embedding | `adapters/embedding/<name>/` | `embedding.*` |
| Update | `adapters/update/<name>/` | `update.*` |

### 4.2 Step-by-step (storage example)

```bash
# 1. Create the directory
mkdir -p adapters/store/mydb

# 2. Write the manifest
cat > adapters/store/mydb/manifest.yaml <<'EOF'
name: mydb-store
version: 1.0.0
capabilities:
  - storage.read
  - storage.write
  - storage.migrate
requires:
  - capability: storage.migrate
    version: ">=1.0"
config:
  dsn: ${DARK_DB:-dark.db}
  pragma.journal_mode: WAL
fallback_chain: []   # empty = no fallback; populated = try in order
EOF

# 3. Implement the Adapter interface
cat > adapters/store/mydb/adapter.go <<'EOF'
package mydb

import "context"

type Adapter struct{ /* connection pool, etc. */ }

func New(cfg Config) (*Adapter, error) { /* ... */ }

func (a *Adapter) Capabilities() []string {
    return []string{"storage.read", "storage.write", "storage.migrate"}
}

func (a *Adapter) Read(ctx context.Context, q Query) (Rows, error) {
    // INV-1: every write must emit write_audit
    // INV-7: filter by ActiveProject()
    // ...
}

func (a *Adapter) Write(ctx context.Context, w WriteOp) error {
    // INV-1 atomic audit
    // ...
}

// ... Migrate, etc.
EOF

# 4. Tests + mutation + drift_judge + PR
```

### 4.3 Adapter contract

Every adapter implements:

```go
type Adapter interface {
    Capabilities() []string
    // ... kind-specific methods ...
}
```

The engine queries `Capabilities()` at boot to populate the
capability registry. An adapter's manifest can declare more
capabilities than its Go code provides; the engine treats the Go
implementation as the source of truth and warns on mismatch.

---

## 5. Adding a research backend

Research backends are a specialized kind of adapter (kind =
`research`). They MUST follow the OSINT R5 tier-1 primary-source
gate (see [ARCHITECTURE-V4.md §3.3](./ARCHITECTURE-V4.md) and
[dark-copilot OSINT_PROTOCOL.md](./docs/OSINT_PROTOCOL.md)).

### 5.1 Step-by-step

```bash
# 1. Create the directory
mkdir -p adapters/research/my_intent

# 2. Write the manifest with R5 + security gates
cat > adapters/research/my_intent/manifest.yaml <<'EOF'
name: research-my_intent
version: 1.0.0
capabilities: [research.my_intent]
requires:
  - capability: http.client
config:
  primary: myprimary.example.com
  fallback_chain: [secondary.example.com]
  rate_limits:
    myprimary.example.com: "5/s"
    secondary.example.com: "1/s"
  timeout_ms: 30000
  requires_api_key: false
security:
  redact_output: true          # INV-13
  pii_gate: mandatory          # M5
  injection_scan: true         # INV-15
osint:                          # OSINT R5 gate
  primary_source_required: true
  source_tier: tier_1          # tier_1 | tier_2 | tier_3
EOF

# 3. Implement
cat > adapters/research/my_intent/adapter.go <<'EOF'
package my_intent

import (
    "context"
    "github.com/dark-memory-mcp/security/redact"
    "github.com/dark-memory-mcp/security/injection"
    "github.com/dark-memory-mcp/security/pii"
)

type Input struct {
    Query string `json:"query" jsonschema:"required,minLength=2"`
}

type Output struct {
    Results []Result `json:"results"`
}

func Execute(ctx context.Context, in Input) (Output, error) {
    // 1. INV-15: scan the query for injection
    if hit := injection.Scan(in.Query); hit != nil {
        return Output{}, ErrInjectionDetected(hit)
    }

    // 2. Call the backend
    raw, err := callPrimary(ctx, in.Query)
    if err != nil {
        // Try fallback_chain
        raw, err = callSecondary(ctx, in.Query)
        if err != nil {
            return Output{}, err
        }
    }

    // 3. M5: pii gate on raw output
    if piiHits := pii.Scan(raw); len(piiHits) > 0 {
        if piiHits.MaxSeverity() == "high" {
            return Output{}, ErrPIIDetected(piiHits)
        }
        raw = pii.Redact(raw, piiHits)
    }

    // 4. INV-13: redact before returning
    out := Output{Results: parse(raw)}
    return redact.Apply(out), nil
}
EOF

# 5.2 Required manifest fields for research adapters
```

| Field | Required | Why |
|---|---|---|
| `rate_limits` | Yes | Prevents provider ban from rate strikes |
| `security.redact_output: true` | Yes | INV-13 |
| `security.pii_gate: mandatory` | Yes | M5 |
| `security.injection_scan: true` | Yes | INV-15 |
| `osint.primary_source_required` | Yes for tier_1 claims | R5 |
| `osint.source_tier` | Yes | Audit provenance |

A research adapter without these fields is a **build error**.

---

## 6. Adding a constitution

A constitution is a YAML document that defines the operator's
governance policy. The default constitution ships with v4-alpha.1;
alternatives (e.g., `strict.yaml`, `research-only.yaml`) live
alongside it.

### 6.1 Step-by-step

```bash
# 1. Copy the default
cp constitutions/default.yaml constitutions/my-constitution.yaml

# 2. Edit the fields you need to change
# At minimum, change the `id` and bump `version`
vim constitutions/my-constitution.yaml

# 3. Verify
dark-mem constitution verify my-constitution
# Output: OK if all required INVs are present; FAIL otherwise

# 4. PR with description explaining the rationale
```

### 6.2 Required fields

| Field | Why |
|---|---|
| `id` | Unique identifier; pinned in audit |
| `version` | Semver; migration requires version bump |
| `invariants` | The list `[INV-1..INV-N]` the binary must support |
| `update.pubkey` | Ed25519 public key for binary signature verification |
| `tools.default_policy` | `deny` (default) or `allow` (not recommended) |

A constitution missing any required field fails the verify step.

### 6.3 When to add a new invariant

Adding a new invariant (e.g., INV-16) is a **breaking change**:

1. Open an issue with the threat model + proposed resolution.
2. Get approval from the constitution-maintainers team.
3. Implement the invariant in code (with defensive test).
4. Add the new INV to `default.yaml` `invariants: [INV-1..INV-16]`.
5. Old binaries refuse to load constitutions requiring INV-16.

---

## 7. Adding a mod pack

Mod packs are loaded at runtime from `extensions/<name>/`. They
are **scoped** by `risk_class` and **gated** by capabilities.

### 7.1 Risk classes

| `risk_class` | Review bar | Default gate |
|---|---|---|
| `research` | Standard | `capability:research` |
| `osint` | Standard | `capability:osint-research` |
| `exploit-development` | Elevated (2 security auditors) | `capability:redteam-research` + `DARK_REDTEAM=armed` |
| `active-probing` | Maximum (3 security auditors + ops sign-off) | `capability:redteam-probing` + `DARK_REDTEAM=armed` + operator consent per session |

### 7.2 Step-by-step

```bash
# 1. Create the directory
mkdir -p extensions/my_mod

# 2. Write the manifest
cat > extensions/my_mod/manifest.yaml <<'EOF'
name: my_mod
version: 0.1.0
risk_class: research         # one of research|osint|exploit-development|active-probing
description: Adds X capability
gated_by:
  - capability: research
files:
  - knowledge/my_knowledge.md
  - directives/my_directive.md
EOF

# 3. Add the content files
# INV-6 sanitization runs at load time; injection markers fail the load.

# 4. PR with risk_class + rationale
```

### 7.3 Mod loading protocol

At boot:

1. Engine reads all `extensions/*/manifest.yaml`.
2. For each, checks `risk_class` against current
   `DARK_REDTEAM` env var + operator's project capabilities.
3. If gated correctly: loads `files:` via `INV-6 sanitization`
   (regex against `injectionMarkers`).
4. If not gated: skipped; log warning.

A mod with injection markers (e.g., "IGNORE PREVIOUS
INSTRUCTIONS") **refuses to load** with `ErrModInjectionMarker`.

---

## 8. Modifying the workflow

The orchestrator (the agent) can issue `modify_workflow` events
at runtime. The engine validates, journals, and drift_judges the
modification itself.

### 8.1 From a tool

```bash
dark-mem vibe modify_workflow \
  --add_state "researching_osint" \
  --add_transition "{from: drafting_spec, event: needs_research, to: researching_osint, guard: HasSpecIntent}" \
  --rationale "Agent decided current drift_detected loop is too rigid; needs an OSINT research phase before re-attempting"
```

### 8.2 What happens

1. The engine validates: every new state is reachable, every new
   event has at least one transition, no orphans.
2. `Workflow.Version` increments.
3. Journal entry written (HMAC-chained).
4. `drift_judge(spec_intent="does this modification preserve INV-1..INV-15 for the current workflow")` runs.
5. If `aligned`: modification applied.
6. If `drift_detected`: modification journaled but reverted;
   operator notified.
7. If `needs_human`: halt; surface to operator.

### 8.3 What the orchestrator should NOT do

- **Do not** add transitions that bypass INV-3 (canary check),
  INV-13 (redaction), or INV-15 (injection scan). The
  drift_judge step rejects these.
- **Do not** remove states without ensuring no current workflow
  is in that state. The validator checks this.
- **Do not** silently merge with `default_workflow`. Every
  modification must have a `rationale`.

---

## 9. Security disclosures

### 9.1 Reporting a vulnerability

Email `security@opitacode.com` (PGP key on the website). Do not
open a public issue.

### 9.2 Response timeline

| Severity | Acknowledgement | Patch target |
|---|---|---|
| Critical (auth bypass, RCE, data loss) | 24 hours | 7 days |
| High (significant data exposure) | 48 hours | 30 days |
| Medium (limited impact) | 7 days | 90 days |
| Low (informational) | 30 days | next minor release |

### 9.3 Bounty program

Currently informal. Critical findings that lead to a CVE get
public credit + a small bounty (TBD). See website for current
terms.

---

## 10. Style guide

### 10.1 Go style

Follow `gofmt`, `go vet`, `golangci-lint` with the project's
`.golangci.yml`. Comments explain **why**, not **what**:

```go
// Bad: redundant with code
// Increment i by 1
i++

// Good: explains why
// Counter goes before the check to avoid a race when two
// goroutines call this concurrently.
i++
```

### 10.2 Documentation style

Follow [MANIFIESTO.md](./docs/MANIFIESTO.md):

- Spanish tuteado for user-facing docs (README, MANIFIESTO).
- English for technical/architecture docs (this file,
  ARCHITECTURE-V4.md).
- TL;DR at the top of every document.
- Tables for structured data, not prose.
- Cross-references as `[text](path.md#anchor)` not bare URLs.

### 10.3 Commit messages

Conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`,
`test:`). Subject line ≤ 72 chars. Body wraps at 72.

```
feat(tools): my_tool — does X with Y, returns Z

Implements the my_tool MCP tool following the Manifest pattern
from ARCHITECTURE-V4.md §2.3.2. Defensive tests cover:
- basic query returns results
- manifest has all required fields
- mutation score ≥ 80% on touched package

drift_judge: aligned conf=0.92
```

### 10.4 PR description template

```markdown
## What this PR does

One-paragraph summary.

## Why

Link to the issue, CRITIQUE-SOTA R-reco, or design discussion.

## How to verify

- [ ] `go test ./...` passes
- [ ] Mutation score ≥ 80% on touched packages
- [ ] drift_judge output: ... (paste verbatim)
- [ ] Two approvals: maintainer + security auditor (if applicable)

## Risk

What could break? What's the rollback plan?
```

---

## Appendix A. References

- [ARCHITECTURE-V4.md](./ARCHITECTURE-V4.md) — the canonical
  architecture spec for v4.0.
- [INVARIANTS.md](./docs/INVARIANTS.md) — INV-1..INV-10 (v3.0);
  v4 extends with INV-11..INV-15 (see ARCHITECTURE-V4.md §6.2).
- [MANIFIESTO.md](./docs/MANIFIESTO.md) — documentation tone.
- [EQUIVALENT_MUTANTS.md](./docs/EQUIVALENT_MUTANTS.md) —
  mutation testing blacklist.
- [CRITIQUE-SOTA.md](./docs/archive/v3.0-research/CRITIQUE-SOTA.md)
  §13 — 30 R-recos synthesis (source of v4 priorities).

---

> **Version**: v4.0.0-alpha.1
> **Branch**: `feat/v4-redesign`
> **Last reviewed**: 2026-09-24
> **Status**: review (pending constitution-maintainers approval)
