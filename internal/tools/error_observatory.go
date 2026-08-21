// Package tools — error_observatory.go: the ERROR_OBS namespace
// (4 tools, spec 757, Wave 5D, v2.11.0).
//
//	dark_memory_error_list      — backlog view (filters, newest-first)
//	dark_memory_error_get       — one cluster by id
//	dark_memory_error_summary   — aggregate metrics (global scope)
//	dark_memory_error_resolve   — operator triage (mark resolved + note)
//
// The namespace closes the "no nos enteramos de nada" gap: every
// silent-discard site, gate refusal, and LLM infra failure lands in
// the error_events table (migration v25) via
// Orchestrator.RecordError / Store.SaveErrorEvent. These 4 tools
// make that backlog queryable + triageable.
//
// --- Cross-project opt-in (admin elevation) ---
//
// Three of the four tools accept an optional `cross_project: bool`
// parameter. When true, the tool reads/writes across project
// boundaries — the operator triage escape hatch from INV-7's
// project-scoped default. The policy gate is at this layer
// (internal/tools/error_observatory.go) before reaching the Store:
//
//   - DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed
//     Process-lifetime bypass flag. Anyone can call with
//     cross_project=true. Audit Actor:
//     "error_resolve_cross_project_override".
//
//   - DARK_ERROR_OBS_ADMIN_OPERATORS=<comma-separated operator ids>
//     Allow-list. Only operators in the list can call with
//     cross_project=true. Audit Actor:
//     "error_resolve_cross_project_admin".
//
// If both are unset, cross_project=true is rejected with
// ErrInvalidArgument. The two modes coexist: if the override is
// set, the allow-list is bypassed (override wins). The override
// requires a process restart to disable; the allow-list is read
// per-call.
package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// --- Cross-project policy (Option C / Option B from audit 2026-08-18) ---

// EnvOverrideFlag is the process-lifetime bypass flag. When set to
// "armed", any operator can use cross_project=true on the
// error_observatory tools. Read once at boot (or per-call — see
// enforceCrossProjectPolicy). Default unset.
const EnvOverrideFlag = "DARK_ERROR_OBS_OPERATOR_OVERRIDE"

// EnvAdminOperators is the comma-separated operator allow-list.
// Operators in this list can use cross_project=true even without
// the override flag.
const EnvAdminOperators = "DARK_ERROR_OBS_ADMIN_OPERATORS"

// Actor strings emitted in the write_audit row when the
// cross-project path is taken. Distinct from the standard
// "error_resolve" so an auditor can tell at a glance whether a
// cluster was resolved in-project or via elevation.
const (
	ActorCrossProjectAdmin   = "error_resolve_cross_project_admin"
	ActorCrossProjectOverride = "error_resolve_cross_project_override"
)

// crossProjectDecision reports the policy outcome for a
// cross_project=true call.
type crossProjectDecision struct {
	// allowed: true if the call should proceed.
	allowed bool
	// actor: the Actor string to embed in the audit row when the
	// call is a write. Empty when not allowed.
	actor string
}

// enforceCrossProjectPolicy applies the two env-flag rules in
// order. Override wins (when armed, allow-list is bypassed). When
// neither rule applies, returns {allowed: false, actor: ""}.
//
// Pure function: reads os.Getenv each call (cheap). The allow-list
// is re-read on every call so changes to the env do not require a
// process restart. The override flag, by contrast, is a process-
// lifetime gate — but reading it per-call is fine because the
// consequence of leaving it armed is exactly what we want (the
// operator needs to restart to disable it, which is the safe
// failure mode).
//
// Operator is the caller identity. Pass empty when the caller
// hasn't supplied one (the resolver falls back to the active
// session's operator).
func enforceCrossProjectPolicy(operator string) crossProjectDecision {
	if os.Getenv(EnvOverrideFlag) == "armed" {
		return crossProjectDecision{allowed: true, actor: ActorCrossProjectOverride}
	}
	if operator != "" {
		allow := os.Getenv(EnvAdminOperators)
		if allow != "" {
			for _, o := range strings.Split(allow, ",") {
				if strings.TrimSpace(o) == operator {
					return crossProjectDecision{allowed: true, actor: ActorCrossProjectAdmin}
				}
			}
		}
	}
	return crossProjectDecision{allowed: false}
}

// resolveOperatorFromStore returns the operator id of the active
// session in the active project, or "" if no session is active.
// Used as a fallback when the tool caller doesn't supply
// `operator` explicitly.
func resolveOperatorFromStore(ctx context.Context, s store.Store) string {
	proj := s.ActiveProject()
	if proj == "" {
		return ""
	}
	sessID, err := s.GetActiveSession(ctx, proj)
	if err != nil || sessID == "" {
		return ""
	}
	sess, err := s.GetSession(ctx, sessID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.Operator
}

// errCrossProjectNotAllowed is returned when cross_project=true is
// passed without the appropriate env flag.
var errCrossProjectNotAllowed = fmt.Errorf(
	"%w: cross_project=true requires DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed or DARK_ERROR_OBS_ADMIN_OPERATORS to include the caller operator",
	store.ErrInvalidArgument,
)

// RegisterErrorObservatory wires the 4 ERROR_OBS tools into the
// registry. All four are Store-bound (no orchestrator layer — the
// Store exposes the full CRUD contract directly).
func RegisterErrorObservatory(reg *Registry, st store.Store) {
	// error_list — backlog view.
	reg.Add(BindStore("error_list",
		"List error_events backlog rows (durable, classified error clusters). Filters: domain, severity, resolved, session_id, tool_name, since. Newest-first (last_seen_at DESC). Read-only. Pass cross_project=true to list across project boundaries (admin elevation — requires DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed OR the caller's operator id in DARK_ERROR_OBS_ADMIN_OPERATORS).",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain": map[string]any{
					"type":        "string",
					"enum":        []string{"store", "llm", "gate", "validation", "network", "sweep", "unknown"},
					"description": "Classification axis. Empty = all.",
				},
				"severity": map[string]any{
					"type":        "string",
					"enum":        []string{"fatal", "error", "warn"},
					"description": "Impact axis. Empty = all.",
				},
				"resolved": map[string]any{
					"type":        "boolean",
					"description": "Filter by triage state. Omitted = unresolved only (backlog default). true = resolved only.",
				},
				"session_id": map[string]any{"type": "string", "description": "Filter by session. Empty = all."},
				"tool_name":  map[string]any{"type": "string", "description": "Filter by originating tool. Empty = all."},
				"since":      map[string]any{"type": "string", "description": "RFC3339: only events with last_seen_at >= this. Empty = all."},
				"limit":      map[string]any{"type": "integer", "description": "Max rows. Default 50, max 200."},
				"cross_project": map[string]any{
					"type":        "boolean",
					"description": "When true, returns rows from ALL projects instead of filtering by the active project_id. Requires DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed OR the caller's operator id in DARK_ERROR_OBS_ADMIN_OPERATORS.",
				},
				"operator": map[string]any{
					"type":        "string",
					"description": "Caller operator id for cross-project allow-list check. Empty = resolve from the active session.",
				},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in ErrorListInput) (*ErrorListResult, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 50
			}
			if limit > 200 {
				limit = 200
			}
			operator := in.Operator
			if operator == "" {
				operator = resolveOperatorFromStore(ctx, s)
			}
			if in.CrossProject {
				if !enforceCrossProjectPolicy(operator).allowed {
					return nil, errCrossProjectNotAllowed
				}
			}
			rows, err := s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
				Domain:       errorobs.Domain(in.Domain),
				Severity:     errorobs.Severity(in.Severity),
				Resolved:     in.Resolved,
				SessionID:    in.SessionID,
				ToolName:     in.ToolName,
				Since:        in.Since,
				Limit:        limit,
				CrossProject: in.CrossProject,
			})
			if err != nil {
				return nil, err
			}
			return &ErrorListResult{Events: rows, Count: len(rows)}, nil
		}))

	// error_get — one cluster by id.
	reg.Add(BindStore("error_get",
		"Get one error_events cluster by id (Error Observatory). Returns null when the id does not exist in the active project (INV-7 existence-leak parity). Read-only. Pass cross_project=true to look up across project boundaries (admin elevation).",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "Row id."},
				"cross_project": map[string]any{
					"type":        "boolean",
					"description": "When true, looks up the row across all projects (admin elevation). Requires DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed OR the caller's operator id in DARK_ERROR_OBS_ADMIN_OPERATORS.",
				},
				"operator": map[string]any{
					"type":        "string",
					"description": "Caller operator id for cross-project allow-list check. Empty = resolve from the active session.",
				},
			},
			"required": []string{"id"},
		}),
		st,
		func(ctx context.Context, s store.Store, in ErrorGetInput) (*errorobs.ErrorEvent, error) {
			operator := in.Operator
			if operator == "" {
				operator = resolveOperatorFromStore(ctx, s)
			}
			if in.CrossProject {
				if !enforceCrossProjectPolicy(operator).allowed {
					return nil, errCrossProjectNotAllowed
				}
				return s.GetErrorEventCrossProject(ctx, in.ID)
			}
			return s.GetErrorEvent(ctx, in.ID)
		}))

	// error_summary — aggregate metrics.
	reg.Add(BindStore("error_summary",
		"Return aggregate Error Observatory metrics: total clusters, unresolved, errors in the last N hours (default 1h), counts by domain + severity, top-5 recurring unresolved clusters. GLOBAL scope (cross-project) so operators see overall health. Read-only.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hours": map[string]any{"type": "integer", "description": "Recent window for errors_last_hour. Default 1. Max 168 (7d)."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in ErrorSummaryInput) (*errorobs.ErrorSummary, error) {
			hours := in.Hours
			if hours <= 0 {
				hours = 1
			}
			if hours > 168 {
				hours = 168
			}
			return s.ErrorSummary(ctx, hours)
		}))

	// error_resolve — operator triage.
	reg.Add(BindStore("error_resolve",
		"Mark an error_events cluster as resolved with an operator note (backlog triage). Idempotent: resolving an already-resolved cluster returns ok. Returns ErrNotFound when the id does not exist in the active project (INV-7). Operator-gated (INV-1 audit via WriteContext). Pass cross_project=true to resolve across project boundaries (admin elevation); emits a write_audit row with Actor=error_resolve_cross_project_{admin,override}.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":   map[string]any{"type": "integer", "description": "Cluster id to resolve."},
				"note": map[string]any{"type": "string", "description": "Resolution note (root cause, fix ref, ...). Optional."},
				"cross_project": map[string]any{
					"type":        "boolean",
					"description": "When true, resolves the cluster across project boundaries (admin elevation). Requires DARK_ERROR_OBS_OPERATOR_OVERRIDE=armed OR the caller's operator id in DARK_ERROR_OBS_ADMIN_OPERATORS. Emits a write_audit row.",
				},
				"operator": map[string]any{
					"type":        "string",
					"description": "Caller operator id for cross-project allow-list check. Empty = resolve from the active session.",
				},
			},
			"required": []string{"id"},
		}),
		st,
		func(ctx context.Context, s store.Store, in ErrorResolveInput) (*ErrorResolveResult, error) {
			operator := in.Operator
			if operator == "" {
				operator = resolveOperatorFromStore(ctx, s)
			}
			if in.CrossProject {
				decision := enforceCrossProjectPolicy(operator)
				if !decision.allowed {
					return nil, errCrossProjectNotAllowed
				}
				wc := store.WriteContext{
					Actor:     decision.actor, // error_resolve_cross_project_{admin,override}
					WritePath: "ResolveErrorEventCrossProject",
				}
				if err := s.ResolveErrorEventCrossProject(ctx, wc, in.ID, in.Note); err != nil {
					return nil, err
				}
				return &ErrorResolveResult{ID: in.ID, Resolved: true}, nil
			}
			wc := store.WriteContext{
				Actor:     "error_resolve",
				WritePath: "ResolveErrorEvent",
			}
			if err := s.ResolveErrorEvent(ctx, wc, in.ID, in.Note); err != nil {
				return nil, err
			}
			return &ErrorResolveResult{ID: in.ID, Resolved: true}, nil
		}))
}

// ErrorListInput is the input for error_list.
type ErrorListInput struct {
	Domain       string `json:"domain,omitempty"`
	Severity     string `json:"severity,omitempty"`
	Resolved     *bool  `json:"resolved,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	Since        string `json:"since,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	CrossProject bool   `json:"cross_project,omitempty"`
	Operator     string `json:"operator,omitempty"`
}

// ErrorListResult is the output for error_list.
type ErrorListResult struct {
	Events []errorobs.ErrorEvent `json:"events"`
	Count  int                   `json:"count"`
}

// ErrorGetInput is the input for error_get.
type ErrorGetInput struct {
	ID           int64  `json:"id"`
	CrossProject bool   `json:"cross_project,omitempty"`
	Operator     string `json:"operator,omitempty"`
}

// ErrorSummaryInput is the input for error_summary.
type ErrorSummaryInput struct {
	Hours int `json:"hours,omitempty"`
}

// ErrorResolveInput is the input for error_resolve.
type ErrorResolveInput struct {
	ID           int64  `json:"id"`
	Note         string `json:"note,omitempty"`
	CrossProject bool   `json:"cross_project,omitempty"`
	Operator     string `json:"operator,omitempty"`
}

// ErrorResolveResult is the output for error_resolve.
type ErrorResolveResult struct {
	ID       int64 `json:"id"`
	Resolved bool  `json:"resolved"`
}
