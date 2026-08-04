// Package tools — observability.go: the OBSERVABILITY namespace (4 tools).
//
// Per RFC §5 / D-9:
//	dark_memory_memory_state
//	dark_memory_writes
//	dark_memory_anomalies
//	dark_memory_health_ping          (v1.3.0)
//
// Maps to orchestrator O10 (MemoryState) + 2 new read-only helpers
// (writes lists recent write_audit rows; anomalies reads the
// anomaly_events table — INV-5 cache mismatches + INV-3 canary hits)
// + 1 health probe (health_ping — v1.3.0; safe for K8s liveness, no
// audit/VLP side effects; see health.go for the contract).
package tools

import (
	"context"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterObservability wires the 4 OBSERVABILITY tools into the registry.
// v1.3.0: registers health_ping as the 4th tool. health_ping is
// intentionally sibling to memory_state (not a replacement) — see
// health.go for the rationale.
func RegisterObservability(reg *Registry, orch *orchestration.Orchestrator, st store.Store) {
	// health_ping — v1.3.0. Operator-facing liveness probe; sits FIRST
	// in the namespace so monitoring rules can pattern-match on
	// "memory_state" or "health_ping" by index without confusing them.
	// Wired via the storeBridge shim in health.go so tests can stub it.
	RegisterHealth(reg, st)
	// memory_state — wraps O10 MemoryState orchestrator.
	reg.Add(BindOrchestrator("memory_state",
		"Return the runtime memory snapshot: driver, schema version, table list, per-table counts, active project, canary presence. Read-only.",
		MustJSONSchema(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, in struct{}) (*orchestration.MemoryStateResult, error) {
			return orch.MemoryState(ctx)
		}))

	// writes — read-only list of recent write_audit rows.
	reg.Add(BindStore("writes",
		"List recent write_audit rows (every Save emits one, per INV-1). Read-only.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit":      map[string]any{"type": "integer", "description": "Max rows. Default 50."},
				"session_id": map[string]any{"type": "string", "description": "Filter by session_id. Empty = all."},
				"actor":      map[string]any{"type": "string", "description": "Filter by actor. Empty = all."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in WritesInput) (*WritesResult, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 50
			}
			evts, err := s.ListWrites(ctx, audit.ListFilters{
				SessionID: in.SessionID,
				Actor:     in.Actor,
				Limit:     limit,
			})
			if err != nil {
				return nil, err
			}
			out := make([]WriteEntry, 0, len(evts))
			for _, e := range evts {
				out = append(out, WriteEntry{
					ID:            e.ID,
					Actor:         e.Actor,
					SessionID:     e.SessionID,
					WritePath:     e.WritePath,
					ContentSHA256: e.ContentSHA256,
					CreatedAt:     e.CreatedAt,
				})
			}
			return &WritesResult{Writes: out, Count: len(out)}, nil
		}))

	// anomalies — read-only. v2.11.0 (spec 757): RESURRECTED. The
	// previous implementation was a dead stub that always returned an
	// empty list ("anomaly_events query not yet exposed by Store").
	// The Error Observatory (error_events table, migration v25) now
	// provides the anomaly surface: this tool queries error_events
	// for the anomaly-shaped clusters — severity=fatal (systemic
	// damage) + domain=gate (INV-3 canary hits, INV-5 cache
	// mismatches, gate refusals). The response shape is preserved
	// (anomalies + count) with the note field explaining the mapping.
	reg.Add(BindStore("anomalies",
		"List recent anomaly events from the Error Observatory: severity=fatal clusters + domain=gate refusals (INV-3 canary hits, INV-5 cache mismatches, capability/scope refusals). Read-only. Kind filter: fatal | gate. Empty = both.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "Max rows. Default 50."},
				"kind":  map[string]any{"type": "string", "description": "Filter by kind (fatal | gate). Empty = all."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in AnomaliesInput) (*AnomaliesResult, error) {
			limit := in.Limit
			if limit <= 0 {
				limit = 50
			}
			unresolved := false
			var rows []errorobs.ErrorEvent
			var err error
			switch in.Kind {
			case "fatal":
				rows, err = s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
					Severity: errorobs.SeverityFatal,
					Resolved: &unresolved,
					Limit:    limit,
				})
			case "gate":
				rows, err = s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
					Domain:   errorobs.DomainGate,
					Resolved: &unresolved,
					Limit:    limit,
				})
			default:
				// Both: two queries, merged, newest-first by id desc.
				fatal, ferr := s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
					Severity: errorobs.SeverityFatal,
					Resolved: &unresolved,
					Limit:    limit,
				})
				gate, gerr := s.ListErrorEvents(ctx, errorobs.ErrorListFilters{
					Domain:   errorobs.DomainGate,
					Resolved: &unresolved,
					Limit:    limit,
				})
				if ferr != nil {
					err = ferr
				} else if gerr != nil {
					err = gerr
				} else {
					rows = mergeByIDDesc(fatal, gate, limit)
				}
			}
			if err != nil {
				return nil, err
			}
			out := make([]AnomalyEntry, 0, len(rows))
			for _, e := range rows {
				out = append(out, AnomalyEntry{
					ID:        e.ID,
					Kind:      string(e.Domain) + ":" + e.Code,
					Severity:  string(e.Severity),
					Detail:    e.Message,
					CreatedAt: e.LastSeenAt,
				})
			}
			return &AnomaliesResult{
				Anomalies: out,
				Count:     len(out),
				Note:      "backed by error_events (Error Observatory, spec 757): severity=fatal + domain=gate clusters, unresolved only",
			}, nil
		}))
}

// WritesInput is the input for writes.
type WritesInput struct {
	Limit     int    `json:"limit,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

// WritesResult is the output for writes.
type WritesResult struct {
	Writes []WriteEntry `json:"writes"`
	Count  int          `json:"count"`
}

// WriteEntry is one write_audit row in the listing.
type WriteEntry struct {
	ID            int64  `json:"id"`
	Actor         string `json:"actor"`
	SessionID     string `json:"session_id,omitempty"`
	WritePath     string `json:"write_path,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	CreatedAt     string `json:"created_at"`
}

// AnomaliesInput is the input for anomalies.
type AnomaliesInput struct {
	Limit int    `json:"limit,omitempty"`
	Kind  string `json:"kind,omitempty"`
}

// AnomaliesResult is the output for anomalies.
type AnomaliesResult struct {
	Anomalies []AnomalyEntry `json:"anomalies"`
	Count     int            `json:"count"`
	Note      string         `json:"note,omitempty"`
}

// AnomalyEntry is one anomaly event in the listing.
type AnomalyEntry struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"` // canary_hit | cache_mismatch
	Severity  string `json:"severity,omitempty"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}

// mergeByIDDesc merges two newest-first lists by id descending,
// dedupes, caps at limit. Helper for the anomalies both-kinds path.
func mergeByIDDesc(a, b []errorobs.ErrorEvent, limit int) []errorobs.ErrorEvent {
	seen := map[int64]bool{}
	out := make([]errorobs.ErrorEvent, 0, len(a)+len(b))
	// Interleave by id desc (both inputs are already desc).
	i, j := 0, 0
	for (i < len(a) || j < len(b)) && len(out) < limit {
		var pick *errorobs.ErrorEvent
		switch {
		case i >= len(a):
			pick = &b[j]
			j++
		case j >= len(b):
			pick = &a[i]
			i++
		case a[i].ID >= b[j].ID:
			pick = &a[i]
			i++
		default:
			pick = &b[j]
			j++
		}
		if seen[pick.ID] {
			continue
		}
		seen[pick.ID] = true
		out = append(out, *pick)
	}
	return out
}