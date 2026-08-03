# Embedder install guide (LLM-readable)

This document is intended for **LLM agents installing dark-memory** (per row 164 §4). Humans can skim it; the structure follows the install ladder so the LLM can self-configure without operator hand-holding.

## TL;DR for the LLM

1. **Detect harness** (which tool is the user using: OpenCode / Claude Code / Codex / Cline / etc).
2. **Read operator config**: which provider + model is the harness configured with.
3. **Install dark-memory** via the harness's standard MCP install path.
4. **Health check**: invoke `health_ping` and report the active embedder provider to the user.
5. If no provider detected → invoke `dark_memory_embedder_setup_prompt` and surface the consent question verbatim.
6. If consent answer is `bundled_onnx` → done; dark-memory will activate the bundled ONNX adapter on first search.

## Install ladder

### Step 1 — Detect harness

```bash
# On the user's machine, look at the env vars + config dirs:
echo "HOME=$HOME"
echo "XDG_CONFIG_HOME=$XDG_CONFIG_HOME"
echo "OPENCODE_VERSION=$OPENCODE_VERSION"
echo "CLAUDE_CODE=$CLAUDE_CODE"
echo "CODEX_HOME=$CODEX_HOME"
echo "OLLAMA_HOST=$OLLAMA_HOST"
ls -la "$HOME/.claude" "$HOME/.opencode" "$HOME/.codex" "$HOME/.config/opencode" "$HOME/.config/claude" 2>/dev/null
```

`dark-memory`'s `internal/embedder/detect` package runs the same probe ladder at boot. Mapping:

| Harness | Detection signal | Preferred embedder |
|---|---|---|
| Claude Code | `CLAUDE_CODE=1` or `~/.claude/settings.json` | Voyage AI voyage-3 |
| OpenCode | `OPENCODE_VERSION=…` or `~/.config/opencode/config.toml` | OpenAI text-embedding-3-small |
| Codex | `CODEX_HOME=…` or `~/.codex/config.toml` | OpenAI text-embedding-3-small |
| Ollama (any) | `OLLAMA_HOST=http://…:11434` or `127.0.0.1:11434` reachable | Ollama nomic-embed-text |
| None | none of the above | bundled ONNX (default offline) |

### Step 2 — Read operator config

For each harness, the config file tells you the user's LLM provider. dark-memory's `internal/embedder/detect` does NOT parse provider configs in PR-2.1; that's deferred. For now, rely on the env-var presence:

| Provider | Env var | dark-memory uses |
|---|---|---|
| OpenAI | `OPENAI_API_KEY=sk-…` | `text-embedding-3-small` (1536d) |
| Anthropic (no native embeddings; partner) | `VOYAGE_API_KEY=pa-…` | `voyage-3` (1024d) |
| Ollama (local) | `OLLAMA_HOST=http://…` | `nomic-embed-text` (768d) |
| None of the above | (unset) | bundled ONNX `all-MiniLM-L6-v2` (384d, ~22 MB, offline) |

### Step 3 — Install dark-memory

Use the harness's MCP install path. For OpenCode it's typically:

```bash
# Add to the user's opencode.jsonc:
{
  "mcp": {
    "dark-memory": {
      "command": "dark-mem-mcp",
      "args": [],
      "environment": {}
    }
  }
}
```

For Claude Code, the equivalent path is `~/.config/claude/settings.json` MCP section. For Codex, `~/.codex/config.toml` MCP section.

### Step 4 — Health check

After install, restart the harness. Then call:

```
dark_memory_health_ping
```

Read the response's `embedder_kind` and `embedder_dim`. Report to the user in plain language:

> dark-memory is running with the **Voyage AI** embedder (1024 dimensions). First search will fetch embeddings from Voyage.

If `embedder_kind=none`: dark-memory is in BM25-only mode (PR-1 baseline). That's safe; vector search is off but BM25 + Porter is still operational.

### Step 5 — Consent (when no provider detected)

If `health_ping` shows `embedder_kind=none` AND `OPENAI_API_KEY` is unset, call:

```
dark_memory_embedder_setup_prompt
```

Surface the `Prompt` field verbatim to the user. The user picks one of:

- `api_key`: paste their OpenAI/Anthropic/Voyage API key in the harness's settings (NOT in chat — row 164 §1 hard rule).
- `bundled_onnx`: dark-memory uses the bundled local ONNX model (offline, ~22 MB).
- `skip_embeddings`: BM25 + Porter (PR-1) only.

The LLM should highlight the recommended choice (flagged via the `recommended: true` field on the choice) without violating the "surface verbatim" rule.

### Step 6 — If user picked `bundled_onnx`

Nothing more to do. The bundled ONNX model + libonnxruntime are embedded in the binary. dark-memory extracts them to `$DARK_HOME/{models,libonnxruntime}/` on first use (idempotent; SHA-pinned).

If the user picked `api_key`: tell them to set the appropriate env var in their harness's settings, then restart. The factory ladder will pick it up automatically.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `embedder_kind=none` after install | No API key + no Ollama + harness detector missed | Run `dark_memory_embedder_setup_prompt` |
| `embedder=openai` but `Embed()` returns `ErrKeyMissing` | `OPENAI_API_KEY` not in MCP process env | Inject the key in `opencode.jsonc` `environment` block |
| `embedder=onnx` returns `ErrDisabled` on darwin/amd64 | We ship `darwin-arm64` only (no `darwin-amd64` libonnxruntime bundle yet) | Set `OPENAI_API_KEY` to use the OpenAI rung, or build from source with `-tags dark_onnx_darwin_amd64` |
| `Embed()` returns slow inference | CPU-only ONNX, no CUDA | Normal; ~10ms/text on a modern CPU. Set `OPENAI_API_KEY` for cloud-routed embeddings |

## Bundle size + binary impact

PR-2.1 adds:

- `model_quantized.onnx` (~22.97 MB, always embedded)
- `libonnxruntime` for the build target platform (~12-33 MB per platform)

Per-binary increase: ~47 MB. CI defaults to embedding all three platforms via `//go:embed` + build tags; release builds pick the platform-specific bundle.

## Privacy posture

The bundled ONNX adapter does **no network I/O**. Inference is local. The OpenAI / Voyage / Ollama adapters only contact their respective API endpoints with the user's text input; dark-memory never logs the input text (per `internal/embedder/embedder.go` godoc + `internal/embedder/onnx/onnx.go` trust boundary notes).
