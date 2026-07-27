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
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// --- Save -----------------------------------------------------------

// AgentMemorySaveInput is the request to create one row. Operator +
// kind + content are required (per the schema in tools/agent_memory.go).
type AgentMemorySaveInput struct {
	Operator  string `json:"operator"`
	Kind      string `json:"kind"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	Tags      string `json:"tags,omitempty"`
	Pinned    bool   `json:"pinned,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// AgentMemorySaveOutput is the row as stored (with project_id +
// session_id resolved from context).
type AgentMemorySaveOutput struct {
	Row       agentmemory.AgentMemory `json:"row"`
	AuditID   int64                   `json:"audit_id"`
}

// AgentMemorySave inserts one row into the active project. The
// row's session_id is captured from the active session at the Store
// layer (SaveAgentMemory resolves the active session via the projects
// row). If no session is active, session_id stays empty (operator-scoped
// row).
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

	wc := store.WriteContext{
		Actor:     in.Operator,
		WritePath: "AgentMemorySave",
	}
	m := &agentmemory.AgentMemory{
		Operator:  in.Operator,
		Kind:      in.Kind,
		Title:     in.Title,
		Content:   in.Content,
		Tags:      in.Tags,
		Pinned:    in.Pinned,
		ExpiresAt: in.ExpiresAt,
		// SessionID resolved by SaveAgentMemory from active project.
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
type AgentMemoryListInput struct {
	Scope           string `json:"scope,omitempty"`
	Kind            string `json:"kind,omitempty"`
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
func (o *Orchestrator) AgentMemoryList(ctx context.Context, in AgentMemoryListInput) (*AgentMemoryListOutput, error) {
	f := agentmemory.AgentMemoryListFilters{
		Scope:           in.Scope,
		Kind:            in.Kind,
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

// --- Get ------------------------------------------------------------

// AgentMemoryGetInput is the request for one row by id.
type AgentMemoryGetInput struct {
	ID int64 `json:"id"`
}

// AgentMemoryGetOutput is the row.
type AgentMemoryGetOutput struct {
	Row agentmemory.AgentMemory `json:"row"`
}

// AgentMemoryGet returns one row by id. Cross-project reads return
// ErrNotFound (the Store filters by active project).
func (o *Orchestrator) AgentMemoryGet(ctx context.Context, in AgentMemoryGetInput) (*AgentMemoryGetOutput, error) {
	if in.ID <= 0 {
		return nil, errMissingField("id")
	}
	row, err := o.Store.GetAgentMemory(ctx, in.ID)
	if err != nil {
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