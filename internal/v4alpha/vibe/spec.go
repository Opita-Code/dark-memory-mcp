// Package vibe is the v4-alpha runtime for the vibe-loop workflow
// (M1 movement: workflow = runtime mutable state). It implements the
// minimum subset of the production vibe_spec / vibe_publish / drift_log
// / pipeline_status / resolve_drift surface in pure Go with SQLite
// persistence, so the orchestration engine can host itself.
package vibe

import (
	"errors"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

// Canonical VibeCase values are sourced from the vibecase package
// (single source of truth per taxonomy.go §0). This file does NOT
// re-declare the constants — adding C8 means editing vibecase/taxonomy.go
// only. See Spec.Validate's IsValidVibeCase for the runtime check.

// IsValidVibeCase reports whether c is a canonical vibe_case identifier
// (per the vibecase package — C1..Cn where n grows with new cases).
// Used by Validate and by downstream consumers that need to dispatch
// on case without a default branch.
func IsValidVibeCase(c string) bool {
	return vibecase.IsValid(c)
}

// Task is one node in the spec's task graph.
//
// Invariants (enforced by Spec.Validate):
//   - ID is non-empty and unique within the spec
//   - Description is non-empty
//   - DependsOn entries must reference other task IDs in the same spec
//   - A task must not depend on itself
//   - The full dependency graph must be acyclic
type Task struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

// Spec is a structured intent with a validated task graph. In the
// v4-alpha runtime, a Spec is the input to vibe_publish — it tells
// the orchestrator what the agent is trying to produce.
type Spec struct {
	ID        int64
	VibeCase  string
	Intent    string
	Tasks     []Task
	CreatedAt time.Time
}

// Validate enforces the v4-alpha vibe-loop invariants for a Spec.
// Returns nil on a fully valid spec, or one of the package-level
// Err* sentinels wrapped with task index/id context on failure.
//
// Invariant 1: VibeCase must be canonical (C1..C7).
// Invariant 2: Intent must be non-empty.
// Invariant 3: At least one task is required.
// Invariant 4: All task IDs are unique and non-empty; all descriptions
//              are non-empty.
// Invariant 5: All depends_on entries reference existing task IDs.
// Invariant 6: No task depends on itself.
// Invariant 7: The dependency graph is acyclic (DFS coloring check).
func (s *Spec) Validate() error {
	if !IsValidVibeCase(s.VibeCase) {
		return fmt.Errorf("%w: got %q", ErrUnknownVibeCase, s.VibeCase)
	}
	if s.Intent == "" {
		return ErrEmptyIntent
	}
	if len(s.Tasks) == 0 {
		return ErrEmptyTasks
	}

	seen := make(map[string]struct{}, len(s.Tasks))
	taskIdx := make(map[string]int, len(s.Tasks))
	for i, t := range s.Tasks {
		if t.ID == "" {
			return fmt.Errorf("%w at index %d", ErrEmptyTaskID, i)
		}
		if t.Description == "" {
			return fmt.Errorf("%w for task %q", ErrEmptyTaskDesc, t.ID)
		}
		if _, dup := seen[t.ID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateTaskID, t.ID)
		}
		seen[t.ID] = struct{}{}
		taskIdx[t.ID] = i
	}

	// Invariants 5 (dangling) and 6 (self-dep).
	for _, t := range s.Tasks {
		for _, dep := range t.DependsOn {
			if dep == t.ID {
				return fmt.Errorf("%w: task %q", ErrSelfDep, t.ID)
			}
			if _, ok := taskIdx[dep]; !ok {
				return fmt.Errorf("%w: task %q depends on %q",
					ErrDanglingDep, t.ID, dep)
			}
		}
	}

	// Invariant 7: cycle detection via DFS coloring.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(s.Tasks))

	// Index tasks by ID for O(1) lookup of depends_on children.
	depsOf := make(map[string][]string, len(s.Tasks))
	for _, t := range s.Tasks {
		depsOf[t.ID] = t.DependsOn
	}

	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			return fmt.Errorf("%w through task %q", ErrTaskCycle, id)
		case black:
			return nil
		}
		color[id] = gray
		for _, dep := range depsOf[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = black
		return nil
	}
	for _, t := range s.Tasks {
		if err := visit(t.ID); err != nil {
			return err
		}
	}

	return nil
}

// Sentinel errors returned by Spec.Validate. Callers use errors.Is to
// branch on failure mode (e.g., to suggest a fix for dangling deps
// vs. cycles).
var (
	ErrUnknownVibeCase  = errors.New("vibe: unknown vibe_case")
	ErrEmptyIntent      = errors.New("vibe: intent is required")
	ErrEmptyTasks       = errors.New("vibe: at least one task is required")
	ErrEmptyTaskID      = errors.New("vibe: empty task id")
	ErrEmptyTaskDesc    = errors.New("vibe: empty task description")
	ErrDuplicateTaskID  = errors.New("vibe: duplicate task id")
	ErrDanglingDep      = errors.New("vibe: dangling depends_on reference")
	ErrSelfDep          = errors.New("vibe: self-dependence in task")
	ErrTaskCycle        = errors.New("vibe: cyclic task dependency graph")
)
