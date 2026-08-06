// O-X: resolveActiveAgentID — canonical active agent_id resolver for
// the VLP integration points (session_start, publish_vibe, research_topic,
// judge, consensus). Returns the Mem0 agent_id (LLM identity) that the
// current operation should use to filter agent_memory and scope
// per-agent decisions.
//
// v2.4.1: closes the agent_id plumbing debt that v2.3.0 introduced.
// Resolution priority:
//
//  1. caller-supplied AgentID on the request (per-call override).
//     This is the parameter the integration passes; e.g.
//     session_start.AgentID or publish_vibe.AgentID.
//  2. projects.default_agent_id (the v2.4.1 project-level default,
//     migration v20). Set at tenant provisioning via
//     dark_memory_project_create(default_agent_id=...).
//  3. Empty string — no agent filter. Backward compatible with
//     v2.4.0 (project-wide scope; same agent sees all memories
//     across all agents in the project).
//
// Best-effort: errors at the Store layer are swallowed (no logger
// yet). The active agent_id is operational metadata, not a
// correctness invariant — a missing resolution just falls back to
// project-wide scope. The vibe-loop MUST NOT be blocked by a broken
// agent_id lookup.
package orchestration

import (
	"context"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// resolveActiveAgentID applies the resolution priority documented in
// the package doc. Returns the effective agent_id (may be empty
// string) + a nil error on success or a swallowed error on Store
// failure (caller treats nil error + empty string the same as
// "no default configured").
//
// Invariants:
//   - The active project is required (Store.requireProject enforces
//     this on every Store call). resolveActiveAgentID reads the
//     project via Store.GetProject(ctx, activeProjectID).
//   - Empty caller-supplied AgentID is treated as "not provided",
//     falling through to projects.default_agent_id.
//   - Empty projects.default_agent_id is treated as "no default",
//     falling through to empty string ("no agent filter").
//
// Concurrency: resolveActiveAgentID is safe for concurrent use; the
// Store handles its own locking. The helper is read-only.
func (o *Orchestrator) resolveActiveAgentID(ctx context.Context, requestedAgentID string) string {
	// Priority 1: caller override.
	if strings.TrimSpace(requestedAgentID) != "" {
		return requestedAgentID
	}
	// Priority 2: project default. Best-effort; if GetProject fails
	// (e.g. transient DB error), we fall through to priority 3
	// rather than blocking the VLP path.
	activeProject := o.Store.ActiveProject()
	if activeProject == "" {
		return ""
	}
	proj, err := o.Store.GetProject(ctx, activeProject)
	if err != nil || proj == nil {
		// Swallow — same as "no default configured". The caller will
		// treat empty as project-wide scope.
		return ""
	}
	return proj.DefaultAgentID
}

// keep store import referenced (some builds strip unused imports
// via goimports; this no-op assignment prevents that even when
// the package's only store-typed code is removed).
var _ = store.ErrInvalidArgument

// activeOperator returns the operator id (INV-1 audit identity) of
// the currently-active session for the active project, or "" if no
// session is active. Best-effort: errors swallowed (same policy as
// resolveActiveAgentID).
//
// Used by:
//   - resolveActiveAgentIDWithSubagent (C2 subagent priority chain)
//   - SetActiveSubagent (writes the active_subagents row)
//
// Why not stored on the Orchestrator struct: sessions can be closed
// and new ones opened without the orchestrator knowing. The source
// of truth is projects.active_session_id, not an in-process cache.
func (o *Orchestrator) activeOperator(ctx context.Context) string {
	activeProject := o.Store.ActiveProject()
	if activeProject == "" {
		return ""
	}
	sessID, err := o.Store.GetActiveSession(ctx, activeProject)
	if err != nil || sessID == "" {
		return ""
	}
	sess, err := o.Store.GetSession(ctx, sessID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.Operator
}

// resolveActiveAgentIDWithSubagent is the v2.8.0-alpha C2 subagent-
// scope-handoff priority chain. Used by AgentMemorySave when
// DARK_MEMORY_V280=1. Falls back to resolveActiveAgentID when the
// flag is off (backward compat with v2.7.x).
//
// Priority chain (v2.8.0-alpha):
//  1. caller-supplied AgentID on the request (per-call override)
//  2. active subagent_id if any (NEW — see GetActiveSubagent)
//  3. projects.default_agent_id (project-level default)
//  4. Empty string (no agent filter — v2.4.0 behavior)
//
// SECURITY: step 2 is the defense-in-depth against arxiv:2605.08460
// inheritance attacks. The subagent's writes are tagged with the
// subagent's opaque uuid (NOT the principal's agent_id), so they
// never appear in the principal's ContextRecap. A poisoned subagent
// memory cannot contaminate the principal's pinned decisions.
func (o *Orchestrator) resolveActiveAgentIDWithSubagent(ctx context.Context, requestedAgentID, operator string) string {
	// Priority 1: caller override (always wins).
	if strings.TrimSpace(requestedAgentID) != "" {
		return requestedAgentID
	}
	// Priority 2: active subagent_id. Only when V280 is enabled
	// AND we have a non-empty operator to look up by.
	if v280Enabled() && strings.TrimSpace(operator) != "" {
		sub, err := o.Store.GetActiveSubagent(ctx, operator)
		if err == nil && sub != nil && strings.TrimSpace(sub.SubagentID) != "" {
			return sub.SubagentID
		}
		// Swallow errors (best-effort; defense-in-depth is the
		// sweeper, not this lookup).
	}
	// Priority 3: project default.
	activeProject := o.Store.ActiveProject()
	if activeProject == "" {
		return ""
	}
	proj, err := o.Store.GetProject(ctx, activeProject)
	if err != nil || proj == nil {
		return ""
	}
	return proj.DefaultAgentID
}
