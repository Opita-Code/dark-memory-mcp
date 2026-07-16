// Package vlp (continued) — atomic spec 2.4 (VLPAuditor).
//
// The Auditor emits a write_audit row on EVERY state transition (INV-1).
// This is the TRANSITION-LEVEL audit — distinct from Persistence.Save's
// row-level audit (spec 2.3, fixed in C4). The two layers together form
// the full provenance chain:
//
//   - row-level:    "vlp_state row id N was written at time T by actor A"
//   - transition:   "session S went from F to T on event E with verdict V at turn K"
//
// Forensic use case: given a session_id, list all write_audit rows ordered
// by created_at; the row-level rows tell you which vlp_state rows existed
// when; the transition-level rows tell you WHY each row changed.
//
// Atomicity caveat: RecordTransition is best-effort with the caller's
// transaction boundary. Standalone (the typical case in spec 2.5
// VLPLoopUseCase), the audit row commits independently. If the caller is
// in the middle of a multi-write transaction and that tx rolls back, the
// audit row also rolls back. Future spec (TxContext) can introduce a
// tx-aware variant; for now callers that need atomic-with-Save should
// add a transactional SaveVLPState variant to the Store interface.
//
// Trust boundary: Auditor trusts caller-supplied (from, event, verdict,
// to). It does NOT verify the transition is valid (that is
// vlp.Transition's job in spec 2.1) and does NOT verify the new state
// was persisted (that is Persistence.Save's job in spec 2.3). The Auditor
// just records what it's told. INV-1 is satisfied by the guarantee that
// the caller (spec 2.5 VLPLoopUseCase) is the single point that drives
// the loop, so every Save is paired with exactly one RecordTransition.
//
// Atomicity contract:
//   - ONE interface: Auditor (RecordTransition / ListTransitionsForSession)
//   - ONE acceptance test: TestVLPAuditor_AuditOnEachTransition
//   - ONE PR worth of work (~140 LoC impl + ~280 LoC tests)
//   - Direct deps: 2.1 (State/Event/Verdict enums) + Layer 0 (Store)
//   - Independently reviewable: no other v1.1 spec touched
package vlp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/audit"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// TransitionRecord is the structured payload for a state transition.
// Serialized to JSON in write_audit.notes for forensic reconstruction.
// All four core fields are required; Verdict is omitempty so non-drift
// events (session_start, vibe_publish, artifact_log, abort) carry no
// verdict noise.
type TransitionRecord struct {
	From    State   `json:"from"`
	Event   Event   `json:"event"`
	Verdict Verdict `json:"verdict,omitempty"`
	To      State   `json:"to"`
	Turn    int     `json:"turn"`
}

// transitionAuditPath is the canonical write_path string used by
// ListTransitionsForSession to filter write_audit rows down to
// transition-level events only. Distinct from the row-level audit
// emitted by SaveVLPState (which uses write_path="SaveVLPState").
const transitionAuditPath = "vlp.transition"

// transitionAuditTable is the table_name recorded for transition-level
// audit rows. Same as the data table (vlp_state) since transitions
// modify that table — keeps ops queries "show me everything that
// touched vlp_state" comprehensive.
const transitionAuditTable = "vlp_state"

// Auditor emits a write_audit row per VLP state transition (INV-1).
type Auditor struct {
	store store.Store
}

// NewAuditor returns an Auditor backed by the given Store. Returns
// error if s is nil — same defensive check as NewPersistence so the
// failure surface is uniform.
func NewAuditor(s store.Store) (*Auditor, error) {
	if s == nil {
		return nil, fmt.Errorf("vlp: NewAuditor: store must not be nil")
	}
	return &Auditor{store: s}, nil
}

// RecordTransition emits a write_audit row capturing this transition.
// Returns error if sessionID is empty or the underlying Store fails.
// The caller is expected to have already validated the transition via
// vlp.Transition(from, event, verdict) and persisted the new state via
// vlp.Persistence.Save.
//
// The audit row is identifiable by:
//   - table_name = "vlp_state"
//   - write_path = "vlp.transition"
//   - row_id     = 0 (no specific data row; the audit tracks the LOGICAL event)
//   - notes      = JSON-serialized transition (self-describing for ops queries)
func (a *Auditor) RecordTransition(ctx context.Context, wc store.WriteContext, sessionID string, rec TransitionRecord) error {
	if sessionID == "" {
		return fmt.Errorf("vlp: auditor.RecordTransition: sessionID is required")
	}
	notes := marshalTransitionNotes(sessionID, rec)
	return a.store.RecordWrite(ctx, audit.WriteEvent{
		TableName:       transitionAuditTable,
		RowID:           0,
		Actor:           wc.Actor,
		SessionID:       sessionID,
		WritePath:       transitionAuditPath,
		ConstitutionID:  wc.ConstitutionID,
		ConstitutionVer: wc.ConstitutionVer,
		Notes:           notes,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// ListTransitionsForSession returns all transition-level audit rows
// for a session, newest-first. limit <= 0 means no limit (caller is
// responsible for result-set size).
//
// Filtering is at the Store layer via audit.ListFilters{WritePath:
// "vlp.transition", SessionID: ...} — no client-side filtering needed.
func (a *Auditor) ListTransitionsForSession(ctx context.Context, sessionID string, limit int) ([]audit.WriteEvent, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("vlp: auditor.ListTransitionsForSession: sessionID is required")
	}
	if limit <= 0 {
		limit = 50
	}
	return a.store.ListWrites(ctx, audit.ListFilters{
		SessionID: sessionID,
		WritePath: transitionAuditPath,
		Limit:     limit,
	})
}

// marshalTransitionNotes serializes the transition record + session id
// into the JSON payload stored in write_audit.notes. The session_id is
// embedded in the JSON even though write_audit also has a session_id
// column — this makes the notes self-describing for ops queries that
// only look at the notes column (e.g. `sqlite3 dark.db "select notes
// from write_audit where notes like '%vibe_publish%'"`).
func marshalTransitionNotes(sessionID string, rec TransitionRecord) string {
	payload := struct {
		SessionID string   `json:"session_id"`
		From      State    `json:"from"`
		Event     Event    `json:"event"`
		Verdict   Verdict  `json:"verdict,omitempty"`
		To        State    `json:"to"`
		Turn      int      `json:"turn"`
	}{
		SessionID: sessionID,
		From:      rec.From,
		Event:     rec.Event,
		Verdict:   rec.Verdict,
		To:        rec.To,
		Turn:      rec.Turn,
	}
	b, _ := json.Marshal(payload)
	return string(b)
}