// Package tools — registry.go: the Tool type and the canonical Registry.
//
// Per BRIDGE_AND_COEXISTENCE.md §3 (spec 164, bridge.4), the 28 tools
// are emitted in tools/list in a fixed canonical order. The order is
// NOT alphabetical — it follows the RFC D-9 namespace grouping plus
// the L6-VLP namespace (DMAP v1.1 spec 193): SESSION (4) →
// RESEARCH (3) → VIBE (4) → CONTEXT (3) → JUDGE (3) → POLICY (2) →
// OBSERVABILITY (4) → ADMIN (3) → L6-VLP (1). The order is part of
// the public contract: changing it is a breaking change for any
// harness that indexes by position.
//
// OBSERVABILITY grew from 3 → 4 in v1.3.0 with the addition of
// `dark_memory_health_ping`. Health_ping is a sibling of
// memory_state (NOT a replacement): it is a strict liveness probe
// intended for K8s/readiness checks and deliberately returns a
// stable documented shape whereas memory_state is the
// debug-friendly snapshot. See RFC §C-2 (Health Probe contract).
package tools

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

// HandlerFunc is the per-tool handler. The raw input is JSON
// (mcp.CallToolRequest.Arguments pre-decoded). The handler returns a
// ToolResponse; any non-nil error is mapped to a generic
// ToolError{Code:"ErrInternal", Message:err.Error()} by the mcp-go
// adapter in server.go.
type HandlerFunc func(ctx context.Context, raw json.RawMessage) (*ToolResponse, error)

// Tool is the metadata + handler for one MCP tool. The mcp-go adapter
// in server.go converts this into a *mcp.Tool + handlerFunc.
type Tool struct {
	// Name is the bare tool name WITHOUT the "dark_memory_" prefix.
	// The server prepends the prefix when registering with mcp-go
	// (so the wire format is "dark_memory_session_start" etc.).
	Name string

	// Description is the human-readable one-liner shown in tools/list.
	// Keep it terse; the LLM uses it to decide which tool to call.
	Description string

	// InputSchema is a JSON Schema describing the tool's input. Kept
	// as a json.RawMessage so we can ship any valid schema (mcp-go
	// will validate input against it on receive).
	InputSchema json.RawMessage

	// Handler is the actual implementation.
	Handler HandlerFunc
}

// Registry collects Tools. Add is not concurrent-safe (register at
// boot only). ListCanonical returns the tools in the fixed canonical
// order (spec 164, bridge.4) — this is what tools/list returns.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]*Tool
	order  []string // canonical order, fixed at construction
}

// NewRegistry constructs an empty Registry with the canonical 28-tool
// order pre-registered (tools may not exist yet; ListCanonical will
// return placeholders that the server filters out at startup).
func NewRegistry() *Registry {
	return &Registry{
		byName: make(map[string]*Tool, 32),
		order:  append([]string{}, canonicalToolOrder...),
	}
}

// Add registers a Tool. Panics on duplicate name (a programming error
// that should fail fast at boot).
func (r *Registry) Add(t *Tool) {
	if t == nil {
		panic("tools: nil Tool")
	}
	if t.Name == "" {
		panic("tools: empty Tool.Name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[t.Name]; exists {
		panic("tools: duplicate tool name: " + t.Name)
	}
	r.byName[t.Name] = t
}

// Get returns the tool registered under name, or nil if not present.
func (r *Registry) Get(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byName[name]
}

// ListCanonical returns all registered tools in the canonical order.
// Tools not yet registered are skipped (this lets the boot phase add
// tools in any order and still emit the canonical sequence).
func (r *Registry) ListCanonical() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.byName[name]; ok {
			out = append(out, t)
		}
	}
	return out
}

// Names returns the registered tool names sorted alphabetically (for
// debugging and for tests that don't care about order).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// CanonicalOrder returns the fixed canonical tool order (spec 164,
// bridge.4). Used by tests that want to assert "did we register all
// 29 in the right order".
//
// 5A.ii.b.2.c: bumped from 28 to 29 (added `recall`).
func CanonicalOrder() []string {
	out := make([]string, len(canonicalToolOrder))
	copy(out, canonicalToolOrder)
	return out
}

// ListExtras returns registered tools that are NOT in the canonical
// order. Used by the server bootstrap to register armed-mode
// extras (e.g. the L7-REDTEAM namespace) without polluting the
// canonical 28-tool surface (v1.3.0; was 27 in v1.2.x and 26 in
// v1.1.x).
//
// The returned order is alphabetical by name (stable across runs;
// no canonical-order contract for extras).
func (r *Registry) ListExtras() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	canon := make(map[string]bool, len(r.order))
	for _, n := range r.order {
		canon[n] = true
	}
	out := make([]*Tool, 0, 8)
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		if !canon[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, r.byName[n])
	}
	return out
}

// CountExtras returns the number of registered tools not in the
// canonical order. Convenience for boot logs.
func (r *Registry) CountExtras() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	canon := make(map[string]bool, len(r.order))
	for _, n := range r.order {
		canon[n] = true
	}
	c := 0
	for n := range r.byName {
		if !canon[n] {
			c++
		}
	}
	return c
}

// canonicalToolOrder is the fixed tool order (bare names, no
// "dark_memory_" prefix; the server prepends on wire).
//
// Per RFC D-9 + BRIDGE_AND_COEXISTENCE.md §3 (bridge.4), v2.6.0:
//
//	PROJECT          (1)  - create                              (v1.2.0, INV-7)
//	SESSION          (4)  - start, resume, status, close
//	RESEARCH         (3)  - topic, recall, resume_thread
//	AGENT_BOOTSTRAP  (3)  - bootstrap, recommend_companions, detect_environment (v2.6.0)
//	VIBE             (4)  - publish, spec, pipeline_status, resolve_drift
//	CONTEXT          (4)  - artifact_context, spec_context, session_context, recall
//	AGENT_MEMORY     (6)  - save, list, recall, get, update, archive (v2.1.0 + v2.3.0)
//	MINDSET          (1)  - mindset_apply                        (v2.7.0-alpha, procedural + judge-validated)
//	JUDGE            (3)  - judge, consensus, judgment_history
//	POLICY           (2)  - active_policy, load_constitution
//	OBSERVABILITY    (4)  - memory_state, writes, anomalies, health_ping (v1.3.0)
//	ADMIN            (3)  - admin_migrate, admin_schema_status, admin_vacuum
//	L6-VLP           (1)  - vlp_handle_event          (DMAP v1.1 spec 193)
//
// Total: 1+4+3+3+4+4+6+1+3+2+4+3+1 = 39.
//
//   - PROJECT was added in v1.2.0 to close the bootstrap loop
//     (operators can now provision a tenant from inside the MCP
//     surface instead of having to insert into the projects table
//     out of band). It is positioned at index 0 because the natural
//     discovery order is project_create  session_start  .;
//     harness callers that iterate the canonical list get
//     project_create first.
//   - OBSERVABILITY grew from 3  4 in v1.3.0 with `health_ping`.
//     Health_ping is the operator-facing liveness probe; it is a
//     SIBLING of memory_state, not a replacement, because the two
//     have different latency budgets and different side-effect
//     profiles (health_ping does not touch the audit bus). See
//     RFC §C-2 and docs/PRODUCTION_CHECKLIST.md §Health probe.
//   - AGENT_MEMORY (v2.1.0) sits between CONTEXT (read-only
//     introspection) and JUDGE (LLM eval). Memory is a read+write
//     data plane; judge/consensus are evals on top of that data.
//     Mem0-aligned (arxiv 2504.19413), with dark-memory's INV-7
//     project isolation layered on. Five tools cover the full
//     lifecycle (save / list / get / update / archive); retrieval
//     search ships as a future minor (F48).
//   - AGENT_BOOTSTRAP (v2.6.0) sits between RESEARCH and VIBE.
//     These are the 3 self-bootstrap tools that let any harness learn
//     how to use the MCP via resources + tool introspection, without
//     requiring the harness to read any external docs. Slotted before
//     VIBE because the natural bootstrap order is:
//     "what is this MCP and what does it do?" (recommend_companions)
//      "what resources can I read?" (agent_bootstrap)
//      "what does my runtime look like?" (detect_environment)
//      "now I can do real work" (vibe_publish, etc.).
var canonicalToolOrder = []string{
	// PROJECT (1) - v1.2.0
	"project_create",
	// SESSION (4)
	"session_start", "session_resume", "session_status", "session_close",
	// RESEARCH (3)
	"research_topic", "research_recall", "research_resume_thread",
	// AGENT_BOOTSTRAP (3) - v2.6.0. 3 self-bootstrap tools that
	// teach any harness how to use the MCP without external docs.
	"agent_bootstrap", "agent_recommend_companions", "agent_detect_environment",
	// VIBE (4)
	"vibe_publish", "vibe_spec", "pipeline_status", "resolve_drift",
	// CONTEXT (4) - v2.0.0 (5A.ii.b.2.c): `recall` added.
	"artifact_context", "spec_context", "session_context", "recall",
	// AGENT_MEMORY (9) - v2.1.0 (Mem0-aligned data plane): 5 tools.
	// v2.3.0 added agent_memory_recall (the missing consumer for the
	// data plane; wraps SearchAgentMemory with FTS5 escape done in
	// the orchestrator layer).
	// v2.8.0-alpha C2 added subagent_register + subagent_unregister
	// (active_subagents table bindings; agent_memory_save uses
	// subagent_id for agent_id resolution when set).
	// v2.9.0-alpha PR-3 added agent_memory_entities (id-only read of
	// the agent_memory_entities side-table). Returned for one row id.
	"agent_memory_save", "agent_memory_list", "agent_memory_recall", "agent_memory_get", "agent_memory_update", "agent_memory_archive", "agent_memory_entities", "subagent_register", "subagent_unregister",
	// MINDSET (1) - v2.7.0-alpha. Procedural composition with judge-validated
	// system prompts for subagent delegation. Cache hit returns in <50ms with
	// 0 LLM calls; cache miss loops composition + validation up to
	// DARK_MINDSET_MAX_ITERATIONS times, each persisting SDDEvaluation rows
	// for full audit trail. Positioned between AGENT_MEMORY (the data plane
	// it caches against) and JUDGE (the validator).
	"mindset_apply",
	// JUDGE (3)
	"judge", "consensus", "judgment_history",
	// POLICY (2)
	"active_policy", "load_constitution",
	// OBSERVABILITY (4) - v1.3.0: health_ping added
	"memory_state", "writes", "anomalies", "health_ping",
	// ADMIN (3)
	"admin_migrate", "admin_schema_status", "admin_vacuum",
	// L6-VLP (1) - DMAP v1.1
	"vlp_handle_event",
	// EMBEDDER (1) - v2.9.0-alpha PR-2. Consent gate for hybrid
	// retrieval per row 164 §3. Single call per project boot —
	// returns the verbatim prompt the harness's LLM should surface
	// when no embedder is detected at first search.
	"embedder_setup_prompt",
}

// WirePrefix is prepended to every bare tool name on the wire. Per
// BRIDGE_AND_COEXISTENCE.md §2, "All public MCP tools use prefix
// dark_memory_*".
const WirePrefix = "dark_memory_"

// WireName returns the wire format of a bare tool name.
func WireName(bare string) string {
	return WirePrefix + bare
}

// CanonicalPosition returns the index of wireName in the canonical
// 28-tool order, or -1 if not found. Used by tools/list filters that
// need to re-sort the alphabetically-sorted output of mcp-go's
// handleListTools back to the RFC D-9 namespace-grouped order.
func CanonicalPosition(wireName string) int {
	for i, n := range canonicalToolOrder {
		if WireName(n) == wireName {
			return i
		}
	}
	return -1
}
