# vibe-loop-git — claude-code adapter

## claude-code-specific invocation

claude-code is Anthropic's CLI for Claude. It can load this mod if
you mount the `mods/vibe-loop-git/` directory as a skills source.

### Mount the mod

```bash
# Add to your claude-code config (~/.config/claude-code/skills/ or
# via the `claude-code skills add` command):
claude-code skills add /c/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git/
```

### Quick start

```bash
# Start claude-code
claude-code --project <your-project>

# In the chat:
/skill vibe-loop-git

# Or auto-trigger via the description keywords (vibe-loop, drift-judge,
# atomic mirror, etc.)
```

### claude-code env vars

claude-code does NOT have dark-memory-mcp auto-loaded (no MCP bridge
yet). You can:

1. **Use claude-code's own memory** (`/memory` command) for cross-session recall
2. **Use the mod for the protocol** but skip the atomic-mirror step
3. **Run drift_judge via the dark-memory CLI** (see `dark-mem-cli --help`)

### Recommended pattern

```bash
# In claude-code:
/skill vibe-loop-git
# Follow the 6-loop protocol
# At the atomic-mirror step, save to claude-code memory OR run:
/bash dark-mem-cli agent_memory_save --kind=decision --title="..."
```

This way you get the protocol without requiring dark-memory-mcp to be
loaded in claude-code.

## See also

- `../SKILL.md` — operator-facing manifest
- `../mod.json` — formal manifest
- `../../adapters/opencode/SKILL.md` — opencode equivalent (has dark-memory auto)
