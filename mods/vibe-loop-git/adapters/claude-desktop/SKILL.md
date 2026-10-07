# vibe-loop-git — claude-desktop adapter

## claude-desktop-specific invocation

claude-desktop is Anthropic's GUI app. It supports custom skills via
the `claude_desktop_config.json` file.

### Mount the mod

```json
{
  "skills": [
    {
      "path": "C:/Users/Nico/Documents/dark-memory-mcp/mods/vibe-loop-git/",
      "name": "vibe-loop-git",
      "autoLoad": true
    }
  ]
}
```

### Quick start

1. Restart claude-desktop after editing the config
2. In a chat, type: `/vibe-loop-git` or describe what you want to do
   using the protocol keywords
3. The skill will surface the 6-loop protocol and walk you through it

### claude-desktop env vars

claude-desktop runs in a sandbox without filesystem access to
`/c/Users/Nico/Documents/...`. For atomic-mirror, the mod provides
a clipboard-based fallback:

1. After each loop, copy the result to clipboard
2. Paste into a file at `C:/Users/Nico/Documents/dark-memory-mcp/atomic-mirrors/`
3. Manually run `dark-mem-cli agent_memory_save` from a terminal

OR, run dark-memory-mcp inside claude-desktop's MCP server config
(see Anthropic's MCP integration docs).

## See also

- `../SKILL.md` — operator-facing manifest
- `../mod.json` — formal manifest
