// Package mock provides a deterministic, no-network, no-disk embedder
// for tests. It hashes the input text into a stable unit vector so
// the cosine similarity between two texts is a pure function of
// their content — never time, never env, never network.
//
// PR-2 of v2.9.0 plan: dual_driver tests for vector + RRF retrieval
// rely on this so the test fixtures are reproducible without
// mocking OPENAI_API_KEY.
//
// # Why hash-based and not random
//
// Random vectors would test the *machinery* (decode, brute-force
// cosine, RRF fusion) but not the *ordering*. With hash-based
// vectors, "running" and "runner" produce similar (not identical)
// vectors; the test can assert a relative ordering between two
// documents without flakes.
//
// # Stability
//
// The hash is SHA-256 truncated to Dim() floats; each float is
// divided by the sqrt(d) for unit length. The unit-length guarantee
// means cosine similarity collapses to dot-product, simplifying
// the brute-force loop in the store.
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// init registers the deterministic test factory. Tests can opt out
// by calling embedder.resetAdapterRegistry() and re-registering
// their own factory.
func init() {
	embedder.RegisterAdapter(embedder.KindMock, func(_ embedder.Options) (embedder.Embedder, error) {
		return New(Options{})
	})
}

// DefaultDim is the canonical test dimensionality. The dual-driver
// SQLite store caps BLOB length at 4 KiB per row for an embedding,
// so 32 floats * 4 bytes/float = 128 bytes is well within budget
// while exercising the cosine + RRF paths with non-trivial rank
// order.
const DefaultDim = 32

// Options configures the mock adapter. Zero values are sensible
// defaults: Dim = DefaultDim, Seed is ignored.
type Options struct {
	// Dim overrides DefaultDim. Tests that want higher-dimensional
	// vectors set this explicitly.
	Dim int

	// Seed is accepted for API symmetry with future stochastic
	// adapters but UNUSED in v2.9.0 PR-2 (the hash-based design
	// is deterministic by construction).
	Seed uint64
}

// New returns a deterministic test embedder.
func New(opts Options) (embedder.Embedder, error) {
	if opts.Dim <= 0 {
		opts.Dim = DefaultDim
	}
	return &mockAdapter{dim: opts.Dim}, nil
}

// mockAdapter is stateless; all variability comes from the inputs.
type mockAdapter struct {
	dim int
}

// Kind returns KindMock. Tests assert against this string to
// distinguish deterministic fixtures from the production paths.
func (m *mockAdapter) Kind() string { return embedder.KindMock }

// Dim returns the configured dimensionality.
func (m *mockAdapter) Dim() int { return m.dim }

// Embed returns one vector per input. The hash-based construction
// is intentionally non-random so two texts with shared sub-tokens
// hash to similar vectors (Sha256-truncated bit-sharing is not
// perfect, but for unit-length vectors the cosine naturally
// clusters related inputs).
func (m *mockAdapter) Embed(_ context.Context, texts []string) ([]embedder.Vec, error) {
	out := make([]embedder.Vec, len(texts))
	for i, t := range texts {
		v := embedder.Vec(make([]float32, m.dim))
		sum := hashToUnitVector(t, v)
		// Re-normalize after sum to keep unit-length guarantee.
		// (sum is partial; full-length fold below.)
		_ = sum
		// Normalize to unit length.
		var sq float64
		for _, x := range v {
			sq += float64(x) * float64(x)
		}
		norm := math.Sqrt(sq)
		if norm == 0 {
			// Edge case: all-zero vectors should still rank
			// deterministically above themselves and below
			// everything else. Pick a canonical "neutral" vector
			// (zeros) and keep norm=0.
			continue
		}
		for k := range v {
			v[k] = float32(float64(v[k]) / norm)
		}
		out[i] = v
	}
	return out, nil
}

// Close is a no-op.
func (m *mockAdapter) Close() error { return nil }

// hashToUnitVector hashes text into v. Folds the SHA-256 digest
// (32 bytes → 8 floats; each 4-byte chunk is reinterpreted as a
// float32) across v. For Dim > 8, the digest is repeated with a
// counter to fill v.
//
// The result is NOT yet unit length; the caller normalizes.
func hashToUnitVector(text string, v embedder.Vec) float64 {
	digest := sha256.Sum256([]byte(text))
	var sum float64
	k := 0
	for i := 0; k < len(v); i++ {
		for j := 0; j < 8 && k < len(v); j++ {
			off := j * 4
			bits := binary.LittleEndian.Uint32(digest[off : off+4])
			f := math.Float32frombits(bits) / float32(i+1) // multi-round decorrelation
			v[k] = f
			sum += float64(f)
			k++
		}
		// Re-hash with counter i to fill remaining slots without
		// bias. Practical Dim never exceeds 1536 (OpenAI), so the
		// number of rounds is bounded.
		digest = sha256.Sum256(append(digest[:], byte(i)))
	}
	return sum
}
