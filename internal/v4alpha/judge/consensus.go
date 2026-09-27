// Package judge — Consensus (N-shot modal verdict, ADR-007 §9 commit 2).
//
// Consensus runs Pipeline.Evaluate N times in parallel and aggregates
// the per-sample verdicts into a single result. Default N=3, clamped
// to [1, 7] (matches orchestration/judge_consensus.go convention so
// v2 + v4 callers share the same upper bound).
//
// Why N>1: a single judge's verdict is one observation. For high-stakes
// decisions (compliance_check on production launch, brand_match on
// release copy) the operator wants variance + modal agreement, not a
// point estimate. Per dark-memory SKILL.md §6 "judge.consensus", this
// is the canonical "ask twice, take the median" pattern.
//
// Concurrency model:
//
//	Pipeline.Evaluate calls run as goroutines, one per sample.
//	A buffered semaphore (channel of struct{}, capacity N) bounds
//	concurrent HTTP requests so a high-N consensus doesn't exhaust
//	the provider's rate limit. Failed samples do NOT block
//	siblings — they record their error and return early.
//
// Variance source:
//
//	For m3-thinking providers (minimax, minimax-cn), responses are
//	NON-DETERMINISTIC by design (spec 1198), so N parallel calls with
//	identical inputs produce variance naturally.
//	For deterministic providers (anthropic with T=0.0, openai with
//	seed=N), all N samples produce the same verdict. The result is
//	still well-formed: modal=that verdict, agreement=100%,
//	stddev=0. This is documented in the ConsensusResult.Reasoning
//	field so operators can detect "I asked 3 judges but got 1 verdict"
//	vs "I asked 3 judges and got 3 different verdicts".
//
// Future extension (commit 4+): per-sample temperature variation for
//	deterministic providers. Out of scope for commit 2.
//
// Cost:
//
//	N LLM calls. Default N=3 = 3× the cost of a single Evaluate.
//	Operators should use Consensus sparingly (high-stakes verdicts
//	only).
package judge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// ---------- Consensus types ----------

// ConsensusRequest is the input to Consensus. Mirrors EvaluateRequest
// (the per-sample input) + N (sample count).
type ConsensusRequest struct {
	EvaluateRequest
	// N is the sample count. Clamped to [1, 7]. Default 3 when zero.
	N int
}

// ConsensusSample is one Pipeline.Evaluate result inside a consensus
// run. LatencyMs is the wall-clock time for that sample (measured
// inside its goroutine). Error is non-nil when the sample failed.
type ConsensusSample struct {
	Index     int
	Verdict   *Verdict
	LatencyMs int64
	Error     error
}

// ConsensusResult is the modal verdict + confidence interval across
// N samples. Fields mirror the orchestration.judge_consensus result
// shape so callers can switch v2 ↔ v4 with minimal friction.
type ConsensusResult struct {
	// ModalVerdict is the most-frequent verdict label among the
	// surviving samples (VerdictAligned, VerdictDriftDetected,
	// VerdictNeedsHuman, VerdictErrored).
	ModalVerdict string

	// ModalCount is how many surviving samples voted for the modal.
	ModalCount int

	// ModalFraction = ModalCount / RequestedN. Computed against the
	// REQUESTED N, not the surviving count, so a minority of
	// successes never overstates agreement (per dark-memory skill
	// §6.2). < 0.60 → Verdict overridden to VerdictNeedsHuman.
	ModalFraction float64

	// AvgConfidence is the mean confidence across surviving samples.
	AvgConfidence float64

	// StdDevConfidence is the sample standard deviation of the
	// surviving confidences. 0 when N=1 or all survivors have the
	// same confidence.
	StdDevConfidence float64

	// ConfidenceLow / ConfidenceHigh = AvgConfidence ± 1σ, clamped
	// to [0, 1]. Use this to detect "the judges agree on the verdict
	// but disagree on how confident they are" → needs_human.
	ConfidenceLow  float64
	ConfidenceHigh float64

	// RequestedN is the (clamped) sample count the operator asked
	// for. SurvivedN = RequestedN - len(FailedSampleIndices).
	RequestedN int

	// Verdict is the FINAL verdict for this consensus call.
	//   - ModalVerdict when ModalFraction ≥ 0.60
	//   - VerdictNeedsHuman when ModalFraction < 0.60 (disagreement)
	//   - VerdictErrored when ZERO samples survived (total failure)
	Verdict string

	// NextAction is a one-word directive for the transport layer:
	//   - "publish"      when Verdict ∈ {aligned, drift_detected}
	//   - "human_gate"   when Verdict == needs_human
	//   - "abort"        when Verdict == errored
	NextAction string

	// Samples is the per-sample breakdown (errors included).
	Samples []ConsensusSample

	// FailedSampleIndices lists the 0-based indices of samples that
	// failed (empty when all succeeded).
	FailedSampleIndices []int

	// Degraded is true when at least one sample failed. The modal
	// is still computed from the survivors but the operator should
	// know that agreement is computed against a smaller N.
	Degraded bool

	// Reasoning is a 1-paragraph human-readable summary that callers
	// can surface in audit logs / dashboards.
	Reasoning string

	// AggregatedAt is when the consensus result was finalized.
	AggregatedAt time.Time
}

// ---------- Consensus entry point ----------

// Consensus runs the Pipeline.Evaluate N times in parallel and
// aggregates the results. Returns ErrInvalidArgument when the request
// is malformed (vibe_case, eval_type, spec_intent required per the
// Pipeline's step [1] invariants).
//
// Backpressure: the N goroutines share a buffered semaphore of
// capacity N. For typical N (3-7) this is just N concurrent HTTP
// calls. For operators who set a lower concurrency budget, use
// ConsensusWithSemaphore instead.
func Consensus(ctx context.Context, p *Pipeline, req ConsensusRequest) (*ConsensusResult, error) {
	return ConsensusWithSemaphore(ctx, p, req, 0)
}

// ConsensusWithSemaphore is the budgeted variant. concurrency=0
// means "unbounded (= N)" (the default Consensus behavior);
// concurrency>0 caps concurrent in-flight samples (useful when the
// provider has a strict rate limit).
//
// Per dark-memory skill §6.3: bounded concurrency is the canonical
// way to interact with rate-limited providers (Anthropic's 50 req/min
// for Haiku, MiniMax-M3's regional caps).
func ConsensusWithSemaphore(
	ctx context.Context,
	p *Pipeline,
	req ConsensusRequest,
	concurrency int,
) (*ConsensusResult, error) {
	if p == nil {
		return nil, errors.New("judge: Consensus: pipeline is nil")
	}
	if req.VibeCase == "" {
		return nil, errors.New("judge: Consensus: VibeCase required")
	}
	if req.EvalType == "" {
		return nil, errors.New("judge: Consensus: EvalType required")
	}
	if len(req.SpecIntent) < 10 {
		return nil, errors.New("judge: Consensus: SpecIntent too short (< 10 chars, EC-006)")
	}

	n := req.N
	if n <= 0 {
		n = 3
	}
	if n > 7 {
		n = 7
	}
	if concurrency <= 0 || concurrency > n {
		concurrency = n
	}

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	samples := make([]ConsensusSample, n)
	startTotal := time.Now()

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				samples[idx] = ConsensusSample{
					Index: idx,
					Error: ctx.Err(),
				}
				return
			}
			start := time.Now()
			v, err := p.Evaluate(ctx, req.EvaluateRequest)
			elapsed := time.Since(start).Milliseconds()
			samples[idx] = ConsensusSample{
				Index:     idx,
				Verdict:   v,
				LatencyMs: elapsed,
				Error:     err,
			}
		}(i)
	}
	wg.Wait()

	result := aggregateConsensus(samples, n)
	result.AggregatedAt = time.Now()
	_ = startTotal // reserved for future wall-clock budget assertions
	return result, nil
}

// ---------- Aggregation ----------

// aggregateConsensus is the pure aggregation step (extracted from
// ConsensusWithSemaphore for testability). Given N samples (some may
// have errored), produces the modal verdict + confidence interval.
//
// Precedence (highest first): VerdictErrored (zero survivors) >
// disagreement (modal fraction < 0.60 → needs_human) > modal verdict.
//
// Cost guard: at least 1 sample must survive. When zero survive, the
// result is VerdictErrored + NextAction=abort.
func aggregateConsensus(samples []ConsensusSample, requestedN int) *ConsensusResult {
	if requestedN <= 0 {
		requestedN = len(samples)
	}
	result := &ConsensusResult{
		RequestedN: requestedN,
		Samples:    samples,
	}

	// Count surviving samples per verdict label. A sample is
	// considered a "failure" when:
	//   - the LLMClient returned an error (s.Error != nil), OR
	//   - the verdict is nil (Pipeline returned nil somehow), OR
	//   - the verdict failed Validate(), OR
	//   - the verdict was caused by EC-002 (LLM unavailable short-
	//     circuit). These are NOT real votes — they are infra errors
	//     masquerading as a verdict label.
	survived := 0
	labelCounts := map[string]int{}
	confidences := []float64{}
	var failedIndices []int
	for i, s := range samples {
		if s.Error != nil || s.Verdict == nil {
			failedIndices = append(failedIndices, i)
			result.Degraded = true
			continue
		}
		if err := s.Verdict.Validate(); err != nil {
			failedIndices = append(failedIndices, i)
			result.Degraded = true
			continue
		}
		if isLLMFailureVerdict(s.Verdict) {
			failedIndices = append(failedIndices, i)
			result.Degraded = true
			continue
		}
		survived++
		labelCounts[s.Verdict.Verdict]++
		confidences = append(confidences, s.Verdict.Confidence)
	}
	result.FailedSampleIndices = failedIndices

	// Zero survivors → errored.
	if survived == 0 {
		result.ModalVerdict = VerdictErrored
		result.ModalCount = 0
		result.ModalFraction = 0.0
		result.AvgConfidence = 0.0
		result.Verdict = VerdictErrored
		result.NextAction = "abort"
		result.Reasoning = fmt.Sprintf(
			"consensus: 0/%d samples survived (all failed); first error: %v",
			requestedN, firstSampleError(samples),
		)
		return result
	}

	// Find modal verdict (highest count; ties broken by
	// VerdictAligned > VerdictDriftDetected > VerdictNeedsHuman >
	// VerdictErrored — i.e. biased toward "more permissive" so an
	// accidental tie does not under-report).
	modalLabel, modalCount := pickModal(labelCounts)
	result.ModalVerdict = modalLabel
	result.ModalCount = modalCount
	result.ModalFraction = float64(modalCount) / float64(requestedN)

	// Confidence statistics.
	avg, stddev := meanAndStdDev(confidences)
	result.AvgConfidence = avg
	result.StdDevConfidence = stddev
	result.ConfidenceLow = clamp01(avg - stddev)
	result.ConfidenceHigh = clamp01(avg + stddev)

	// Verdict precedence: disagreement overrides modal.
	if result.ModalFraction < 0.60 {
		result.Verdict = VerdictNeedsHuman
		result.NextAction = "human_gate"
		result.Reasoning = fmt.Sprintf(
			"consensus: modal %q had %d/%d votes (%.0f%% < 60%%); disagreement → needs_human",
			modalLabel, modalCount, requestedN, result.ModalFraction*100,
		)
		return result
	}

	result.Verdict = modalLabel
	switch modalLabel {
	case VerdictAligned:
		result.NextAction = "publish"
	case VerdictDriftDetected:
		result.NextAction = "publish" // publish the drift finding
	case VerdictNeedsHuman:
		result.NextAction = "human_gate"
	case VerdictErrored:
		result.NextAction = "abort"
	}
	if result.Degraded {
		result.Reasoning = fmt.Sprintf(
			"consensus: modal %q with %d/%d votes (%.0f%%); %d sample(s) failed (degraded, agreement computed against requested N)",
			modalLabel, modalCount, requestedN, result.ModalFraction*100, len(failedIndices),
		)
	} else {
		result.Reasoning = fmt.Sprintf(
			"consensus: modal %q with %d/%d votes (%.0f%% agreement)",
			modalLabel, modalCount, requestedN, result.ModalFraction*100,
		)
	}
	return result
}

// pickModal finds the verdict label with the highest count. Ties are
// broken by canonical priority: VerdictAligned > VerdictDriftDetected
// > VerdictNeedsHuman > VerdictErrored (more-permissive wins, so an
// accidental tie does not under-report success).
func pickModal(counts map[string]int) (string, int) {
	if len(counts) == 0 {
		return "", 0
	}
	priority := map[string]int{
		VerdictAligned:       4,
		VerdictDriftDetected: 3,
		VerdictNeedsHuman:    2,
		VerdictErrored:       1,
	}
	bestLabel := ""
	bestCount := -1
	bestPriority := -1
	for label, c := range counts {
		p := priority[label]
		if c > bestCount || (c == bestCount && p > bestPriority) {
			bestLabel = label
			bestCount = c
			bestPriority = p
		}
	}
	return bestLabel, bestCount
}

// meanAndStdDev returns (mean, sample std dev). n=1 → stddev=0 (no
// variance in a single observation).
func meanAndStdDev(xs []float64) (float64, float64) {
	n := len(xs)
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(n)
	if n == 1 {
		return mean, 0
	}
	var sqDiff float64
	for _, x := range xs {
		d := x - mean
		sqDiff += d * d
	}
	variance := sqDiff / float64(n-1) // sample (Bessel-corrected) variance
	return mean, math.Sqrt(variance)
}

// clamp01 returns x clamped to [0, 1].
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// isLLMFailureVerdict reports whether v is a Pipeline EC-002
// short-circuit verdict (LLM unavailable). These are NOT real
// votes — they are infra errors that the Pipeline converts into a
// valid-looking Verdict for backwards compat. Consensus must
// distinguish "the LLM failed" from "the LLM said X".
func isLLMFailureVerdict(v *Verdict) bool {
	if v == nil {
		return false
	}
	if v.Verdict != VerdictErrored {
		return false
	}
	// EC-002 hit marks an LLM-call failure. A verdict can be
	// "errored" for other reasons (e.g., validation failure), but
	// those have empty EdgeCaseHits.
	for _, h := range v.EdgeCaseHits {
		if h.ID == "EC-002" {
			return true
		}
	}
	return false
}

// firstSampleError returns the first error among failed samples, or
// a sentinel when all samples succeeded.
func firstSampleError(samples []ConsensusSample) error {
	for _, s := range samples {
		if s.Error != nil {
			return s.Error
		}
	}
	return errors.New("no error recorded")
}

// ---------- Helper for callers ----------

// VerdictDistribution returns the per-label vote count, sorted by
// count descending. Useful for dashboards / debug logs that want to
// see "3 aligned, 1 needs_human" instead of a flat list.
func (r *ConsensusResult) VerdictDistribution() []struct {
	Verdict string
	Count   int
} {
	counts := map[string]int{}
	for _, s := range r.Samples {
		if s.Verdict == nil {
			continue
		}
		counts[s.Verdict.Verdict]++
	}
	out := make([]struct {
		Verdict string
		Count   int
	}, 0, len(counts))
	for v, c := range counts {
		out = append(out, struct {
			Verdict string
			Count   int
		}{Verdict: v, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Verdict < out[j].Verdict
	})
	return out
}
