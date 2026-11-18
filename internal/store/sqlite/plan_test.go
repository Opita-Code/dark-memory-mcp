package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dark-agents/dark-memory-mcp/internal/project"
	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// SPEC-194 T6: the persistence of the plan.
//
// The load-bearing test here is TestChargePlanCall_Atomic: the budget
// is the one thing in the plan that MUST NOT be raced, because a
// double-spend means the gate's promise ("the budget cuts") is a lie.

// newPlanStore creates a Store on a fresh SQLite DB (the production
// Open() path, so the DB is at the latest schema — v33 with the plan
// columns).
func newPlanStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	cfg := store.Config{
		Driver:      store.DriverSQLite,
		DSN:         filepath.Join(tmp, "plan-test.db"),
		WALMode:     true,
		ForeignKeys: true,
		BusyTimeout: 5 * time.Second,
	}
	st, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.CreateProject(ctx, &project.Project{ProjectID: "default", DisplayName: "Default"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.SetActiveProject(ctx, "default"); err != nil {
		t.Fatalf("SetActiveProject: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.(*Store)
}

func seedSpec(t *testing.T, s *Store, sessionID string) int64 {
	t.Helper()
	id, err := s.SaveSpec(context.Background(), store.WriteContext{
		Actor:     "test",
		SessionID: sessionID,
		WritePath: "test_seed",
	}, &vibeflow.Spec{
		VibeCase:  "C2",
		SessionID: sessionID,
		Spec:      `{"intent":"write a doc"}`,
		Tasks:     `[{"id":"t1","description":"do it"}]`,
	})
	if err != nil {
		t.Fatalf("SaveSpec: %v", err)
	}
	return id
}

func seedPlan(t *testing.T, s *Store, specID int64, maxTool, maxJudge int) {
	t.Helper()
	plan := vibeflow.Plan{
		Intent: "write a doc",
		Candidates: []vibeflow.Candidate{
			{Tool: "agent_memory_save", Weight: 3.2, Rationale: "writes the result", ScoreSource: vibeflow.ScoreSourceBM25},
			{Tool: "judge", Weight: 1.1, Rationale: "validates the shortlist", ScoreSource: vibeflow.ScoreSourceBM25},
		},
		PlanTier: vibeflow.PlanTierShort,
		Budget:   vibeflow.Budget{MaxToolCalls: maxTool, MaxJudgeCalls: maxJudge},
	}
	err := s.SavePlan(context.Background(), store.WriteContext{
		Actor:     "test",
		WritePath: "test_seed_plan",
	}, specID, &vibeflow.PlanRow{
		SpecID:    specID,
		SessionID: "sess-test",
		Plan:      plan,
		Status:    vibeflow.PlanApproved,
		Decision:  vibeflow.DecisionExecute,
	})
	if err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
}

func TestSavePlan_RoundTripsTheWholePlan(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")
	seedPlan(t, s, specID, 3, 1)

	got, err := s.GetPlan(ctx, specID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got == nil {
		t.Fatal("GetPlan returned nil: the plan was saved")
	}
	if got.Status != vibeflow.PlanApproved {
		t.Errorf("Status = %q, want %q", got.Status, vibeflow.PlanApproved)
	}
	if got.Decision != vibeflow.DecisionExecute {
		t.Errorf("Decision = %q, want %q", got.Decision, vibeflow.DecisionExecute)
	}
	if len(got.Plan.Candidates) != 2 {
		t.Fatalf("Candidates = %d, want 2", len(got.Plan.Candidates))
	}
	if got.Plan.Candidates[0].Tool != "agent_memory_save" {
		t.Errorf("candidate[0].Tool = %q, want agent_memory_save", got.Plan.Candidates[0].Tool)
	}
	if got.Plan.PlanTier != vibeflow.PlanTierShort {
		t.Errorf("PlanTier = %q, want %q", got.Plan.PlanTier, vibeflow.PlanTierShort)
	}
	if got.Plan.Budget.MaxToolCalls != 3 || got.Plan.Budget.MaxJudgeCalls != 1 {
		t.Errorf("Budget = %+v, want max 3 tool / 1 judge", got.Plan.Budget)
	}
	if got.BudgetUsed != 0 {
		t.Errorf("BudgetUsed = %d, want 0", got.BudgetUsed)
	}
}

func TestSavePlan_RefusesWithoutASpec(t *testing.T) {
	s := newPlanStore(t)
	err := s.SavePlan(context.Background(), store.WriteContext{
		Actor: "test", WritePath: "test",
	}, 999999, &vibeflow.PlanRow{Plan: vibeflow.Plan{Intent: "x"}})
	if err == nil {
		t.Fatal("SavePlan on a non-existent spec returned nil error: a plan without a spec is an orphan")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestGetPlan_NoPlanIsNotAnError(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")

	got, err := s.GetPlan(ctx, specID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got != nil {
		t.Errorf("GetPlan = %+v, want nil: 'no plan' is a normal state, not an error", got)
	}
}

func TestChargePlanCall_IncrementsAndReportsTheNewCount(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")
	seedPlan(t, s, specID, 3, 1)

	after, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, false)
	if err != nil {
		t.Fatalf("ChargePlanCall: %v", err)
	}
	if after.BudgetUsed != 1 {
		t.Errorf("BudgetUsed = %d, want 1", after.BudgetUsed)
	}
	// The effective budget merges the plan's maximums with the
	// store's authoritative counter.
	b := after.EffectiveBudget()
	if b.UsedToolCalls != 1 || b.MaxToolCalls != 3 {
		t.Errorf("EffectiveBudget = %+v, want used 1 / max 3", b)
	}
}

func TestChargePlanCall_RefusesWhenExhausted(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")
	seedPlan(t, s, specID, 1, 1)

	if _, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, false); err != nil {
		t.Fatalf("first ChargePlanCall: %v", err)
	}
	_, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, false)
	if err == nil {
		t.Fatal("ChargePlanCall past the budget returned nil error")
	}
	if !errors.Is(err, store.ErrBudgetExceeded) {
		t.Errorf("err = %v, want ErrBudgetExceeded", err)
	}
}

func TestChargePlanCall_JudgeBudgetIsSeparate(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")
	// 1 tool call allowed, 1 judge call allowed.
	seedPlan(t, s, specID, 1, 1)

	if _, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, false); err != nil {
		t.Fatalf("tool ChargePlanCall: %v", err)
	}
	// The tool budget is now spent, but the judge budget is not: a
	// judge call must still work, because the two resources are
	// distinct.
	if _, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, true); err != nil {
		t.Fatalf("judge ChargePlanCall: %v", err)
	}
	// And now the judge budget is spent too.
	if _, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, true); err == nil {
		t.Fatal("second judge ChargePlanCall returned nil error: the judge budget was exhausted")
	}
}

// AC11: the counter is ATOMIC. Two concurrent calls must not both
// spend the last unit of budget.
//
// The test is deliberately hostile: it starts N goroutines against a
// budget of 1 and asserts that exactly ONE succeeds. A read-then-write
// implementation would let several through, and the gate's promise
// ("the budget cuts") would be a lie.
func TestChargePlanCall_Atomic(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")
	seedPlan(t, s, specID, 1, 1)

	const attempts = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	budgetErrors := 0
	otherErrors := 0

	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them all at once
			_, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, false)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, store.ErrBudgetExceeded):
				budgetErrors++
			default:
				otherErrors++
			}
		}()
	}
	close(start)
	wg.Wait()

	if otherErrors != 0 {
		t.Errorf("%d calls failed with something other than ErrBudgetExceeded", otherErrors)
	}
	if okCount != 1 {
		t.Errorf("%d calls succeeded, want exactly 1: the budget of 1 was spent %d times", okCount, okCount)
	}
	if budgetErrors != attempts-1 {
		t.Errorf("%d calls refused with ErrBudgetExceeded, want %d", budgetErrors, attempts-1)
	}

	// And the stored counter agrees: it must be exactly 1.
	got, err := s.GetPlan(ctx, specID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.BudgetUsed != 1 {
		t.Errorf("BudgetUsed = %d, want 1", got.BudgetUsed)
	}
}

func TestChargePlanCall_RefusesAnUnapprovedPlan(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-test")

	// A DRAFT plan: nobody approved it, so nothing may be charged.
	err := s.SavePlan(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, &vibeflow.PlanRow{
		SpecID: specID,
		Plan: vibeflow.Plan{
			Intent: "x",
			Budget: vibeflow.Budget{MaxToolCalls: 3, MaxJudgeCalls: 1},
		},
		Status: vibeflow.PlanDraft,
	})
	if err != nil {
		t.Fatalf("SavePlan: %v", err)
	}
	if _, err := s.ChargePlanCall(ctx, store.WriteContext{Actor: "test", WritePath: "test"}, specID, false); err == nil {
		t.Fatal("ChargePlanCall on a draft plan returned nil error: an unapproved plan must not spend budget")
	}
}

func TestGetOpenPlan(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-open")
	seedPlan(t, s, specID, 3, 1)

	// With no vlp_state row, there is no open spec, so no open plan.
	got, err := s.GetOpenPlan(ctx, "sess-open")
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got != nil {
		t.Errorf("GetOpenPlan = %+v, want nil (no open spec)", got)
	}
}

func TestPlanRow_EffectiveBudgetMergesTheAuthoritativeCounter(t *testing.T) {
	r := &vibeflow.PlanRow{
		Plan: vibeflow.Plan{
			Budget: vibeflow.Budget{MaxToolCalls: 8, MaxJudgeCalls: 2, UsedToolCalls: 99},
		},
		BudgetUsed: 3,
	}
	b := r.EffectiveBudget()
	if b.UsedToolCalls != 3 {
		t.Errorf("UsedToolCalls = %d, want 3 (the store's counter wins over the document's snapshot)", b.UsedToolCalls)
	}
	if b.MaxToolCalls != 8 {
		t.Errorf("MaxToolCalls = %d, want 8 (the maximum lives in the document)", b.MaxToolCalls)
	}
}
