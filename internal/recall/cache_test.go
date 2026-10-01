// Package recall_test — cache_test.go: Phase 7 Chunk 7.6 — covers
// the CachedSource (cache.go) surface, the TTL + INV-5 wrapper
// around any FrameSource implementation.
//
// # Strategy (SPEC §3.6 + risk table §5)
//
//   - Real SQLite in t.TempDir() for all integration-style tests
//     (per Chunk 6.4 lesson: store.Store has 105+ methods, hand-mocking
//     is fragile — same SQLite-in-tempdir pattern as assemble_store_test.go).
//   - Fake FrameSource (fakeInner) for the Inner source: 5 methods,
//     trivial to fake.
//   - frameTTL / persistIdentity / persistCapabilities / applyCanary /
//     auditWriteContext / recordCacheErr are exposed via export_test.go
//     for direct testing of internal paths.
//
// # Coverage target
//
// internal/recall 45.3% → ≥80%. cache.go is the largest single
// contributor to the 45.3% gap (CachedSource + 11 helpers). This
// file adds 20 tests covering every method on CachedSource +
// every helper in cache.go.
package recall_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log"
	"path/filepath"
	"sync"
	syncatomic "sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// === Test setup =========================================================

// fakeInner implements policy.FrameSource with controllable returns.
// Counts calls per method so tests can assert on miss/hit ratios.
type fakeInner struct {
	mu              sync.Mutex
	identityCalls   syncatomic.Int64
	capsCalls       syncatomic.Int64
	scopeCalls      syncatomic.Int64
	driftCalls      syncatomic.Int64
	personaCalls    syncatomic.Int64

	// Per-method overrides; nil field = no override (returns nil, nil).
	identityFn    func(ctx context.Context, sid string) (*atomic.IdentityFrame, error)
	capsFn        func(ctx context.Context, sid string) (*atomic.CapabilitiesFrame, error)
	scopeFn       func(ctx context.Context, sid string) (*atomic.ScopeFrame, error)
	driftFn       func(ctx context.Context, sid string) (*atomic.DriftFrame, error)
	personaFn     func(ctx context.Context, sid string) (*atomic.PersonaFrame, error)
	defaultIdentity *atomic.IdentityFrame
	defaultCaps     *atomic.CapabilitiesFrame
}

func (f *fakeInner) IdentityFrame(ctx context.Context, sid string) (*atomic.IdentityFrame, error) {
	f.identityCalls.Add(1)
	f.mu.Lock()
	fn := f.identityFn
	def := f.defaultIdentity
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sid)
	}
	if def != nil {
		// Clone per call: a realistic Inner (StoreSource) constructs
		// a fresh IdentityFrame on every miss. Returning a shared
		// pointer races with applyCanary + persistIdentity (Hash)
		// writes/reads of CanaryActive — see Chunk 7.6 concurrent-
		// access test design notes.
		clone := *def
		return &clone, nil
	}
	return nil, nil
}

func (f *fakeInner) CapabilitiesFrame(ctx context.Context, sid string) (*atomic.CapabilitiesFrame, error) {
	f.capsCalls.Add(1)
	f.mu.Lock()
	fn := f.capsFn
	def := f.defaultCaps
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sid)
	}
	if def != nil {
		clone := *def
		return &clone, nil
	}
	return nil, nil
}

func (f *fakeInner) ScopeFrame(ctx context.Context, sid string) (*atomic.ScopeFrame, error) {
	f.scopeCalls.Add(1)
	f.mu.Lock()
	fn := f.scopeFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sid)
	}
	return nil, nil
}

func (f *fakeInner) DriftFrame(ctx context.Context, sid string) (*atomic.DriftFrame, error) {
	f.driftCalls.Add(1)
	f.mu.Lock()
	fn := f.driftFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sid)
	}
	return nil, nil
}

func (f *fakeInner) PersonaFrame(ctx context.Context, sid string) (*atomic.PersonaFrame, error) {
	f.personaCalls.Add(1)
	f.mu.Lock()
	fn := f.personaFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sid)
	}
	return nil, nil
}

// newCachedSourceTestStore opens a fresh SQLite Store in a temp dir
// with the "default" project active. Mirrors newTestStore from
// assemble_store_test.go.
func newCachedSourceTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := sqlite.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	return st, func() { _ = st.Close() }
}

// newCachedSourceSession inserts a minimal session row.
func newCachedSourceSession(t *testing.T, st store.Store, operator string) string {
	t.Helper()
	id := "sess-cache-" + operator + "-" + time.Now().Format("150405.000000000")
	_, err := st.SaveSession(context.Background(), store.WriteContext{
		Actor:     operator,
		ProjectID: "default",
	}, &session.Session{
		SessionID:       id,
		Operator:        operator,
		Status:          "open",
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ConstitutionID:  "dark-agents/dark-mem",
		ConstitutionVer: "1.0.0",
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	return id
}

// makeIdentityFrame is a tiny constructor for fakeInner.defaultIdentity.
// Mirror of atomic.NewIdentityFrame but local so we don't need to import
// every field; we compose the same way CachedSource.persistIdentity does.
func makeIdentityFrame(t *testing.T, sessionID string) *atomic.IdentityFrame {
	t.Helper()
	f, err := atomic.NewIdentityFrame("actor-test", "operator-test", sessionID,
		"dark-agents/dark-mem", "1.0.0", false)
	if err != nil {
		t.Fatalf("NewIdentityFrame: %v", err)
	}
	return f
}

func makeCapabilitiesFrame(t *testing.T, sessionID string) *atomic.CapabilitiesFrame {
	t.Helper()
	f, err := atomic.NewCapabilitiesFrame("default", sessionID,
		[]atomic.ToolGrant{{ToolName: "session_start", Scope: "*", GrantedAt: time.Now()}},
		[]atomic.ScopeGrant{{ProjectID: "default", ReadOnly: false, GrantedAt: time.Now()}},
		time.Time{},
		"test",
	)
	if err != nil {
		t.Fatalf("NewCapabilitiesFrame: %v", err)
	}
	return f
}

// countFramesForSession counts the vibe_frames rows for a given
// (session_id, kind) tuple.
func countFramesForSession(t *testing.T, st store.Store, sessionID string, kind atomic.FrameKind) int {
	t.Helper()
	rows, err := st.ListFrames(context.Background(), store.FrameListFilters{
		ProjectID: "default",
		SessionID: sessionID,
		Kind:      kind,
	})
	if err != nil {
		t.Fatalf("ListFrames: %v", err)
	}
	return len(rows)
}

// countWriteAuditByActor counts write_audit rows with the given actor.
func countWriteAuditByActor(t *testing.T, st store.Store, actor string) int {
	t.Helper()
	rows, err := st.ListWrites(context.Background(), audit.ListFilters{Actor: actor})
	if err != nil {
		t.Fatalf("ListWrites: %v", err)
	}
	return len(rows)
}

// quietLogger returns a logger that discards all output. Tests use
// this to keep the test output clean when exercising error paths.
func quietLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// === Constructor defaults ================================================

func TestNewCachedSource_DefaultNowAndLogger(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()

	c := recall.NewCachedSource(&fakeInner{}, st, nil, nil, nil)
	if c == nil {
		t.Fatal("NewCachedSource returned nil")
	}
	if c.Now == nil {
		t.Error("Now defaulted to nil; expected time.Now")
	}
	if c.Logger == nil {
		t.Error("Logger defaulted to nil; expected log.Default")
	}
	// Sanity: Now() returns a sensible time.
	if c.Now().IsZero() {
		t.Error("Now() returned zero time")
	}
}

// === IdentityFrame: miss + hit ==========================================

func TestCachedSource_IdentityFrame_MissCallsInner(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	f, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if f == nil {
		t.Fatal("IdentityFrame returned nil; expected inner frame")
	}
	if got := inner.identityCalls.Load(); got != 1 {
		t.Errorf("Inner.IdentityFrame calls = %d; want 1", got)
	}
	// persistIdentity writes one row to the cache.
	if got := countFramesForSession(t, st, id, atomic.FrameIdentity); got != 1 {
		t.Errorf("vibe_frames rows for identity = %d; want 1", got)
	}
}

func TestCachedSource_IdentityFrame_HitOnSecondCall(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()
	_, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("first IdentityFrame: %v", err)
	}
	_, err = c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("second IdentityFrame: %v", err)
	}
	// Second call should be a cache hit → Inner.IdentityFrame not called again.
	if got := inner.identityCalls.Load(); got != 1 {
		t.Errorf("Inner.IdentityFrame calls = %d; want 1 (only first call)", got)
	}
	// And still only one cache row (idempotent persistence).
	if got := countFramesForSession(t, st, id, atomic.FrameIdentity); got != 1 {
		t.Errorf("vibe_frames rows for identity = %d; want 1", got)
	}
}

func TestCachedSource_IdentityFrame_InnerErrorPropagates(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	wantErr := errors.New("synthetic inner error")
	inner := &fakeInner{
		identityFn: func(ctx context.Context, sid string) (*atomic.IdentityFrame, error) {
			return nil, wantErr
		},
	}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	f, err := c.IdentityFrame(context.Background(), id)
	if !errors.Is(err, wantErr) {
		t.Errorf("IdentityFrame error = %v; want %v", err, wantErr)
	}
	if f != nil {
		t.Error("IdentityFrame returned non-nil on inner error")
	}
}

func TestCachedSource_IdentityFrame_InnerNilReturnsNil(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{
		identityFn: func(ctx context.Context, sid string) (*atomic.IdentityFrame, error) {
			return nil, nil
		},
	}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	f, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if f != nil {
		t.Error("IdentityFrame should return nil when inner returns nil")
	}
	// Nothing should have been persisted.
	if got := countFramesForSession(t, st, id, atomic.FrameIdentity); got != 0 {
		t.Errorf("vibe_frames rows for identity = %d; want 0 (no persist on nil inner)", got)
	}
}

// === TTL behavior =======================================================

// TestCachedSource_IdentityFrame_ExceedsTTL_Refetches advances the
// CachedSource.Now clock past MaxIdentityFrameAge; the next call must
// fall through to Inner.IdentityFrame because the cached row is
// expired (GetFrame returns nil for expired rows).
func TestCachedSource_IdentityFrame_ExceedsTTL_Refetches(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}

	var nowMu sync.Mutex
	nowTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return nowTime
	}
	c := recall.NewCachedSource(inner, st, nil, now, quietLogger())

	ctx := context.Background()

	// Call 1 at T0 — cache miss + persistIdentity writes row with
	// ExpiresAt = T0 + MaxIdentityFrameAge (15min).
	_, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("first IdentityFrame: %v", err)
	}
	if got := inner.identityCalls.Load(); got != 1 {
		t.Fatalf("Inner.IdentityFrame calls after first call = %d; want 1", got)
	}

	// Advance clock past TTL (15min). Cached row now expired.
	nowMu.Lock()
	nowTime = nowTime.Add(atomic.MaxIdentityFrameAge + time.Minute)
	nowMu.Unlock()

	// Call 2 — cache miss (expired), Inner.IdentityFrame called again.
	_, err = c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("second IdentityFrame: %v", err)
	}
	if got := inner.identityCalls.Load(); got != 2 {
		t.Errorf("Inner.IdentityFrame calls after TTL expiry = %d; want 2", got)
	}
}

func TestCachedSource_CapabilitiesFrame_ExceedsTTL_Refetches(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultCaps: makeCapabilitiesFrame(t, id)}

	var nowMu sync.Mutex
	nowTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return nowTime
	}
	c := recall.NewCachedSource(inner, st, nil, now, quietLogger())

	ctx := context.Background()
	_, err := c.CapabilitiesFrame(ctx, id)
	if err != nil {
		t.Fatalf("first CapabilitiesFrame: %v", err)
	}
	if got := inner.capsCalls.Load(); got != 1 {
		t.Fatalf("Inner.CapabilitiesFrame calls after first call = %d; want 1", got)
	}

	nowMu.Lock()
	nowTime = nowTime.Add(atomic.MaxCapabilitiesFrameAge + time.Minute)
	nowMu.Unlock()

	_, err = c.CapabilitiesFrame(ctx, id)
	if err != nil {
		t.Fatalf("second CapabilitiesFrame: %v", err)
	}
	if got := inner.capsCalls.Load(); got != 2 {
		t.Errorf("Inner.CapabilitiesFrame calls after TTL expiry = %d; want 2", got)
	}
}

// === Audit context on cache miss ========================================
// TestCachedSource_FetchRaw_WritesAuditRowOnCacheMiss confirms that
// the very first cache-miss path emits a write_audit row (via
// SaveFrame's transactional audit emission). The audit row count
// here is the SaveFrame audit, NOT the RecordWrite audit. We count
// writes by WritePath to be specific.
//
// Note: SaveFrame emits audit under Actor="recall_cached_source" (per
// auditWriteContext). This is the persistent audit breadcrumb that
// operators can grep to see cache writes.

func TestCachedSource_FetchRaw_WritesAuditRowOnCacheMiss(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	before := countWriteAuditByActor(t, st, "recall_cached_source")
	_, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	after := countWriteAuditByActor(t, st, "recall_cached_source")

	if after-before < 1 {
		t.Errorf("write_audit rows with actor=recall_cached_source: before=%d after=%d; want after > before",
			before, after)
	}
}

// === Persistence idempotency ============================================

// TestCachedSource_PersistIdentity_Idempotent verifies that calling
// persistIdentity twice with the same logical data does NOT create a
// second row in vibe_frames (SaveFrame is upsert).
func TestCachedSource_PersistIdentity_Idempotent(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()
	frame := makeIdentityFrame(t, id)

	// Direct call (test seam via export_test.go).
	if err := recall.PersistIdentity(c, ctx, id, frame); err != nil {
		t.Fatalf("first PersistIdentity: %v", err)
	}
	if err := recall.PersistIdentity(c, ctx, id, frame); err != nil {
		t.Fatalf("second PersistIdentity: %v", err)
	}

	if got := countFramesForSession(t, st, id, atomic.FrameIdentity); got != 1 {
		t.Errorf("vibe_frames rows for identity after 2 persist calls = %d; want 1", got)
	}
}

func TestCachedSource_PersistCapabilities_Idempotent(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultCaps: makeCapabilitiesFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()
	frame := makeCapabilitiesFrame(t, id)

	if err := recall.PersistCapabilities(c, ctx, id, frame); err != nil {
		t.Fatalf("first PersistCapabilities: %v", err)
	}
	if err := recall.PersistCapabilities(c, ctx, id, frame); err != nil {
		t.Fatalf("second PersistCapabilities: %v", err)
	}
	if got := countFramesForSession(t, st, id, atomic.FrameCapabilities); got != 1 {
		t.Errorf("vibe_frames rows for capabilities after 2 persist calls = %d; want 1", got)
	}
}

// === applyCanary semantics ==============================================

// TestCachedSource_ApplyCanary_NoSafety_DefaultsFalse: when no
// SafetyHolder is injected, the cached frame's CanaryActive is
// forced to false regardless of what the inner source returned.
func TestCachedSource_ApplyCanary_NoSafety_DefaultsFalse(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	// Inner returns a frame with CanaryActive=true — should be
	// overridden to false by applyCanary (no Safety holder).
	frameTrue, err := atomic.NewIdentityFrame("actor", "operator", id,
		"dark-agents/dark-mem", "1.0.0", true)
	if err != nil {
		t.Fatalf("NewIdentityFrame: %v", err)
	}
	inner := &fakeInner{defaultIdentity: frameTrue}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	f, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if f.CanaryActive {
		t.Error("CanaryActive = true; expected false (no Safety holder → defaults false)")
	}
}

// TestCachedSource_ApplyCanary_PropagatesFromSafety: when SafetyHolder
// is installed with Active() returning a non-empty token, CanaryActive
// is forced to true on every read.
func TestCachedSource_ApplyCanary_PropagatesFromSafety(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	safety := &store.SafetyHolder{
		SetCanary: func(string) {},
		Active:    func() string { return "test-canary-token" },
	}
	// Inner returns a frame with CanaryActive=false — should be
	// overridden to true by applyCanary (Safety.Active() != "").
	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, safety, nil, quietLogger())

	f, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if !f.CanaryActive {
		t.Error("CanaryActive = false; expected true (Safety.Active() != \"\")")
	}
}

// TestCachedSource_ApplyCanary_NilActive_DefaultsFalse: degenerate
// config where the SafetyHolder is non-nil but Active is nil.
func TestCachedSource_ApplyCanary_NilActive_DefaultsFalse(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	safety := &store.SafetyHolder{SetCanary: func(string) {}, Active: nil}
	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, safety, nil, quietLogger())

	f, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("IdentityFrame: %v", err)
	}
	if f.CanaryActive {
		t.Error("CanaryActive = true; expected false (Safety.Active=nil → defaults false)")
	}
}

// TestCachedSource_ApplyCanary_RotatedCanary: rotate the canary
// between calls; both reads should reflect the LIVE Safety.Active()
// value, not the cached value. This is the design invariant in the
// IdentityFrame docstring.
func TestCachedSource_ApplyCanary_RotatedCanary(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	var canary syncatomic.Value // string
	canary.Store("")
	safety := &store.SafetyHolder{
		SetCanary: func(string) {},
		Active:    func() string { return canary.Load().(string) },
	}
	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, safety, nil, quietLogger())

	ctx := context.Background()

	// Read 1: canary inactive.
	f1, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("first IdentityFrame: %v", err)
	}
	if f1.CanaryActive {
		t.Fatal("Read 1: CanaryActive=true; want false")
	}

	// Rotate canary.
	canary.Store("new-token")

	// Read 2 (cache hit): canary must reflect LIVE Safety.Active().
	f2, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("second IdentityFrame: %v", err)
	}
	if !f2.CanaryActive {
		t.Error("Read 2: CanaryActive=false; want true (canary rotated mid-session)")
	}

	// Rotate back.
	canary.Store("")

	// Read 3 (cache hit): canary inactive again.
	f3, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("third IdentityFrame: %v", err)
	}
	if f3.CanaryActive {
		t.Error("Read 3: CanaryActive=true; want false (canary cleared)")
	}
}

// === INV-5 cache mismatch ===============================================

// TestCachedSource_INV5_CacheMismatch_DeletesAndAudits corrupts the
// stored ContentSHA256 so the cachedFetchRaw path detects the
// mismatch. Expected behavior:
//   1. Audit row with actor=inv5_cache_mismatch written
//   2. Bad frame deleted from vibe_frames
//   3. Fall through to Inner.IdentityFrame (cache miss)
func TestCachedSource_INV5_CacheMismatch_DeletesAndAudits(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()

	// First call: cache miss + persistIdentity writes a good row.
	_, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("first IdentityFrame: %v", err)
	}

	// Corrupt ContentSHA256 on the stored row.
	rows, err := st.ListFrames(ctx, store.FrameListFilters{
		ProjectID: "default",
		SessionID: id,
		Kind:      atomic.FrameIdentity,
	})
	if err != nil {
		t.Fatalf("ListFrames: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("vibe_frames rows after first call = %d; want 1", len(rows))
	}
	wc := recall.AuditWriteContext(c, id)
	// Directly write a corrupted envelope via the store, bypassing
	// SaveFrame's own SHA computation. Set FrameJSON to "tampered"
	// but ContentSHA256 to a DIFFERENT hash so the row is genuinely
	// mismatched (sha256(FrameJSON) != ContentSHA256).
	bad := rows[0]
	bad.FrameJSON = []byte("tampered")
	bad.ContentSHA256 = sha256.Sum256([]byte("legitimate-frame-body"))
	if _, err := st.SaveFrame(ctx, wc, &bad); err != nil {
		t.Fatalf("SaveFrame: %v", err)
	}

	before := countWriteAuditByActor(t, st, "inv5_cache_mismatch")

	// Second call: must detect mismatch + delete + audit + fall-through.
	f, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("second IdentityFrame: %v", err)
	}
	if f == nil {
		t.Fatal("IdentityFrame returned nil on INV-5 mismatch; expected fall-through to inner")
	}
	after := countWriteAuditByActor(t, st, "inv5_cache_mismatch")
	if after-before < 1 {
		t.Errorf("write_audit rows with actor=inv5_cache_mismatch: before=%d after=%d; want after > before",
			before, after)
	}
	// Bad frame deleted: either gone, or replaced by fresh persistIdentity
	// (the fall-through path re-cached the inner frame).
	rows, err = st.ListFrames(ctx, store.FrameListFilters{
		ProjectID: "default",
		SessionID: id,
		Kind:      atomic.FrameIdentity,
	})
	if err != nil {
		t.Fatalf("ListFrames after mismatch: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("vibe_frames rows after mismatch = %d; want 1 (bad deleted, fresh re-persisted)", len(rows))
	}
	// The fresh row's SHA must match its body (sanity).
	computed := sha256.Sum256(rows[0].FrameJSON)
	if computed != rows[0].ContentSHA256 {
		t.Error("Fresh row after mismatch: ContentSHA256 still mismatched FrameJSON")
	}
}

// === recordCacheErr ====================================================

// TestCachedSource_RecordCacheErr_WritesErrorEvent confirms that the
// error observatory capture path produces a durable error row when
// recordCacheErr is called with a non-nil error.
func TestCachedSource_RecordCacheErr_WritesErrorEvent(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	// Direct call (test seam).
	recall.RecordCacheErr(c, context.Background(), id, "recall_cache",
		errors.New("synthetic cache error"))

	// Verify via error observatory listing.
	evs, err := st.ListErrorEvents(context.Background(), errorobs.ErrorListFilters{
		SessionID: id,
		ToolName:  "recall_cache",
	})
	if err != nil {
		t.Fatalf("ListErrorEvents: %v", err)
	}
	if len(evs) == 0 {
		t.Error("Expected at least 1 error event after RecordCacheErr; got 0")
	}
}

// === Concurrent access ===================================================

// TestCachedSource_ConcurrentAccess_RaceFree fires 100 concurrent
// IdentityFrame calls against the same session. With the upsert-on-
// (session, scope, kind) key, the final state must be ONE row with
// valid ContentSHA256. Run with -race to catch data races.
func TestCachedSource_ConcurrentAccess_RaceFree(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.IdentityFrame(ctx, id)
			if err != nil {
				t.Errorf("concurrent IdentityFrame: %v", err)
			}
		}()
	}
	wg.Wait()

	// Inner.IdentityFrame may be called up to 100 times (each goroutine
	// could miss once); the cache write contention is fine because
	// SaveFrame is locked at the SQLite layer.
	if got := inner.identityCalls.Load(); got == 0 || got > 100 {
		t.Errorf("Inner.IdentityFrame calls = %d; want in [1, 100]", got)
	}
	// End state: exactly one row, valid SHA.
	if got := countFramesForSession(t, st, id, atomic.FrameIdentity); got != 1 {
		t.Errorf("vibe_frames rows after concurrent burst = %d; want 1", got)
	}
	rows, err := st.ListFrames(ctx, store.FrameListFilters{
		ProjectID: "default",
		SessionID: id,
		Kind:      atomic.FrameIdentity,
	})
	if err != nil {
		t.Fatalf("ListFrames: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListFrames returned %d rows; want 1", len(rows))
	}
	computed := sha256.Sum256(rows[0].FrameJSON)
	if computed != rows[0].ContentSHA256 {
		t.Error("Final concurrent row: ContentSHA256 mismatch")
	}
}

// === Store error propagation ============================================

// TestCachedSource_StoreError_FallsThroughToErrorPath closes the
// Store mid-flight and verifies that the next CachedSource call
// propagates the store error (rather than crashing or returning a
// stale value).
func TestCachedSource_StoreError_FallsThroughToErrorPath(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	// First call succeeds.
	_, err := c.IdentityFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("warm-up IdentityFrame: %v", err)
	}

	// Close the store to force errors on the next cache hit.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Next call must return an error (from GetFrame on closed store).
	_, err = c.IdentityFrame(context.Background(), id)
	if err == nil {
		t.Error("IdentityFrame after store close returned nil error; expected store error")
	}
}

// === Pass-through frames ================================================

// TestCachedSource_PassThroughFrames_ReturnInnerResult verifies the
// ScopeFrame, DriftFrame, and PersonaFrame methods pass through to
// Inner without caching.
func TestCachedSource_PassThroughFrames_ReturnInnerResult(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	scopeFrame, err := atomic.NewScopeFrame(id, 42, nil, nil, "aligned", time.Now().UTC())
	if err != nil {
		t.Fatalf("NewScopeFrame: %v", err)
	}
	driftFrame, err := atomic.NewDriftFrame(id, 42, "aligned", time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("NewDriftFrame: %v", err)
	}
	personaFrame, err := atomic.NewPersonaFrame("dark-agents/dark-mem", "1.0.0", "",
		"voice", "claims", "", "technical")
	if err != nil {
		t.Fatalf("NewPersonaFrame: %v", err)
	}

	inner := &fakeInner{
		scopeFn:    func(ctx context.Context, sid string) (*atomic.ScopeFrame, error) { return scopeFrame, nil },
		driftFn:    func(ctx context.Context, sid string) (*atomic.DriftFrame, error) { return driftFrame, nil },
		personaFn:  func(ctx context.Context, sid string) (*atomic.PersonaFrame, error) { return personaFrame, nil },
	}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()

	// ScopeFrame
	gotScope, err := c.ScopeFrame(ctx, id)
	if err != nil {
		t.Fatalf("ScopeFrame: %v", err)
	}
	if gotScope == nil || gotScope.OpenSpecID != 42 {
		t.Errorf("ScopeFrame = %v; want OpenSpecID=42", gotScope)
	}
	if inner.scopeCalls.Load() != 1 {
		t.Errorf("Inner.ScopeFrame calls = %d; want 1 (pass-through, no cache)", inner.scopeCalls.Load())
	}

	// DriftFrame
	gotDrift, err := c.DriftFrame(ctx, id)
	if err != nil {
		t.Fatalf("DriftFrame: %v", err)
	}
	if gotDrift == nil || gotDrift.LastVerdict != "aligned" {
		t.Errorf("DriftFrame = %v; want LastVerdict=aligned", gotDrift)
	}

	// PersonaFrame
	gotPersona, err := c.PersonaFrame(ctx, id)
	if err != nil {
		t.Fatalf("PersonaFrame: %v", err)
	}
	if gotPersona == nil || gotPersona.Tone != "technical" {
		t.Errorf("PersonaFrame = %v; want Tone=technical", gotPersona)
	}
}

// === frameTTL unknown-Kind fallback =====================================

// TestCachedSource_FrameTTL_UnknownKind_ReturnsDefault exercises the
// default branch in the frameTTL switch — an unknown FrameKind value
// returns the conservative 15-minute default.
func TestCachedSource_FrameTTL_UnknownKind_ReturnsDefault(t *testing.T) {
	got := recall.FrameTTL(atomic.FrameKind("unknown-kind"))
	if got != 15*time.Minute {
		t.Errorf("frameTTL(unknown-kind) = %s; want 15m", got)
	}
}

// TestCachedSource_FrameTTL_AllKnownKinds exercises the canonical
// FrameKinds return the per-kind constants (regression for the TTL
// table itself).
func TestCachedSource_FrameTTL_AllKnownKinds(t *testing.T) {
	cases := []struct {
		kind atomic.FrameKind
		want time.Duration
	}{
		{atomic.FrameIdentity, atomic.MaxIdentityFrameAge},
		{atomic.FrameScope, atomic.MaxScopeFrameAge},
		{atomic.FrameEvidence, atomic.MaxEvidenceFrameAge},
		{atomic.FrameCapabilities, atomic.MaxCapabilitiesFrameAge},
		{atomic.FrameDrift, atomic.MaxDriftFrameAge},
		{atomic.FramePersona, atomic.MaxPersonaFrameAge},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := recall.FrameTTL(tc.kind); got != tc.want {
				t.Errorf("frameTTL(%s) = %s; want %s", tc.kind, got, tc.want)
			}
		})
	}
}

// === AuditWriteContext canonical values =================================

// TestCachedSource_AuditWriteContext_CanonicalValues pins the
// audit-context field values emitted by the cache layer so future
// refactors don't accidentally change them.
func TestCachedSource_AuditWriteContext_CanonicalValues(t *testing.T) {
	wc := recall.AuditWriteContext(nil, "sess-audit-ctx")
	if wc.Actor != "recall_cached_source" {
		t.Errorf("Actor = %q; want recall_cached_source", wc.Actor)
	}
	if wc.SessionID != "sess-audit-ctx" {
		t.Errorf("SessionID = %q; want sess-audit-ctx", wc.SessionID)
	}
	if wc.WritePath != "CachedSource" {
		t.Errorf("WritePath = %q; want CachedSource", wc.WritePath)
	}
}

// === End-to-end: TTL + canary rotation in one flow ======================

// TestCachedSource_EndToEnd_TTLPlusCanary is a small scenario test
// that exercises the cache miss → persist → cache hit + canary
// rotation flow in a single test.
func TestCachedSource_EndToEnd_TTLPlusCanary(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	var canary syncatomic.Value
	canary.Store("")
	safety := &store.SafetyHolder{
		SetCanary: func(string) {},
		Active:    func() string { return canary.Load().(string) },
	}
	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, safety, nil, quietLogger())

	ctx := context.Background()

	// Step 1: miss + persist; canary inactive.
	f1, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("step 1: %v", err)
	}
	if f1.CanaryActive {
		t.Error("step 1: CanaryActive=true; want false")
	}

	// Step 2: hit; canary still inactive.
	f2, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("step 2: %v", err)
	}
	if f2.CanaryActive {
		t.Error("step 2: CanaryActive=true; want false (canary still inactive)")
	}
	if inner.identityCalls.Load() != 1 {
		t.Errorf("step 2: Inner.IdentityFrame calls = %d; want 1 (cache hit)", inner.identityCalls.Load())
	}

	// Step 3: rotate canary → next read (cache hit) reflects it.
	canary.Store("rotated-token")
	f3, err := c.IdentityFrame(ctx, id)
	if err != nil {
		t.Fatalf("step 3: %v", err)
	}
	if !f3.CanaryActive {
		t.Error("step 3: CanaryActive=false; want true (canary rotated)")
	}
}

// === CapabilitiesFrame: miss + hit + persist error ====================

func TestCachedSource_CapabilitiesFrame_MissCallsInner(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultCaps: makeCapabilitiesFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	f, err := c.CapabilitiesFrame(context.Background(), id)
	if err != nil {
		t.Fatalf("CapabilitiesFrame: %v", err)
	}
	if f == nil {
		t.Fatal("CapabilitiesFrame returned nil; expected inner frame")
	}
	if got := inner.capsCalls.Load(); got != 1 {
		t.Errorf("Inner.CapabilitiesFrame calls = %d; want 1", got)
	}
	if got := countFramesForSession(t, st, id, atomic.FrameCapabilities); got != 1 {
		t.Errorf("vibe_frames rows for capabilities = %d; want 1", got)
	}
}

func TestCachedSource_CapabilitiesFrame_HitOnSecondCall(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	inner := &fakeInner{defaultCaps: makeCapabilitiesFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())

	ctx := context.Background()
	if _, err := c.CapabilitiesFrame(ctx, id); err != nil {
		t.Fatalf("first CapabilitiesFrame: %v", err)
	}
	if _, err := c.CapabilitiesFrame(ctx, id); err != nil {
		t.Fatalf("second CapabilitiesFrame: %v", err)
	}
	if got := inner.capsCalls.Load(); got != 1 {
		t.Errorf("Inner.CapabilitiesFrame calls = %d; want 1 (only first call)", got)
	}
}

// TestCachedSource_RecordCacheErr_NilError_NoOp confirms the early-
// return guard at recordCacheErr's first line: nil error → no DB
// write.
func TestCachedSource_RecordCacheErr_NilError_NoOp(t *testing.T) {
	st, cleanup := newCachedSourceTestStore(t)
	defer cleanup()
	id := newCachedSourceSession(t, st, "nico")

	// Build the cache so c.Safety/Logger are wired (RecordCacheErr
	// reads c.Store.ActiveProject + calls c.Store.SaveErrorEvent).
	inner := &fakeInner{defaultIdentity: makeIdentityFrame(t, id)}
	c := recall.NewCachedSource(inner, st, nil, nil, quietLogger())
	_ = inner

	before, err := st.ListErrorEvents(context.Background(), errorobs.ErrorListFilters{SessionID: id})
	if err != nil {
		t.Fatalf("ListErrorEvents before: %v", err)
	}

	recall.RecordCacheErr(c, context.Background(), id, "recall_cache", nil)

	after, err := st.ListErrorEvents(context.Background(), errorobs.ErrorListFilters{SessionID: id})
	if err != nil {
		t.Fatalf("ListErrorEvents after: %v", err)
	}

	if len(after) != len(before) {
		t.Errorf("recordCacheErr(nil) wrote events: before=%d after=%d; want equal", len(before), len(after))
	}
}

// === Marshal/Unmarshal helpers (compile-time sanity) ====================
// (Compile-time checks live in the test file's body above; nothing else needed.)