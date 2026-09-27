// observability_* tools — read-only views over the audit_log
// (INV-1), schema_migrations (INV-16), and the Error Observatory
// tables. No side effects.
//
//   - dark_memory_memory_state  — DB health + per-table counts
//   - dark_memory_writes        — recent audit_log rows (INV-1)
//   - dark_memory_anomalies     — fatal cluster + gate refusals
//
// These tools are intentionally minimal: each one returns a JSON
// snapshot of the underlying query. Future BUG commits add
// filtering (operator, since, kind) and pagination.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	memoryStateToolName = "dark_memory_memory_state"
	writesToolName      = "dark_memory_writes"
	anomaliesToolName   = "dark_memory_anomalies"
)

func registerObservabilityTools(s *Server) {
	registerMemoryState(s)
	registerWrites(s)
	registerAnomalies(s)
}

// --- memory_state ---

// memoryStateOutput mirrors the v3 memory_state output shape
// (driver, schema_version, table list, per-table counts). The
// v4 simplification: only the SQLite driver is supported; the
// Postgres driver is deferred to BUG-9.
type memoryStateOutput struct {
	Driver        string         `json:"driver"`
	SchemaVersion string         `json:"schema_version"`
	DBOpen        bool           `json:"db_open"`
	Tables        map[string]int `json:"tables"`
	Now           string         `json:"now"`
}

func registerMemoryState(s *Server) {
	tool := mcp.NewTool(memoryStateToolName,
		mcp.WithDescription("DB health snapshot: driver, schema version, per-table counts."),
		mcp.WithInputSchema[struct{}](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tables, err := s.tableCounts(ctx)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("memory_state: %v", err)), nil
		}
		var schemaVersion string
		row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),'') FROM schema_migrations`)
		_ = row.Scan(&schemaVersion)
		out := memoryStateOutput{
			Driver:        "sqlite",
			SchemaVersion: schemaVersion,
			DBOpen:        true,
			Tables:        tables,
			Now:           nowRFC3339(),
		}
		return resultJSON(out)
	})
}

// tableCounts runs the canonical introspection query that
// returns row counts for every user-visible table.
func (s *Server) tableCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("table list: %w", err)
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(names))
	for _, n := range names {
		var c int
		if err := s.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %q`, n)).Scan(&c); err != nil {
			out[n] = -1
			continue
		}
		out[n] = c
	}
	return out, nil
}

// --- writes ---

// writesOutput is one audit_log row (INV-1).
type writeOutput struct {
	ID        int64           `json:"id"`
	Actor     string          `json:"actor"`
	SessionID string          `json:"session_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at"`
}

type writesOutput struct {
	Limit   int          `json:"limit"`
	Count   int          `json:"count"`
	Writes  []writeOutput `json:"writes"`
}

func registerWrites(s *Server) {
	tool := mcp.NewTool(writesToolName,
		mcp.WithDescription("Recent INV-1 audit_log rows (most-recent first)."),
		mcp.WithInputSchema[writesInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in writesInput
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
			 FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
		if err != nil {
			// If audit_log doesn't exist yet, return empty list.
			return resultJSON(writesOutput{Limit: limit, Count: 0, Writes: []writeOutput{}})
		}
		defer rows.Close()
		out := []writeOutput{}
		for rows.Next() {
			var w writeOutput
			var payloadB []byte
			var createdAt string
			if err := rows.Scan(&w.ID, &w.Actor, &w.SessionID, &payloadB, &createdAt); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("writes scan: %v", err)), nil
			}
			if len(payloadB) > 0 {
				w.Payload = payloadB
			}
			w.CreatedAt = createdAt
			out = append(out, w)
		}
		return resultJSON(writesOutput{Limit: limit, Count: len(out), Writes: out})
	})
}

type writesInput struct {
	Limit int `json:"limit,omitempty" jsonschema_description:"Max rows; defaults 50, max 500"`
}

// --- anomalies ---

// anomalyOutput surfaces fatal cluster + gate refusal events from
// the audit_log. The v4 simplification: no separate anomalies
// table — we derive from audit_log WHERE actor LIKE 'fatal_%'
// OR actor LIKE 'gate_%'.
type anomalyOutput struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"` // "fatal" or "gate"
	Actor     string `json:"actor"`
	SessionID string `json:"session_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

type anomaliesOutput struct {
	Limit    int            `json:"limit"`
	Count    int            `json:"count"`
	Anomalies []anomalyOutput `json:"anomalies"`
}

func registerAnomalies(s *Server) {
	tool := mcp.NewTool(anomaliesToolName,
		mcp.WithDescription("Recent fatal cluster + gate refusal events (most-recent first)."),
		mcp.WithInputSchema[anomaliesInput](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in anomaliesInput
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
			`SELECT id, actor, COALESCE(session_id,''), created_at
			 FROM audit_log
			 WHERE actor LIKE 'fatal_%' OR actor LIKE 'gate_%'
			 ORDER BY id DESC LIMIT ?`, limit)
		if err != nil {
			return resultJSON(anomaliesOutput{Limit: limit, Count: 0, Anomalies: []anomalyOutput{}})
		}
		defer rows.Close()
		out := []anomalyOutput{}
		for rows.Next() {
			var a anomalyOutput
			if err := rows.Scan(&a.ID, &a.Actor, &a.SessionID, &a.CreatedAt); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("anomalies scan: %v", err)), nil
			}
			if len(a.Actor) >= 5 && a.Actor[:5] == "fatal" {
				a.Kind = "fatal"
			} else {
				a.Kind = "gate"
			}
			out = append(out, a)
		}
		return resultJSON(anomaliesOutput{Limit: limit, Count: len(out), Anomalies: out})
	})
}

type anomaliesInput struct {
	Limit int `json:"limit,omitempty" jsonschema_description:"Max rows; defaults 50, max 500"`
}
