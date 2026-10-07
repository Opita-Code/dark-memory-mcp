// Package bench — tools_list.go: Tools-list micro-benchmarks.
//
// These benchmarks measure the canonical-order tools enumeration
// path that runs every time a harness asks for the tool surface
// (every MCP `initialize` + every `tools/list` request).
//
// The hot path is:
//   1. internal/tools.CanonicalOrder() — returns the 73-tool order
//      (PROJECT → SESSION → RESEARCH → AGENT_BOOTSTRAP → VIBE →
//      CONTEXT → AGENT_MEMORY → RECALL → JUDGE → POLICY →
//      OBSERVABILITY → ADMIN → VLP → EMBEDDER) at wire-name level.
//   2. tools.WireName(n) — convert canonical name to wire name
//      (e.g. "session_start" → "dark_memory_session_start"). Called
//      per tool during boot (server.go:73-76) to build the
//      canonicalPos map; the resulting map is reused for every
//      tools/list sort.
//   3. sort.SliceStable over the listed slice — happens on every
//      tools/list request (server.go:81-95).
//
// We benchmark both the boot-time map construction and the
// per-request sort.
//
//go:build bench
// +build bench

package bench

import (
	"context"
	"sort"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/tools"
	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// BenchmarkToolsCanonicalOrder measures the per-boot cost of
// constructing the canonical-position map at server.go:73-76.
func BenchmarkToolsCanonicalOrder(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		canonicalPos := make(map[string]int, 32)
		for j, n := range tools.CanonicalOrder() {
			canonicalPos[tools.WireName(n)] = j
		}
	}
}

// mockTools returns a synthetic tools/list payload with 73 entries
// in alphabetical order (the canonical case the harness sends). The
// bench sort uses the canonicalPos map to re-sort to namespace order.
func mockTools(pos map[string]int) []mcplib.Tool {
	canonical := tools.CanonicalOrder()
	out := make([]mcplib.Tool, 0, len(canonical))
	for i, n := range canonical {
		wire := tools.WireName(n)
		// Reverse the order so the sort has work. Out is around
		// 73 entries; sort is O(n log n).
		_ = pos
		_ = i
		out = append(out, mcplib.NewTool(wire, mcplib.WithDescription("synthetic")))
	}
	// Reverse so the test forces a real sort.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// BenchmarkToolsListSort measures the per-request cost of sorting
// the 73-tool canonical order on every tools/list request. This
// is what server.go:81-95 does.
func BenchmarkToolsListSort(b *testing.B) {
	canonicalPos := make(map[string]int, 32)
	for i, n := range tools.CanonicalOrder() {
		canonicalPos[tools.WireName(n)] = i
	}
	listed := mockTools(canonicalPos)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sort.SliceStable(listed, func(i, j int) bool {
			pi, oki := canonicalPos[listed[i].Name]
			pj, okj := canonicalPos[listed[j].Name]
			switch {
			case oki && okj:
				return pi < pj
			case oki:
				return true
			case okj:
				return false
			default:
				return listed[i].Name < listed[j].Name
			}
		})
	}
}

// BenchmarkToolsListFull measures the cost of the entire per-request
// tools/list closure (sort + filter + return). This is the exact
// code path the harness exercises.
func BenchmarkToolsListFull(b *testing.B) {
	canonicalPos := make(map[string]int, 32)
	for i, n := range tools.CanonicalOrder() {
		canonicalPos[tools.WireName(n)] = i
	}
	listed := mockTools(canonicalPos)
	canonicalOrderFilter := func(_ context.Context, listed []mcplib.Tool) []mcplib.Tool {
		sort.SliceStable(listed, func(i, j int) bool {
			pi, oki := canonicalPos[listed[i].Name]
			pj, okj := canonicalPos[listed[j].Name]
			switch {
			case oki && okj:
				return pi < pj
			case oki:
				return true
			case okj:
				return false
			default:
				return listed[i].Name < listed[j].Name
			}
		})
		return listed
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = canonicalOrderFilter(context.Background(), listed)
	}
}