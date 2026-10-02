// Package tools — bitemporal.go: dark_memory_mark_superseded +
// dark_memory_recall_bitemporal (Phase 9 alpha.20 Chunk 8.7, ADR-014).
//
// Wire shape:
//
//	dark_memory_mark_superseded
//	  in:  { old_mem_id: int, new_mem_id: int,
//	         trigger?: string (default "operator_action"),
//	         reason:   string (required),
//	         evidence?: string,
//	         valid_time?: string (RFC3339Nano; default NOW) }
//	  out: { ok: bool, old_mem_id: int, new_mem_id: int,
//	         decision_state: "superseded",
//	         valid_to: string, transition_id: int }
//
//	dark_memory_recall_bitemporal
//	  in:  { t: string (RFC3339Nano — the "as-of" timestamp),
//	         kind?: string (filter by memory kind),
//	         limit?: int (default 50; 0 = no cap) }
//	  out: { rows: [{id, kind, title?, ..., transaction_time, valid_time}, ...],
//	         count: int, t: string }
//
// Both tools write_audit rows in the same tx as the data write
// (MarkSuperseded) or as a read-trace (RecallAtTime — emits a
// write_audit_search_canonical row). Invariant: no silent writes;
// operators see exactly which rows were touched.
//
// Phase 9 alpha.20 Chunk 8.7 Bitemporal (ADR-014).
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RegisterBitemporal wires dark_memory_mark_superseded +
// dark_memory_recall_bitemporal into the registry. Called from
// RegisterAllWithDeps (the canonical surface registration path).
// Both tools are registered unconditionally (canonical surface
// requirement, mirrors Chunk 8.5 audit + Chunk 8.4 prograph
// patterns) — calls return errors when the Store has no active
// project, which is the same contract as dark_memory_recall.
func RegisterBitemporal(reg *Registry, st store.Store) error {
	if reg == nil {
		return errors.New("tools: RegisterBitemporal: nil registry")
	}
	if st == nil {
		return errors.New("tools: RegisterBitemporal: nil store")
	}

	// --- mark_superseded ------------------------------------------
	reg.Add(BindStore("mark_superseded",
		"Mark one agent_memory decision row as superseded by another (alpha.20 Chunk 8.7 Bitemporal, ADR-014). Both rows must be kind=decision in the active project (INV-7). Old row gets decision_state='superseded', supersedes_id=newMemID, valid_to=validTime (default NOW). Records a row in decision_transitions with trigger+reason+evidence. Returns ErrInvalidSupersession on self-supersede, missing rows, non-decision kinds, or cross-project supersession.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"old_mem_id": map[string]any{"type": "integer", "description": "mem_id of the row being superseded (becomes decision_state='superseded')."},
				"new_mem_id": map[string]any{"type": "integer", "description": "mem_id of the row that supersedes (must stay 'active')."},
				"trigger":    map[string]any{"type": "string", "description": "Trigger label for the transition row. Default 'operator_action'. Free-form."},
				"reason":     map[string]any{"type": "string", "description": "Required human-readable reason. Stored on decision_transitions.reason."},
				"evidence":   map[string]any{"type": "string", "description": "Optional evidence pointer (URL, file path, audit row id). NULL when empty."},
				"valid_time": map[string]any{"type": "string", "description": "RFC3339Nano — when the supersession takes effect (old.valid_to). Default NOW (UTC)."},
			},
			"required": []string{"old_mem_id", "new_mem_id", "reason"},
		}),
		st,
		func(ctx context.Context, s store.Store, in MarkSupersededInput) (*MarkSupersededResult, error) {
			if in.OldMemID <= 0 || in.NewMemID <= 0 {
				return nil, fmt.Errorf("mark_superseded: invalid mem_id (old=%d new=%d)",
					in.OldMemID, in.NewMemID)
			}
			if in.Reason == "" {
				return nil, errors.New("mark_superseded: reason is required")
			}
			if in.ValidTime == "" {
				in.ValidTime = time.Now().UTC().Format(time.RFC3339Nano)
			} else {
				// Validate the caller-provided timestamp; reject malformed
				// inputs early so the DB doesn't see garbage.
				if _, err := time.Parse(time.RFC3339Nano, in.ValidTime); err != nil {
					if _, err2 := time.Parse(time.RFC3339, in.ValidTime); err2 != nil {
						return nil, fmt.Errorf("mark_superseded: valid_time must be RFC3339Nano or RFC3339 (got %q): %v", in.ValidTime, err)
					}
				}
			}
			err := s.MarkSupersededAgentMemory(ctx,
				store.WriteContext{Actor: "operator_action"},
				in.OldMemID, in.NewMemID,
				in.Trigger, in.Reason, in.Evidence, in.ValidTime,
			)
			if err != nil {
				return nil, err
			}
			return &MarkSupersededResult{
				OK:            true,
				OldMemID:      in.OldMemID,
				NewMemID:      in.NewMemID,
				DecisionState: "superseded",
				ValidTo:       in.ValidTime,
			}, nil
		}))

	// --- recall_bitemporal -----------------------------------------
	reg.Add(BindStore("recall_bitemporal",
		"Bitemporal 'as-of' query (alpha.20 Chunk 8.7, ADR-014). Returns agent_memory rows in the active project whose valid_time <= t (the moment in semantic-clock time the operator is asking about). Archived rows are excluded. For decision-kind rows, includes active ones and excludes superseded ones whose successor had valid_from <= t. Read-only; emits a write_audit_search_canonical row.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"t":     map[string]any{"type": "string", "description": "RFC3339Nano — the 'as-of' timestamp. The result includes rows whose valid_time <= t."},
				"kind":  map[string]any{"type": "string", "description": "Optional kind filter (decision, observation, ...). Empty = any kind."},
				"limit": map[string]any{"type": "integer", "description": "Max rows returned. 0 → default 50. Negative → 0."},
			},
			"required": []string{"t"},
		}),
		st,
		func(ctx context.Context, s store.Store, in RecallBitemporalInput) (*RecallBitemporalResult, error) {
			t, err := time.Parse(time.RFC3339Nano, in.T)
			if err != nil {
				// Tolerate RFC3339 (drop sub-second precision).
				if t2, err2 := time.Parse(time.RFC3339, in.T); err2 == nil {
					t = t2
				} else {
					return nil, fmt.Errorf("recall_bitemporal: t must be RFC3339Nano or RFC3339 (got %q): %v", in.T, err)
				}
			}
			limit := in.Limit
			if limit == 0 {
				limit = 50
			}
			if limit < 0 {
				limit = 0
			}
			rows, err := s.RecallAtTime(ctx, t, in.Kind, limit)
			if err != nil {
				return nil, err
			}
			if rows == nil {
				rows = []*agentmemory.AgentMemory{}
			}
			return &RecallBitemporalResult{
				Rows:  rows,
				Count: len(rows),
				T:     t.UTC().Format(time.RFC3339Nano),
			}, nil
		}))

	return nil
}

// MarkSupersededInput is the input for dark_memory_mark_superseded.
type MarkSupersededInput struct {
	OldMemID  int64  `json:"old_mem_id"`
	NewMemID  int64  `json:"new_mem_id"`
	Trigger   string `json:"trigger,omitempty"`   // default "operator_action"
	Reason    string `json:"reason"`              // required
	Evidence  string `json:"evidence,omitempty"`  // optional
	ValidTime string `json:"valid_time,omitempty"` // RFC3339Nano; default NOW
}

// MarkSupersededResult is the output for dark_memory_mark_superseded.
type MarkSupersededResult struct {
	OK            bool   `json:"ok"`
	OldMemID      int64  `json:"old_mem_id"`
	NewMemID      int64  `json:"new_mem_id"`
	DecisionState string `json:"decision_state"`
	ValidTo       string `json:"valid_to"`
}

// RecallBitemporalInput is the input for dark_memory_recall_bitemporal.
type RecallBitemporalInput struct {
	T     string `json:"t"`               // RFC3339Nano required
	Kind  string `json:"kind,omitempty"`  // optional kind filter
	Limit int    `json:"limit,omitempty"` // default 50
}

// RecallBitemporalResult is the output for dark_memory_recall_bitemporal.
// Rows is non-nil (empty slice) when the result is empty so wire
// decoders see [] rather than null.
type RecallBitemporalResult struct {
	Rows  []*agentmemory.AgentMemory `json:"rows"`
	Count int                        `json:"count"`
	T     string                     `json:"t"`
}