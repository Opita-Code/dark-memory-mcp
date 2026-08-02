// Package onnx is the bundled-ONNX local-embedding adapter (PR-2.1
// of v2.9.0 plan, deferred from PR-2).
//
// PR-2 ships a stub that registers KindONNX and returns
// embedder.ErrDisabled on every Embed call. The full adapter
// (libonnxruntime session + model_quantized.onnx download + SHA256
// verification + GOMAXPROCS-pooled inference) lands in PR-2.1
// alongside the model bundle (~22 MB) and the cross-platform
// libonnxruntime shared library (~25 MB per platform).
//
// # Why a stub now
//
// row 160 PR-2 lists ONNX as one of three live paths, but row 163
// makes the model bundle an explicit choice ("+95 MB binary
// footprint, +1 dynamic library per platform"). Shipping the bundle
// in the same PR as the wire + cosine + RRF would have made the
// PR too big to review. Splitting lets the operator verify the
// retrieval math on its own merit first; the model bundle ships
// behind an opt-in `DARK_MEMORY_EMBEDDER=onnx` flag once active.
//
// # Activation
//
// `DARK_MEMORY_EMBEDDER=onnx` currently returns this stub; Embed
// errors with a "ship in PR-2.1" message. This is the deliberate
// failure mode: operators learn at first .Embed call that the
// bundle is intentionally not yet shipped (per row 170 design
// note: embedder factory degrade-graceful ambiguity).
package onnx

import (
	"context"
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// init registers the ONNX stub factory. PR-2.1 will swap the body
// here (the bundled ONNX session + libonnxruntime + model_quantized.onnx)
// without touching embedder.go.
func init() {
	embedder.RegisterAdapter(embedder.KindONNX, func(_ embedder.Options) (embedder.Embedder, error) {
		return New(Options{})
	})
}

// Options is the reserved parameter surface. PR-2.1 will populate
// ModelPath, TokenizerPath, OpaqueRuntime* fields.
type Options struct {
	// ModelPath overrides the default ONNX model file location.
	// Currently unused — model is not bundled in PR-2.
	ModelPath string

	// TokenizerPath overrides the default tokenizer.json location.
	// Currently unused.
	TokenizerPath string

	// ExpectedSHA256 is the build-time pinned SHA256 of the model
	// file. PR-2.1 verifies this on every model load.
	ExpectedSHA256 string

	// PoolSize overrides GOMAXPROCS-bound worker goroutines. 0 →
	// runtime.GOMAXPROCS(0) (one worker per logical CPU).
	PoolSize int
}

// New returns the stub ONNX adapter. Currently always succeeds;
// the failure mode is on first .Embed (returns ErrDisabled wrapped
// with a "ship in PR-2.1" hint).
//
// PR-2.1 will rewrite this to:
//  1. Load the model file from ModelPath (or download to
//     $DARK_HOME/models/).
//  2. Verify SHA256 against ExpectedSHA256.
//  3. Construct the ONNX Runtime session.
//  4. Spawn PoolSize worker goroutines + a request channel.
//  5. Embed enqueues inputs and reads from the result channel.
func New(opts Options) (embedder.Embedder, error) {
	_ = opts
	return &onnxStub{}, nil
}

// onnxStub is the PR-2 placeholder. Errors with embedder.ErrDisabled
// plus a clarifying message so operators understand the path is
// reserved but not yet wired.
type onnxStub struct{}

// Kind returns KindONNX. Operators see in health_ping that the
// env-var requested ONNX but the runtime is still the stub.
func (o *onnxStub) Kind() string { return embedder.KindONNX }

// Dim returns 384 (the all-MiniLM-L6-v2 dimension) so the schema
// sizing logic sees a non-zero number; if PR-2.1 ships a different
// model, this changes.
func (o *onnxStub) Dim() int { return 384 }

// Embed returns embedder.ErrDisabled plus a "ship in PR-2.1"
// message. The Store's search path matches on errors.Is to
// degrade gracefully to bm25.
func (o *onnxStub) Embed(_ context.Context, texts []string) ([]embedder.Vec, error) {
	_ = texts
	return nil, fmt.Errorf("embedder.onnx: bundled ONNX ships in PR-2.1 (row 162); %w", embedder.ErrDisabled)
}

// Close is a no-op. PR-2.1 will release the ONNX Runtime session.
func (o *onnxStub) Close() error { return nil }

// Compile-time guard that errors.Is (against embedder.ErrDisabled)
// remains valid as the package evolves.
var _ = errors.Is
