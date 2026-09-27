// error_obs_* tools — Error Observatory interface. The v3 has a
// dedicated error_events table (clustered by signature), but the
// v4 derivation: anomalies + error_summary are read-only queries
// over the audit_log + audit_log.error_summary view. This keeps
// the v4 schema minimal at the cost of less granular error
// tracking. The full Error Observatory lands in BUG-9.
//
//   - dark_memory_error_summary   — global metrics (counts by domain/severity)
//   - dark_memory_error_list      — recent error audit rows (most-recent first)
//   - dark_memory_error_get       — one audit row by id
//   - dark_memory_error_resolve   — append a 'resolved' note to the audit_log
//
// resolve is a write — it inserts a new audit_log row that
// references the original error id. The original row is never
// mutated (INV-1 audit integrity).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	errorSummaryToolName = "dark_memory_error_summary"
	errorListToolName    = "dark_memory_error_list"
	errorGetToolName     = "dark_memory_error_get"
	errorResolveToolName = "dark_memory_error_resolve"
)

func registerErrorObsTools(s *Server) {
	registerErrorSummary(s)
	registerErrorList(s)
	registerErrorGet(s)
	registerErrorResolve(s)
}

// --- error_summary ---

// errorSummaryOutput is the global Error Observatory snapshot.
// All counts are derived from the audit_log; the v4 has no
// separate error_events table.
type errorSummaryOutput struct {
	HoursWindow    int            `json:"hours_window"`
	TotalErrors    int            `json:"total_errors"`
	TotalFatal     int            `json:"total_fatal"`
	TotalWarn      int            `json:"total_warn"`
	ByDomain       map[string]int `json:"by_domain"`
	TopRecurring   []string       `json:"top_recurring"`
}

func registerErrorSummary(s *Server) {
	tool := mcp.NewTool(errorSummaryToolName,
		mcp.WithDescription("Global error metrics from the audit_log (counts + top recurring actors)."),
		mcp.WithInputSchema[errorSummaryInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in errorSummaryInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hours := in.Hours
		if hours <= 0 {
			hours = 1
		}
		if hours > 168 {
			hours = 168
		}
		out := errorSummaryOutput{
			HoursWindow:  hours,
			ByDomain:     map[string]int{},
			TopRecurring: []string{},
		}
		// Per-actor counts limited to error_*/fatal_*/warn_*/gate_*.
		rows, err := s.db.QueryContext(ctx,
			`SELECT actor, COUNT(*) FROM audit_log
			 WHERE actor LIKE 'error_%' OR actor LIKE 'fatal_%'
			    OR actor LIKE 'warn_%'  OR actor LIKE 'gate_%'
			 GROUP BY actor
			 ORDER BY COUNT(*) DESC LIMIT 10`)
		if err != nil {
			return resultJSON(out)
		}
		defer rows.Close()
		for rows.Next() {
			var actor string
			var n int
			if err := rows.Scan(&actor, &n); err == nil {
				out.TopRecurring = append(out.TopRecurring, fmt.Sprintf("%s=%d", actor, n))
				switch {
				case strings.HasPrefix(actor, "fatal_"):
					out.TotalFatal += n
				case strings.HasPrefix(actor, "warn_"):
					out.TotalWarn += n
				default:
					out.TotalErrors += n
				}
				// Domain = actor prefix up to first '_'
				if i := strings.Index(actor, "_"); i > 0 {
					out.ByDomain[actor[:i]] += n
				}
			}
		}
		return resultJSON(out)
	})
}

type errorSummaryInput struct {
	Hours int `json:"hours,omitempty" jsonschema_description:"Lookback window in hours (1..168). Defaults 1."`
}

// --- error_list ---

type errorListInput struct {
	Limit int `json:"limit,omitempty" jsonschema_description:"Max rows; defaults 50, max 500"`
}

type errorRow struct {
	ID        int64           `json:"id"`
	Actor     string          `json:"actor"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at"`
}

type errorListOutput struct {
	Limit  int         `json:"limit"`
	Count  int         `json:"count"`
	Errors []errorRow  `json:"errors"`
}

func registerErrorList(s *Server) {
	tool := mcp.NewTool(errorListToolName,
		mcp.WithDescription("Recent error audit_log rows (most-recent first)."),
		mcp.WithInputSchema[errorListInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in errorListInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT id, actor, COALESCE(session_id,''), payload, created_at
			 FROM audit_log
			 WHERE actor LIKE 'error_%' OR actor LIKE 'fatal_%'
			    OR actor LIKE 'warn_%'  OR actor LIKE 'gate_%'
			 ORDER BY id DESC LIMIT ?`, limit)
		if err != nil {
			return resultJSON(errorListOutput{Limit: limit, Count: 0, Errors: []errorRow{}})
		}
		defer rows.Close()
		out := []errorRow{}
		for rows.Next() {
			var e errorRow
			var payloadB []byte
			if err := rows.Scan(&e.ID, &e.Actor, &e.SessionID, &payloadB, &e.CreatedAt); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("error_list scan: %v", err)), nil
			}
			if len(payloadB) > 0 {
				e.Payload = payloadB
			}
			out = append(out, e)
		}
		return resultJSON(errorListOutput{Limit: limit, Count: len(out), Errors: out})
	})
}

// --- error_get ---

type errorGetInput struct {
	ID int64 `json:"id" jsonschema:"required" jsonschema_description:"Audit row id"`
}

type errorGetOutput struct {
	ID        int64           `json:"id"`
	Actor     string          `json:"actor"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at"`
	Found     bool            `json:"found"`
}

func registerErrorGet(s *Server) {
	tool := mcp.NewTool(errorGetToolName,
		mcp.WithDescription("Read one audit_log row by id. Found=false when the id does not exist."),
		mcp.WithInputSchema[errorGetInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in errorGetInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ID <= 0 {
			return mcp.NewToolResultError("error_get: id must be positive"), nil
		}
		var out errorGetOutput
		var payloadB []byte
		err := s.db.QueryRowContext(ctx,
			`SELECT id, actor, COALESCE(session_id,''), payload, created_at
			 FROM audit_log WHERE id = ?`, in.ID).Scan(
			&out.ID, &out.Actor, &out.SessionID, &payloadB, &out.CreatedAt)
		if err != nil {
			return resultJSON(errorGetOutput{ID: in.ID, Found: false})
		}
		if len(payloadB) > 0 {
			out.Payload = payloadB
		}
		out.Found = true
		return resultJSON(out)
	})
}

// --- error_resolve ---

// error_resolve appends a new audit_log row whose payload
// references the original error id and the operator's note.
// The original row is never modified (INV-1).
type errorResolveInput struct {
	ID       int64  `json:"id" jsonschema:"required" jsonschema_description:"Audit row id to resolve"`
	Note     string `json:"note,omitempty" jsonschema_description:"Resolution note (root cause, fix ref, etc.)"`
	Operator string `json:"operator" jsonschema:"required" jsonschema_description:"Operator id (INV-1 audit owner)"`
	SessionID string `json:"session_id,omitempty" jsonschema_description:"Optional session_id for cross-reference"`
}

type errorResolveOutput struct {
	OriginalID int64  `json:"original_id"`
	AuditID    int64  `json:"audit_id"`
	ResolvedAt string `json:"resolved_at"`
}

func registerErrorResolve(s *Server) {
	tool := mcp.NewTool(errorResolveToolName,
		mcp.WithDescription("Append a 'resolved' note to the audit_log. Original row is never mutated."),
		mcp.WithInputSchema[errorResolveInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in errorResolveInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if in.ID <= 0 {
			return mcp.NewToolResultError("error_resolve: id must be positive"), nil
		}
		if in.Operator == "" {
			return mcp.NewToolResultError("error_resolve: operator is required (INV-1)"), nil
		}
		// Verify the original row exists so we can return a clear error.
		var exists int
		err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM audit_log WHERE id = ?`, in.ID).Scan(&exists)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("error_resolve: original id %d not found", in.ID)), nil
		}
		// Build the resolution payload as JSON: {"ref_id": N, "note": "..."}.
		payload, _ := json.Marshal(map[string]any{
			"ref_id": in.ID,
			"note":   in.Note,
		})
		auditID, err := s.audit.Write(ctx, "resolve", in.SessionID, payload)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("error_resolve: %v", err)), nil
		}
		out := errorResolveOutput{
			OriginalID: in.ID,
			AuditID:    auditID,
			ResolvedAt: nowRFC3339(),
		}
		return resultJSON(out)
	})
}
