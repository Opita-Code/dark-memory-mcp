// Package tools — registry.go: the Tool type and the canonical Registry.
//
// Per BRIDGE_AND_COEXISTENCE.md §3 (spec 164, bridge.4), the canonical tools
// are emitted in tools/list in a fixed canonical order. The order is
// NOT alphabetical — it follows the RFC D-9 namespace grouping plus
// the L6-VLP namespace (DMAP v1.1 spec 193) and the newer namespaces
// (AGENT_BOOTSTRAP, AGENT_MEMORY, MINDSET, DELEGATION, ERROR_OBS,
// EMBEDDER). The order is part of the public contract: changing it is
// a breaking change for any harness that indexes by position.
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

// NewRegistry constructs an empty Registry with the canonical
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
// the tools in the right order". The count is len(canonicalToolOrder)
// — derived, never hardcoded.
func CanonicalOrder() []string {
	out := make([]string, len(canonicalToolOrder))
	copy(out, canonicalToolOrder)
	return out
}

// NamespaceGroups returns a copy of the canonical namespace grouping
// (source of truth: canonicalNamespaces). Consumers that render the
// namespace overview (SYSTEM_PROMPT.md, README tool tables, tests)
// derive counts from this — never hardcode them.
func NamespaceGroups() []NamespaceGroup {
	out := make([]NamespaceGroup, len(canonicalNamespaces))
	for i, ns := range canonicalNamespaces {
		names := make([]string, len(ns.Tools))
		copy(names, ns.Tools)
		out[i] = NamespaceGroup{Name: ns.Name, Tools: names}
	}
	return out
}

// NamespaceCount returns the number of namespaces in the canonical
// surface (len(canonicalNamespaces)).
func NamespaceCount() int {
	return len(canonicalNamespaces)
}

// NamespaceCounts returns namespace name → tool count, derived from
// the namespace grouping. Used to render the namespace overview table
// without hardcoding per-namespace counts.
func NamespaceCounts() map[string]int {
	out := make(map[string]int, len(canonicalNamespaces))
	for _, ns := range canonicalNamespaces {
		out[ns.Name] = len(ns.Tools)
	}
	return out
}

// ListExtras returns registered tools that are NOT in the canonical
// order. Used by the server bootstrap to register armed-mode
// extras (e.g. the L7-REDTEAM namespace) without polluting the
// canonical surface (count derived from canonicalNamespaces; 28 in
// v1.3.0, 26 in v1.1.x historically).
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

// NamespaceGroup is one namespace in the canonical surface: its name
// and the bare tool names that belong to it, in canonical order.
type NamespaceGroup struct {
	Name  string
	Tools []string
}

// canonicalNamespaces is the SINGLE source of truth for the canonical
// tool surface. The flattened tool order (canonicalToolOrder) is
// derived from this structure; per-namespace counts, the namespace
// count, and the namespace overview table in SYSTEM_PROMPT.md are all
// derived from it too. Never hardcode a count anywhere else — adding
// a tool here propagates to every consumer automatically.
//
// Per RFC D-9 + BRIDGE_AND_COEXISTENCE.md §3 (bridge.4), v2.6.0:
//
//	PROJECT          (1)  - create                              (v1.2.0, INV-7)
//	SESSION          (4)  - start, resume, status, close
//	RESEARCH         (3)  - topic, recall, resume_thread
//	AGENT_BOOTSTRAP  (3)  - bootstrap, recommend_companions, detect_environment (v2.6.0)
//	VIBE             (4)  - publish, spec, pipeline_status, resolve_drift
//	CONTEXT          (4)  - artifact_context, spec_context, session_context, recall
//	AGENT_MEMORY     (10) - save, list, recall, get, update, archive, delegate, entities, subagent_register, subagent_unregister (v2.1.0 + v2.3.0 + v2.9.3)
//	MINDSET          (1)  - mindset_apply                        (v2.7.0-alpha, procedural + judge-validated)
//	DELEGATION       (1)  - delegate_intent                      (Wave 5C, A1: handle/delegate/refuse)
//	JUDGE            (3)  - judge, consensus, judgment_history
//	POLICY           (2)  - active_policy, load_constitution
//	OBSERVABILITY    (4)  - memory_state, writes, anomalies, health_ping (v1.3.0)
//	ERROR_OBS        (4)  - error_list, error_get, error_summary, error_resolve (v2.11.0, spec 757)
//	ADMIN            (3)  - admin_migrate, admin_schema_status, admin_vacuum
//	L6-VLP           (1)  - vlp_handle_event          (DMAP v1.1 spec 193)
//	EMBEDDER         (1)  - embedder_setup_prompt     (v2.9.0-alpha PR-2)
//
//   - PROJECT was added in v1.2.0 to close the bootstrap loop
//     (operators can now provision a tenant from inside the MCP
//     surface instead of having to insert into the projects table
//     out of band). It is positioned at index 0 because the natural
//     discovery order is project_create  session_start  .;
//     harness callers that iterate the canonical list get
//     project_create first.
//   - OBSERVABILITY grew from 3 to 4 in v1.3.0 with `health_ping`.
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
var canonicalNamespaces = []NamespaceGroup{
	{
		Name:  "PROJECT",
		Tools: []string{"project_create"}, // v1.2.0
	},
	{
		Name:  "SESSION",
		Tools: []string{"session_start", "session_resume", "session_status", "session_close"},
	},
	{
		Name:  "RESEARCH",
		Tools: []string{"research_topic", "research_recall", "research_resume_thread"},
	},
	{
		// v2.6.0. 3 self-bootstrap tools that teach any harness how to
		// use the MCP without external docs.
		Name:  "AGENT_BOOTSTRAP",
		Tools: []string{"agent_bootstrap", "agent_recommend_companions", "agent_detect_environment"},
	},
	{
		Name:  "VIBE",
		Tools: []string{"vibe_publish", "vibe_spec", "pipeline_status", "resolve_drift"},
	},
	{
		// v2.0.0 (5A.ii.b.2.c): `recall` added.
		Name:  "CONTEXT",
		Tools: []string{"artifact_context", "spec_context", "session_context", "recall"},
	},
	{
		// v2.1.0 (Mem0-aligned data plane): 5 tools.
		// v2.3.0 added agent_memory_recall (the missing consumer for the
		// data plane; wraps SearchAgentMemory with FTS5 escape done in
		// the orchestrator layer).
		// v2.8.0-alpha C2 added subagent_register + subagent_unregister
		// (active_subagents table bindings; agent_memory_save uses
		// subagent_id for agent_id resolution when set).
		// v2.9.0-alpha PR-3 added agent_memory_entities (id-only read of
		// the agent_memory_entities side-table). Returned for one row id.
		// v2.9.3 added agent_memory_delegate (prepares a delegation context
		// for sub-agent spawns; registers the C2 binding + returns the
		// ready-to-inject markdown block).
		Name: "AGENT_MEMORY",
		Tools: []string{
			"agent_memory_save", "agent_memory_list", "agent_memory_recall", "agent_memory_get", "agent_memory_update", "agent_memory_archive", "agent_memory_delegate", "agent_memory_entities", "subagent_register", "subagent_unregister",
		},
	},
	{
		// v2.7.0-alpha. Procedural composition with judge-validated
		// system prompts for subagent delegation. Cache hit returns in <50ms
		// with 0 LLM calls; cache miss loops composition + validation up to
		// DARK_MINDSET_MAX_ITERATIONS times, each persisting SDDEvaluation
		// rows for full audit trail. Positioned between AGENT_MEMORY (the
		// data plane it caches against) and JUDGE (the validator).
		Name:  "MINDSET",
		Tools: []string{"mindset_apply"},
	},
	{
		// Wave 5C. delegate_intent decides whether the orchestrator
		// handles an intent inline, delegates it to sub-agents, or refuses
		// (A1: Memory decides). Runs the DelegationRouter's
		// DECIDE→PLAN→MIND→CURATE pipeline and returns ready-to-spawn
		// material (system_prompt + curated delegation context + C2
		// subagent binding). Positioned between MINDSET (the prompt engine
		// it consumes) and JUDGE (the validator that drift-checks the
		// synthesized output).
		Name:  "DELEGATION",
		Tools: []string{"delegate_intent"},
	},
	{
		Name:  "JUDGE",
		Tools: []string{"judge", "consensus", "judgment_history"},
	},
	{
		Name:  "POLICY",
		Tools: []string{"active_policy", "load_constitution"},
	},
	{
		// v1.3.0: health_ping added
		Name:  "OBSERVABILITY",
		Tools: []string{"memory_state", "writes", "anomalies", "health_ping"},
	},
	{
		// v2.11.0 (spec 757, Wave 5D). Error Observatory: durable,
		// classified, backlog-able error capture. error_list = backlog
		// view (filters); error_get = one cluster; error_summary =
		// aggregate metrics (global scope); error_resolve = operator
		// triage. Positioned right after OBSERVABILITY (it IS
		// observability — the durable error plane the other tools
		// surface). Store-bound, no orchestrator layer.
		Name:  "ERROR_OBS",
		Tools: []string{"error_list", "error_get", "error_summary", "error_resolve"},
	},
	{
		Name:  "ADMIN",
		Tools: []string{"admin_migrate", "admin_schema_status", "admin_vacuum"},
	},
	{
		// DMAP v1.1
		Name:  "L6-VLP",
		Tools: []string{"vlp_handle_event"},
	},
	{
		// v2.9.0-alpha PR-2. Consent gate for hybrid retrieval per row
		// 164 §3. Single call per project boot — returns the verbatim
		// prompt the harness's LLM should surface when no embedder is
		// detected at first search.
		Name:  "EMBEDDER",
		Tools: []string{"embedder_setup_prompt"},
	},
}

// flattenCanonicalNamespaces flattens canonicalNamespaces into the
// bare-name canonical order. The flatten happens once at package init
// (canonicalToolOrder below); the order is deterministic because
// canonicalNamespaces is a fixed slice.
func flattenCanonicalNamespaces() []string {
	var total int
	for _, ns := range canonicalNamespaces {
		total += len(ns.Tools)
	}
	out := make([]string, 0, total)
	for _, ns := range canonicalNamespaces {
		out = append(out, ns.Tools...)
	}
	return out
}

// canonicalToolOrder is the fixed tool order (bare names, no
// "dark_memory_" prefix; the server prepends on wire). DERIVED from
// canonicalNamespaces — never edit this slice directly; edit
// canonicalNamespaces instead. The order is part of the public
// contract: changing it is a breaking change for any harness that
// indexes by position.
var canonicalToolOrder = flattenCanonicalNamespaces()

// WirePrefix is prepended to every bare tool name on the wire. Per
// BRIDGE_AND_COEXISTENCE.md §2, "All public MCP tools use prefix
// dark_memory_*".
const WirePrefix = "dark_memory_"

// WireName returns the wire format of a bare tool name.
func WireName(bare string) string {
	return WirePrefix + bare
}

// CanonicalPosition returns the index of wireName in the canonical
// order, or -1 if not found. Used by tools/list filters that
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
