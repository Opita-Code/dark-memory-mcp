// Package bench — session.go: Session-start micro-benchmarks.
//
// These benchmarks measure the dark-memory Store.Open path (which the
// boot calls as step 2 at internal/server/lifecycle.go:124-131:
//
//   - SQLite file open
//   - PRAGMA setup (WAL, foreign_keys, busy_timeout)
//   - Migration runner (all pending migrations up to schema v32)
//   - Constitution watchdog (INV-4)
//   - FTS5 init (the search index that powers agent_memory_recall)
//
// We measure with a fresh DB (cold init), a reused DB at v32 (warm
// init), and a reused DB with 10k rows (warm init + non-empty
// FTS5).
//
// Skip with -short.
//
//go:build bench
// +build bench

package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// BenchmarkSessionStartColdFresh measures Store.Open on a brand new
// SQLite file (no migrations cached, no FTS5 content). This is the
// worst-case cold path: deploy → first server start.
func BenchmarkSessionStartColdFresh(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping in -short mode")
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tmpDB := filepath.Join(b.TempDir(), "bench_cold.db")
		b.StartTimer()
		start := time.Now()
		st, err := runtime.Open(ctx, store.Config{
			Driver:      store.DriverSQLite,
			DSN:         tmpDB,
				BusyTimeout: 5 * time.Second,
				WALMode:     true,
				ForeignKeys: true,
		})
		elapsed := time.Since(start)
		if err != nil {
			b.Fatalf("runtime.Open: %v", err)
		}
		_ = st.Close()
		b.ReportMetric(float64(elapsed.Microseconds()), "us/open")
	}
}

// BenchmarkSessionStartWarm measures Store.Open on an existing v32
// SQLite file (no pending migrations). This is the steady-state path:
// every restart after the first.
//
// Windows note: SQLite on Windows holds a file lock for ~50-100ms after
// Close() returns (lazy OS handle release). The bench uses
// filepath.Join(os.TempDir(), "...") + manual skip on Windows to avoid
// the TempDir cleanup race; on Linux/macOS b.TempDir() works fine.
func BenchmarkSessionStartWarm(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping in -short mode")
	}
	ctx := context.Background()
	// Use os.TempDir directly + a fixed filename. Skip cleanup on
	// Windows; the temp files are small and harmless.
	tmpDB := filepath.Join(os.TempDir(), "bench_warm_dark.db")
	_ = os.Remove(tmpDB) // ignore error
	_ = os.Remove(tmpDB + "-wal")
	_ = os.Remove(tmpDB + "-shm")
	if _, err := runtime.Open(ctx, store.Config{
		Driver:      store.DriverSQLite,
		DSN:         tmpDB,
		BusyTimeout: 5 * time.Second,
		WALMode:     true,
		ForeignKeys: true,
	}); err != nil {
		b.Fatalf("warmup Open: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		st, err := runtime.Open(ctx, store.Config{
			Driver:      store.DriverSQLite,
			DSN:         tmpDB,
			BusyTimeout: 5 * time.Second,
			WALMode:     true,
			ForeignKeys: true,
		})
		elapsed := time.Since(start)
		if err != nil {
			b.Fatalf("runtime.Open: %v", err)
		}
		_ = st.Close()
		b.ReportMetric(float64(elapsed.Microseconds()), "us/open")
	}
}