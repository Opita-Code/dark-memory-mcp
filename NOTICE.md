# NOTICE — License coexistence in dark-memory-mcp

**Status**: ACTIVE 2026-10-14 (per atomic mirror pending — Sesión 6)
**Scope**: Repository `github.com/Opita-Code/dark-memory-mcp`

This repository is **dual-licensed by directory**. Files in different
paths may be governed by different licenses. The summary below is
authoritative for the top-level structure; specific subdirectories may
carry their own LICENSE files that take precedence.

## License map (top-level)

| Path | License | SPDX | Notes |
|---|---|---|---|
| `./LICENSE` (parent repo, root files) | MIT | `MIT` | Default license |
| `internal/`, `cmd/`, `tests/`, `bench/`, `docs/`, `core/` (Go code) | MIT | `MIT` | Inherited from parent repo |
| `mods/vibe-loop-git/` | CC BY-NC 4.0 | `CC-BY-NC-4.0` | **Dual-licensing exception** (operator decision 2026-10-14). Non-commercial use only. See `mods/vibe-loop-git/LICENSE`. |
| `mods/dark-research/` (if present) | MIT | `MIT` | Inherited from parent repo unless its own LICENSE declares otherwise |
| `mods/dark-copilot/` (if present) | MIT | `MIT` | Inherited from parent repo unless its own LICENSE declares otherwise |

## Why dual-licensing?

The companion mod `vibe-loop-git` is a protocol specification, skill
bundle, and protocol artifact rather than a runtime dependency. The
operator chose to release the mod itself under CC BY-NC 4.0 to:

1. Allow free study, modification, and non-commercial distribution.
2. Reserve commercial licensing as a separate decision point.
3. Keep the parent repository's MIT license (MIT) intact for the
   runtime engine that actually executes the protocol.

This split is common for protocol specifications that ship as
companion mods to a runtime — the spec travels under terms appropriate
for a spec; the runtime travels under terms appropriate for code.

## How to verify the license of a specific file

1. Look for an SPDX header at the top of the file:
   - `<!-- SPDX-License-Identifier: ... -->` (Markdown)
   - `# SPDX-License-Identifier: ...` (shell, scripts)
   - `// SPDX-License-Identifier: ...` (Go source)
   - A `"license"` JSON field at the root (JSON manifests)
2. If no SPDX header is present, the file inherits the license of
   its containing directory.
3. If a `LICENSE` file is present in the containing directory, that
   file is authoritative.
4. Otherwise, the file inherits from `./LICENSE` at the repo root.

## Software vs. cultural-works caveat

CC BY-NC 4.0 was designed for cultural works (text, images, audio,
video). It is used here per the operator's explicit decision. For
software, **PolyForm Noncommercial License 1.0.0** is the more conventional
choice because it explicitly enumerates permitted non-commercial
uses. Consumers of `mods/vibe-loop-git/` who require a software-tailored
non-commercial license should contact the copyright holder for a
separate grant.

## References

- `LICENSE` (MIT, parent repo) — this NOTICE.md sits alongside it
- `mods/vibe-loop-git/LICENSE` (CC BY-NC 4.0) — full terms
- `mods/vibe-loop-git/mod.json` — `license: "CC-BY-NC-4.0"` field at root
- Atomic mirror (cuaderno) — pending, will reference this NOTICE.md

## Changelog

- **2026-10-14**: Initial NOTICE.md created. Dual-licensing exception
  for `mods/vibe-loop-git/` introduced. CC BY-NC 4.0 selected over
  PolyForm Noncommercial 1.0.0 per operator explicit decision.