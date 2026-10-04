// Package store — events.go: Event struct + ListEventsFilter.
//
// Phase 12 T-105: the Event struct + ListEventsFilter were MOVED here
// from internal/store/sqlite/events.go so the store.Store interface
// can reference them WITHOUT creating a store→sqlite→store import
// cycle (the interface methods return *Event).
//
// The sqlite package aliases the type so existing callers that use
// sqlite.Event continue to work:
//
//	type Event = store.Event
//
// Field semantics are unchanged from the sqlite version (Phase 12
// T-101): the table is polymorphic (single events table for both
// modifications and progress), columns are NULLABLE, nullable fields
// use sql.NullString / sql.NullInt64 / sql.NullFloat64.
package store

import "database/sql"

// EventKind* values are the discriminator for the events table
// (single polymorphic table, schema v32). Mirrored from sqlite so
// callers in this package can refer to them.
const (
	EventKindModification = "modification"
	EventKindProgress     = "progress"
)

// Event is one modification or progress event in the events table.
// Fields are nullable via sql.NullX (NULL when the column is unused
// for the kind). Caller convention: set Kind = EventKindModification
// or EventKindProgress before Insert.
//
// Polymorphic shape:
//
//	modification rows populate TargetTable, TargetRowID, Operation,
//	  Rationale (REQUIRED by INV-20), RationaleKind, Classification,
//	  Source, JudgeVerdict (optional), Confidence (optional).
//	progress rows populate ProcessID, Phase, ProgressPct, Message,
//	  DurationMs (on completion), ErrorMsg (on failure), ParentEventID
//	  (LangFuse tree) + RootEventID (Step Functions exec).
//
// Both kinds can populate PayloadJSON (OTel GenAI-style attributes).
type Event struct {
	ID        int64
	ProjectID string
	Kind      string // EventKindModification | EventKindProgress
	TS        string // RFC3339

	// Common
	Actor         string
	SessionID     sql.NullString
	ParentEventID sql.NullInt64
	RootEventID   sql.NullInt64

	// Modification-only (NULL for progress)
	TargetTable    sql.NullString
	TargetRowID    sql.NullInt64
	Operation      sql.NullString
	Classification sql.NullString
	Source         sql.NullString
	Rationale      sql.NullString
	RationaleKind  sql.NullString
	PayloadBefore  sql.NullString
	PayloadAfter   sql.NullString
	Confidence     sql.NullFloat64
	JudgeVerdict   sql.NullString
	JudgeReasoning sql.NullString
	JudgeRunID     sql.NullInt64

	// Progress-only (NULL for modification)
	ProcessID   sql.NullString
	Phase       sql.NullString
	ProgressPct sql.NullFloat64
	Message     sql.NullString
	DurationMs  sql.NullInt64
	ErrorMsg    sql.NullString

	// Both kinds (OTel GenAI-style attributes)
	PayloadJSON sql.NullString
}

// ListEventsFilter holds optional filters for the List* family. The
// zero value returns everything for the active project (INV-7
// scoping is enforced via ProjectID; if empty, ActiveProject() is
// used).
type ListEventsFilter struct {
	// ProjectID is the project to scope to. Empty → ActiveProject().
	ProjectID string
	// Kind filter: "" | "modification" | "progress".
	Kind string
	// TargetTable filter: e.g. "agent_memory" | "delegation_events".
	TargetTable string
	// TargetRowID filter: 0 → no filter.
	TargetRowID int64
	// ProcessID filter: e.g. "drift-<artifactID>".
	ProcessID string
	// SessionID filter: empty → no filter.
	SessionID string
	// Actor filter: empty → no filter.
	Actor string
	// SinceID cursor for incremental fetch (LangFuse flush pattern).
	// 0 → no cursor; rows with id > sinceId are returned (id ASC).
	SinceID int64
	// Limit: 0 → no limit (caller is responsible for result size).
	// Negative → 0 (no rows).
	Limit int
}