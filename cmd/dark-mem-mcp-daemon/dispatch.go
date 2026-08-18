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
// Spec 1176 §4.10 (2026-08-14): the FULL MCP surface is now served over
// the socket via daemon.Config.OnConn → server.Server.ServeStream, which
// runs the same MCPServer as the single-binary mode (initialize,
// tools/list, tools/call, notifications, resources). This Frame-based
// handler is retained as a COMPATIBILITY FALLBACK for internal tooling
// and tests that speak the Frame protocol directly; it supports a
// minimal subset:
//   - "ping"        -> {ok:true, ts:<unixnano>}
//   - "tools/list"  -> {tools:[{name, description}, ...]}
//
// `tools/call` via the Frame shim returns an error because the
// daemon never wired the in-process tool handler (§4.10 pending).
// Production harnesses never hit this branch: t6 (spec 1242)
// consolidated the boot path to single-binary legacy (the only
// active mode), so opencode and the wire conformance suite talk
// to that path directly. See ARCHITECTURE.md §boot path.
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
		// we surface this as a placeholder error.
		//
		// TODO(spec 1176 §4.10): wire tools/call through the daemon
		// by lifting t.Handler's signature to a bridge-friendly
		// variant (context + json.RawMessage + json.RawMessage).
		// Until then, tools/call only works through the single-
		// binary legacy boot (t6, spec 1242 — the active path).
		return nil, fmt.Errorf("tools/call via daemon socket not yet wired (spec 1176 §4.10); the single-binary legacy mode (t6) is the only available boot path. See ARCHITECTURE.md §boot path.")
	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
}
