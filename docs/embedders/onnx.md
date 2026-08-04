# Bundled ONNX embedder (LLM-readable)

The default offline embedder for dark-memory. Backed by `Xenova/all-MiniLM-L6-v2 model_quantized.onnx` (int8 quantized, 22.97 MB) + libonnxruntime per-platform.

## When this rung activates

The bundled ONNX rung is selected by `FactoryAuto()` when:

1. No `DARK_MEMORY_EMBEDDER` manual override is set.
2. The harness detector does NOT identify a harness with a higher-preference rung (Voyage / OpenAI / Ollama).
3. The `OPENAI_API_KEY` env var is unset (otherwise the OpenAI rung wins).

It is also the rung operators explicitly select via `DARK_MEMORY_EMBEDDER=onnx`.

## Model details

- **Model**: `Xenova/all-MiniLM-L6-v2` (quantized int8)
- **Source**: `https://huggingface.co/Xenova/all-MiniLM-L6-v2/resolve/main/onnx/model_quantized.onnx`
- **SHA-256**: `afdb6f1a0e45b715d0bb9b11772f032c399babd23bfc31fed1c170afc848bdb1` (compile-time pinned; verified on extract)
- **Dimension**: 384
- **Max sequence length**: 256 word pieces
- **Tokenizer**: BERT WordPiece (lowercased, basic tokenization, greedy longest-match-first)
- **Output recipe**: mean-pool across the sequence with attention mask, then L2-normalize. Matches the sentence-transformers reference for `all-MiniLM-L6-v2`.

## libonnxruntime per-platform

PR-2.1 ships ONNX Runtime 1.22.0 via `//go:embed` with build tags:

| Platform | File | Size |
|---|---|---|
| `windows && amd64` | `bundled/windows-amd64/onnxruntime.dll` | 12.42 MB |
| `linux && amd64` | `bundled/linux-amd64/libonnxruntime.so.1.22.0` | 21.04 MB |
| `darwin && arm64` | `bundled/darwin-arm64/libonnxruntime.1.22.0.dylib` | 33.48 MB |

Unsupported platforms (linux/arm64, windows/arm64, freebsd, etc) compile to a stub that returns `embedder.ErrDisabled` at boot. Operators on unsupported platforms should set `OPENAI_API_KEY` to use the cloud rung.

## CGO build requirement

`yalue/onnxruntime_go` uses CGO to dlopen the bundled runtime. Build with `CGO_ENABLED=1`:

```bash
CGO_ENABLED=1 go build ./...
```

Local Windows builds need a C compiler; install MinGW (`winget install BrechtSanders.WinLibs.POSIX.UCRT`). CI (Ubuntu 22.04) ships `gcc` by default.

## Caching

First `dark_memory_embedder_setup_prompt` after install extracts the model + libonnxruntime to `$DARK_HOME/{models,libonnxruntime}/`:

```
$HOME/.dark-agents/models/onnx/model_quantized.onnx
$HOME/.dark-agents/libonnxruntime/windows-amd64/onnxruntime.dll
$HOME/.dark-agents/libonnxruntime/linux-amd64/libonnxruntime.so.1.22.0
$HOME/.dark-agents/libonnxruntime/darwin-arm64/libonnxruntime.1.22.0.dylib
```

SHA verification on extract + size check on subsequent boots. To force a re-extract, delete the corresponding cache subdir.

## When to use a different rung

The bundled ONNX adapter is intentionally minimal:

- **English-only**: basic tokenization drops most CJK / Arabic / Cyrillic accents. For multilingual embeddings, switch to Voyage-3 (`DARK_MEMORY_EMBEDDER=voyage`) or a custom endpoint.
- **Lower-dimensional**: 384d is the minimum for good-enough semantic search. OpenAI's 1536d captures finer-grained nuance but is slower + costs money.
- **No fine-tuning**: SBERT reference weights only. If you need domain-specific embeddings, train a custom model and override `Options.ExpectedSHA256` + `Options.MaxSeqLen`.

For these use cases, set `DARK_MEMORY_EMBEDDER` to one of:

- `openai` (cloud, 1536d, $0.02/1M tokens)
- `voyage` (cloud, 1024d, requires `VOYAGE_API_KEY`)
- `ollama` (local, 768d nomic-embed-text / 1024d mxbai-embed-large)
- `custom:<dotted-path>` (operator-pluggable, see `internal/embedder/embedder.go` FactoryOptions)
