// Package tools - agent_memory.go: the AGENT_MEMORY namespace (5 tools).
//
// Per RFC D-12 / v2.1.0 (Mem0-aligned agent memory data plane):
//
//	dark_memory_agent_memory_save
//	dark_memory_agent_memory_list
//	dark_memory_agent_memory_get
//	dark_memory_agent_memory_update
//	dark_memory_agent_memory_archive
//
// Each tool reads/writes a single row in the agent_memory table
// (migration v18). INV-7 project isolation is enforced at the Store
// layer; the tools just funnel JSON in/out.
//
// Scope semantics (resolved at the Store layer):
//
//   - save    -> if a session is active, row.session_id = sessionID;
//                else row.session_id = "". The kind/title/content/tags
//                come from the caller.
//   - list    -> scope filter defaults to "current" (= session if
//                active, else operator). Explicit values are honored.
//   - get     -> by id, must be in the active project.
//   - update  -> by id; only the fields the caller sends are touched.
//   - archive -> soft delete (sets archived_at); recoverable via
//                list(include_archived=true).
package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

var _ = audit.WriteEvent{}

// RegisterAgentMemory wires the AGENT_MEMORY tools into the registry.
// The orchestrator and store are passed so handlers can reach them
// without circular imports.
//
// v2.1.0 wire-contract: 5 tools (save / list / get / update /
// archive). v2.3.0 added agent_memory_recall (the missing consumer
// for the data plane). All 6 are gate-allowed only with an active
// session (the gate's RequiresActiveSession allowlist covers them
// by default — see internal/policy/gate.go RequiresActiveSession).
func RegisterAgentMemory(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// v2.8.0-alpha C2: register the 2 subagent binding tools.
	registerSubagentTools(reg, orch)

	reg.Add(BindOrchestrator("agent_memory_save",
		"Save an agent memory. Mem0-aligned. Scoped to the active project. By default (bind_session=false) the row survives session close; set bind_session=true to tag the row with the active session. v2.3.0.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"operator", "kind", "content"},
			"properties": map[string]any{
				"operator":     map[string]any{"type": "string", "description": "Operator id (for ownership + audit). Required."},
				"agent_id":     map[string]any{"type": "string", "description": "Mem0 agent_id (LLM that owns the memory). Optional. v2.3.0."},
				"kind":         map[string]any{"type": "string", "enum": []string{"note", "observation", "decision", "finding", "todo", "link", "context"}},
				"memory_type":  map[string]any{"type": "string", "enum": []string{"episodic", "semantic", "procedural"}, "description": "Mem0 three-class taxonomy. Optional; empty = unclassified. v2.3.0."},
				"title":        map[string]any{"type": "string"},
				"content":      map[string]any{"type": "string", "description": "The memory payload. Required."},
				"tags":         map[string]any{"type": "string", "description": "Comma-separated, normalized lowercase."},
				"pinned":       map[string]any{"type": "boolean", "default": false},
				"expires_at":   map[string]any{"type": "string", "description": "RFC3339. Optional TTL hint (sweeper follow-up)."},
				"bind_session": map[string]any{"type": "boolean", "default": false, "description": "If true, tag the row with the active session id. Default false (v2.3.0 change; pre-v2.3.0 was implicitly true)."},
			},
		}),
		func(ctx context.Context, in orchestration.AgentMemorySaveInput) (*orchestration.AgentMemorySaveOutput, error) {
			return orch.AgentMemorySave(ctx, in)
		}))

	reg.Add(BindOrchestrator("agent_memory_list",
		"List agent memories in the active project. v2.3.0: scope default is 'project' (was implicit session); scope=operator now requires the explicit operator parameter; scope=agent is new (Mem0 agent_id).",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope":            map[string]any{"type": "string", "enum": []string{"current", "session", "project", "operator", "agent", "all"}},
				"operator":         map[string]any{"type": "string", "description": "Caller identity for scope=operator resolution. v2.3.0."},
				"agent_id":         map[string]any{"type": "string", "description": "Required for scope=agent (Mem0 agent_id). v2.3.0."},
				"kind":             map[string]any{"type": "string"},
				"memory_type":      map[string]any{"type": "string", "enum": []string{"episodic", "semantic", "procedural"}, "description": "Mem0 three-class taxonomy filter. v2.3.0."},
				"tag":              map[string]any{"type": "string"},
				"pinned_only":      map[string]any{"type": "boolean", "default": false},
				"include_archived": map[string]any{"type": "boolean", "default": false},
				"limit":            map[string]any{"type": "integer", "default": 50, "maximum": 200},
			},
		}),
		func(ctx context.Context, in orchestration.AgentMemoryListInput) (*orchestration.AgentMemoryListOutput, error) {
			return orch.AgentMemoryList(ctx, in)
		}))

	reg.Add(BindOrchestrator("agent_memory_recall",
		"BM25-ranked search over content+title+tags in the active project's agent_memory. v2.3.0 NEW. Lexical-only (FTS5). Vector search is v2.4.0+. Use this tool instead of re-implementing FTS5 escape in client code.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"operator", "query"},
			"properties": map[string]any{
				"operator":    map[string]any{"type": "string", "description": "Operator id (caller identity for INV-1 audit + result attribution). Required."},
				"query":       map[string]any{"type": "string", "description": "FTS5 search query. Alphanumeric + . - _ / + * only; AND/OR/NOT/NEAR rejected."},
				"agent_id":    map[string]any{"type": "string", "description": "Optional. Scopes results to rows where agent_id == this value (Mem0 agent_id). v2.3.0."},
				"kind":        map[string]any{"type": "string", "description": "Optional. Filter to one operator-kind (note, observation, decision, ...)."},
				"memory_type": map[string]any{"type": "string", "enum": []string{"episodic", "semantic", "procedural"}, "description": "Optional. Mem0 three-class taxonomy filter. v2.3.0."},
				"limit":       map[string]any{"type": "integer", "default": 10, "maximum": 50},
			},
		}),
		func(ctx context.Context, in orchestration.AgentMemoryRecallInput) (*orchestration.AgentMemoryRecallOutput, error) {
			return orch.AgentMemoryRecall(ctx, in)
		}))

	reg.Add(BindOrchestrator("agent_memory_get",
		"Get an agent memory by id. Cross-project reads return ErrNotFound (INV-7).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"id"},
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "Row id."},
			},
		}),
		func(ctx context.Context, in orchestration.AgentMemoryGetInput) (*orchestration.AgentMemoryGetOutput, error) {
			return orch.AgentMemoryGet(ctx, in)
		}))

	reg.Add(BindOrchestrator("agent_memory_update",
		"Update an agent memory's mutable fields (content/title/tags/pinned/expires_at/memory_type). Operator + project + agent_id are immutable.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"id"},
			"properties": map[string]any{
				"id":          map[string]any{"type": "integer"},
				"title":       map[string]any{"type": "string"},
				"content":     map[string]any{"type": "string"},
				"tags":        map[string]any{"type": "string"},
				"pinned":      map[string]any{"type": "boolean"},
				"expires_at":  map[string]any{"type": "string"},
				"memory_type": map[string]any{"type": "string", "enum": []string{"episodic", "semantic", "procedural", ""}, "description": "Mem0 three-class taxonomy. Empty string clears. v2.3.0."},
			},
		}),
		func(ctx context.Context, in orchestration.AgentMemoryUpdateInput) (*orchestration.AgentMemoryUpdateOutput, error) {
			return orch.AgentMemoryUpdate(ctx, in)
		}))

	reg.Add(BindOrchestrator("agent_memory_archive",
		"Soft-delete an agent memory (sets archived_at). Recoverable with list(include_archived=true).",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"id"},
			"properties": map[string]any{
				"id": map[string]any{"type": "integer"},
			},
		}),
		func(ctx context.Context, in orchestration.AgentMemoryArchiveInput) (*orchestration.AgentMemoryArchiveOutput, error) {
			return orch.AgentMemoryArchive(ctx, in)
		}))
}

// --- Orchestrator input/output shapes --------------------------------
//
// These are defined here (not in internal/orchestration) so the tool
// surface and the orchestrator wire contract live next to each other.
// The orchestration package owns the implementations.

// AgentMemorySearchInput is the optional query parameter for
// agent_memory_list. When non-empty, switches the call from "list
// with filters" to "BM25 search". Exposed as a sibling shape rather
// than a separate tool because the contract is small and the list
// tool's already expensive to call twice (once for list, once for
// search).
//
// (v2.1.0 keeps search and list as one tool to minimize wire surface.
// F48 in CHANGELOG tracks splitting them if usage shows it worth it.)
//
// NOTE: kept as a struct comment reference; not actually wired in
// v2.1.0 list. The orchestrator's List path takes a flat
// AgentMemoryListInput below.
//
// FTS5 escape helper: FTS5 has reserved operators (AND, OR, NOT, NEAR,
// parentheses, quotes, colons). For v2.1.0 we do a simple escape:
//   - strip double quotes (FTS5 phrase boundaries) by doubling them
//     inside the term
//   - reject input containing unescaped parentheses or colons
// This is conservative; a full tokenizer-aware escape is F49.
func escapeFTS5(q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("empty query")
	}
	// Reject inputs that look like injection attempts. We allow
	// alphanumeric, whitespace, dot, dash, underscore, slash, plus.
	for _, r := range q {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == ' ' || r == '\t' || r == '.' || r == '-' || r == '_' || r == '/' || r == '+' || r == '*':
		default:
			return "", fmt.Errorf("fts5: invalid character %q in query", r)
		}
	}
	// Conservative: don't allow standalone AND/OR/NOT/NEAR at the
	// start of a token (FTS5 reserved words).
	tokens := strings.Fields(q)
	for _, t := range tokens {
		upper := strings.ToUpper(t)
		switch upper {
		case "AND", "OR", "NOT", "NEAR":
			return "", fmt.Errorf("fts5: reserved word %q not allowed in query", t)
		}
	}
	return q, nil
}

// nowFunc is a package-level seam for tests. Default is time.Now.
var nowFunc = func() time.Time { return time.Now().UTC() }

// auditContext builds a WriteContext with sensible defaults for
// agent_memory ops. Actor defaults to "operator" if set, else the
// actor literal "agent_memory_<op>". ConstitutionID/Ver are pulled
// from the active project (best-effort — Store has no public getter).
func auditContext(st store.Store, operator, sessionID, writePath string) store.WriteContext {
	wc := store.WriteContext{
		Actor:     operator,
		SessionID: sessionID,
		WritePath: writePath,
	}
	if operator == "" {
		wc.Actor = "agent_memory_" + writePath
	}
	// Note: actor="system" with empty operator is OK for read paths,
	// but every write path here has an explicit operator from input.
	_ = st // st reserved for future constitution lookup
	return wc
}

// v2.8.0-alpha C2 — register/unregister subagent bindings. These
// tools explicitly manipulate the active_subagents table so the
// harness can register a subagent binding WITHOUT going through
// mindset_apply(spawn_subagent=true), or clear a stale binding
// manually. Both gated by DARK_MEMORY_V280=1; when the flag is off,
// the orchestrator returns ErrInvalidState.
//
// Canonical tool count: 39 → 41.
func registerSubagentTools(reg *Registry, orch *orchestration.Orchestrator) {
	reg.Add(BindOrchestrator("subagent_register",
		"Register or refresh an active subagent binding. v2.8.0-alpha C2. After this call, the subagent's agent_memory_save writes are tagged with subagent_id (NOT the principal's agent_id) so they never leak into the principal's ContextRecap. Defense-in-depth against arxiv:2605.08460 inheritance attacks. Gated by DARK_MEMORY_V280=1.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"operator", "subagent_id"},
			"properties": map[string]any{
				"operator":        map[string]any{"type": "string", "description": "Operator id (INV-1 audit). Required."},
				"subagent_id":     map[string]any{"type": "string", "description": "Opaque uuid the principal generated when spawning the subagent. Required."},
				"parent_agent_id": map[string]any{"type": "string", "description": "Principal's resolved agent_id for audit provenance. Optional; defaults to projects.default_agent_id."},
				"ttl_seconds":     map[string]any{"type": "integer", "default": 3600, "minimum": 60, "maximum": 86400, "description": "TTL in seconds (clamp 60..86400; default 3600 = 1h)."},
			},
		}),
		func(ctx context.Context, in orchestration.SubagentRegisterInput) (*orchestration.SubagentRegisterOutput, error) {
			return orch.SubagentRegister(ctx, in)
		}))

	reg.Add(BindOrchestrator("subagent_unregister",
		"Clear an active subagent binding. v2.8.0-alpha C2. After this call, subsequent agent_memory_save calls fall through to projects.default_agent_id (or empty). Idempotent: returns ErrNotFound if no binding matches. Gated by DARK_MEMORY_V280=1.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"operator", "subagent_id"},
			"properties": map[string]any{
				"operator":    map[string]any{"type": "string", "description": "Operator id (must match the one used in subagent_register). Required."},
				"subagent_id": map[string]any{"type": "string", "description": "The subagent_id to clear. Required."},
			},
		}),
		func(ctx context.Context, in orchestration.SubagentUnregisterInput) (*orchestration.SubagentUnregisterOutput, error) {
			return orch.SubagentUnregister(ctx, in)
		}))
}

// auditSentinel keeps the import live in case future versions need
// to read audit row shapes (e.g. last actor for scope=operator).
var _ = audit.WriteEvent{}