// Package tools — recall.go: dark_memory_recall.
//
// RFC §3 M1 + §6.1: the canonical scoped recall orchestrator. Returns
// the assembled atomic frames for the requested scope plus an
// incremental delta (write_audit rows since the supplied cursor).
//
// # Inputs
//   - scope:        "global" | "project" | "session"
//   - project_id:   optional (defaults to active project)
//   - session_id:   required when scope="session"
//   - since_token:  optional int64 cursor for delta (delta items
//                   with id > since_token)
//
// # Outputs
//   - frames: per-kind atomic frames (Identity, Scope, Capabilities,
//     Drift, Persona) — nil where the FrameSource returns nil
//   - delta: write_audit rows since since_token (capped to 50)
//   - new_token: max id of returned delta (cursor for next call)
//
// # Wave placement
// Wave 5A.ii.b.2.c. Depends on:
//   - 5A.ii.a (SaveFrame/GetFrame for caching)
//   - 5A.ii.b.1 (SaveRecallSubscription for cursor advancement)
//   - 5A.ii.b.2.a (StoreSource)
//   - 5A.ii.b.2.b (CachedSource)
//
// # Scope (5A.ii.b.2.c)
// This tool constructs a fresh CachedSource per invocation. The
// alternative — caching the FrameSource at registration time — is
// deferred to 5A.ii.b.2.c.1 (a follow-up). Per-invocation cost is
// one Store.GetVLPState + one Store.ListSDDEvaluations + one
// Store.GetConstitution + the cache I/O, all O(1) under typical use.
package tools

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/atomic"
	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/policy"
	"github.com/dark-agents/dark-memory-mcp/internal/recall"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// RecallInput is the input for dark_memory_recall.
type RecallInput struct {
	Scope      string `json:"scope"`
	ProjectID  string `json:"project_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	SinceToken int64  `json:"since_token,omitempty"`
}

// RecallOutput is the output for dark_memory_recall.
//
// FrameJSON is the canonical JSON encoding (Render()) of the
// atomic.Frame — the same bytes that get persisted to vibe_frames.
// Callers should treat the JSON as opaque; the cache layer is the
// canonical reader.
type RecallOutput struct {
	Frames    RecallFrames         `json:"frames"`
	Delta     []audit.WriteEvent   `json:"delta"`
	NewToken  int64                `json:"new_token"`
	CacheHits map[string]bool      `json:"cache_hits,omitempty"` // reserved for future wave
	ComposedAt time.Time           `json:"composed_at"`
}

// RecallFrames bundles the 5 atomic frame kinds. Each field is nil
// when the FrameSource returned (nil, nil) for that kind (cache miss
// + composition miss).
type RecallFrames struct {
	Identity     *atomic.IdentityFrame     `json:"identity,omitempty"`
	Scope        *atomic.ScopeFrame        `json:"scope,omitempty"`
	Capabilities *atomic.CapabilitiesFrame `json:"capabilities,omitempty"`
	Drift        *atomic.DriftFrame        `json:"drift,omitempty"`
	Persona      *atomic.PersonaFrame      `json:"persona,omitempty"`
}

// RegisterRecall wires dark_memory_recall into the registry. This
// is a separate Register function (not folded into RegisterContext)
// because it needs to construct a FrameSource at registration time
// rather than per-call.
//
// # Why not per-call?
// CachedSource is currently stateless; per-call construction is
// cheap. A future optimization (5A.ii.b.2.c.1) lifts the
// FrameSource to a singleton with cross-call TTL. For now, per-call
// keeps the wiring simple.
func RegisterRecall(reg *Registry, st store.Store, safety *store.SafetyHolder) {
	reg.Add(BindStore("recall",
		"Scoped recall: returns the assembled atomic frames for the requested scope plus an incremental delta (write_audit rows since since_token). scope=global|project|session.",
		MustJSONSchema(map[string]any{
			"type":     "object",
			"required": []string{"scope"},
			"properties": map[string]any{
				"scope":       map[string]any{"type": "string", "enum": []string{"global", "project", "session"}},
				"project_id":  map[string]any{"type": "string", "description": "Optional. Defaults to active project."},
				"session_id":  map[string]any{"type": "string", "description": "Required when scope=session."},
				"since_token": map[string]any{"type": "integer", "description": "Optional cursor for delta. Returns writes with id > since_token."},
			},
		}),
		st,
		func(ctx context.Context, s store.Store, in RecallInput) (*RecallOutput, error) {
			return runRecall(ctx, s, safety, in)
		}))
}

// runRecall composes the FrameSource, fetches all 5 frames, computes
// the delta, and returns the bundle.
func runRecall(ctx context.Context, st store.Store, safety *store.SafetyHolder, in RecallInput) (*RecallOutput, error) {
	if in.Scope != "global" && in.Scope != "project" && in.Scope != "session" {
		return nil, fmt.Errorf("recall: scope must be one of {global, project, session}, got %q", in.Scope)
	}
	if in.Scope == "session" && in.SessionID == "" {
		return nil, fmt.Errorf("recall: scope=session requires session_id")
	}

	// Build a fresh FrameSource per invocation.
	inner := recall.NewStoreSource(st, nil)
	src := policy.FrameSource(recall.NewCachedSource(inner, st, safety, nil, log.Default()))

	out := &RecallOutput{
		Frames:    RecallFrames{},
		ComposedAt: time.Now().UTC(),
	}

	// Compose the 5 frames. SessionID is what the FrameSource needs;
	// for non-session scopes we pass the active session id (or "").
	sessionForFrames := in.SessionID
	if sessionForFrames == "" {
		// Best-effort: use the most-recent session in the active project.
		// Returns (nil, nil) if none — that's fine, caller treats as miss.
		if sessionID, err := mostRecentSessionID(ctx, st); err == nil && sessionID != "" {
			sessionForFrames = sessionID
		}
	}

	if sessionForFrames != "" {
		if id, err := src.IdentityFrame(ctx, sessionForFrames); err == nil {
			out.Frames.Identity = id
		} else {
			return nil, fmt.Errorf("recall: IdentityFrame: %w", err)
		}
		if sc, err := src.ScopeFrame(ctx, sessionForFrames); err == nil {
			out.Frames.Scope = sc
		} else {
			return nil, fmt.Errorf("recall: ScopeFrame: %w", err)
		}
		if caps, err := src.CapabilitiesFrame(ctx, sessionForFrames); err == nil {
			out.Frames.Capabilities = caps
		} else {
			return nil, fmt.Errorf("recall: CapabilitiesFrame: %w", err)
		}
		if df, err := src.DriftFrame(ctx, sessionForFrames); err == nil {
			out.Frames.Drift = df
		} else {
			return nil, fmt.Errorf("recall: DriftFrame: %w", err)
		}
		if pf, err := src.PersonaFrame(ctx, sessionForFrames); err == nil {
			out.Frames.Persona = pf
		} else {
			return nil, fmt.Errorf("recall: PersonaFrame: %w", err)
		}
	}

	// Delta computation: write_audit rows with id > since_token,
	// filtered by project + (when scope=session) session_id.
	deltaFilter := audit.ListFilters{
		SinceID: in.SinceToken,
		Limit:   50,
	}
	if in.Scope == "session" && in.SessionID != "" {
		deltaFilter.SessionID = in.SessionID
	}
	if in.ProjectID != "" {
		deltaFilter.ProjectID = in.ProjectID
	}
	delta, err := st.ListWrites(ctx, deltaFilter)
	if err != nil {
		return nil, fmt.Errorf("recall: ListWrites delta: %w", err)
	}
	// ListWrites returns id DESC, so the FIRST row has the max id.
	out.Delta = delta
	if len(delta) > 0 {
		out.NewToken = delta[0].ID
	} else {
		out.NewToken = in.SinceToken
	}
	return out, nil
}

// mostRecentSessionID returns the most-recently-started session id
// in the active project. Used as a fallback when the caller asks
// for scope=global or scope=project without specifying session_id.
// Returns "" if no session exists.
func mostRecentSessionID(ctx context.Context, st store.Store) (string, error) {
	sessions, err := st.ListSessions(ctx, 1)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", nil
	}
	return sessions[0].SessionID, nil
}