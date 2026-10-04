// Package event — Phase 12 / alpha.23 T-102: EventWriter with INV-20
// combo (a)+(c) + HMAC chain integration.
//
// This is the v4alpha-level rich Go API for emitting events. The data
// layer (sqlite.Event, events table operations) lives in
// internal/store/sqlite/events.go (T-101). The Writer is the policy
// layer: it enforces INV-20 rationale-first invariant, writes the
// audit_log chain row, and exposes EmitAsync for non-blocking progress.
//
// # Architecture (per SPEC-alpha-11-phase12 §4.1)
//
//	Writer.Emit(ctx, Event) -> (eventID, error)
//	  ├─ validation: kind ∈ {modification, progress}, actor ≠ ""
//	  ├─ INV-20 combo (a)+(c): if modification AND rationale=="":
//	  │    ├─ emitSentinel(MISSING_RATIONALE)  // best-effort
//	  │    └─ return ErrRationaleRequired      // reject
//	  └─ happy path: emit(events) + emit(audit_log chain row)
//
//	Writer.EmitAsync(ctx, Event) — fire-and-forget for progress
//	  events (always async per Q2 decision). Errors logged via std
//	  logger, never block the caller.
//
// # INV-20 combo (a)+(c) (Q3 operator decision 2026-10-04)
//
// Why both:
//   - (a) reject alone: any forgotten rationale = silent production
//     bug, no forensic record of WHAT was attempted.
//   - (c) sentinel alone: operator has no UI surface (Q3 operator
//     decision: "no tengo ningún dashboard").
//   - Combo: loud failure + forensic record in DB for post-incident
//     analysis. Sentinel IS queryable via event_log tool (T-105).
//
// # HMAC chain integration
//
// Every Emit writes:
//   1. One events table row (the event itself).
//   2. One audit_log table row via the injected *audit.Writer (chain
//      continuity). payload_kind = ev.Kind so audit_export knows about
//      the event. payload_id = the events.id returned by step 1.
//
// The audit_log row is what makes the HMAC chain "single" across both
// tables: audit_export reads audit_log in id ASC, cross-references
// events by payload_id, and produces a unified JSONL stream.
//
// # Files
//
//	types.go      — Event struct + sentinel errors + kind/classification/rationale_kind
//	              constants + Convert helpers (this file)
//	writer.go     — Writer struct + Emit / EmitAsync / emitSentinel
//	writer_test.go — 12 tests (rationale + sentinel, atomic insert,
//	              chain, error paths, multi-key rotation, async, kind)
package event

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/store/sqlite"
)

// Sentinel errors. Callers use errors.Is() to discriminate.
var (
	// ErrRationaleRequired fires when a modification event is emitted
	// without a rationale (INV-20 combo (a)+(c) reject). The Writer
	// auto-emits a MISSING_RATIONALE sentinel BEFORE returning this
	// error so the forensic record exists in the events table.
	ErrRationaleRequired = errors.New("event: rationale is required for modification events (INV-20)")

	// ErrInvalidKind fires when ev.Kind is neither "modification" nor
	// "progress". Maps to store.ErrInvalidArgument semantically.
	ErrInvalidKind = errors.New("event: kind must be 'modification' or 'progress'")

	// ErrActorRequired fires when ev.Actor is empty. INV-1 audit
	// requirement: every write must identify its actor.
	ErrActorRequired = errors.New("event: actor is required (INV-1)")

	// ErrStoreRequired fires when New is called with a nil store. The
	// Writer cannot function without a database handle.
	ErrStoreRequired = errors.New("event: store is required")
)

// Kind values. Aliased from sqlite package for convenience so callers
// don't have to import both packages.
const (
	KindModification = sqlite.EventKindModification
	KindProgress     = sqlite.EventKindProgress
)

// Phase values for progress events. Per SPEC §3.6 (LangFuse / Step
// Functions phase taxonomy).
const (
	PhaseSpawned       = "spawned"        // orchestrator emitted, process hasn't started
	PhaseStarted       = "started"        // process began execution
	PhaseRunning       = "running"        // process is making progress
	PhaseHeartbeat     = "heartbeat"      // periodic update from long-running process
	PhaseWaitCallback  = "wait_callback"  // process waiting for external callback
	PhaseCompleted     = "completed"      // process finished successfully
	PhaseFailed        = "failed"         // process terminated with error
)

// RationaleKind values per SPEC §3.5. Used by the rationale_kind column
// to distinguish WHY the change was made.
const (
	RationaleDecayFunction         = "decay_function"         // applied per Phase 5 decay formula
	RationaleJudgeVerdict          = "judge_verdict"          // applied per LLM-as-judge verdict
	RationaleBitemporalTransition  = "bitemporal_transition"  // supersession per Phase 7
	RationaleSchemaChange          = "schema_change"          // explicit ALTER TABLE
	RationaleEntityGraphUpdate     = "entity_graph_update"    // ProGraph BFS found new edges
	RationaleCacheTTLExpired       = "cache_ttl_expired"      // recall cache invalidation
	RationaleProgressStarted       = "progress_started"       // async process started
	RationaleProgressCompleted     = "progress_completed"     // async process completed
	RationaleProgressFailed        = "progress_failed"        // async process failed
	RationaleUnknown               = "unknown"                // generic catch-all
)

// Classification values per SPEC §3.4. Used by the classification column to
// distinguish WHAT kind of operation it is.
const (
	ClassificationDecay             = "decay"             // Phase 5 decay.go
	ClassificationJudge            = "judge"             // judge pipeline (UpdateConfidence)
	ClassificationSupersede         = "supersede"         // mark_superseded
	ClassificationEmbedder         = "embedder"          // entity graph refresh
	ClassificationCalibration      = "calibration"       // judge calibration re-run
	ClassificationCache            = "cache"             // semantic-affecting cache invalidation
	ClassificationSchema           = "schema"            // schema migration
	ClassificationInv20Violation   = "inv20_violation"   // MISSING_RATIONALE sentinel
)

// SentinelRationale is the rationale baked into INV-20 sentinels. The
// string is exact-match so test assertions can grep for it.
const SentinelRationale = "MISSING_RATIONALE — auto-emitted pre-reject for forensic record"

// Event is the rich Go API for emitting events. All fields are pointers
// (or []byte) for optional fields to distinguish "set to empty" from
// "not set" (NULL). Conversion to *sqlite.Event happens via toDBEvent.
type Event struct {
	// Common
	ProjectID     string // empty → use store.activeProject (default)
	Kind          string // "modification" | "progress" (required)
	Actor         string // required (INV-1)
	SessionID     string // optional
	ParentEventID *int64 // optional — nesting (LangFuse tree)
	RootEventID   *int64 // optional — execution tree (Step Functions)

	// Modification-only (NULL for progress)
	TargetTable    string  // e.g. "agent_memory"
	TargetRowID    *int64  // the row being modified
	Operation      string  // "insert" | "update" | "delete"
	Classification string  // "decay" | "judge" | ...
	Source         string  // file path of the emitting code
	Rationale      string  // ** REQUIRED ** for modifications (INV-20)
	RationaleKind  string  // "decay_function" | "judge_verdict" | ...
	PayloadBefore  []byte  // pre-state JSON (NULL for insert)
	PayloadAfter   []byte  // post-state JSON (NULL for delete)
	Confidence    *float64 // LLM-as-judge confidence (0..1)
	JudgeVerdict  string   // "aligned" | "drift_detected" | "needs_human"
	JudgeReasoning string  // free-form
	JudgeRunID    *int64  // links to sdd_evaluations.id

	// Progress-only (NULL for modification)
	ProcessID   string   // unique async-work identifier
	Phase       string   // "spawned" | "started" | "running" | ...
	ProgressPct *float64 // 0.0..1.0 (heartbeat)
	Message     string   // human-readable progress message
	DurationMs  *int64   // total process duration (set on completion)
	ErrorMsg    string   // set on failure

	// Both kinds (OTel GenAI-style attributes, JSON-encoded)
	PayloadJSON []byte
}

// toDBEvent converts the rich Event to the DB-layer *sqlite.Event.
// Required-field validation happens in Emit (so we don't duplicate the
// error messages); this function does pure structural conversion.
//
// All pointer fields become sql.NullX with Valid=false for nil;
// empty strings also map to Valid=false (NULL in DB).
func toDBEvent(ev Event) *sqlite.Event {
	return &sqlite.Event{
		ProjectID: ev.ProjectID,
		Kind:      ev.Kind,
		Actor:     ev.Actor,

		SessionID:     nullStr(ev.SessionID),
		ParentEventID: nullInt64Ptr(ev.ParentEventID),
		RootEventID:   nullInt64Ptr(ev.RootEventID),

		// Modification-only
		TargetTable:    nullStr(ev.TargetTable),
		TargetRowID:    nullInt64Ptr(ev.TargetRowID),
		Operation:      nullStr(ev.Operation),
		Classification: nullStr(ev.Classification),
		Source:         nullStr(ev.Source),
		Rationale:      nullStr(ev.Rationale),
		RationaleKind:  nullStr(ev.RationaleKind),
		PayloadBefore:  nullBytes(ev.PayloadBefore),
		PayloadAfter:   nullBytes(ev.PayloadAfter),
		Confidence:     nullFloat64Ptr(ev.Confidence),
		JudgeVerdict:   nullStr(ev.JudgeVerdict),
		JudgeReasoning: nullStr(ev.JudgeReasoning),
		JudgeRunID:     nullInt64Ptr(ev.JudgeRunID),

		// Progress-only
		ProcessID:   nullStr(ev.ProcessID),
		Phase:       nullStr(ev.Phase),
		ProgressPct: nullFloat64Ptr(ev.ProgressPct),
		Message:     nullStr(ev.Message),
		DurationMs:  nullInt64Ptr(ev.DurationMs),
		ErrorMsg:    nullStr(ev.ErrorMsg),

		// Both
		PayloadJSON: nullBytes(ev.PayloadJSON),
	}
}

// nullStr returns sql.NullString with Valid=true only when s is non-empty.
// Maps "" → NULL (NOT NULL).
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullBytes returns sql.NullString with Valid=true only when b is non-nil.
// Maps nil → NULL (NOT NULL).
func nullBytes(b []byte) sql.NullString {
	if b == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// nullInt64Ptr returns sql.NullInt64 with Valid=true only when p is non-nil.
// Maps nil → NULL (NOT NULL).
func nullInt64Ptr(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// nullFloat64Ptr returns sql.NullFloat64 with Valid=true only when p is non-nil.
// Maps nil → NULL (NOT NULL).
func nullFloat64Ptr(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

// Validate enforces required-field constraints without writing. Returns nil
// on success, or one of ErrInvalidKind / ErrActorRequired /
// ErrRationaleRequired on failure. Used by tests for dry-run validation.
func (ev Event) Validate() error {
	if ev.Actor == "" {
		return fmt.Errorf("%w", ErrActorRequired)
	}
	if ev.Kind != KindModification && ev.Kind != KindProgress {
		return fmt.Errorf("%w: got %q", ErrInvalidKind, ev.Kind)
	}
	if ev.Kind == KindModification && ev.Rationale == "" {
		return fmt.Errorf("%w", ErrRationaleRequired)
	}
	return nil
}