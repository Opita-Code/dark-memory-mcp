// Package internal — vector.go: cosine helper + decoder for the
// recall package's embedder integration (Phase 13 T-202).
//
// The full Encode/Decode + cosine pipeline lives in
// internal/store/sqlite/vector.go (production path for stores).
// The recall package is read-only over the agent_memory.embedding
// BLOB column, so we re-implement a minimal decode + cosine here
// rather than depending on internal/store/sqlite (which would
// force a circular import for the sqlite → v4alpha/recall calls).
//
// All helpers are deterministic + NaN-safe: zero-length or
// dimension-mismatched inputs return 0 instead of polluting
// ranking with NaN or negative weights.
package recall

import (
	"encoding/binary"
	"math"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
)

// decodeEmbeddingBlob mirrors store/sqlite/vector.go:decodeVec but
// is local to recall (no cross-package dependency). The BLOB
// layout is the canonical little-endian float32 contiguous array
// (4 bytes per element), so `len(raw) % 4 == 0` is the only
// invariant the helper checks. Returns nil for empty inputs
// (caller decides whether nil is "no embedding" vs "skip row").
//
// Returns ErrInvalidEmbedding (the package-local one) when the BLOB
// is malformed; callers match on errors.Is to drop the row.
//
// expectedDim is optional — when > 0, the helper enforces the
// caller knows the row's metadata (e.g., from the EmbeddingDim
// column). When 0, the helper accepts any non-empty multiple of 4.
func decodeEmbeddingBlob(raw []byte, expectedDim int) (embedder.Vec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw)%4 != 0 {
		return nil, ErrInvalidEmbedding
	}
	n := len(raw) / 4
	if expectedDim > 0 && n != expectedDim {
		return nil, ErrInvalidEmbedding
	}
	out := make(embedder.Vec, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
}

// cosineSimilarity returns the cosine of a and b. Returns 0 for
// zero-length, mismatched-dim, or zero-norm inputs. NaN-safe.
//
// For unit vectors (the deterministic mock adapter's contract) the
// cosine collapses to a dot product; production adapters (OpenAI,
// ONNX) return unit-or-not-unit-length vectors and we compute the
// full cosine.
func cosineSimilarity(a, b embedder.Vec) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < len(a); i++ {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		na += ai * ai
		nb += bi * bi
	}
	denom := math.Sqrt(na * nb)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// ErrInvalidEmbedding signals the BLOB was malformed (not a
// multiple of 4 bytes, or expected-dim mismatch). Recall callers
// match on errors.Is to drop the row from vector scoring without
// failing the whole recall.
var ErrInvalidEmbedding = errInvalidEmbedding("recall: invalid embedding blob (wrong length or non-aligned)")

// errInvalidEmbedding is a typed string error so the helper above
// can construct it without `var Err... = errors.New(...)` losing
// the type identity. The unique named type also lets callers use
// errors.Is on a stable identity even if the helper signature
// evolves.
type errInvalidEmbedding string

func (e errInvalidEmbedding) Error() string { return string(e) }