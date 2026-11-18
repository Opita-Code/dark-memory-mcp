// Package adapters registers the embedder adapters in this binary.
//
// # Why this package exists
//
// Each adapter (internal/embedder/onnx, .../openai, .../voyage,
// .../ollama) registers itself with embedder.RegisterAdapter in its own
// init() function. That init() only runs if the package is IMPORTED.
// Nothing imported them: the facade (internal/embedder) was imported,
// the adapters were not.
//
// The consequence was measured, not guessed. With no adapter compiled
// in, tryKind() could never find a factory, whatever
// DARK_MEMORY_EMBEDDER said, so the server ALWAYS booted with the
// disabled stub (embedder.None(), kind="none", dim=0) and the hybrid
// retrieval axis (Mode=vector|rrf) was dead code in every deployment.
//
// This package is the single place that imports them, so the wiring is
// visible in one file instead of being an accident of what some other
// package happened to import.
//
// # Which adapters are registered, and why not ONNX
//
// The three that build without cgo:
//
//   - ollama: local, offline, no API key. Needs a reachable Ollama
//     daemon (OLLAMA_HOST or 127.0.0.1:11434).
//   - openai: needs OPENAI_API_KEY in the keyring/env.
//   - voyage: needs VOYAGE_API_KEY.
//
// The ONNX adapter is NOT registered here, and that is a deliberate
// decision with a measured reason: it needs cgo
// (github.com/yalue/onnxruntime_go) and therefore a C toolchain.
// Importing it unconditionally breaks the build on every machine
// without a C compiler — including the CI runners and the release
// builders, which have none. MEASURED on 2026-11-18: the parent
// module's own build fails with "build constraints exclude all Go
// files in github.com/yalue/onnxruntime_go" for exactly this reason.
//
// It is NOT behind a build tag either. A build tag would need the
// dependency added to cmd/dark-mem-mcp's go.mod and go.sum (the
// sibling module has its own), and the resulting binary could not be
// built or verified on any machine in this project's CI — which would
// ship a path nobody has ever compiled. So the local ONNX adapter
// stays unregistered, and an operator who wants it adds the import
// themselves and owns that build.
//
// # How an adapter gets selected
//
// The boot ladder (embedder.FactoryAuto) tries, in order:
//
//  1. the DARK_MEMORY_EMBEDDER override (now including "ollama"),
//  2. the harness detector's preference,
//  3. the bundled ONNX adapter (only if something registers it),
//  4. OPENAI_API_KEY,
//  5. a local Ollama daemon, if one is reachable,
//  6. the disabled stub.
//
// Rung 5 exists because Ollama is local, offline and keyless, so it is
// the right fallback before the stub — and because the harness rung
// maps opencode and codex to "openai", which fails without a cloud
// key.
package adapters

import (
	// Blank imports: each package's init() calls
	// embedder.RegisterAdapter, and an init() only runs if the package
	// is imported. That is the whole mechanism.
	_ "github.com/dark-agents/dark-memory-mcp/internal/embedder/ollama"
	_ "github.com/dark-agents/dark-memory-mcp/internal/embedder/openai"
	_ "github.com/dark-agents/dark-memory-mcp/internal/embedder/voyage"
)
