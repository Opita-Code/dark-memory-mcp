// Package orchestration — v2.8.0-alpha feature flags.
//
// DARK_MEMORY_V280 is the single env var that gates all 5 P1 features
// of the v2.8.0-alpha release (memory timing & coordination). When
// the flag is unset (the v2.7.x default), every gated path behaves
// exactly as it did in v2.7.x — zero breaking changes. When the flag
// is "1", the new behavior is live.
//
// Default value when unset: "0" (off). This is the conservative
// default; operators opt in by exporting DARK_MEMORY_V280=1 before
// starting the binary.
//
// The flag is read at process boot by cmd/dark-mem-mcp/main.go and
// passed into the Orchestrator struct (this file owns the lookup
// helpers). It's a constant for the lifetime of the process — there
// is no hot-swap.
//
// Five features gated:
//   - A1 Decision auto-save on vibe_publish verdict=aligned
//   - A4 Todo auto-save on vibe_spec + auto-archive on aligned publish
//   - B1 Cold start + token budget on session_start
//   - C2 Subagent scope handoff (active_subagents table + agent_id
//     resolution chain extension)
//   - D5 Cross-project error code (ErrCrossProjectAccess)
//
// Out of scope (separate env vars / future flags):
//   - DARK_MINDSET_DISABLED (Phase 1 cache disable; v2.7.0-alpha)
//   - DARK_MINDSET_CACHE_TTL, DARK_MINDSET_MAX_ITERATIONS, etc.
//   - DARK_FEDERATION_PEER_DSN (F45 cross-project reads)
//   - DARK_REDTEAM (armed mode for redteam tools)
package orchestration

import "os"

// v280Enabled reports whether DARK_MEMORY_V280 is "1" (or "true",
// case-insensitive). Returns false on any other value OR unset env.
// Empty string + explicit "0" + garbage all map to false.
func v280Enabled() bool {
	v := os.Getenv("DARK_MEMORY_V280")
	if v == "" {
		return false
	}
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "Yes", "on", "ON", "On":
		return true
	}
	return false
}
