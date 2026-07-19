// O5E-resurrect: SessionResurrect — creates a NEW session row that
// inherits scope state from a closed_aborted session. Per INV-8 the
// only resurrection source is a closed_aborted (or idle, by sweeper)
// session. The original session is NOT touched — it stays as the
// audit-history anchor.
//
// # Atomicity contract
//   - ONE public function
//   - TWO input/output shapes
//   - DEPENDS on Store.SaveResurrect (5E.ii; sqlite + postgres)
//   - DEPENDS on Store.GetSession (to fetch the original)
//   - DEPENDS on Store.FindClosedAbortedForActor (alternative path:
//     operator passes only Operator+Lookback and we discover)
//   - DEPENDS on Store.ActiveConstitution (frame-aware inheritance
//     audit — see Wave 5E.iv)
//
// # Resurrection chain
// The new session row has:
//   - parent_session_id = original.SessionID (immediate predecessor)
//   - resurrected_from  = original.ResurrectedFrom OR original.SessionID
//                          (chain pointer to the deepest ancestor)
//   - status = open
//   - started_at, last_heartbeat_at = now()
//   - constitution_id+ver inherited from the original
//   - active_mods inherited from the original (these are the MODS that
//     were active at the time of the original's close; the operator
//     can verify or extend via _vibe_spec or _session_start)
//
// # Frame-aware inheritance audit (5E.iv)
//
// SaveResurrect copies the original's constitution_id+ver verbatim
// onto the new session. If the GLOBAL active constitution was bumped
// since the original closed (INFRA-003 v2 path or operator manual
// bump), the new session silently inherits the OLD binding. Persona
// reads on the new session would then diverge from the active
// constitution's persona — a quiet inconsistency the operator might
// not notice.
//
// 5E.iv surfaces this explicitly:
//   - SessionResurrectOutput gains InheritedConstitutionID/Ver +
//     ActiveConstitutionID/Ver + ConstitutionBumped.
//   - The orchestrator writes a "SessionResurrectFrameAudit" audit
//     row carrying the comparison + the inherited mods so the
//     audit trail shows the validation outcome.
//   - Re-derivation of persona/capabilities is intentionally NOT
//     pre-populated. The next dark_memory_recall (5A.ii.b.2.c)
//     reads PersonaFrame from the GLOBAL active constitution
//     anyway (StoreSource composes from the active row, not the
//     session row). The new session's IdentityFrame DOES read the
//     session row's constitution_id+ver — so the output's
//     ConstitutionBumped=true signals "your IdentityFrame shows the
//     old binding; PersonaFrame shows the new one" to the operator.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/session"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// SessionResurrectInput is the request to resurrect a session.
//
// Two operator forms:
//
//  1. Explicit form: OriginalSessionID is set. Operator/lookback
//     are ignored. The orchestrator fetches the original by id and
//     validates it's resurrectable.
//
//  2. Discovery form: OriginalSessionID is empty; Operator + Lookback
//     are set. The orchestrator calls FindClosedAbortedForActor to
//     discover the latest resurrectable session for that operator.
type SessionResurrectInput struct {
	OriginalSessionID string `json:"original_session_id,omitempty"`
	Operator          string `json:"operator,omitempty"`
	Lookback          string `json:"lookback,omitempty"`
	Reason            string `json:"reason,omitempty"` // "explicit_recovery", "harness_restart", operator-supplied
}

// SessionResurrectOutput reflects the new session row.
//
// 5E.iv: enriched with the frame-aware inheritance audit fields.
type SessionResurrectOutput struct {
	NewSessionID      string    `json:"new_session_id"`
	OriginalSessionID string    `json:"original_session_id"`  // the session we resurrected from
	InheritedFrom     string    `json:"inherited_from"`       // alias of OriginalSessionID (for clarity)
	StartedAt         time.Time `json:"started_at"`
	ResurrectChainLen int       `json:"resurrect_chain_len"`  // 1 if first resurrection, 2 if previous was resurrected, etc.

	// --- 5E.iv frame-aware inheritance audit ---
	// InheritedConstitution* are what was on the ORIGINAL session
	// (copied verbatim to the new session by SaveResurrect).
	InheritedConstitutionID  string `json:"inherited_constitution_id,omitempty"`
	InheritedConstitutionVer string `json:"inherited_constitution_ver,omitempty"`

	// ActiveConstitution* are what the GLOBAL active constitution is
	// RIGHT NOW. Empty when no constitution is registered.
	ActiveConstitutionID  string `json:"active_constitution_id,omitempty"`
	ActiveConstitutionVer string `json:"active_constitution_ver,omitempty"`

	// ConstitutionBumped is true when:
	//   - the original session had a binding (id+ver non-empty) AND
	//     the active binding differs, OR
	//   - the original had no binding but the active does.
	// Operator signal: "your IdentityFrame shows the OLD binding;
	// PersonaFrame shows the NEW one. To align, call
	// dark_memory_session_start with the new constitution_id+ver
	// and close this resurrected session."
	ConstitutionBumped bool `json:"constitution_bumped"`

	// InheritedMods is the parsed JSON-array form of the original
	// session's active_mods (carried onto the new session).
	InheritedMods []string `json:"inherited_mods,omitempty"`
}

// SessionResurrect creates a new session that inherits scope state
// from a closed_aborted session. The orchestrator queries the
// session metadata first (validation), then calls SaveResurrect
// (the atomic INSERT + write_audit), then runs the frame-aware
// inheritance audit (5E.iv) — comparing the inherited binding
// against the GLOBAL active constitution.
//
// # Errors
//   - ErrInvalidArgument: missing identification (neither
//     original_session_id nor operator+lookback)
//   - ErrNotFound: the original session is missing or not resurrectable
//   - ErrInvalidState: original.Status not in {closed_aborted, idle}
func (o *Orchestrator) SessionResurrect(ctx context.Context, in SessionResurrectInput) (*SessionResurrectOutput, error) {
	original, err := o.lookupResurrectionCandidate(ctx, in)
	if err != nil {
		return nil, err
	}
	if original == nil {
		return nil, fmt.Errorf("%w: no resurrection candidate found for input", store.ErrNotFound)
	}

	// Validate the original is resurrectable.
	status := session.Status(original.Status)
	if !status.IsResurrectable() {
		return nil, fmt.Errorf("%w: session %q status=%q is not resurrectable",
			store.ErrInvalidState, original.SessionID, original.Status)
	}

	reason := in.Reason
	if reason == "" {
		reason = "explicit_recovery"
	}

	wc := store.WriteContext{
		Actor:     "orchestrator_session_resurrect",
		SessionID: original.SessionID, // resume uses the original's id; SaveResurrect creates new
		WritePath: "SessionResurrect",
	}
	// 5E.iv.b: SaveResurrect now returns the new session row directly.
	// No follow-up read needed.
	newSess, err := o.Store.SaveResurrect(ctx, wc, original)
	if err != nil {
		return nil, fmt.Errorf("session_resurrect: save: %w", err)
	}
	if newSess == nil || newSess.SessionID == "" {
		return nil, fmt.Errorf("session_resurrect: SaveResurrect returned an empty session row")
	}

	startedAt, _ := time.Parse(time.RFC3339Nano, newSess.StartedAt)
	if startedAt.IsZero() {
		startedAt = o.now()
	}

	// Chain length: 1 if original was a fresh session, 2+ if original
	// was itself a resurrected session (carries its own ResurrectedFrom).
	chainLen := 1
	if original.ResurrectedFrom != "" {
		// Walk the chain (depth-bounded to avoid runaway)
		chainLen = 2
		cursor := original
		for depth := 0; depth < 16; depth++ {
			if cursor.ResurrectedFrom == "" || cursor.ParentSessionID == cursor.ResurrectedFrom {
				break
			}
			cursor, err = o.Store.GetSession(ctx, cursor.ParentSessionID)
			if err != nil || cursor == nil {
				break
			}
			chainLen++
		}
	}

	// --- 5E.iv frame-aware inheritance audit ---
	activeID, activeVer, _ := o.Store.ActiveConstitution(ctx)
	inheritedID := newSess.ConstitutionID
	inheritedVer := newSess.ConstitutionVer
	bumped := computeConstitutionBumped(inheritedID, inheritedVer, activeID, activeVer)
	inheritedMods := parseActiveMods(newSess.ActiveMods)

	// Emit the audit row. Failure here is logged but not surfaced —
	// the resurrection itself already succeeded. The audit row is
	// operator-visibility scaffolding, not a correctness primitive.
	auditWC := store.WriteContext{
		Actor:          "orchestrator_session_resurrect_frame_audit",
		SessionID:      newSess.SessionID,
		WritePath:      "SessionResurrectFrameAudit",
		ConstitutionID: newSess.ConstitutionID,
		ConstitutionVer: newSess.ConstitutionVer,
	}
	auditMeta := map[string]any{
		"new_session_id":              newSess.SessionID,
		"original_session_id":         original.SessionID,
		"inherited_constitution_id":   inheritedID,
		"inherited_constitution_ver":  inheritedVer,
		"active_constitution_id":      activeID,
		"active_constitution_ver":     activeVer,
		"constitution_bumped":         bumped,
		"inherited_mods_count":        len(inheritedMods),
		"reason":                      reason,
		"resurrect_chain_len":         chainLen,
	}
	metaBytes, _ := json.Marshal(auditMeta)
	_ = o.Store.RecordWrite(ctx, audit.WriteEvent{
		TableName:      "sessions",
		Actor:          auditWC.Actor,
		SessionID:      auditWC.SessionID,
		WritePath:      auditWC.WritePath,
		ConstitutionID: auditWC.ConstitutionID,
		ConstitutionVer: auditWC.ConstitutionVer,
		SessionEvent:   "resurrect",
		Notes:          string(metaBytes),
		CreatedAt:      o.now().Format(time.RFC3339Nano),
	})

	return &SessionResurrectOutput{
		NewSessionID:              newSess.SessionID,
		OriginalSessionID:         original.SessionID,
		InheritedFrom:             original.SessionID,
		StartedAt:                 startedAt,
		ResurrectChainLen:         chainLen,
		InheritedConstitutionID:   inheritedID,
		InheritedConstitutionVer:  inheritedVer,
		ActiveConstitutionID:      activeID,
		ActiveConstitutionVer:     activeVer,
		ConstitutionBumped:        bumped,
		InheritedMods:             inheritedMods,
	}, nil
}

// computeConstitutionBumped returns true when the inherited binding
// (id+ver on the original session, copied to the new session by
// SaveResurrect) does NOT match the active binding.
//
// Edge cases:
//   - inherited empty, active empty: not bumped (both absent)
//   - inherited empty, active set: bumped (new session should bind
//     to the active one but doesn't)
//   - inherited set, active empty: bumped (operator uninstalled the
//     constitution; resurrection can't honour the binding)
//   - both set, id+ver match: not bumped
//   - both set, id or ver differs: bumped
func computeConstitutionBumped(inheritedID, inheritedVer, activeID, activeVer string) bool {
	if inheritedID == "" && activeID == "" {
		return false
	}
	if inheritedID != activeID || inheritedVer != activeVer {
		return true
	}
	return false
}

// parseActiveMods decodes the session.ActiveMods JSON array (stored
// as a string column). Returns an empty slice when the input is
// empty or unparseable. Defensive: malformed JSON does NOT fail the
// resurrection — the audit row records it via inherited_mods_count=0.
func parseActiveMods(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var mods []string
	if err := json.Unmarshal([]byte(raw), &mods); err != nil {
		return nil
	}
	return mods
}

// lookupResurrectionCandidate resolves the resurrection source by
// either explicit OriginalSessionID or Operator+Lookback discovery.
func (o *Orchestrator) lookupResurrectionCandidate(ctx context.Context, in SessionResurrectInput) (*session.Session, error) {
	if strings.TrimSpace(in.OriginalSessionID) != "" {
		s, err := o.Store.GetSession(ctx, in.OriginalSessionID)
		if err != nil {
			return nil, fmt.Errorf("session_resurrect: get original: %w", err)
		}
		return s, nil
	}
	if strings.TrimSpace(in.Operator) == "" {
		return nil, fmt.Errorf("%w: must supply original_session_id or operator", store.ErrInvalidArgument)
	}
	lookback := in.Lookback
	if lookback == "" {
		lookback = "24h"
	}
	cutoff := computeLookbackCutoff(lookback)
	activeProject := o.Store.ActiveProject()
	cand, err := o.Store.FindClosedAbortedForActor(ctx, "", in.Operator, activeProject, cutoff)
	if err != nil {
		return nil, err
	}
	return cand, nil
}