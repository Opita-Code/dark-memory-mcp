// Package tools — canonical_staleness_test.go
//
// v2.7.1 deferred cleanup. The "4 commits to ship 1 release" pattern
// (v2.6.1→v2.6.2, v2.7.0-alpha) repeated because the hand-maintained
// canonicalWireOrder() helper in tests/conformance/bridge7_*
// got out of sync with the actual canonical tool order each time
// we added a tool. The operator had to update 4 places manually.
//
// This test catches the staleness at CI time: if the canonical wire
// order (the source-of-truth mirror in this file) diverges from
// tools.CanonicalOrder() (the source of truth in registry.go), the
// test fails immediately with a precise diff of what changed.
//
// The test does NOT replace tests/conformance/TestBridge7_ListToolsCanonical
// (which checks the WIRE order tools/list returns). That test catches
// runtime reordering bugs. This test catches drift in the source
// code itself.
//
// Placement note: this test lives in internal/tools (not tests/conformance)
// because tests/conformance spawns a live server (slow, ~30s per run).
// This is a pure unit test that just compares two slices — runs in
// milliseconds on every CI build.

package tools

import (
	"strings"
	"testing"
)

// TestCanonicalWireOrder_NotStale asserts that the hand-maintained
// canonicalWireOrderBares() in this file stays in sync with
// tools.CanonicalOrder() (the source of truth).
//
// Failure mode this catches: operator adds a tool to registry.go +
// bumps cardinality guard, forgets to update canonicalWireOrderBares()
// helper, ships, CI fails with confusing per-position errors that
// take 30+ minutes to diagnose.
//
// With this test, the failure is: "canonicalWireOrder length (N) !=
// CanonicalOrder length (M), missing tools: [mindset_apply]". Operator
// fixes it in one place, one place.
func TestCanonicalWireOrder_NotStale(t *testing.T) {
	want := canonicalWireOrderBares()
	got := CanonicalOrder()

	// Build set for membership checks.
	gotSet := make(map[string]struct{}, len(got))
	for _, n := range got {
		gotSet[n] = struct{}{}
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, n := range want {
		wantSet[n] = struct{}{}
	}

	// Length match — the cheap signal.
	if len(want) != len(got) {
		t.Errorf("canonicalWireOrder length (%d) != CanonicalOrder length (%d).\n"+
			"  Re-sync canonicalWireOrderBares() in internal/tools/canonical_staleness_test.go.\n"+
			"  Single source of truth: internal/tools/registry.go canonicalToolOrder.",
			len(want), len(got))
	}

	// Missing tools (in CanonicalOrder but not in canonicalWireOrder).
	var missing []string
	for _, n := range got {
		if _, ok := wantSet[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("canonicalWireOrder is MISSING %d tools: %v\n"+
			"  These tools are in tools.CanonicalOrder() but not in canonicalWireOrderBares().\n"+
			"  Update internal/tools/canonical_staleness_test.go canonicalWireOrderBares() to include them.",
			len(missing), missing)
	}

	// Extra tools (in canonicalWireOrder but not in CanonicalOrder) —
	// these would be tools removed from the canonical.
	var extra []string
	for _, n := range want {
		if _, ok := gotSet[n]; !ok {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		t.Errorf("canonicalWireOrder has %d STALE tools (removed from canonical): %v\n"+
			"  Update internal/tools/canonical_staleness_test.go canonicalWireOrderBares() to remove them.",
			len(extra), extra)
	}

	// Position drift — same set but wrong order. Catches accidental
	// reordering within a namespace.
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] != got[i] {
			t.Errorf("canonicalWireOrder position %d: want %q (canonical), got %q (CanonicalOrder)\n"+
				"  Find the diff and reorder canonicalWireOrderBares() to match tools.CanonicalOrder().",
				i, want[i], got[i])
			break // first diff only — keeps the failure output readable
		}
	}
}

// canonicalWireOrderBares returns the bare (unprefixed) names in the
// order they should appear on the wire per canonical RFC D-9. Mirrored
// from internal/tools/registry.go.
//
// IMPORTANT: this MUST stay in sync with tools.CanonicalOrder().
// TestCanonicalWireOrder_NotStale enforces this; if you add a tool
// and break it, CI will catch it.
func canonicalWireOrderBares() []string {
	return []string{
		// PROJECT (1) — v1.2.0
		"project_create",
		// SESSION (4)
		"session_start", "session_resume", "session_status", "session_close",
		// RESEARCH (3)
		"research_topic", "research_recall", "research_resume_thread",
		// AGENT_BOOTSTRAP (3) — v2.6.0
		"agent_bootstrap", "agent_recommend_companions", "agent_detect_environment",
		// VIBE (4)
		"vibe_publish", "vibe_spec", "pipeline_status", "resolve_drift",
		// CONTEXT (4) — v2.0.0 grew from 3 to 4 with `recall`
		"artifact_context", "spec_context", "session_context", "recall",
		// AGENT_MEMORY (10) — v2.1.0 (5) + v2.3.0 (1: recall) +
		// v2.8.0-alpha C2 (2: subagent register/unregister bindings
		// for active_subagents table) +
		// v2.9.0-alpha PR-3 (1: entities, id-only read of the
		// agent_memory_entities side-table) +
		// v2.9.3 (1: delegate, delegation context for sub-agent spawns).
		"agent_memory_save", "agent_memory_list", "agent_memory_recall", "agent_memory_get", "agent_memory_update", "agent_memory_archive", "agent_memory_delegate", "agent_memory_entities", "subagent_register", "subagent_unregister",
		// MINDSET (1) — v2.7.0-alpha
		"mindset_apply",
		// DELEGATION (1) — Wave 5C
		"delegate_intent",
		// JUDGE (3)
		"judge", "consensus", "judgment_history",
		// POLICY (2)
		"active_policy", "load_constitution",
		// OBSERVABILITY (4) — v1.3.0 grew from 3 to 4 with health_ping
		"memory_state", "writes", "anomalies", "health_ping",
		// ADMIN (3)
		"admin_migrate", "admin_schema_status", "admin_vacuum",
		// L6-VLP (1) — DMAP v1.1
		"vlp_handle_event",
		// EMBEDDER (1) — v2.9.0-alpha PR-2. Consent gate for hybrid
		// retrieval per row 164 §3. Single call per project boot —
		// returns the verbatim prompt the harness's LLM should surface
		// when no embedder is detected at first search.
		"embedder_setup_prompt",
	}
}

// stripWirePrefix removes the "dark_memory_" prefix from a tool name.
// Helper exported for callers that compare against canonicalWireOrderBares.
// Currently unused but kept here so future tests don't reach across
// packages.
var _ = stripWirePrefix

// stripWirePrefix removes the "dark_memory_" prefix from a tool name.
func stripWirePrefix(s string) string {
	const prefix = "dark_memory_"
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}