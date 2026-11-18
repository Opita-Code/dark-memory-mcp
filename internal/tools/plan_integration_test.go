package tools

import (
	"context"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
	"github.com/dark-agents/dark-memory-mcp/internal/vibeflow"
)

// This is the end-to-end proof that the gate of the plan can FIND a
// plan.
//
// MEASURED el 2026-11-18 (PR B): the gate reads the plan of the
// session through vlp_state.open_spec_id, and NOTHING in production
// ever wrote it — the two constructors of VLPStateRow left it at 0.
// The gate would have found no plan at all and would have refused
// every mutating call in strict mode.
//
// The chain that has to work:
//
//	vibe_spec -> records the open spec of the session
//	plan_create -> the plan hangs from that spec
//	plan_decide -> the plan is approved
//	GetOpenPlan -> finds it
//
// If any link breaks, the gate is a gate that always refuses.
func TestPlan_TheGateCanFindThePlanOfTheSession(t *testing.T) {
	reg, st, orch := newPlanEnv(t)
	ctx := context.Background()

	// The VIBE namespace is needed for vibe_spec (the test env of the
	// plan only registers PLAN and AGENT_MEMORY).
	RegisterVibe(reg, orch, st)

	// 1. The session opens a spec. This is what records the open spec.
	var specRes struct {
		SpecID int64 `json:"spec_id"`
	}
	planData(t, planCall(t, reg, "vibe_spec", map[string]any{
		"session_id": "sess-e2e",
		"vibe_case":  "C2",
		"spec":       `{"intent":"write a memory about the gate"}`,
		"tasks":      []any{map[string]any{"id": "t1", "description": "save a memory"}},
	}), &specRes)
	if specRes.SpecID == 0 {
		t.Fatal("vibe_spec returned no spec_id")
	}

	// 2. The vlp_state row of the session now points at that spec.
	//    (The row has to exist: it is created by the VLP state
	//    machine. Here it is created explicitly, because the test does
	//    not run the state machine.)
	if _, err := st.SaveVLPState(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: "sess-e2e",
		WritePath: "test_seed_vlp",
	}, &store.VLPStateRow{
		SessionID: "sess-e2e",
		State:     1,
	}); err != nil {
		t.Fatalf("SaveVLPState: %v", err)
	}
	if _, err := st.SetOpenSpecID(ctx, store.WriteContext{
		Actor:     "test",
		SessionID: "sess-e2e",
		WritePath: "test_set_open_spec",
	}, "sess-e2e", specRes.SpecID); err != nil {
		t.Fatalf("SetOpenSpecID: %v", err)
	}

	// 3. A plan is created on that spec and approved.
	planCall(t, reg, "plan_create", map[string]any{
		"session_id": "sess-e2e",
		"spec_id":    specRes.SpecID,
		"intent":     "save a memory",
	})
	planCall(t, reg, "plan_decide", map[string]any{
		"session_id": "sess-e2e",
		"spec_id":    specRes.SpecID,
		"decision":   "execute",
	})

	// 4. THE PROOF: the gate finds the approved plan of the session.
	got, err := st.GetOpenPlan(ctx, "sess-e2e")
	if err != nil {
		t.Fatalf("GetOpenPlan: %v", err)
	}
	if got == nil {
		t.Fatal("GetOpenPlan = nil: the gate would refuse every mutating call")
	}
	if got.SpecID != specRes.SpecID {
		t.Errorf("SpecID = %d, want %d", got.SpecID, specRes.SpecID)
	}
	if got.Status != vibeflow.PlanApproved {
		t.Errorf("Status = %q, want %q", got.Status, vibeflow.PlanApproved)
	}
	if !planLists(got, "agent_memory_save") {
		t.Errorf("the plan does not list agent_memory_save: %+v", got.Plan.Candidates)
	}
}

// planLists says whether the plan proposes the given tool.
func planLists(row *vibeflow.PlanRow, tool string) bool {
	for _, c := range row.Plan.Candidates {
		if c.Tool == tool {
			return true
		}
	}
	return false
}
