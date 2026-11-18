package sqlite

import (
	"context"
	"database/sql"
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

	// A session whose spec has NO plan has no open plan.
	//
	// MEDIDO el 2026-11-18 (PR B): this test used to seed a spec WITH
	// a plan and assert that GetOpenPlan returned nil, on the premise
	// that "no vlp_state row means no open spec means no open plan".
	// That premise was FICTION: nothing in production ever wrote
	// vlp_state.open_spec_id (the two constructors of VLPStateRow
	// left it at 0), so the gate of the plan would have found no plan
	// at all and would have refused every mutating call in strict
	// mode.
	//
	// The intent of the test is kept and the premise is fixed: the
	// case that matters is a spec WITHOUT a plan, and there is no
	// plan to find. The case of a spec WITH a plan and no
	// open_spec_id is covered by
	// TestGetOpenPlan_ResolvesTheOpenSpecOfTheSession (the fallback).
	specID := seedSpec(t, s, "sess-open")

	got, err := s.GetOpenPlan(ctx, "sess-open")
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got != nil {
		t.Errorf("GetOpenPlan = %+v, want nil (the spec has no plan)", got)
	}
	_ = specID
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

// T5 asked for a migration test and there was none: the columns were
// declared in the migration and never checked. This one checks three
// things: that the eight columns EXIST, that their DEFAULTS put a
// pre-existing row in the safest possible state, and that a row
// created before the plan existed reads back as a draft plan with
// zero counters.
func TestPlanColumns_MigrationV33(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()

	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info(vibe_specs)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(vibe_specs): %v", err)
	}
	defer rows.Close()

	type colInfo struct {
		name    string
		deflt   sql.NullString
		notNull bool
	}
	cols := map[string]colInfo{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = colInfo{name: name, deflt: dflt, notNull: notNull == 1}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := []string{
		"plan_json", "plan_status", "plan_decision", "plan_decided_by",
		"plan_decided_at", "plan_tier", "plan_budget_used", "plan_judge_used",
	}
	for _, w := range want {
		if _, ok := cols[w]; !ok {
			t.Errorf("column %q is missing: the v33 migration did not create it", w)
		}
	}
	if len(want) != 8 {
		t.Fatalf("the expectation itself lists %d columns; the plan has eight", len(want))
	}

	// The default of plan_status has to be 'draft': a row that
	// predates the plan must land in the SAFEST state (a plan exists,
	// nobody approved it). A default of 'approved' would let a
	// pre-existing row authorise calls.
	if got := cols["plan_status"].deflt.String; got != "'draft'" {
		t.Errorf("plan_status default = %q, want %q", got, "'draft'")
	}
	// And the counters start at zero.
	for _, c := range []string{"plan_budget_used", "plan_judge_used"} {
		if got := cols[c].deflt.String; got != "0" {
			t.Errorf("%s default = %q, want %q", c, got, "0")
		}
	}

	// A row created without touching the plan columns reads back as a
	// draft plan with zero counters.
	specID := seedSpec(t, s, "sess-legacy")
	got, err := s.GetPlan(ctx, specID)
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got != nil {
		t.Errorf("GetPlan = %+v, want nil: a spec with no plan has no plan", got)
	}
	var status string
	var used, judge int
	if err := s.db.QueryRowContext(ctx,
		`SELECT plan_status, plan_budget_used, plan_judge_used FROM vibe_specs WHERE id = ?`,
		specID).Scan(&status, &used, &judge); err != nil {
		t.Fatalf("read the plan columns: %v", err)
	}
	if status != "draft" {
		t.Errorf("plan_status = %q, want %q", status, "draft")
	}
	if used != 0 || judge != 0 {
		t.Errorf("counters = %d/%d, want 0/0", used, judge)
	}
}

// GetOpenPlan reads the plan of the OPEN spec of the session. The
// negative case was covered (no open spec); the positive one was not:
// the path that the gate will actually use is the positive one.
func TestGetOpenPlan_Positive(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	specID := seedSpec(t, s, "sess-open2")
	seedPlan(t, s, specID, 3, 1)

	// The session opens that spec.
	if _, err := s.SaveVLPState(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: "sess-open2",
		WritePath: "test_seed_vlp",
	}, &store.VLPStateRow{
		SessionID:  "sess-open2",
		State:      1,
		OpenSpecID: specID,
	}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	got, err := s.GetOpenPlan(ctx, "sess-open2")
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got == nil {
		t.Fatal("GetOpenPlan = nil, want the plan of the open spec")
	}
	if got.SpecID != specID {
		t.Errorf("SpecID = %d, want %d", got.SpecID, specID)
	}
	if got.Status != vibeflow.PlanApproved {
		t.Errorf("Status = %q, want %q", got.Status, vibeflow.PlanApproved)
	}
}

// MEASURED el 2026-11-18 (PR B): GetOpenPlan leans on
// vlp_state.open_spec_id, and NOTHING in production ever wrote it —
// the two constructors of VLPStateRow left it at 0. So the gate of
// the plan would have found no plan at all.
//
// This test fixes the two halves of the fix:
//
//  1. SetOpenSpecID records the open spec (the root fix: vibe_spec
//     calls it).
//  2. GetOpenPlan falls back to the most recent spec of the session
//     that has a plan, for the rows that were written before the
//     fix.
func TestGetOpenPlan_ResolvesTheOpenSpecOfTheSession(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	sessionID := "sess-open-resolve"
	specID := seedSpec(t, s, sessionID)
	seedPlan(t, s, specID, 3, 1)

	// A vlp_state row for the session, WITHOUT the open spec (the
	// state that production was in).
	if _, err := s.SaveVLPState(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: sessionID,
		WritePath: "test_seed_vlp",
	}, &store.VLPStateRow{
		SessionID: sessionID,
		State:     1,
	}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	// The fallback finds the plan even though open_spec_id is 0.
	got, err := s.GetOpenPlan(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got == nil {
		t.Fatal("GetOpenPlan = nil: the plan of the session was not found")
	}
	if got.SpecID != specID {
		t.Errorf("SpecID = %d, want %d", got.SpecID, specID)
	}

	// And the root fix: SetOpenSpecID records it, and the first
	// path (the explicit answer) then wins.
	otherSpec := seedSpec(t, s, sessionID)
	seedPlan(t, s, otherSpec, 3, 1)
	ok, err := s.SetOpenSpecID(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: sessionID,
		WritePath: "test_set_open_spec",
	}, sessionID, otherSpec)
	if err != nil {
		t.Fatalf("SetOpenSpecID: %v", err)
	}
	if !ok {
		t.Fatal("SetOpenSpecID reported no row updated")
	}
	got, err = s.GetOpenPlan(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got == nil || got.SpecID != otherSpec {
		t.Fatalf("GetOpenPlan = %+v, want the spec %d recorded by SetOpenSpecID", got, otherSpec)
	}
}

// The fallback picks the MOST RECENT spec with a plan: that is the
// one the session is working on, not the first one it opened.
func TestGetOpenPlan_FallbackPicksTheMostRecentPlan(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	sessionID := "sess-most-recent"

	first := seedSpec(t, s, sessionID)
	seedPlan(t, s, first, 3, 1)
	second := seedSpec(t, s, sessionID)
	seedPlan(t, s, second, 3, 1)

	got, err := s.GetOpenPlan(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got == nil {
		t.Fatal("GetOpenPlan = nil")
	}
	if got.SpecID != second {
		t.Errorf("SpecID = %d, want %d (the most recent)", got.SpecID, second)
	}
}

// A spec WITHOUT a plan does not become the open plan: the fallback
// looks for a spec that HAS one.
func TestGetOpenPlan_FallbackSkipsSpecsWithoutAPlan(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	sessionID := "sess-with-plan-then-without"

	withPlan := seedSpec(t, s, sessionID)
	seedPlan(t, s, withPlan, 3, 1)
	_ = seedSpec(t, s, sessionID) // a newer spec, with no plan

	got, err := s.GetOpenPlan(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got == nil {
		t.Fatal("GetOpenPlan = nil")
	}
	if got.SpecID != withPlan {
		t.Errorf("SpecID = %d, want %d (the one WITH a plan)", got.SpecID, withPlan)
	}
}

// SetOpenSpecID touches ONE column: it must not clobber the VLP
// state machine. A SaveVLPState upsert of the whole row would have
// done that.
func TestSetOpenSpecID_DoesNotClobberTheVLPState(t *testing.T) {
	s := newPlanStore(t)
	ctx := context.Background()
	sessionID := "sess-vlp-state"

	if _, err := s.SaveVLPState(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: sessionID,
		WritePath: "test_seed_vlp",
	}, &store.VLPStateRow{
		SessionID: sessionID,
		State:     7,
		TurnCount: 3,
		LastEvent: "vibe_publish",
	}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}

	specID := seedSpec(t, s, sessionID)
	if _, err := s.SetOpenSpecID(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: sessionID,
		WritePath: "test_set_open_spec",
	}, sessionID, specID); err != nil {
		t.Fatalf("SetOpenSpecID: %v", err)
	}

	state, err := s.GetVLPState(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetVLPState: %v", err)
	}
	if state == nil {
		t.Fatal("GetVLPState = nil")
	}
	if state.State != 7 {
		t.Errorf("State = %d, want 7: SetOpenSpecID clobbered the VLP state", state.State)
	}
	if state.TurnCount != 3 {
		t.Errorf("TurnCount = %d, want 3: SetOpenSpecID clobbered the VLP state", state.TurnCount)
	}
	if state.LastEvent != "vibe_publish" {
		t.Errorf("LastEvent = %q, want %q: SetOpenSpecID clobbered the VLP state", state.LastEvent, "vibe_publish")
	}
	if state.OpenSpecID != specID {
		t.Errorf("OpenSpecID = %d, want %d", state.OpenSpecID, specID)
	}
}
