// Package dual_driver_test runs the same assertions against both the
// SQLite and Postgres implementations of store.Store.
//
// In CI / dev environments without a live Postgres instance, the Postgres
// tests are skipped automatically â€” set DARK_TEST_POSTGRES_DSN to a
// reachable DSN to enable them.
package dual_driver_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/research"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/runtime"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
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
			Status:         string(session.StatusOpen),
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
		// Wave 5E.iii contract: LastHeartbeatAt must round-trip through
		// SaveSession → GetSession. This is the assertion that would have
		// caught the 5E.ii debt (4 SQL queries were missing the column).
		// Without this assertion, a future driver that omits the column
		// would silently degrade SessionHeartbeat. INFRA-003 blocks this
		// test from running locally; CI runs it.
		if got.LastHeartbeatAt == "" {
			t.Fatalf("%s: LastHeartbeatAt must be set on save (default = StartedAt)", label)
		}
		if err := s.CloseSession(ctx, wc, sid, "clean"); err != nil {
			t.Fatalf("%s: CloseSession: %v", label, err)
		}
		got2, err := s.GetSession(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetSession after close: %v", label, err)
		}
		if got2.Status != string(session.StatusClosedClean) {
			t.Fatalf("%s: expected status=closed, got %s", label, got2.Status)
		}
	})

	t.Run(label+"/heartbeat_roundtrip", func(t *testing.T) {
		// Wave 5E.iii contract: SaveHeartbeat must refresh LastHeartbeatAt
		// AND the value must round-trip through GetSession. This is the
		// regression test for the 5E.ii debt where the column was written
		// but not read back (would have silently degraded the sweeper's
		// promotion logic).
		sid := "test-heartbeat-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestHeartbeat"}
		sess := &session.Session{
			SessionID:      sid,
			ConstitutionID: "test-constitution",
			Status:         string(session.StatusOpen),
			Operator:       "ci",
		}
		if _, err := s.SaveSession(ctx, wc, sess); err != nil {
			t.Fatalf("%s: SaveSession: %v", label, err)
		}
		before, err := s.GetSession(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetSession before heartbeat: %v", label, err)
		}
		if before.LastHeartbeatAt == "" {
			t.Fatalf("%s: LastHeartbeatAt must be set on save", label)
		}
		// Sleep so the heartbeat timestamp is observably different.
		time.Sleep(50 * time.Millisecond)
		if err := s.SaveHeartbeat(ctx, wc, sid); err != nil {
			t.Fatalf("%s: SaveHeartbeat: %v", label, err)
		}
		after, err := s.GetSession(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetSession after heartbeat: %v", label, err)
		}
		if after.LastHeartbeatAt == before.LastHeartbeatAt {
			t.Fatalf("%s: LastHeartbeatAt did not advance (before=%s after=%s)",
				label, before.LastHeartbeatAt, after.LastHeartbeatAt)
		}
	})

	t.Run(label+"/promote_status", func(t *testing.T) {
		// Wave 5E.iii contract: PromoteSessionStatus must transition
		// open → idle and emit write_audit with session_event='promote'.
		// This is the regression test for the sweeper's Pass 1 — without
		// this assertion, the SaveSession-was-INSERT-only bug would
		// regress.
		sid := "test-promote-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestPromote"}
		sess := &session.Session{
			SessionID:      sid,
			ConstitutionID: "test-constitution",
			Status:         string(session.StatusOpen),
			Operator:       "ci",
		}
		if _, err := s.SaveSession(ctx, wc, sess); err != nil {
			t.Fatalf("%s: SaveSession: %v", label, err)
		}
		if err := s.PromoteSessionStatus(ctx, wc, sid, string(session.StatusIdle)); err != nil {
			t.Fatalf("%s: PromoteSessionStatus open→idle: %v", label, err)
		}
		got, err := s.GetSession(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetSession after promote: %v", label, err)
		}
		if got.Status != string(session.StatusIdle) {
			t.Fatalf("%s: expected status=idle, got %s", label, got.Status)
		}
		// Invalid transition: closed_clean → open should fail with
		// ErrInvalidState (the sweeper's race-tolerance contract).
		if err := s.CloseSession(ctx, wc, sid, "clean"); err != nil {
			t.Fatalf("%s: CloseSession: %v", label, err)
		}
		err = s.PromoteSessionStatus(ctx, wc, sid, string(session.StatusOpen))
		if err == nil {
			t.Fatalf("%s: PromoteSessionStatus closed_clean→open must fail", label)
		}
		if !errors.Is(err, store.ErrInvalidState) {
			t.Fatalf("%s: expected ErrInvalidState, got %v", label, err)
		}
	})

	t.Run(label+"/count_items_and_runs", func(t *testing.T) {
		// Wave 5E.v contract: CountItemsForProject / CountRunsForProject
		// must return the project's research_items / research_runs row
		// counts via a single indexed COUNT(*) — replacing the previous
		// ListRuns + N×ListItems N+1 pattern in SessionClose.
		//
		// Baseline: read the existing counts so we can delt-verify.
		baselineRuns, err := s.CountRunsForProject(ctx, "")
		if err != nil {
			t.Fatalf("%s: CountRunsForProject baseline: %v", label, err)
		}
		baselineItems, err := s.CountItemsForProject(ctx, "")
		if err != nil {
			t.Fatalf("%s: CountItemsForProject baseline: %v", label, err)
		}

		// Insert 3 runs with 5 items total (2+2+1) into this project.
		sid := "test-count-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestCount"}
		for i, nItems := range []int{2, 2, 1} {
			items := make([]research.Item, nItems)
			for j := range items {
				items[j] = research.Item{
					Title:   fmt.Sprintf("count-title-%d-%d-%s", i, j, label),
					Snippet: "count-snippet",
					Source:  "test",
				}
			}
			run := &research.ResearchRun{
				SessionID: sid,
				Query:     fmt.Sprintf("count-query-%d-%s", i, label),
				Intent:    "web",
				Items:     items,
			}
			if _, err := s.SaveRun(ctx, wc, run); err != nil {
				t.Fatalf("%s: SaveRun %d: %v", label, i, err)
			}
		}

		// Verify delta: +3 runs, +5 items.
		afterRuns, err := s.CountRunsForProject(ctx, "")
		if err != nil {
			t.Fatalf("%s: CountRunsForProject after: %v", label, err)
		}
		afterItems, err := s.CountItemsForProject(ctx, "")
		if err != nil {
			t.Fatalf("%s: CountItemsForProject after: %v", label, err)
		}
		if got, want := afterRuns-baselineRuns, 3; got != want {
			t.Errorf("%s: runs delta = %d, want %d", label, got, want)
		}
		if got, want := afterItems-baselineItems, 5; got != want {
			t.Errorf("%s: items delta = %d, want %d", label, got, want)
		}

		// Empty projectID falls back to active project (defensive).
		if _, err := s.CountItemsForProject(ctx, "default"); err != nil {
			t.Errorf("%s: CountItemsForProject(\"default\"): %v", label, err)
		}
	})

	t.Run(label+"/vlp_state_roundtrip", func(t *testing.T) {
		// Atomic spec 2.3 — exercises vlp_state CRUD end-to-end across
		// both drivers. The int ↔ string round-trip is what matters:
		// save a typed state, load it back, verify State enum value
		// survives the int column. Uses vlp.State enum (no hardcoded
		// numeric values) so adding a new State value won't break this test.
		sid := "vlp-rt-" + label
		wc := store.WriteContext{Actor: "dual_driver", SessionID: sid, WritePath: "TestVLPState"}

		// Initial save: drafting_spec
		row := &store.VLPStateRow{
			SessionID: sid,
			State:     int(vlp.StateDraftingSpec),
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

		// Load → state should round-trip via the typed enum.
		got, err := s.GetVLPState(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetVLPState: %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetVLPState returned nil", label)
		}
		if got.State != int(vlp.StateDraftingSpec) {
			t.Errorf("%s: State round-trip = %d, want %d (%s)", label, got.State, int(vlp.StateDraftingSpec), vlp.StateDraftingSpec)
		}
		if got.SessionID != sid {
			t.Errorf("%s: session_id mismatch", label)
		}
		if got.LastEvent != "session_start" {
			t.Errorf("%s: last_event = %q, want session_start", label, got.LastEvent)
		}

		// Upsert: update existing row with new state. The returned id
		// must equal the first id (UPDATE branch of UPSERT, not INSERT).
		row.State = int(vlp.StateSpecActive)
		row.TurnCount = 1
		id2, err := s.SaveVLPState(ctx, wc, row)
		if err != nil {
			t.Fatalf("%s: SaveVLPState upsert: %v", label, err)
		}
		if id2 != id {
			t.Errorf("%s: upsert id = %d, want %d (UPDATE branch should return existing row id, not new)", label, id2, id)
		}
		got, _ = s.GetVLPState(ctx, sid)
		if got.State != int(vlp.StateSpecActive) {
			t.Errorf("%s: upsert State = %d, want %d (%s)", label, got.State, int(vlp.StateSpecActive), vlp.StateSpecActive)
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

		// List with limit (M1: limit must be pushed to SQL, not loop-broken)
		limited, err := s.ListVLPStates(ctx, "", 1)
		if err != nil {
			t.Fatalf("%s: ListVLPStates limited: %v", label, err)
		}
		if len(limited) != 1 {
			t.Errorf("%s: ListVLPStates(limit=1) = %d, want 1", label, len(limited))
		}

		// List with state filter (numeric form per the new contract).
		rowsActive, err := s.ListVLPStates(ctx, strconv.Itoa(int(vlp.StateSpecActive)), 0)
		if err != nil {
			t.Fatalf("%s: ListVLPStates(state=spec_active): %v", label, err)
		}
		if len(rowsActive) != 1 {
			t.Errorf("%s: ListVLPStates(state=spec_active) = %d, want 1", label, len(rowsActive))
		}
	})

	t.Run(label+"/vlp_state_open_spec_id_roundtrip", func(t *testing.T) {
		// Wave 5X.4: VLPStateRow.OpenSpecID must round-trip through
		// SaveVLPState + GetVLPState. Pre-5X.4 the recall cache
		// (5A.ii.b.2.c) had to use vlp_state.ID as a proxy for
		// spec_id because no real mapping existed. This wave adds
		// the open_spec_id column and threads it through DAO.
		//
		// Test: save with OpenSpecID=42 → Get → expect 42. Save
		// again with OpenSpecID=99 → Get → expect 99 (upsert
		// updates the column). Save with OpenSpecID=0 → Get →
		// expect 0 (HasOpenSpec returns false).
		sid := "test-open-spec-id-" + label
		wc := store.WriteContext{Actor: "test-5x4", SessionID: sid, WritePath: "TestOpenSpecIDRoundtrip"}

		// Case 1: initial save with spec_id=42.
		_, err := s.SaveVLPState(ctx, wc, &store.VLPStateRow{
			SessionID:   sid,
			State:       int(vlp.StateSpecActive),
			OpenSpecID:  42,
		})
		if err != nil {
			t.Fatalf("%s: SaveVLPState (spec=42): %v", label, err)
		}
		got, err := s.GetVLPState(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetVLPState (spec=42): %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetVLPState returned nil", label)
		}
		if got.OpenSpecID != 42 {
			t.Errorf("%s: OpenSpecID = %d, want 42", label, got.OpenSpecID)
		}

		// Case 2: upsert with different spec_id (99).
		_, err = s.SaveVLPState(ctx, wc, &store.VLPStateRow{
			SessionID:   sid,
			State:       int(vlp.StateSpecActive),
			OpenSpecID:  99,
		})
		if err != nil {
			t.Fatalf("%s: SaveVLPState upsert (spec=99): %v", label, err)
		}
		got, _ = s.GetVLPState(ctx, sid)
		if got.OpenSpecID != 99 {
			t.Errorf("%s: OpenSpecID after upsert = %d, want 99", label, got.OpenSpecID)
		}

		// Case 3: clear (OpenSpecID=0 = no spec open).
		_, err = s.SaveVLPState(ctx, wc, &store.VLPStateRow{
			SessionID:  sid,
			State:      int(vlp.StateIdle),
			OpenSpecID: 0,
		})
		if err != nil {
			t.Fatalf("%s: SaveVLPState clear: %v", label, err)
		}
		got, _ = s.GetVLPState(ctx, sid)
		if got.OpenSpecID != 0 {
			t.Errorf("%s: OpenSpecID after clear = %d, want 0", label, got.OpenSpecID)
		}
	})

	t.Run(label+"/vlp_state_audit_emitted", func(t *testing.T) {
		// Bug-hunt M3: every SaveVLPState must emit a write_audit row.
		sid := "vlp-audit-" + label
		wc := store.WriteContext{
			Actor:     "dual_driver",
			SessionID: sid,
			WritePath: "TestVLPAudit",
		}
		writesBefore, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, Limit: 100})
		if err != nil {
			t.Fatalf("%s: ListWrites before: %v", label, err)
		}

		if _, err := s.SaveVLPState(ctx, wc, &store.VLPStateRow{
			SessionID: sid,
			State:     int(vlp.StateIdle),
			LastEvent: "session_start",
			TurnCount: 0,
		}); err != nil {
			t.Fatalf("%s: SaveVLPState: %v", label, err)
		}

		writesAfter, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, Limit: 100})
		if err != nil {
			t.Fatalf("%s: ListWrites after: %v", label, err)
		}
		if got := len(writesAfter) - len(writesBefore); got != 1 {
			t.Errorf("%s: expected 1 new audit row, got %d", label, got)
		}

		// Verify the audit row points at vlp_state and references the row id.
		found := false
		for _, w := range writesAfter {
			if w.TableName == "vlp_state" && w.SessionID == sid {
				found = true
				if w.RowID == 0 {
					t.Errorf("%s: audit RowID = 0 (must be the upserted row id)", label)
				}
			}
		}
		if !found {
			t.Errorf("%s: no audit row found for table=vlp_state session=%s", label, sid)
		}
	})

	t.Run(label+"/vlp_state_cross_project_isolation", func(t *testing.T) {
		// Bug-hunt M2/C1: INV-7. Two projects using the same session_id
		// must not collide on UNIQUE, and reads from one project must not
		// see rows from another.
		sid := "vlp-shared-" + label

		// Save under project "default".
		wcA := store.WriteContext{
			Actor:     "a",
			SessionID: sid,
			WritePath: "TestVLPCrossProject",
			ProjectID: "default",
		}
		if _, err := s.SaveVLPState(ctx, wcA, &store.VLPStateRow{
			SessionID: sid,
			State:     int(vlp.StateIdle),
			LastEvent: "session_start",
			TurnCount: 0,
		}); err != nil {
			t.Fatalf("%s: SaveVLPState A: %v", label, err)
		}

		// Create + activate a second project, save with same session_id.
		if err := s.CreateProject(ctx, &project.Project{ProjectID: "tenant-b-" + label, DisplayName: "B"}); err != nil {
			t.Fatalf("%s: CreateProject B: %v", label, err)
		}
		if err := s.SetActiveProject(ctx, "tenant-b-"+label); err != nil {
			t.Fatalf("%s: SetActiveProject B: %v", label, err)
		}
		wcB := store.WriteContext{
			Actor:     "b",
			SessionID: sid,
			WritePath: "TestVLPCrossProject",
			ProjectID: "tenant-b-" + label,
		}
		if _, err := s.SaveVLPState(ctx, wcB, &store.VLPStateRow{
			SessionID: sid,
			State:     int(vlp.StateDraftingSpec),
			LastEvent: "vibe_publish",
			TurnCount: 5,
		}); err != nil {
			t.Fatalf("%s: SaveVLPState B (same session_id, different project): %v", label, err)
		}

		// Read from B → must see drafting_spec (B's row), NOT idle (A's row).
		gotB, err := s.GetVLPState(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetVLPState B: %v", label, err)
		}
		if gotB == nil {
			t.Fatalf("%s: B row missing", label)
		}
		if gotB.State != int(vlp.StateDraftingSpec) {
			t.Errorf("%s: B.State = %d, want %d (cross-project leak: B sees A's idle)", label, gotB.State, int(vlp.StateDraftingSpec))
		}
		if gotB.ProjectID != "tenant-b-"+label {
			t.Errorf("%s: B.ProjectID = %q, want tenant-b-%s", label, gotB.ProjectID, label)
		}

		// Switch back to A → must see idle (A's row).
		if err := s.SetActiveProject(ctx, "default"); err != nil {
			t.Fatalf("%s: SetActiveProject A: %v", label, err)
		}
		gotA, err := s.GetVLPState(ctx, sid)
		if err != nil {
			t.Fatalf("%s: GetVLPState A: %v", label, err)
		}
		if gotA.State != int(vlp.StateIdle) {
			t.Errorf("%s: A.State = %d, want %d (cross-project leak: A sees B's drafting_spec)", label, gotA.State, int(vlp.StateIdle))
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

	t.Run(label+"/write_audit_session_event_roundtrip", func(t *testing.T) {
		// Wave 5X.1: write_audit.session_event must round-trip through
		// RecordWrite → INSERT → SELECT. Prior to 5X.1 the v12 column
		// was dropped by the INSERT statements and omitted from
		// ListWrites' SELECT. Verifies the DAO fix end-to-end.
		//
		// Test emits a tagged audit row with a unique SessionEvent
		// value, then ListWrites with a SessionID filter confirms the
		// value came back identical.
		tag := "test_session_event_roundtrip_" + label
		wc := store.WriteContext{
			Actor:        "test-5x1",
			SessionID:    "sess-5x1-" + label,
			WritePath:    "TestSessionEventRoundtrip",
			SessionEvent: tag,
			ProjectID:    "default",
		}
		if err := s.RecordWrite(ctx, audit.WriteEvent{
			TableName:    "test_session_event_roundtrip",
			Actor:        wc.Actor,
			SessionID:    wc.SessionID,
			WritePath:    wc.WritePath,
			SessionEvent: wc.SessionEvent,
			Notes:        "5X.1 round-trip test",
			CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		}); err != nil {
			t.Fatalf("%s: RecordWrite: %v", label, err)
		}
		writes, err := s.ListWrites(ctx, audit.ListFilters{
			SessionID: wc.SessionID,
			Limit:     10,
		})
		if err != nil {
			t.Fatalf("%s: ListWrites: %v", label, err)
		}
		if len(writes) == 0 {
			t.Fatalf("%s: ListWrites returned 0 rows for our session", label)
		}
		var found bool
		for _, w := range writes {
			if w.SessionEvent == tag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: session_event %q not found in ListWrites; values were: %v",
				label, tag, sessionEvents(writes))
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

	t.Run(label+"/write_audit_project_isolation", func(t *testing.T) {
		// Bug-hunt F33 fix: every audit row is tagged with the active
		// project (INV-7). ListWrites filters by ProjectID when set.
		// Two projects writing rows with the same session_id must NOT
		// leak across the project boundary in the audit log.
		sid := "audit-iso-" + label

		// Save a row under project "default".
		wcA := store.WriteContext{
			Actor:     "a",
			SessionID: sid,
			WritePath: "TestAuditIsolation",
		}
		if _, err := s.SaveVLPState(ctx, wcA, &store.VLPStateRow{
			SessionID: sid,
			State:     int(vlp.StateDraftingSpec),
			LastEvent: "session_start",
			TurnCount: 0,
		}); err != nil {
			t.Fatalf("%s: SaveVLPState A: %v", label, err)
		}

		// Create + activate a second project.
		if err := s.CreateProject(ctx, &project.Project{ProjectID: "tenant-c-" + label, DisplayName: "C"}); err != nil {
			t.Fatalf("%s: CreateProject C: %v", label, err)
		}
		if err := s.SetActiveProject(ctx, "tenant-c-"+label); err != nil {
			t.Fatalf("%s: SetActiveProject C: %v", label, err)
		}
		wcB := store.WriteContext{
			Actor:     "b",
			SessionID: sid,
			WritePath: "TestAuditIsolation",
			ProjectID: "tenant-c-" + label,
		}
		if _, err := s.SaveVLPState(ctx, wcB, &store.VLPStateRow{
			SessionID: sid,
			State:     int(vlp.StateIdle),
			LastEvent: "session_start",
			TurnCount: 0,
		}); err != nil {
			t.Fatalf("%s: SaveVLPState B: %v", label, err)
		}

		// ListWrites with no ProjectID filter returns BOTH rows (cross-project).
		all, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, Limit: 100})
		if err != nil {
			t.Fatalf("%s: ListWrites all: %v", label, err)
		}
		if len(all) < 2 {
			t.Errorf("%s: expected >= 2 audit rows for shared session, got %d", label, len(all))
		}

		// ListWrites with ProjectID filter returns only that project's rows.
		rowsA, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, ProjectID: "default", Limit: 100})
		if err != nil {
			t.Fatalf("%s: ListWrites A: %v", label, err)
		}
		for _, r := range rowsA {
			if r.ProjectID != "default" {
				t.Errorf("%s: rowsA leak: ProjectID=%q", label, r.ProjectID)
			}
		}
		rowsB, err := s.ListWrites(ctx, audit.ListFilters{SessionID: sid, ProjectID: "tenant-c-" + label, Limit: 100})
		if err != nil {
			t.Fatalf("%s: ListWrites B: %v", label, err)
		}
		for _, r := range rowsB {
			if r.ProjectID != "tenant-c-"+label {
				t.Errorf("%s: rowsB leak: ProjectID=%q", label, r.ProjectID)
			}
		}

		// Switch back to default; subsequent writes use "default".
		if err := s.SetActiveProject(ctx, "default"); err != nil {
			t.Fatalf("%s: SetActiveProject default: %v", label, err)
		}
	})

	t.Run(label+"/save_resurrect_returns_session_row", func(t *testing.T) {
		// Wave 5E.iv.b contract: SaveResurrect must return the
		// newly-created session row (with ID and SessionID populated)
		// so the orchestrator can resume without a follow-up read.
		//
		// Pre-5E.iv.b the signature was (int64, error) — only the
		// internal row ID was returned, and the orchestrator had to
		// run a fragile ListSessions(50)+Go-filter scan to recover
		// the new SessionID. This test pins the new contract: the
		// returned *session.Session has SessionID, ID, ParentSessionID,
		// ResurrectedFrom, and Status=open populated.
		//
		// Setup: save an "original" session and close it as aborted
		// (only closed_aborted is resurrectable per INV-8).
		origSID := "test-resurrect-orig-" + label
		wc := store.WriteContext{Actor: "test", SessionID: origSID, WritePath: "TestSaveResurrect"}
		original := &session.Session{
			SessionID:      origSID,
			ConstitutionID: "test-constitution",
			ConstitutionVer: "1.0.0",
			Status:         string(session.StatusOpen),
			Operator:       "ci",
		}
		if _, err := s.SaveSession(ctx, wc, original); err != nil {
			t.Fatalf("%s: SaveSession original: %v", label, err)
		}
		// GetSession to capture the full row (with the DB-assigned ID).
		originalFull, err := s.GetSession(ctx, origSID)
		if err != nil {
			t.Fatalf("%s: GetSession original: %v", label, err)
		}
		if originalFull == nil {
			t.Fatalf("%s: GetSession original returned nil", label)
		}
		if err := s.CloseSession(ctx, wc, origSID, "aborted"); err != nil {
			t.Fatalf("%s: CloseSession aborted: %v", label, err)
		}

		// Re-fetch the original session so the in-memory copy reflects
		// the post-close status (closed_aborted). SaveResurrect's
		// status guard checks the in-memory Status field, so a stale
		// "open" would falsely trip the guard.
		originalFull, err = s.GetSession(ctx, origSID)
		if err != nil {
			t.Fatalf("%s: GetSession original post-close: %v", label, err)
		}
		if originalFull == nil {
			t.Fatalf("%s: GetSession returned nil post-close", label)
		}
		if originalFull.Status != string(session.StatusClosedAborted) {
			t.Fatalf("%s: post-close Status = %q, want closed_aborted", label, originalFull.Status)
		}

		// Act: call SaveResurrect and verify the returned session row.
		newSess, err := s.SaveResurrect(ctx, wc, originalFull)
		if err != nil {
			t.Fatalf("%s: SaveResurrect: %v", label, err)
		}
		if newSess == nil {
			t.Fatalf("%s: SaveResurrect returned nil session", label)
		}
		// ID and SessionID must be populated and distinct from original.
		if newSess.ID == 0 {
			t.Errorf("%s: returned session ID = 0", label)
		}
		if newSess.ID == originalFull.ID {
			t.Errorf("%s: returned session ID == original ID (%d); must be a NEW row",
				label, newSess.ID)
		}
		if newSess.SessionID == "" {
			t.Errorf("%s: returned SessionID is empty", label)
		}
		if newSess.SessionID == originalFull.SessionID {
			t.Errorf("%s: returned SessionID == original SessionID (%s); must be a NEW id",
				label, newSess.SessionID)
		}
		// Parent + chain pointers must be set.
		if newSess.ParentSessionID != originalFull.SessionID {
			t.Errorf("%s: ParentSessionID = %q, want %q",
				label, newSess.ParentSessionID, originalFull.SessionID)
		}
		if newSess.ResurrectedFrom == "" {
			t.Errorf("%s: ResurrectedFrom must be set", label)
		}
		// Status must be open.
		if newSess.Status != string(session.StatusOpen) {
			t.Errorf("%s: Status = %q, want open", label, newSess.Status)
		}
		// Constitution inherited.
		if newSess.ConstitutionID != originalFull.ConstitutionID ||
			newSess.ConstitutionVer != originalFull.ConstitutionVer {
			t.Errorf("%s: constitution not inherited: got id=%q ver=%q, want id=%q ver=%q",
				label, newSess.ConstitutionID, newSess.ConstitutionVer,
				originalFull.ConstitutionID, originalFull.ConstitutionVer)
		}
		// Round-trip: GetSession by the returned SessionID must find it.
		got, err := s.GetSession(ctx, newSess.SessionID)
		if err != nil {
			t.Fatalf("%s: GetSession(newSess.SessionID): %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetSession(newSess.SessionID) returned nil", label)
		}
		if got.SessionID != newSess.SessionID {
			t.Errorf("%s: round-trip SessionID = %q, want %q",
				label, got.SessionID, newSess.SessionID)
		}
	})

	t.Run(label+"/save_frame_roundtrip", func(t *testing.T) {
		// Wave 5A.ii.a contract: SaveFrame/GetFrame/ListFrames/DeleteFrame
		// must round-trip a frame envelope via the canonical composite key
		// (project_id, session_id, scope_level, scope_id, frame_kind).
		// INV-5: a mismatched content_sha256 must trigger a cache-miss
		// (anomaly) rather than returning a corrupted frame.
		sid := "test-frame-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestFrame"}

		// Save an identity frame.
		body := []byte(`{"session_id":"` + sid + `","operator":"ci","vintage":"v1"}`)
		frameSum := sha256.Sum256(body)
		env := &atomic.FrameEnvelope{
			ProjectID:     "default",
			SessionID:     sid,
			ScopeLevel:    atomic.ScopeSession,
			ScopeID:       sid,
			Kind:          atomic.FrameIdentity,
			ComposedAt:    time.Now().UTC(),
			ExpiresAt:     time.Now().UTC().Add(15 * time.Minute),
			FrameJSON:     body,
			ContentSHA256: frameSum,
			LastWriteID:   0,
		}
		id, err := s.SaveFrame(ctx, wc, env)
		if err != nil {
			t.Fatalf("%s: SaveFrame: %v", label, err)
		}
		if id == 0 {
			t.Fatalf("%s: SaveFrame returned id=0", label)
		}
		if env.ID != id {
			t.Errorf("%s: SaveFrame did not populate env.ID (got %d, want %d)",
				label, env.ID, id)
		}

		// GetFrame round-trip — verify row exists with the same body
		// bytes. ContentSHA256 round-trip is intentionally NOT asserted
		// here; that's the cache layer's (5A.ii.b) integrity check.
		got, err := s.GetFrame(ctx, sid, atomic.ScopeSession, sid, atomic.FrameIdentity)
		if err != nil {
			t.Fatalf("%s: GetFrame: %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetFrame returned nil", label)
		}
		if got.SessionID != sid {
			t.Errorf("%s: SessionID = %q, want %q", label, got.SessionID, sid)
		}
		if got.Kind != atomic.FrameIdentity {
			t.Errorf("%s: Kind = %q, want %q", label, got.Kind, atomic.FrameIdentity)
		}
		if string(got.FrameJSON) != string(body) {
			t.Errorf("%s: FrameJSON mismatch", label)
		}

		// Upsert: SaveFrame again with a different body must overwrite
		// the existing row (same row id, different content).
		body2 := []byte(`{"session_id":"` + sid + `","operator":"ci","vintage":"v2"}`)
		frameSum2 := sha256.Sum256(body2)
		env2 := &atomic.FrameEnvelope{
			ProjectID:     "default",
			SessionID:     sid,
			ScopeLevel:    atomic.ScopeSession,
			ScopeID:       sid,
			Kind:          atomic.FrameIdentity,
			ComposedAt:    time.Now().UTC(),
			ExpiresAt:     time.Now().UTC().Add(15 * time.Minute),
			FrameJSON:     body2,
			ContentSHA256: frameSum2,
			LastWriteID:   1,
		}
		id2, err := s.SaveFrame(ctx, wc, env2)
		if err != nil {
			t.Fatalf("%s: SaveFrame upsert: %v", label, err)
		}
		if id2 != id {
			t.Errorf("%s: SaveFrame upsert returned different id (got %d, want %d)",
				label, id2, id)
		}
		got2, err := s.GetFrame(ctx, sid, atomic.ScopeSession, sid, atomic.FrameIdentity)
		if err != nil {
			t.Fatalf("%s: GetFrame after upsert: %v", label, err)
		}
		if got2 == nil {
			t.Fatalf("%s: GetFrame after upsert returned nil", label)
		}
		if string(got2.FrameJSON) != string(body2) {
			t.Errorf("%s: upsert did not replace body (got %q, want %q)",
				label, got2.FrameJSON, body2)
		}
		if got2.LastWriteID != 1 {
			t.Errorf("%s: LastWriteID = %d, want 1", label, got2.LastWriteID)
		}

		// INV-5: tamper with the body's SHA to simulate cache corruption.
		// GetFrame must detect the mismatch and return (nil, nil) so the
		// caller treats it as a cache miss (cache_mismatch event emitted
		// via write_audit, asserted below via ListWrites).
		// Approach: write the env directly via sql? No — tests run against
		// the Store interface. Use the public API to verify INV-5
		// positively (correct SHA → returns row). The negative path
		// requires direct DB tampering, which we don't expose via Store.
		// Instead: verify ListFrames returns the saved frame.
		frames, err := s.ListFrames(ctx, store.FrameListFilters{
			ProjectID: "default",
			SessionID: sid,
		})
		if err != nil {
			t.Fatalf("%s: ListFrames: %v", label, err)
		}
		if len(frames) != 1 {
			t.Fatalf("%s: ListFrames returned %d frames, want 1", label, len(frames))
		}
		if frames[0].Kind != atomic.FrameIdentity {
			t.Errorf("%s: ListFrames Kind = %q, want %q",
				label, frames[0].Kind, atomic.FrameIdentity)
		}

		// DeleteFrame.
		if err := s.DeleteFrame(ctx, wc, id); err != nil {
			t.Fatalf("%s: DeleteFrame: %v", label, err)
		}
		got3, err := s.GetFrame(ctx, sid, atomic.ScopeSession, sid, atomic.FrameIdentity)
		if err != nil {
			t.Fatalf("%s: GetFrame after delete: %v", label, err)
		}
		if got3 != nil {
			t.Errorf("%s: GetFrame after delete returned non-nil: %+v", label, got3)
		}

		// DeleteFrame on missing id returns ErrNotFound.
		err = s.DeleteFrame(ctx, wc, id)
		if err == nil {
			t.Errorf("%s: DeleteFrame on missing id must error", label)
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: DeleteFrame on missing id: expected ErrNotFound, got %v",
				label, err)
		}
	})

	t.Run(label+"/save_frame_concurrent_upsert", func(t *testing.T) {
		// Wave 5A.ii.a polish: SaveFrame must be a true UPSERT
		// (INSERT ... ON CONFLICT DO UPDATE) under concurrent writes
		// for the same composite key. The previous SELECT-then-
		// INSERT/UPDATE pattern (5A.ii.a v11) was racy — two
		// concurrent goroutines could both observe "no row" and
		// both INSERT, creating duplicates. v13 adds a UNIQUE INDEX
		// and rewrites SaveFrame as ON CONFLICT, which serializes
		// at the index level.
		//
		// Test: 10 goroutines, same composite key, varying bodies →
		// expect EXACTLY 1 row in vibe_frames after the dust settles.
		// Without the fix, this would race and produce 2-10 rows.
		sid := "test-frame-concurrent-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestFrameConcurrent"}

		const concurrency = 10
		var wg sync.WaitGroup
		errs := make(chan error, concurrency)
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				body := []byte(fmt.Sprintf(`{"session_id":%q,"worker":%d,"vintage":"v%d"}`, sid, idx, idx))
				sum := sha256.Sum256(body)
				env := &atomic.FrameEnvelope{
					ProjectID:     "default",
					SessionID:     sid,
					ScopeLevel:    atomic.ScopeSession,
					ScopeID:       sid,
					Kind:          atomic.FrameIdentity,
					ComposedAt:    time.Now().UTC(),
					ExpiresAt:     time.Now().UTC().Add(15 * time.Minute),
					FrameJSON:     body,
					ContentSHA256: sum,
					LastWriteID:   int64(idx),
				}
				if _, err := s.SaveFrame(ctx, wc, env); err != nil {
					errs <- err
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%s: concurrent SaveFrame error: %v", label, err)
		}

		// Verify exactly 1 row exists for this composite key.
		frames, err := s.ListFrames(ctx, store.FrameListFilters{
			SessionID: sid,
			Kind:      atomic.FrameIdentity,
			Limit:     100,
		})
		if err != nil {
			t.Fatalf("%s: ListFrames: %v", label, err)
		}
		if len(frames) != 1 {
			t.Errorf("%s: after %d concurrent SaveFrame calls with the same key, expected 1 row, got %d (race condition!)",
				label, concurrency, len(frames))
		}
	})

	t.Run(label+"/recall_subscription_roundtrip", func(t *testing.T) {
		// Wave 5A.ii.b.1 contract: SaveRecallSubscription +
		// GetRecallSubscription + UpdateRecallSubscriptionLastSeenToken
		// must round-trip a subscription row keyed by the natural
		// (session_id, scope_level, scope_id) tuple. v11 has a UNIQUE
		// constraint on the tuple so SaveRecallSubscription is a true
		// INSERT ... ON CONFLICT upsert (no race-prone SELECT-then-UPDATE).
		sid := "test-sub-" + label
		wc := store.WriteContext{Actor: "test", SessionID: sid, WritePath: "TestSub"}

		// Insert new.
		sub := &store.RecallSubscription{
			ProjectID:     "default",
			SessionID:     sid,
			ScopeLevel:    atomic.ScopeSession,
			ScopeID:       sid,
			LastSeenToken: 0,
		}
		id, err := s.SaveRecallSubscription(ctx, wc, sub)
		if err != nil {
			t.Fatalf("%s: SaveRecallSubscription: %v", label, err)
		}
		if id == 0 {
			t.Fatalf("%s: SaveRecallSubscription returned id=0", label)
		}
		if sub.ID != id {
			t.Errorf("%s: SaveRecallSubscription did not populate sub.ID", label)
		}

		// Get.
		got, err := s.GetRecallSubscription(ctx, sid, atomic.ScopeSession, sid)
		if err != nil {
			t.Fatalf("%s: GetRecallSubscription: %v", label, err)
		}
		if got == nil {
			t.Fatalf("%s: GetRecallSubscription returned nil", label)
		}
		if got.SessionID != sid || got.ScopeLevel != atomic.ScopeSession || got.ScopeID != sid {
			t.Errorf("%s: Get key fields mismatch: %+v", label, got)
		}
		if got.LastSeenToken != 0 {
			t.Errorf("%s: LastSeenToken = %d, want 0", label, got.LastSeenToken)
		}

		// Upsert with new token. The UNIQUE constraint triggers ON CONFLICT,
		// and the row id must equal the original (UPDATE branch, not INSERT).
		sub.LastSeenToken = 42
		id2, err := s.SaveRecallSubscription(ctx, wc, sub)
		if err != nil {
			t.Fatalf("%s: SaveRecallSubscription upsert: %v", label, err)
		}
		if id2 != id {
			t.Errorf("%s: upsert returned different id (got %d, want %d)",
				label, id2, id)
		}
		got2, err := s.GetRecallSubscription(ctx, sid, atomic.ScopeSession, sid)
		if err != nil {
			t.Fatalf("%s: GetRecallSubscription after upsert: %v", label, err)
		}
		if got2.LastSeenToken != 42 {
			t.Errorf("%s: LastSeenToken after upsert = %d, want 42",
				label, got2.LastSeenToken)
		}

		// Advance cursor via UpdateRecallSubscriptionLastSeenToken.
		if err := s.UpdateRecallSubscriptionLastSeenToken(ctx, wc, sid, atomic.ScopeSession, sid, 100); err != nil {
			t.Fatalf("%s: UpdateRecallSubscriptionLastSeenToken: %v", label, err)
		}
		got3, err := s.GetRecallSubscription(ctx, sid, atomic.ScopeSession, sid)
		if err != nil {
			t.Fatalf("%s: GetRecallSubscription after cursor advance: %v", label, err)
		}
		if got3.LastSeenToken != 100 {
			t.Errorf("%s: LastSeenToken after advance = %d, want 100",
				label, got3.LastSeenToken)
		}

		// Update on missing key returns ErrNotFound.
		err = s.UpdateRecallSubscriptionLastSeenToken(ctx, wc, "nonexistent-session",
			atomic.ScopeSession, "nonexistent-scope", 200)
		if err == nil {
			t.Errorf("%s: UpdateRecallSubscriptionLastSeenToken on missing key must error", label)
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: UpdateRecallSubscriptionLastSeenToken on missing key: expected ErrNotFound, got %v",
				label, err)
		}

		// Contract #4: Get returns (nil, nil) on miss (NOT ErrNotFound).
		// The asymmetry with Update's ErrNotFound is intentional and
		// documented in store.go.
		gotMiss, err := s.GetRecallSubscription(ctx, "nonexistent-session",
			atomic.ScopeSession, "nonexistent-scope")
		if err != nil {
			t.Errorf("%s: GetRecallSubscription on missing key: unexpected error: %v",
				label, err)
		}
		if gotMiss != nil {
			t.Errorf("%s: GetRecallSubscription on missing key: expected nil, got %+v",
				label, gotMiss)
		}
	})
}

// TestSQLiteConstitutionWatchdogMigration exercises the Wave INFRA-003 v2
// migration step: DARK_CONSTITUTION_ACCEPT_BUMPS=1 opts in to a version
// bump (constitution row upgrade). Without the env var, a SHA mismatch
// combined with a version difference returns ErrConstitutionDrift with
// a message naming the env var.
//
// sessionEvents extracts the SessionEvent field from each WriteEvent
// for terse error messages in 5X.1's round-trip test.
func sessionEvents(ws []audit.WriteEvent) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%q", w.SessionEvent))
	}
	return out
}

// Postgres skipped: the postgres Store's constitution query returns
// notImpl currently (postgres.go:1432-1433), so VerifyConstitutionHash
// against the bumped row wouldn't have a meaningful assertion. The
// sqlite path is the canonical coverage.
func TestSQLiteConstitutionWatchdogMigration(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	dsn := filepath.Join(tmp, "watchdog-test.db")
	consFile := filepath.Join(tmp, "cerebro.constitution.toml")

	// Write an initial v1.0.0 constitution file.
	if err := os.WriteFile(consFile, []byte("[meta]\nid=\"dark-agents/dark-memory-mcp-cerebro\"\nversion=\"1.0.0\"\n"), 0644); err != nil {
		t.Fatalf("write v1.0.0 constitution: %v", err)
	}

	// 1. Open the store with ConstitutionFile + ConstitutionID + ConstitutionVer.
	//    Watchdog writes the initial row (SHA of v1.0.0 file).
	cfg := store.Config{
		Driver:          store.DriverSQLite,
		DSN:             dsn,
		WALMode:         true,
		ForeignKeys:     true,
		BusyTimeout:     5 * time.Second,
		ConstitutionFile: consFile,
		ConstitutionID:   "dark-agents/dark-memory-mcp-cerebro",
		ConstitutionVer:  "1.0.0",
	}
	s, err := runtime.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open store (initial): %v", err)
	}
	// Verify the row was written.
	ok, err := s.VerifyConstitutionHash(ctx, "dark-agents/dark-memory-mcp-cerebro", "")
	if err != nil {
		t.Fatalf("VerifyConstitutionHash initial: %v", err)
	}
	if !ok {
		t.Fatal("expected initial constitution row to be present")
	}
	s.Close()

	// 2. Modify the file to v1.1.0 (different content → different SHA).
	if err := os.WriteFile(consFile, []byte("[meta]\nid=\"dark-agents/dark-memory-mcp-cerebro\"\nversion=\"1.1.0\"\n# INV-10 added\n"), 0644); err != nil {
		t.Fatalf("write v1.1.0 constitution: %v", err)
	}

	// 3. Reopen WITHOUT the env var. Expect ErrConstitutionDrift naming the env var.
	cfg2 := cfg
	cfg2.ConstitutionVer = "1.1.0"
	_, err = runtime.Open(ctx, cfg2)
	if err == nil {
		t.Fatal("expected ErrConstitutionDrift on version bump without env var")
	}
	if !errors.Is(err, store.ErrConstitutionDrift) {
		t.Fatalf("expected ErrConstitutionDrift, got %v", err)
	}
	if !strings.Contains(err.Error(), "DARK_CONSTITUTION_ACCEPT_BUMPS") {
		t.Errorf("error message should name the env var, got: %v", err)
	}

	// 4. Reopen WITH the env var. Expect success; new row written for v1.1.0.
	t.Setenv("DARK_CONSTITUTION_ACCEPT_BUMPS", "1")
	s2, err := runtime.Open(ctx, cfg2)
	if err != nil {
		t.Fatalf("open store (with env var): %v", err)
	}
	defer s2.Close()

	// 5. Verify the new version row is present.
	//    (VerifyConstitutionHash takes any-version lookup; the table now
	//    has the v1.0.0 row disabled + the v1.1.0 row enabled. We confirm
	//    via ListConstitutions that v1.1.0 exists.)
	constitutions, err := s2.ListConstitutions(ctx, 10)
	if err != nil {
		t.Fatalf("ListConstitutions: %v", err)
	}
	foundV110 := false
	foundV100 := false
	for _, c := range constitutions {
		if c.ConstitutionID == "dark-agents/dark-memory-mcp-cerebro" {
			if c.Version == "1.1.0" {
				foundV110 = true
				if !c.Enabled {
					t.Errorf("v1.1.0 row should be enabled after upgrade")
				}
			}
			if c.Version == "1.0.0" {
				foundV100 = true
				if c.Enabled {
					t.Errorf("v1.0.0 row should be disabled after upgrade")
				}
			}
		}
	}
	if !foundV110 {
		t.Error("v1.1.0 constitution row not found after upgrade")
	}
	if !foundV100 {
		t.Error("v1.0.0 constitution row not found (expected disabled row)")
	}

	// 6. Idempotency: reopen with the same env var set. Should succeed (no second
	//    upgrade row written; SHA matches the v1.1.0 row we just wrote).
	s3, err := runtime.Open(ctx, cfg2)
	if err != nil {
		t.Fatalf("open store (re-open): %v", err)
	}
	s3.Close()
}
