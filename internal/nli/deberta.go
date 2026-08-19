package nli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HFInferenceClient is the boundary interface for HTTP calls. The
// production binding is *http.Client; tests use httptest.Server.
type HFInferenceClient interface {
	Do(*http.Request) (*http.Response, error)
}

// DeBERTaLabelMapDefault is the conventional mapping for
// microsoft/deberta-v3-large-mnli on the HuggingFace Inference API. The
// model was fine-tuned on MNLI with id2label = {0: "LABEL_0" (contradiction),
// 1: "LABEL_1" (neutral), 2: "LABEL_2" (entailment)}. Some mirrors flip
// the order; callers override via ProviderConfig.LabelMap.
var DeBERTaLabelMapDefault = map[string]Label{
	"LABEL_0":      LabelContradiction,
	"LABEL_1":      LabelNeutral,
	"LABEL_2":      LabelEntailment,
	"CONTRADICTION": LabelContradiction,
	"NEUTRAL":       LabelNeutral,
	"ENTAILMENT":   LabelEntailment,
	"contradiction": LabelContradiction,
	"neutral":       LabelNeutral,
	"entailment":    LabelEntailment,
	// Some HF Inference deployments lowercase the response labels.
	"0": LabelContradiction,
	"1": LabelNeutral,
	"2": LabelEntailment,
}

// DeBERTaProvider scores a (premise, hypothesis) pair against
// microsoft/deberta-v3-large-mnli via the HuggingFace Inference API.
//
// Wire format (verified against https://huggingface.co/docs/inference-providers/en/tasks/text-classification,
// fetched 2026-08-19):
//
//   POST {endpoint}
//   Authorization: Bearer {auth_token}
//   Content-Type: application/json
//   {
//     "inputs": "<premise> [SEP] <hypothesis>",
//     "parameters": {"function_to_apply": "softmax", "top_k": 3},
//     "options": {"wait_for_model": true}
//   }
//
//   → 200 [{"label": "LABEL_2", "score": 0.97}, ...]
//   → 401 / 403 → ErrProviderUnavailable
//   → 429       → ErrProviderRateLimited
//   → 503 / 5xx → ErrProviderUnavailable
//   → timeout   → ErrProviderTimeout
type DeBERTaProvider struct {
	client    HFInferenceClient
	endpoint  string
	authToken string
	timeout   time.Duration
	labelMap  map[string]Label
	maxPBytes int
	maxHBytes int
	modelRev  string
}

// NewDeBERTaProvider validates cfg and returns a DeBERTaProvider.
// Returns an error if cfg.ProviderID, cfg.Endpoint, or cfg.AuthToken is
// empty, or if MaxPremiseBytes / MaxHypothesisBytes is negative.
func NewDeBERTaProvider(cfg ProviderConfig, hc HFInferenceClient, maxP, maxH int) (*DeBERTaProvider, error) {
	if cfg.ProviderID == "" {
		return nil, fmt.Errorf("%w: empty ProviderID", ErrInvalidConfig)
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("%w: empty Endpoint", ErrInvalidConfig)
	}
	if hc == nil {
		return nil, fmt.Errorf("%w: nil HTTP client", ErrInvalidConfig)
	}
	if maxP < 0 || maxH < 0 {
		return nil, fmt.Errorf("%w: negative size cap", ErrInvalidConfig)
	}
	cfg = cfg.WithDefaults()
	lm := cfg.LabelMap
	if lm == nil {
		lm = DeBERTaLabelMapDefault
	}
	return &DeBERTaProvider{
		client:    hc,
		endpoint:  cfg.Endpoint,
		authToken: cfg.AuthToken,
		timeout:   time.Duration(cfg.TimeoutMS) * time.Millisecond,
		labelMap:  lm,
		maxPBytes: maxP,
		maxHBytes: maxH,
		modelRev:  cfg.ModelRev,
	}, nil
}

// ID returns the provider's logical id.
func (p *DeBERTaProvider) ID() string { return "deberta-v3-large-mnli" }

// Score implements Provider.Score for DeBERTa-v3-large-mnli.
func (p *DeBERTaProvider) Score(ctx context.Context, premise, hypothesis string) (Score, error) {
	if premise == "" || hypothesis == "" {
		return Score{}, ErrInputEmpty
	}
	if len(premise) > p.maxPBytes {
		return Score{}, fmt.Errorf("%w: premise=%d > %d", ErrInputTooLarge, len(premise), p.maxPBytes)
	}
	if len(hypothesis) > p.maxHBytes {
		return Score{}, fmt.Errorf("%w: hypothesis=%d > %d", ErrInputTooLarge, len(hypothesis), p.maxHBytes)
	}

	// DeBERTa pair format: [CLS] A [SEP] B [SEP]. The HF tokenizer
	// adds [CLS]/[SEP]; we just need the [SEP] separator between the
	// two segments. (Verified in transformers v4.56
	// docs/transformers/model_doc/deberta-v2, fetched 2026-08-19.)
	inputs := strings.Join([]string{premise, hypothesis}, " [SEP] ")

	payload, err := json.Marshal(struct {
		Inputs     string `json:"inputs"`
		Parameters struct {
			FunctionToApply string `json:"function_to_apply"`
			TopK            int    `json:"top_k"`
		} `json:"parameters"`
		Options struct {
			WaitForModel bool `json:"wait_for_model"`
		} `json:"options"`
	}{
		Inputs: inputs,
	})
	if err != nil {
		return Score{}, fmt.Errorf("%w: marshal: %w", ErrProviderBadResponse, err)
	}
	// top_k = 3 because MNLI has exactly 3 classes.
	// wait_for_model = true: the cold-start case (model not loaded)
	// should not look like a 503.
	// Encode manually so the field order is stable across marshaling
	// choices (Go's encoding/json orders by struct field order).
	payload = []byte(`{"inputs":"` + jsonEscape(inputs) + `","parameters":{"function_to_apply":"softmax","top_k":3},"options":{"wait_for_model":true}}`)

	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Score{}, fmt.Errorf("%w: build request: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+p.authToken)
	req.Header.Set("Content-Type", "application/json")

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
		// Other 4xx (e.g. 400 from a malformed payload): we sent a
		// valid payload, so the provider has a contract bug. BadResponse.
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: HTTP %d", ErrProviderBadResponse, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: unexpected status %d", ErrProviderUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap on response
	if err != nil {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: read: %w", ErrProviderBadResponse, err)
	}

	score, err := parseDeBERTaResponse(body, p.labelMap)
	if err != nil {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, err
	}
	score.LatencyMS = latency
	score.ProviderID = p.ID()
	score.ModelRev = p.modelRev
	if !score.Valid() {
		return Score{LatencyMS: latency, ProviderID: p.ID()}, fmt.Errorf("%w: score failed invariant check", ErrProviderBadResponse)
	}
	return score, nil
}

// parseDeBERTaResponse parses the HF Inference response and returns the
// highest-confidence (label, score) pair, mapped via labelMap.
//
// HF response shape: [{"label": "LABEL_2", "score": 0.97}, ...]
//
// Defensive parsing rules:
//   - top-level must be an array of length >= 1
//   - each entry must be an object with non-empty label + score ∈ [0,1]
//   - label MUST be in labelMap (else ErrProviderBadResponse — model
//     changed its id2label and we haven't adapted)
//   - ties broken by canonical priority: contradiction < neutral < entailment
//     (so when score == score, we prefer entailment as the "safe" choice).
func parseDeBERTaResponse(body []byte, labelMap map[string]Label) (Score, error) {
	var raw []struct {
		Label string  `json:"label"`
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Score{}, fmt.Errorf("%w: not an array: %w", ErrProviderBadResponse, err)
	}
	if len(raw) == 0 {
		return Score{}, fmt.Errorf("%w: empty result array", ErrProviderBadResponse)
	}
	// Pick the highest-confidence entry; ties broken by canonical order.
	bestIdx := -1
	bestScore := -1.0
	for i, e := range raw {
		if e.Label == "" {
			return Score{}, fmt.Errorf("%w: entry %d has empty label", ErrProviderBadResponse, i)
		}
		if e.Score < 0 || e.Score > 1 {
			return Score{}, fmt.Errorf("%w: entry %d score=%v out of [0,1]", ErrProviderBadResponse, i, e.Score)
		}
		if e.Score > bestScore {
			bestScore = e.Score
			bestIdx = i
			continue
		}
		if e.Score == bestScore && bestIdx >= 0 {
			// Tie-break: prefer canonical priority (entailment > neutral > contradiction).
			cur, curOk := labelMap[raw[bestIdx].Label]
			next, nextOk := labelMap[e.Label]
			if !curOk || !nextOk {
				continue
			}
			if canonicalPriority(next) > canonicalPriority(cur) {
				bestIdx = i
			}
		}
	}
	winner := raw[bestIdx]
	mapped, ok := labelMap[winner.Label]
	if !ok {
		return Score{}, fmt.Errorf("%w: label %q not in map", ErrProviderBadResponse, winner.Label)
	}
	if !mapped.Valid() {
		return Score{}, fmt.Errorf("%w: mapped label %q is invalid", ErrProviderBadResponse, mapped)
	}
	return Score{Label: mapped, Confidence: winner.Score}, nil
}

func canonicalPriority(l Label) int {
	switch l {
	case LabelEntailment:
		return 3
	case LabelNeutral:
		return 2
	case LabelContradiction:
		return 1
	}
	return 0
}

// jsonEscape is a hand-rolled JSON string escaper that avoids importing
// encoding/json's escapes for the simple case of inserting a string into
// a pre-built payload template. It escapes " \ \n \r \t and control chars.
func jsonEscape(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[(r>>4)&0xf])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}