// Package tools contains the type definitions, registry, error adapter,
// and per-namespace tool handlers for the 26 dark_memory_* MCP tools.
//
// Layering:
//
//	external MCP harness (opencode, claude, ...)
//	    |
//	    v
//	mcp-go server  (cmd/dark-mem-mcp uses github.com/mark3labs/mcp-go)
//	    |
//	    v
//	Tool / Registry / ToolResponse  (this package)
//	    |
//	    v
//	Orchestrator  (internal/orchestration)
//	    |
//	    v
//	Store / Safety
//
// Per RFC D-9 + DMAP v1.1 spec 193, the MCP surface is exactly 26
// intent-driven tools in 9 namespaces (SESSION / RESEARCH / VIBE /
// CONTEXT / JUDGE / POLICY / OBSERVABILITY / ADMIN / L6-VLP). The
// L6-VLP namespace (vlp_handle_event) was added in v1.1.0 to expose
// the VLP state machine to MCP harnesses. Per
// BRIDGE_AND_COEXISTENCE.md §3 (spec 164, bridge.4), the canonical tool
// order is fixed and emitted in tools/list.
package tools

// ToolResponse is the canonical shape every tool returns. Matches
// RFC §5 "every tool response shape":
//
//	{
//	  "data":   <result>,                  // the actual response payload
//	  "audit":  {"id":..., "sha256":...},  // write_audit row(s) produced (optional)
//	  "next":   {"tool":..., "args":...},  // sequence-aware next-step hint
//	  "error":  {"code":..., "hint":...}   // typed error (if non-nil, data is best-effort)
//	}
//
// Only one of {Data, Error} is meaningful at a time: on error, Data
// is best-effort (may be nil or partial) and Error carries the
// structured failure. Next is independent of success state — it can
// appear on both happy and sad paths (e.g. happy → suggest next
// artifact; sad → suggest resolve_drift).
type ToolResponse struct {
	Data  any        `json:"data,omitempty"`
	Audit *AuditRef  `json:"audit,omitempty"`
	Next  *NextAction `json:"next,omitempty"`
	Error *ToolError `json:"error,omitempty"`
}

// AuditRef is the reference to the write_audit row(s) a tool call
// produced. INV-1 (write-path audit) guarantees that every Save emits
// a write_audit row atomically; this struct is how we surface that
// row's ID + payload SHA back to the caller.
type AuditRef struct {
	ID       int64    `json:"id"`        // write_audit.id (or first id if multi-row)
	IDs      []int64  `json:"ids,omitempty"` // all ids when a single call emits >1
	Operation string  `json:"operation"` // e.g. "SaveSpec", "SaveArtifact", "SaveDriftReport"
	SHA256   string   `json:"sha256,omitempty"` // sha256(payload) per INV-1
}

// NextAction is the sequence-aware "what to do next" hint. Always
// present when applicable; nil when the call is terminal. The "tool"
// field is the bare tool name (without "dark_memory_" prefix; the
// server re-adds the prefix on dispatch).
//
// When is one of:
//   - "always": every time after this call succeeds
//   - "on_drift": only when the call returned a drift_detected verdict
//   - "on_human_gate": only when verdict == needs_human
type NextAction struct {
	Tool string         `json:"tool"`         // bare tool name (no prefix)
	Args map[string]any `json:"args,omitempty"` // argument hints (typed)
	When string         `json:"when"`         // always | on_drift | on_human_gate
	Reason string       `json:"reason,omitempty"` // 1-sentence human-readable explanation
}

// ToolError is the structured error returned by a failed tool call.
// Code is the typed sentinel name (e.g. "ErrCanaryInPayload"); Message
// is 1 sentence fact + 1 sentence implication; Hint is optional
// guidance.
type ToolError struct {
	Code    string `json:"code"`              // sentinel name
	Message string `json:"message"`           // 1-sentence fact + 1-sentence implication
	Hint    string `json:"hint,omitempty"`    // optional: what to do next
}

// Common canonical NextAction verbs. Used by next.go and the tool
// namespace files.
const (
	NextActionPublish    = "publish"      // artifact accepted; safe to ship
	NextActionReconcile  = "reconcile"    // drift_detected; call resolve_drift
	NextActionHumanGate  = "human_gate"   // needs_human; operator reviews
	NextActionAlways     = "always"       // when trigger
	NextActionOnDrift    = "on_drift"     // when trigger
	NextActionOnHumanGate = "on_human_gate" // when trigger
)