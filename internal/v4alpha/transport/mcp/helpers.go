// helpers shared across all tool registration files. Kept here
// (not in server.go) so each tool file imports only its own
// dependencies.
package mcp

import (
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// nowRFC3339 returns the current time in canonical RFC3339
// format. Used by every tool that surfaces a timestamp so the
// output is grep-friendly.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// bindArgs unmarshals the JSON-RPC CallToolRequest arguments into
// the target struct. mcp-go v0.40.0 returns arguments as
// map[string]any via req.GetArguments(); we re-encode to JSON
// and unmarshal into the typed struct so the per-tool input
// shape is the single source of truth.
func bindArgs(req mcp.CallToolRequest, target any) error {
	args := req.GetArguments()
	if len(args) == 0 {
		return nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, target); err != nil {
		return err
	}
	return nil
}

// resultJSON wraps a JSON-marshalable value in an mcp-go
// CallToolResult with text content. Used by every tool that
// returns structured data.
func resultJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(b)), nil
}
