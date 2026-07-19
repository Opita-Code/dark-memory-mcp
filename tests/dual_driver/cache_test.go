// Package dual_driver_test — cache_test.go: dual-driver contract
// tests for CachedSource (Wave 5A.ii.b.2.b). Verifies:
//   1. Identity + Capabilities cache round-trip.
//   2. INV-5 hash mismatch triggers recompose + cache_mismatch audit.
//   3. Safety threading to IdentityFrame.CanaryActive.
//
// # 5A.ii.b.2.b scope
// Only Identity + Capabilities are cached. Scope/Drift/Persona
// pass through (they return nil from StoreSource today).
package dual_driver_test

import (
	"context"
	"crypto/sha256"
	"log"
	"os"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// TestCachedSourceIdentity exercises cache round-trip for IdentityFrame.
// First call: cache miss → compose via inner → persist.
// Second call: cache hit → return cached.
func TestCachedSourceIdentity(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{Driver: store.DriverSQLite, DSN: tmp + "/cache.db", WALMode: true, ForeignKeys: true}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "D"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	sid := "test-cache-identity"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestCache"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-op",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	inner := recall.NewStoreSource(s, nil)
	cached := recall.NewCachedSource(inner, s, nil, nil, nil)

	// First call — cache miss.
	id1, err := cached.IdentityFrame(ctx, sid)
	if err != nil || id1 == nil {
		t.Fatalf("IdentityFrame #1: err=%v id=%+v", err, id1)
	}
	if id1.SessionID != sid {
		t.Errorf("IdentityFrame #1 SessionID = %q, want %q", id1.SessionID, sid)
	}

	// Verify the row landed in vibe_frames.
	env, err := s.GetFrame(ctx, sid, atomic.ScopeSession, sid, atomic.FrameIdentity)
	if err != nil || env == nil {
		t.Fatalf("GetFrame after first call: err=%v env=%+v", err, env)
	}

	// Second call — cache hit. Mutate the cached frame in the DB to
	// detect that the cached path was taken (rather than recompose).
	// We can't easily prove "cache hit" without timing, but we can
	// prove "no second SaveFrame row" by listing frames.
	framesBefore, err := s.ListFrames(ctx, store.FrameListFilters{ProjectID: "default", SessionID: sid})
	if err != nil {
		t.Fatalf("ListFrames #1: %v", err)
	}
	id2, err := cached.IdentityFrame(ctx, sid)
	if err != nil || id2 == nil {
		t.Fatalf("IdentityFrame #2: err=%v id=%+v", err, id2)
	}
	framesAfter, err := s.ListFrames(ctx, store.FrameListFilters{ProjectID: "default", SessionID: sid})
	if err != nil {
		t.Fatalf("ListFrames #2: %v", err)
	}
	if len(framesBefore) != len(framesAfter) {
		t.Errorf("IdentityFrame #2 added a row: before=%d after=%d (cache miss should not occur)",
			len(framesBefore), len(framesAfter))
	}
	if id1.SessionID != id2.SessionID || id1.Operator != id2.Operator || id1.ConstitutionID != id2.ConstitutionID {
		t.Errorf("IdentityFrame mismatch between calls: %+v vs %+v", id1, id2)
	}
}

// TestCachedSourceCapabilities exercises cache round-trip for CapabilitiesFrame.
func TestCachedSourceCapabilities(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{Driver: store.DriverSQLite, DSN: tmp + "/cache.db", WALMode: true, ForeignKeys: true}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "D"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	sid := "test-cache-caps"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestCache"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-op",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	inner := recall.NewStoreSource(s, nil)
	cached := recall.NewCachedSource(inner, s, nil, nil, nil)

	// First call — cache miss.
	caps1, err := cached.CapabilitiesFrame(ctx, sid)
	if err != nil || caps1 == nil {
		t.Fatalf("CapabilitiesFrame #1: err=%v caps=%+v", err, caps1)
	}
	if !caps1.HasGrant("dark_memory_active_policy") {
		t.Errorf("CapabilitiesFrame #1 missing default grant")
	}

	// Verify row landed.
	env, err := s.GetFrame(ctx, sid, atomic.ScopeSession, sid, atomic.FrameCapabilities)
	if err != nil || env == nil {
		t.Fatalf("GetFrame after first call: err=%v env=%+v", err, env)
	}

	// Second call — should be a cache hit (no second row).
	framesBefore, err := s.ListFrames(ctx, store.FrameListFilters{ProjectID: "default", SessionID: sid})
	if err != nil {
		t.Fatalf("ListFrames #1: %v", err)
	}
	caps2, err := cached.CapabilitiesFrame(ctx, sid)
	if err != nil || caps2 == nil {
		t.Fatalf("CapabilitiesFrame #2: err=%v caps=%+v", err, caps2)
	}
	framesAfter, err := s.ListFrames(ctx, store.FrameListFilters{ProjectID: "default", SessionID: sid})
	if err != nil {
		t.Fatalf("ListFrames #2: %v", err)
	}
	if len(framesBefore) != len(framesAfter) {
		t.Errorf("CapabilitiesFrame #2 added a row: before=%d after=%d (cache miss should not occur)",
			len(framesBefore), len(framesAfter))
	}
}

// TestCachedSourceInv5Mismatch exercises INV-5 verification. Saves a
// frame with deliberately wrong content_sha256, then verifies that
// CachedSource.IdentityFrame detects the mismatch, deletes the bad
// row, and returns a valid (recomposed) frame.
//
// Also verifies that the cache_mismatch audit row was emitted.
func TestCachedSourceInv5Mismatch(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{Driver: store.DriverSQLite, DSN: tmp + "/cache.db", WALMode: true, ForeignKeys: true}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "D"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	sid := "test-cache-inv5"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestCache"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-op",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Save a tampered frame directly via the Store. body is valid
	// JSON; ContentSHA256 is a deliberately wrong hash.
	body := []byte(`{"actor":"ci-op","operator":"ci-op","session_id":"` + sid + `","constitution_id":"dark-agents/dark-memory-mcp-test","constitution_ver":"1.0.0","canary_active":false,"composed_at":"2026-01-01T00:00:00Z"}`)
	wrongHash := sha256.Sum256([]byte("deliberately-wrong-content"))
	env := &atomic.FrameEnvelope{
		ProjectID:     "default",
		SessionID:     sid,
		ScopeLevel:    atomic.ScopeSession,
		ScopeID:       sid,
		Kind:          atomic.FrameIdentity,
		ComposedAt:    nowFn(),
		ExpiresAt:     nowFn().Add(time.Hour), // well in the future so it doesn't expire
		FrameJSON:     body,
		ContentSHA256: wrongHash,
	}
	if _, err := s.SaveFrame(ctx, wc, env); err != nil {
		t.Fatalf("SaveFrame (tampered): %v", err)
	}

	// Capture write_audit count before; verify a new cache_mismatch
	// audit row gets emitted on the read path.
	writesBefore, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, Limit: 100})
	if err != nil {
		t.Fatalf("ListWrites before: %v", err)
	}
	mmBefore := 0
	for _, w := range writesBefore {
		if w.SessionEvent == "cache_mismatch" {
			mmBefore++
		}
	}

	// Now call CachedSource.IdentityFrame. It must:
	//   1. Detect the SHA mismatch.
	//   2. Emit a cache_mismatch audit row.
	//   3. Delete the bad row.
	//   4. Recompose via the inner source.
	//   5. Persist a fresh, valid row.
	//   6. Return a non-nil, valid frame.
	inner := recall.NewStoreSource(s, nil)
	cached := recall.NewCachedSource(inner, s, nil, nil, log.New(os.Stderr, "", 0))

	id, err := cached.IdentityFrame(ctx, sid)
	if err != nil || id == nil {
		t.Fatalf("IdentityFrame after tamper: err=%v id=%+v", err, id)
	}
	if id.SessionID != sid {
		t.Errorf("IdentityFrame SessionID = %q, want %q", id.SessionID, sid)
	}

	// Verify cache_mismatch audit was emitted.
	writesAfter, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, Limit: 100})
	if err != nil {
		t.Fatalf("ListWrites after: %v", err)
	}
	mmAfter := 0
	for _, w := range writesAfter {
		if w.SessionEvent == "cache_mismatch" {
			mmAfter++
		}
	}
	if mmAfter <= mmBefore {
		t.Errorf("expected cache_mismatch audit to be emitted; before=%d after=%d", mmBefore, mmAfter)
	}

	// Verify the tampered row is gone (Store.GetFrame should now
	// return the fresh, valid row, not the tampered one).
	fresh, err := s.GetFrame(ctx, sid, atomic.ScopeSession, sid, atomic.FrameIdentity)
	if err != nil || fresh == nil {
		t.Fatalf("GetFrame after recompose: err=%v env=%+v", err, fresh)
	}
	freshHash := sha256.Sum256(fresh.FrameJSON)
	if freshHash != fresh.ContentSHA256 {
		t.Errorf("recomposed frame still fails INV-5: fresh_sha=%x stored_sha=%x",
			freshHash, fresh.ContentSHA256)
	}
}

// TestCachedSourceCanary exercises safety threading. With Safety
// reporting active, CanaryActive must be true on the returned
// IdentityFrame. With Safety reporting inactive (or nil), it must
// be false.
func TestCachedSourceCanary(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{Driver: store.DriverSQLite, DSN: tmp + "/cache.db", WALMode: true, ForeignKeys: true}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "D"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	s.SetActiveProject(ctx, "default")

	sid := "test-cache-canary"
	wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestCache"}
	_, err = s.SaveSession(ctx, wc, &session.Session{
		SessionID:       sid,
		Operator:        "ci-op",
		ConstitutionID:  "dark-agents/dark-memory-mcp-test",
		ConstitutionVer: "1.0.0",
		Status:          string(session.StatusOpen),
	})
	if err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	inner := recall.NewStoreSource(s, nil)

	// Case 1: nil Safety → CanaryActive=false.
	cachedNoSafety := recall.NewCachedSource(inner, s, nil, nil, nil)
	id1, err := cachedNoSafety.IdentityFrame(ctx, sid)
	if err != nil || id1 == nil {
		t.Fatalf("IdentityFrame (no safety): err=%v id=%+v", err, id1)
	}
	if id1.CanaryActive {
		t.Errorf("IdentityFrame with nil Safety: CanaryActive=true, want false")
	}

	// Case 2: Safety reporting active canary → CanaryActive=true.
	activeSafety := &store.SafetyHolder{
		Active: func() string { return "DARK_CANARY_ACTIVE_TOKEN" },
	}
	cachedActive := recall.NewCachedSource(inner, s, activeSafety, nil, nil)
	id2, err := cachedActive.IdentityFrame(ctx, sid)
	if err != nil || id2 == nil {
		t.Fatalf("IdentityFrame (active safety): err=%v id=%+v", err, id2)
	}
	if !id2.CanaryActive {
		t.Errorf("IdentityFrame with active Safety: CanaryActive=false, want true")
	}

	// Case 3: Safety reporting empty → CanaryActive=false.
	inactiveSafety := &store.SafetyHolder{
		Active: func() string { return "" },
	}
	cachedInactive := recall.NewCachedSource(inner, s, inactiveSafety, nil, nil)
	id3, err := cachedInactive.IdentityFrame(ctx, sid)
	if err != nil || id3 == nil {
		t.Fatalf("IdentityFrame (inactive safety): err=%v id=%+v", err, id3)
	}
	if id3.CanaryActive {
		t.Errorf("IdentityFrame with empty Safety.Active(): CanaryActive=true, want false")
	}
}

// nowFn is a tiny helper for tests that need a fresh time.Time.
func nowFn() (t time.Time) {
	return time.Now().UTC()
}