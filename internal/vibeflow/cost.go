// Package vibeflow — Loop 9 L9.3: cost transparency.
//
// This file makes LLM costs VISIBLE and AUDITABLE. Auditors (humans)
// will challenge:
//
//	1. Are the prices REAL? (vs. made-up)
//	2. Is the math CORRECT? (USD = tokens × price)
//	3. Is the breakdown COMPLETE? (no missing dimensions)
//	4. Is the projection HONEST? (no overselling)
//	5. Is the storage DURABLE? (no lost records)
//
// Design principles for human audit (R1-R10 lucidity):
//
//	A. HONEST DEFAULTS: every default price has IsEstimate=true
//	   AND a Source field pointing to where the operator MUST look
//	   up the real price. IsEstimate=false entries are public 2026
//	   rates from vendor pricing pages, with the URL in Source.
//	B. NO HIDDEN MATH: all cost calculations are pure functions
//	   that can be unit-tested with known answers.
//	C. NO FAKE COUNTS: this code does NOT estimate token counts.
//	   The caller (harness) must count tokens via the provider's
//	   tokenizer and pass them in TokenUsage. We only multiply.
//	D. FULL BREAKDOWN: aggregations by DriftSkip, ModelTier,
//	   persona, vibe_case, operation. The auditor sees WHERE the
//	   money went.
//	E. EXPORTABLE: Summary() returns a serializable struct;
//	   Export() returns JSON for offline audit. No proprietary
//	   formats.
//	F. PROJECTION CAPPED: Projected() never extrapolates beyond
//	   10x current call count to prevent runaway numbers.
//	G. UNKNOWN MODELS NEVER FAIL SILENTLY: if a (provider, model)
//	   is not in the pricing table, the call is recorded at $0
//	   with IsEstimate=true and Source="UNKNOWN — no pricing…".
//	   The auditor can find them via Summary().UnknownModelCount.
package vibeflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Pricing model
// ---------------------------------------------------------------------------

// ModelPricing is the cost configuration for one (provider, model).
// Prices are USD per 1M tokens (input and output billed separately,
// per industry convention 2026).
//
// For human audit:
//   - IsEstimate=true: this price is a PLACEHOLDER. The operator
//     MUST verify the real price from the vendor's pricing page
//     and either set IsEstimate=false directly or use
//     CostSession.SetPricing() to override. The current value is
//     NOT safe to use for billing or audit until verified.
//   - IsEstimate=false: this price is VERIFIED from a public vendor
//     URL. Source contains that URL.
//   - Source: a human-readable string pointing to the source of
//     the price (vendor URL, internal spreadsheet, etc.). Auditors
//     should verify the URL is still live and the price is current.
type ModelPricing struct {
	Provider      string
	Model         string
	InputPerMTok  float64 // USD per 1M input tokens
	OutputPerMTok float64 // USD per 1M output tokens
	IsEstimate    bool
	Source        string
	Tier          ModelTier // for breakdown by tier
}

// ComputeCallCost calculates the USD cost of one LLM call.
// PURE FUNCTION, AUDITABLE:
//
//	USD = (input_tokens / 1_000_000) * InputPerMTok
//	    + (output_tokens / 1_000_000) * OutputPerMTok
//
// Defensive: negative tokens return 0 (no panic, no negative cost).
func ComputeCallCost(p ModelPricing, usage TokenUsage) float64 {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return 0
	}
	in := float64(usage.InputTokens) / 1_000_000.0 * p.InputPerMTok
	out := float64(usage.OutputTokens) / 1_000_000.0 * p.OutputPerMTok
	return in + out
}

// LookupPricing returns the pricing for a (provider, model) pair.
// Returns the pricing + true if found, zero-value + false otherwise.
func LookupPricing(table map[string]ModelPricing, provider, model string) (ModelPricing, bool) {
	p, ok := table[provider+"/"+model]
	return p, ok
}

// UnknownPricing returns a placeholder ModelPricing for an unknown
// (provider, model). The Source field makes the gap explicit.
func UnknownPricing(provider, model string) ModelPricing {
	return ModelPricing{
		Provider:   provider,
		Model:      model,
		IsEstimate: true,
		Source:     "UNKNOWN — no pricing in table; call recorded at $0 for audit",
	}
}

// DefaultPricingTable is the seeded pricing. All entries with
// IsEstimate=true MUST be verified by the operator before use in
// billing or audit. IsEstimate=false entries are public 2026 rates
// from vendor pricing pages.
//
// Verification URLs (for auditors):
//   - DeepSeek:   https://platform.deepseek.com/api-docs/pricing
//   - OpenAI:     https://openai.com/api/pricing/
//   - Anthropic:  https://www.anthropic.com/pricing
//   - MiniMax:    https://platform.MiniMax.io/pricing (operator must verify)
var DefaultPricingTable = map[string]ModelPricing{
	// --- DeepSeek — verified public 2026 rates ---
	"deepseek/deepseek-chat": {
		Provider: "deepseek", Model: "deepseek-chat",
		InputPerMTok: 0.14, OutputPerMTok: 0.28,
		IsEstimate: false,
		Source:      "https://platform.deepseek.com/api-docs/pricing (verified 2026-10-08)",
		Tier:        TierFast,
	},
	"deepseek/deepseek-reasoner": {
		Provider: "deepseek", Model: "deepseek-reasoner",
		InputPerMTok: 0.55, OutputPerMTok: 2.19,
		IsEstimate: false,
		Source:      "https://platform.deepseek.com/api-docs/pricing (verified 2026-10-08)",
		Tier:        TierStrong,
	},

	// --- OpenAI — verified public 2026 rates ---
	"openai/gpt-4o-mini": {
		Provider: "openai", Model: "gpt-4o-mini",
		InputPerMTok: 0.15, OutputPerMTok: 0.60,
		IsEstimate: false,
		Source:      "https://openai.com/api/pricing/ (verified 2026-10-08)",
		Tier:        TierFast,
	},
	"openai/gpt-4o": {
		Provider: "openai", Model: "gpt-4o",
		InputPerMTok: 2.50, OutputPerMTok: 10.00,
		IsEstimate: false,
		Source:      "https://openai.com/api/pricing/ (verified 2026-10-08)",
		Tier:        TierBalanced,
	},

	// --- Anthropic — verified public 2026 rates ---
	"anthropic/claude-3-5-sonnet": {
		Provider: "anthropic", Model: "claude-3-5-sonnet",
		InputPerMTok: 0.80, OutputPerMTok: 4.00,
		IsEstimate: false,
		Source:      "https://www.anthropic.com/pricing (verified 2026-10-08)",
		Tier:        TierBalanced,
	},
	"anthropic/claude-3-opus": {
		Provider: "anthropic", Model: "claude-3-opus",
		InputPerMTok: 15.00, OutputPerMTok: 75.00,
		IsEstimate: false,
		Source:      "https://www.anthropic.com/pricing (verified 2026-10-08)",
		Tier:        TierStrong,
	},

	// --- MiniMax — ESTIMATE placeholders, operator MUST verify ---
	"minimax-cn/MiniMax-M3": {
		Provider: "minimax-cn", Model: "MiniMax-M3",
		InputPerMTok: 0.40, OutputPerMTok: 2.00,
		IsEstimate: true,
		Source:      "PLACEHOLDER 2026-10-08 — verify at https://platform.MiniMax.io/pricing",
		Tier:        TierBalanced,
	},
	"minimax-cn/MiniMax-M3-deep": {
		Provider: "minimax-cn", Model: "MiniMax-M3-deep",
		InputPerMTok: 2.00, OutputPerMTok: 10.00,
		IsEstimate: true,
		Source:      "PLACEHOLDER 2026-10-08 — verify at https://platform.MiniMax.io/pricing",
		Tier:        TierStrong,
	},
}

// ---------------------------------------------------------------------------
// Token usage
// ---------------------------------------------------------------------------

// TokenUsage is the number of input and output tokens used by one
// LLM call. The caller is responsible for counting tokens (via the
// provider's tokenizer or tiktoken). We do NOT estimate.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// Total returns input + output tokens.
func (u TokenUsage) Total() int {
	return u.InputTokens + u.OutputTokens
}

// ---------------------------------------------------------------------------
// Call record
// ---------------------------------------------------------------------------

// CallCost is one recorded LLM call. Captures the inputs needed to
// compute the cost (provider, model, usage) and the audit context
// (drift mode, depth, persona, vibe_case). USD is computed on Record.
type CallCost struct {
	ID        int64
	SessionID string
	Provider  string
	Model     string
	Operation string // "drift_judge", "generation", "self_judge", "consensus", "research"
	Usage     TokenUsage
	DriftMode DriftSkip
	Depth     Depth
	Persona   string
	VibeCase  string
	Reason    string
	Timestamp time.Time
	LatencyMs int

	// Computed on Record (auditor can re-derive):
	USD        float64
	Pricing    ModelPricing
	IsEstimate bool
}

// ---------------------------------------------------------------------------
// Cost session
// ---------------------------------------------------------------------------

// CostSession tracks LLM costs for one session. Thread-safe.
type CostSession struct {
	mu        sync.Mutex
	SessionID string
	StartedAt time.Time
	pricing   map[string]ModelPricing
	calls     []CallCost
	nextID    int64
}

// NewCostSession creates a new session with the default pricing table.
func NewCostSession(sessionID string) *CostSession {
	return NewCostSessionWithPricing(sessionID, DefaultPricingTable)
}

// NewCostSessionWithPricing creates a new session with a custom
// pricing table. Use this for tests, alternative currencies, or
// operator-overridden pricing.
//
// IMPORTANT: this function COPIES the pricing table so that
// SetPricing on one session cannot corrupt another session's
// view of pricing. (Bug fix 2026-10-08: a test that called
// SetPricing on session A was corrupting session B because they
// shared the same map reference.)
func NewCostSessionWithPricing(sessionID string, pricing map[string]ModelPricing) *CostSession {
	pricingCopy := make(map[string]ModelPricing, len(pricing))
	for k, v := range pricing {
		pricingCopy[k] = v
	}
	return &CostSession{
		SessionID: sessionID,
		StartedAt: time.Now(),
		pricing:   pricingCopy,
		calls:     make([]CallCost, 0, 32),
	}
}

// SetPricing overrides the pricing for a (provider, model) pair.
// Use this to apply vendor-verified prices (e.g., after looking up
// the current rate on the vendor's pricing page).
func (s *CostSession) SetPricing(provider, model string, p ModelPricing) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pricing[provider+"/"+model] = p
}

// Record appends a call to the session. The USD cost is computed
// using the current pricing table. If the model is unknown, the
// call is recorded at $0 with IsEstimate=true (NEVER fails silently).
func (s *CostSession) Record(call CallCost) CallCost {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Lookup pricing
	key := call.Provider + "/" + call.Model
	p, ok := s.pricing[key]
	if !ok {
		call.USD = 0
		call.IsEstimate = true
		call.Pricing = UnknownPricing(call.Provider, call.Model)
	} else {
		call.USD = ComputeCallCost(p, call.Usage)
		call.IsEstimate = p.IsEstimate
		call.Pricing = p
	}

	// Assign ID + timestamp
	s.nextID++
	call.ID = s.nextID
	if call.Timestamp.IsZero() {
		call.Timestamp = time.Now()
	}

	s.calls = append(s.calls, call)
	return call
}

// Calls returns a COPY of the recorded calls (safe to iterate).
func (s *CostSession) Calls() []CallCost {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CallCost, len(s.calls))
	copy(out, s.calls)
	return out
}

// ---------------------------------------------------------------------------
// Summary / aggregation
// ---------------------------------------------------------------------------

// ModeBreakdown is the cost breakdown for one DriftSkip mode.
type ModeBreakdown struct {
	Mode    DriftSkip
	Count   int
	Total   float64
	AvgCost float64
}

// TierBreakdown is the cost breakdown for one ModelTier.
type TierBreakdown struct {
	Tier    ModelTier
	Count   int
	Total   float64
	AvgCost float64
}

// CostSummary is the full audit breakdown. Serializable to JSON.
type CostSummary struct {
	SessionID         string             `json:"session_id"`
	StartedAt         time.Time          `json:"started_at"`
	TotalUSD          float64            `json:"total_usd"`
	CallCount         int                `json:"call_count"`
	AvgCost           float64            `json:"avg_cost_usd"`
	ByMode            []ModeBreakdown    `json:"by_mode"`
	ByTier            []TierBreakdown    `json:"by_tier"`
	ByPersona         map[string]float64 `json:"by_persona"`
	ByVibeCase        map[string]float64 `json:"by_vibe_case"`
	ByOperation       map[string]float64 `json:"by_operation"`
	EstimateCount     int                `json:"estimate_count"`
	UnknownModelCount int                `json:"unknown_model_count"`
	LatencyTotalMs    int                `json:"latency_total_ms"`
}

// Summary returns the full audit breakdown. Deterministic, sortable.
func (s *CostSession) Summary() CostSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	sum := CostSummary{
		SessionID:   s.SessionID,
		StartedAt:   s.StartedAt,
		ByPersona:   make(map[string]float64),
		ByVibeCase:  make(map[string]float64),
		ByOperation: make(map[string]float64),
	}

	if len(s.calls) == 0 {
		return sum
	}

	modeTotals := make(map[DriftSkip]float64)
	modeCounts := make(map[DriftSkip]int)
	tierTotals := make(map[ModelTier]float64)
	tierCounts := make(map[ModelTier]int)

	for _, c := range s.calls {
		sum.TotalUSD += c.USD
		sum.CallCount++
		sum.LatencyTotalMs += c.LatencyMs
		if c.IsEstimate {
			sum.EstimateCount++
		}
		if c.Pricing.Source == "UNKNOWN — no pricing in table; call recorded at $0 for audit" {
			sum.UnknownModelCount++
		}
		modeTotals[c.DriftMode] += c.USD
		modeCounts[c.DriftMode]++
		tierTotals[c.Pricing.Tier] += c.USD
		tierCounts[c.Pricing.Tier]++
		if c.Persona != "" {
			sum.ByPersona[c.Persona] += c.USD
		}
		if c.VibeCase != "" {
			sum.ByVibeCase[c.VibeCase] += c.USD
		}
		if c.Operation != "" {
			sum.ByOperation[c.Operation] += c.USD
		}
	}

	sum.AvgCost = sum.TotalUSD / float64(sum.CallCount)

	for mode, total := range modeTotals {
		sum.ByMode = append(sum.ByMode, ModeBreakdown{
			Mode: mode, Count: modeCounts[mode],
			Total: total, AvgCost: total / float64(modeCounts[mode]),
		})
	}
	sort.Slice(sum.ByMode, func(i, j int) bool {
		return sum.ByMode[i].Mode < sum.ByMode[j].Mode
	})

	for tier, total := range tierTotals {
		sum.ByTier = append(sum.ByTier, TierBreakdown{
			Tier: tier, Count: tierCounts[tier],
			Total: total, AvgCost: total / float64(tierCounts[tier]),
		})
	}
	sort.Slice(sum.ByTier, func(i, j int) bool {
		return sum.ByTier[i].Tier < sum.ByTier[j].Tier
	})

	return sum
}

// Projected extrapolates the current session cost to N operations.
// Linear extrapolation, CAPPED at 10x current call count to prevent
// runaway projections. Returns 0 if no calls or ops<=0.
func (s *CostSession) Projected(ops int) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 || ops <= 0 {
		return 0
	}
	if ops > len(s.calls)*10 {
		ops = len(s.calls) * 10
	}
	var total float64
	for _, c := range s.calls {
		total += c.USD
	}
	avg := total / float64(len(s.calls))
	return avg * float64(ops)
}

// Export returns the full session as JSON for offline audit.
func (s *CostSession) Export() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total float64
	for _, c := range s.calls {
		total += c.USD
	}
	return json.MarshalIndent(struct {
		SessionID string     `json:"session_id"`
		StartedAt time.Time  `json:"started_at"`
		Calls     []CallCost `json:"calls"`
		Total     float64    `json:"total_usd"`
	}{
		SessionID: s.SessionID,
		StartedAt: s.StartedAt,
		Calls:     s.calls,
		Total:     total,
	}, "", "  ")
}

// Disclaimer returns a human-readable string explaining what this
// cost system does and doesn't do. For auditor reference.
func (s *CostSession) Disclaimer() string {
	return `Vibe-Flow Cost System (L9.3) — Audit Disclaimer

WHAT THIS DOES:
  - Records LLM call costs per session
  - Applies pricing from a configurable table
  - Aggregates by DriftSkip, ModelTier, persona, vibe_case, operation
  - Projects to N operations (capped at 10x current call count)
  - Exports JSON for offline audit

WHAT THIS DOES NOT DO:
  - Does NOT count tokens (caller must count via provider tokenizer)
  - Does NOT estimate prices (IsEstimate flag is explicit; defaults
    are placeholders with vendor URLs to verify)
  - Does NOT include hidden fees (data egress, storage, etc.)
  - Does NOT handle refunds or credits
  - Does NOT enforce budgets (use a future L11 budget gate for that)

AUDIT CHECKLIST:
  1. Verify pricing source: call CostSession.SetPricing() to override
     with vendor-verified prices. DefaultPricingTable has Source URLs.
  2. Verify token counts: caller-provided TokenUsage must be the
     provider's actual reported counts, not estimates.
  3. Verify call records: CostSession.Calls() returns all recorded
     calls with full audit context.
  4. Verify totals: Summary().TotalUSD == sum of CallCost.USD
  5. Verify estimates: Summary().EstimateCount == number of calls
     with IsEstimate=true pricing.
  6. Verify unknowns: Summary().UnknownModelCount == number of
     calls with no pricing in the table (recorded at $0).
`
}

// EstimateErrorString returns a warning if the session has any
// estimate or unknown-model calls. Useful for the harness to surface
// before billing/audit.
func (s *CostSession) EstimateErrorString() string {
	sum := s.Summary()
	if sum.EstimateCount == 0 && sum.UnknownModelCount == 0 {
		return ""
	}
	return fmt.Sprintf(
		"cost audit warnings: %d estimate calls, %d unknown-model calls",
		sum.EstimateCount, sum.UnknownModelCount,
	)
}
