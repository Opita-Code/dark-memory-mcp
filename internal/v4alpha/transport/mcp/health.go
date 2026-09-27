// health_ping tool — operator's "is the server alive?" check.
// Returns server identity, version, schema version, and a
// process-start timestamp.
//
// Mirrors v3 dark_memory_health_ping (internal/tools/health.go
// RegisterHealth) but minimal: no canary / federation /
// observability fields. Those land in BUG-9 when the
// observability namespace is migrated to v4alpha.
package mcp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

const healthPingToolName = "dark_memory_health_ping"

// healthPingInput has no required fields — the operator just
// wants to know "are you there?". Schema is registered as an
// empty object so mcp-go's strict JSON Schema validation
// accepts `{}`.
func registerHealthTool(s *Server) {
	tool := mcp.NewTool(healthPingToolName,
		mcp.WithDescription("Return server identity, version, and a liveness snapshot. No side effects."),
		mcp.WithInputSchema[struct{}](),
	)
	s.mcpSrv.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		now := time.Now().UTC()
		startedAt := s.startedAt
		if startedAt == "" {
			startedAt = now.Format(time.RFC3339Nano)
			s.startedAt = startedAt
		}

		resp := healthPingResponse{
			runtimeInfo: runtimeInfo{
				ServerName:    ServerName,
				ServerVersion: Version,
				GoVersion:     goRuntimeVersion,
				SchemaVersion: schemaVersion,
			},
			StartedAt:    startedAt,
			Now:         now.Format(time.RFC3339Nano),
			UptimeMillis: uptimeMillis(startedAt, now),
			ToolCount:   6, // BUG-7 MVP: health + session*3 + memory*2
			DBOpen:      s.db != nil,
			Status:      "ok",
		}

		b, err := json.Marshal(resp)
		if err != nil {
			return mcp.NewToolResultError("health_ping marshal: " + err.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	})
}

// healthPingResponse is the JSON shape returned by health_ping.
// Exported as a type so tests can decode the response without
// hardcoding field names.
type healthPingResponse struct {
	runtimeInfo
	StartedAt    string `json:"started_at"`
	Now          string `json:"now"`
	UptimeMillis int64  `json:"uptime_ms"`
	ToolCount    int    `json:"tool_count"`
	DBOpen       bool   `json:"db_open"`
	Status       string `json:"status"`
}

// schemaVersion is the v4alpha marker stamped by migrate /
// serve. Kept in lockstep with cmd/dark-memory-v4/schemaVersion
// (duplicated here so the transport package has no dependency
// on cmd/).
const schemaVersion = "v4alpha/2026-09-27/001"

func uptimeMillis(startedAt string, now time.Time) int64 {
	t, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return 0
	}
	return now.Sub(t).Milliseconds()
}
