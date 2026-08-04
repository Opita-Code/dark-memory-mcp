// Package delegation — router_test.go: DECIDE-stage acceptance
// tests for the DelegationRouter (Wave 5C, spec 728).
package delegation

import (
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/vibecase"
)

func TestRouter_C7_Mixed_AlwaysDelegates(t *testing.T) {
	r := NewRouter()
	dec := r.Decide(RouterInput{
		Case: vibecase.CaseMixed,
		Task: "campaña: imagen hero + copy de venta + código de landing",
	})
	if dec.Handler != HandlerDelegate {
		t.Fatalf("C7 handler = %s, want DELEGATE", dec.Handler)
	}
	if dec.Plan == nil {
		t.Fatal("C7 decision must carry a plan")
	}
	if len(dec.Plan.Subtasks) == 0 {
		t.Fatal("C7 plan must have at least one subtask")
	}
	if dec.Plan.Subtasks[0].VibeCase != "C7" {
		t.Errorf("bundle subtask vibe_case = %q, want C7", dec.Plan.Subtasks[0].VibeCase)
	}
	if dec.Plan.Subtasks[0].Task != "campaña: imagen hero + copy de venta + código de landing" {
		t.Errorf("bundle subtask task mismatch: %q", dec.Plan.Subtasks[0].Task)
	}
}

func TestRouter_UnknownCase_DefaultsToHandle(t *testing.T) {
	r := NewRouter()
	dec := r.Decide(RouterInput{
		Case: vibecase.CaseCode, // C1 — conditional, MVP defaults to HANDLE
		Task: "refactor auth module",
	})
	if dec.Handler != HandlerHandle {
		t.Fatalf("C1 handler = %s, want HANDLE (MVP fallback)", dec.Handler)
	}
	if dec.Plan != nil {
		t.Fatal("HANDLE decision must not carry a plan")
	}
	if dec.Reasoning == "" {
		t.Error("decision must carry reasoning")
	}
}

func TestRouter_Text_DefaultsToHandle(t *testing.T) {
	r := NewRouter()
	dec := r.Decide(RouterInput{
		Case: vibecase.CaseText, // C2 — conditional, MVP defaults to HANDLE
		Task: "documentar el endpoint de chat",
	})
	if dec.Handler != HandlerHandle {
		t.Fatalf("C2 handler = %s, want HANDLE (MVP fallback)", dec.Handler)
	}
}

func TestRouter_C3_RefusesWithoutProvider(t *testing.T) {
	r := NewRouter()
	// Capabilities known: image_generate NOT granted → grounded REFUSE.
	dec := r.Decide(RouterInput{
		Case:         vibecase.CaseImage,
		Task:         "generar hero image",
		GrantedTools: []string{"agent_memory_save", "mindset_apply"},
	})
	if dec.Handler != HandlerRefuse {
		t.Fatalf("C3 without provider handler = %s, want REFUSE", dec.Handler)
	}
	if dec.Plan != nil {
		t.Fatal("REFUSE decision must not carry a plan")
	}
}

func TestRouter_C3_DelegatesWithProvider(t *testing.T) {
	r := NewRouter()
	dec := r.Decide(RouterInput{
		Case:         vibecase.CaseImage,
		Task:         "generar hero image",
		GrantedTools: []string{"image_generate"},
	})
	if dec.Handler != HandlerDelegate {
		t.Fatalf("C3 with provider handler = %s, want DELEGATE", dec.Handler)
	}
	if dec.Plan == nil || len(dec.Plan.Subtasks) != 1 {
		t.Fatal("C3 delegate plan must have exactly 1 subtask")
	}
}

func TestRouter_C3_UnknownCapabilitiesDefaultsToHandle(t *testing.T) {
	r := NewRouter()
	// Capabilities UNKNOWN (empty grant list): cannot ground a
	// refusal → safe HANDLE (per router contract).
	dec := r.Decide(RouterInput{
		Case: vibecase.CaseImage,
		Task: "generar hero image",
	})
	if dec.Handler != HandlerHandle {
		t.Fatalf("C3 unknown caps handler = %s, want HANDLE", dec.Handler)
	}
}

func TestRouter_C7_EmptyTask_Handles(t *testing.T) {
	r := NewRouter()
	dec := r.Decide(RouterInput{
		Case: vibecase.CaseMixed,
		Task: "   ",
	})
	if dec.Handler != HandlerHandle {
		t.Fatalf("C7 empty task handler = %s, want HANDLE", dec.Handler)
	}
}

func TestPlan_ParallelDispatch(t *testing.T) {
	// C7: all subtasks independent → single batch (parallel).
	p := &Plan{
		Subtasks: []SubTask{
			{ID: "t1", VibeCase: "C3", Task: "imagen"},
			{ID: "t2", VibeCase: "C2", Task: "copy"},
			{ID: "t3", VibeCase: "C1", Task: "landing"},
		},
	}
	batches := p.Batch(nil)
	if len(batches) != 1 {
		t.Fatalf("C7 parallel dispatch: got %d batches, want 1", len(batches))
	}
	if len(batches[0]) != 3 {
		t.Errorf("batch 0 has %d subtasks, want 3", len(batches[0]))
	}
}

func TestPlan_TopologicalBatching(t *testing.T) {
	p := &Plan{
		Subtasks: []SubTask{
			{ID: "t1", VibeCase: "C2", Task: "script"},
			{ID: "t2", VibeCase: "C3", Task: "storyboard", DependsOn: []string{"t1"}},
			{ID: "t3", VibeCase: "C4", Task: "render", DependsOn: []string{"t2"}},
		},
	}
	batches := p.Batch(nil)
	if len(batches) != 3 {
		t.Fatalf("sequential pipeline: got %d batches, want 3", len(batches))
	}
	if len(batches[0]) != 1 || batches[0][0].ID != "t1" {
		t.Errorf("batch 0 = %+v, want [t1]", batches[0])
	}
	if len(batches[1]) != 1 || batches[1][0].ID != "t2" {
		t.Errorf("batch 1 = %+v, want [t2]", batches[1])
	}
	if len(batches[2]) != 1 || batches[2][0].ID != "t3" {
		t.Errorf("batch 2 = %+v, want [t3]", batches[2])
	}
}

func TestPlan_MixedDependencies(t *testing.T) {
	p := &Plan{
		Subtasks: []SubTask{
			{ID: "t1", VibeCase: "C2", Task: "copy"},
			{ID: "t2", VibeCase: "C3", Task: "imagen"},
			{ID: "t3", VibeCase: "C1", Task: "landing", DependsOn: []string{"t2"}},
		},
	}
	batches := p.Batch(nil)
	if len(batches) != 2 {
		t.Fatalf("mixed deps: got %d batches, want 2", len(batches))
	}
	// Batch 0: t1 + t2 (parallel). Batch 1: t3.
	if len(batches[0]) != 2 {
		t.Errorf("batch 0 has %d subtasks, want 2", len(batches[0]))
	}
	if len(batches[1]) != 1 || batches[1][0].ID != "t3" {
		t.Errorf("batch 1 = %+v, want [t3]", batches[1])
	}
}

func TestPlan_Validate_OK(t *testing.T) {
	p := &Plan{
		Subtasks: []SubTask{
			{ID: "a", VibeCase: "C1", Task: "x"},
			{ID: "b", VibeCase: "C2", Task: "y", DependsOn: []string{"a"}},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestPlan_Validate_Errors(t *testing.T) {
	cases := []struct {
		name string
		plan *Plan
	}{
		{"empty id", &Plan{Subtasks: []SubTask{{ID: "", VibeCase: "C1", Task: "x"}}}},
		{"duplicate id", &Plan{Subtasks: []SubTask{{ID: "a", VibeCase: "C1"}, {ID: "a", VibeCase: "C2"}}}},
		{"bad case", &Plan{Subtasks: []SubTask{{ID: "a", VibeCase: "C9"}}}},
		{"self dep", &Plan{Subtasks: []SubTask{{ID: "a", VibeCase: "C1", DependsOn: []string{"a"}}}}},
		{"unknown dep", &Plan{Subtasks: []SubTask{{ID: "a", VibeCase: "C1", DependsOn: []string{"zzz"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.plan.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error (%s)", tc.name)
			}
		})
	}
}

func TestPlan_Batch_CircularDependency(t *testing.T) {
	// Defensive: a hand-built cyclic plan must not loop forever.
	p := &Plan{
		Subtasks: []SubTask{
			{ID: "a", VibeCase: "C1", DependsOn: []string{"b"}},
			{ID: "b", VibeCase: "C1", DependsOn: []string{"a"}},
		},
	}
	batches := p.Batch(nil)
	if len(batches) != 1 {
		t.Fatalf("cycle: got %d batches, want 1 (defensive escape)", len(batches))
	}
}
