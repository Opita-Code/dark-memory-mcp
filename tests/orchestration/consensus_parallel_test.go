// consensus_parallel_test.go — v2.11.0 update tests for the
// concurrent JudgeConsensus: samples run in parallel goroutines and
// partial failure degrades instead of aborting.
//
// Determinism strategy: mocks vary responses by call order (atomic
// counters) and assertions only depend on the response SET (modal
// counts, means, stddevs are order-independent), never on which
// goroutine got which sample. The concurrency test uses a barrier so
// it provably demonstrates parallel execution without timing flakes.
package orchestration_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/orchestration"
)

// TestJudgeConsensus_PartialFailureDegrades verifies that when one
// sample of N fails, the consensus still returns a verdict computed
// from the survivors, flagged Degraded with the failed index.
func TestJudgeConsensus_PartialFailureDegrades(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_partial",
		VerdictJSON: `{"aligned":true,"confidence":0.9}`,
		Confidence:  0.9,
		Model:       "mock",
	}
	// Make the 2nd invocation fail (sample index 1).
	orch.WithLLMSelector(orchestration.NewOSINTSelector(
		&flakyLLMClient{MockLLMClient: mock, failOnCall: 2},
	))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType:   "compliance_check",
		TargetType: "artifact",
		TargetID:   "degraded-test",
		Content:    "some content",
		N:          3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus should degrade, not fail: %v", err)
	}
	if !out.Degraded {
		t.Fatalf("Degraded should be true (one sample failed)")
	}
	// Exactly one sample failed; WHICH index is nondeterministic
	// under parallelism (the flaky wrapper fails the 2nd invocation,
	// not a fixed sample), so assert the set: one failed index in
	// [0,3) and 2 survivors with the other indices.
	if len(out.FailedSampleIndices) != 1 {
		t.Fatalf("FailedSampleIndices should have exactly 1 entry, got %v", out.FailedSampleIndices)
	}
	if out.FailedSampleIndices[0] < 0 || out.FailedSampleIndices[0] >= 3 {
		t.Fatalf("failed index out of range: %v", out.FailedSampleIndices)
	}
	failedSet := map[int]bool{out.FailedSampleIndices[0]: true}
	if len(out.Samples) != 2 {
		t.Fatalf("Samples should have 2 survivors, got %d", len(out.Samples))
	}
	for _, s := range out.Samples {
		if s.SampleIndex < 0 || s.SampleIndex >= 3 || failedSet[s.SampleIndex] {
			t.Fatalf("survivor index %d overlaps failed set or is out of range", s.SampleIndex)
		}
	}
	// 2 aligned of REQUESTED 3 → fraction 0.67 → aligned survives.
	if out.ModalVerdict != "aligned" || out.ModalCount != 2 {
		t.Fatalf("modal: want aligned 2/3, got %s %d/3", out.ModalVerdict, out.ModalCount)
	}
	if out.ModalFraction < 0.66 || out.ModalFraction > 0.67 {
		t.Fatalf("ModalFraction should be ~0.667, got %f", out.ModalFraction)
	}
	if out.Verdict != "aligned" {
		t.Fatalf("Verdict should be aligned (2/3 >= 0.6), got %q", out.Verdict)
	}
	if !strings.Contains(out.Reasoning, "DEGRADED") {
		t.Fatalf("Reasoning should mention DEGRADED, got: %s", out.Reasoning)
	}
	// The flaky wrapper consumed the failing call itself — only the 2
	// surviving samples reached the wrapped mock.
	if mock.Calls != 2 {
		t.Fatalf("LLM should have been called 2 times (2 survivors), got %d", mock.Calls)
	}
}

// TestJudgeConsensus_PartialFailureLowFraction verifies the safety
// property: 1 success of 3 → fraction 0.33 < 0.6 → needs_human.
func TestJudgeConsensus_PartialFailureLowFraction(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{
		Name_:       "mock_low",
		VerdictJSON: `{"aligned":true,"confidence":0.9}`,
		Confidence:  0.9,
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(&flakyLLMClient{MockLLMClient: mock, failFromCall: 2}))

	out, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "compliance_check", TargetType: "artifact", TargetID: "low-fraction",
		Content: "x", N: 3,
	})
	if err != nil {
		t.Fatalf("JudgeConsensus should degrade, not fail: %v", err)
	}
	if !out.Degraded {
		t.Fatal("Degraded should be true")
	}
	if out.ModalFraction < 0.33 || out.ModalFraction > 0.34 {
		t.Fatalf("ModalFraction should be ~0.333 (1/3), got %f", out.ModalFraction)
	}
	if out.Verdict != "needs_human" {
		t.Fatalf("Verdict should be needs_human (1/3 < 0.6), got %q", out.Verdict)
	}
	if out.NextAction != "human_gate" {
		t.Fatalf("NextAction should be human_gate, got %q", out.NextAction)
	}
}

// TestJudgeConsensus_AllFailErrors verifies the all-samples-failed
// case surfaces an explicit error instead of a bogus verdict.
func TestJudgeConsensus_AllFailErrors(t *testing.T) {
	ctx := context.Background()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	mock := &orchestration.MockLLMClient{Name_: "mock_dead", Err: orchestration.ErrNoLLMAvailable}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(mock))

	_, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
		EvalType: "compliance_check", TargetType: "artifact", TargetID: "all-fail",
		Content: "x", N: 3,
	})
	if err == nil {
		t.Fatal("expected error when all samples fail")
	}
	if !strings.Contains(err.Error(), "all 3 samples failed") {
		t.Fatalf("error should mention all samples failed, got: %v", err)
	}
}

// TestJudgeConsensus_RunsInParallel proves concurrency without
// timing flakes: every sample must ARRIVE at the mock before any of
// them is released. Sequential execution would deadlock (the first
// sample would block on the release channel with no one left to
// release it) and the test fails on the arrival timeout.
func TestJudgeConsensus_RunsInParallel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orch, s := openOrchestratorTestEnv(t)
	if err := s.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("set: %v", err)
	}

	const n = 3
	arrived := make(chan struct{}, n)
	release := make(chan struct{})
	var once sync.Once
	mock := &orchestration.MockLLMClient{Name_: "mock_barrier"}
	barrier := &barrierLLMClient{
		MockLLMClient: mock,
		onArrive: func() {
			arrived <- struct{}{}
			once.Do(func() {
				// Wait for all n arrivals, then release everyone.
				for i := 0; i < n; i++ {
					select {
					case <-arrived:
					case <-ctx.Done():
						return
					}
				}
				close(release)
			})
			<-release
		},
	}
	orch.WithLLMSelector(orchestration.NewOSINTSelector(barrier))

	done := make(chan error, 1)
	go func() {
		_, err := orch.JudgeConsensus(ctx, orchestration.JudgeConsensusInput{
			EvalType: "compliance_check", TargetType: "artifact", TargetID: "parallel-proof",
			Content: "x", N: n,
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("JudgeConsensus: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("consensus did not complete — samples did not run in parallel (barrier never released)")
	}
}

// flakyLLMClient fails the Nth invocation of the wrapped mock
// (failOnCall) or every invocation from failFromCall on.
type flakyLLMClient struct {
	*orchestration.MockLLMClient
	failOnCall    int32 // fail exactly this call (0 = off)
	failFromCall  int32 // fail every call >= this (0 = off)
	callCount     int32
}

func (f *flakyLLMClient) Judge(ctx context.Context, req orchestration.JudgeRequest) (*orchestration.JudgeResponse, error) {
	c := atomic.AddInt32(&f.callCount, 1)
	if (f.failOnCall != 0 && c == f.failOnCall) || (f.failFromCall != 0 && c >= f.failFromCall) {
		return nil, orchestration.ErrNoLLMAvailable
	}
	return f.MockLLMClient.Judge(ctx, req)
}

// barrierLLMClient blocks every Judge call until ALL n calls have
// arrived, then releases them — a deterministic parallel-execution
// proof.
type barrierLLMClient struct {
	*orchestration.MockLLMClient
	onArrive func()
}

func (b *barrierLLMClient) Judge(ctx context.Context, req orchestration.JudgeRequest) (*orchestration.JudgeResponse, error) {
	b.onArrive()
	return b.MockLLMClient.Judge(ctx, req)
}
