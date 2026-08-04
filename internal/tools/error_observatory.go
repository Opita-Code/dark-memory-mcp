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
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterErrorObservatory wires the 4 ERROR_OBS tools into the
// registry. All four are Store-bound (no orchestrator layer — the
// Store exposes the full CRUD contract directly).
func RegisterErrorObservatory(reg *Registry, st store.Store) {
	// error_list — backlog view.
	reg.Add(BindStore("error_list",
		"List error_events backlog rows (durable, classified error clusters). Filters: domain, severity, resolved, session_id, tool_name, since. Newest-first (last_seen_at DESC). Read-only. Part of the Error Observatory (spec 757): every silent-discard site + gate refusal + LLM infra failure lands here.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"domain": map[string]any{
					"type": "string",
					"enum": []string{"store", "llm", "gate", "validation", "network", "sweep", "unknown"},
					"description": "Classification axis. Empty = all.",
				},
				"severity": map[string]any{
					"type": "string",
					"enum": []string{"fatal", "error", "warn"},
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
			rows, err := s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
				Domain:    errorobs.Domain(in.Domain),
				Severity:  errorobs.Severity(in.Severity),
				Resolved:  in.Resolved,
				SessionID: in.SessionID,
				ToolName:  in.ToolName,
				Since:     in.Since,
				Limit:     limit,
			})
			if err != nil {
				return nil, err
			}
			return &ErrorListResult{Events: rows, Count: len(rows)}, nil
		}))

	// error_get — one cluster by id.
	reg.Add(BindStore("error_get",
		"Get one error_events cluster by id (Error Observatory). Returns null when the id does not exist in the active project. Read-only.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer", "description": "Row id."},
			},
			"required": []string{"id"},
		}),
		st,
		func(ctx context.Context, s store.Store, in ErrorGetInput) (*errorobs.ErrorEvent, error) {
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
		"Mark an error_events cluster as resolved with an operator note (backlog triage). Idempotent: resolving an already-resolved cluster returns ok. Returns ErrNotFound when the id does not exist in the active project. Operator-gated (INV-1 audit via WriteContext).",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":   map[string]any{"type": "integer", "description": "Cluster id to resolve."},
				"note": map[string]any{"type": "string", "description": "Resolution note (root cause, fix ref, ...). Optional."},
			},
			"required": []string{"id"},
		}),
		st,
		func(ctx context.Context, s store.Store, in ErrorResolveInput) (*ErrorResolveResult, error) {
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
	Domain    string `json:"domain,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Resolved  *bool  `json:"resolved,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Since     string `json:"since,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// ErrorListResult is the output for error_list.
type ErrorListResult struct {
	Events []errorobs.ErrorEvent `json:"events"`
	Count  int                   `json:"count"`
}

// ErrorGetInput is the input for error_get.
type ErrorGetInput struct {
	ID int64 `json:"id"`
}

// ErrorSummaryInput is the input for error_summary.
type ErrorSummaryInput struct {
	Hours int `json:"hours,omitempty"`
}

// ErrorResolveInput is the input for error_resolve.
type ErrorResolveInput struct {
	ID   int64  `json:"id"`
	Note string `json:"note,omitempty"`
}

// ErrorResolveResult is the output for error_resolve.
type ErrorResolveResult struct {
	ID       int64 `json:"id"`
	Resolved bool  `json:"resolved"`
}
