# Vendoring guide: redteam mod fixtures for internal/tools tests

The redteam namespace tests in `internal/tools/redteam_test.go`
(`TestRegisterRedTeam_*`, `TestScanRedTeamMods_*`, `TestRedteam*`)
need on-disk mod fixtures (a `mod.toml` plus `knowledge/*.md` or
`knowledge/*.jsonl`) to exercise the arming, scanner, and handler
paths.

## Where the canonical mods live

The canonical mods are **not** vendored into this repo. They live in
the peer project:

- `dark-research-mcp/mods-examples/red-team-jailbreak-arsenal/`
- `dark-research-mcp/mods-examples/osint-cve-deepdive/`

These use the dark-research naming convention (`user/<mod-id>`).
dark-memory's redteam namespace now aligns with this convention. The
two expected mods are:

- `user/red-team-jailbreak-arsenal`
- `user/osint-cve-deepdive`

## Where dark-memory's redteam tests look for them

The resolution order (defined in `redteamModsAbsPath`):

1. `$DARK_REDTEAM_MODS_PATH` — operator override. If set, used as-is.
2. `<repo>/mods/redteam/` — default fallback.

If neither is available, the redteam tests skip (not fail) on
modern main (since PR #8). Vendoring is optional and only needed
for local development.

## Vendoring instructions

To run the redteam tests locally, point at the dark-research-mcp
sibling via:

```
DARK_REDTEAM_MODS_PATH=C:\Users\Nico\dark-research-mcp\mods-examples
```

The two mods under that path are:

- `user/red-team-jailbreak-arsenal/` — risk_class=research-only,
  knowledge keys: `jailbreak_techniques`, `refusal_taxonomy`.
- `user/osint-cve-deepdive/` — risk_class=research-only.

Both mods have markdown knowledge files (not JSONL payloads). The
`redteam_get_prompts` handler in dark-memory-mcp expects
`prompt_injection` knowledge kind; these research-only mods do not
provide it. Tests that depend on JSONL prompt payloads will skip
cleanly when the mod lacks `prompt_injection` knowledge.

## Suggested layout (no longer needed)

The tests now point at the dark-research-mcp sibling directly via
`DARK_REDTEAM_MODS_PATH`. No local vendoring required.

```
C:\Users\Nico\dark-research-mcp\mods-examples\
  red-team-jailbreak-arsenal\
    mod.toml
    directives\
    knowledge\
      jailbreak_techniques.md
      refusal_taxonomy.md
  osint-cve-deepdive\
    mod.toml
    ...
```

## Why fixtures aren't vendored in-repo

The redteam namespace is **armed-mode only** (`DARK_REDTEAM=armed`)
and ships payloads that are by definition adversarial. Vendoring
them in the public dark-memory-mcp repo would:

1. Mix operator-mode and redteam-mode artifacts in a single tree.
2. Force every contributor / CI runner to download adversarial
   payloads even if they don't run the redteam tools.
3. Bypass the "armed gate" social contract — the redteam mods
   should be an explicit operator decision, not a default.

So: not vendored by default. Operator opt-in via the env var or
explicit directory layout.

## Tests that always run (no fixtures needed)

- `TestRegisterRedTeam_RefusesWhenNotArmed` — exercises the gate,
  not the mods.
- `TestRedteamLogAttemptHandler_*` — exercises the audit-writer,
  not the mod scanner.

These tests pass unconditionally on CI.

## Tests that require fixtures (skip if absent)

- `TestScanRedTeamMods_ReturnsOurTwoMods` — expects
  `user/red-team-jailbreak-arsenal` and `user/osint-cve-deepdive`.
- `TestRedteamListModsHandler_Success` — expects count >= 2.
- `TestRedteamGetPromptsHandler_*` — skip when the mod has no
  `prompt_injection` knowledge (research-only mods).
- `TestLoader_*` — use temporary fixtures or the real mods.

