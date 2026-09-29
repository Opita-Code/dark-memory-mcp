// audit_verify tool (v4alpha) — Phase 2 (alpha.15, INV-12).
//
// One MCP tool: dark_memory_audit_verify. Walks the audit_log
// hash chain and reports whether every row's row_hash is consistent
// with its predecessor. Detects modification, deletion, and forgery.
//
// # Wire shape
//
//   dark_memory_audit_verify({start_id?, end_id?})
//     → {verified, broken_at, count, start_id, end_id, elapsed_ms}
//
// start_id and end_id are optional (defaults: smallest audit_id
// with row_hash NOT NULL, and MAX(audit_id) respectively).
//
// # Concurrency
//
// Read-only; no locks. The snapshot is point-in-time — a row
// committed DURING verify may or may not be visible. Acceptable:
// the goal is "did anyone tamper with the past", not "is the
// live DB consistent right now".
//
// # When does this emit audit?
//
// Never. Verify is read-only; emitting an audit row would pollute
// the chain (the verify itself would add a row that needs to be
// verified). Same rationale as summarize_session (PRE-1 C4).
package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/audit"
)

const auditVerifyToolName = "dark_memory_audit_verify"

// auditVerifyInput is the wire shape. Both fields optional;
// defaults are applied in audit.Verify (start_id = MIN where
// row_hash IS NOT NULL, end_id = MAX).
type auditVerifyInput struct {
	StartID *int64 `json:"start_id,omitempty" jsonschema_description:"First audit_id to walk (inclusive). Default: smallest audit_id with row_hash NOT NULL."`
	EndID   *int64 `json:"end_id,omitempty" jsonschema_description:"Last audit_id to walk (inclusive). Default: MAX(audit_id)."`
}

// auditVerifyOutput mirrors audit.VerifyResult with explicit JSON
// field names so the wire contract is stable across changes to
// the audit package.
type auditVerifyOutput struct {
	Verified  bool  `json:"verified"`
	BrokenAt  int64 `json:"broken_at"` // 0 if verified, else first broken audit_id
	Count     int   `json:"count"`     // rows walked (incl. legacy NULL rows skipped)
	StartID   int64 `json:"start_id"`
	EndID     int64 `json:"end_id"`
	ElapsedMS int64 `json:"elapsed_ms"`
}

func registerAuditVerifyTool(s *Server) {
	tool := mcp.NewTool(auditVerifyToolName,
		mcp.WithDescription("Walk the audit_log hash chain and verify each row's row_hash. Detects modification, deletion, and forgery. Returns {verified, broken_at, count, start_id, end_id, elapsed_ms}. Read-only; no audit emission."),
		mcp.WithInputSchema[auditVerifyInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in auditVerifyInput
		if err := bindArgs(req, &in); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Resolve pointer fields to 0 (audit.Verify's "use default"
		// sentinel).
		var startID, endID int64
		if in.StartID != nil {
			startID = *in.StartID
		}
		if in.EndID != nil {
			endID = *in.EndID
		}

		res, err := audit.Verify(ctx, s.db, startID, endID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("audit_verify: %v", err)), nil
		}

		out := auditVerifyOutput{
			Verified:  res.Verified,
			BrokenAt:  res.BrokenAt,
			Count:     res.Count,
			StartID:   res.StartID,
			EndID:     res.EndID,
			ElapsedMS: res.ElapsedMS,
		}
		return resultJSON(out)
	})
}