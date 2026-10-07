# vibe-loop-git — codex adapter

## codex-specific invocation

OpenAI Codex is OpenAI's coding agent. It supports custom skills via
the `~/.codex/skills/` directory.

### Mount the mod

```bash
# Symlink or copy the mod into codex's skills directory
ln -s /c/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git/ \
      ~/.codex/skills/vibe-loop-git
```

### Quick start

1. Restart codex
2. In a chat, type: `use the vibe-loop-git skill` or describe a
   multi-step task that needs drift-judging

### codex env vars

codex uses OpenAI's API directly. For drift_judge, you can:

1. **Use codex's own evaluation** (model evaluates its own work)
2. **Cross-check against a different model** (GPT-4 vs codex-judge)
3. **Skip drift_judge** and rely on codex's self-consistency

For dark-memory atomic-mirror, run `dark-mem-cli` from a terminal
between codex sessions.

### Note on vibe_cases

codex is best for C1 (code) and partially C2 (text). For C3 (decision),
C4 (research), C5 (video), C6 (audio), C7 (multi), you may need
multiple agents or a different tool.

## See also

- `../SKILL.md` — operator-facing manifest
- `../mod.json` — formal manifest
