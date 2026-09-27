// Tests for consensus.go (N-shot modal verdict).
//
// Uses a MultiFakeLLM that returns different responses per call to
// simulate non-deterministic providers. Tests cover all aggregation
// branches: all-aligned, all-drift, mixed, disagreement override,
// N=1, partial failure, zero survivors.
package judge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// MultiFakeLLM returns a different response per call. Responses[i]
// is returned on call i (mod len). Set ErrFromIndex > 0 to make
// the client return Err (or a default error) starting at that call
// index. Default (0 or negative) = no errors.
type MultiFakeLLM struct {
	Responses    []string // each is a verdict JSON blob
	ErrFromIndex int      // call index (0-based) where errors start firing; <=0 = no errors
	Err          error
	Calls        int32
	mu           sync.Mutex // unused but kept for API parity
}

func (m *MultiFakeLLM) Complete(_ context.Context, _ LLMRequest) (*LLMResponse, error) {
	n := atomic.AddInt32(&m.Calls, 1)
	idx := int(n) - 1
	if m.ErrFromIndex > 0 && idx >= m.ErrFromIndex {
		if m.Err != nil {
			return nil, m.Err
		}
		return nil, errors.New("simulated error")
	}
	if len(m.Responses) == 0 {
		return nil, errors.New("MultiFakeLLM: no responses configured")
	}
	if idx >= len(m.Responses) {
		idx = idx % len(m.Responses)
	}
	return &LLMResponse{
		Content:      m.Responses[idx],
		Provider:     "test",
		Model:        "test",
		FinishReason: "stop",
	}, nil
}

// errNever is the default (no errors).
const errNever = 0

// alignedResponse is a canned aligned verdict for fake clients.
func alignedResponse() string {
	return fakeResponse(map[string]float64{
		"correctness": 0.95, "tests": 0.95, "security": 0.95,
		"idiomatic": 0.95, "docs": 0.95,
	}, "all checks pass")
}

// driftResponse is a canned drift verdict for fake clients.
func driftResponse() string {
	return fakeResponse(map[string]float64{
		"correctness": 0.4, "tests": 0.4, "security": 0.4,
		"idiomatic": 0.5, "docs": 0.5,
	}, "drift detected across multiple axes")
}

func TestConsensus_AllAligned(t *testing.T) {
	fake := &MultiFakeLLM{
		Responses: []string{alignedResponse(), alignedResponse(), alignedResponse()},
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this aligned code artifact",
			VibeCase:        "C1",
			ArtifactContent: []byte("a sample aligned artifact with passing tests"),
		},
		N: 3,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.Verdict != VerdictAligned {
		t.Errorf("Verdict = %s; want aligned", res.Verdict)
	}
	if res.ModalVerdict != VerdictAligned {
		t.Errorf("ModalVerdict = %s; want aligned", res.ModalVerdict)
	}
	if res.ModalCount != 3 {
		t.Errorf("ModalCount = %d; want 3", res.ModalCount)
	}
	if res.ModalFraction < 0.99 {
		t.Errorf("ModalFraction = %f; want ~1.0", res.ModalFraction)
	}
	if res.NextAction != "publish" {
		t.Errorf("NextAction = %s; want publish", res.NextAction)
	}
	if res.Degraded {
		t.Error("Degraded = true; want false")
	}
}

func TestConsensus_AllDrift(t *testing.T) {
	fake := &MultiFakeLLM{
		Responses: []string{driftResponse(), driftResponse(), driftResponse()},
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("a drift-prone artifact"),
		},
		N: 3,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.Verdict != VerdictDriftDetected {
		t.Errorf("Verdict = %s; want drift_detected", res.Verdict)
	}
	if res.NextAction != "publish" {
		t.Errorf("NextAction = %s; want publish (publish the drift finding)", res.NextAction)
	}
}

func TestConsensus_MixedLabels_ModalPicksAligned(t *testing.T) {
	// 2 aligned + 1 drift → 2/3 ≈ 66.7% > 60% threshold → aligned wins.
	fake := &MultiFakeLLM{
		Responses: []string{alignedResponse(), driftResponse(), alignedResponse()},
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("mixed signal artifact"),
		},
		N: 3,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.ModalVerdict != VerdictAligned {
		t.Errorf("ModalVerdict = %s; want aligned", res.ModalVerdict)
	}
	if res.ModalCount != 2 {
		t.Errorf("ModalCount = %d; want 2", res.ModalCount)
	}
	if res.Verdict != VerdictAligned {
		t.Errorf("Verdict = %s; want aligned (modal fraction >= 0.60)", res.Verdict)
	}
}

func TestConsensus_Disagreement_OverridesToNeedsHuman(t *testing.T) {
	// 1 aligned + 1 drift + 1 needs_human → modal 1/3 = 33% < 60% → needs_human.
	fake := &MultiFakeLLM{
		Responses: []string{alignedResponse(), driftResponse(), `{"verdict_label":"needs_human","reasoning":"ambiguous","criteria":[{"name":"correctness","score":0.5,"note":"maybe"}]}`},
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("ambiguous artifact"),
		},
		N: 3,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.Verdict != VerdictNeedsHuman {
		t.Errorf("Verdict = %s; want needs_human (disagreement)", res.Verdict)
	}
	if res.NextAction != "human_gate" {
		t.Errorf("NextAction = %s; want human_gate", res.NextAction)
	}
	if res.ModalFraction >= 0.60 {
		t.Errorf("ModalFraction = %f; want < 0.60", res.ModalFraction)
	}
}

func TestConsensus_N1_EdgeCase(t *testing.T) {
	fake := &MultiFakeLLM{Responses: []string{alignedResponse()}}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("single-sample artifact"),
		},
		N: 1,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.ModalCount != 1 {
		t.Errorf("ModalCount = %d; want 1", res.ModalCount)
	}
	if res.ModalFraction != 1.0 {
		t.Errorf("ModalFraction = %f; want 1.0", res.ModalFraction)
	}
	if res.StdDevConfidence != 0 {
		t.Errorf("StdDevConfidence = %f; want 0 (N=1)", res.StdDevConfidence)
	}
}

func TestConsensus_PartialFailure_Degrades(t *testing.T) {
	// 2 aligned succeed + 1 fails → modal 2/3 (67%) but degraded.
	fake := &MultiFakeLLM{
		Responses:    []string{alignedResponse(), alignedResponse()},
		ErrFromIndex: 2,
		Err:          errors.New("provider 503"),
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("partial-failure artifact"),
		},
		N: 3,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if !res.Degraded {
		t.Error("Degraded = false; want true (1 failed sample)")
	}
	if len(res.FailedSampleIndices) != 1 {
		t.Errorf("FailedSampleIndices = %v; want 1 entry", res.FailedSampleIndices)
	}
	if res.ModalCount != 2 {
		t.Errorf("ModalCount = %d; want 2 (only 2 survived)", res.ModalCount)
	}
	if res.ModalFraction < 0.65 || res.ModalFraction > 0.67 {
		t.Errorf("ModalFraction = %f; want ~0.67 (2/3)", res.ModalFraction)
	}
}

func TestConsensus_ZeroSurvivors_Errored(t *testing.T) {
	fake := &MultiFakeLLM{
		ErrFromIndex: 1, // error from call 1 onwards (covers all 3)
		Err:          errors.New("all samples fail"),
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("total failure artifact"),
		},
		N: 3,
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.Verdict != VerdictErrored {
		t.Errorf("Verdict = %s; want errored", res.Verdict)
	}
	if res.NextAction != "abort" {
		t.Errorf("NextAction = %s; want abort", res.NextAction)
	}
	if res.ModalCount != 0 {
		t.Errorf("ModalCount = %d; want 0", res.ModalCount)
	}
}

func TestConsensus_NClampedToMax7(t *testing.T) {
	fake := &MultiFakeLLM{
		Responses: []string{alignedResponse(), alignedResponse(), alignedResponse()},
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("artifact"),
		},
		N: 100, // should be clamped to 7
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.RequestedN != 7 {
		t.Errorf("RequestedN = %d; want 7 (clamped)", res.RequestedN)
	}
}

func TestConsensus_NDefaultsTo3(t *testing.T) {
	fake := &MultiFakeLLM{
		Responses: []string{alignedResponse(), alignedResponse(), alignedResponse()},
	}
	p := newConsensusPipeline(t, fake)
	res, err := Consensus(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("artifact"),
		},
		N: 0, // default
	})
	if err != nil {
		t.Fatalf("Consensus: %v", err)
	}
	if res.RequestedN != 3 {
		t.Errorf("RequestedN = %d; want 3 (default)", res.RequestedN)
	}
}

func TestConsensus_InvalidRequest(t *testing.T) {
	p, _ := New(PipelineConfig{LLMClient: &FakeLLMClient{}})
	cases := []struct {
		name string
		req  ConsensusRequest
	}{
		{
			name: "missing vibe_case",
			req: ConsensusRequest{
				EvaluateRequest: EvaluateRequest{
					EvalType:   "drift_judge",
					SpecIntent: "long enough spec intent for the test",
				},
			},
		},
		{
			name: "missing eval_type",
			req: ConsensusRequest{
				EvaluateRequest: EvaluateRequest{
					VibeCase:   "C1",
					SpecIntent: "long enough spec intent for the test",
				},
			},
		},
		{
			name: "short spec_intent (EC-006)",
			req: ConsensusRequest{
				EvaluateRequest: EvaluateRequest{
					EvalType:   "drift_judge",
					VibeCase:   "C1",
					SpecIntent: "short",
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Consensus(context.Background(), p, tc.req)
			if err == nil {
				t.Error("expected error for invalid request")
			}
		})
	}
}

func TestConsensusWithSemaphore_RespectsConcurrency(t *testing.T) {
	// Verify the bounded semaphore actually limits concurrency.
	// We start 7 goroutines, limit to 2 concurrent, and verify no
	// more than 2 are in-flight at once.
	var inFlight int32
	var maxInFlight int32

	fake := &MultiFakeLLM{
		Responses: []string{alignedResponse(), alignedResponse(), alignedResponse(), alignedResponse(), alignedResponse(), alignedResponse(), alignedResponse()},
	}
	// Wrap to count in-flight.
	wrapped := &countingFake{inner: fake, inFlight: &inFlight, maxInFlight: &maxInFlight, delay: 30 * time.Millisecond}

	p := newConsensusPipeline(t, wrapped)
	_, err := ConsensusWithSemaphore(context.Background(), p, ConsensusRequest{
		EvaluateRequest: EvaluateRequest{
			EvalType:        "drift_judge",
			SpecIntent:      "evaluate this artifact for drift",
			VibeCase:        "C1",
			ArtifactContent: []byte("bounded concurrency test"),
		},
		N: 7,
	}, 2)
	if err != nil {
		t.Fatalf("ConsensusWithSemaphore: %v", err)
	}
	max := atomic.LoadInt32(&maxInFlight)
	if max > 2 {
		t.Errorf("max in-flight = %d; want <= 2 (semaphore capacity)", max)
	}
	if max < 1 {
		t.Errorf("max in-flight = %d; want >= 1", max)
	}
}

// ---------- Aggregation helpers ----------

func TestPickModal(t *testing.T) {
	cases := []struct {
		name   string
		counts map[string]int
		want   string
		count  int
	}{
		{"single aligned", map[string]int{VerdictAligned: 1}, VerdictAligned, 1},
		{"aligned wins", map[string]int{VerdictAligned: 3, VerdictDriftDetected: 1}, VerdictAligned, 3},
		{"drift wins", map[string]int{VerdictAligned: 1, VerdictDriftDetected: 3}, VerdictDriftDetected, 3},
		{"tie broken by priority (aligned > drift)", map[string]int{VerdictAligned: 2, VerdictDriftDetected: 2}, VerdictAligned, 2},
		{"empty", map[string]int{}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, count := pickModal(tc.counts)
			if label != tc.want {
				t.Errorf("label = %q; want %q", label, tc.want)
			}
			if count != tc.count {
				t.Errorf("count = %d; want %d", count, tc.count)
			}
		})
	}
}

func TestMeanAndStdDev(t *testing.T) {
	mean, stddev := meanAndStdDev([]float64{0.5, 0.5, 0.5})
	if mean != 0.5 {
		t.Errorf("mean = %f; want 0.5", mean)
	}
	if stddev != 0 {
		t.Errorf("stddev = %f; want 0 (constant input)", stddev)
	}

	mean, stddev = meanAndStdDev([]float64{0.6, 0.8})
	if mean != 0.7 {
		t.Errorf("mean = %f; want 0.7", mean)
	}
	if stddev < 0.13 || stddev > 0.15 {
		t.Errorf("stddev = %f; want ~0.141", stddev)
	}

	mean, stddev = meanAndStdDev([]float64{})
	if mean != 0 || stddev != 0 {
		t.Errorf("empty mean = %f, stddev = %f; want 0,0", mean, stddev)
	}

	mean, stddev = meanAndStdDev([]float64{0.42})
	if mean != 0.42 || stddev != 0 {
		t.Errorf("single sample: mean = %f, stddev = %f; want 0.42, 0", mean, stddev)
	}
}

func TestClamp01(t *testing.T) {
	if got := clamp01(-0.5); got != 0 {
		t.Errorf("clamp01(-0.5) = %f; want 0", got)
	}
	if got := clamp01(1.5); got != 1 {
		t.Errorf("clamp01(1.5) = %f; want 1", got)
	}
	if got := clamp01(0.42); got != 0.42 {
		t.Errorf("clamp01(0.42) = %f; want 0.42", got)
	}
}

func TestConsensusResult_VerdictDistribution(t *testing.T) {
	res := &ConsensusResult{
		Samples: []ConsensusSample{
			{Verdict: &Verdict{Verdict: VerdictAligned}},
			{Verdict: &Verdict{Verdict: VerdictAligned}},
			{Verdict: &Verdict{Verdict: VerdictDriftDetected}},
			{Verdict: &Verdict{Verdict: VerdictNeedsHuman}},
		},
	}
	dist := res.VerdictDistribution()
	if len(dist) != 3 {
		t.Fatalf("len(dist) = %d; want 3", len(dist))
	}
	if dist[0].Verdict != VerdictAligned || dist[0].Count != 2 {
		t.Errorf("dist[0] = %+v; want aligned with count 2", dist[0])
	}
}

// ---------- Test helpers ----------

// newConsensusPipeline builds a Pipeline wired to the given LLMClient.
func newConsensusPipeline(t *testing.T, llm LLMClient) *Pipeline {
	t.Helper()
	p, err := New(PipelineConfig{LLMClient: llm})
	if err != nil {
		t.Fatalf("New Pipeline: %v", err)
	}
	return p
}

// countingFake wraps a MultiFakeLLM to count in-flight calls. Used
// for the bounded-concurrency test.
type countingFake struct {
	inner       *MultiFakeLLM
	inFlight    *int32
	maxInFlight *int32
	delay       time.Duration
}

func (c *countingFake) Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error) {
	cur := atomic.AddInt32(c.inFlight, 1)
	defer atomic.AddInt32(c.inFlight, -1)
	// Record the maximum concurrent in-flight count. Use CAS to
	// avoid losing updates when multiple goroutines race.
	for {
		max := atomic.LoadInt32(c.maxInFlight)
		if cur <= max {
			break // already higher (or equal) — no update needed
		}
		if atomic.CompareAndSwapInt32(c.maxInFlight, max, cur) {
			break
		}
	}
	time.Sleep(c.delay)
	return c.inner.Complete(ctx, req)
}

// keep strings import alive in builds where unused
var _ = strings.TrimSpace
