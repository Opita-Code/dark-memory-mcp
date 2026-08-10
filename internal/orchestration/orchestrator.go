// Package orchestration contains the workflow API that wraps the
// Store with safety (INV-3), audit (INV-1), and economy (Atlan).
// Each Orchestrator method is one workflow operation that the MCP
// server (or any external caller) can invoke. They are the typed,
// reusable counterpart of the untyped Store interface.
//
// Conventions (spec 173):
//   - All methods take ctx context.Context as first param.
//   - All methods validate inputs and return error typed by the
//     store layer (ErrSessionRequired, ErrInvalidArgument, etc.).
//   - All writes emit write_audit atomically with the data write
//     (Store enforces this; orchestrator just supplies WriteContext).
//   - All reads require an active project (Store.requireProject).
//
// Layering:
//
//	MCP server (Wave 4)
//	    |
//	    v
//	Orchestrator  <-- this package
//	    |
//	    v
//	Store (sqlite or postgres partial impl)
//
// Safety:
//   - The canary check (INV-3) is invoked inside Store.Save*, so
//     orchestrators don't need to call it explicitly. They DO need
//     to populate the WriteContext so the audit row carries the
//     orchestrator's actor name.
package orchestration

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/safety"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vlp"
)

// Orchestrator is the typed workflow API. Construct with New().
type Orchestrator struct {
	Store    store.Store
	Safety   *safety.Holder
	now      func() time.Time  // injectable for tests
	backends []ResearchBackend // registered research backends (O3)
	selector LLMSelector       // LLM selector for O5 Judge
	vlpUC    *vlp.UseCase      // VLP state machine (v2.13.0: auto-drive vibe-loop)

	// OnActiveSessionChanged (v2.1.3 cache-invalidation fix) is invoked
	// after every successful SetActiveSession / ClearActiveSession write
	// so external caches (specifically the gate's
	// StoreBackedActiveSessionResolver) can invalidate their stale
	// entries synchronously.
	//
	// Background: the resolver caches the lookup result for
	// DefaultActiveSessionCacheTTL (5s). The cache is keyed by
	// project_id. session_start's wire path is
	//   wrapHandler → GateMiddleware.Wrap → buildGateInput
	//   → resolver.ActiveSessionID → cache miss → DB lookup
	//   → Cache filled with whatever was in projects.active_session_id
	//     BEFORE SetActiveSession wrote the new value (i.e. empty
	//     for a fresh boot, or the previous session_id otherwise)
	//   → inner → orch.SessionStart → SetActiveSession writes the
	//     new value to DB → cache NOT invalidated.
	// The next tool call within the 5s TTL returns the stale value
	// (empty or wrong session_id) and the gate refuses with
	// ErrFrameStaleTooFar / ErrSessionNotFound.
	//
	// main.go wires this to resolver.Invalidate so the cache is
	// flushed synchronously after each write. nil is safe — the
	// orchestrator skips the call.
	OnActiveSessionChanged func(projectID string)
}

// New constructs an Orchestrator with the given Store and Safety
// holder. Safety may be nil (orchestrator will construct an empty
// Holder). now is injectable for deterministic tests; pass nil to
// default to time.Now. Use WithBackends to register research backends
// and WithLLMSelector to wire the Judge pipeline.
func New(s store.Store, safe *safety.Holder) *Orchestrator {
	if safe == nil {
		h := &safety.Holder{}
		safe = h
	}
	return &Orchestrator{
		Store:  s,
		Safety: safe,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// WithVLP attaches the VLP state machine (UseCase) to the orchestrator.
// When set, PublishVibe / VibeSpec / SessionStart auto-emit VLP events
// so the vibe-loop state stays in sync with data-plane operations.
// Nil-safe: operations skip VLP emission when uc is nil.
func (o *Orchestrator) WithVLP(uc *vlp.UseCase) *Orchestrator {
	o.vlpUC = uc
	return o
}

// WithLLMSelector attaches an LLMSelector to the orchestrator. Used
// by O5 Judge. This is the PRIMARY mechanism: the harness wires its
// cloud LLM at boot time (spec 173 O5, v2.11.1).
//
// When no selector is injected, ensureLLMSelector (the SECONDARY
// fallback) auto-detects the harness LLM from env vars at call time.
// Operators who want explicit control should call WithLLMSelector
// at boot; operators without harness injection get the env-var bridge.
func (o *Orchestrator) WithLLMSelector(s LLMSelector) *Orchestrator {
	o.selector = s
	return o
}

// ensureLLMSelector lazily returns the LLM selector.
//
// Primary: harness injection via WithLLMSelector (checked first).
// Secondary: auto-detect the harness LLM from env vars
//   (ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY /
//    DARK_DRIFT_JUDGE_DAEMON_URL / legacy DARK_SCRAPPER_URL).
//   This is a bridge for operators who have not yet adopted the
//   injection pattern — it detects the SAME LLM the harness used to
//   call the MCP tool and reuses it for Judge/mindset calls.
//
// Falls back to ErrNoLLMAvailable if no LLM is reachable through
// either mechanism.
func (o *Orchestrator) ensureLLMSelector() LLMSelector {
	if o.selector != nil {
		return o.selector
	}
	// Secondary: use the harness's LLM via env-var detection.
	// If no key is set, the selector will return ErrNoLLMAvailable
	// on Select — same behavior as the primary nil-injection path.
	client, _ := NewSelfHarnessClient()
	return NewOSINTSelector(client)
}

// fieldError carries the offending field name AND the sentinel it
// wraps, so the tools layer (ToToolError) can populate the structured
// ToolError.Field field path for the operator. F35 wire-propagation.
//
// The error string is preserved for backward-compat fallback with
// log scrapers that grep on `Errorf("%w: %s is required", ...)`, but
// callers SHOULD errors.As(err, &fieldError{}) instead of string-parsing.
type fieldError struct {
	store error
	Field string
}

func (e *fieldError) Error() string {
	if e.Field == "" {
		return e.store.Error()
	}
	return e.store.Error() + ": field=" + e.Field
}

func (e *fieldError) Unwrap() error { return e.store }

// errMissingField produces a structured error that carries the field
// name. The tools layer extracts it via errors.As and populates
// ToolError.Field so the harness's error path renders a precise
// fix-up hint instead of a generic message.
func errMissingField(field string) error {
	return &fieldError{store: store.ErrInvalidArgument, Field: field}
}

// emitVLP fires a VLP event against a session. Best-effort: no VLP
// wired (uc nil), no session_id, or state-already-advanced
// (ErrInvalidTransition) are all silent no-ops. Other errors are
// logged but never fail the caller — VLP is a companion, not a gate.
// Callers use this after their data-plane operations succeed so the
// vibe-loop state stays in sync without requiring a separate harness
// call to vlp_handle_event.
func (o *Orchestrator) emitVLP(ctx context.Context, sessionID, actor string, event vlp.Event) {
	o.emitVLPWithVerdict(ctx, sessionID, actor, event, vlp.VerdictUnknown)
}

// emitVLPWithVerdict is emitVLP with a verdict payload (for drift_log).
func (o *Orchestrator) emitVLPWithVerdict(ctx context.Context, sessionID, actor string, event vlp.Event, verdict vlp.Verdict) {
	if o.vlpUC == nil || sessionID == "" {
		return
	}
	wc := store.WriteContext{
		Actor:     actor,
		SessionID: sessionID,
		WritePath: "emitVLP:" + event.String(),
	}
	_, err := o.vlpUC.HandleEvent(ctx, wc, sessionID, event, verdict, "")
	if err != nil {
		// ErrInvalidTransition means the harness already advanced the
		// VLP manually — not an error, just a no-op.
		var invalidTransition vlp.ErrInvalidTransition
		if errors.As(err, &invalidTransition) {
			return
		}
		// Other errors (store failures, missing session, etc.) are
		// logged but don't fail the caller. VLP is best-effort.
		log.Printf("dark-mem-mcp: emitVLP session=%s event=%s verdict=%s: %v",
			sessionID, event, verdict, err)
	}
}

// verdictToVLP maps a publish verdict string to the VLP Verdict enum.
func verdictToVLP(v string) vlp.Verdict {
	switch v {
	case "aligned":
		return vlp.VerdictAligned
	case "drift_detected":
		return vlp.VerdictDriftDetected
	case "needs_human":
		return vlp.VerdictNeedsHuman
	default:
		return vlp.VerdictUnknown
	}
}
