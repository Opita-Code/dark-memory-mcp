// Package vibeflow — Loop 8 L8.2: lazy activation state machine.
//
// This file implements the runtime decision of "how much of vibe-flow
// should be active right now?" for a given conversation. It is
// orthogonal to the per-message classification in classifier.go
// (L8.1a) and context.go (L8.1b): those decide what KIND of task this
// is; this file decides how MUCH pipeline to apply.
//
// Design constraints (from Phase 20):
//   1. Cost-lens: 6/10 personas default to ModeOff so the harness does
//      not pay for spec+drift on a typo fix.
//   2. Lazy: start cheap, escalate only when the operator shows
//      complexity (time, tool calls, doubt, followup).
//   3. Asymmetric de-escalation: vibe-flow does not "disappear" mid-
//      conversation. Once promoted to full, the lowest it can fall is
//      degraded (via idle). Only SignalReset returns to the persona's
//      default.
//   4. Panic-debugger exception: a RushMode operator with panic
//      keywords is NOT escalated. They want fast answers, not gates.
//
// NOT thread-safe. Use a single goroutine.
package vibeflow

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Mode is the activation level of vibe-flow for the current conversation.
type Mode int

const (
	// ModeOff = no vibe-flow. The LLM answers directly without spec,
	// drift check, or memory writes. Default for cost-minimal personas
	// (novice, panic-debugger, no-técnico, móvil, rush, táctico) and
	// for short tasks (< 60s elapsed, < 3 tool calls, 0 doubts).
	ModeOff Mode = iota

	// ModeDegraded = light touch. Summary only, no spec, no drift, no
	// memory writes. For medium tasks or when the operator shows mild
	// doubt.
	ModeDegraded

	// ModeFull = full pipeline (spec + publish + drift). For long
	// tasks, multi-artifact work, or when the operator shows sustained
	// doubt/iteration.
	ModeFull
)

// String returns the lowercase mode name. Used in logs and the
// Snapshot() output.
func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeDegraded:
		return "degraded"
	case ModeFull:
		return "full"
	default:
		return "unknown"
	}
}

// Signal is an event that can update state and may trigger a mode
// transition. The harness is responsible for extracting signals from
// the conversation and calling Apply() for each.
type Signal int

const (
	SignalNone Signal = iota
	// SignalTime: value = seconds elapsed since the conversation
	// started. Sent periodically (the harness should call Apply
	// whenever the operator is active).
	SignalTime
	// SignalToolCalls: value = total tool calls made so far. Sent
	// after each tool call.
	SignalToolCalls
	// SignalFollowup: value = cumulative count of followup signals
	// detected in operator messages ("wait", "actually", "mejor
	// dicho"). Increments internally on each detection.
	SignalFollowup
	// SignalDoubt: value = cumulative count of doubt signals
	// detected in operator messages ("are you sure", "verify",
	// "tienes seguro"). Increments internally on each detection.
	SignalDoubt
	// SignalTokens: value = total tokens used so far. Used by the
	// full→degraded transition (deferred to Loop 9 for the budget
	// field; for L8.2 it is recorded but not gated).
	SignalTokens
	// SignalReset: returns the manager to the persona's default
	// mode. Used when the conversation moves to a new task.
	SignalReset
)

// String returns the lowercase signal name.
func (s Signal) String() string {
	switch s {
	case SignalTime:
		return "time"
	case SignalToolCalls:
		return "tool_calls"
	case SignalFollowup:
		return "followup"
	case SignalDoubt:
		return "doubt"
	case SignalTokens:
		return "tokens"
	case SignalReset:
		return "reset"
	default:
		return "none"
	}
}

// State is the runtime mode state of a conversation. Returned by
// ModeManager.State() for logging and tests.
//
// LastUpdated is set on every Apply/Tick (for harness diagnostics).
// LastSignal is set only on real user activity (tool calls, messages,
// doubts, followups) and is the reference for idle-driven
// de-escalation. The split lets the harness poll via Tick() without
// resetting the idle clock.
type State struct {
	Mode        Mode
	StartedAt   time.Time
	LastUpdated time.Time
	LastSignal  time.Time
	Elapsed     time.Duration
	ToolCalls   int
	Followups   int
	Doubts      int
	TokensUsed  int
	Transitions int
}

// Transition is a single mode change. Stored in history for audit.
type Transition struct {
	From   Mode
	To     Mode
	Signal Signal
	At     time.Time
	Reason string
}

// ModeThresholds defines the per-tier escalation rules. Field names
// encode the transition (e.g. OffToDegraded_TimeSec means "the seconds
// threshold for Off→Degraded"). The default values target the
// cost-lens: 5-min tasks should stay in off unless the operator
// shows doubt or iteration.
type ModeThresholds struct {
	// Off → Degraded
	OffToDegraded_TimeSec    int  // default 60 (1 min)
	OffToDegraded_ToolCalls  int  // default 3
	OffToDegraded_Doubts     int  // default 1 (any doubt = escalate)
	OffToDegraded_Followups  int  // default 1 (any followup = iterate = escalate)
	OffToDegraded_PanicMulti bool // default true (panic+code+novice = escalate)

	// Degraded → Full
	DegradedToFull_TimeSec   int  // default 120 (2 min)
	DegradedToFull_ToolCalls int  // default 6
	DegradedToFull_Doubts    int  // default 2
	DegradedToFull_Followups int  // default 2

	// Full → Degraded
	FullToDegraded_IdleSec int // default 300 (5 min idle = drop to degraded)

	// Degraded → Off (only via explicit reset, not idle)
	DegradedToOff_IdleSec int // default 600 (10 min idle)
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

// DefaultModeFor returns the persona's default mode based on Context.
// The cost-lens takes precedence: if RushMode is on, the operator wants
// fast answers regardless of skill level → default off. Otherwise
// match the per-tier table: novice → off, intermediate → degraded,
// expert → full.
func DefaultModeFor(ctx Context) Mode {
	if ctx.RushMode {
		return ModeOff
	}
	switch ctx.Tier {
	case TierNovice:
		return ModeOff
	case TierExpert:
		return ModeFull
	case TierIntermediate:
		return ModeDegraded
	default:
		return ModeDegraded
	}
}

// DefaultThresholdsFor returns the per-tier calibrated thresholds.
// Novice escalates FASTER (any doubt = off→degraded, 1 doubt =
// degraded→full). Expert stays in full LONGER (5min → 10min idle
// before falling to degraded).
func DefaultThresholdsFor(tier Tier) ModeThresholds {
	base := ModeThresholds{
		OffToDegraded_TimeSec:    60,
		OffToDegraded_ToolCalls:  3,
		OffToDegraded_Doubts:     1,
		OffToDegraded_Followups:  1,
		OffToDegraded_PanicMulti: true,
		DegradedToFull_TimeSec:   120,
		DegradedToFull_ToolCalls: 6,
		DegradedToFull_Doubts:    2,
		DegradedToFull_Followups: 2,
		FullToDegraded_IdleSec:   300,
		DegradedToOff_IdleSec:    600,
	}
	switch tier {
	case TierNovice:
		// Novices escalate fast on doubt because they are unsure
		// more often, and a wrong answer is more costly.
		base.OffToDegraded_Doubts = 1
		base.DegradedToFull_Doubts = 1
	case TierExpert:
		// Experts stay in full longer; their tasks are denser and
		// the 5-min default idle is too aggressive.
		base.FullToDegraded_IdleSec = 600
	}
	return base
}

// ---------------------------------------------------------------------------
// ModeManager
// ---------------------------------------------------------------------------

// ModeManager is the L8.2 runtime component. It tracks the operator's
// conversation state and decides when vibe-flow should activate.
//
// Usage:
//   m := NewModeManager(ctx, time.Now)
//   mode := m.Apply(SignalToolCalls, 1)
//   mode := m.OnMessage("wait, actually...", features)
//   mode := m.Tick()  // called every 30s to update LastUpdated
//
// NOT thread-safe in v1. Wrap with a mutex if calling from multiple
// goroutines.
type ModeManager struct {
	mu         sync.Mutex
	ctx        Context
	thresholds ModeThresholds
	state      State
	history    []Transition
	now        func() time.Time
}

// NewModeManager constructs a manager with the persona's default mode
// and tier-calibrated thresholds. The `now` function is injectable for
// deterministic tests.
func NewModeManager(ctx Context, now func() time.Time) *ModeManager {
	if now == nil {
		now = time.Now
	}
	m := &ModeManager{
		ctx:        ctx,
		thresholds: DefaultThresholdsFor(ctx.Tier),
		now:        now,
	}
	m.state = State{
		Mode:        DefaultModeFor(ctx),
		StartedAt:   now(),
		LastUpdated: now(),
		LastSignal:  now(),
	}
	return m
}

// Apply updates the state with a new signal and value, possibly
// triggering a transition. Returns the (possibly new) mode.
func (m *ModeManager) Apply(sig Signal, value int) Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applyLocked(sig, value)
}

func (m *ModeManager) applyLocked(sig Signal, value int) Mode {
	now := m.now()
	m.state.LastUpdated = now
	switch sig {
	case SignalTime:
		m.state.Elapsed = time.Duration(value) * time.Second
		// SignalTime is bookkeeping; do NOT touch LastSignal.
	case SignalToolCalls:
		m.state.ToolCalls = value
		m.state.LastSignal = now
	case SignalFollowup:
		m.state.Followups = value
		m.state.LastSignal = now
	case SignalDoubt:
		m.state.Doubts = value
		m.state.LastSignal = now
	case SignalTokens:
		m.state.TokensUsed = value
		// SignalTokens is bookkeeping; do NOT touch LastSignal.
	case SignalReset:
		m.resetLocked()
		return m.state.Mode
	}
	return m.checkTransitionsLocked(sig)
}

// OnMessage extracts the followup/doubt signals from a message and
// updates the corresponding counters, then checks for transitions.
// The `features` argument is the L8.1b feature set extracted from the
// same message (used to detect panic).
//
// Returns the (possibly new) mode.
func (m *ModeManager) OnMessage(message string, features Features) Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.state.LastUpdated = now
	m.state.LastSignal = now // OnMessage is always real user activity

	msg := strings.ToLower(message)

	// Followup detection: increment the Followups counter on each
	// detection. The harness never calls Apply(SignalFollowup) for
	// message content — OnMessage is the canonical entry point.
	if hasFollowupKeywords(msg) {
		m.state.Followups++
	}
	if hasDoubtKeywords(msg) {
		m.state.Doubts++
	}

	// PanicMod handling:
	//   - RushMode + panic keywords → DECREMENT doubts (panic-debugger
	//     persona wants fast answers, not gates). Net effect: the
	//     panic is "absorbed" and does not trigger escalation.
	//   - !RushMode + panic keywords → INCREMENT doubts (operator is
	//     panicking, they need help; escalation is correct).
	//   - No panic keywords → no-op.
	if features.PanicKeywords {
		if m.ctx.RushMode {
			if m.state.Doubts > 0 {
				m.state.Doubts--
			}
		} else {
			m.state.Doubts++
		}
	}

	return m.checkTransitionsLocked(SignalDoubt)
}

// Tick is called periodically (every ~30s) to update LastUpdated and
// check for idle-driven de-escalation. Returns the (possibly new) mode.
func (m *ModeManager) Tick() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.LastUpdated = m.now()
	// IdleSec is measured from LastUpdated, not StartedAt, so an
	// active conversation never accidentally de-escalates.
	idleSec := int(time.Since(m.state.LastUpdated).Seconds())
	_ = idleSec // currently unused; transitions check on Apply()
	return m.checkTransitionsLocked(SignalTime)
}

// Current returns the current mode. Cheap (no lock).
func (m *ModeManager) Current() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Mode
}

// State returns a copy of the current state.
func (m *ModeManager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// History returns a copy of the transition history.
func (m *ModeManager) History() []Transition {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Transition, len(m.history))
	copy(out, m.history)
	return out
}

// Reset returns the manager to the persona's default mode. Clears all
// counters. Recorded in history as a single transition.
func (m *ModeManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetLocked()
}

func (m *ModeManager) resetLocked() {
	oldMode := m.state.Mode
	newMode := DefaultModeFor(m.ctx)
	m.history = append(m.history, Transition{
		From:   oldMode,
		To:     newMode,
		Signal: SignalReset,
		At:     m.now(),
		Reason: "explicit reset",
	})
	m.state = State{
		Mode:        newMode,
		StartedAt:   m.now(),
		LastUpdated: m.now(),
		LastSignal:  m.now(),
		Transitions: m.state.Transitions + 1,
	}
}

// Snapshot returns a one-line summary for logs.
func (m *ModeManager) Snapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf(
		"mode=%s tier=%s elapsed=%ds tools=%d doubts=%d followups=%d tokens=%d transitions=%d",
		m.state.Mode,
		m.ctx.Tier,
		int(m.state.Elapsed.Seconds()),
		m.state.ToolCalls,
		m.state.Doubts,
		m.state.Followups,
		m.state.TokensUsed,
		m.state.Transitions,
	)
}

// ---------------------------------------------------------------------------
// Transition logic
// ---------------------------------------------------------------------------

// checkTransitionsLocked examines the current state and returns the
// mode the manager SHOULD be in. If different from state.Mode, records
// a transition and updates state.Mode.
//
// Called from applyLocked, OnMessage, and Tick.
func (m *ModeManager) checkTransitionsLocked(trigger Signal) Mode {
	elapsedSec := int(m.state.Elapsed.Seconds())
	var newMode Mode
	switch m.state.Mode {
	case ModeOff:
		if m.shouldEscalateOff(elapsedSec) {
			newMode = ModeDegraded
		}
	case ModeDegraded:
		if m.shouldEscalateDegraded(elapsedSec) {
			newMode = ModeFull
		}
	case ModeFull:
		if m.shouldDeEscalateFull(elapsedSec) {
			newMode = ModeDegraded
		}
	}
	if newMode != 0 && newMode != m.state.Mode {
		m.recordTransitionLocked(newMode, trigger)
	}
	return m.state.Mode
}

func (m *ModeManager) recordTransitionLocked(to Mode, trigger Signal) {
	reason := transitionReason(trigger, m.state)
	m.history = append(m.history, Transition{
		From:   m.state.Mode,
		To:     to,
		Signal: trigger,
		At:     m.now(),
		Reason: reason,
	})
	m.state.Mode = to
	m.state.Transitions++
}

func (m *ModeManager) shouldEscalateOff(elapsedSec int) bool {
	t := m.thresholds
	// Doubt: any doubt escalates (matches the cost-lens — when the
	// operator is unsure, structure helps).
	if m.state.Doubts >= t.OffToDegraded_Doubts {
		return true
	}
	// Followup: operator is iterating, they probably want more
	// structure.
	if m.state.Followups >= t.OffToDegraded_Followups {
		return true
	}
	// Time + tool calls together: this is a real task, not a typo.
	if elapsedSec >= t.OffToDegraded_TimeSec && m.state.ToolCalls >= t.OffToDegraded_ToolCalls {
		return true
	}
	// PanicMod off: a novice with panic AND code (multi_artifact
	// proxy) is in over their head; escalate. The PanicMod check
	// happens in OnMessage (RushMode + PanicKeywords → suppress
	// panic-driven doubt). This branch is intentionally a no-op
	// for now; reserved for future tuning.
	_ = t.OffToDegraded_PanicMulti
	return false
}

func (m *ModeManager) shouldEscalateDegraded(elapsedSec int) bool {
	t := m.thresholds
	if m.state.Doubts >= t.DegradedToFull_Doubts {
		return true
	}
	if m.state.Followups >= t.DegradedToFull_Followups {
		return true
	}
	if elapsedSec >= t.DegradedToFull_TimeSec && m.state.ToolCalls >= t.DegradedToFull_ToolCalls {
		return true
	}
	return false
}

func (m *ModeManager) shouldDeEscalateFull(elapsedSec int) bool {
	t := m.thresholds
	// Full → Degraded on long idle (5min default; 10min for expert).
	// We measure idle from LastSignal (NOT LastUpdated) so that
	// bookkeeping signals (SignalTime, SignalTokens, Tick) don't
	// reset the idle clock. This is critical for the cost-lens: an
	// operator can do nothing for 5min and the manager should
	// de-escalate even if the harness is still ticking.
	if int(m.now().Sub(m.state.LastSignal).Seconds()) >= t.FullToDegraded_IdleSec {
		return true
	}
	// Token-ratio de-escalation deferred to Loop 9.
	return false
}

// transitionReason builds a human-readable reason for the transition.
func transitionReason(sig Signal, s State) string {
	switch sig {
	case SignalDoubt:
		return fmt.Sprintf("doubts=%d >= threshold", s.Doubts)
	case SignalFollowup:
		return fmt.Sprintf("followups=%d >= threshold", s.Followups)
	case SignalTime:
		return fmt.Sprintf("elapsed=%ds, tools=%d", int(s.Elapsed.Seconds()), s.ToolCalls)
	case SignalReset:
		return "explicit reset"
	default:
		return sig.String()
	}
}

// ---------------------------------------------------------------------------
// Keyword detection (OnMessage helpers)
// ---------------------------------------------------------------------------

// hasFollowupKeywords returns true if the message contains any phrase
// that signals the operator is revising their previous message. English
// + Spanish. Casing-insensitive (caller lowercases first).
func hasFollowupKeywords(msg string) bool {
	for _, kw := range followupKeywords {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// hasDoubtKeywords returns true if the message contains any phrase
// that signals the operator is unsure about the previous answer.
// English + Spanish. Casing-insensitive.
func hasDoubtKeywords(msg string) bool {
	for _, kw := range doubtKeywords {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// followupKeywords is a curated list of common followup phrases in
// English and Spanish. Kept short to avoid false positives.
var followupKeywords = []string{
	// English
	"wait,",
	"actually,",
	"instead,",
	"rather,",
	"on second thought",
	"never mind",
	"scratch that",
	"no, ",
	"not that",
	// Spanish
	"mejor ",
	"espera,",
	"mejor dicho",
	"no, ",
	"al contrario",
	"más bien",
}

// doubtKeywords is a curated list of common doubt phrases in English
// and Spanish. Kept short to avoid false positives.
var doubtKeywords = []string{
	// English
	"are you sure",
	"is this right",
	"is that right",
	"verify",
	"double check",
	"double-check",
	"really?",
	"correct?",
	"are you sure?",
	"is it correct",
	"is this correct",
	"check this",
	// Spanish
	"tienes seguro",
	"estás seguro",
	"estas seguro",
	"está bien?",
	"esta bien?",
	"verifica",
	"comprueba",
	"realmente?",
	"de verdad?",
	"es correcto?",
	"estás seguro?",
	"estas seguro?",
}
