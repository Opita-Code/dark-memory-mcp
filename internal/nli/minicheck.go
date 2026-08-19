package nli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MiniCheck is a BINARY classifier (claim supported by document / not).
// We map it into the canonical 3-label space via two thresholds on the
// reported probability:
//   - score >= EntailAt  → entailment
//   - score <= ContraAt  → contradiction
//   - in between          → neutral
// Defaults: EntailAt = 0.7, ContraAt = 0.3 (a 0.4-wide neutral band).
// Tunable per-project via Config; Config IS the input.
const (
	DefaultMiniCheckEntailAt = 0.7
	DefaultMiniCheckContraAt = 0.3
)

// MiniCheckProvider scores a (document, claim) pair against a self-hosted
// MiniCheck HTTP endpoint. MiniCheck's reference Python implementation
// exposes /score with a JSON body {"doc": ..., "claim": ...} →
// {"score": 0.97, "label": 1, ...}; the exact wire shape varies by
// deployment (Bespoke, ollama, vLLM), so we accept any body that contains
// a "score" float in [0, 1].
//
// Verified: github.com/Liyan06/MiniCheck README (Apache-2.0, fetched
// 2026-08-19) — model_name one of roberta-large, deberta-v3-large,
// flan-t5-large, Bespoke-MiniCheck-7B; score(doc, claim) returns
// (pred_label, raw_prob) ∈ {0,1} × [0,1].
//
// We do not implement the local transformers/vLLM path here; that is
// out-of-process for T05. T05 wires a self-hosted HTTP endpoint that
// exposes the same input/output contract.
type MiniCheckProvider struct {
	client    HFInferenceClient
	endpoint  string
	authToken string
	timeout   time.Duration
	entailAt  float64
	contraAt  float64
	maxDBytes int // doc == premise for our usage
	maxCBytes int // claim == hypothesis for our usage
	modelRev  string
}

// NewMiniCheckProvider validates cfg and returns a MiniCheckProvider.
// Returns an error if cfg.ProviderID, cfg.Endpoint is empty, the HTTP
// client is nil, or entailAt ≤ contraAt.
func NewMiniCheckProvider(cfg ProviderConfig, hc HFInferenceClient, maxD, maxC int) (*MiniCheckProvider, error) {
	if cfg.ProviderID == "" {
		return nil, fmt.Errorf("%w: empty ProviderID", ErrInvalidConfig)
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("%w: empty Endpoint", ErrInvalidConfig)
	}
	if hc == nil {
		return nil, fmt.Errorf("%w: nil HTTP client", ErrInvalidConfig)
	}
	if maxD < 0 || maxC < 0 {
		return nil, fmt.Errorf("%w: negative size cap", ErrInvalidConfig)
	}
	cfg = cfg.WithDefaults()
	return &MiniCheckProvider{
		client:    hc,
		endpoint:  cfg.Endpoint,
		authToken: cfg.AuthToken,
		timeout:   time.Duration(cfg.TimeoutMS) * time.Millisecond,
		entailAt:  DefaultMiniCheckEntailAt,
		contraAt:  DefaultMiniCheckContraAt,
		maxDBytes: maxD,
		maxCBytes: maxC,
		modelRev:  cfg.ModelRev,
	}, nil
}

// SetThresholds overrides the default entail / contra bands. entailAt
// must be > contraAt.
func (p *MiniCheckProvider) SetThresholds(entailAt, contraAt float64) error {
	if entailAt <= contraAt {
		return fmt.Errorf("%w: entailAt (%v) must be > contraAt (%v)", ErrInvalidConfig, entailAt, contraAt)
	}
	if entailAt > 1 || contraAt < 0 {
		return fmt.Errorf("%w: thresholds must be in [0, 1]", ErrInvalidConfig)
	}
	p.entailAt = entailAt
	p.contraAt = contraAt
	return nil
}

// ID returns the provider's logical id.
func (p *MiniCheckProvider) ID() string { return "minicheck-flan-t5-large" }

// Score implements Provider.Score for MiniCheck. The premise is the
// "document" and the hypothesis is the "claim" — MiniCheck's natural
// input order. We send both as fields so any deployment that accepts
// either {"doc","claim"} or {"premise","hypothesis"} can be supported.
func (p *MiniCheckProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	if premise == "" || hypothesis == "" {
		return Score{}, ErrInputEmpty
	}
	if len(premise) > p.maxDBytes {
		return Score{}, fmt.Errorf("%w: doc=%d > %d", ErrInputTooLarge, len(premise), p.maxDBytes)
	}
	if len(hypothesis) > p.maxCBytes {
		return Score{}, fmt.Errorf("%w: claim=%d > %d", ErrInputTooLarge, len(hypothesis), p.maxCBytes)
	}

	payload, err := json.Marshal(map[string]any{
		"doc":        premise,
		"claim":      hypothesis,
		"premise":    premise,
		"hypothesis": hypothesis,
	})
	if err != nil {
		return Score{}, fmt.Errorf("%w: marshal: %w", ErrProviderBadResponse, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Score{}, fmt.Errorf("%w: build request: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.authToken)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderTimeout
		}
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderRateLimited
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderUnavailable
	case resp.StatusCode >= 500:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, ErrProviderUnavailable
	case resp.StatusCode >= 400:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: HTTP %d", ErrProviderBadResponse, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: unexpected status %d", ErrProviderUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: read: %w", ErrProviderBadResponse, err)
	}

	prob, err := parseMiniCheckResponse(body)
	if err != nil {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, err
	}

	label := mapMiniCheckScore(prob, p.entailAt, p.contraAt)
	score := Score{
		Label:      label,
		Confidence: prob,
		LatencyMS:  latency,
		ProviderID: p.ID(),
		ModelRev:   p.modelRev,
	}
	if !score.Valid() {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: score failed invariant check", ErrProviderBadResponse)
	}
	return score, nil
}

// parseMiniCheckResponse extracts the supported-probability float from
// the MiniCheck HTTP response. Acceptable body shapes:
//
//   {"score": 0.97, "label": 1}
//   {"raw_prob": 0.97}
//   {"probability": 0.97, "pred_label": 1}
//   [0.97, 0.03]   (legacy: [prob_supported, prob_not_supported])
//
// The "score"/"raw_prob"/"probability" field is preferred. The legacy
// [P, 1-P] array is accepted for the ollama-style deployment.
func parseMiniCheckResponse(body []byte) (float64, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return 0, fmt.Errorf("%w: empty body", ErrProviderBadResponse)
	}
	if body[0] == '[' {
		var arr []float64
		if err := json.Unmarshal(body, &arr); err != nil {
			return 0, fmt.Errorf("%w: array shape: %w", ErrProviderBadResponse, err)
		}
		if len(arr) == 0 {
			return 0, fmt.Errorf("%w: empty probability array", ErrProviderBadResponse)
		}
		if arr[0] < 0 || arr[0] > 1 {
			return 0, fmt.Errorf("%w: probability[0]=%v out of [0,1]", ErrProviderBadResponse, arr[0])
		}
		return arr[0], nil
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("%w: not JSON object: %w", ErrProviderBadResponse, err)
	}
	for _, k := range []string{"score", "raw_prob", "probability"} {
		if v, ok := raw[k]; ok {
			f, ok := v.(float64)
			if !ok {
				return 0, fmt.Errorf("%w: %q not a number", ErrProviderBadResponse, k)
			}
			if f < 0 || f > 1 {
				return 0, fmt.Errorf("%w: %s=%v out of [0,1]", ErrProviderBadResponse, k, f)
			}
			return f, nil
		}
	}
	return 0, fmt.Errorf("%w: no score / raw_prob / probability field", ErrProviderBadResponse)
}

// mapMiniCheckScore maps the binary probability to the canonical 3-label
// space. Out-of-band values clamp.
func mapMiniCheckScore(prob, entailAt, contraAt float64) Label {
	switch {
	case prob >= entailAt:
		return LabelEntailment
	case prob <= contraAt:
		return LabelContradiction
	default:
		return LabelNeutral
	}
}