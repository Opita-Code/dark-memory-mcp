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

import (
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/embedder"
	"github.com/dark-agents/dark-memory-mcp/internal/entity"
)

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
//
// v2.3.0 adds ScopeAgent = (project_id, agent_id) — Mem0 agent_id
// semantics. Rows are independently queryable by (project_id,
// agent_id, kind). Closing the session does NOT invalidate rows. See
// INV-10 in docs/INVARIANTS.md.
const (
	ScopeCurrent  = "current"  // session if bound, else operator (default)
	ScopeSession  = "session"  // exactly the active session (back-compat)
	ScopeProject  = "project"  // the active project, all operators/sessions
	ScopeOperator = "operator" // the active operator, all projects (gated)
	ScopeAgent    = "agent"    // (project_id, agent_id) — Mem0 agent_id (v2.3.0)
	ScopeAll      = "all"      // everything in active project
)

// ValidScope returns true if s is a recognized scope filter value.
func ValidScope(s string) bool {
	switch s {
	case "", ScopeCurrent, ScopeSession, ScopeProject, ScopeOperator, ScopeAgent, ScopeAll:
		return true
	}
	return false
}

// MemoryType enumerates the Mem0-aligned three memory classes.
// Independent of Kind (which is the operator's tool filter — 10
// values: note, observation, decision, finding, todo, link,
// context, and free-form). MemoryType is the export contract; v2.4.0
// may also surface it in dark_memory_recall for cross-system port.
//
// Free-form strings are accepted at the SQL boundary for
// forward-compat, but the canonical three are listed here for tooling
// + tests. Mapping: episodic = KindObservation + event-anchored;
// semantic = atemporal facts (KindDecision, KindFinding, KindNote);
// procedural = learned workflows (how-to instructions, typically
// pinned).
const (
	MemoryTypeEpisodic   = "episodic"   // event-anchored; mirrors Mem0 episodic
	MemoryTypeSemantic   = "semantic"   // atemporal facts; mirrors Mem0 semantic
	MemoryTypeProcedural = "procedural" // learned workflows; mirrors Mem0 procedural
)

// ValidMemoryType returns true if t is one of the three canonical
// Mem0-aligned memory classes. Free-form values are accepted by the
// Store but rejected here for callers that want forward-compat.
func ValidMemoryType(t string) bool {
	switch t {
	case MemoryTypeEpisodic, MemoryTypeSemantic, MemoryTypeProcedural:
		return true
	case "": // empty = unset; allowed
		return true
	}
	return false
}

// AgentMemory is the row shape for the agent_memory table.
//
// SessionID is OPTIONAL metadata (v2.3.0). Pre-v2.3.0 the
// orchestrator auto-bound session_id from the active session; that
// behavior was removed because it caused rows to "disappear" via
// scope=session / scope=operator after session close. v2.3.0 callers
// MUST pass session_id explicitly via the wire (via AgentMemorySaveInput
// / agent_memory_save's bind_session flag) if they want a session
// tag; otherwise the row is born with session_id = "" and is
// accessible from any session within the same (project_id, agent_id).
//
// AgentID is the Mem0 agent_id field (v2.3.0): identifies the LLM
// that owns the memory, distinct from Operator (which is the human/
// agent identity per dark-memory INV-1 audit trail). For a single-
// agent tool flow, AgentID and Operator may carry the same value.
//
// MemoryType is the Mem0-aligned three-class taxonomy (episodic /
// semantic / procedural); independent of Kind. Both columns are
// nullable; callers can save without setting either.
//
// EmbeddedHook (v2.5.0 placeholder; reserved column NOT used in
// v2.3.0) and QuarantinedUntil (v2.5.0 placeholder) are documented
// here for forward-compat but the schema column does not exist yet.
type AgentMemory struct {
	ID         int64  `json:"id"`
	ProjectID  string `json:"project_id"`
	SessionID  string `json:"session_id,omitempty"`
	Operator   string `json:"operator"`
	AgentID    string `json:"agent_id,omitempty"`
	Kind       string `json:"kind"`
	MemoryType string `json:"memory_type,omitempty"`
	Title      string `json:"title,omitempty"`
	Content    string `json:"content"`
	Tags       string `json:"tags,omitempty"`
	Pinned     bool   `json:"pinned"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	ArchivedAt string `json:"archived_at,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	// QuarantinedUntil is reserved for v2.5.0 (memory-poisoning
	// defenses, zylos.ai 2026-04-05 §6). v2.3.0 schema does NOT
	// include this column; the field always returns "". Carried
	// here as wire-contract placeholder for tool forward-compat.
	QuarantinedUntil string `json:"quarantined_until,omitempty"`

	// Embedding is the optional dense vector for hybrid retrieval
	// (PR-2 of v2.9.0 plan, agent_memory row 160). Persisted as BLOB
	// (SQLite) or BYTEA (Postgres). nil/empty means no embedding
	// computed → row is invisible to Mode="vector"/"rrf" search.
	//
	// The vector is round-tripped through the embedder factory's
	// Dim() so callers can deserialize without knowing the provider.
	// Encoding: contiguous little-endian float32, length = Dim().
	// See internal/store/sqlite/vector.go for the encode/decode +
	// brute-force cosine.
	//
	// v2.9.0 schema (v23 migration): `agent_memory.embedding BLOB NULL`.
	Embedding embedder.Vec `json:"-"`

	// Entities is a transient field (json:"-") carrying the
	// extracted entity list for THIS row's content + title + tags.
	// Populated by the orchestrator (via internal/entity.Extract)
	// only when AgentMemorySaveInput.ExtractEntities = true; the
	// Store writes them to the agent_memory_entities side table in
	// the same tx as the main INSERT.
	//
	// PR-3 of v2.9.0 plan (agent_memory row 160). Backward compat:
	// when Entities is nil/empty, no rows land in
	// agent_memory_entities — pre-PR-3 callers see no change.
	//
	// Source tag is "deterministic" for PR-3 (the local heuristic
	// from internal/entity); PR-3.1 will add Source="drift_judge:..."
	// once the LLM-driven extractor lands.
	Entities []entity.Entity `json:"-"`
}

// IsArchived returns true if the row has been soft-deleted.
func (m *AgentMemory) IsArchived() bool {
	return m.ArchivedAt != ""
}

// Entity is one extracted noun phrase from a row's payload. It
// is a Go type alias for entity.Entity so callers (orchestrator,
// store, tool) can share the same shape without an explicit
// import dance. The internal/entity package owns the producer
// logic (deterministic extractor for PR-3, drift_judge bridge
// for PR-3.1).
//
// v2.9.0 PR-3 (agent_memory row 160). The Source tag distinguishes
// rows: "deterministic" for PR-3, "drift_judge:<prompt>" for
// PR-3.1. Confidence is always 1.0 for PR-3 (no model to score
// against); future LLM-driven extractors emit 0 < c < 1.
type Entity = entity.Entity

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
//
// v2.3.0 added MemoryType (Mem0 three-class taxonomy). Operator
// and ProjectID remain immutable (those are identity / INV-7
// tags, not editable content).
type AgentMemoryUpdate struct {
	Title      *string `json:"title,omitempty"`
	Content    *string `json:"content,omitempty"`
	Tags       *string `json:"tags,omitempty"`
	Pinned     *bool   `json:"pinned,omitempty"`
	ExpiresAt  *string `json:"expires_at,omitempty"`
	MemoryType *string `json:"memory_type,omitempty"`
}

// AgentMemoryListFilters are the optional filters for List. Zero
// value is "all rows in active project, excluding archived, sorted
// pinned DESC + created_at DESC".
//
// v2.3.0 added Operator + AgentID fields so the scope=operator and
// scope=agent resolutions work from caller intent (NOT from the
// unreliable write_audit.actor lookup that v2.1.x used). See INV-10.
type AgentMemoryListFilters struct {
	// Scope narrows the result set. Empty = ScopeProject (v2.3.0
	// change; pre-v2.3.0 default was ScopeSession when bound).
	// Allowed: current|session|project|operator|agent|all.
	Scope string

	// Operator narrows to rows where row.operator == filter.Operator.
	// Required for scope=operator (the resolver previously used
	// write_audit.actor; v2.3.0 uses THIS field).
	Operator string

	// AgentID narrows to rows where row.agent_id == filter.AgentID.
	// Required for scope=agent (v2.3.0 NEW).
	AgentID string

	// Kind filters to one kind. Empty = any kind.
	Kind string

	// MemoryType filters to one Mem0 memory class
	// (episodic|semantic|procedural). Empty = any.
	MemoryType string

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
//
// v2.3.0 added Operator + AgentID + MemoryType fields so the recall
// path mirrors the list filters (governance: caller intent, not
// audit side-channel).
//
// v2.9.0 (PR-2 of the v2.9.0 plan, agent_memory row 160) added Mode
// + RRFK + RRFWeights to dispatch hybrid retrieval:
//   - Mode = "bm25" (default, backward-compat): FTS5 BM25 only.
//   - Mode = "vector": brute-force cosine against stored embeddings.
//     Requires the active Embedder to be non-None; the Store uses
//     it to embed f.Query.
//   - Mode = "rrf": parallel BM25 + vector, fused via RRF (k=RRFK,
//     weights = RRFWeightBM25 + RRFWeightVector).
//
// Mode is opt-in per call. Operators who don't set Mode get the
// pre-PR-2 behavior (BM25 only).
type SearchFilters struct {
	Query string

	// Kind narrows the result set. Empty = any.
	Kind string

	// MemoryType narrows to one Mem0 class
	// (episodic|semantic|procedural). Empty = any.
	MemoryType string

	// Scope narrows the result set by ownership axis. Empty =
	// ScopeProject (all operators/sessions in the active project).
	// ScopeOperator additionally narrows by Operator; ScopeAgent by
	// AgentID; ScopeSession by the active session. v2.21.0 audit fix
	// (spec 1200 T2): previously the BM25 path applied
	// row.operator = filter.Operator whenever Operator was non-empty,
	// which made recall return only the caller's own rows — the
	// operator's human rows were invisible. Scope is the explicit
	// opt-in for ownership filtering; Operator alone (required for
	// INV-1 audit) is NOT an ownership filter.
	Scope string

	// Operator is the caller identity for INV-1 audit + result
	// attribution. It is NOT an implicit ownership filter: it only
	// narrows the result set when Scope == ScopeOperator. INV-7 +
	// INV-10 compliance.
	Operator string

	// AgentID narrows to rows where row.agent_id == filter.AgentID.
	AgentID string

	// Limit caps the result count. 0 = default (50).
	Limit int

	// Mode selects the retrieval axis. Empty = "bm25" (backward
	// compat). Allowed: "bm25" | "vector" | "rrf".
	//
	// "vector" and "rrf" require the active Embedder to be non-None.
	// If the Embedder is None(), the Store returns
	// embedder.ErrDisabled wrapped (test-friendly via errors.Is);
	// callers can degrade gracefully by falling back to Mode="bm25".
	Mode string

	// RRFK is the Reciprocal Rank Fusion constant (PR-2, row 160).
	// Used only when Mode == "rrf". 0 → default 60.
	RRFK int

	// RRFWeightBM25 is the BM25 arm weight in RRF fusion. Used only
	// when Mode == "rrf". 0 → default 1.0.
	RRFWeightBM25 float64

	// RRFWeightVector is the vector arm weight in RRF fusion. Used
	// only when Mode == "rrf". 0 → default 1.0.
	RRFWeightVector float64

	// Entities restricts to rows that have ALL the listed entity
	// strings in their extracted entity set (case-insensitive
	// equality, AND semantics). Empty = unfiltered (row 160 PR-3
	// backward compat: Entity filter is opt-in).
	//
	// Implementation: EXISTS join on agent_memory_entities
	// (mem_id, entity) WHERE entity IN (...). Rows without
	// entities never appear when this filter is non-empty.
	//
	// Examples:
	//   Entities: ["dark-memory", "vector-search"] returns rows
	//   extracted with both entities.
	//   Entities: ["openai"] returns rows that mention OpenAI as
	//   an entity. Stored values are lowercase per the entity
	//   package contract (caller must lowercase before passing
	//   to keep filter deterministic across case variants — or
	//   the Store does the lowercase on its side; see PR-3
	//   reader note).
	Entities []string
}

// SearchHit extends AgentMemory with the FTS5 BM25 rank (lower is
// better; 0 means exact match for some queries). Negative scores
// are valid FTS5 output — the Store surfaces the raw value.
//
// v2.9.0 (PR-2) extends this with rank + score fields per axis so
// Mode="rrf" + Mode="vector" + Mode="bm25" all return the same
// struct. -1 (negative one) means "axis did not contribute" — e.g.,
// when a row has no stored embedding, VectorRank = -1.
//
// The legacy `Rank` field is preserved as the BM25 axis numeric so
// pre-PR-2 callers see no breakage.
type SearchHit struct {
	AgentMemory
	Rank float64 `json:"rank"`

	// BM25Rank is the FTS5 rank position (1-based) when the BM25
	// axis contributed. 0 means "not ranked by BM25 axis".
	BM25Rank int `json:"bm25_rank,omitempty"`

	// VectorRank is the brute-force cosine position (1-based) when
	// the vector axis contributed. 0 means "not ranked by vector
	// axis" (e.g., row has no embedding).
	VectorRank int `json:"vector_rank,omitempty"`

	// RRFScore is the Reciprocal Rank Fusion score summed across axes.
	// Only populated when Mode == "rrf"; 0 otherwise.
	RRFScore float64 `json:"rrf_score,omitempty"`
}
