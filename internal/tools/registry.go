// Package tools — registry.go: the Tool type and the canonical Registry.
//
// Per BRIDGE_AND_COEXISTENCE.md §3 (spec 164, bridge.4), the 25 tools
// are emitted in tools/list in a fixed canonical order. The order is
// NOT alphabetical — it follows the RFC D-9 namespace grouping:
// SESSION (4) → RESEARCH (3) → VIBE (4) → CONTEXT (3) → JUDGE (3) →
// POLICY (2) → OBSERVABILITY (3) → ADMIN (3). The order is part of
// the public contract: changing it is a breaking change for any
// harness that indexes by position.
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
	mu      sync.RWMutex
	byName  map[string]*Tool
	order   []string // canonical order, fixed at construction
}

// NewRegistry constructs an empty Registry with the canonical 25-tool
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
// 25 in the right order".
func CanonicalOrder() []string {
	out := make([]string, len(canonicalToolOrder))
	copy(out, canonicalToolOrder)
	return out
}

// canonicalToolOrder is the fixed 25-tool order (bare names, no
// "dark_memory_" prefix; the server prepends on wire).
//
// Per RFC D-9 + BRIDGE_AND_COEXISTENCE.md §3 (bridge.4):
//
//	SESSION        (4)  → start, resume, status, close
//	RESEARCH       (3)  → topic, recall, resume_thread
//	VIBE           (4)  → publish, spec, pipeline_status, resolve_drift
//	CONTEXT        (3)  → artifact_context, spec_context, session_context
//	JUDGE          (3)  → judge, consensus, judgment_history
//	POLICY         (2)  → active_policy, load_constitution
//	OBSERVABILITY  (3)  → memory_state, writes, anomalies
//	ADMIN          (3)  → admin_migrate, admin_schema_status, admin_vacuum
//
// Total: 4+3+4+3+3+2+3+3 = 25.
var canonicalToolOrder = []string{
	// SESSION (4)
	"session_start", "session_resume", "session_status", "session_close",
	// RESEARCH (3)
	"research_topic", "research_recall", "research_resume_thread",
	// VIBE (4)
	"vibe_publish", "vibe_spec", "pipeline_status", "resolve_drift",
	// CONTEXT (3)
	"artifact_context", "spec_context", "session_context",
	// JUDGE (3)
	"judge", "consensus", "judgment_history",
	// POLICY (2)
	"active_policy", "load_constitution",
	// OBSERVABILITY (3)
	"memory_state", "writes", "anomalies",
	// ADMIN (3)
	"admin_migrate", "admin_schema_status", "admin_vacuum",
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
// 25-tool order, or -1 if not found. Used by tools/list filters that
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