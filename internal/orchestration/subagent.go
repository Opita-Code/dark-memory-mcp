// O-X: Subagent register / unregister — v2.8.0-alpha C2 explicit
// subagent-binding tools. These complement the implicit registration
// that happens when mindset_apply(spawn_subagent=true, subagent_id=X)
// is called. Use these when the harness wants to register / clear a
// subagent binding WITHOUT going through mindset_apply (e.g. the
// subagent was spawned by an external tool, or the operator wants to
// clear a stale binding manually).
//
// Both are gated by DARK_MEMORY_V280=1. When the flag is off, the
// methods return ErrInvalidState with a clear "feature disabled"
// message — preserving v2.7.x behavior.
//
// SECURITY: subagent_id is opaque to the principal. The
// resolveActiveAgentIDWithSubagent chain only consults the
// active_subagents table; the principal cannot forge a subagent_id
// to inherit someone else's pinned memory (because the principal's
// own ContextRecap filters by projects.default_agent_id, not by
// subagent_id).
package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// --- Subagent Register ------------------------------------------------

// SubagentRegisterInput is the request to register or refresh an
// active subagent binding. Use after spawning a subagent via the
// harness's subagent tool (or to refresh a binding that's about to
// expire).
//
// When DARK_MEMORY_V280=0, the method returns ErrInvalidState. The
// caller can check v280Enabled() externally if they want to skip the
// call entirely.
type SubagentRegisterInput struct {
	Operator      string `json:"operator"`
	SubagentID    string `json:"subagent_id"`
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	TTLSeconds    int    `json:"ttl_seconds,omitempty"`
}

// SubagentRegisterOutput echoes the registered row + the resolved
// parent agent_id (from projects.default_agent_id when caller left
// ParentAgentID empty).
type SubagentRegisterOutput struct {
	RowID         int64  `json:"row_id"`
	SubagentID    string `json:"subagent_id"`
	ParentAgentID string `json:"parent_agent_id"`
	SpawnedAt     string `json:"spawned_at"`
	TTLSeconds    int    `json:"ttl_seconds"`
}

// SubagentRegister registers or refreshes the active subagent
// binding for the current (project_id, operator). Idempotent:
// re-registering the same subagent_id refreshes spawned_at +
// parent_agent_id + ttl_seconds.
//
// TTL clamp: [60, 86400]. Default 3600.
func (o *Orchestrator) SubagentRegister(ctx context.Context, in SubagentRegisterInput) (*SubagentRegisterOutput, error) {
	if !v280Enabled() {
		return nil, fmt.Errorf("subagent_register: %w (DARK_MEMORY_V280 not enabled)", store.ErrInvalidState)
	}
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}
	if strings.TrimSpace(in.SubagentID) == "" {
		return nil, errMissingField("subagent_id")
	}

	ttl := in.TTLSeconds
	if ttl <= 0 {
		ttl = 3600
	}
	if ttl < 60 {
		ttl = 60
	}
	if ttl > 86400 {
		ttl = 86400
	}

	// Resolve parent_agent_id: caller > projects.default_agent_id.
	parentAgentID := in.ParentAgentID
	if strings.TrimSpace(parentAgentID) == "" {
		parentAgentID = o.resolveActiveAgentID(ctx, "")
	}

	wc := store.WriteContext{
		Actor:     in.Operator,
		WritePath: "SubagentRegister",
	}
	row := &store.ActiveSubagent{
		Operator:      in.Operator,
		SubagentID:    in.SubagentID,
		ParentAgentID: parentAgentID,
		TTLSeconds:    ttl,
	}
	id, err := o.Store.SetActiveSubagent(ctx, wc, row)
	if err != nil {
		return nil, fmt.Errorf("subagent_register: %w", err)
	}
	return &SubagentRegisterOutput{
		RowID:         id,
		SubagentID:    in.SubagentID,
		ParentAgentID: parentAgentID,
		SpawnedAt:     row.SpawnedAt,
		TTLSeconds:    ttl,
	}, nil
}

// --- Subagent Unregister ----------------------------------------------

// SubagentUnregisterInput is the request to clear an active
// subagent binding.
type SubagentUnregisterInput struct {
	Operator   string `json:"operator"`
	SubagentID string `json:"subagent_id"`
}

// SubagentUnregisterOutput confirms the clear (or reports
// ErrNotFound when nothing was bound).
type SubagentUnregisterOutput struct {
	Cleared     bool   `json:"cleared"`
	SubagentID  string `json:"subagent_id"`
	ClearedAtRFC string `json:"cleared_at"`
}

// SubagentUnregister clears the (project_id, operator, subagent_id)
// binding. After this call, the subagent's subsequent
// agent_memory_save calls will fall through to the project default
// agent_id (or empty). Returns ErrNotFound when no matching binding
// exists.
func (o *Orchestrator) SubagentUnregister(ctx context.Context, in SubagentUnregisterInput) (*SubagentUnregisterOutput, error) {
	if !v280Enabled() {
		return nil, fmt.Errorf("subagent_unregister: %w (DARK_MEMORY_V280 not enabled)", store.ErrInvalidState)
	}
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}
	if strings.TrimSpace(in.SubagentID) == "" {
		return nil, errMissingField("subagent_id")
	}

	wc := store.WriteContext{
		Actor:     in.Operator,
		WritePath: "SubagentUnregister",
	}
	if err := o.Store.ClearActiveSubagent(ctx, wc, in.Operator, in.SubagentID); err != nil {
		return nil, err
	}
	return &SubagentUnregisterOutput{
		Cleared:      true,
		SubagentID:   in.SubagentID,
		ClearedAtRFC: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}
