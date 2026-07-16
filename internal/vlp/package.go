// Package vlp (continued) — atomic spec 2.2 (VLPPackage).
//
// Provides the four VLP primitives that the harness adapter calls per turn:
//
//   - Brief    — called BEFORE each LLM invocation. Returns the context
//                brief (state + budget + suggested tools) that the harness
//                injects into the system prompt.
//   - Propose  — called BEFORE executing LLM-proposed tool calls. Validates
//                each call against the current state machine (spec 2.1)
//                and returns approved/rejected/redirected sets.
//   - Record   — called AFTER each tool result. Transitions state via the
//                state machine and returns the next action the LLM should take.
//   - Complete — called at end of loop. Requires a terminal state; returns
//                final state, summary metrics, and human-readable handoff.
//
// Atomicity contract:
//   - ONE function set: Brief, Propose, Record, Complete (all typed)
//   - ONE acceptance test: TestVLPPackage_PrimitivesReturnTyped
//   - ONE PR worth of work (~280 LoC)
//   - Direct deps: spec 2.1 (SessionState) only
//   - Independently reviewable: no persistence (2.3), no audit (2.4),
//     no loop driver (2.5), no context injection (Layer 1)
//
// Trust boundary: Package trusts caller-supplied CurrentState. Spec 2.3
// (VLPPersistence) will load state from the store and pass it in; spec
// 2.5 will orchestrate Brief → Propose → Record → Complete in a loop.
package vlp

import "fmt"

// ToolCall is a proposed or recorded MCP tool invocation. Params is the raw
// parameter map; typed deserialization is the MCP layer's job.
type ToolCall struct {
	Name   string                 `json:"name"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// BriefInput is the input to Package.Brief.
type BriefInput struct {
	SessionID    string `json:"session_id"`
	CurrentState State  `json:"current_state"`
	Intent       string `json:"intent,omitempty"` // optional, for logging
}

// BriefOutput is the result of Package.Brief. Server-driven; injected into
// the LLM prompt by the harness adapter. Spec 2.5 will compute these values
// dynamically (context selection, mod resolution, persona lookup); spec 2.2
// returns the static defaults defined here.
type BriefOutput struct {
	State          State    `json:"state"`
	ContextBudget  int      `json:"context_budget"`            // tokens (T1+T2+T3)
	SuggestedTools []string `json:"suggested_tools,omitempty"`  // dark_memory_* names
	ModsActive     []string `json:"mods_active,omitempty"`     // mod IDs running this turn
	PersonaSummary string   `json:"persona_summary,omitempty"` // short; full lookup is Layer 4
}

// ProposeInput is the input to Package.Propose.
type ProposeInput struct {
	SessionID     string    `json:"session_id"`
	CurrentState  State     `json:"current_state"`
	ProposedCalls []ToolCall `json:"proposed_calls"`
}

// Rejection records why a proposed call was not approved.
type Rejection struct {
	Call   ToolCall `json:"call"`
	Reason string   `json:"reason"`
}

// Redirect records a substitution: instead of Original, do Replacement,
// because Reason.
type Redirect struct {
	Original    ToolCall `json:"original"`
	Replacement ToolCall `json:"replacement"`
	Reason      string   `json:"reason"`
}

// ProposeOutput is the result of Package.Propose. Calls partitioned by fate:
// approved (execute), rejected (skip + tell LLM why), redirected (execute
// Replacement instead). Empty Redirected is acceptable in v1.1; full redirect
// logic arrives with the persona/minset layer.
type ProposeOutput struct {
	Approved   []ToolCall  `json:"approved"`
	Rejected   []Rejection `json:"rejected"`
	Redirected []Redirect  `json:"redirected,omitempty"`
}

// RecordInput is the input to Package.Record.
type RecordInput struct {
	SessionID    string                 `json:"session_id"`
	CurrentState State                  `json:"current_state"`
	Call         ToolCall               `json:"call"`
	Result       map[string]interface{} `json:"result,omitempty"`
}

// RecordOutput is the result of Package.Record.
type RecordOutput struct {
	NextState  State  `json:"next_state"`
	NextAction string `json:"next_action"` // see nextActionFor()
}

// CompleteInput is the input to Package.Complete.
type CompleteInput struct {
	SessionID    string         `json:"session_id"`
	CurrentState State          `json:"current_state"`
	Metrics      map[string]int `json:"metrics,omitempty"` // caller-supplied (e.g. drift verdicts count)
}

// CompleteOutput is the result of Package.Complete.
type CompleteOutput struct {
	FinalState State                    `json:"final_state"`
	Summary    map[string]interface{}  `json:"summary"`
	Handoff    string                  `json:"handoff"` // human-readable
}

// Package implements the four VLP primitives. It is stateless in v1.1;
// spec 2.5 may add fields for dependency injection (state source, persona
// store, mod registry, etc.).
type Package struct {
	// Future: state source (2.3), context selector (Layer 1),
	// persona store (4.x), mod registry (5.x).
}

// NewPackage returns a Package ready to serve the four VLP primitives.
func NewPackage() *Package {
	return &Package{}
}

// vlpPrimitives is the interface Package must satisfy. The compile-time
// assertion in TestVLPPackage_PrimitivesReturnTyped (via `var _ vlpPrimitives
// = (*Package)(nil)`) guarantees the contract is honored.
type vlpPrimitives interface {
	Brief(BriefInput) (BriefOutput, error)
	Propose(ProposeInput) (ProposeOutput, error)
	Record(RecordInput) (RecordOutput, error)
	Complete(CompleteInput) (CompleteOutput, error)
}

// Compile-time assertion that *Package satisfies vlpPrimitives.
var _ vlpPrimitives = (*Package)(nil)

// contextBudgetByState is the per-state token budget (T1+T2+T3 sum).
// Static in v1.1; spec 2.5 computes dynamically from active mods + spec
// intent. Values are conservative defaults.
var contextBudgetByState = map[State]int{
	StateIdle:         2000,
	StateDraftingSpec: 3000,
	StateSpecActive:   4000,
	StateDriftJudging: 3500,
	StateComplete:     500,
	StateNeedsHuman:   1500,
	StateAborted:      500,
}

// suggestedToolsByState lists the dark_memory_* tool names appropriate
// for each non-terminal state. Empty for terminal states.
var suggestedToolsByState = map[State][]string{
	StateIdle:         {"dark_memory_session_start"},
	StateDraftingSpec: {"dark_memory_vibe_publish"},
	StateSpecActive:   {"dark_memory_artifact_log"},
	StateDriftJudging: {"dark_memory_drift_log"},
}

// callMapping maps a tool name to the VLP event it represents. Used by
// Propose (to validate) and Record (to advance state). The Verdict payload
// for drift_log is extracted from the call's Params (see eventForCall).
type callMapping struct {
	Event          Event
	ParamForVerdict string // "" = no verdict payload
}

// callMappings is the canonical map of dark_memory_* tool names to VLP
// events. Spec 2.5 may extend this with subagent delegations or mod-driven
// tools, but the core 5 (session_start, vibe_publish, artifact_log,
// drift_log, session_abort) are stable. session_abort is operator-driven
// but exposed as a tool name so harnesses can route it through Propose/Record.
var callMappings = map[string]callMapping{
	"dark_memory_session_start": {Event: EventSessionStart},
	"dark_memory_vibe_publish":  {Event: EventVibePublish},
	"dark_memory_artifact_log":  {Event: EventArtifactLog},
	"dark_memory_drift_log":     {Event: EventDriftLog, ParamForVerdict: "verdict"},
	"dark_memory_session_abort": {Event: EventAbort},
}

// eventForCall converts a ToolCall to (Event, Verdict) for state machine
// evaluation. Returns error if the tool is unknown or if a required verdict
// param is missing/invalid.
func eventForCall(call ToolCall) (Event, Verdict, error) {
	mapping, ok := callMappings[call.Name]
	if !ok {
		return EventUnknown, VerdictUnknown,
			fmt.Errorf("vlp: tool %q is not a recognised VLP event", call.Name)
	}
	if mapping.ParamForVerdict == "" {
		return mapping.Event, VerdictUnknown, nil
	}
	raw, ok := call.Params[mapping.ParamForVerdict].(string)
	if !ok {
		return EventUnknown, VerdictUnknown,
			fmt.Errorf("vlp: tool %q requires param %q (string)", call.Name, mapping.ParamForVerdict)
	}
	verdict, err := ParseVerdict(raw)
	if err != nil {
		return EventUnknown, VerdictUnknown,
			fmt.Errorf("vlp: tool %q: %w", call.Name, err)
	}
	return mapping.Event, verdict, nil
}

// nextActionFor returns the canonical "what should the LLM do next" string
// for a given state. Used by Record to steer the loop. Stable across
// versions; new states (APPEND-ONLY) must add a case here.
func nextActionFor(s State) string {
	switch s {
	case StateIdle:
		return "session_start"
	case StateDraftingSpec:
		return "vibe_publish"
	case StateSpecActive:
		return "artifact_log"
	case StateDriftJudging:
		return "drift_log"
	case StateComplete, StateNeedsHuman, StateAborted:
		return "stop"
	default:
		return "unknown"
	}
}

// Brief computes the context brief for the current state. Returns static
// defaults from contextBudgetByState and suggestedToolsByState. Spec 2.5
// will replace this with dynamic computation.
func (p *Package) Brief(in BriefInput) (BriefOutput, error) {
	if in.SessionID == "" {
		return BriefOutput{}, fmt.Errorf("vlp: BriefInput.SessionID is required")
	}
	if in.CurrentState == StateUnknown {
		return BriefOutput{}, fmt.Errorf("vlp: BriefInput.CurrentState is StateUnknown")
	}
	return BriefOutput{
		State:          in.CurrentState,
		ContextBudget:  contextBudgetByState[in.CurrentState],
		SuggestedTools: suggestedToolsByState[in.CurrentState],
	}, nil
}

// Propose validates a batch of proposed tool calls against the current state
// machine. Each call is evaluated independently; valid calls land in
// Approved, invalid calls land in Rejected with a reason. Redirected is
// reserved for future persona/minset-driven rewrites (spec 2.5+).
func (p *Package) Propose(in ProposeInput) (ProposeOutput, error) {
	if in.SessionID == "" {
		return ProposeOutput{}, fmt.Errorf("vlp: ProposeInput.SessionID is required")
	}
	if in.CurrentState == StateUnknown {
		return ProposeOutput{}, fmt.Errorf("vlp: ProposeInput.CurrentState is StateUnknown")
	}

	out := ProposeOutput{
		Approved:   []ToolCall{},
		Rejected:   []Rejection{},
		Redirected: []Redirect{},
	}
	for _, call := range in.ProposedCalls {
		event, verdict, err := eventForCall(call)
		if err != nil {
			out.Rejected = append(out.Rejected, Rejection{Call: call, Reason: err.Error()})
			continue
		}
		// Try the transition (validation, not state advance).
		if _, err := Transition(in.CurrentState, event, verdict); err != nil {
			out.Rejected = append(out.Rejected, Rejection{Call: call, Reason: err.Error()})
			continue
		}
		out.Approved = append(out.Approved, call)
	}
	return out, nil
}

// Record applies a completed tool call: looks up the corresponding VLP
// event, transitions state via Transition, and returns the next state +
// next action the LLM should take.
func (p *Package) Record(in RecordInput) (RecordOutput, error) {
	if in.SessionID == "" {
		return RecordOutput{}, fmt.Errorf("vlp: RecordInput.SessionID is required")
	}
	if in.CurrentState == StateUnknown {
		return RecordOutput{}, fmt.Errorf("vlp: RecordInput.CurrentState is StateUnknown")
	}

	event, verdict, err := eventForCall(in.Call)
	if err != nil {
		return RecordOutput{}, fmt.Errorf("vlp: cannot record: %w", err)
	}

	nextState, err := Transition(in.CurrentState, event, verdict)
	if err != nil {
		return RecordOutput{}, fmt.Errorf("vlp: cannot record: %w", err)
	}
	return RecordOutput{
		NextState:  nextState,
		NextAction: nextActionFor(nextState),
	}, nil
}

// Complete finalizes the session. Requires a terminal state (Complete,
// NeedsHuman, or Aborted). Returns the final state, a summary metrics map,
// and a human-readable handoff string.
func (p *Package) Complete(in CompleteInput) (CompleteOutput, error) {
	if in.SessionID == "" {
		return CompleteOutput{}, fmt.Errorf("vlp: CompleteInput.SessionID is required")
	}
	if !in.CurrentState.Terminal() {
		return CompleteOutput{},
			fmt.Errorf("vlp: Complete requires terminal state, got %s", in.CurrentState)
	}

	summary := map[string]interface{}{
		"final_state": in.CurrentState.String(),
	}
	for k, v := range in.Metrics {
		summary[k] = v
	}

	return CompleteOutput{
		FinalState: in.CurrentState,
		Summary:    summary,
		Handoff:    fmt.Sprintf("session %s ended in state %s", in.SessionID, in.CurrentState),
	}, nil
}