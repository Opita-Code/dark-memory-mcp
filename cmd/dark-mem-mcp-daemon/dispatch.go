package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dark-agents/dark-memory-mcp/internal/server"
)

// handleRPC dispatches one JSON-RPC request to the dark-memory server's
// tool registry. The result is JSON-serializable (used as the bridge's
// response frame body).
//
// Today this supports a minimal subset of the MCP surface:
//   - "ping"        -> {ok:true, ts:<unixnano>}
//   - "tools/list"  -> {tools:[{name, description}, ...]}
//
// `tools/call` and notifications are explicitly NOT routed through
// this shim in v2.19.0 — they remain on the stdio bridge path until
// spec 1176 §4.10's MCP-over-socket plumbing lands in a follow-up.
// (Today, calling `tools/call` here returns an error that surfaces
// to the bridge caller; the harness can then fall back to spawning
// a legacy single-binary if needed.)
func handleRPC(ctx context.Context, srv *server.Server, method string, params []byte) (any, error) {
	switch method {
	case "ping":
		return map[string]any{"ok": true, "ts": nowUnixNano()}, nil
	case "tools/list":
		tools := srv.Registry().ListCanonical()
		extras := srv.Registry().ListExtras()
		out := make([]map[string]any, 0, len(tools)+len(extras))
		for _, t := range tools {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
			})
		}
		for _, t := range extras {
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
			})
		}
		return map[string]any{"tools": out}, nil
	case "tools/call":
		var args struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if args.Name == "" {
			return nil, fmt.Errorf("missing tool name")
		}
		// Lookup + dispatch. The in-process tool invocation goes
		// through the Server's per-tool Handler (the same one the
		// stdio MCP path uses). We construct a minimal request
		// envelope and call.
		t := srv.Registry().Get(args.Name)
		if t == nil {
			return nil, fmt.Errorf("unknown tool: %s", args.Name)
		}
		// We cannot easily call t.Handler directly (it's a typed
		// wrapper that takes mcplib.CallToolRequest). For v2.19.0,
		// we surface this as a placeholder error. The bridge will
		// detect this and fall back to spawning a fresh single-
		// binary mode (DARK_MEM_BRIDGE=0) for tools/call only.
		//
		// TODO(spec 1176 §4.10): wire tools/call through the daemon
		// by lifting t.Handler's signature to a bridge-friendly
		// variant (context + json.RawMessage + json.RawMessage).
		return nil, fmt.Errorf("tools/call via daemon socket not yet wired (spec 1176 §4.10); set DARK_MEM_BRIDGE=0 to fallback to single-binary mode")
	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
}
