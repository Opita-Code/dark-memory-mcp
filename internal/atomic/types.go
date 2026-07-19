// Package atomic implements the active-memory Frame model from
// ACTIVE_MEMORY_RFC.md §3 M1. Frames are atomic context units composed
// server-side and attached to every gate-mediated tools/call response;
// the LLM never assembles context across multiple tool calls.
//
// # Why atomic
//
// The RFC commits to A2 (atomic context, not chunks of rows) — every
// context injection is a frame of {kind, scope, bindings, drifts,
// persona}, with the LLM receiving one coherent view per call instead
// of joining four row-dumps in its head. This package is the canonical
// type system for that commitment.
//
// # Scope
//
// This package provides:
//   - The FrameKind enum (the 6 canonical kinds).
//   - The ScopeLevel enum (where the frame lives in the global/project/session/call hierarchy).
//   - The FrameEnvelope carrier struct (the persisted shape, owned by the cache layer).
//   - The Frame interface (runtime shape that orchestrators + Gate consume).
//
// This package does NOT provide:
//   - Persistence (save/load). That lives in internal/store/frames.go (5A.iii).
//   - Composition (which scopes subsume, what fields are required per kind).
//     That lives in internal/recall/assemble.go (5A.ii).
//   - Cache invalidation / TTL logic. That lives in internal/recall/cache.go (5A.ii).
//
// # Trust boundary
//
// This package is the lowest layer; all validation rules here are enforced
// at construction time (New*Frame constructor) AND on every Hash() /
// VerifyAgainstWriteAudit() call. The Gate layer above MUST treat
// these errors as authoritative refusal reasons.
package atomic

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FrameKind is the canonical kind of an atomic frame. The order of these
// constants is the authoritative order returned by `AllFrameKinds()` — do
// not reorder without a major version bump. Frame labels are persisted as
// TEXT in the vibe_frames table (frame_kind column).
type FrameKind string

// Canonical frame kinds. The six kinds are listed in the order they are
// composed by the Gate (per ACTIVE_MEMORY_RFC.md §M1; composition order
// is fixed so cached frames can be compared positionally when subsumed).
const (
	FrameIdentity     FrameKind = "identity"     // actor/operator/session/constitution binding
	FrameScope        FrameKind = "scope"        // open spec, in-flight tasks, evidence pointers
	FrameEvidence     FrameKind = "evidence"     // recent writes + linked research + last_seen_token
	FrameCapabilities FrameKind = "capabilities" // granted tools, scopes, expires_at
	FrameDrift        FrameKind = "drift"        // last drift verdict per open spec
	FramePersona      FrameKind = "persona"      // voice, claims, refusal pattern, tone
)

// allFrameKinds is the canonical ordered slice. `AllFrameKinds()` returns
// a defensive copy.
var allFrameKinds = [...]FrameKind{
	FrameIdentity,
	FrameScope,
	FrameEvidence,
	FrameCapabilities,
	FrameDrift,
	FramePersona,
}

// AllFrameKinds returns every canonical FrameKind in composition order.
func AllFrameKinds() []FrameKind {
	out := make([]FrameKind, len(allFrameKinds))
	copy(out, allFrameKinds[:])
	return out
}

// ErrUnknownFrameKind is returned when a FrameKind value is not one of the
// canonical six. Use errors.Is to branch in higher layers.
var ErrUnknownFrameKind = errors.New("atomic: unknown frame kind")

// ParseFrameKind validates and canonicalises a frame-kind string. Unknown
// values return ErrUnknownFrameKind wrapped with the offending value.
func ParseFrameKind(s string) (FrameKind, error) {
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrUnknownFrameKind)
	}
	k := FrameKind(s)
	for _, candidate := range allFrameKinds {
		if k == candidate {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not one of %v", ErrUnknownFrameKind, s, AllFrameKinds())
}

// ScopeLevel identifies where a frame lives in the subsumption hierarchy.
// Frames at lower levels subsume higher levels (call ⊂ session ⊂ project ⊂ global).
// Persisted as TEXT in vibe_frames.scope_level; validated by the v11 schema CHECK constraint.
type ScopeLevel string

// Canonical scope levels in subsumption order (lowest to highest).
const (
	ScopeCall    ScopeLevel = "call"    // per-call ephemeral (rarely cached)
	ScopeSession ScopeLevel = "session" // session-scoped (resurrectable)
	ScopeProject ScopeLevel = "project" // project-scoped (cross-session)
	ScopeGlobal  ScopeLevel = "global"  // operator/instance-scoped (system-wide)
)

// allScopeLevels is the canonical ordered slice.
var allScopeLevels = [...]ScopeLevel{
	ScopeCall,
	ScopeSession,
	ScopeProject,
	ScopeGlobal,
}

// AllScopeLevels returns every canonical ScopeLevel in subsumption order.
func AllScopeLevels() []ScopeLevel {
	out := make([]ScopeLevel, len(allScopeLevels))
	copy(out, allScopeLevels[:])
	return out
}

// ErrUnknownScopeLevel is returned when a ScopeLevel value is not one of
// the canonical four.
var ErrUnknownScopeLevel = errors.New("atomic: unknown scope level")

// ParseScopeLevel validates and canonicalises a scope-level string.
func ParseScopeLevel(s string) (ScopeLevel, error) {
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrUnknownScopeLevel)
	}
	l := ScopeLevel(s)
	for _, candidate := range allScopeLevels {
		if l == candidate {
			return l, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not one of %v", ErrUnknownScopeLevel, s, AllScopeLevels())
}

// FrameEnvelope is the persisted shape of a frame. It carries the kind,
// scope, composition timestamp, expiration, canonical JSON body, the
// INV-5 content-hash, and a write-audit pointer for cache invalidation.
//
// FrameEnvelope is the I/O shape between this package and the store/cache
// layer (internal/store/frames.go, internal/recall/cache.go). The Frame
// interface is the runtime shape orchestrators consume; FrameEnvelope
// wraps Frame for persistence.
type FrameEnvelope struct {
	ID            int64      `json:"id"`                  // row id in vibe_frames (0 for in-flight)
	ProjectID     string     `json:"project_id"`           // INV-7 explicit project_id
	SessionID     string     `json:"session_id"`           // session that composed this frame
	ScopeLevel    ScopeLevel `json:"scope_level"`          // global|project|session|call
	ScopeID       string     `json:"scope_id"`             // project_id for project-level scope, etc.
	Kind          FrameKind  `json:"kind"`                 // identity|scope|evidence|capabilities|drift|persona
	ComposedAt    time.Time  `json:"composed_at"`          // RFC3339Nano
	ExpiresAt     time.Time  `json:"expires_at"`           // RFC3339Nano; cache TTL
	FrameJSON     []byte     `json:"frame_json"`           // canonical JSON of the frame body (canonicalised, sorted keys)
	ContentSHA256 [32]byte   `json:"content_sha256"`       // INV-5; sha256(FrameJSON)
	LastWriteID   int64      `json:"last_write_id"`        // pointer into write_audit.max(id) at compose time
	CreatedAt     time.Time  `json:"created_at"`           // RFC3339Nano (persistence timestamp)
}

// Frame is the runtime shape orchestrators and the Gate consume. Every
// frame kind implements this interface. The Render() output is the
// canonical JSON that gets persisted as FrameEnvelope.FrameJSON.
//
// Frame is read-only from the consumer's perspective; mutators go
// through the New*Frame constructors.
type Frame interface {
	// Kind returns the canonical FrameKind. Implementations return a
	// constant; no allocation.
	Kind() FrameKind

	// ComposedAt returns the wall-clock time the frame was composed.
	// Used to compute staleness against MAX_FRAME_AGE.
	ComposedAt() time.Time

	// Validate returns nil if the frame's internal invariants hold
	// (non-empty required fields, consistent timestamps, etc.). The
	// Gate calls this before forwarding the frame to the orchestrator.
	Validate() error

	// Hash returns the canonical SHA-256 of the frame's bytes plus a
	// non-nil error if the canonical encoder fails. The caching layer
	// uses this to detect drift at read time (INV-5); a non-nil error
	// means the frame is not hashable and the cache layer should
	// treat it as a miss rather than a guaranteed-clean comparison.
	//
	// Wave 5X.2: signature aligned with all 6 concrete impls, which
	// have always returned ([32]byte, error). Pre-5X.2 the interface
	// declared `Hash() [32]byte` (no error) — latent mismatch that
	// only surfaced if a caller ever typed a variable as `atomic.Frame`.
	// No production code did, but the interface was a footgun for
	// future callers.
	Hash() ([32]byte, error)

	// Render returns the canonical JSON encoding used for persistence.
	// Sorted keys, stable field order, no whitespace — so the same
	// frame always hashes to the same value.
	Render() ([]byte, error)
}

// hashCanonical is a helper that JSON-encodes a frame via the canonical
// encoding/json package (which sorts map keys) and returns SHA-256 of
// the bytes. Centralises the canonicalisation so all Frame impls
// produce comparable hashes.
func hashCanonical(v interface{}) ([32]byte, error) {
	var zero [32]byte
	b, err := json.Marshal(v)
	if err != nil {
		return zero, fmt.Errorf("hashCanonical: marshal: %w", err)
	}
	return sha256.Sum256(b), nil
}

// jsonMarshal is a small helper around json.Marshal — kept as its own
// function so each Frame impl's Render() looks identical and stays
// consistent if we later wrap the encoder (e.g. to enforce sorted keys
// or add canonical-time formatting).
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
