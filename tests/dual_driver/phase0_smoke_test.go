// Package dual_driver_test — phase0_smoke_test.go: B2-D Phase 0 smoke
// test. Boots the Postgres store implementation against a live
// PostgreSQL instance reachable via DARK_TEST_POSTGRES_DSN, verifies
// the boot path succeeds (Ping + migrate + watchdog), and asserts
// the schema version is at v32 (matches SQLite parity per the freeze
// "Frozen_78_21_28"). Does NOT exercise any Save*/Get* stubs — those
// come in subsequent phases as the 54 notImpl closures in
// internal/store/postgres/store.go are filled with real pgx-backed
// implementations.
//
// Gating: skipped when DARK_TEST_POSTGRES_DSN is unset (mirrors the
// other dual_driver tests). On CI, the new "postgres-smoke" job
// (added in B2-D Phase 0 to .github/workflows/ci.yml) sets this env
// var explicitly after spinning up a postgres:16-alpine service.
//
// Pin lockstep UNCHANGED by this commit: only a test file is added;
// the Store implementation and ci.yml are the other two diffs that
// together land the Phase 0 cimiento.
package dual_driver_test

import (
	"context"
	"os"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// TestPhase0_Smoke_PostgresBoot verifies that the Postgres Store
// implementation boots end-to-end against a live pg container: Open
// (parse DSN → pgxpool → Ping), runMigrations (v32 schema lands),
// watchdog (constitution registry initialized), and Close cleanly
// tears down the pool.
//
// What this test does NOT do:
//   - Save / Get / Update / Delete any row (most of these are notImpl
//     stubs as of 2026-10-15; the operator chose option D in the B2
//     menu — implementation across phases 1-4 — over the soft/hard
//     decommission options).
//   - Cross-driver parity comparison against SQLite. The pg-specific
//     assertions here use only methods that are already implemented
//     on Postgres (DriverName, Ping, Migrate, SchemaVersion,
//     MigrationStatus, Close).
//
// Failure modes this test catches:
//   - Postgres service not reachable (CI service health check failed,
//     DNS misconfig, port mismatch in services block).
//   - runMigrations panics on a v32 migration (DDL syntax error in
//     internal/migrate/postgres/ddl.go).
//   - pgxpool config incompatible (e.g. MaxConns > pg max_connections).
//   - watchdog invokes a method that's notImpl on Postgres (would
//     surface as the post-Open SchemaVersion check).
func TestPhase0_Smoke_PostgresBoot(t *testing.T) {
	dsn := os.Getenv("DARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DARK_TEST_POSTGRES_DSN not set; skipping postgres phase0 smoke (this test is CI-only)")
	}

	ctx := context.Background()
	cfg := store.Config{
		Driver: store.DriverPostgres,
		DSN:    dsn,
	}

	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("postgres Open: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("postgres Close: %v", err)
		}
	}()

	if got := s.DriverName(); got != "postgres" {
		t.Errorf("DriverName: got %q want %q", got, "postgres")
	}

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Migrate is idempotent (re-applying pending migrations is a no-op
	// once the schema_version row exists). Calling it twice on the
	// same db is safe and validates the runMigrations path is
	// re-entrant.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	// The freeze "Frozen_78_21_28" fixes the canonical surface to
	// 78 tools / 21 namespaces / schema 32. Postgres parity means
	// the pg schema_version must match SQLite's at the same freeze.
	if v < 32 {
		t.Errorf("SchemaVersion: got %d want >= 32 (freeze Frozen_78_21_28)", v)
	}

	status, err := s.MigrationStatus(ctx)
	if err != nil {
		t.Fatalf("MigrationStatus: %v", err)
	}
	if len(status) < 32 {
		t.Errorf("MigrationStatus: got %d migration rows want >= 32", len(status))
	}

	t.Logf("postgres phase0 smoke OK: schema v%d, %d migration rows applied", v, len(status))
}
