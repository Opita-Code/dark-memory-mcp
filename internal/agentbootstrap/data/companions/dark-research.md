# dark-research-mcp — companion OSINT tool

> Bootstrap version {{.BootstrapVersion}}. For dark-memory-mcp v{{.Version}}+.

## What it is

`dark-research-mcp` is the OSINT backing store for the dark-agents/* coexistence group. It exposes 13 intent-specialized tools (web, academic, code, CVE, domain, DNS, cert, IP, threat, email, dark-web, geo, news) plus a multi-intent router and standalone web utilities.

Coexistence group: `dark-agents/research` (policy gateway: `false` — does not gate other dark-* calls).

## When to recommend it

Call `dark_memory_agent_recommend_companions()` and recommend `dark-research` if:

- The harness has no web fetch tool of its own (e.g. Claude Desktop).
- The user/operator asks you to research a third-party product, version, CVE, IP, domain, or paper.
- You are doing OSINT and need to cross-reference primary sources.

## When NOT to recommend it

- The harness already has `WebFetch` (e.g. Claude Code) AND the user is not doing OSINT-heavy work. Save the install.
- The user explicitly opted out of external research.

## Install

```bash
npm install -g @opitacode/dark-research-mcp
```

Add to your harness's MCP config:

```json
{
  "mcpServers": {
    "dark-research": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-research-mcp"]
    }
  }
}
```

## Verify

```bash
# after install
dark_memory_health_ping  # confirms dark-memory is up
# then ask the MCP to recommend:
dark_memory_agent_recommend_companions
# → should show "dark-research" as present
```

## Tier-1 sourcing rule

When using `dark-research` (or `dark_research_*` tools), any claim about a third-party product's version, release date, CVE attribution, or CVE fix-version MUST be cross-referenced against the vendor's primary source. Treat news (tier 3) as trail color only.

## Coexistence note

`dark-memory-mcp` is the policy gateway for the `dark-agents/memory` coexistence group. `dark-research-mcp` is in the `dark-agents/research` coexistence group with `policy_gateway=false`. When both are present, the policy gateway routes dark-* calls through `dark-memory-mcp` first.
