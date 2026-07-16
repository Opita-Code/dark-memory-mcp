# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| v1.0.x | ✅ Active |
| < v1.0  | ❌ Not supported |

We follow [SemVer](https://semver.org/). Security fixes land in the latest
minor release and are backported to the previous major for 12 months.

## Reporting a vulnerability

**Please do NOT open a public issue for security vulnerabilities.**

Use one of these private channels:

1. **GitHub Security Advisories** (preferred): open a
   [private security advisory](https://github.com/Opita-Code/dark-memory-mcp/security/advisories/new)
   on this repository.
2. **Email**: [security@opitacode.com](mailto:security@opitacode.com). Include
   `dark-memory-mcp` in the subject.

You should receive an acknowledgment within **72 hours**. We aim to triage
within **5 business days** and ship a fix or mitigation within **30 days** for
high-severity issues.

## What to include

A good vulnerability report includes:

- **Description** — what the vulnerability is and what an attacker can do
- **Reproduction** — minimal steps to reproduce (commands, code snippets, or a
  proof-of-concept)
- **Affected versions** — which versions of dark-memory-mcp are vulnerable
- **Environment** — OS, Go version, database driver (SQLite/Postgres), driver
  version
- **Impact assessment** — your view of severity (critical/high/medium/low)
- **Suggested fix** — if you have one, great; we will credit you

## Security model

The 6 operational invariants in our constitution are the security foundation:

| INV | Threat mitigated |
|---|---|
| INV-1 | Silent writes (no audit trail of mutations) |
| INV-2 | Cross-session contamination |
| INV-3 | Prompt injection via canary-leaked payload |
| INV-4 | Constitution drift (rogue config affecting all writes) |
| INV-5 | Cache tampering |
| INV-6 | Prompt injection via mod content |
| INV-7 | Cross-tenant data access |

The full INV-* table is in [`docs/INVARIANTS.md`](docs/INVARIANTS.md). Each
invariant has at least one defensive test in `tests/invariants/` or exercised
via `tests/dual_driver/` contract tests.

## Threat model scope

### In scope

- The 25 `dark_memory_*` MCP tools and their handlers
- The SQLite and Postgres store implementations
- The 9 orchestrators
- The 8 context projections
- The 6 INV-* enforcement points
- The CLI binary (`dark-mem-cli`) — flag parsing, config file handling
- The inspect binary (`dark-mem-inspect`) — read-only diagnostic
- The MCP server stdio transport
- Schema migrations

### Out of scope

- The `dark-research-mcp` sibling (separate repo, separate security policy)
- The `dark-scrapper` daemon (separate repo, separate security policy)
- Third-party LLM providers (we are a storage layer; LLM behavior is upstream)
- The operator's `dark.db` file once it leaves the boundary of dark-memory-mcp
- The Go module proxy / public Go ecosystem

## Hall of fame

We thank the following security researchers for responsible disclosure:

*(none yet — be the first)*

## Coordinated disclosure timeline

```
Day 0   You file a private report
Day 1-3 We acknowledge + assign severity
Day 4-7 Triage + reproduce + propose fix
Day 8-30 Develop + review + ship patch
Day 30+ Public CVE assignment (if applicable) + disclosure
```

We follow the [GCP disclosure guidelines](https://googleprojectzero.blogspot.com/2021/04/policy-and-disclosure-2021-edition.html)
as a reference.

## Bug bounty

We do not currently run a paid bug bounty program. Valid reports earn:

- Public credit in this file's Hall of Fame
- Acknowledgment in the next release notes
- Our genuine thanks

If you need a paid engagement, contact [security@opitacode.com](mailto:security@opitacode.com)
to discuss terms.