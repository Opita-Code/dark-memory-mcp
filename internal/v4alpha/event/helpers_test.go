package event_test

// Test helpers for EventWriter (Phase 12 T-102). All hermetic: in-memory
// SQLite via the production Open() path (no mocks — per dark-testing
// A3 no mystery guests + A5 mock only at boundaries).
//
// Pattern mirrors internal/store/sqlite/events_test.go helpers but
// adapted for the v4alpha/event package. The events table lives in the
// SAME sqlite.Store DB; the audit_log table lives in a SEPARATE
// in-memory DB because the audit.Writer is a different package and
// binds to its own schema.
//
// Per dark-testing A14 (no default values): every test passes
// non-empty rationale, actor, classification.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
	eventpkg "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/event"
	v4project "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/project"
	v4store "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/store"
)

// testEnv wires up: typed events store + audit_log writer on a sibling DB.
// Both are hermetic; no shared state between tests.
type testEnv struct {
	eventsStore *sqlite.Store
	auditWriter *audit.Writer
	auditDB     *sql.DB
}

// newTestEnv opens a fresh temp-file SQLite for events + a fresh
// temp-file SQLite for audit_log (audit.CreateSchema expects its own
// DB handle so it can create the audit_log table independently).
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	// --- events store ---
	eventsCfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(t.TempDir(), "events.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	iface, err := sqlite.Open(ctx, eventsCfg)
	if err != nil {
		t.Fatalf("sqlite.Open events: %v", err)
	}
	eventsStore := iface.(*sqlite.Store)
	t.Cleanup(func() { _ = eventsStore.Close() })

	if err := eventsStore.CreateProject(ctx,
		&project.Project{ProjectID: "default", DisplayName: "Default"},
	); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := eventsStore.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}

	// --- audit_log DB (separate file) ---
	auditPath := filepath.Join(t.TempDir(), "audit.db")
	auditDB, err := v4store.OpenSQLite(ctx, auditPath)
	if err != nil {
		t.Fatalf("v4store.OpenSQLite audit: %v", err)
	}
	t.Cleanup(func() { _ = auditDB.Close() })

	if err := audit.CreateSchema(auditDB); err != nil {
		t.Fatalf("audit.CreateSchema: %v", err)
	}
	if err := v4project.ApplyProjectIDColumns(context.Background(), auditDB); err != nil {
		t.Fatalf("ApplyProjectIDColumns: %v", err)
	}

	return &testEnv{
		eventsStore: eventsStore,
		auditDB:     auditDB,
		auditWriter: audit.NewWriter(auditDB),
	}
}

// newWriter constructs an EventWriter wired to the testEnv. Returns the
// writer + the events store (so tests can call store.GetEventByID etc.).
//
// Audit writer is OPTIONAL: pass withAudit=true to wire a real
// *audit.Writer; pass withAudit=false to pass a nil interface (so
// event.Writer.emit's nil check correctly skips the chain row).
func newWriter(t *testing.T, env *testEnv, withAudit bool) *eventpkg.Writer {
	t.Helper()
	var aw eventAuditWriterIface
	if withAudit {
		aw = eventAuditWriter{w: env.auditWriter}
	}
	// else: aw remains nil interface
	w, err := eventpkg.New(env.eventsStore, aw)
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	return w
}

// eventAuditWriterIface aliases the auditWriter interface from the
// event package so we can pass nil cleanly. Defined as a local type
// so changes to the interface don't break this helper.
type eventAuditWriterIface interface {
	WriteWithProject(ctx context.Context, actor, sessionID, projectID string, payload []byte) (int64, error)
}

// eventAuditWriter adapts *v4alpha/audit.Writer to the
// auditWriter interface declared in event package. Both have a
// WriteWithProject method with the same signature, so the bridge is
// trivial.
type eventAuditWriter struct {
	w *audit.Writer
}

func (a eventAuditWriter) WriteWithProject(
	ctx context.Context,
	actor, sessionID, projectID string,
	payload []byte,
) (int64, error) {
	return a.w.WriteWithProject(ctx, actor, sessionID, projectID, payload)
}