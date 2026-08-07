// O12: VibeSpec — spec_create wrapper with structured tasks
// validation. The orchestrator validates the tasks array (unique
// ids, consistent depends_on references) before persisting, so a
// malformed spec fails at creation time rather than at drift_check
// time.
//
// Tasks validation rules:
//   - Every task has a non-empty id.
//   - ids are unique within the spec.
//   - depends_on references either a task id in the same spec, OR
//     a known external ref (prefix "ext:" — for cross-spec deps
//     recorded for audit but not enforced here).
//   - No circular dependencies among in-spec depends_on.
//
// On success, VibeSpec persists the spec via Store.SaveSpec and
// returns the new spec_id. The caller (the MCP server) then logs
// the spec as an artifact for drift-tracking.
//
// INV-7 (per-project): the spec is persisted with the active
// project_id tagged by the Store layer.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/errorobs"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// VibeSpecTask is one work unit in the spec.
type VibeSpecTask struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on,omitempty"` // task ids in same spec OR "ext:..." refs
	Owner       string   `json:"owner,omitempty"`
}

// VibeSpecInput is the request to create a new spec with validated
// tasks. The caller supplies the spec body (intent + constitution +
// raw spec string) plus the parsed tasks array.
//
// F36 (v1.2.1, see CHANGELOG): Tasks is now json.RawMessage rather
// than []VibeSpecTask because the gemela tool `dark_research_spec_create`
// declares `tasks` as `type: "string"` (it stores the value as opaque
// text) while `dark_memory_vibe_spec` declares `tasks` as `type: "array"`.
// Some MCP harnesses serialise arrays as JSON-encoded strings under
// either schema; previously this caused `json.UnmarshalTypeError: cannot
// unmarshal string into Go struct field VibeSpecInput.tasks of type
// []orchestration.VibeSpecTask` to surface as a generic ErrInvalidArgument
// (the field path detail was preserved by typeMismatchToolError but the
// operator-visible error message had no specific field hint). Accepting
// both forms here closes the compatibility loop.
//
// v2.8.0-alpha A4: AutoSaveTodos (default true when DARK_MEMORY_V280=1)
// triggers one kind=todo agent_memory row per task. The rows are
// auto-archived by publish_vibe when verdict=aligned. Set false to
// suppress per-call.
type VibeSpecInput struct {
	VibeCase     string          `json:"vibe_case"`
	Constitution string          `json:"constitution,omitempty"`
	Spec         string          `json:"spec,omitempty"` // opaque intent JSON
	Tasks        json.RawMessage `json:"tasks"`          // F36: accept array OR JSON-string-encoded array
	SessionID    string          `json:"session_id,omitempty"`
	// AutoSaveTodos (v2.8.0-alpha A4) — when true (default when
	// DARK_MEMORY_V280=1), each task auto-creates a kind=todo
	// agent_memory row tagged with spec:<id>,task:<id>. Set false
	// to suppress per-call (the auto-save might add noise for trivial
	// specs with many small tasks).
	AutoSaveTodos bool `json:"auto_save_todos,omitempty"`
}

// parseTasksField accepts both forms of the `tasks` input:
//
//   - JSON array of objects: `[{...}, {...}]` (preferred, "Form A")
//   - JSON-encoded string of an array: `"[{...}, {...}]"` (legacy
//     `dark_research_spec_create` style; some MCP harnesses stringify
//     arrays under either schema, "Form B")
//
// Any other shape returns a wrapped store.ErrInvalidArgument carrying:
//
//  1. The underlying json.Unmarshal error message (preserves offset +
//     offending byte info) so the operator can pinpoint the malformed
//     payload without grepping server logs.
//  2. Which Form (A/B) we attempted before bailing — closes the
//     diagnostic gap that PRODUCTION_CHECKLIST.md R-3/R-4 flag as
//     "file a bug with the actual JSON payload that fails"; the error
//     now surfaces the failure mode on its own.
//  3. The FieldError pointing at `tasks` so F35 wire-propagation
//     (errors.As → ToolError.Field) keeps working unchanged.
//
// INFRA-002 (2026-07-19): pre-fix parseTasksField discarded the
// underlying json.Unmarshal error via `store.NewFieldError(...)` with no
// cause attached, surfacing only `"invalid argument at field=tasks"` to
// the harness. The harness (LLM or operator) had no way to tell
// whether they had picked the wrong form, shipped a trailing garbage
// byte, or hit a real schema violation — every failure mode looked
// identical. The fix preserves the cause so the operator can act.
func parseTasksField(raw json.RawMessage) ([]VibeSpecTask, error) {
	if len(raw) == 0 {
		return nil, errMissingField("tasks")
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, errMissingField("tasks")
	}

	// Peek at the first non-whitespace byte to decide which path.
	switch trimmed[0] {
	case '[':
		// Form A: JSON array of objects.
		var out []VibeSpecTask
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, wrapTasksParseErr("Form A (JSON array of objects)", err)
		}
		return out, nil
	case '"':
		// Form B: JSON-encoded string of an array.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, wrapTasksParseErr("Form B step 1 (outer JSON string)", err)
		}
		if strings.TrimSpace(s) == "" {
			return nil, errMissingField("tasks")
		}
		var out []VibeSpecTask
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, wrapTasksParseErr("Form B step 2 (inner JSON array parsed from string)", err)
		}
		return out, nil
	default:
		// First non-whitespace byte is neither '[' nor '"' — neither
		// form applies. Surface what we saw (one byte is safe to
		// disclose; deliberately do not leak the rest of the payload,
		// see classifyUnknown in internal/tools/errors.go).
		return nil, wrapTasksParseErr(
			fmt.Sprintf("unknown form (first non-whitespace byte=%q; expected '[' for Form A or '\"' for Form B)", string(trimmed[0])),
			nil,
		)
	}
}

// wrapTasksParseErr attaches a *store.FieldError pointing at field
// "tasks" so F35 wire-propagation surfaces it as ToolError.Field, then
// wraps THAT with a descriptive prefix + the underlying json.Unmarshal
// cause (if any) for F36-bis diagnostic depth. The returned error
// chain satisfies both `errors.Is(err, store.ErrInvalidArgument)` and
// `errors.As(err, &store.FieldError{Field:"tasks"})`.
//
// When cause is nil (the default-form rejection where we never reached
// json.Unmarshal), only the descriptive prefix is appended.
func wrapTasksParseErr(formDesc string, cause error) error {
	fe := store.NewFieldError(store.ErrInvalidArgument, "tasks")
	if cause == nil {
		return fmt.Errorf("%w: rejected by parser (%s)", fe, formDesc)
	}
	return fmt.Errorf("%w: rejected by parser (%s): %v", fe, formDesc, cause)
}

// VibeSpecResult is the outcome.
type VibeSpecResult struct {
	SpecID         int64    `json:"spec_id"`
	TasksValidated int      `json:"tasks_validated"`
	TaskIDs        []string `json:"task_ids"`           // canonical order
	TasksJSON      string   `json:"tasks_json"`         // serialised for storage
	Warnings       []string `json:"warnings,omitempty"` // non-fatal: orphan ext refs, etc.
	// AutoSavedTodoIDs (v2.8.0-alpha A4) — one row id per task,
	// parallel to TaskIDs. Empty when AutoSaveTodos=false, V280
	// flag off, or any save failed.
	AutoSavedTodoIDs []int64 `json:"auto_saved_todo_ids,omitempty"`
}

// VibeSpec persists a spec with task validation. See package doc.
func (o *Orchestrator) VibeSpec(ctx context.Context, in VibeSpecInput) (*VibeSpecResult, error) {
	// 1. Validate top-level fields.
	if strings.TrimSpace(in.VibeCase) == "" {
		return nil, errMissingField("vibe_case")
	}
	// 1a. v1.4.1: vibe_case must be a canonical C1..C7 identifier.
	//     This closes the asymmetry where vibe_publish enforced the
	//     enum (JSON Schema layer) but vibe_spec did not. Both
	//     orchestrators now derive from the same source of truth
	//     (internal/vibecase). Defense in depth: even if the JSON
	//     Schema enum is bypassed (direct orchestrator call, future
	//     HTTP transport, etc.), the validator rejects unknown cases
	//     before the row is persisted.
	if _, err := vibecase.Parse(in.VibeCase); err != nil {
		return nil, store.NewFieldError(store.ErrInvalidArgument, "vibe_case: "+err.Error())
	}

	// 1a. F36: parse `tasks` accepting both forms (array OR stringified array).
	tasks, err := parseTasksField(in.Tasks)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, errMissingField("tasks")
	}

	// 2. Validate tasks: unique ids + non-empty description.
	seen := make(map[string]bool, len(tasks))
	canonicalIDs := make([]string, 0, len(tasks))
	for i, t := range tasks {
		if strings.TrimSpace(t.ID) == "" {
			return nil, fmt.Errorf("%w: task[%d].id is required", store.ErrInvalidArgument, i)
		}
		if strings.TrimSpace(t.Description) == "" {
			return nil, fmt.Errorf("%w: task[%d].description is required (id=%q)", store.ErrInvalidArgument, i, t.ID)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("%w: duplicate task id %q", store.ErrInvalidArgument, t.ID)
		}
		seen[t.ID] = true
		canonicalIDs = append(canonicalIDs, t.ID)
	}

	// 3. Validate depends_on: must reference an in-spec task id OR
	// be an "ext:..." external ref. Collect warnings for ext refs
	// whose target isn't otherwise known (we don't enforce; we just
	// flag for the operator).
	var warnings []string
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if strings.HasPrefix(dep, "ext:") {
				// External ref. Log a warning if the target
				// isn't a known convention (heuristic: warn if
				// it doesn't look like an artifact_id, spec_id,
				// or task_ref).
				if !looksLikeExternalRef(dep) {
					warnings = append(warnings, fmt.Sprintf("task %q depends_on %q which doesn't look like a standard external ref", t.ID, dep))
				}
				continue
			}
			if !seen[dep] {
				return nil, fmt.Errorf("%w: task %q depends_on %q which is not in this spec", store.ErrInvalidArgument, t.ID, dep)
			}
		}
	}

	// 4. Cycle detection: BFS from each task; if we revisit a
	// task in the same walk, there's a cycle.
	if cycle := detectCycle(tasks); cycle != nil {
		return nil, fmt.Errorf("%w: cycle detected: %s", store.ErrInvalidArgument, strings.Join(cycle, " -> "))
	}

	// 5. Serialise tasks to JSON for storage.
	tasksJSON, err := json.Marshal(tasks)
	if err != nil {
		return nil, fmt.Errorf("vibe_spec: marshal tasks: %w", err)
	}

	// 6. Persist the spec. The Store layer attaches the active
	// project_id and emits write_audit (INV-1, INV-7).
	now := o.now().Format(time.RFC3339Nano)
	spec := &vibeflow.Spec{
		VibeCase:     in.VibeCase,
		Constitution: in.Constitution,
		Spec:         in.Spec,
		Tasks:        string(tasksJSON),
		CreatedAt:    now,
	}
	wc := store.WriteContext{
		Actor:     "orchestrator_vibe_spec",
		SessionID: in.SessionID,
		WritePath: "VibeSpec",
	}
	specID, err := o.Store.SaveSpec(ctx, wc, spec)
	if err != nil {
		return nil, fmt.Errorf("vibe_spec: save spec: %w", err)
	}

	result := &VibeSpecResult{
		SpecID:         specID,
		TasksValidated: len(tasks),
		TaskIDs:        canonicalIDs,
		TasksJSON:      string(tasksJSON),
		Warnings:       warnings,
	}

	// v2.8.0-alpha A4: auto-save kind=todo row per task when
	// DARK_MEMORY_V280=1 AND AutoSaveTodos=true. Best-effort.
	if v280Enabled() && in.AutoSaveTodos {
		todoIDs, todoErr := o.autoSaveTodosForSpec(ctx, specID, tasks)
		if todoErr != nil {
			log.Printf("dark-mem-mcp: vibe_spec auto_save_todos err=%v (spec_id=%d)", todoErr, specID)
			// v2.11.0 (spec 757): was log-only; now durable.
			o.RecordError(ctx, "vibe_spec", "", fmt.Errorf("auto_save_todos spec_id=%d: %w", specID, todoErr), errorobs.SeverityWarn)
		}
		result.AutoSavedTodoIDs = todoIDs
	}

	return result, nil
}

// autoSaveTodosForSpec is the A4 hook. One kind=todo row per task,
// tagged with spec:<id>,task:<id>,status:open. The agent_id is
// resolved via the v2.8.0 priority chain (caller > subagent > project
// default > empty). Best-effort: returns the IDs it did save + a
// non-nil error if any save failed (callers log + continue).
func (o *Orchestrator) autoSaveTodosForSpec(
	ctx context.Context,
	specID int64,
	tasks []VibeSpecTask,
) ([]int64, error) {
	operator := o.activeOperator(ctx)
	if operator == "" {
		operator = "orchestrator_vibe_spec"
	}
	activeAgentID := o.resolveActiveAgentIDWithSubagent(ctx, "", operator)

	ids := make([]int64, 0, len(tasks))
	var firstErr error
	for _, task := range tasks {
		content := fmt.Sprintf("Task: %s\nSpec: %d\nDescription: %s\nStatus: open",
			task.ID, specID, task.Description)
		tags := fmt.Sprintf("spec:%d,task:%s,status:open", specID, task.ID)
		out, err := o.AgentMemorySave(ctx, AgentMemorySaveInput{
			Operator: operator,
			AgentID:  activeAgentID,
			Kind:     agentmemory.KindTodo,
			Title:    task.ID,
			Content:  content,
			Tags:     tags,
		})
		if err != nil {
			log.Printf("dark-mem-mcp: vibe_spec auto_save_todo task=%s err=%v", task.ID, err)
			// v2.11.0 (spec 757): was log-only; now durable.
			o.RecordError(ctx, "vibe_spec", "", fmt.Errorf("auto_save_todo task=%s spec_id=%d: %w", task.ID, specID, err), errorobs.SeverityWarn)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ids = append(ids, out.Row.ID)
	}
	return ids, firstErr
}

// looksLikeExternalRef returns true if dep matches known external
// ref conventions: ext:spec:<id>, ext:artifact:<id>, ext:cve:<id>,
// ext:task:<id>, ext:run:<id>. Anything else gets a warning.
func looksLikeExternalRef(dep string) bool {
	prefixes := []string{"ext:spec:", "ext:artifact:", "ext:cve:", "ext:task:", "ext:run:"}
	for _, p := range prefixes {
		if strings.HasPrefix(dep, p) {
			return true
		}
	}
	return false
}

// detectCycle returns the cycle path if one exists, or nil if the
// task graph is acyclic. Uses DFS with a colour map (white=unseen,
// grey=in-progress, black=done).
func detectCycle(tasks []VibeSpecTask) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := make(map[string]int, len(tasks))
	parent := make(map[string]string, len(tasks))
	adj := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		adj[t.ID] = t.DependsOn
		colour[t.ID] = white
	}

	var cycleStart, cycleEnd string
	var dfs func(u string) bool
	dfs = func(u string) bool {
		colour[u] = grey
		for _, v := range adj[u] {
			if strings.HasPrefix(v, "ext:") {
				continue // external refs aren't part of the in-spec graph
			}
			switch colour[v] {
			case white:
				parent[v] = u
				if dfs(v) {
					return true
				}
			case grey:
				// Found a back-edge. Reconstruct the cycle.
				cycleStart = v
				cycleEnd = u
				return true
			}
		}
		colour[u] = black
		return false
	}

	for _, t := range tasks {
		if colour[t.ID] == white {
			if dfs(t.ID) {
				// Reconstruct path from cycleEnd back to cycleStart.
				path := []string{cycleEnd}
				for cur := cycleEnd; cur != cycleStart; {
					p, ok := parent[cur]
					if !ok {
						break
					}
					path = append(path, p)
					cur = p
				}
				path = append(path, cycleStart)
				// Reverse to get start→...→end→start.
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path
			}
		}
	}
	return nil
}
