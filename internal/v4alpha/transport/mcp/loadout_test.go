package mcp_test

// PRE-1 C3 tests for the Loadout builder.
//
// Eight tests cover the contract:
//   1. TestBuildLoadout_EmptyOperator   — fresh operator, no rows,
//      no errors. All fields present, all slices empty.
//   2. TestBuildLoadout_PinnedRows      — pinned rows surface,
//      unpinned do not.
//   3. TestBuildLoadout_OpenTodos       — kind=todo rows surface,
//      other kinds do not.
//   4. TestBuildLoadout_RecentWrites    — audit_log rows surface
//      (latest 20 only).
//   5. TestBuildLoadout_PartialFailure_PinnedRows — Save fails,
//      pinned_rows empty + warning emitted.
//   6. TestBuildLoadout_Constitution    — ConstitutionInfo shape
//      is populated.
//   7. TestBuildLoadout_SchemaVersion   — schema_migrations
//      version is read.
//   8. TestBuildLoadout_ServerNow       — server_now is recent.
//
// All tests use a LoadoutBuilder with stub schema/constit
// sources so they're independent of the live DB schema state.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	am "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
	mcppkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/transport/mcp"
)

// loadoutEnv wires up audit + agent_memory + a shared *sql.DB.
// The DB also has schema_migrations applied (default empty
// state) so the loadout's schema query works.
type loadoutEnv struct {
	DB      *sql.DB
	AmStore *am.Store
}

// newLoadoutEnv builds a fresh in-memory environment.
func newLoadoutEnv(t *testing.T) *loadoutEnv {
	t.Helper()
	ctx := context.Background()

	db, err := store.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("store.OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := audit.CreateSchema(db); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := am.CreateSchema(db); err != nil {
		t.Fatalf("am.CreateSchema: %v", err)
	}
	// Create the schema_migrations table (empty by design).
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	w := audit.NewWriter(db)
	return &loadoutEnv{
		DB:      db,
		AmStore: am.NewStore(db, w),
	}
}

// newLoadoutBuilder returns a builder with stub schema/constit
// sources. The stubs return canned values so the tests don't
// depend on the live schema or constitution globals.
func newLoadoutBuilder(env *loadoutEnv) *mcppkg.LoadoutBuilder {
	b := mcppkg.NewLoadoutBuilder(env.AmStore, env.DB)
	b.SetSchemaFn(func(_ context.Context, _ *sql.DB) (string, error) {
		return "v4alpha/test/000", nil
	})
	b.SetConstitFn(func() *mcppkg.ConstitutionInfo {
		return &mcppkg.ConstitutionInfo{
			ID:         "test-constitution",
			Version:    "v0",
			ActiveMods: 0,
		}
	})
	return b
}

// TestBuildLoadout_EmptyOperator — fresh operator, no rows,
// no errors. All fields present, slices empty, constitution
// populated, server_now non-empty.
func TestBuildLoadout_EmptyOperator(t *testing.T) {
	env := newLoadoutEnv(t)
	b := newLoadoutBuilder(env)

	loadout, warnings, err := b.Build(context.Background(), "operator-fresh")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if loadout == nil {
		t.Fatal("loadout is nil")
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: expected 0, got %d (%v)", len(warnings), warnings)
	}
	if len(loadout.PinnedRows) != 0 {
		t.Errorf("PinnedRows: expected 0, got %d", len(loadout.PinnedRows))
	}
	if len(loadout.OpenTodos) != 0 {
		t.Errorf("OpenTodos: expected 0, got %d", len(loadout.OpenTodos))
	}
	if len(loadout.RecentWrites) != 0 {
		t.Errorf("RecentWrites: expected 0, got %d", len(loadout.RecentWrites))
	}
	if loadout.Constitution == nil {
		t.Error("Constitution: expected non-nil")
	} else if loadout.Constitution.ID != "test-constitution" {
		t.Errorf("Constitution.ID: expected 'test-constitution', got %q", loadout.Constitution.ID)
	}
	if loadout.SchemaVersion != "v4alpha/test/000" {
		t.Errorf("SchemaVersion: expected 'v4alpha/test/000', got %q", loadout.SchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, loadout.ServerNow); err != nil {
		t.Errorf("ServerNow: not RFC3339 (%q): %v", loadout.ServerNow, err)
	}
}

// TestBuildLoadout_PinnedRows — 5 pinned + 2 unpinned.
// Only the 5 pinned surface.
func TestBuildLoadout_PinnedRows(t *testing.T) {
	env := newLoadoutEnv(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
			"operator-nico", "link", "pinned doc",
			"content", "doc:test", true); err != nil {
			t.Fatalf("Save pinned: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
			"operator-nico", "link", "unpinned",
			"content", "doc:test", false); err != nil {
			t.Fatalf("Save unpinned: %v", err)
		}
	}

	b := newLoadoutBuilder(env)
	loadout, _, err := b.Build(ctx, "operator-nico")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(loadout.PinnedRows) != 5 {
		t.Errorf("PinnedRows: expected 5, got %d", len(loadout.PinnedRows))
	}
}

// TestBuildLoadout_OpenTodos — 3 todos + 2 notes + 1 decision.
// Only the 3 todos surface.
func TestBuildLoadout_OpenTodos(t *testing.T) {
	env := newLoadoutEnv(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
			"operator-nico", "todo", "todo item",
			"content", "todo:test", false); err != nil {
			t.Fatalf("Save todo: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
			"operator-nico", "note", "note item",
			"content", "note:test", false); err != nil {
			t.Fatalf("Save note: %v", err)
		}
	}
	if _, err := env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
		"operator-nico", "decision", "decision item",
		"content", "decision:test", false); err != nil {
		t.Fatalf("Save decision: %v", err)
	}

	b := newLoadoutBuilder(env)
	loadout, _, err := b.Build(ctx, "operator-nico")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(loadout.OpenTodos) != 3 {
		t.Errorf("OpenTodos: expected 3, got %d", len(loadout.OpenTodos))
	}
	for _, r := range loadout.OpenTodos {
		if r.Kind != "todo" {
			t.Errorf("OpenTodos: row kind=%q, expected 'todo'", r.Kind)
		}
	}
}

// TestBuildLoadout_RecentWrites — 25 audit_log rows; only the
// latest 20 surface. Verify descending order.
func TestBuildLoadout_RecentWrites(t *testing.T) {
	env := newLoadoutEnv(t)
	ctx := context.Background()

	// Write 25 audit_log rows directly (am.Save would also emit
	// audit rows, but this gives us exact control over the count).
	for i := 0; i < 25; i++ {
		if _, err := env.DB.ExecContext(ctx, `
			INSERT INTO audit_log (actor, payload)
			VALUES (?, ?)`,
			"operator-nico", `{"event":"test","n":`+itoa(i)+`}`); err != nil {
			t.Fatalf("insert audit %d: %v", i, err)
		}
	}

	b := newLoadoutBuilder(env)
	loadout, _, err := b.Build(ctx, "operator-nico")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(loadout.RecentWrites) != 20 {
		t.Errorf("RecentWrites: expected 20, got %d", len(loadout.RecentWrites))
	}
	// Verify descending order: latest audit_id first.
	for i := 1; i < len(loadout.RecentWrites); i++ {
		if loadout.RecentWrites[i-1].AuditID <= loadout.RecentWrites[i].AuditID {
			t.Errorf("RecentWrites: not descending at i=%d (prev=%d, curr=%d)",
				i, loadout.RecentWrites[i-1].AuditID, loadout.RecentWrites[i].AuditID)
		}
	}
}

// TestBuildLoadout_PartialFailure_PinnedRows — force the pinned
// query to fail by closing the DB first. Loadout returns empty
// pinned_rows + a warning; other fields still populate.
func TestBuildLoadout_PartialFailure_PinnedRows(t *testing.T) {
	env := newLoadoutEnv(t)
	ctx := context.Background()

	// Write a real pinned row so the unfiltered case would
	// succeed; we'll close the db to force failure on the
	// second Build call.
	if _, err := env.AmStore.Save(ctx, &am.Audit{Actor: "operator-nico"},
		"operator-nico", "link", "pinned doc",
		"content", "doc:test", true); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b := newLoadoutBuilder(env)
	// Force pinned query to fail by closing the DB. The other
	// queries use the same DB so they will ALSO fail. That's
	// fine: we want to verify the partial-failure contract on
	// at least one field.
	_ = env.DB.Close()

	loadout, warnings, err := b.Build(ctx, "operator-nico")
	if err != nil {
		t.Fatalf("Build should not error on partial failure: %v", err)
	}
	if loadout == nil {
		t.Fatal("loadout is nil despite partial-failure tolerance")
	}
	if len(warnings) == 0 {
		t.Error("warnings: expected >= 1, got 0")
	}
	// All affected fields should be empty.
	if len(loadout.PinnedRows) != 0 {
		t.Errorf("PinnedRows: expected 0 on failure, got %d", len(loadout.PinnedRows))
	}
	if len(loadout.OpenTodos) != 0 {
		t.Errorf("OpenTodos: expected 0 on failure, got %d", len(loadout.OpenTodos))
	}
	if len(loadout.RecentWrites) != 0 {
		t.Errorf("RecentWrites: expected 0 on failure, got %d", len(loadout.RecentWrites))
	}
	// server_now should still be populated (it's time-based).
	if loadout.ServerNow == "" {
		t.Error("ServerNow: expected non-empty even on partial failure")
	}
}

// TestBuildLoadout_Constitution — verify the ConstitutionInfo
// shape is populated from the stub source.
func TestBuildLoadout_Constitution(t *testing.T) {
	env := newLoadoutEnv(t)
	b := newLoadoutBuilder(env)

	loadout, _, err := b.Build(context.Background(), "operator-nico")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if loadout.Constitution == nil {
		t.Fatal("Constitution: nil")
	}
	if loadout.Constitution.ID != "test-constitution" {
		t.Errorf("ID: expected 'test-constitution', got %q", loadout.Constitution.ID)
	}
	if loadout.Constitution.Version != "v0" {
		t.Errorf("Version: expected 'v0', got %q", loadout.Constitution.Version)
	}
	if loadout.Constitution.ActiveMods != 0 {
		t.Errorf("ActiveMods: expected 0, got %d", loadout.Constitution.ActiveMods)
	}
}

// TestBuildLoadout_SchemaVersion — verify the schema source is
// invoked and the value is propagated.
func TestBuildLoadout_SchemaVersion(t *testing.T) {
	env := newLoadoutEnv(t)
	ctx := context.Background()

	// Insert a real schema_migrations row.
	if _, err := env.DB.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, applied_at) VALUES (?, CURRENT_TIMESTAMP)
	`, "v4alpha/real/123"); err != nil {
		t.Fatalf("insert migration: %v", err)
	}

	b := mcppkg.NewLoadoutBuilder(env.AmStore, env.DB)
	b.SetSchemaFn(mcppkg.DefaultSchemaVersion)
	b.SetConstitFn(func() *mcppkg.ConstitutionInfo {
		return &mcppkg.ConstitutionInfo{ID: "x", Version: "y"}
	})

	loadout, _, err := b.Build(ctx, "operator-nico")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if loadout.SchemaVersion != "v4alpha/real/123" {
		t.Errorf("SchemaVersion: expected 'v4alpha/real/123', got %q", loadout.SchemaVersion)
	}
}

// TestBuildLoadout_ServerNow — server_now is within 1s of now.
func TestBuildLoadout_ServerNow(t *testing.T) {
	env := newLoadoutEnv(t)
	b := newLoadoutBuilder(env)

	before := time.Now().UTC()
	loadout, _, err := b.Build(context.Background(), "operator-nico")
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := time.Parse(time.RFC3339, loadout.ServerNow)
	if err != nil {
		t.Fatalf("ServerNow not RFC3339: %v", err)
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("ServerNow out of range: got=%s, before=%s, after=%s",
			got, before, after)
	}
}

// TestBuildLoadout_SchemaFnError — schema source returns an
// error; loadout schema_version is empty + warning emitted.
func TestBuildLoadout_SchemaFnError(t *testing.T) {
	env := newLoadoutEnv(t)
	b := newLoadoutBuilder(env)
	b.SetSchemaFn(func(_ context.Context, _ *sql.DB) (string, error) {
		return "", errors.New("simulated schema failure")
	})

	loadout, warnings, err := b.Build(context.Background(), "operator-nico")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if loadout.SchemaVersion != "" {
		t.Errorf("SchemaVersion: expected empty on error, got %q", loadout.SchemaVersion)
	}
	if len(warnings) == 0 {
		t.Error("warnings: expected >= 1 for schema failure")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "schema_version") && strings.Contains(w, "simulated") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings: missing 'schema_version: simulated' message; got %v", warnings)
	}
}

// itoa is a tiny integer-to-string helper for SQL string
// concatenation (avoids pulling in strconv just for tests).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}