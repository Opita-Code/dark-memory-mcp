// O-X: resolveActiveAgentID — canonical active agent_id resolver for
// the VLP integration points (session_start, publish_vibe, research_topic,
// judge, consensus). Returns the Mem0 agent_id (LLM identity) that the
// current operation should use to filter agent_memory and scope
// per-agent decisions.
//
// v2.4.1: closes the agent_id plumbing debt that v2.3.0 introduced.
// Resolution priority:
//
//	1. caller-supplied AgentID on the request (per-call override).
//	   This is the parameter the integration passes; e.g.
//	   session_start.AgentID or publish_vibe.AgentID.
//	2. projects.default_agent_id (the v2.4.1 project-level default,
//	   migration v20). Set at tenant provisioning via
//	   dark_memory_project_create(default_agent_id=...).
//	3. Empty string — no agent filter. Backward compatible with
//	   v2.4.0 (project-wide scope; same agent sees all memories
//	   across all agents in the project).
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
