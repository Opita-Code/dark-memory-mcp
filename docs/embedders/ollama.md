# Ollama embedder (LLM-readable)

The preferred rung when the operator is running Ollama locally (any harness, no cloud egress). Backed by Ollama's `/api/embeddings` endpoint with the `nomic-embed-text` model (768d).

## When this rung activates

`FactoryAuto()` selects the Ollama rung when:

1. No `DARK_MEMORY_EMBEDDER=...` manual override is set.
2. `internal/embedder/detect` identifies an Ollama daemon: either via the `OLLAMA_HOST` env var, or via a 500ms TCP probe of `127.0.0.1:11434`.
3. The Ollama daemon responds to `/api/embeddings` calls.

If the daemon is unreachable at first `.Embed()` call, the adapter returns an error and the search path falls back to BM25-only for that batch.

Operators can force this rung via `DARK_MEMORY_EMBEDDER=ollama`.

## Setup

```bash
# Install Ollama (https://ollama.com/download).
# Pull an embedding model:
ollama pull nomic-embed-text
# Verify the daemon is reachable:
curl http://127.0.0.1:11434/api/tags
```

No API key required. If the operator's Ollama daemon binds to a non-default port or remote machine, set `OLLAMA_HOST`:

```bash
export OLLAMA_HOST=http://192.168.1.50:11434
```

## Model details

- **Default model**: `nomic-embed-text` (768d, Apache 2.0)
- **Endpoint**: `http://127.0.0.1:11434/api/embeddings` (override via `OLLAMA_HOST` or `DARK_MEMORY_OLLAMA_BASE`)
- **No auth**: Ollama is local; the adapter does NOT read or send any API key.
- **Latency**: ~5-30ms per text (depends on hardware; CPU is fine for low-volume use, GPU recommended for >10 embeddings/sec).

## Other Ollama models (override via `DARK_MEMORY_OLLAMA_MODEL`)

| Model | Dim | Notes |
|---|---|---|
| `nomic-embed-text` | 768 | Default; Apache 2.0, multilingual |
| `mxbai-embed-large` | 1024 | Higher quality, slower |
| `snowflake-arctic-embed` | 1024 | Multilingual, Apache 2.0 |
| `all-minilm` | 384 | Smaller; matches the bundled ONNX dim |

## Parallelism

Ollama's `/api/embeddings` accepts one text per call. The adapter uses a bounded goroutine pool (parallelism=4) to send concurrent requests for batch inputs. The pool size is a constant in `internal/embedder/ollama/ollama.go`; increase it if your Ollama host has multiple GPUs / cores and your batch sizes are >10.

## Fail-safe

The adapter does NOT pre-flight a TCP probe at construction. It assumes `detect.probeOllama` already verified the daemon. If the daemon is unreachable on first `.Embed()` call:

- Single request fails fast (5s connect timeout per request).
- Batched requests: 1 retry on 5xx + 429, then fall back to BM25-only for the batch.

The factory ladder does NOT pre-arm the bundled ONNX fallback if Ollama detection succeeded — operators wanting a hot fallback should set `OPENAI_API_KEY` or `VOYAGE_API_KEY` alongside `OLLAMA_HOST`.

## Privacy

Ollama is fully local. No network egress. The adapter sends the user's text to the configured Ollama endpoint (default localhost). Ollama may log requests on disk if its logging is enabled (operator's responsibility to disable).
