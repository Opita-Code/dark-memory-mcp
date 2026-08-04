# Voyage AI embedder (LLM-readable)

The preferred rung for Claude Code harness detection (per row 164 §2 + row 163 amendment). Backed by Voyage AI's `voyage-3` model (1024d, Anthropic's recommended embedding partner).

## When this rung activates

`FactoryAuto()` selects the Voyage rung when:

1. No `DARK_MEMORY_EMBEDDER=...` manual override is set.
2. `internal/embedder/detect` identifies the host as `claude-code` (via `CLAUDE_CODE` env var or `~/.claude/settings.json`).
3. `VOYAGE_API_KEY` is set in the harness's process env.

If `VOYAGE_API_KEY` is unset, the ladder falls through to the next rung (bundled ONNX).

Operators can force this rung via `DARK_MEMORY_EMBEDDER=voyage`.

## API key setup

Get a key from https://www.voyageai.com/ (Anthropic's recommended embedder partner; same dashboard as their docs). Set the key in the harness's MCP env block:

```jsonc
// opencode.jsonc
{
  "mcp": {
    "dark-memory": {
      "command": "dark-mem-mcp",
      "environment": {
        "VOYAGE_API_KEY": "pa-…"
      }
    }
  }
}
```

**Do NOT paste the key in chat** (per row 164 §1 hard rule). Always set it in the harness's settings.

## Model details

- **Default model**: `voyage-3` (1024d)
- **Endpoint**: `https://api.voyageai.com/v1/embeddings` (override via `DARK_MEMORY_VOYAGE_BASE`)
- **Auth**: `Authorization: Bearer <VOYAGE_API_KEY>`
- **Latency**: ~50-200ms per batch of 100 inputs
- **Cost**: ~$0.06 per 1M tokens (cheaper than OpenAI for comparable quality)

## Other Voyage models (override via `DARK_MEMORY_VOYAGE_MODEL`)

| Model | Dim | Use case |
|---|---|---|
| `voyage-3` | 1024 | Default; English + multilingual, retrieval-optimized |
| `voyage-3-lite` | 512 | Cheaper, faster, slightly lower quality |
| `voyage-code-3` | 1024 | Code-aware retrieval (better for code search) |
| `voyage-large-2` | 1536 | Older but higher-dim; not recommended for new deploys |

## Retry policy

Per row 160 cross-cutting:

- 5s connect timeout, 10s read timeout.
- One retry on `5xx` and `429`; no retry on other `4xx`.
- Exponential backoff: 250ms, 750ms, capped at 3s.

The adapter does NOT retry on context cancellation (the caller sees `ctx.Err()` immediately).

## Fail-safe

On a `401` from Voyage (key invalid / revoked), the adapter returns `embedder.ErrKeyMissing`. The factory ladder logs the failure (not the response body) and falls through to the next rung (bundled ONNX). The search path on subsequent calls re-tries Voyage in case the operator rotated the key.

On any other `4xx`, the adapter returns the error to the caller. The search path surfaces this via the response so the operator sees the failure.

## Privacy

`voyage-3` accepts the user's text input and returns embeddings. Voyage AI stores request/response logs for 30 days per their TOS (https://docs.voyageai.com/docs/privacy-policy). For operators with stricter privacy requirements, use the bundled ONNX rung.
