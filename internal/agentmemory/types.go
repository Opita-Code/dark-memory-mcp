// Package agentmemory defines the agent-memory data plane (v2.1.0).
//
// Mem0-aligned (see https://arxiv.org/abs/2504.19413 and
// https://docs.mem0.ai/core-concepts/memory-types), but with dark-memory's
// INV-7 project isolation layered on top. Each row carries:
//
//	project_id   — INV-7 tenant key (always non-empty)
//	session_id   — operational session id (optional; NULL = persistent)
//	operator     — INV-1 audit + per-operator ownership
//	kind         — note | observation | decision | finding | todo | link | context
//	title        — short label (optional)
//	content      — the actual memory payload (required)
//	tags         — comma-separated, normalized lowercase
//	pinned       — surfaces at top of list results (boolean)
//	created_at   — RFC3339Nano
//	updated_at   — RFC3339Nano
//	archived_at  — RFC3339Nano or NULL (soft-delete)
//	expires_at   — RFC3339Nano or NULL (sweeper-eligible after this)
//
// Scope semantics (from the research findings, dark-mem-research/2026-07-27-v2.0.1-v2.0.2-research.md):
//
//   - session scope  = row.session_id == active session id (short-lived, defaults)
//   - project scope  = row.project_id == active project (across operators/sessions)
//   - operator scope = row.operator == current operator (across projects — gated)
//
// INV-7 always applies: rows are only visible within the active project,
// regardless of scope filter. cross-project reads require federation
// (DARK_FEDERATION_PEER_DSN — not yet implemented; see F45).
package agentmemory

import "time"

// Kind enumerates the seven high-level memory kinds. Free-form strings
// are accepted at the SQL boundary for forward-compat (operators may
// introduce a custom kind without a schema bump) but the canonical
// seven are listed here for tooling + tests.
const (
	KindNote        = "note"        // a running note; generic
	KindObservation = "observation" // something noticed about state/behavior
	KindDecision    = "decision"    // an architectural/strategic choice
	KindFinding     = "finding"     // a research-derived fact
	KindTodo        = "todo"        // an actionable follow-up
	KindLink        = "link"        // a pointer to an external resource
	KindContext     = "context"     // environmental/constitutional background
)

// ValidKind returns true if the kind is one of the seven canonical
// values. Free-form kinds are accepted by the Store but rejected by
// validation here; callers wanting forward-compat should bypass
// validation.
func ValidKind(k string) bool {
	switch k {
	case KindNote, KindObservation, KindDecision, KindFinding,
		KindTodo, KindLink, KindContext:
		return true
	}
	return false
}

// ScopeFilter enumerates the values the `scope` parameter of List can
// take. The default ("current") defers to the orchestrator: if an
// active session is bound, the scope narrows to that session; else it
// widens to operator. Explicit values let callers force a view.
const (
	ScopeCurrent  = "current"  // session if bound, else operator (default)
	ScopeSession  = "session"  // exactly the active session
	ScopeProject  = "project"  // the active project, all operators/sessions
	ScopeOperator = "operator" // the active operator, all projects (gated)
	ScopeAll      = "all"      // everything in active project
)

// ValidScope returns true if s is a recognized scope filter value.
func ValidScope(s string) bool {
	switch s {
	case "", ScopeCurrent, ScopeSession, ScopeProject, ScopeOperator, ScopeAll:
		return true
	}
	return false
}

// AgentMemory is the row shape for the agent_memory table.
//
// SessionID being non-empty means the row was created while a session
// was active; the orchestrator captures this at save time. Operators
// can later filter by "session" to see only those rows. Operators that
// save without a session get session_id = "" (operator-scoped rows
// that persist across sessions).
type AgentMemory struct {
	ID         int64     `json:"id"`
	ProjectID  string    `json:"project_id"`
	SessionID  string    `json:"session_id,omitempty"`
	Operator   string    `json:"operator"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title,omitempty"`
	Content    string    `json:"content"`
	Tags       string    `json:"tags,omitempty"`
	Pinned     bool      `json:"pinned"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
	ArchivedAt string    `json:"archived_at,omitempty"`
	ExpiresAt  string    `json:"expires_at,omitempty"`
}

// IsArchived returns true if the row has been soft-deleted.
func (m *AgentMemory) IsArchived() bool {
	return m.ArchivedAt != ""
}

// CreatedAtTime parses the CreatedAt RFC3339Nano string. Returns
// zero time + false on parse error (callers can decide whether to
// treat zero-time rows as invalid; v2.1.0 just logs).
func (m *AgentMemory) CreatedAtTime() (time.Time, bool) {
	return parseRFC3339Nano(m.CreatedAt)
}

func parseRFC3339Nano(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	// Fallback: trim sub-nanosecond precision (sqlite can drop it
	// in older drivers).
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// AgentMemoryUpdate is the input for Update. Pointer fields make
// "leave unchanged" distinct from "set to empty". The Store impl
// MUST treat a nil pointer as no-op and an empty-string pointer
// as explicit clear.
type AgentMemoryUpdate struct {
	Title     *string `json:"title,omitempty"`
	Content   *string `json:"content,omitempty"`
	Tags      *string `json:"tags,omitempty"`
	Pinned    *bool   `json:"pinned,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// AgentMemoryListFilters are the optional filters for List. Zero
// value is "all rows in active project, excluding archived, sorted
// pinned DESC + created_at DESC".
type AgentMemoryListFilters struct {
	// Scope narrows the result set. Empty = ScopeCurrent
	// (session if active, else operator).
	Scope string

	// Kind filters to one kind. Empty = any kind.
	Kind string

	// Tag filters to rows whose Tags contains the given token
	// (case-insensitive, comma-separated match). Empty = any.
	Tag string

	// PinnedOnly restricts to pinned rows. Default false.
	PinnedOnly bool

	// IncludeArchived includes soft-deleted rows. Default false
	// (archived rows are hidden unless explicitly requested).
	IncludeArchived bool

	// Limit caps the result count. 0 = default (50). Max 200
	// (Store impl enforces).
	Limit int
}

// SearchFilters are the optional filters for full-text search. The
// query is matched against content+title+tags via FTS5 BM25 ranking.
// Empty Query means "no search" — the Store should return ErrInvalidArgument
// rather than silently return everything.
type SearchFilters struct {
	Query string

	// Kind narrows the result set. Empty = any.
	Kind string

	// Limit caps the result count. 0 = default (50).
	Limit int
}

// SearchHit extends AgentMemory with the FTS5 BM25 rank (lower is
// better; 0 means exact match for some queries). Negative scores
// are valid FTS5 output — the Store surfaces the raw value.
type SearchHit struct {
	AgentMemory
	Rank float64 `json:"rank"`
}