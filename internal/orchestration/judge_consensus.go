// O8: JudgeConsensus — runs the Judge N times and returns the modal
// verdict with a confidence interval. Use this for HIGH-STAKES
// verdicts (compliance, brand match on launch, grounding of political
// claims) where a single sample's confidence might be misleading.
//
// Persistence model: each sample is persisted as its own SDDEvaluation
// row (so the audit trail captures the variance), AND one
// "consensus" SDDEvaluation row is persisted carrying the modal
// verdict + average confidence. The consensus row's TargetID is
// suffixed with ":consensus" so callers can filter it out of regular
// lists.
//
// Variance reporting: if the modal verdict has < 60% of votes, the
// orchestrator marks the result Verdict="needs_human" regardless of
// the modal string — high variance = low trust.
//
// Cost: N LLM calls. Default N=3. Max N=7 (per dark_ssd_consensus
// spec). Callers should use JudgeConsensus sparingly — it is
// roughly N× the cost of a single Judge call.
//
// Latency (v2.11.0): the N samples run CONCURRENTLY, so wall-clock
// latency is ~1 sample (plus per-sample retry/backoff), not N×.
// Partial failure degrades instead of aborting: failed samples are
// reported via Degraded + FailedSampleIndices and the modal fraction
// is computed against the requested N (survivors never overstate
// agreement).
package orchestration

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/ssd"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// JudgeConsensusInput is the request to run a consensus Judge.
type JudgeConsensusInput struct {
	EvalType   string `json:"eval_type"`       // brand_match | compliance_check | drift_judge | grounding_check | pii_detect | prompt_injection_scan
	TargetType string `json:"target_type"`     // brand | artifact | spec | claim | code | ...
	TargetID   string `json:"target_id"`       // brand_id | artifact_id | ...
	Content    string `json:"content"`         // the text to evaluate
	N          int    `json:"n,omitempty"`     // sample count; default 3, clamped to [1, 7]
	Model      string `json:"model,omitempty"` // optional override
	// AgentID (v2.4.2) is forwarded to every N Judge sample so all
	// samples see the same agent-scoped agent_memory enrichment
	// (for brand_match + compliance_check). Same priority chain as
	// v2.4.1 (caller > projects.default_agent_id > ""). drift_judge
	// enrichment still lives in PublishVibe (deliberate scope).
	AgentID string `json:"agent_id,omitempty"`
	// NoEnrich (v2.4.2) is forwarded to every N Judge sample so all
	// samples see the same opt-out semantics. Default false
	// (enrichment on for brand_match + compliance_check). When true,
	// every consensus sample receives raw content unchanged (no
	// enrichment block). Mirrors JudgeInput.NoEnrich for consistency.
	NoEnrich bool `json:"no_enrich,omitempty"`
	// VibeCase (v2.12.0) forwarded to every Judge sample so all N
	// samples use the same vibe-case rubric (G-Eval style).
	VibeCase string `json:"vibe_case,omitempty"`
}

// JudgeConsensusSample is one Judge call's result inside a consensus run.
type JudgeConsensusSample struct {
	SampleIndex  int     `json:"sample_index"`
	EvaluationID int64   `json:"evaluation_id"`
	Verdict      string  `json:"verdict"` // aligned | drift_detected | needs_human | skipped
	Confidence   float32 `json:"confidence"`
	VerdictJSON  string  `json:"verdict_json"`
}

// JudgeConsensusResult is the consensus verdict across N samples.
type JudgeConsensusResult struct {
	EvaluationID     int64                  `json:"evaluation_id"`     // id of the consensus row
	ModalVerdict     string                 `json:"modal_verdict"`     // the most-frequent verdict string
	ModalCount       int                    `json:"modal_count"`       // how many samples voted for the modal
	ModalFraction    float32                `json:"modal_fraction"`    // modal_count / N; < 0.6 → needs_human
	AvgConfidence    float32                `json:"avg_confidence"`    // mean confidence across samples
	StdDevConfidence float32                `json:"stddev_confidence"` // sample std dev (0 if N=1)
	ConfidenceLow    float32                `json:"confidence_low"`    // avg - 1σ (clamped to 0)
	ConfidenceHigh   float32                `json:"confidence_high"`   // avg + 1σ (clamped to 1)
	Verdict          string                 `json:"verdict"`           // modal_verdict OR "needs_human" if low agreement
	NextAction       string                 `json:"next_action"`       // publish | reconcile | human_gate
	Samples          []JudgeConsensusSample `json:"samples"`           // per-sample breakdown
	Reasoning        string                 `json:"reasoning"`
	// Degraded (v2.11.0) is true when at least one sample failed and
	// the consensus was computed from the survivors. The modal
	// fraction is still computed against the REQUESTED N, so a
	// minority of successes never overstates agreement.
	Degraded bool `json:"degraded"`
	// FailedSampleIndices (v2.11.0) lists the 0-based indices of
	// samples that failed (empty when not degraded).
	FailedSampleIndices []int `json:"failed_sample_indices,omitempty"`
}

// JudgeConsensus runs the Judge N times and aggregates. See package
// doc for cost + persistence model.
//
// Returns ErrInvalidArgument if Content is empty or N is invalid
// (negative, zero, or > 7). Returns ErrSessionRequired if no
// active project. Returns ErrCanaryInPayload if Content contains
// the active canary (same as Judge).
func (o *Orchestrator) JudgeConsensus(ctx context.Context, in JudgeConsensusInput) (*JudgeConsensusResult, error) {
	// 1. Validate.
	if strings.TrimSpace(in.Content) == "" {
		return nil, errMissingField("content")
	}
	if strings.TrimSpace(in.EvalType) == "" {
		return nil, errMissingField("eval_type")
	}
	n := in.N
	if n <= 0 {
		n = 3
	}
	if n > 7 {
		n = 7
	}

	// 2. Canary check (INV-3) — run once, share across samples. If
	// the content has the canary, the Judge would refuse each call;
	// we surface the error here instead of N times.
	if !o.Safety.Active().IsZero() && o.Safety.Active().Match(in.Content) {
		return nil, fmt.Errorf("%w: judge_consensus content contains canary token", store.ErrCanaryInPayload)
	}

	// 3. Run N samples CONCURRENTLY (v2.11.0). Sequential used to
	// cost N × per-call latency and a single sample failure killed
	// the whole consensus. Now each sample runs in its own
	// goroutine: LLM clients are stateless, the selector is
	// RWMutex-guarded, and the SQLite store serializes writes at the
	// driver (SetMaxOpenConns(1)), so parallel Judge calls are safe.
	// Per-sample retry/backoff lives in the LLM client.
	//
	// Partial failure DEGRADES instead of aborting: failed samples
	// are recorded in FailedSampleIndices; the modal fraction is
	// still computed against the REQUESTED N, so a minority of
	// successes naturally forces needs_human (never overstates
	// agreement).
	wc := store.WriteContext{
		Actor:     "orchestrator_judge_consensus",
		WritePath: "JudgeConsensus",
	}
	now := o.now().Format(time.RFC3339Nano)

	type sampleOutcome struct {
		sample JudgeConsensusSample
		err    error
	}
	outcomes := make([]sampleOutcome, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			jOut, jerr := o.Judge(ctx, JudgeInput{
				EvalType:   in.EvalType,
				TargetType: in.TargetType,
				TargetID:   in.TargetID,
				Content:    in.Content,
				Model:      in.Model,
				// v2.4.2: forward AgentID so each sample gets the same
				// agent-scoped enrichment (brand_match + compliance_check).
				AgentID: in.AgentID,
				// v2.4.2: forward NoEnrich so all N samples share the
				// same opt-out semantics (raw content vs enriched).
				NoEnrich: in.NoEnrich,
				// v2.12.0: forward VibeCase so all N samples use the
				// same G-Eval rubric.
				VibeCase: in.VibeCase,
			})
			if jerr != nil {
				outcomes[i].err = jerr
				return
			}
			v := parseDriftVerdict(jOut.VerdictJSON, jOut.Confidence)
			outcomes[i].sample = JudgeConsensusSample{
				SampleIndex:  i,
				EvaluationID: jOut.EvaluationID,
				Verdict:      v,
				Confidence:   jOut.Confidence,
				VerdictJSON:  jOut.VerdictJSON,
			}
		}(i)
	}
	wg.Wait()

	// Aggregate the survivors (order-preserving; samples keep their
	// original indices).
	samples := make([]JudgeConsensusSample, 0, n)
	verdicts := make(map[string]int)
	confSum := float64(0)
	var failed []int
	for i := range outcomes {
		if outcomes[i].err != nil {
			failed = append(failed, i)
			continue
		}
		s := outcomes[i].sample
		samples = append(samples, s)
		verdicts[s.Verdict]++
		confSum += float64(s.Confidence)
	}

	if len(samples) == 0 {
		first := "unknown"
		if outcomes[0].err != nil {
			first = outcomes[0].err.Error()
		}
		return nil, fmt.Errorf("judge_consensus: all %d samples failed (first error: %s)", n, first)
	}

	// 4. Compute modal verdict + fraction. ModalFraction uses the
	// REQUESTED n (not len(samples)) so failed samples count as
	// non-votes.
	modalVerdict, modalCount := modalVerdictFromCounts(verdicts)
	modalFraction := float32(modalCount) / float32(n)
	avgConfidence := float32(confSum / float64(len(samples)))

	// 5. Compute confidence interval (sample std dev).
	stddev := stdDevConfidence(samples, avgConfidence)
	low := clamp01(avgConfidence - stddev)
	high := clamp01(avgConfidence + stddev)

	// 6. Decide final verdict. If modal agreement is < 60%, override
	// to "needs_human" (variance too high to trust the modal).
	finalVerdict := modalVerdict
	if modalFraction < 0.6 {
		finalVerdict = "needs_human"
	}

	result := &JudgeConsensusResult{
		ModalVerdict:        modalVerdict,
		ModalCount:          modalCount,
		ModalFraction:       modalFraction,
		AvgConfidence:       avgConfidence,
		StdDevConfidence:    stddev,
		ConfidenceLow:       low,
		ConfidenceHigh:      high,
		Verdict:             finalVerdict,
		NextAction:          nextActionForVerdict(finalVerdict),
		Samples:             samples,
		Degraded:            len(failed) > 0,
		FailedSampleIndices: failed,
		Reasoning: fmt.Sprintf("modal=%s (%d/%d, fraction=%.2f); avg_conf=%.3f; stddev=%.3f; interval=[%.3f, %.3f]",
			modalVerdict, modalCount, n, modalFraction, avgConfidence, stddev, low, high),
	}
	if result.Degraded {
		result.Reasoning += fmt.Sprintf("; DEGRADED: %d sample(s) failed (indices %v)",
			len(failed), failed)
	}

	// 7. Persist the consensus SDDEvaluation row (with :consensus
	// suffix on TargetID so it's filterable).
	consensusEval := &ssd.SDDEvaluation{
		EvalType:       in.EvalType,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID + ":consensus",
		VerdictJSON:    consensusVerdictJSON(result),
		Confidence:     avgConfidence,
		Model:          "",
		ConstitutionID: wc.ConstitutionID,
		CreatedAt:      now,
	}
	consensusID, err := o.Store.SaveSDDEvaluation(ctx, wc, consensusEval)
	if err != nil {
		return nil, fmt.Errorf("judge_consensus: save consensus evaluation: %w", err)
	}
	result.EvaluationID = consensusID

	return result, nil
}

// modalVerdictFromCounts returns the verdict with the most votes,
// breaking ties by canonical priority: aligned > drift_detected >
// needs_human > skipped (the order operators trust most).
func modalVerdictFromCounts(counts map[string]int) (string, int) {
	if len(counts) == 0 {
		return "needs_human", 0
	}
	priority := []string{"aligned", "drift_detected", "needs_human", "skipped"}

	// First find the max count.
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	// Among verdicts tied at max, pick by priority order.
	for _, v := range priority {
		if counts[v] == max {
			return v, max
		}
	}
	// Fallback: any verdict with max count (shouldn't happen given
	// priority covers the canonical set, but defensive).
	for v, c := range counts {
		if c == max {
			return v, max
		}
	}
	return "needs_human", 0
}

// stdDevConfidence computes the sample standard deviation of
// confidence values across samples. Returns 0 if N < 2 (no variance
// defined).
func stdDevConfidence(samples []JudgeConsensusSample, mean float32) float32 {
	if len(samples) < 2 {
		return 0
	}
	var sumSq float64
	for _, s := range samples {
		d := float64(s.Confidence) - float64(mean)
		sumSq += d * d
	}
	variance := sumSq / float64(len(samples)-1) // sample std dev (Bessel's correction)
	return float32(math.Sqrt(variance))
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// consensusVerdictJSON serialises the consensus verdict into a
// canonical JSON shape that callers (and later drift judges) can
// parse. Shape:
//
//	{
//	  "modal": "aligned",
//	  "fraction": 0.92,
//	  "avg_confidence": 0.88,
//	  "stddev_confidence": 0.03,
//	  "degraded": false,
//	  "failed": [1],            // only when degraded
//	  "samples": [...],
//	  "n": 3
//	}
func consensusVerdictJSON(r *JudgeConsensusResult) string {
	type sampleJSON struct {
		SampleIndex int     `json:"sample_index"`
		Verdict     string  `json:"verdict"`
		Confidence  float32 `json:"confidence"`
	}
	type out struct {
		Modal            string       `json:"modal"`
		Fraction         float32      `json:"fraction"`
		AvgConfidence    float32      `json:"avg_confidence"`
		StdDevConfidence float32      `json:"stddev_confidence"`
		Degraded         bool         `json:"degraded"`
		Failed           []int        `json:"failed,omitempty"`
		N                int          `json:"n"`
		Samples          []sampleJSON `json:"samples"`
	}
	o := out{
		Modal:            r.ModalVerdict,
		Fraction:         r.ModalFraction,
		AvgConfidence:    r.AvgConfidence,
		StdDevConfidence: r.StdDevConfidence,
		Degraded:         r.Degraded,
		Failed:           r.FailedSampleIndices,
		N:                len(r.Samples),
	}
	for _, s := range r.Samples {
		o.Samples = append(o.Samples, sampleJSON{
			SampleIndex: s.SampleIndex,
			Verdict:     s.Verdict,
			Confidence:  s.Confidence,
		})
	}
	// Stable JSON output: sort samples by SampleIndex.
	sort.SliceStable(o.Samples, func(i, j int) bool {
		return o.Samples[i].SampleIndex < o.Samples[j].SampleIndex
	})
	// Encode by hand to keep the dependency surface small (the
	// rest of the package uses json.Marshal; this is the exception
	// because we want a strict shape).
	var b strings.Builder
	b.WriteString("{")
	b.WriteString(`"modal":"`)
	b.WriteString(o.Modal)
	b.WriteString(`",`)
	writeFloat(&b, "fraction", o.Fraction)
	writeFloat(&b, "avg_confidence", o.AvgConfidence)
	writeFloat(&b, "stddev_confidence", o.StdDevConfidence)
	if o.Degraded {
		b.WriteString(`"degraded":true,`)
		b.WriteString(`"failed":[`)
		for i, f := range o.Failed {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(fmt.Sprintf("%d", f))
		}
		b.WriteString(`],`)
	} else {
		b.WriteString(`"degraded":false,`)
	}
	b.WriteString(`"n":`)
	b.WriteString(fmt.Sprintf("%d", o.N))
	b.WriteString(`,"samples":[`)
	for i, s := range o.Samples {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"sample_index":`)
		b.WriteString(fmt.Sprintf("%d", s.SampleIndex))
		b.WriteString(`,"verdict":"`)
		b.WriteString(s.Verdict)
		b.WriteString(`",`)
		writeFloat(&b, "confidence", s.Confidence)
		b.WriteString("}")
	}
	b.WriteString("]}")
	return b.String()
}

// writeFloat writes "key":value as a 4-decimal float. Avoids the
// encoding/json dependency here (we still use encoding/json
// elsewhere; this helper just avoids reflection overhead in the
// hot consensus path).
func writeFloat(b *strings.Builder, key string, v float32) {
	b.WriteString(`"`)
	b.WriteString(key)
	b.WriteString(`":`)
	b.WriteString(fmt.Sprintf("%.4f", v))
	b.WriteString(",")
}
