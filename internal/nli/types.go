// Package nli provides Natural Language Inference scoring as the canonical
// input to dark-memory's drift_judge (v2.20.0 T05, spec 1276).
//
// # Threat model
//
// The drift_judge previously took a caller-controlled "Content string"
// parameter (judge_consensus.go:47 in v2.19.x), which let any caller shape
// the verdict input. v2.20.0 fixes this: the judge resolves the artifact
// via artifact.Resolver (T01) and asks nli.Provider.Score(ctx, premise,
// hypothesis) — there is no free-form "Content" parameter anywhere in
// this package.
//
// # Hard invariants (immutable)
//
//   - Label is one of exactly 3 canonical values: entailment, contradiction,
//     neutral. No provider string leaks through; model-specific labels are
//     mapped via ProviderConfig.LabelMap.
//   - Score.Confidence is in [0, 1].
//   - Score.ProviderID is non-empty (provenance — never anonymous).
//   - Errors are sealed: ErrProviderTimeout, ErrProviderUnavailable,
//     ErrProviderBadResponse, ErrProviderRateLimited, ErrInputTooLarge,
//     ErrInputEmpty. Callers classify via errors.Is; no string matching.
//   - Score() does not log the auth token, the premise, or the hypothesis
//     at info level (those may contain caller data). Latency + label +
//     provider id are logged at debug.
//
// # Tunable parameters (per-project)
//
//   - MaxPremiseBytes (default 65536), MaxHypothesisBytes (default 8192).
//   - ProviderConfig.TimeoutMS (default 10000).
//   - ProviderConfig.LabelMap (default: DeBERTa-v3-large-mnli convention).
//   - MiniCheck thresholds for entailment / contradiction bands.
//   - Router.LatencyBudgetMS (default = ProviderConfig.TimeoutMS).
//
// # Package layout
//
//   - types.go      : Label, Score, Provider, Config, Error types (this file).
//   - deberta.go    : DeBERTa-v3-large-mnli HTTP client (HuggingFace Inference).
//   - minicheck.go  : MiniCheck HTTP client (self-hosted; binary classifier,
//                     mapped to 3-label via two thresholds).
//   - router.go     : Primary + fallback selection per Policy.
//   - *_test.go     : property-based + boundary + httptest.Server.
package nli

import (
	"context"
	"errors"
)

// Label is the canonical 3-way NLI label that the router and the rest of
// dark-memory reason about. Providers MUST map their model-specific
// strings (e.g. "LABEL_2", "ENTAILMENT", "supported") to one of these.
type Label string

const (
	LabelEntailment    Label = "entailment"
	LabelContradiction Label = "contradiction"
	LabelNeutral       Label = "neutral"
)

// Valid reports whether l is one of the canonical labels.
func (l Label) Valid() bool {
	switch l {
	case LabelEntailment, LabelContradiction, LabelNeutral:
		return true
	}
	return false
}

// Score is the canonical output of a single NLI call. ProviderID is the
// provenance field that the Merkle chain (T04) and the drift_judge
// consumer use to attribute the verdict.
type Score struct {
	Label      Label    // post-mapping: entailment | contradiction | neutral
	Confidence float64  // [0, 1]; 1 = model fully confident
	ProviderID string   // e.g. "deberta-v3-large-mnli", "minicheck-flan-t5-large"
	LatencyMS  int64    // observed wall-clock for this Score call
	ModelRev   string   // model version / commit sha (best-effort; empty if unknown)
}

// Valid reports whether s is a well-formed Score (no zero-value surprises).
func (s Score) Valid() bool {
	if !s.Label.Valid() {
		return false
	}
	if s.Confidence < 0 || s.Confidence > 1 {
		return false
	}
	if s.ProviderID == "" {
		return false
	}
	if s.LatencyMS < 0 {
		return false
	}
	return true
}

// Provider is the interface every NLI backend satisfies. Implementations:
// DeBERTaProvider (HuggingFace Inference), MiniCheckProvider (self-hosted),
// stub providers for tests.
//
// Score() must NOT panic on any input (including empty, oversized, or
// containing invalid UTF-8). It returns one of the sealed errors below
// when the call cannot produce a valid Score.
type Provider interface {
	// Score returns entailment/contradiction/neutral + confidence +
	// provenance. The caller (Router or judge_orchestration) is
	// responsible for retry / fallback; Score is one shot.
	Score(ctx context.Context, premise, hypothesis string) (Score, error)

	// ID returns the provider id (for logging, audit, config keys).
	ID() string
}

// Config is the project's NLI settings. Loaded from the active project's
// NLIConfig field (added in T05's wiring layer); defaults if absent.
type Config struct {
	Primary            ProviderConfig
	Fallback           ProviderConfig // zero-value if no fallback
	FallbackEnabled    bool
	LatencyBudgetMS    int64 // primary timeout; >budget triggers fallback if enabled
	MaxPremiseBytes    int   // hard cap; 0 → DefaultMaxPremiseBytes
	MaxHypothesisBytes int   // hard cap; 0 → DefaultMaxHypothesisBytes
}

// ProviderConfig is one provider's settings.
type ProviderConfig struct {
	// ProviderID is the logical id used for audit, logging, and LabelMap
	// lookup (e.g. "deberta-v3-large-mnli", "minicheck-flan-t5-large").
	ProviderID string
	// Endpoint is the full URL the client POSTs to.
	// DeBERTa example: https://router.huggingface.co/hf-inference/models/microsoft/deberta-v3-large-mnli
	// MiniCheck example: http://localhost:8080/score (self-hosted)
	Endpoint string
	// AuthToken is the bearer token (HuggingFace HF_TOKEN) or
	// self-hosted token. Never logged; never echoed in errors.
	AuthToken string
	// TimeoutMS is the per-call timeout. 0 → DefaultTimeoutMS.
	TimeoutMS int64
	// LabelMap maps provider-specific label strings to canonical Labels.
	// nil → provider's default map.
	LabelMap map[string]Label
	// ModelRev is the model revision (commit sha or version). Empty
	// → provider leaves Score.ModelRev empty.
	ModelRev string
}

// DefaultTunables (A14 anti-pattern guard: concrete numbers, no zeros).
const (
	DefaultTimeoutMS         = 10_000
	DefaultLatencyBudgetMS   = 10_000
	DefaultMaxPremiseBytes   = 65_536  // 64 KiB
	DefaultMaxHypothesisBytes = 8_192  // 8 KiB
)

// Sealed error set. Use errors.Is to classify; do not match strings.
var (
	// ErrProviderUnavailable: network failure, non-2xx (except 429),
	// auth failure (401/403). Router → fallback if enabled.
	ErrProviderUnavailable = errors.New("nli: provider unavailable")

	// ErrProviderTimeout: ctx deadline exceeded OR provider's own
	// timeout fired. Router → fallback if enabled.
	ErrProviderTimeout = errors.New("nli: provider timed out")

	// ErrProviderRateLimited: HTTP 429. Router → fallback if enabled.
	ErrProviderRateLimited = errors.New("nli: provider rate limited")

	// ErrProviderBadResponse: 2xx but body is malformed (wrong shape,
	// missing label, score out of range, unmapped label). This is a
	// CONTRACT bug — fallback would have the same contract, so router
	// does NOT retry. Caller decides whether to escalate to needs_human.
	ErrProviderBadResponse = errors.New("nli: provider returned malformed response")

	// ErrInputTooLarge: premise or hypothesis exceeds the configured
	// byte cap. Caller must truncate upstream (the artifact pipeline
	// already enforces size caps; this is a defense-in-depth gate).
	ErrInputTooLarge = errors.New("nli: input exceeds size cap")

	// ErrInputEmpty: premise or hypothesis is empty. NLI is undefined
	// on empty inputs. Caller bug.
	ErrInputEmpty = errors.New("nli: premise or hypothesis is empty")

	// ErrInvalidConfig: provider/router configuration rejected at
	// construction (empty endpoint, invalid threshold, negative cap).
	// Distinct from ErrProviderBadResponse so the operator can tell
	// "I misconfigured" apart from "the model misbehaved".
	ErrInvalidConfig = errors.New("nli: invalid configuration")

	// ErrNoProvider: router has neither primary nor a working
	// fallback (both failed). The caller (drift_judge) maps this to
	// verdict=needs_human per the v2.20.0 spec.
	ErrNoProvider = errors.New("nli: no provider produced a valid score")
)

// DefaultsFor returns cfg with zero-valued tunables replaced by the
// package defaults. It does NOT mutate the input.
func (c Config) DefaultsFor() Config {
	if c.LatencyBudgetMS == 0 {
		c.LatencyBudgetMS = DefaultLatencyBudgetMS
	}
	if c.MaxPremiseBytes == 0 {
		c.MaxPremiseBytes = DefaultMaxPremiseBytes
	}
	if c.MaxHypothesisBytes == 0 {
		c.MaxHypothesisBytes = DefaultMaxHypothesisBytes
	}
	c.Primary = c.Primary.WithDefaults()
	c.Fallback = c.Fallback.WithDefaults()
	return c
}

// WithDefaults returns pc with zero-valued tunables replaced. It does NOT
// mutate the input.
func (pc ProviderConfig) WithDefaults() ProviderConfig {
	if pc.TimeoutMS == 0 {
		pc.TimeoutMS = DefaultTimeoutMS
	}
	return pc
}