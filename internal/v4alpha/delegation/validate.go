// Package delegation — validate extracted subtasks (Phase 7 alpha.19).
//
// Per SPEC §3.1 §3.1 P4 constraints:
//   - cap MaxSubtasks (truncate + audit kind=observation)
//   - reject subtasks with description < MinSubtaskLength (drop + audit)
//   - reject dependency cycles (return topo_cycle failure mode)
//   - normalize IDs (sequential subtask-1, subtask-2, ...)
//
// All functions are pure — no LLM, no DB. Validation happens AFTER
// EXTRACT returns and BEFORE MIND/CURATE.
package delegation

import (
	"fmt"
	"strings"
)

// ValidationWarning is one non-fatal issue caught by validate.
// Triggers and subtidings: drop subtask (<MinSubtaskLength), truncate
// (>MaxSubtasks), missing-ID. Returned by ValidateSubtasks for the
// caller to log via audit_log kind=observation.
type ValidationWarning struct {
	// ID is the canonical identifier ("drop_short_subtask",
	// "truncate_overflow", "rename_missing_id").
	ID string

	// Index is the position in the original LLM output (0-based).
	// -1 for global issues (e.g., cycle spanning all).
	Index int

	// Detail is the human-readable reason.
	Detail string
}

// ValidateResult is what ValidateSubtasks returns.
type ValidateResult struct {
	// Subtasks is the cleaned list (capped + filtered + IDs normalized).
	Subtasks []Subtask

	// Warnings is the list of non-fatal issues. Caller should write
	// audit_log rows with kind=observation for each.
	Warnings []ValidationWarning

	// Err is the FATAL error. nil when valid (warnings may still exist).
	// Currently set only for dependency cycles.
	Err error
}

// ValidateSubtasks applies the SPEC §3.1 P4 constraints. Returns a
// ValidateResult with cleaned subtasks + warnings + fatal error.
//
// Pipeline:
//  1. Filter subtasks with description < MinSubtaskLength (drop).
//  2. Truncate to first MaxSubtasks (overflow dropped).
//  3. Normalize IDs to subtask-1, subtask-2, ... (in order).
//  4. Topological sort dependencies (returns ErrCycle on cycle).
//  5. Re-assign Dependencies to point to new IDs.
func ValidateSubtasks(raw []Subtask) ValidateResult {
	out := ValidateResult{}

	// 1. Drop too-short subtasks.
	filtered := make([]Subtask, 0, len(raw))
	for i, st := range raw {
		desc := strings.TrimSpace(st.Description)
		if len(desc) < MinSubtaskLength {
			out.Warnings = append(out.Warnings, ValidationWarning{
				ID:     "drop_short_subtask",
				Index:  i,
				Detail: fmt.Sprintf("subtask[%d] description %d chars < %d (dropped)", i, len(desc), MinSubtaskLength),
			})
			continue
		}
		st.Description = desc
		filtered = append(filtered, st)
	}

	// 2. Truncate overflow.
	if len(filtered) > MaxSubtasks {
		out.Warnings = append(out.Warnings, ValidationWarning{
			ID:     "truncate_overflow",
			Index:  -1,
			Detail: fmt.Sprintf("LLM produced %d subtasks > cap %d (last %d dropped)", len(filtered), MaxSubtasks, len(filtered)-MaxSubtasks),
		})
		filtered = filtered[:MaxSubtasks]
	}

	// 3. Normalize IDs (and build old→new ID map for dep rewriting).
	idMap := make(map[string]string, len(filtered))
	for i := range filtered {
		newID := fmt.Sprintf("subtask-%d", i+1)
		oldID := filtered[i].ID
		if oldID == "" {
			out.Warnings = append(out.Warnings, ValidationWarning{
				ID:     "rename_missing_id",
				Index:  i,
				Detail: fmt.Sprintf("subtask[%d] had empty id, renamed to %q", i, newID),
			})
		}
		idMap[oldID] = newID
		if oldID != newID {
			filtered[i].ID = newID
		}
	}

	// 4. Rewrite Dependencies to new IDs + topo sort.
	for i := range filtered {
		newDeps := make([]string, 0, len(filtered[i].Dependencies))
		for _, dep := range filtered[i].Dependencies {
			if newDep, ok := idMap[dep]; ok {
				if newDep != filtered[i].ID { // drop self-deps silently
					newDeps = append(newDeps, newDep)
				}
			}
			// else: dangling dep — silently drop (not in SPEC §3.1 P4 list,
			// but a malicious / hallucinated dep should not poison the result)
		}
		filtered[i].Dependencies = newDeps
	}

	// 5. Detect cycles via DFS coloring (mirrors spec.go's approach).
	if hasCycle(filtered) {
		out.Err = fmt.Errorf("delegation: dependency cycle detected in %d subtasks", len(filtered))
		return out
	}

	out.Subtasks = filtered
	return out
}

// hasCycle runs DFS coloring to detect cycles in the subtask dependency graph.
// Returns true if any cycle exists.
func hasCycle(subtasks []Subtask) bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(subtasks))
	depsOf := make(map[string][]string, len(subtasks))
	for _, s := range subtasks {
		depsOf[s.ID] = s.Dependencies
	}
	var visit func(id string) bool
	visit = func(id string) bool {
		switch color[id] {
		case gray:
			return true // cycle
		case black:
			return false
		}
		color[id] = gray
		for _, dep := range depsOf[id] {
			if visit(dep) {
				return true
			}
		}
		color[id] = black
		return false
	}
	for _, s := range subtasks {
		if visit(s.ID) {
			return true
		}
	}
	return false
}

// TopoSort orders subtasks so each appears AFTER all its dependencies.
// Returns the sorted slice. Caller MUST validate hasCycle=false first.
// Pure — no I/O.
func TopoSort(subtasks []Subtask) []Subtask {
	if len(subtasks) == 0 {
		return nil
	}
	// Kahn's algorithm: indegree + queue.
	indeg := make(map[string]int, len(subtasks))
	childrenOf := make(map[string][]string, len(subtasks))
	ids := make([]string, 0, len(subtasks))
	for _, s := range subtasks {
		ids = append(ids, s.ID)
		indeg[s.ID] = len(s.Dependencies)
		for _, dep := range s.Dependencies {
			childrenOf[dep] = append(childrenOf[dep], s.ID)
		}
	}
	// Queue: zero-indegree nodes, in original order (stable).
	queue := make([]string, 0)
	for _, id := range ids {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	out := make([]Subtask, 0, len(subtasks))
	byID := make(map[string]Subtask, len(subtasks))
	for _, s := range subtasks {
		byID[s.ID] = s
	}
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		out = append(out, byID[head])
		for _, child := range childrenOf[head] {
			indeg[child]--
			if indeg[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	return out
}