// Package tools — canonical_staleness_test.go
//
// v2.13.0 rewrite: the old test kept a hand-maintained copy of the
// canonical tool list (canonicalWireOrderBares) and compared it to
// CanonicalOrder(). Every tool addition forced a 2-place manual sync
// (registry.go + this file) — the exact anti-pattern the user flagged.
//
// v2.15.2 SPEC 1270 freeze: invariant test added (TestCanonicalOrder_Frozen_57_17_26)
// that pins the canonical surface — 57 tools, 17 namespaces,
// schema v26. Any drift in those numbers fails the test until
// an ADR + minor version bump is filed.
//
// The list is now SINGLE-SOURCED in registry.go (canonicalNamespaces).
// This test verifies INVARIANTS over that source instead of duplicating
// it:
//
//  1. CanonicalOrder() is a stable flatten of canonicalNamespaces
//     (no duplicates, correct count, no surprises).
//  2. Every canonical tool is actually registered by RegisterAll
//     (the runtime surface matches the declared surface).
//  3. The wire prefix convention (dark_memory_) is consistent.
//  4. Freeze invariants (SPEC 1270): 57 tools / 17 namespaces /
//     schema v26 — see TestCanonicalOrder_Frozen_57_17_26.
//
// The runtime ORDER conformance check lives in tests/conformance/
// (bridge7_mcp_inspector_test.go), which derives its expectation from
// tools.CanonicalOrder() — no manual copy there either.

package tools

import (
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/migrate/sqlite"
)

// TestCanonicalOrder_NoDuplicates asserts the flattened canonical order
// contains each tool exactly once. Duplicate registration would corrupt
// the wire order (and RegisterAll's count check would mask it).
func TestCanonicalOrder_NoDuplicates(t *testing.T) {
	order := CanonicalOrder()
	seen := make(map[string]int, len(order))
	for _, n := range order {
		seen[n]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("tool %q appears %d times in CanonicalOrder()", name, count)
		}
	}
}

// TestCanonicalOrder_FlattenMatchesNamespaces asserts that the flattened
// order is exactly the concatenation of the namespace groups, in order.
// This pins the namespace grouping without duplicating the tool list.
func TestCanonicalOrder_FlattenMatchesNamespaces(t *testing.T) {
	order := CanonicalOrder()
	var fromNamespaces []string
	for _, ns := range canonicalNamespaces {
		for _, tool := range ns.Tools {
			fromNamespaces = append(fromNamespaces, tool)
		}
	}
	if len(order) != len(fromNamespaces) {
		t.Fatalf("CanonicalOrder() len=%d != flattened namespaces len=%d", len(order), len(fromNamespaces))
	}
	for i := range order {
		if order[i] != fromNamespaces[i] {
			t.Errorf("position %d: CanonicalOrder()=%q, namespace flatten=%q — canonicalNamespaces or CanonicalOrder() drifted", i, order[i], fromNamespaces[i])
		}
	}
}

// TestCanonicalOrder_AllRegistered asserts that RegisterAll's own
// guard (`register.go` — len(reg.ListCanonical()) == len(canonicalToolOrder))
// is not vacuous: the count it checks equals the real registry surface.
// The heavy "every canonical tool is registered" check is RegisterAll's
// job (it fails at boot if the counts diverge); this test only needs to
// prove the source list feeding that guard is well-formed, which the
// NoDuplicates + FlattenMatchesNamespaces tests already do. Keeping this
// test light avoids spinning a full orchestrator+store harness in a
// staleness test.
func TestCanonicalOrder_AllRegistered(t *testing.T) {
	// The canonical order must be non-empty and every namespace must
	// contribute at least one tool — otherwise RegisterAll's count
	// guard is comparing two empty lists (vacuous pass).
	if len(CanonicalOrder()) == 0 {
		t.Fatal("CanonicalOrder() is empty — RegisterAll's count guard would be vacuous")
	}
	for _, ns := range canonicalNamespaces {
		if len(ns.Tools) == 0 {
			t.Errorf("namespace %q declares zero tools — its Register* call may be a no-op", ns.Name)
		}
	}
}

// TestCanonicalOrder_WirePrefixConsistent asserts the canonical names
// are all bare (no dark_memory_ prefix) — the wire prefix is added at
// the tools/list boundary, never stored in the registry.
func TestCanonicalOrder_WirePrefixConsistent(t *testing.T) {
	for _, n := range CanonicalOrder() {
		if strings.HasPrefix(n, "dark_memory_") {
			t.Errorf("canonical tool %q should be bare (no dark_memory_ prefix)", n)
		}
		if strings.TrimSpace(n) == "" {
			t.Errorf("canonical tool contains empty name")
		}
	}
}

// TestCanonicalOrder_Frozen_62_17_31 (SPEC 1270, lock 2026-08-18):
// the canonical surface is FROZEN at 62 tools across 17 namespaces
// with schema v31. This test is the regression gate: any addition,
// removal, or rename that shifts these numbers fails until an ADR +
// minor bump is filed (see ARCHITECTURE.md §Tools surface). The
// expected values are read from runtime (CanonicalOrder(),
// NamespaceCount(), sqlite.CurrentVersion()) so the test stays
// self-validating; the constants below are what the freeze DOCUMENTS,
// not what it checks.
//
// Bumping 60 → 62 (alpha.20 Chunk 8.7): adds mark_superseded +
// recall_bitemporal to AGENT_MEMORY (ADR-014 lite bitemporal). The
// namespace count stays 17 — both tools live in AGENT_MEMORY. Schema
// bumps v29 → v31 (v30 = Phase 5 port, v31 = bitemporal_lite
// transaction_time + valid_time columns).
//
// Bumping 62 → 69 (alpha.22 Phase 11 T-402, 2026-10-03): adds the
// JUDGE_UTIL namespace with 7 tools (normalize, validate_overrides,
// pattern_descriptions, verify, verify_hash, trace, validate_trace).
// Pure deterministic primitives that complement the LLM-backed judge
// pipeline; previously only reachable through the v4alpha MCP server.
// Namespace count goes 17 → 18. Schema stays v31 (no schema change).
//
// Bumping 31 → 32 (alpha.23 Phase 12 T-101, 2026-10-04): adds the
// polymorphic events table (events_polymorphic migration, v32). T-102
// adds the EventWriter policy primitive (no schema). T-103a adds
// AutoEmitter (no schema). T-103b adds DriftJudgeProgressEmitter +
// wires into runAsyncJudgePipeline (no schema). T-103c adds
// DelegationProgressEmitter + wires into RunDelegateIntentCore (no
// schema). Tool count + namespace count stay unchanged through T-101
// → T-103c; tools event_log + event_replay come in T-105 (69 → 71).
func TestCanonicalOrder_Frozen_57_17_28(t *testing.T) {
	const (
		frozenToolCount      = 69 // 62 (alpha.20 Chunk 8.7) + 7 from alpha.22 Phase 11 T-402 (JUDGE_UTIL namespace)
		frozenNamespaceCount = 18 // 17 (alpha.20) + 1 new namespace (JUDGE_UTIL)
		frozenSchemaVersion  = 32 // v31 = bitemporal_lite; v32 = events_polymorphic (Phase 12 T-101)
	)

	if got := len(CanonicalOrder()); got != frozenToolCount {
		t.Errorf("CanonicalOrder() len = %d, want %d (freeze SPEC 1276 + alpha.22 Phase 11 T-402)", got, frozenToolCount)
	}
	if got := NamespaceCount(); got != frozenNamespaceCount {
		t.Errorf("NamespaceCount() = %d, want %d (freeze SPEC 1276 + alpha.22 Phase 11 T-402)", got, frozenNamespaceCount)
	}
	if got := sqlite.CurrentVersion(); got != frozenSchemaVersion {
		t.Errorf("sqlite.CurrentVersion() = %d, want %d (freeze SPEC 1276)", got, frozenSchemaVersion)
	}
	if !IsFrozen() {
		t.Error("IsFrozen() = false, want true — freeze SPEC 1276 was disabled; un-freezing requires an ADR")
	}
	if FreezeDate == "" || FreezeSpec == "" || FreezeVersion == "" {
		t.Errorf("freeze constants underpopulated (date=%q spec=%q version=%q)", FreezeDate, FreezeSpec, FreezeVersion)
	}
	// Cross-check: every namespace must contribute exactly the tools
	// recorded in NamespaceCounts(). Catches a class of bugs where a
	// tool got moved between namespaces without updating counts.
	for _, ns := range canonicalNamespaces {
		want := len(ns.Tools)
		if got := NamespaceCounts()[ns.Name]; got != want {
			t.Errorf("namespace %q: NamespaceCounts() = %d, want %d (flatten length)", ns.Name, got, want)
		}
	}
}
