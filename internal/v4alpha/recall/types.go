package recall

import (
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// AnnotatedRow is the recall-side projection of one agent_memory row.
// It embeds agent_memory.Row (Phase 4 contract) and overlays the 20
// Phase 5 columns. The recall package owns this struct; agent_memory
// stays backwards-compatible.
//
// The decay fields (decay_class, decay_tau_days, last_refreshed_at,
// access_count, refresh_on_access) are populated on read; pre-Phase-5
// rows get auto-derived values via ApplyDefaultDecay.
//
// The embedding fields (embedding, embedding_model, embedding_dim,
// embedding_created_at) are populated by the embedder adapter; legacy
// rows have nil/zero values and are indexed lazily on first access.
type AnnotatedRow struct {
	agent_memory.Row
	// Embedding (R-B).
	Embedding         []byte  `json:"embedding,omitempty"`
	EmbeddingModel    string  `json:"embedding_model,omitempty"`
	EmbeddingDim      int     `json:"embedding_dim,omitempty"`
	EmbeddingCreated  *time.Time `json:"embedding_created_at,omitempty"`
	VibeCase          string  `json:"vibe_case,omitempty"`
	VoiceEmbedKind    string  `json:"voice_embed_kind,omitempty"`
	// Decay (R-C).
	DecayClass       string     `json:"decay_class,omitempty"`
	DecayTauDays     int        `json:"decay_tau_days,omitempty"`
	LastRefreshedAt  *time.Time `json:"last_refreshed_at,omitempty"`
	AccessCount      int        `json:"access_count,omitempty"`
	RefreshOnAccess  bool       `json:"refresh_on_access,omitempty"`
	// Code refs (R-E).
	AdrRefs      string `json:"adr_refs,omitempty"`
	InvRefs      string `json:"inv_refs,omitempty"`
	CommitHashes string `json:"commit_hashes,omitempty"`
	// Graph residuals (R-D).
	ExtractedResiduals string `json:"extracted_residuals,omitempty"`
	// Decision subsystem (R-F).
	Rationale      string `json:"rationale,omitempty"`
	SupersedesID   *int64 `json:"supersedes_id,omitempty"`
	DecisionState  string `json:"decision_state,omitempty"`
	ValidFrom      string `json:"valid_from,omitempty"`
	ValidTo        string `json:"valid_to,omitempty"`
}

// DecayClass values per R-C §4 I-1 (Fortunate Recall 10+1 ontology, simplified to 5).
// These map the canonical kind to a decay policy:
//
//	"forever"   — never decay. Use for permanent decisions (C3).
//	"persistent" — long half-life (τ ~3 years). Stable findings.
//	"stable"     — medium half-life (τ ~1 year). Observations, context.
//	"perishable" — short half-life (τ ~3 months). Notes, todos.
//	"instant"    — immediate decay (τ ~1 month). Links, references.
const (
	DecayClassForever   = "forever"
	DecayClassPersistent = "persistent"
	DecayClassStable    = "stable"
	DecayClassPerishable = "perishable"
	DecayClassInstant   = "instant"
)

// DefaultDecayForKind returns the canonical decay_class for the given
// agent_memory kind, per R-C §4 I-1 mapping. Returns ("", 0) if the
// kind is unknown (caller decides what to do).
//
// Mapping (alpha.18):
//   decision     → forever       (C3 retains decisions forever)
//   finding      → persistent    (3-year half-life)
//   observation  → stable        (1-year)
//   context      → stable        (1-year)
//   note         → perishable    (3-month)
//   todo         → perishable    (2-month, special-cased below)
//   link         → instant       (1-month)
func DefaultDecayForKind(kind string) (string, int) {
	switch kind {
	case "decision":
		return DecayClassForever, 0
	case "finding":
		return DecayClassPersistent, 1095
	case "observation":
		return DecayClassStable, 365
	case "context":
		return DecayClassStable, 365
	case "note":
		return DecayClassPerishable, 90
	case "todo":
		return DecayClassPerishable, 60
	case "link":
		return DecayClassInstant, 30
	default:
		return "", 0
	}
}

// VibeCase values per internal/v4alpha/vibe/spec.go:14-16. These are
// the canonical identifiers for the 7 RecallStrategy implementations.
//
// The mapping (from spec.go) is:
//   C1 = code, C2 = text, C3 = decision, C4 = research,
//   C5 = video, C6 = audio, C7 = multi.
//
// NOTE: persona-registry-v4.md and ADR-007 §persona-routing use a
// different mapping (C3=image, C4=video, C5=bundle, C6=infra,
// C7=governance). This inconsistency is documented in
// docs/research/phase-5/ as a Phase 5-prep fix candidate
// (~1 day, mapping correction only).
const (
	VibeCaseCode     = "code"
	VibeCaseText     = "text"
	VibeCaseDecision = "decision"
	VibeCaseResearch = "research"
	VibeCaseVideo    = "video"
	VibeCaseAudio    = "audio"
	VibeCaseMulti    = "multi"
)

// ValidVibeCases is the canonical allow-list of vibe_case values.
// Empty string is NOT in this list — operator must pick one.
var ValidVibeCases = map[string]struct{}{
	VibeCaseCode: {}, VibeCaseText: {}, VibeCaseDecision: {},
	VibeCaseResearch: {}, VibeCaseVideo: {}, VibeCaseAudio: {},
	VibeCaseMulti: {},
}

// IsValidVibeCase returns true if v is one of the 7 canonical
// vibe_case values. Empty string is NOT valid.
func IsValidVibeCase(v string) bool {
	_, ok := ValidVibeCases[v]
	return ok
}

// DecisionState values per R-F §4 I-2.
//
//	"active"      — current decision, returned by default RecallFor(C3).
//	"superseded"  — replaced by a newer decision (decision_transitions
//	                carries the trigger + reason + evidence).
//	"contested"   — under review / debate. alpha.18 manual only;
//	                alpha.19 auto-detect.
const (
	DecisionStateActive     = "active"
	DecisionStateSuperseded = "superseded"
	DecisionStateContested  = "contested"
)
