// Package dual_driver_test — cache_invalidation_test.go: regression
// tests for the v2.1.3 resolver cache-invalidation hook. Reproduces
// the production race where session_start's pre-inner buildGateInput
// populated the resolver cache with a stale empty value, then the
// inner SetActiveSession write happened — but the cache wasn't
// flushed, so the next tool call within the 5s TTL returned "" and
// the gate refused.
//
// Fix: Orchestrator exposes OnActiveSessionChanged callback; main.go
// wires it to resolver.Invalidate. These tests verify:
//
//  1. SessionStart invokes the callback with the project_id.
//  2. SessionClose invokes the callback with the project_id.
//  3. nil callback is safe (orchestrator skips the call).
//  4. End-to-end race regression: with the hook wired, a
//     pre-session_start cache entry is gone after SessionStart
//     returns. The next ActiveSessionID call returns the new
//     session_id, not the stale empty value.
package dual_driver_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/server"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// hookRecorder is a thread-safe collector for OnActiveSessionChanged
// invocations. Tests use it to assert the orchestrator calls the hook
// at the right times with the right argument.
type hookRecorder struct {
	mu  sync.Mutex
	got []string
}

func (h *hookRecorder) hook(projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, projectID)
}

func (h *hookRecorder) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.got))
	copy(out, h.got)
	return out
}

// openCacheDB creates a fresh SQLite-backed Store with the "default"
// project seeded + active, ready for SessionStart / Close.
func openCacheDB(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "cache.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("create default: %v", err)
	}
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set active project: %v", err)
	}
	return s
}

// TestSessionStart_InvokesOnActiveSessionChanged verifies the v2.1.3
// hook fires after SetActiveSession so external caches can flush.
func TestSessionStart_InvokesOnActiveSessionChanged(t *testing.T) {
	st := openCacheDB(t)
	orch := orchestration.New(st, nil)
	rec := &hookRecorder{}
	orch.OnActiveSessionChanged = rec.hook

	out, err := orch.SessionStart(context.Background(), orchestration.SessionStartInput{
		Operator:  "op-test",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if out.SessionID == "" {
		t.Fatal("SessionStart returned empty session_id")
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 hook call, got %d: %v", len(calls), calls)
	}
	if calls[0] != "default" {
		t.Errorf("hook called with %q, want %q", calls[0], "default")
	}
}

// TestSessionClose_InvokesOnActiveSessionChanged verifies the v2.1.3
// hook also fires after ClearActiveSession so a session_start within
// 5s doesn't see the just-closed id cached.
func TestSessionClose_InvokesOnActiveSessionChanged(t *testing.T) {
	st := openCacheDB(t)
	orch := orchestration.New(st, nil)
	rec := &hookRecorder{}
	orch.OnActiveSessionChanged = rec.hook

	out, err := orch.SessionStart(context.Background(), orchestration.SessionStartInput{
		Operator:  "op-test",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// reset recorder so we only count the close
	rec.mu.Lock()
	rec.got = nil
	rec.mu.Unlock()

	_, err = orch.SessionClose(context.Background(), orchestration.SessionCloseInput{
		SessionID: out.SessionID,
	})
	if err != nil {
		t.Fatalf("SessionClose: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 hook call from close, got %d: %v", len(calls), calls)
	}
	if calls[0] != "default" {
		t.Errorf("hook called with %q, want %q", calls[0], "default")
	}
}

// TestSessionStart_NilHookIsSafe: production wiring in main.go sets
// the hook, but tests + alternative harnesses should not crash if
// it's left nil.
func TestSessionStart_NilHookIsSafe(t *testing.T) {
	st := openCacheDB(t)
	orch := orchestration.New(st, nil)
	// intentionally do NOT set OnActiveSessionChanged

	out, err := orch.SessionStart(context.Background(), orchestration.SessionStartInput{
		Operator:  "op-test",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart with nil hook: %v", err)
	}
	if out.SessionID == "" {
		t.Fatal("nil hook broke SessionStart output")
	}
}

// TestResolverCacheInvalidatedAfterSessionStart is the regression test
// for the original bug. It wires a real
// StoreBackedActiveSessionResolver + the orchestrator hook, then
// reproduces the exact production race:
//
//  1. Look up the active session BEFORE SessionStart → DB returns ""
//     (no active session yet). Cache filled with "".
//  2. Call SessionStart. Hook fires → resolver.Invalidate("default")
//     flushes the cache.
//  3. Look up again. Without the hook, this would return "" (cache
//     hit on stale value). With the hook, the cache is empty so we
//     hit the DB and get the new session_id.
//
// Pre-fix behavior (hook NOT wired): step 3 returns "" — same as the
// live-MCP failure. Post-fix: step 3 returns the new session_id.
func TestResolverCacheInvalidatedAfterSessionStart(t *testing.T) {
	st := openCacheDB(t)

	resolver := server.NewStoreBackedActiveSessionResolver(
		server.StoreBackedLookup(st),
	)
	orch := orchestration.New(st, nil)
	orch.OnActiveSessionChanged = resolver.Invalidate

	ctx := context.Background()

	// Step 1: pre-warm the cache with the empty (no-active-session) value.
	if got := resolver.ActiveSessionID(ctx, "default"); got != "" {
		t.Fatalf("pre-warm: expected empty, got %q", got)
	}

	// Step 2: session_start. Inner calls SetActiveSession + hook.
	out, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "op-test",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}

	// Step 3: post-session_start lookup. With the hook wired, the cache
	// was flushed → fresh DB lookup → returns the new session_id.
	// Without the hook, this would return "" (stale cache hit).
	got := resolver.ActiveSessionID(ctx, "default")
	if got != out.SessionID {
		t.Errorf("post-session_start lookup: got %q, want %q (cache stale? hook not wired?)",
			got, out.SessionID)
	}
}

// TestResolverCacheInvalidatedAfterSessionClose mirrors the close-side
// race: if the orchestrator's hook doesn't fire on close, a subsequent
// session_start (within 5s) would buildGateInput against the just-closed
// id cached, get the right value, but it's the WRONG session — close
// already invalidated it.
func TestResolverCacheInvalidatedAfterSessionClose(t *testing.T) {
	st := openCacheDB(t)

	resolver := server.NewStoreBackedActiveSessionResolver(
		server.StoreBackedLookup(st),
	)
	orch := orchestration.New(st, nil)
	orch.OnActiveSessionChanged = resolver.Invalidate

	ctx := context.Background()

	// Open a session, let the cache populate via a lookup.
	sessOut, err := orch.SessionStart(ctx, orchestration.SessionStartInput{
		Operator:  "op-test",
		ProjectID: "default",
	})
	if err != nil {
		t.Fatalf("SessionStart: %v", err)
	}
	if got := resolver.ActiveSessionID(ctx, "default"); got != sessOut.SessionID {
		t.Fatalf("post-start lookup: got %q, want %q", got, sessOut.SessionID)
	}

	// Close it. Hook fires → resolver.Invalidate.
	if _, err := orch.SessionClose(ctx, orchestration.SessionCloseInput{
		SessionID: sessOut.SessionID,
	}); err != nil {
		t.Fatalf("SessionClose: %v", err)
	}

	// Post-close lookup. DB has active_session_id=NULL. Without hook
	// firing, the cache would still return sessOut.SessionID (stale).
	// With hook firing, fresh DB lookup returns "".
	got := resolver.ActiveSessionID(ctx, "default")
	if got != "" {
		t.Errorf("post-close lookup: got %q, want empty (cache stale? hook not wired?)", got)
	}
}
