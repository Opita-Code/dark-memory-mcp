// O1: SessionStart — opens a session for an operator and binds it to
// a project. The active project is set on the Store so subsequent
// operations land in the right tenant.
//
// v2.4.0: SessionStart now consults agent_memory and surfaces a
// ContextRecap in the response so the operator can see what they (or
// their project) already know before starting work. This is the
// first vibe-loop integration point — previously the data plane
// had no consumers, leaving the operator with a blank slate on every
// session. INV-10 keeps memories persistent across session close;
// v2.4.0 makes that persistence visible at session start.
//
// v2.4.1: ContextRecap is filtered by the active agent_id so each
// LLM (or operator identity) sees only its own pinned memories +
// open todos, not the cross-agent noise that v2.4.0 surfaced. The
// agent_id is resolved with priority (caller input >
// projects.default_agent_id > empty).
package orchestration

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/agentmemory"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SessionStartInput is the request to open a session.
//
// ProjectID is required and must already exist (or be "default" — the
// catch-all project seeded on Open). ConstitutionID is optional; if
// set it is recorded on the session for runWatchdog provenance.
//
// AgentID (v2.4.1) is the Mem0 agent_id (LLM identity) for this
// session. Optional; resolution priority is (1) this field, (2)
// projects.default_agent_id, (3) empty string (no filter — v2.4.0
// project-wide behavior).
//
// v2.8.0-alpha B1: ColdStart (skip ContextRecap) + ContextRecapTokens
// (token budget for the recap; default 2000 when DARK_MEMORY_V280=1).
// Both gated by the env flag; when off, the v2.7.x behavior is
// preserved (recap fires unconditionally, no truncation).
type SessionStartInput struct {
	Operator        string `json:"operator"`
	ProjectID       string `json:"project_id"`
	ConstitutionID  string `json:"constitution_id,omitempty"`
	ConstitutionVer string `json:"constitution_ver,omitempty"`
	Notes           string `json:"notes,omitempty"`
	// AgentID (v2.4.1) is the per-session agent_id override. When
	// non-empty, takes priority over projects.default_agent_id for
	// ContextRecap filtering. When empty, falls through to the
	// project-level default. Max 128 chars (matches project_create
	// schema); not validated further.
	AgentID string `json:"agent_id,omitempty"`
	// ColdStart (v2.8.0-alpha B1) — when true, skip ContextRecap
	// entirely. Useful for debug, subagent dispatch, or benchmarking
	// (no prior decisions biasing the LLM). Default false.
	ColdStart bool `json:"cold_start,omitempty"`
	// ContextRecapTokens (v2.8.0-alpha B1) — token budget for the
	// formatted ContextRecap. Default 2000 when V280=1, 0 = no
	// truncation (legacy v2.7.x behavior). Clamped to [0, 8000] at
	// the orchestrator layer. Truncation drops open todos first,
	// then pinned (least recent). When truncation fires,
	// ContextRecap.Truncated=true + TruncatedRows count dropped.
	ContextRecapTokens int `json:"context_recap_tokens,omitempty"`
}

// SessionStartOutput is what SessionStart returns. SessionID is the
// new opaque ID; subsequent operations carry it. ContextRecap is
// the v2.4.0 addition (omitted when empty so callers can detect
// "no accumulated knowledge" vs "fetch failed"). ActiveAgentID
// (v2.4.1) echoes the resolved agent_id so callers know what scope
// the recap used.
type SessionStartOutput struct {
	SessionID      string        `json:"session_id"`
	ProjectID      string        `json:"project_id"`
	StartedAt      time.Time     `json:"started_at"`
	ConstitutionID string        `json:"constitution_id,omitempty"`
	ContextRecap   *ContextRecap `json:"context_recap,omitempty"`
	// ActiveAgentID (v2.4.1) is the resolved agent_id used for
	// ContextRecap filtering. Echoes the priority-resolved value
	// (caller input > projects.default_agent_id > ""). Empty when
	// no agent_id is configured (v2.4.0 project-wide behavior).
	ActiveAgentID string `json:"active_agent_id,omitempty"`
}

// ContextRecap is the v2.4.0 agent_memory-derived context prepended
// to SessionStart's response. It surfaces accumulated knowledge so
// the operator can pick up where the last session left off instead of
// starting blind. All fields are best-effort: an empty list (no
// hits) means the project has no agent_memory rows yet (for the
// resolved agent_id; see v2.4.1 ActiveAgentID).
//
// v2.8.0-alpha B1: Truncated + TruncatedRows + FormattedChars surface
// the truncation result. Truncated=true when some rows were dropped
// to fit the ContextRecapTokens budget; TruncatedRows is the count
// dropped (informational); FormattedChars is the actual char count
// of the formatted recap (budget = FormattedChars / 4 ≈ tokens).
type ContextRecap struct {
	// PinnedMemories is the list of pinned rows from
	// agent_memory_list(scope=project, pinned_only=true, agent_id=...).
	// v2.4.1: filtered by resolved agent_id when set; empty
	// agent_id = project-wide (v2.4.0 behavior).
	PinnedMemories []agentmemory.AgentMemory `json:"pinned_memories,omitempty"`
	// OpenTodos is the list of kind=todo rows that are still open
	// (no closed status in v2.4.0; "open" = not yet archived).
	// v2.4.1: filtered by resolved agent_id when set. Limited to
	// 20 by default.
	OpenTodos []agentmemory.AgentMemory `json:"open_todos,omitempty"`
	// Truncated (v2.8.0-alpha B1) — true when rows were dropped to
	// fit the ContextRecapTokens budget.
	Truncated bool `json:"truncated,omitempty"`
	// TruncatedRows (v2.8.0-alpha B1) — count of rows dropped.
	TruncatedRows int `json:"truncated_rows,omitempty"`
	// FormattedChars (v2.8.0-alpha B1) — actual char count of the
	// formatted recap (4 chars ≈ 1 token).
	FormattedChars int `json:"formatted_chars,omitempty"`
}

// SessionStart opens a session for the given operator + project.
// Returns ErrInvalidArgument if Operator or ProjectID is empty.
// Returns ErrSessionRequired if the project could not be set active
// (defensive — should not happen with a projectID that just passed
// SetActiveProject validation).
func (o *Orchestrator) SessionStart(ctx context.Context, in SessionStartInput) (*SessionStartOutput, error) {
	if strings.TrimSpace(in.Operator) == "" {
		return nil, errMissingField("operator")
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return nil, errMissingField("project_id")
	}

	// Set the active project. SetActiveProject validates the project
	// exists; special-cases "default" for legacy compat.
	if err := o.Store.SetActiveProject(ctx, in.ProjectID); err != nil {
		return nil, fmt.Errorf("session_start: set active project: %w", err)
	}

	// v2.4.1: resolve the active agent_id (caller override >
	// projects.default_agent_id > ""). Best-effort: if GetProject
	// fails the resolver returns "" and the recap falls back to
	// v2.4.0 project-wide scope. See resolveActiveAgentID docstring.
	activeAgentID := o.resolveActiveAgentID(ctx, in.AgentID)

	now := o.now().Format(time.RFC3339Nano)
	sess := &session.Session{
		SessionID:       session.NewSessionID(), // see session/types.go
		Status:          string(session.StatusOpen),
		ConstitutionID:  in.ConstitutionID,
		ConstitutionVer: in.ConstitutionVer,
		Notes:           in.Notes,
		Operator:        in.Operator,
		StartedAt:       now,
		LastHeartbeatAt: now, // pristine heartbeat = started time; sweeps compare against HEARTBEAT_TIMEOUT
	}

	wc := store.WriteContext{
		Actor:          "orchestrator_session_start",
		SessionID:      sess.SessionID,
		WritePath:      "SessionStart",
		ConstitutionID: in.ConstitutionID,
		ConstitutionVer: in.ConstitutionVer,
		ProjectID:      in.ProjectID,
	}
	if _, err := o.Store.SaveSession(ctx, wc, sess); err != nil {
		return nil, fmt.Errorf("session_start: save: %w", err)
	}

	// v2.0.2: bind the new session as the project's active session so
	// subsequent tool calls resolve a non-empty SessionID through the
	// StoreBackedActiveSessionResolver. Best-effort: if the column
	// pointer write fails (e.g. the project was archived
	// concurrently), the call still returned a valid session_id —
	// the resolver just won't find it. The failure is logged at
	// debug-level by the store, not promoted to an error here.
	if err := o.Store.SetActiveSession(ctx, in.ProjectID, sess.SessionID); err != nil {
		// Non-fatal. The session row exists; only the lookup pointer
		// didn't update. Operationally, the operator will see
		// session-resolution misses until they retry session_start.
		// We intentionally don't wrap as a hard error because the
		// SaveSession succeeded and that's what downstream tools
		// also need.
		_ = err
	}

	// v2.1.3 cache-invalidation: flush the resolver cache so the next
	// tool call (within the 5s TTL) sees the new session_id, not the
	// stale empty value the gate's pre-inner buildGateInput cached.
	// See orchestrator.go OnActiveSessionChanged doc for the race
	// timeline. Best-effort: nil-safe, doesn't fail the call.
	if o.OnActiveSessionChanged != nil {
		o.OnActiveSessionChanged(in.ProjectID)
	}

	// Note: SaveSession itself emits a write_audit row (INV-1). No
	// second audit row needed here. The orchestrator-level audit
	// signal is the SaveSession call itself.

	// v2.4.0: surface the v2.1.0 agent_memory data plane on
	// SessionStart. v2.4.1: filter by resolved active_agent_id so
	// each LLM sees only its own pinned + todos. Best-effort: a
	// broken agent_memory store MUST NOT block session creation.
	// Errors are swallowed (no logger interface yet — orchestrator
	// package nop). If both queries return zero hits, ContextRecap
	// is nil (the JSON omits the field so existing callers see no
	// behavioral change).
	recap := o.recapSessionStartMemory(ctx, activeAgentID)

	// v2.8.0-alpha C2: sweep expired active_subagents on session
	// start (precursor to F51 expired-rows sweeper for
	// agent_memory). Best-effort — must NOT block session creation.
	if v280Enabled() {
		if swept, err := o.Store.SweepExpiredSubagents(ctx); err != nil {
			log.Printf("dark-mem-mcp: session_start sweep_active_subagents err=%v", err)
		} else if swept > 0 {
			log.Printf("dark-mem-mcp: session_start swept %d expired subagent binding(s)", swept)
		}
	}

	// v2.8.0-alpha B1: ColdStart + ContextRecapTokens. When
	// DARK_MEMORY_V280=1, apply the token budget + skip-on-cold-start.
	// When the flag is off, the recap fires unconditionally as in
	// v2.7.x.
	if v280Enabled() {
		recap = o.applyContextRecapBudget(ctx, recap, in.ColdStart, in.ContextRecapTokens)
	}

	startedAt, _ := time.Parse(time.RFC3339Nano, now)
	return &SessionStartOutput{
		SessionID:      sess.SessionID,
		ProjectID:      in.ProjectID,
		StartedAt:      startedAt,
		ConstitutionID: in.ConstitutionID,
		ContextRecap:   recap,
		ActiveAgentID:  activeAgentID,
	}, nil
}

// recapSessionStartMemory is the v2.4.0 best-effort agent_memory
// recap helper. v2.4.1: takes agentID and filters listPinnedForVibe +
// listOpenTodosForVibe by it. Returns nil when the project has no
// accumulated memory (the JSON omits the field). Errors are
// swallowed — see SessionStart docstring for the rationale.
func (o *Orchestrator) recapSessionStartMemory(ctx context.Context, agentID string) *ContextRecap {
	// Pinned: operator-curated canonical facts. v2.4.1: scoped by
	// the resolved agent_id. Empty agent_id = project-wide (v2.4.0
	// backward compat).
	pinned, _ := o.listPinnedForVibe(ctx, "", agentID, 10)
	// Open todos: kind=todo rows. v2.4.1: same scoping.
	openTodos, _ := o.listOpenTodosForVibe(ctx, agentID, 20)
	if len(pinned) == 0 && len(openTodos) == 0 {
		return nil
	}
	return &ContextRecap{
		PinnedMemories: pinned,
		OpenTodos:      openTodos,
	}
}

// applyContextRecapBudget is the v2.8.0-alpha B1 helper. Given a
// freshly-built recap, returns either the same recap (legacy), a
// nil recap (ColdStart), or a truncated recap (ContextRecapTokens
// exceeded). Truncation policy: drop open todos first, then pinned
// from the least-recent. Truncation counters are surfaced in the
// result.
//
// Token estimate: 4 chars ≈ 1 token (rule of thumb for English text
// in LLM context; rough but adequate for budget enforcement).
func (o *Orchestrator) applyContextRecapBudget(
	_ context.Context,
	recap *ContextRecap,
	coldStart bool,
	tokenBudget int,
) *ContextRecap {
	if coldStart {
		return nil
	}
	if recap == nil {
		return nil
	}

	// Resolve budget. 0 = legacy v2.7.x (no truncation). Otherwise
	// clamp to [0, 8000].
	if tokenBudget <= 0 {
		tokenBudget = 2000 // v2.8.0-alpha default
	}
	if tokenBudget > 8000 {
		tokenBudget = 8000
	}
	charBudget := tokenBudget * 4

	// Format the full recap and measure chars.
	formattedPinned := formatPinnedForRecap(recap.PinnedMemories)
	formattedTodos := formatTodosForRecap(recap.OpenTodos)
	totalChars := len(formattedPinned) + len(formattedTodos)

	if totalChars <= charBudget {
		// Fits — just record the size.
		recap.FormattedChars = totalChars
		return recap
	}

	// Over budget — drop open todos first (less critical than
	// pinned decisions). Then drop pinned from the tail.
	remaining := charBudget
	keptTodos := recap.OpenTodos
	if len(formattedTodos) > remaining {
		// Drop all todos (each is too big). Sub-budget = remaining
		// already covers the open-todos full; since > remaining,
		// we drop them all.
		keptTodos = nil
		// Now use the full budget for pinned.
		remaining = charBudget
	} else {
		remaining -= len(formattedTodos)
	}

	keptPinned := recap.PinnedMemories
	if len(formattedPinned) > remaining {
		// Drop from the tail (least recent — listPinnedForVibe
		// returns sorted by created_at DESC, so tail = oldest).
		keptPinned, _ = truncatePinnedList(recap.PinnedMemories, remaining)
	}

	dropped := (len(recap.PinnedMemories) - len(keptPinned)) +
		(len(recap.OpenTodos) - len(keptTodos))

	formattedKeptPinned := formatPinnedForRecap(keptPinned)
	formattedKeptTodos := formatTodosForRecap(keptTodos)
	recap.PinnedMemories = keptPinned
	recap.OpenTodos = keptTodos
	recap.Truncated = true
	recap.TruncatedRows = dropped
	recap.FormattedChars = len(formattedKeptPinned) + len(formattedKeptTodos)
	return recap
}

// formatPinnedForRecap renders pinned memories as a markdown block
// for token estimation + future injection. The format mirrors what
// the harness would inject into the system prompt.
func formatPinnedForRecap(rows []agentmemory.AgentMemory) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Pinned memories\n")
	for i, r := range rows {
		fmt.Fprintf(&sb, "%d. [%s] %s\n   %s\n",
			i+1, r.Kind, titleOrEmpty(r.Title), truncate(r.Content, 200))
	}
	return sb.String()
}

// formatTodosForRecap renders open todos as a markdown block.
func formatTodosForRecap(rows []agentmemory.AgentMemory) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Open todos\n")
	for i, r := range rows {
		fmt.Fprintf(&sb, "%d. [%s] %s\n",
			i+1, titleOrEmpty(r.Title), truncate(r.Content, 200))
	}
	return sb.String()
}

// titleOrEmpty returns the title if non-empty, else "(untitled)".
func titleOrEmpty(t string) string {
	if strings.TrimSpace(t) == "" {
		return "(untitled)"
	}
	return t
}

// truncate caps s at max chars with "..." suffix when truncated.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// truncatePinnedList returns the largest prefix of pinned whose
// rendered chars fit in charBudget. listPinnedForVibe returns rows
// sorted by created_at DESC; tail = oldest, which we drop first.
func truncatePinnedList(pinned []agentmemory.AgentMemory, charBudget int) ([]agentmemory.AgentMemory, int) {
	if charBudget <= 0 {
		return nil, len(pinned)
	}
	// Try increasingly smaller prefixes.
	for n := len(pinned); n > 0; n-- {
		rendered := formatPinnedForRecap(pinned[:n])
		if len(rendered) <= charBudget {
			dropped := len(pinned) - n
			return pinned[:n], dropped
		}
	}
	return nil, len(pinned)
}