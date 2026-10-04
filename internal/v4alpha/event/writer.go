// Package event — writer.go: the EventWriter primitive (Phase 12 T-102).
//
// Emit is the canonical entry point for emitting events. It enforces
// INV-20 (rationale-first for modifications) via combo (a)+(c) and
// writes the events row + an audit_log chain row (when an *audit.Writer
// is injected) for HMAC chain continuity.
//
// # Architecture (per SPEC §4.1, §4.2, §4.3)
//
//	Emit(ctx, ev)
//	  ├─ Validate (kind, actor)
//	  ├─ INV-20 combo:
//	  │   if Kind=="modification" && Rationale=="":
//	  │     emitSentinel("MISSING_RATIONALE — ...")  // best-effort
//	  │     return 0, ErrRationaleRequired
//	  ├─ emit(events)         // atomic insert via store.InsertEvent
//	  ├─ emit(audit_log row)  // chain continuity (optional)
//	  └─ return eventID, nil
//
//	EmitAsync(ctx, ev)
//	  fire-and-forget goroutine; errors logged but NOT returned.
//	  Used by progress events (always async per Q2 decision) and
//	  cosmetic modifications (async per Q2 hybrid).
//
// # Threading
//
// The Writer holds a sync.Mutex that serializes Emit. Async emits each
// run in their own goroutine, so they may interleave with sync emits from
// other goroutines. The mutex prevents two simultaneous inserts from
// the SAME Writer from racing.
//
// The mutex does NOT coordinate between Writers. Cross-process
// monotonicity comes from SQLite AUTOINCREMENT (per-Store, not global).
//
// # Audit chain
//
// When constructed with a non-nil audit.Writer, every Emit also calls
// audit.Writer.WriteWithProject, which appends an audit_log row linked
// to the prior row via prev_hash. The chain is what makes the unified
// HMAC stream work (single chain across events + write_audit).
//
// The audit_log row's payload_event/payload_id/payload_kind columns
// (ADR-019) carry the event metadata: payload_kind = ev.Kind,
// payload_id = events.id, payload_event = "event". audit_export knows
// to cross-reference events by payload_id when payload_kind ∈
// {"modification", "progress"}.
package event

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// sqliteStore is the minimum interface Writer needs from the store layer.
// Defined as an interface so tests can inject fakes without spinning up
// a real SQLite DB. The real *sqlite.Store satisfies this interface.
type sqliteStore interface {
	InsertEvent(ctx context.Context, ev *sqlite.Event) (int64, error)
	ActiveProject() string // INV-7 — for chain-row project_id defaulting
}

// auditWriter is the minimum interface Writer needs from the audit layer
// for chain row writes. The real *v4alpha/audit.Writer satisfies it.
// Nil is allowed: when nil, no chain row is written (caller responsibility
// to track chain via another mechanism — typically used in tests).
type auditWriter interface {
	WriteWithProject(ctx context.Context, actor, sessionID, projectID string, payload []byte) (int64, error)
}

// Writer emits events to the events table + the audit_log chain.
//
// Concurrency: see package doc. The mutex serializes Emit calls within
// the SAME Writer. Distinct Writers share no synchronization (SQLite
// auto-handles cross-process id monotonicity).
type Writer struct {
	mu       sync.Mutex
	store    sqliteStore
	audit    auditWriter // may be nil
	now      func() time.Time
	logger   *log.Logger // may be nil → falls back to log.Default
}

// Option configures optional Writer fields (logger, clock, audit).
type Option func(*Writer)

// WithLogger installs a custom *log.Logger for EmitAsync failures.
// Default: log.Default (writes to stderr).
func WithLogger(l *log.Logger) Option {
	return func(w *Writer) { w.logger = l }
}

// WithClock installs a custom clock for tests. Default: time.Now (UTC).
func WithClock(now func() time.Time) Option {
	return func(w *Writer) { w.now = now }
}

// New constructs a Writer bound to the store. auditW may be nil (no
// chain row written). The audit.Writer is optional because some test
// scenarios don't need chain coverage.
func New(store sqliteStore, auditW auditWriter, opts ...Option) (*Writer, error) {
	if store == nil {
		return nil, fmt.Errorf("%w", ErrStoreRequired)
	}
	w := &Writer{
		store:  store,
		audit:  auditW,
		now:    func() time.Time { return time.Now().UTC() },
		logger: log.Default(),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w, nil
}

// Emit inserts one event. Returns the new event id.
//
// # INV-20 combo (a)+(c)
//
// When ev.Kind == "modification" and ev.Rationale == "":
//   - (c) emit a MISSING_RATIONALE sentinel event for forensic record
//     (best-effort; never fails the call)
//   - (a) return 0, ErrRationaleRequired
//
// Caller checks errors.Is(err, ErrRationaleRequired) to detect.
//
// # Happy path
//
// Emits 1 events row + (optionally) 1 audit_log row. The audit_log row
// uses payload_kind = ev.Kind so audit_export + audit_verify can
// reconstruct the unified HMAC chain.
func (w *Writer) Emit(ctx context.Context, ev Event) (int64, error) {
	// 1. Validate required fields.
	if ev.Actor == "" {
		return 0, fmt.Errorf("%w", ErrActorRequired)
	}
	if ev.Kind != KindModification && ev.Kind != KindProgress {
		return 0, fmt.Errorf("%w: got %q", ErrInvalidKind, ev.Kind)
	}

	// 2. INV-20 combo (a)+(c).
	if ev.Kind == KindModification && ev.Rationale == "" {
		// (c) sentinel — best-effort; we deliberately swallow errors
		// here because the SENTINEL is not the primary action; the
		// primary action is to REJECT the caller.
		_, _ = w.emitSentinel(ctx, ev)

		// (a) reject.
		return 0, fmt.Errorf("%w", ErrRationaleRequired)
	}

	// 3. Happy path: write the event + (optionally) the chain row.
	return w.emit(ctx, ev)
}

// EmitAsync fires Emit in a goroutine. Returns immediately.
//
// Use for progress events (always async per Q2 decision) and cosmetic
// modifications (async per Q2 hybrid).
//
// Errors are logged via w.logger (default log.Default). The caller MUST
// NOT expect error propagation — by definition, fire-and-forget means
// the caller moves on. If error propagation matters, use Emit + a channel.
func (w *Writer) EmitAsync(ctx context.Context, ev Event) {
	go func() {
		_, err := w.Emit(ctx, ev)
		if err != nil && !errors.Is(err, ErrRationaleRequired) {
			// Rationale violation is logged at debug level — it's
			// the caller's fault, not an internal error.
			if w.logger != nil {
				w.logger.Printf("event: emit async failed (kind=%s, actor=%s): %v",
					ev.Kind, ev.Actor, err)
			}
		}
	}()
}

// emit is the happy-path writer. It writes the events row + the
// audit_log chain row. Called by Emit (after validation + INV-20 check)
// and by emitSentinel (for the sentinel specifically).
//
// Locking: takes w.mu. Emits serialized within one Writer.
func (w *Writer) emit(ctx context.Context, ev Event) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. Set ts to now if the caller left it empty. The events table
	// default is empty (we fill it here, before insert). Mirrors
	// sqlite.InsertEvent default behavior.
	dbEv := toDBEvent(ev)

	// 2. Insert the events row.
	eventID, err := w.store.InsertEvent(ctx, dbEv)
	if err != nil {
		return 0, fmt.Errorf("event: insert: %w", err)
	}

	// 3. Write the audit_log chain row (if audit writer is configured).
	if w.audit != nil {
		payload, err := encodeEventPayload(eventID, ev)
		if err != nil {
			// The event IS in the DB; the audit row is for chain
			// continuity. Log it but don't fail the call.
			if w.logger != nil {
				w.logger.Printf("event: encode chain payload failed (event_id=%d): %v",
					eventID, err)
			}
			return eventID, nil
		}
		// Resolve the project_id for the audit_log row. Prefer the
		// explicit ev.ProjectID; fall back to the store's active
		// project (INV-7); fall back to "default" as a last resort so
		// the audit Writer's INV-1 check passes.
		chainProjectID := ev.ProjectID
		if chainProjectID == "" {
			chainProjectID = w.store.ActiveProject()
		}
		if chainProjectID == "" {
			chainProjectID = "default"
		}
		if _, err := w.audit.WriteWithProject(ctx,
			"event:"+ev.Kind, ev.SessionID, chainProjectID, payload,
		); err != nil {
			if w.logger != nil {
				w.logger.Printf("event: chain row failed (event_id=%d): %v",
					eventID, err)
			}
		}
	}

	return eventID, nil
}

// emitSentinel emits a MISSING_RATIONALE sentinel BEFORE the caller
// rejection. Best-effort — never returns an error to the caller; any
// failure is logged.
//
// Rationale: combo (a)+(c) — even when rejecting, we leave a forensic
// trail so post-incident analysis can see WHAT was attempted.
//
// The sentinel inherits the original event's fields but overrides:
//   - Rationale = SentinelRationale (the constant string)
//   - RationaleKind = "inv20_violation"
//   - Classification = ClassificationInv20Violation
//   - Actor = "system" (the Writer emitting, not the caller)
//
// The sentinel BYPASSES INV-20 because its Rationale is non-empty
// (the sentinel rationale is itself a rationale). This avoids the
// infinite regression of "emit a sentinel for a sentinel".
func (w *Writer) emitSentinel(ctx context.Context, original Event) (int64, error) {
	sentinel := original
	sentinel.Rationale = SentinelRationale
	sentinel.RationaleKind = "inv20_violation"
	sentinel.Classification = ClassificationInv20Violation
	sentinel.Actor = "system"

	id, err := w.emit(ctx, sentinel)
	if err != nil && w.logger != nil {
		w.logger.Printf("event: sentinel emit failed (actor=%s, source=%s): %v",
			original.Actor, original.Source, err)
	}
	return id, err
}

// encodeEventPayload serializes the chain-row payload as JSON. The
// payload contains the event metadata (id, kind, classification,
// rationale, source, target_table/target_row_id) so audit_export can
// reconstruct the chain link without re-querying events.
func encodeEventPayload(eventID int64, ev Event) ([]byte, error) {
	p := struct {
		EventID         int64    `json:"event_id"`
		Kind            string   `json:"kind"`
		Classification  string   `json:"classification,omitempty"`
		Source          string   `json:"source,omitempty"`
		Rationale       string   `json:"rationale,omitempty"`
		RationaleKind   string   `json:"rationale_kind,omitempty"`
		TargetTable     string   `json:"target_table,omitempty"`
		TargetRowID     *int64   `json:"target_row_id,omitempty"`
		Operation       string   `json:"operation,omitempty"`
		ProcessID       string   `json:"process_id,omitempty"`
		Phase           string   `json:"phase,omitempty"`
	}{
		EventID:        eventID,
		Kind:           ev.Kind,
		Classification: ev.Classification,
		Source:         ev.Source,
		Rationale:      ev.Rationale,
		RationaleKind:  ev.RationaleKind,
		TargetTable:    ev.TargetTable,
		TargetRowID:    ev.TargetRowID,
		Operation:      ev.Operation,
		ProcessID:      ev.ProcessID,
		Phase:          ev.Phase,
	}
	return json.Marshal(p)
}

// Compile-time guard: ensure writer holds no zero-value resource leaks.
var _ = sql.NullString{} // imported to keep sql in the import set (used by types.go)