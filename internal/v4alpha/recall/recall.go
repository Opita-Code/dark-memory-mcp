package recall

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/agent_memory"
)

// RecallFor is the Phase 5 polymorphic dispatch entry point. It
// routes the query to the strategy registered for vibeCase and
// returns the top-K AnnotatedRow matches.
//
// Parameters:
//
//   - ctx:        request context (cancellation propagates).
//   - db:         the shared dark-db *sql.DB. The function does NOT
//                 manage transactions — every strategy is a single
//                 read-only query plan.
//   - vibeCase:   one of the canonical 7 (code/text/decision/research/
//                 video/audio/multi). Validated via IsValidVibeCase;
//                 unknown values return ErrUnknownVibeCase.
//   - query:      the natural-language query. Strategies tokenize
//                 differently (FTS5 bm25, vector cosine, ADR regex),
//                 but every strategy treats empty query as a no-op
//                 (returns nil, nil).
//   - projectID:  INV-19 namespace primitive (alpha.17). Empty
//                 projectID is treated as "default" to match the
//                 alpha.17 column DEFAULT.
//   - topK:       maximum results. 0 → 10.
//
// Returns AnnotatedRow slices, NOT plain agent_memory.Row, because
// strategies surface Phase 5 columns (decay, embedding, adr_refs,
// rationale, etc.) that the legacy Row struct does not carry.
func RecallFor(ctx context.Context, db *sql.DB, vibeCase, query, projectID string, topK int) ([]AnnotatedRow, error) {
	if db == nil {
		return nil, fmt.Errorf("recall: db is nil")
	}
	if !IsValidVibeCase(vibeCase) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownVibeCase, vibeCase)
	}
	if projectID == "" {
		projectID = "default"
	}
	if topK <= 0 {
		topK = 10
	}
	strategy, ok := strategyRegistry[vibeCase]
	if !ok {
		return nil, fmt.Errorf("%w: %q (alpha.18 ships C3 only; C1/C2/C4/C5/C6/C7 ship in chunks 3-4)",
			ErrNoStrategyRegistered, vibeCase)
	}
	return strategy.Recall(ctx, db, query, projectID, topK)
}

// ErrUnknownVibeCase is returned when the caller passes a vibe_case
// that is not in the canonical allow-list (per types.go).
var ErrUnknownVibeCase = errors.New("recall: unknown vibe_case")

// ErrNoStrategyRegistered is returned when a valid vibe_case has no
// strategy registered yet. In alpha.18, only C3 (decision) is wired;
// the other 6 ship in Chunks 3-4 (per SPEC-alpha-11-phase5.md §6).
var ErrNoStrategyRegistered = errors.New("recall: no strategy registered for vibe_case")

// RecallStrategy is the polymorphic interface that every vibe-case
// strategy implements. One concrete type per vibe_case (C1..C7).
//
// The interface is small on purpose: a strategy receives a query +
// project scope and returns ranked AnnotatedRow. Weights, decay,
// graph expansion, and embedding integration are implementation
// details per strategy.
type RecallStrategy interface {
	// VibeCase returns the canonical vibe_case identifier
	// (e.g. "decision" for C3DecisionRecall). Used for registry
	// dispatch and self-identification in error messages.
	VibeCase() string
	// Weights returns the per-strategy blend of signals.
	// Sum must equal 1.0; Weights.Validate() enforces this.
	Weights() Weights
	// Recall returns up to topK ranked rows.
	Recall(ctx context.Context, db *sql.DB, query, projectID string, topK int) ([]AnnotatedRow, error)
}

// Weights is the per-strategy blend of signals. The sum of all four
// fields must equal 1.0 (±1e-6 tolerance for float arithmetic).
//
// Per SPEC §3 the canonical weight matrix is:
//
//	C1 code     : FTS5=0.55, Graph=0.45
//	C2 text     : FTS5=0.40, Vector=0.50, Graph=0.10
//	C3 decision : FTS5=0.30, Graph=0.70
//	C4 research : FTS5=0.25, Vector=0.45, Graph=0.30
//	C5 video    : Graph=0.20, CrossModal=0.80
//	C6 audio    : Graph=0.20, CrossModal=0.80
//	C7 multi    : ensemble (subtask-specific)
type Weights struct {
	FTS5       float64
	Vector     float64
	Graph      float64
	CrossModal float64
}

// Validate returns nil if the weights sum to 1.0 ± 1e-6.
//
// Strategies should call this in their constructor (or a constructor
// test) so a typo in the weight literal fails fast at package init
// rather than silently mis-ranking at recall time.
func (w Weights) Validate() error {
	sum := w.FTS5 + w.Vector + w.Graph + w.CrossModal
	if math.Abs(sum-1.0) > 1e-6 {
		return fmt.Errorf("recall: Weights sum to %v, want 1.0", sum)
	}
	return nil
}

// strategyRegistry maps vibe_case identifier → RecallStrategy. Populated
// in init() below. Reading is goroutine-safe (Go map reads are safe
// when the map is not being written); writers (RegisterStrategy) are
// only called from init() so no lock is needed.
var strategyRegistry = map[string]RecallStrategy{}

// RegisterStrategy adds s to the registry under s.VibeCase().
// Re-registering replaces the prior strategy. Intended for init()
// use; calling from goroutines is undefined.
func RegisterStrategy(s RecallStrategy) {
	strategyRegistry[s.VibeCase()] = s
}

// RegisteredVibeCases returns the sorted list of vibe_cases that
// have a registered strategy. Useful for diagnostics + tests.
func RegisteredVibeCases() []string {
	out := make([]string, 0, len(strategyRegistry))
	for k := range strategyRegistry {
		out = append(out, k)
	}
	// stable sort for predictable diagnostics
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// init registers the canonical strategies. C3 (decision) is the
// first-implemented per SPEC §15 (sign-off order). C1/C2/C4/C5/C6/C7
// land in Chunks 3-4.
func init() {
	RegisterStrategy(&C3DecisionRecall{})
}

// annotatedColumns is the canonical column list for SELECT statements
// in strategies. Keep in sync with AnnotatedRow's embedded Row plus
// the 20 Phase 5 columns. Strategies use scanAnnotatedRows to materialise.
//
// Order matters: the scan function reads columns positionally. Adding
// a column here requires adding a field to AnnotatedRow AND updating
// scanAnnotatedRows in lockstep.
const annotatedColumns = `
	m.id, m.operator, m.kind, COALESCE(m.title,''), m.content,
	COALESCE(m.tags,''), m.pinned, m.created_at,
	COALESCE(m.embedding, x''),
	m.embedding_model, COALESCE(m.embedding_dim, 0),
	m.embedding_created_at,
	m.vibe_case, m.voice_embed_kind,
	m.decay_class, m.decay_tau_days, m.last_refreshed_at,
	COALESCE(m.access_count, 0), COALESCE(m.refresh_on_access, 1),
	m.adr_refs, m.inv_refs, m.commit_hashes,
	m.extracted_residuals,
	m.rationale, m.supersedes_id,
	COALESCE(m.decision_state, 'active'),
	m.valid_from, m.valid_to`

// scanAnnotatedRows materialises rows from a Query that selects
// annotatedColumns. Strategies call this after their FTS5 / JOIN
// queries. Returns ErrNoRows-free slice (may be empty).
//
// Null handling: every Phase 5 column is nullable on the SQLite side
// (no NOT NULL constraint). Scan uses sql.Null* types where the Go
// counterpart doesn't have a natural zero-value semantics, then
// unwraps to the AnnotatedRow's plain-string / *int64 / *time.Time
// fields. The legacy embedded agent_memory.Row fields (id, operator,
// kind, title, content, tags, pinned, created_at) are NOT NULL on the
// base table so plain Go types are safe there.
func scanAnnotatedRows(rows *sql.Rows) ([]AnnotatedRow, error) {
	out := []AnnotatedRow{}
	for rows.Next() {
		var r AnnotatedRow
		var pinned int
		var createdAt string
		var refreshOnAccess int
		var embedding []byte
		var embeddingModel sql.NullString
		var embeddingDim sql.NullInt64
		var embeddingCreatedAt sql.NullString
		var vibeCase sql.NullString
		var voiceEmbedKind sql.NullString
		var decayClass sql.NullString
		var decayTauDays sql.NullInt64
		var lastRefreshedAt sql.NullString
		var accessCount sql.NullInt64
		var adrRefs, invRefs, commitHashes sql.NullString
		var extractedResiduals sql.NullString
		var rationale sql.NullString
		var supersedesID sql.NullInt64
		var decisionState sql.NullString
		var validFrom, validTo sql.NullString
		if err := rows.Scan(
			&r.ID, &r.Operator, &r.Kind, &r.Title, &r.Content,
			&r.Tags, &pinned, &createdAt,
			&embedding,
			&embeddingModel, &embeddingDim,
			&embeddingCreatedAt,
			&vibeCase, &voiceEmbedKind,
			&decayClass, &decayTauDays, &lastRefreshedAt,
			&accessCount, &refreshOnAccess,
			&adrRefs, &invRefs, &commitHashes,
			&extractedResiduals,
			&rationale, &supersedesID,
			&decisionState,
			&validFrom, &validTo,
		); err != nil {
			return nil, fmt.Errorf("recall scanAnnotatedRows: %w", err)
		}
		r.Pinned = pinned != 0
		r.RefreshOnAccess = refreshOnAccess != 0
		r.Embedding = embedding
		if embeddingModel.Valid {
			r.EmbeddingModel = embeddingModel.String
		}
		if embeddingDim.Valid {
			r.EmbeddingDim = int(embeddingDim.Int64)
		}
		if embeddingCreatedAt.Valid {
			if t, err := parseMemoryTimestamp(embeddingCreatedAt.String); err == nil {
				r.EmbeddingCreated = &t
			}
		}
		if vibeCase.Valid {
			r.VibeCase = vibeCase.String
		}
		if voiceEmbedKind.Valid {
			r.VoiceEmbedKind = voiceEmbedKind.String
		}
		if decayClass.Valid {
			r.DecayClass = decayClass.String
		}
		if decayTauDays.Valid {
			r.DecayTauDays = int(decayTauDays.Int64)
		}
		if lastRefreshedAt.Valid {
			if t, err := parseMemoryTimestamp(lastRefreshedAt.String); err == nil {
				r.LastRefreshedAt = &t
			}
		}
		if accessCount.Valid {
			r.AccessCount = int(accessCount.Int64)
		}
		if adrRefs.Valid {
			r.AdrRefs = adrRefs.String
		}
		if invRefs.Valid {
			r.InvRefs = invRefs.String
		}
		if commitHashes.Valid {
			r.CommitHashes = commitHashes.String
		}
		if extractedResiduals.Valid {
			r.ExtractedResiduals = extractedResiduals.String
		}
		if rationale.Valid {
			r.Rationale = rationale.String
		}
		if supersedesID.Valid {
			v := supersedesID.Int64
			r.SupersedesID = &v
		}
		if decisionState.Valid {
			r.DecisionState = decisionState.String
		}
		if validFrom.Valid {
			r.ValidFrom = validFrom.String
		}
		if validTo.Valid {
			r.ValidTo = validTo.String
		}
		// Parse created_at (matches agent_memory scan semantics:
		// try RFC3339Nano first, fall back to SQLite CURRENT_TIMESTAMP).
		t, err := parseMemoryTimestamp(createdAt)
		if err != nil {
			return nil, fmt.Errorf("recall scanAnnotatedRows created_at %q: %w", createdAt, err)
		}
		r.CreatedAt = t
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recall scanAnnotatedRows rows: %w", err)
	}
	return out, nil
}

// parseMemoryTimestamp accepts the two timestamp formats the schema
// produces: RFC3339Nano (set by code paths that write time.Time
// values) and SQLite's CURRENT_TIMESTAMP "YYYY-MM-DD HH:MM:SS". Both
// sort lexicographically the same, so direct string comparisons are
// safe (used by the agent_memory package's Since filter).
func parseMemoryTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// placeholders returns a comma-separated list of "?" placeholders of
// length n. Used by strategies that build dynamic IN clauses.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

// ensureC3 weights are valid at package load time. If a future refactor
// changes the C3 weights, init() will fail loudly rather than silently
// mis-rank decisions at runtime. Caught by TestC3DecisionRecall_WeightsValid.
var _ = (&C3DecisionRecall{}).Weights().Validate()

// Type assertion: C3DecisionRecall must satisfy RecallStrategy at compile time.
var _ RecallStrategy = (*C3DecisionRecall)(nil)

// Type assertion: AnnotatedRow must embed agent_memory.Row so the
// transport layer (Phase 4 contract) can read core fields.
// We construct a real AnnotatedRow instead of dereferencing nil so
// init() runs cleanly (the embedded struct field is a value, but
// Go's spec leaves accessing fields of nil pointers implementation-
// defined for value-type fields of nil pointer receivers).
var _ agent_memory.Row = AnnotatedRow{}.Row
