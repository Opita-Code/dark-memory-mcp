// Package recall — cache.go: CachedSource, the TTL + INV-5 wrapper
// around any FrameSource implementation.
//
// # Atomicity contract
//   - ONE public type: CachedSource
//   - ONE public constructor: NewCachedSource
//   - DEPENDS on store.Store (5A.ii.a) for cache I/O
//   - DEPENDS on internal/policy.FrameSource (the inner source)
//   - DEPENDS on internal/store.SafetyHolder for canary threading
//
// # Trust boundary
// CachedSource is the trust boundary that enforces INV-5 (cache
// integrity). On every cache hit, it recomputes sha256(frame_json)
// and compares against the stored content_sha256. On mismatch:
//   1. Emit a write_audit row tagged session_event="cache_mismatch"
//      with the expected vs computed SHA. This is the audit
//      breadcrumb until the anomaly_events table lands (separate
//      future migration).
//   2. Delete the bad row.
//   3. Fall through to the inner source for recompose.
//
// # Wave placement
// This is 5A.ii.b.2.b. It depends on 5A.ii.a (FrameEnvelope +
// SaveFrame/GetFrame/DeleteFrame) and 5A.ii.b.2.a (StoreSource as
// the inner source). It does NOT depend on 5A.ii.b.2.c (the
// dark_memory_recall tool + delta) — that's a separate concern.
//
// # Scope (5A.ii.b.2.b)
//   - IdentityFrame and CapabilitiesFrame are cached (TTL 15min).
//   - ScopeFrame, DriftFrame, PersonaFrame pass through to inner
//     (inner returns nil for those today; caching them later is a
//     5A.ii.b.2.c concern).
//   - safety.Holder is threaded: IdentityFrame.CanaryActive reflects
//     Safety.Active() != "" at compose time.
package recall

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// frameTTL returns the cache TTL for a given frame kind. Mirrors
// the atomic.Max*FrameAge constants so cache expiry matches the
// gate's staleness budget.
func frameTTL(kind atomic.FrameKind) time.Duration {
	switch kind {
	case atomic.FrameIdentity:
		return atomic.MaxIdentityFrameAge
	case atomic.FrameCapabilities:
		return atomic.MaxCapabilitiesFrameAge
	case atomic.FrameScope:
		return atomic.MaxScopeFrameAge
	case atomic.FrameDrift:
		return atomic.MaxDriftFrameAge
	case atomic.FrameEvidence:
		return atomic.MaxEvidenceFrameAge
	case atomic.FramePersona:
		return atomic.MaxPersonaFrameAge
	default:
		// Unknown kind — be conservative (15min).
		return 15 * time.Minute
	}
}

// CachedSource wraps a FrameSource with TTL caching + INV-5 integrity
// verification. Implements policy.FrameSource.
//
// # Thread-safety
// CachedSource holds an Inner + Store + Safety. All three are
// thread-safe (Store has internal mutex; Safety has RWMutex;
// FrameSource is stateless composition). CachedSource itself holds
// no mutable state beyond the injected Now clock.
type CachedSource struct {
	// Inner is the upstream FrameSource that produces fresh frames
	// on cache miss. Required.
	Inner policy.FrameSource
	// Store is the persistent backing for cached frames. Required.
	Store store.Store
	// Safety is the canary holder. Optional. nil = canary always
	// reported as inactive (IdentityFrame.CanaryActive=false).
	Safety *store.SafetyHolder
	// Now is the wall-clock source. Defaults to time.Now if nil.
	Now func() time.Time
	// Logger is the optional audit logger for cache_mismatch events.
	// Defaults to the standard log package's default logger.
	Logger *log.Logger
}

// NewCachedSource constructs a CachedSource. now and logger default
// to time.Now and log.Default() respectively when nil.
func NewCachedSource(inner policy.FrameSource, st store.Store, safety *store.SafetyHolder, now func() time.Time, logger *log.Logger) *CachedSource {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = log.Default()
	}
	return &CachedSource{
		Inner:  inner,
		Store:  st,
		Safety: safety,
		Now:    now,
		Logger: logger,
	}
}

// IdentityFrame returns the cached IdentityFrame for sessionID, or
// composes one via Inner if no valid cached entry exists.
//
// # Canary threading semantics (re-evaluated on EVERY read)
//
// CanaryActive is NOT stored in the cache — it's re-evaluated
// from the live Safety holder on every read (both cache hit and
// cache miss paths). This means:
//
//   - If the canary was inactive when the frame was cached, but is
//     now active, the cache hit returns CanaryActive=true.
//   - If the canary was active when the frame was cached, but is
//     now inactive, the cache hit returns CanaryActive=false.
//
// Why this matters: the canary state is a property of the running
// server, not of the cached frame. Operators rotating canaries
// expect new reads to see the new canary immediately, not after
// the cache expires. By re-evaluating on every read, this is
// guaranteed.
func (c *CachedSource) IdentityFrame(ctx context.Context, sessionID string) (*atomic.IdentityFrame, error) {
	hitID, ok, err := c.cachedGetIdentity(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ok {
		return hitID, nil
	}
	if hitID != nil {
		// Cache row existed but was invalid (INV-5 mismatch or
		// unmarshal failure). Already deleted. Fall through to inner.
		_ = hitID
	}

	inner, err := c.Inner.IdentityFrame(ctx, sessionID)
	if err != nil || inner == nil {
		return inner, err
	}
	c.applyCanary(&inner.CanaryActive)
	if err := c.persistIdentity(ctx, sessionID, inner); err != nil {
		c.Logger.Printf("recall: cache write failed for session_id=%s kind=identity: %v",
			sessionID, err)
	}
	return inner, nil
}

// CapabilitiesFrame returns the cached CapabilitiesFrame, or
// composes one via Inner on miss.
func (c *CachedSource) CapabilitiesFrame(ctx context.Context, sessionID string) (*atomic.CapabilitiesFrame, error) {
	hitID, ok, err := c.cachedGetCapabilities(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ok {
		return hitID, nil
	}
	caps, err := c.Inner.CapabilitiesFrame(ctx, sessionID)
	if err != nil || caps == nil {
		return caps, err
	}
	if err := c.persistCapabilities(ctx, sessionID, caps); err != nil {
		c.Logger.Printf("recall: cache write failed for session_id=%s kind=capabilities: %v",
			sessionID, err)
	}
	return caps, nil
}

// ScopeFrame passes through to Inner. Caching for scope frames is
// deferred to 5A.ii.b.2.c (when StoreSource actually composes one).
func (c *CachedSource) ScopeFrame(ctx context.Context, sessionID string) (*atomic.ScopeFrame, error) {
	return c.Inner.ScopeFrame(ctx, sessionID)
}

// DriftFrame passes through to Inner. Deferred to 5A.ii.b.2.b in
// spirit (the cache layer would own it, but Inner doesn't compose
// drift frames yet).
func (c *CachedSource) DriftFrame(ctx context.Context, sessionID string) (*atomic.DriftFrame, error) {
	return c.Inner.DriftFrame(ctx, sessionID)
}

// PersonaFrame passes through to Inner. Deferred to 5A.ii.b.2.c.
func (c *CachedSource) PersonaFrame(ctx context.Context, sessionID string) (*atomic.PersonaFrame, error) {
	return c.Inner.PersonaFrame(ctx, sessionID)
}

// cachedGetIdentity is the per-kind wrapper around the cache fetch
// logic. Returns:
//   - (non-nil, true, nil)  — valid cache hit
//   - (nil, false, nil)     — miss (no row); caller falls through
//   - (nil, false, err)     — store error
//
// On INV-5 mismatch or unmarshal failure, the bad row is deleted
// and a cache_mismatch audit row is emitted (by cachedFetchRaw).
// The caller sees this as a miss and falls through to inner.
func (c *CachedSource) cachedGetIdentity(ctx context.Context, sessionID string) (*atomic.IdentityFrame, bool, error) {
	body, ok, err := c.cachedFetchRaw(ctx, sessionID, atomic.FrameIdentity)
	if err != nil || !ok {
		return nil, false, err
	}
	var f atomic.IdentityFrame
	if err := json.Unmarshal(body, &f); err != nil {
		c.Logger.Printf("recall: identity unmarshal failed: %v", err)
		return nil, false, nil
	}
	c.applyCanary(&f.CanaryActive)
	return &f, true, nil
}

// cachedGetCapabilities is the per-kind wrapper for CapabilitiesFrame.
func (c *CachedSource) cachedGetCapabilities(ctx context.Context, sessionID string) (*atomic.CapabilitiesFrame, bool, error) {
	body, ok, err := c.cachedFetchRaw(ctx, sessionID, atomic.FrameCapabilities)
	if err != nil || !ok {
		return nil, false, err
	}
	var f atomic.CapabilitiesFrame
	if err := json.Unmarshal(body, &f); err != nil {
		c.Logger.Printf("recall: capabilities unmarshal failed: %v", err)
		return nil, false, nil
	}
	return &f, true, nil
}

// cachedFetchRaw is the raw cache fetch + INV-5 verify + audit.
// Returns the frame_json bytes on hit. Performs deletion on mismatch.
func (c *CachedSource) cachedFetchRaw(ctx context.Context, sessionID string, kind atomic.FrameKind) ([]byte, bool, error) {
	env, err := c.Store.GetFrame(ctx, sessionID, atomic.ScopeSession, sessionID, kind)
	if err != nil {
		return nil, false, fmt.Errorf("recall: cachedFetchRaw GetFrame: %w", err)
	}
	if env == nil {
		return nil, false, nil
	}
	computed := sha256.Sum256(env.FrameJSON)
	if computed != env.ContentSHA256 {
		c.Logger.Printf("recall: cache_mismatch session_id=%s kind=%s frame_id=%d expected_sha=%x computed_sha=%x",
			sessionID, kind, env.ID, env.ContentSHA256, computed)
		_ = c.Store.RecordWrite(ctx, audit.WriteEvent{
			TableName:    "vibe_frames",
			Actor:        "inv5_cache_mismatch",
			SessionID:    sessionID,
			WritePath:    "CachedSource.cachedFetchRaw",
			SessionEvent: "cache_mismatch",
			Notes: fmt.Sprintf("frame_id=%d kind=%s expected_sha=%x computed_sha=%x",
				env.ID, kind, env.ContentSHA256, computed),
			CreatedAt: c.Now().UTC().Format(time.RFC3339Nano),
		})
		wc := c.auditWriteContext(sessionID)
		_ = c.Store.DeleteFrame(ctx, wc, env.ID)
		return nil, false, nil
	}
	return env.FrameJSON, true, nil
}

// persistIdentity writes a composed IdentityFrame to the cache.
func (c *CachedSource) persistIdentity(ctx context.Context, sessionID string, frame *atomic.IdentityFrame) error {
	body, err := frame.Render()
	if err != nil {
		return fmt.Errorf("recall: persistIdentity Render: %w", err)
	}
	hash, err := frame.Hash()
	if err != nil {
		return fmt.Errorf("recall: persistIdentity Hash: %w", err)
	}
	return c.persistRaw(ctx, sessionID, atomic.FrameIdentity, body, hash)
}

// persistCapabilities writes a composed CapabilitiesFrame to the cache.
func (c *CachedSource) persistCapabilities(ctx context.Context, sessionID string, frame *atomic.CapabilitiesFrame) error {
	body, err := frame.Render()
	if err != nil {
		return fmt.Errorf("recall: persistCapabilities Render: %w", err)
	}
	hash, err := frame.Hash()
	if err != nil {
		return fmt.Errorf("recall: persistCapabilities Hash: %w", err)
	}
	return c.persistRaw(ctx, sessionID, atomic.FrameCapabilities, body, hash)
}

// persistRaw is the shared persistence path. TTL comes from frameTTL(kind).
func (c *CachedSource) persistRaw(ctx context.Context, sessionID string, kind atomic.FrameKind, body []byte, hash [32]byte) error {
	now := c.Now()
	env := &atomic.FrameEnvelope{
		SessionID:     sessionID,
		ScopeLevel:    atomic.ScopeSession,
		ScopeID:       sessionID,
		Kind:          kind,
		ComposedAt:    now,
		ExpiresAt:     now.Add(frameTTL(kind)),
		FrameJSON:     body,
		ContentSHA256: hash,
		LastWriteID:   0, // cursor: deferred to a follow-up wave (write-audit max-id)
	}
	wc := c.auditWriteContext(sessionID)
	_, err := c.Store.SaveFrame(ctx, wc, env)
	return err
}

// auditWriteContext is the canonical WriteContext for cache-layer
// audit emissions. project_id is auto-filled by the Store.
func (c *CachedSource) auditWriteContext(sessionID string) store.WriteContext {
	return store.WriteContext{
		Actor:     "recall_cached_source",
		SessionID: sessionID,
		WritePath: "CachedSource",
	}
}

// applyCanary sets *dst to (Safety != nil && Safety.Active() != "")
// where Active() is the underlying safety.Holder.Active() call.
// Safety may be nil (canary disabled) or its Active function may be
// nil (degenerate config) — both cases default to false.
func (c *CachedSource) applyCanary(dst *bool) {
	if c.Safety == nil || c.Safety.Active == nil {
		*dst = false
		return
	}
	*dst = c.Safety.Active() != ""
}

// Compile-time check: CachedSource implements policy.FrameSource.
var _ policy.FrameSource = (*CachedSource)(nil)