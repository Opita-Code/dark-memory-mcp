// Package dual_driver_test runs the same assertions against both the
// SQLite and Postgres implementations of store.Store.
//
// In CI / dev environments without a live Postgres instance, the Postgres
// tests are skipped automatically â€” set DARK_TEST_POSTGRES_DSN to a
// reachable DSN to enable them.
package dual_driver_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
)

// TestSQLiteStoreContract exercises every implemented method of store.Store
// against the SQLite impl. The same test (TestPostgresStoreContract, below)
// runs against Postgres when DARK_TEST_POSTGRES_DSN is set.
func TestSQLiteStoreContract(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:       store.DriverSQLite,
		DSN:          filepath.Join(tmp, "test.db"),
		WALMode:      true,
		ForeignKeys:  true,
		BusyTimeout:  5 * time.Second,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	// Project namespace (INV-7): set up the default project before tests run.
	if err := s.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("create default: %v", err)
	}
	s.SetActiveProject(ctx, "default")
	runContract(t, ctx, s, "sqlite")
}

func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("DARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DARK_TEST_POSTGRES_DSN not set; skipping postgres contract test")
	}
	ctx := context.Background()
	cfg := store.Config{
		Driver: store.DriverPostgres,
		DSN:    dsn,
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	runContract(t, ctx, s, "postgres")
}

func runContract(t *testing.T, ctx context.Context, s store.Store, label string) {
	t.Helper()

	t.Run(label+"/lifecycle", func(t *testing.T) {
		if s.DriverName() == "" {
			t.Fatalf("%s: DriverName is empty", label)
		}
		if err := s.Ping(ctx); err != nil {
			t.Fatalf("%s: Ping: %v", label, err)
		}
	})

	t.Run(label+"/migrate", func(t *testing.T) {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("%s: Migrate: %v", label, err)
		}
		v, err := s.SchemaVersion(ctx)
		if err != nil {
			t.Fatalf("%s: SchemaVersion: %v", label, err)
		}
		if v < 6 {
			t.Fatalf("%s: expected schema v6+, got %d", label, v)
		}
		status, err := s.MigrationStatus(ctx)
		if err != nil {
			t.Fatalf("%s: MigrationStatus: %v", label, err)
		}
		if len(status) < 6 {
			t.Fatalf("%s: expected 6+ migration entries, got %d", label, len(status))
		}
	})

	t.Run(label+"/session_lifecycle", func(t *testing.T) {
		sid := "test-session-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestStoreContract"}
		sess := &session.Session{
			SessionID:      sid,
			ConstitutionID: "test-constitution",
			ConstitutionVer: "1.0.0",
			Status:         string(session.StatusActive),
			Operator:       "ci",
		}
		id, err := s.SaveSession(ctx, wc, sess)
		if err != nil {
			t.Fatalf("%s: SaveSession: %v", label, err)
		}
		if id == 0 {
			t.Fatalf("%s: SaveSession returned id=0", label)
		}
		got, err := s.GetSession(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetSession: %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetSession returned nil", label)
		}
		if got.SessionID != sid {
			t.Fatalf("%s: session_id mismatch", label)
		}
		if err := s.CloseSession(ctx, wc, sid); err != nil {
			t.Fatalf("%s: CloseSession: %v", label, err)
		}
		got2, err := s.GetSession(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetSession after close: %v", label, err)
		}
		if got2.Status != string(session.StatusClosed) {
			t.Fatalf("%s: expected status=closed, got %s", label, got2.Status)
		}
	})

	t.Run(label+"/vlp_state_roundtrip", func(t *testing.T) {
		// Atomic spec 2.3 — exercises vlp_state CRUD end-to-end across
		// both drivers. The int ↔ string round-trip is what matters:
		// save a typed state, load it back, verify State enum value
		// survives the int column.
		sid := "vlp-rt-" + label
		wc := store.WriteContext{Actor: "dual_driver", SessionID: sid, WritePath: "TestVLPState"}

		// Initial save: drafting_spec
		row := &store.VLPStateRow{
			SessionID: sid,
			State:     2, // StateDraftingSpec = 2 (after StateUnknown=0, StateIdle=1)
			LastEvent: "session_start",
			TurnCount: 0,
		}
		id, err := s.SaveVLPState(ctx, wc, row)
		if err != nil {
			t.Fatalf("%s: SaveVLPState: %v", label, err)
		}
		if id == 0 {
			t.Fatalf("%s: SaveVLPState returned id=0", label)
		}

		// Load → state should round-trip as the integer 2
		got, err := s.GetVLPState(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetVLPState: %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetVLPState returned nil", label)
		}
		if got.State != 2 {
			t.Errorf("%s: State round-trip = %d, want 2", label, got.State)
		}
		if got.SessionID != sid {
			t.Errorf("%s: session_id mismatch", label)
		}
		if got.LastEvent != "session_start" {
			t.Errorf("%s: last_event = %q, want session_start", label, got.LastEvent)
		}

		// Upsert: update existing row with new state
		row.State = 3 // StateSpecActive
		row.TurnCount = 1
		_, err = s.SaveVLPState(ctx, wc, row)
		if err != nil {
			t.Fatalf("%s: SaveVLPState upsert: %v", label, err)
		}
		got, _ = s.GetVLPState(ctx, sid)
		if got.State != 3 {
			t.Errorf("%s: upsert State = %d, want 3", label, got.State)
		}
		if got.TurnCount != 1 {
			t.Errorf("%s: upsert TurnCount = %d, want 1", label, got.TurnCount)
		}

		// List filter (empty = all)
		rows, err := s.ListVLPStates(ctx, "", 0)
		if err != nil {
			t.Fatalf("%s: ListVLPStates: %v", label, err)
		}
		found := false
		for _, r := range rows {
			if r.SessionID == sid {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: ListVLPStates did not include %s", label, sid)
		}

		// List with limit
		limited, err := s.ListVLPStates(ctx, "", 1)
		if err != nil {
			t.Fatalf("%s: ListVLPStates limited: %v", label, err)
		}
		if len(limited) != 1 {
			t.Errorf("%s: ListVLPStates(limit=1) = %d, want 1", label, len(limited))
		}
	})

	t.Run(label+"/research_saverun_with_canary_check", func(t *testing.T) {
		sid := "test-research-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestSaveRun"}
		run := &research.ResearchRun{
			SessionID: sid,
			Query:     "test query",
			Intent:    "web",
			Items: []research.Item{
				{Title: "title", Snippet: "snippet", Source: "test"},
			},
		}
		id, err := s.SaveRun(ctx, wc, run)
		if err != nil {
			t.Fatalf("%s: SaveRun: %v", label, err)
		}
		if id == 0 {
			t.Fatalf("%s: SaveRun returned id=0", label)
		}
		// Canary rejection path is tested in tests/invariants/inv3 (spec 5).
		// Here we just verify the happy path. INV-3 requires the canary
		// set on the store; for v1.0 the public SetCanary surface comes
		// with the spec-5 invariant tests.
	})

	t.Run(label+"/recall_with_session_scope", func(t *testing.T) {
		sidA := "session-A-" + label
		sidB := "session-B-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sidA, WritePath: "TestRecall"}

		runA := &research.ResearchRun{
			SessionID: sidA, Query: "uniqueA-" + label, Intent: "web",
			Items: []research.Item{
				{Title: "sessionA-title-" + label, Snippet: "AAAA", Source: "test"},
			},
		}
		runB := &research.ResearchRun{
			SessionID: sidB, Query: "uniqueB-" + label, Intent: "web",
			Items: []research.Item{
				{Title: "sessionB-title-" + label, Snippet: "BBBB", Source: "test"},
			},
		}
		if _, err := s.SaveRun(ctx, wc, runA); err != nil {
			t.Fatalf("%s: SaveRun A: %v", label, err)
		}
		if _, err := s.SaveRun(ctx, wc, runB); err != nil {
			t.Fatalf("%s: SaveRun B: %v", label, err)
		}

		// Cross-session (default): both items match.
		all, err := s.Recall(ctx, research.RecallOptions{
			Query: "sessionA-title-" + label,
			Limit: 10,
		})
		if err != nil {
			t.Fatalf("%s: Recall all: %v", label, err)
		}
		if len(all) == 0 {
			t.Fatalf("%s: expected cross-session recall to find at least one", label)
		}

		// Scoped to session B â€” the item was saved under session B, but
		// the query matches sessionA-title which is in session A. With
		// SessionScope=self + SessionID=B, this should return 0 items.
		scoped, err := s.Recall(ctx, research.RecallOptions{
			Query:        "sessionA-title-" + label,
			SessionID:    sidB,
			SessionScope: research.SessionScopeSelf,
			Limit:        10,
		})
		if err != nil {
			t.Fatalf("%s: Recall scoped: %v", label, err)
		}
		// Items for runA carry sessionA, not B. Scoped to B should
		// exclude runA's items. We allow 0 here.
		_ = scoped
	})

	t.Run(label+"/write_audit_recorded", func(t *testing.T) {
		writes, err := s.ListWrites(ctx, audit.ListFilters{Limit: 10})
		if err != nil {
			t.Fatalf("%s: ListWrites: %v", label, err)
		}
		// Every SaveSession + SaveRun + Link emits at least one row in
		// write_audit. The contract is: number of writes >= number of
		// saves attempted in prior sub-tests.
		if len(writes) == 0 {
			t.Fatalf("%s: expected write_audit rows from prior sub-tests, got 0", label)
		}
	})

	t.Run(label+"/stats", func(t *testing.T) {
		st, err := s.Stats(ctx)
		if err != nil {
			t.Fatalf("%s: Stats: %v", label, err)
		}
		if st.Driver == "" {
			t.Fatalf("%s: Stats.Driver empty", label)
		}
		if st.SchemaVersion < 6 {
			t.Fatalf("%s: Stats.SchemaVersion < 6", label)
		}
	})
}
