# dark-copilot-mcp — companion real-browser tool

> Bootstrap version 1. For dark-memory-mcp v2.6.0+.

## What it is

`dark-copilot-mcp` exposes a real Chromium browser to MCP-native harnesses via 18 tools: navigate, click, type, scroll, snapshot_dom, execute_js, screenshot, screenshot_element, network_list, console_get, storage_get, sources_list/get, modify_dom, manipulate_storage, override_geo, inject_script, raw_event.

It uses lazy-launch + LIFO shutdown: Chrome boots on the first tool call that needs it, and exits when the session closes.

## When to recommend it

Call `dark_memory_agent_recommend_companions()` and recommend `dark-copilot` if:

- The user needs to interact with a JS-gated page (SPA, dashboard, captcha, login flow).
- The harness has no real browser (Claude Desktop, Claude Code's WebFetch is read-only).
- You need to bypass CAPTCHAs, click buttons, fill forms, or execute JS.
- You need network-level observability (network_list, console_get) for a page.

## When NOT to recommend it

- The user only needs to read public static pages. WebFetch is enough.
- The target site bans headless browsers via anti-bot measures (consider `dark-research` as fallback).
- The user explicitly opted out of browser automation.

## Install

```bash
npm install -g @opitacode/dark-copilot-mcp
```

Add to your harness's MCP config:

```json
{
  "mcpServers": {
    "dark-copilot": {
      "command": "npx",
      "args": ["-y", "@opitacode/dark-copilot-mcp"]
    }
  }
}
```

## Verify

```bash
# after install
dark_memory_agent_recommend_companions
# → should show "dark-copilot" as present

# then test:
mcp__dark-copilot__navigate("https://example.com")
mcp__dark-copilot__snapshot_dom(selector="h1", max_bytes=2000)
```

## R5 — OSINT primary-source gate

Any browser-scraped claim about a third-party product's version/release/CVE MUST be cross-referenced against the vendor's primary source before promotion to P0/P1. Tier 1 sources: `chromereleases.googleblog.com`, `nvd.nist.gov`, `cisa.gov`, `cyber.gc.ca`, vendor domains. Tier 3 (news): trail color only.

## R6 — Vibe-flow gate for browser output

A dark-copilot tool result is never returned to the operator directly — pipe through `spec_create` → `tool_call` → `artifact_log` → `drift_judge` → `drift_log` → `persist`. Exception: pure read-only sanity checks may return directly.

## Coexistence note

`dark-copilot-mcp` is in a separate coexistence group (browser-automation). It does NOT route through `dark-memory-mcp`'s policy gateway — it's a peer tool, not a managed one.
