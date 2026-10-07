# vibe-loop-git — opencode adapter

## opencode-specific invocation

opencode is the canonical harness for dark-memory-mcp. It loads
`mods/vibe-loop-git/SKILL.md` automatically when the mod is enabled.

### Quick start

```bash
# Start a session
dark_memory_session_start(operator="nico", project_id="<project>")

# Invoke the mod
skill(name="vibe-loop-git")
```

The skill loader reads `SKILL.md` and surfaces the protocol phases.
Then you (operator) choose a loop, choose a vibe_case, and execute.

### opencode-specific env vars (no setup needed)

These are already in your `~/.config/opencode/opencode.jsonc`:

| Env var | Why |
|---|---|
| `DARK_DB` | Where dark-memory stores its SQLite DB |
| `DARK_HOME` | Where dark-memory stores its config.toml |
| `DARK_CONSTITUTION_ID` | Which constitution to load |
| `DARK_AGENT_BOOTSTRAP_DIR` | Where to load SYSTEM_PROMPT.md content |
| `MINIMAX_API_KEY` | Your hardcoded LLM API key (live, 126 chars) |

### Local-only enforcement

opencode is configured for `feat/v4-redesign` branch. The mod's
discipline: **never `git push`, never create remote tags**. Tags are
local (`v4.0.0-alpha.28` etc.) and only exist in your local clone.

If you accidentally try to push, the mod's `tests/audit-trail-verify.sh`
will detect the anomaly and surface it as `needs_human`.

### Verifying the mod is loaded

```bash
# In opencode chat:
echo $DARK_MEMORY_VERSION  # should be alpha.28+
# Or check the opencode.jsonc mcp.dark-memory section
# The supervisor should be running: tasklist | grep dark-mem-mcp
```

If the supervisor died (Path B), restart opencode. The mod is
filesystem-only and survives the restart.

## See also

- `../SKILL.md` — operator-facing manifest
- `../mod.json` — formal manifest
- `../core/` — 6 loop artifacts + T-407-c design
