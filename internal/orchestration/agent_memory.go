// Orchestrator input/output shapes + methods for the AGENT_MEMORY
// namespace (v2.1.0).
//
// See internal/agentmemory/types.go for the row type + scope model,
// and internal/tools/agent_memory.go for the tool surface.
//
// Wire contract (v2.1.0):
//   - AgentMemorySave:      inserts one row, returns id + row
//   - AgentMemoryList:      filterable list, sorted pinned DESC + created_at DESC
//   - AgentMemoryGet:       by id (INV-7: cross-project returns ErrNotFound)
//   - AgentMemoryUpdate:    partial update; only the fields the caller sends
//   - AgentMemoryArchive:   soft delete (idempotent)
//
// All 5 require an active project (Store.requireProject). The
// orchestrator layer does NOT explicitly check for an active session
// — that's the gate's job (the 5 tools are not in the gate's
// allowlist, so RequiresActiveSession returns true for them, which
// means the gate blocks calls without a session).
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/entity"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// --- Save -----------------------------------------------------------

// AgentMemorySaveInput is the request to create one row. Operator +
// kind + content are required (per the schema in tools/agent_memory.go).
//
// v2.3.0 added AgentID (Mem0 agent_id; the LLM that owns the
// memory) and MemoryType (Mem0 three-class taxonomy:
// episodic|semantic|procedural; empty = unclassified). Both are
// optional. BindSession replaces the v2.1.x implicit auto-bind; the
// caller must explicitly opt in to attach a session tag (see
// AgentMemorySave below).
type AgentMemorySaveInput struct {
	Operator    string `json:"operator"`
	AgentID     string `json:"agent_id,omitempty"`
	Kind        string `json:"kind"`
	MemoryType  string `json:"memory_type,omitempty"`
	Title       string `json:"title,omitempty"`
	Content     string `json:"content"`
	Tags        string `json:"tags,omitempty"`
	Pinned      bool   `json:"pinned,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	BindSession bool   `json:"bind_session,omitempty"`

	// ExtractEntities (v2.9.0 PR-3, agent_memory row 160) runs the
	// deterministic extractor (internal/entity) on
	// content+title+tags and writes the result to
	// agent_memory_entities IN THE SAME tx as the main INSERT.
	// Default false: backward-compat with PR-0..PR-2 callers.
	ExtractEntities bool `json:"extract_entities,omitempty"`
}

// AgentMemorySaveOutput is the row as stored (with project_id +
// session_id resolved from context).
type AgentMemorySaveOutput struct {
	Row     agentmemory.AgentMemory `json:"row"`
	AuditID int64                   `json:"audit_id"`
}

// AgentMemorySave inserts one row into the active project.
//
// v2.3.0 BindSession contract:
//   - BindSession = false (default): session_id is NOT auto-bound.
//     The row is born with session_id = "" and survives session
//     close. INV-10.
//   - BindSession = true: session_id is captured from the active
//     session at the Store layer (existing behavior). The row is
//     tagged with the session at creation time; closing the session
//     makes it invisible via scope=session but NOT via scope=agent,
//     scope=operator, or scope=project. v2.3.0 callers should use
//     BindSession only for rows they explicitly want pinned to a
//     single session lifecycle (rare).
func (o *Orchestrator) AgentMemorySave(ctx context.Context, in AgentMemorySaveInput) (*AgentMemorySaveOutput, error) {
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}
	if strings.TrimSpace(in.Content) == "" {
		return nil, errMissingField("content")
	}
	if !agentmemory.ValidKind(in.Kind) {
		return nil, fmt.Errorf("agent_memory_save: invalid kind %q", in.Kind)
	}
	if !agentmemory.ValidMemoryType(in.MemoryType) {
		return nil, fmt.Errorf("agent_memory_save: invalid memory_type %q", in.MemoryType)
	}

	// v2.8.0-alpha C2: when DARK_MEMORY_V280=1, the agent_id
	// resolution priority chain extends with step 2 (active
	// subagent_id). The caller-provided AgentID still wins
	// (priority 1); only when the caller leaves AgentID empty do
	// we consult the active_subagents table. When the flag is off,
	// this falls back to the v2.7.x priority chain (caller >
	// project default > "").
	resolvedAgentID := in.AgentID
	if v280Enabled() && strings.TrimSpace(resolvedAgentID) == "" {
		activeOp := o.activeOperator(ctx)
		if activeOp != "" {
			resolvedAgentID = o.resolveActiveAgentIDWithSubagent(ctx, "", activeOp)
		}
	}

	wc := store.WriteContext{
		Actor:     in.Operator,
		WritePath: "AgentMemorySave",
	}
	m := &agentmemory.AgentMemory{
		Operator:   in.Operator,
		AgentID:    resolvedAgentID,
		Kind:       in.Kind,
		MemoryType: in.MemoryType,
		Title:      in.Title,
		Content:    in.Content,
		Tags:       in.Tags,
		Pinned:     in.Pinned,
		ExpiresAt:  in.ExpiresAt,
	}
	// v2.3.0: bind_session is caller-driven (not implicit).
	// Pre-v2.3.0 behavior auto-bound here at the orchestrator level
	// too (via SaveAgentMemory's resolveActiveSessionID call); v2.3.0
	// honors BindSession explicitly. If the caller wants the row
	// tied to the session lifecycle, they MUST set BindSession=true
	// AND have an active session at save time.
	if in.BindSession {
		if sid, err := o.Store.GetActiveSession(ctx, o.Store.ActiveProject()); err == nil && sid != "" {
			m.SessionID = sid
		}
	}
	// v2.9.0 PR-3 (agent_memory row 160): when ExtractEntities is
	// true, run the deterministic extractor (internal/entity) on
	// content+title+tags and populate the transient m.Entities
	// field. The Store persists the entity rows in the SAME tx as
	// the main INSERT (atomic with the row per row 160 PR-3 spec).
	// PR-3 minimum uses the local heuristic; PR-3.1 swaps the body
	// for a drift_judge bridge without touching the contract.
	if in.ExtractEntities {
		m.Entities = entity.Extract(in.Content, in.Title, in.Tags)
	}
	id, err := o.Store.SaveAgentMemory(ctx, wc, m)
	if err != nil {
		return nil, err
	}
	// The Store mutates m to fill ProjectID/CreatedAt/UpdatedAt.
	// Re-fetch to also surface SessionID (the Store knows it but
	// doesn't mutate m.SessionID in our impl — keep it consistent).
	got, err := o.Store.GetAgentMemory(ctx, id)
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, fmt.Errorf("agent_memory_save: row vanished after insert")
	}
	// Best-effort audit id read for the response. We don't fail the
	// call if audit_id can't be resolved; the audit row IS created
	// atomically (INV-1) but its id isn't surfaced by the Store
	// interface. Use a fresh write query for the most recent
	// SaveAgentMemory audit row on this row_id.
	auditID, _ := o.latestAuditIDForRow(ctx, "agent_memory", id)

	return &AgentMemorySaveOutput{Row: *got, AuditID: auditID}, nil
}

// --- List -----------------------------------------------------------

// AgentMemoryListInput is the filter set for list. All fields are
// optional; the zero value means "give me the current scope's most
// recent rows, default limit, excluding archived".
//
// v2.3.0: Scope defaults to "project" (NOT "session") and Operator
// is required when Scope=operator; AgentID is required when
// Scope=agent. MemoryType added as a Mem0-class filter.
type AgentMemoryListInput struct {
	Scope           string `json:"scope,omitempty"`
	Operator        string `json:"operator,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	Kind            string `json:"kind,omitempty"`
	MemoryType      string `json:"memory_type,omitempty"`
	Tag             string `json:"tag,omitempty"`
	PinnedOnly      bool   `json:"pinned_only,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

// AgentMemoryListOutput is the list + a count for caller convenience.
type AgentMemoryListOutput struct {
	Rows  []agentmemory.AgentMemory `json:"rows"`
	Count int                       `json:"count"`
}

// AgentMemoryList returns rows matching the filters, with INV-7
// (project isolation) enforced by the Store. Scope is resolved at
// the Store layer against the active session / operator.
//
// v2.3.0: the orchestrator previously did NOT plumb Operator through
// to the Store (the Store's pre-v2.3.0 scope=operator resolution
// used write_audit.actor, which was unreliable — see INV-10). The
// orchestrator MUST forward in.Operator to f.Operator so callers
// can request rows by their own identity, not by an audit side-channel.
func (o *Orchestrator) AgentMemoryList(ctx context.Context, in AgentMemoryListInput) (*AgentMemoryListOutput, error) {
	f := agentmemory.AgentMemoryListFilters{
		Scope:           in.Scope,
		Operator:        in.Operator,
		AgentID:         in.AgentID,
		Kind:            in.Kind,
		MemoryType:      in.MemoryType,
		Tag:             in.Tag,
		PinnedOnly:      in.PinnedOnly,
		IncludeArchived: in.IncludeArchived,
		Limit:           in.Limit,
	}
	rows, err := o.Store.ListAgentMemory(ctx, f)
	if err != nil {
		return nil, err
	}
	return &AgentMemoryListOutput{Rows: rows, Count: len(rows)}, nil
}

// --- Recall ---------------------------------------------------------

// AgentMemoryRecallInput is the BM25-ranked search over the agent
// memory data plane. Query is required (FTS5 escape is handled by
// the tool layer). v2.3.0 NEW.
//
// Filter semantics mirror agent_memory_list: Operator scopes by
// caller's identity, AgentID scopes by Mem0 agent_id, MemoryType
// scopes by Mem0 three-class taxonomy, Kind scopes by operator's
// 10-kind taxonomy. All filters are AND-combined.
type AgentMemoryRecallInput struct {
	Operator   string `json:"operator,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	Query      string `json:"query"`
	Kind       string `json:"kind,omitempty"`
	MemoryType string `json:"memory_type,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// AgentMemoryRecallOutput is the hits + a count.
type AgentMemoryRecallOutput struct {
	Hits  []agentmemory.SearchHit `json:"hits"`
	Count int                    `json:"count"`
}

// AgentMemoryRecall runs FTS5 BM25-ranked search over
// content+title+tags in the active project's agent_memory table.
// Caller must supply Operator (their own identity) so the query can
// be attributed for INV-1 audit and so the result set is scoped
// appropriately for compliance.
func (o *Orchestrator) AgentMemoryRecall(ctx context.Context, in AgentMemoryRecallInput) (*AgentMemoryRecallOutput, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, errMissingField("query")
	}
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}
	if !agentmemory.ValidMemoryType(in.MemoryType) {
		return nil, fmt.Errorf("agent_memory_recall: invalid memory_type %q", in.MemoryType)
	}
	hits, err := o.Store.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:      in.Query,
		Operator:   in.Operator,
		AgentID:    in.AgentID,
		Kind:       in.Kind,
		MemoryType: in.MemoryType,
		Limit:      in.Limit,
	})
	if err != nil {
		return nil, err
	}
	return &AgentMemoryRecallOutput{Hits: hits, Count: len(hits)}, nil
}

// --- v2.4.0 helpers (memory RAG into the vibe-loop) ---------------
//
// These helpers exist so the VLP integration points (session_start,
// publish_vibe/drift_judge, research_topic, judge, consensus) can
// consult agent_memory without each call site re-implementing FTS5
// escape + the Store filter shape. Errors are SWALLOWED
// (best-effort): a broken agent_memory store MUST NOT block a
// session_start or a drift_judge — the vibe-loop has higher
// priority than the memory layer.
//
// All helpers require an active project (Store.requireProject at the
// SearchAgentMemory boundary). If no project is active, they return
// empty results and a nil error; the caller treats that as "no
// context to inject".

// recallForVibe runs a BM25-ranked search over the active project's
// agent_memory using the caller-provided query. Filters narrow by
// Kind + MemoryType + AgentID; empty filters = any. limit clamps to
// [1, 50] (Store enforces 200 max, but VLP callers typically want 3-10).
//
// Returns hits + an error. Errors are non-fatal at the call site;
// this helper returns them so the caller can choose to log them.
func (o *Orchestrator) recallForVibe(ctx context.Context, query, kind, memoryType, agentID string, limit int) ([]agentmemory.SearchHit, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	hits, err := o.Store.SearchAgentMemory(ctx, agentmemory.SearchFilters{
		Query:      query,
		Kind:       kind,
		MemoryType: memoryType,
		AgentID:    agentID,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// listPinnedForVibe returns the most recent pinned rows in the active
// project, optionally filtered by kind + agent_id. v2.4.1: added
// agentID parameter so SessionStart can scope the recap to the
// resolved agent (per Mem0 agent_id semantics). Empty agentID =
// project-wide (v2.4.0 backward compat). limit defaults to 10, max 50.
func (o *Orchestrator) listPinnedForVibe(ctx context.Context, kind, agentID string, limit int) ([]agentmemory.AgentMemory, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := o.Store.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:      agentmemory.ScopeProject,
		Kind:       kind,
		AgentID:    agentID,
		PinnedOnly: true,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// listOpenTodosForVibe returns kind=todo rows in the active project,
// scoped by AgentID filter if set. Used by SessionStart so the
// operator sees their pending work without re-listing manually.
// Note: by v2.5.0 this should respect a "due_at" / status column
// (deferred). For v2.4.0 we return all kind=todo rows.
func (o *Orchestrator) listOpenTodosForVibe(ctx context.Context, agentID string, limit int) ([]agentmemory.AgentMemory, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := o.Store.ListAgentMemory(ctx, agentmemory.AgentMemoryListFilters{
		Scope:   agentmemory.ScopeProject,
		Kind:    "todo",
		AgentID: agentID,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// formatHitsForContext formats a list of SearchHits as a
// human-readable block suitable for prepending to an LLM judge
// prompt or to a research_topic output. The format is:
//
//	=== Relevant prior context (n hits) ===
//	[kind=decision] "<title or first 80 chars of content>"
//	[kind=decision] "<title or first 80 chars of content>"
//	...
//
// Empty list returns "" so callers can branch on len==0 / result==""
// without parsing newlines. v2.4.0 returns the human-readable shape;
// v2.4.x follow-ups may add JSON-typed evidence (EvidenceFrame) if a
// downstream consumer wants machine-parseable citations.
func formatHitsForContext(hits []agentmemory.SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== Relevant prior context (%d hits) ===\n", len(hits))
	for _, h := range hits {
		title := h.Title
		if title == "" {
			// Use first 80 chars of content (word boundary).
			title = firstLine(h.Content, 80)
		}
		fmt.Fprintf(&b, "[kind=%s", h.Kind)
		if h.MemoryType != "" {
			fmt.Fprintf(&b, " memory_type=%s", h.MemoryType)
		}
		if h.AgentID != "" {
			fmt.Fprintf(&b, " agent_id=%s", h.AgentID)
		}
		fmt.Fprintf(&b, "] %s\n", title)
	}
	return b.String()
}

// firstLine returns up to maxChars of the first line of s. If the
// first line is longer than maxChars, the truncation appends "…".
// Used as a fallback title in formatHitsForContext when the row
// has no explicit title.
func firstLine(s string, maxChars int) string {
	for i, r := range s {
		if r == '\n' || r == '\r' {
			return s[:i]
		}
		if i >= maxChars {
			return s[:maxChars] + "…"
		}
	}
	return s
}

// --- Get ------------------------------------------------------------

// AgentMemoryGetInput is the request for one row by id.
type AgentMemoryGetInput struct {
	ID int64 `json:"id"`
}

// AgentMemoryGetOutput is the row.
type AgentMemoryGetOutput struct {
	Row agentmemory.AgentMemory `json:"row"`
}

// AgentMemoryGet returns one row by id.
//
// v2.8.0-alpha D5: when DARK_MEMORY_V280=1, cross-project reads
// return *store.CrossProjectAccessError (wrapping
// store.ErrCrossProjectAccess). When the flag is off, the v2.7.x
// behavior is preserved: cross-project reads return (nil, nil) just
// like "not found" — to avoid leaking existence.
func (o *Orchestrator) AgentMemoryGet(ctx context.Context, in AgentMemoryGetInput) (*AgentMemoryGetOutput, error) {
	if in.ID <= 0 {
		return nil, errMissingField("id")
	}
	row, err := o.Store.GetAgentMemory(ctx, in.ID)
	if err != nil {
		// v2.8.0-alpha D5: when the flag is on, propagate the typed
		// CrossProjectAccessError directly — it implements Is(target)
		// for the sentinel AND its Error() method carries the same
		// diagnostic. When the flag is off, convert to ErrNotFound
		// for v2.7.x backward compat (no existence leak).
		//
		// NOTE: previously this wrapped with fmt.Errorf("%w", sentinel)
		// which dropped the typed struct, causing errors.As to fail in
		// the tools layer. Direct return preserves the struct.
		var cpe *store.CrossProjectAccessError
		if errors.As(err, &cpe) {
			if v280Enabled() {
				return nil, cpe
			}
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if row == nil {
		return nil, store.ErrNotFound
	}
	return &AgentMemoryGetOutput{Row: *row}, nil
}

// --- Update ---------------------------------------------------------

// AgentMemoryUpdateInput is the partial update. Pointer fields let
// the caller leave a field unchanged while still allowing "set to
// empty string". The orchestrator layer forwards to the Store as-is
// (Store treats nil = no-op, "" = explicit clear).
type AgentMemoryUpdateInput struct {
	ID        int64   `json:"id"`
	Operator  string  `json:"operator,omitempty"`
	Title     *string `json:"title,omitempty"`
	Content   *string `json:"content,omitempty"`
	Tags      *string `json:"tags,omitempty"`
	Pinned    *bool   `json:"pinned,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// AgentMemoryUpdateOutput is the refreshed row + audit id.
type AgentMemoryUpdateOutput struct {
	Row     agentmemory.AgentMemory `json:"row"`
	AuditID int64                   `json:"audit_id"`
}

// AgentMemoryUpdate applies the partial update. The caller must
// supply ID + Operator (Operator is logged for the audit row's
// Actor field; Store doesn't use it for authz in v2.1.0).
func (o *Orchestrator) AgentMemoryUpdate(ctx context.Context, in AgentMemoryUpdateInput) (*AgentMemoryUpdateOutput, error) {
	if in.ID <= 0 {
		return nil, errMissingField("id")
	}
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}

	wc := store.WriteContext{
		Actor:     in.Operator,
		WritePath: "AgentMemoryUpdate",
	}
	u := &agentmemory.AgentMemoryUpdate{
		Title:     in.Title,
		Content:   in.Content,
		Tags:      in.Tags,
		Pinned:    in.Pinned,
		ExpiresAt: in.ExpiresAt,
	}
	row, err := o.Store.UpdateAgentMemory(ctx, wc, in.ID, u)
	if err != nil {
		return nil, err
	}
	auditID, _ := o.latestAuditIDForRow(ctx, "agent_memory", in.ID)
	return &AgentMemoryUpdateOutput{Row: *row, AuditID: auditID}, nil
}

// --- Archive --------------------------------------------------------

// AgentMemoryArchiveInput is the request to soft-delete one row.
type AgentMemoryArchiveInput struct {
	ID       int64  `json:"id"`
	Operator string `json:"operator,omitempty"`
}

// AgentMemoryArchiveOutput is a small confirmation shape: the id +
// the archived_at timestamp.
type AgentMemoryArchiveOutput struct {
	ID         int64  `json:"id"`
	ArchivedAt string `json:"archived_at"`
}

// AgentMemoryArchive soft-deletes the row. Idempotent: a second call
// for an already-archived row returns the same ArchivedAt without
// double-counting in the audit log (Store emits audit only on the
// first transition).
func (o *Orchestrator) AgentMemoryArchive(ctx context.Context, in AgentMemoryArchiveInput) (*AgentMemoryArchiveOutput, error) {
	if in.ID <= 0 {
		return nil, errMissingField("id")
	}
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}

	wc := store.WriteContext{
		Actor:     in.Operator,
		WritePath: "AgentMemoryArchive",
	}
	if err := o.Store.ArchiveAgentMemory(ctx, wc, in.ID); err != nil {
		return nil, err
		return nil, err
	}
	// Re-fetch to surface the canonical archived_at timestamp.
	got, err := o.Store.GetAgentMemory(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if got == nil {
		// Race: row was hard-deleted between archive and refetch.
		// Treat as success (the archive did happen) but surface nil.
		return &AgentMemoryArchiveOutput{ID: in.ID, ArchivedAt: ""}, nil
	}
	return &AgentMemoryArchiveOutput{ID: in.ID, ArchivedAt: got.ArchivedAt}, nil
}

// --- Entities (v2.9.0-alpha PR-3) ----------------------------------

// AgentMemoryEntitiesInput is the request to fetch the extracted
// entity list for one agent_memory row. v2.9.0 PR-3.
type AgentMemoryEntitiesInput struct {
	ID int64 `json:"id"`
}

// AgentMemoryEntitiesOutput is the row id + the entity list.
// Entities is nil when the row has no extracted entities (most
// common pre-PR-3 caller; extract_entities was unset on Save).
type AgentMemoryEntitiesOutput struct {
	ID       int64                `json:"id"`
	Entities []agentmemory.Entity `json:"entities"`
}

// AgentMemoryEntities returns the extracted entity list for one
// row. Cross-project reads return nil entities (INV-7). The Store
// enforces project isolation on the side-table join.
func (o *Orchestrator) AgentMemoryEntities(ctx context.Context, in AgentMemoryEntitiesInput) (*AgentMemoryEntitiesOutput, error) {
	if in.ID <= 0 {
		return nil, errMissingField("id")
	}
	ents, err := o.Store.GetAgentMemoryEntities(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if ents == nil {
		return &AgentMemoryEntitiesOutput{ID: in.ID, Entities: []agentmemory.Entity{}}, nil
	}
	return &AgentMemoryEntitiesOutput{ID: in.ID, Entities: ents}, nil
}

// --- helpers --------------------------------------------------------

// latestAuditIDForRow returns the most recent write_audit.id for the
// given table_name + row_id. Best-effort: errors are swallowed (the
// caller doesn't fail the user-facing operation on a transient audit
// lookup miss). Returns 0 if no audit row is found.
func (o *Orchestrator) latestAuditIDForRow(ctx context.Context, table string, rowID int64) (int64, error) {
	// We don't have a typed Store method for this; the Orchestrator's
	// only Store dependency is the abstract interface. Instead of
	// adding one, use the active project's project_id + a delegated
	// helper. The simplest approach: query via a Store.ListWrites call
	// with a tight filter.
	//
	// For v2.1.0 we accept "audit_id may be 0" as a documented
	// limitation; F47 tracks adding a typed method.
	if o.Store == nil {
		return 0, nil
	}
	// Best-effort 0 — the Store enforces INV-1; we just can't
	// surface the id without a new method.
	return 0, nil
}

// errMissingField is the canonical "field X is required" error. It
// is defined in orchestrator.go (line 110) and reused here.

// --- imports kept live ----------------------------------------------

// errInvalidArgument surfaces an upstream ErrInvalidArgument. The
// orchestration layer doesn't translate; the tool layer's
// ToToolError handles the type assertion.
var _ = errors.Is

// keep time import referenced (some builds strip unused imports via
// goimports; this no-op assignment prevents that even when the
// package's only time-typed code is removed).
var _ = time.Now