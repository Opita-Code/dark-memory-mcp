// Package tools — events.go: dark_memory_event_log +
// dark_memory_event_replay (Phase 12 T-105).
//
// The events table (schema v32, single polymorphic table) is the
// audit trail for the data plane. These 2 tools give operators
// a READ-ONLY view:
//
//   event_log     — filterable list of events (by kind, target_table,
//                  target_row_id, process_id, session_id, actor, since_id).
//                  Returns rows in id ASC order (insertion order — the
//                  canonical LangFuse timeline). Honors INV-7 (project
//                  isolation via filter.ProjectID, defaults to active
//                  project). Limit clamped to [1, 10000].
//
//   event_replay — return the full event tree rooted at a given
//                  event_id. 2 levels of nesting: root + direct
//                  children. Returns the parent + all children (one
//                  level deep) so operators can see the DECIDE→
// EXTRACT→MIND→CURATE→COMPLETED tree of a delegate_intent call
//                  or the started→in_progress→completed lifecycle of
//                  an async drift_judge.
//
// Both tools are read-only by design (no writes, no mutations).
// The WRITE side is internal: the EventWriter + AutoEmitter +
// DriftJudgeProgressEmitter + DelegationProgressEmitter emit
// events; these tools are the OBSERVER side.

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// EventLogInput is the input for dark_memory_event_log.
//
// All filter fields are optional. Empty filter returns all events
// for the active project (INV-7 default). At least one of {kind,
// target_table, target_row_id, process_id, session_id, actor} should
// be set for a useful query — full-table scans on large event
// tables are expensive (use since_id cursor for incremental fetch).
type EventLogInput struct {
	Kind        string `json:"kind,omitempty"`
	TargetTable string `json:"target_table,omitempty"`
	TargetRowID int64  `json:"target_row_id,omitempty"`
	ProcessID   string `json:"process_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Actor       string `json:"actor,omitempty"`
	SinceID     int64  `json:"since_id,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// EventLogResult is the output for dark_memory_event_log.
//
// Events are returned in id ASC order (canonical insertion order).
// Empty Events slice means the filter matched no rows (NOT an error).
// The tool result shape is intentionally flat (no nested metadata)
// to keep wire parsing simple — callers can post-process if needed.
type EventLogResult struct {
	ProjectID string             `json:"project_id"`
	Count     int                `json:"count"`
	Events    []*store.Event      `json:"events"`
}

// RegisterEventsTools registers event_log + event_replay tools into
// the registry. Called by RegisterAllWithDeps (canonical order).
func RegisterEventsTools(reg *Registry, st store.Store) {
	reg.Add(BindStore("event_log",
		"Phase 12 T-105: filterable list of events from the polymorphic events table (single table for modifications + progress, schema v32). Honors INV-7 (active project scoping). Returns rows in id ASC order (LangFuse timeline). Limit clamped to [1, 10000]. Read-only.",
		MustJSONSchema(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":         map[string]any{"type": "string", "enum": []any{"modification", "progress"}, "description": "Filter by kind (modification | progress)."},
				"target_table": map[string]any{"type": "string", "description": "Filter by target_table (e.g. agent_memory, drift_report, delegation_trees)."},
				"target_row_id": map[string]any{"type": "integer", "description": "Filter by target_row_id (the row being modified)."},
				"process_id":    map[string]any{"type": "string", "description": "Filter by process_id (drift-<artifactID> | delegate-<taskID>)."},
				"session_id":    map[string]any{"type": "string", "description": "Filter by session_id (LangFuse trace)."},
				"actor":         map[string]any{"type": "string", "description": "Filter by actor (system | agent_id | operator:<name>)."},
				"since_id":      map[string]any{"type": "integer", "description": "Cursor: return rows with id > since_id (LangFuse flush pattern)."},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 10000, "description": "Max rows to return. Default 1000, clamped to 10000."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in EventLogInput) (*EventLogResult, error) {
			filter := store.ListEventsFilter{
				Kind:        in.Kind,
				TargetTable: in.TargetTable,
				TargetRowID: in.TargetRowID,
				ProcessID:   in.ProcessID,
				SessionID:   in.SessionID,
				Actor:       in.Actor,
				SinceID:     in.SinceID,
				Limit:       in.Limit,
			}
			events, err := s.ListEvents(ctx, filter)
			if err != nil {
				return nil, err
			}
			// Resolve project_id for observability. If filter.ProjectID
			// is empty, the active project was enforced — surface that
			// in the result so callers see what scope they got.
			pid := filter.ProjectID
			if pid == "" {
				pid = s.ActiveProject()
			}
			return &EventLogResult{
				ProjectID: pid,
				Count:     len(events),
				Events:    events,
			}, nil
		}))

	reg.Add(BindStore("event_replay",
		"Phase 12 T-105: return the event tree rooted at event_id (1-level children). Useful for inspecting a delegate_intent tree (DECIDE→EXTRACT→MIND→CURATE→COMPLETED) or an async drift_judge lifecycle (started→in_progress→completed). Returns root + direct children. Read-only.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"event_id"},
			"properties": map[string]any{
				"event_id": map[string]any{"type": "integer", "description": "Root event id (the id of the root event itself, not its parent)."},
				"include_children": map[string]any{"type": "boolean", "description": "Default true. When false, return only the root event."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in EventReplayInput) (*EventReplayResult, error) {
			if in.EventID <= 0 {
				return nil, fmt.Errorf("%w: event_id must be positive (got %d)", store.ErrInvalidArgument, in.EventID)
			}
			root, err := s.GetEventByID(ctx, in.EventID)
			if err != nil {
				// ErrNotFound = the event_id is not in this project
				// (INV-7 scoping) or doesn't exist. Return an empty
				// result instead of propagating the error — the
				// caller can check Root==nil to detect "not found".
				if errors.Is(err, store.ErrNotFound) {
					return &EventReplayResult{
						Root:       nil,
						Children:   nil,
						ChildCount: 0,
					}, nil
				}
				return nil, err
			}
			if root == nil {
				return &EventReplayResult{
					Root:       nil,
					Children:   nil,
					ChildCount: 0,
				}, nil
			}
			out := &EventReplayResult{
				Root:      root,
				ChildCount: 0,
			}
			// Phase 12 T-103c: by convention, the tree is rooted via
			// root_event_id (Step Functions exec-history). For the
			// root event itself, root_event_id == its own id (set by
			// EmitDecide which captures the id at write time).
			// ListEventsByRootEventID includes the root event itself
			// (per the data-layer contract).
			includeChildren := in.IncludeChildren == nil || *in.IncludeChildren
			if includeChildren {
				// Prefer ListEventsByRootEventID for the full tree;
				// fall back to ListEventsByParentEventID when the root
				// is itself (1-level view only).
				if root.RootEventID.Valid && root.RootEventID.Int64 > 0 {
					tree, err := s.ListEventsByRootEventID(ctx, root.RootEventID.Int64)
					if err != nil {
						return nil, err
					}
					out.Children = tree
					out.ChildCount = len(tree)
				} else {
					// Root has no root_event_id set; 1-level children
					// via parent_event_id pointing at the root.
					children, err := s.ListEventsByParentEventID(ctx, root.ID)
					if err != nil {
						return nil, err
					}
					out.Children = children
					out.ChildCount = len(children)
				}
			}
			return out, nil
		}))
}

// EventReplayInput is the input for dark_memory_event_replay.
type EventReplayInput struct {
	EventID         int64 `json:"event_id"`
	IncludeChildren *bool `json:"include_children,omitempty"`
}

// EventReplayResult is the output for dark_memory_event_replay.
//
// Children are returned in id ASC order (insertion order). Root is
// the event matching event_id (may be nil when not found — caller
// should check Root == nil before reading Children).
type EventReplayResult struct {
	Root       *store.Event `json:"root"`
	Children   []*store.Event `json:"children"`
	ChildCount int            `json:"child_count"`
}

// summarizeKind is a small helper for logging / debug output.
func summarizeKind(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		return s[:i]
	}
	return s
}

// _ ensures summarizeKind stays linked even when unused (callers
// may use it for debug output without forcing a build error).
var _ = summarizeKind