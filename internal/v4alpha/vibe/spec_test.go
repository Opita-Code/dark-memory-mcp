package vibe

import (
	"errors"
	"strings"
	"testing"
)

// validSpec returns a minimal 3-task linear spec that passes Validate.
// Used as the base for mutation: each test starts from a valid spec
// and breaks one invariant to assert the corresponding sentinel error.
func validSpec() *Spec {
	return &Spec{
		VibeCase: CaseC1,
		Intent:   "ship v4-alpha vibe-loop",
		Tasks: []Task{
			{ID: "t1", Description: "design"},
			{ID: "t2", Description: "implement", DependsOn: []string{"t1"}},
			{ID: "t3", Description: "deploy", DependsOn: []string{"t2"}},
		},
	}
}

func TestExample_Spec_ValidLinearGraphAccepted(t *testing.T) {
	s := validSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid 3-task linear graph: expected nil, got %v", err)
	}
}

func TestExample_Spec_ValidDiamondGraphAccepted(t *testing.T) {
	// Diamond: t1 -> {t2, t3} -> t4. Tests cycle detection does
	// not false-positive on DAGs with shared ancestors.
	s := &Spec{
		VibeCase: CaseC2,
		Intent:   "diamond",
		Tasks: []Task{
			{ID: "t1", Description: "root"},
			{ID: "t2", Description: "left", DependsOn: []string{"t1"}},
			{ID: "t3", Description: "right", DependsOn: []string{"t1"}},
			{ID: "t4", Description: "join", DependsOn: []string{"t2", "t3"}},
		},
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("diamond DAG: expected nil, got %v", err)
	}
}

func TestExample_Spec_RejectsUnknownVibeCase(t *testing.T) {
	s := validSpec()
	s.VibeCase = "C9"
	err := s.Validate()
	if !errors.Is(err, ErrUnknownVibeCase) {
		t.Fatalf("unknown vibe_case C9: expected ErrUnknownVibeCase, got %v", err)
	}
}

func TestExample_Spec_RejectsEmptyVibeCase(t *testing.T) {
	s := validSpec()
	s.VibeCase = ""
	err := s.Validate()
	if !errors.Is(err, ErrUnknownVibeCase) {
		t.Fatalf("empty vibe_case: expected ErrUnknownVibeCase, got %v", err)
	}
}

func TestExample_Spec_RejectsEmptyIntent(t *testing.T) {
	s := validSpec()
	s.Intent = ""
	err := s.Validate()
	if !errors.Is(err, ErrEmptyIntent) {
		t.Fatalf("empty intent: expected ErrEmptyIntent, got %v", err)
	}
}

func TestExample_Spec_RejectsEmptyTasks(t *testing.T) {
	s := validSpec()
	s.Tasks = nil
	err := s.Validate()
	if !errors.Is(err, ErrEmptyTasks) {
		t.Fatalf("empty tasks: expected ErrEmptyTasks, got %v", err)
	}
}

func TestExample_Spec_RejectsEmptyTaskID(t *testing.T) {
	s := validSpec()
	s.Tasks[1].ID = ""
	err := s.Validate()
	if !errors.Is(err, ErrEmptyTaskID) {
		t.Fatalf("empty task id: expected ErrEmptyTaskID, got %v", err)
	}
}

func TestExample_Spec_RejectsEmptyTaskDescription(t *testing.T) {
	s := validSpec()
	s.Tasks[0].Description = ""
	err := s.Validate()
	if !errors.Is(err, ErrEmptyTaskDesc) {
		t.Fatalf("empty task desc: expected ErrEmptyTaskDesc, got %v", err)
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Fatalf("error should mention task id t1, got: %v", err)
	}
}

func TestExample_Spec_RejectsDuplicateTaskID(t *testing.T) {
	s := validSpec()
	s.Tasks[2].ID = "t1" // duplicate of s.Tasks[0]
	err := s.Validate()
	if !errors.Is(err, ErrDuplicateTaskID) {
		t.Fatalf("duplicate task id: expected ErrDuplicateTaskID, got %v", err)
	}
	if !strings.Contains(err.Error(), "t1") {
		t.Fatalf("error should mention duplicated id t1, got: %v", err)
	}
}

func TestExample_Spec_RejectsDanglingDep(t *testing.T) {
	s := validSpec()
	s.Tasks[1].DependsOn = []string{"t-missing"}
	err := s.Validate()
	if !errors.Is(err, ErrDanglingDep) {
		t.Fatalf("dangling dep: expected ErrDanglingDep, got %v", err)
	}
	if !strings.Contains(err.Error(), "t-missing") {
		t.Fatalf("error should mention missing target, got: %v", err)
	}
}

func TestExample_Spec_RejectsSelfDep(t *testing.T) {
	s := validSpec()
	s.Tasks[0].DependsOn = []string{"t1"} // depends on itself
	err := s.Validate()
	if !errors.Is(err, ErrSelfDep) {
		t.Fatalf("self-dep: expected ErrSelfDep, got %v", err)
	}
}

func TestExample_Spec_RejectsCycle(t *testing.T) {
	s := &Spec{
		VibeCase: CaseC1,
		Intent:   "cycle",
		Tasks: []Task{
			{ID: "a", Description: "first", DependsOn: []string{"b"}},
			{ID: "b", Description: "second", DependsOn: []string{"a"}},
		},
	}
	err := s.Validate()
	if !errors.Is(err, ErrTaskCycle) {
		t.Fatalf("cycle a->b->a: expected ErrTaskCycle, got %v", err)
	}
}

func TestExample_Spec_RejectsLongerCycle(t *testing.T) {
	// Three-node cycle: a -> b -> c -> a.
	s := &Spec{
		VibeCase: CaseC1,
		Intent:   "three-cycle",
		Tasks: []Task{
			{ID: "a", Description: "alpha", DependsOn: []string{"c"}},
			{ID: "b", Description: "beta", DependsOn: []string{"a"}},
			{ID: "c", Description: "gamma", DependsOn: []string{"b"}},
		},
	}
	err := s.Validate()
	if !errors.Is(err, ErrTaskCycle) {
		t.Fatalf("cycle a->b->c->a: expected ErrTaskCycle, got %v", err)
	}
}

func TestExample_Spec_AllowsNoDependsOn(t *testing.T) {
	// Tasks with no DependsOn should validate fine (independent
	// tasks are a valid DAG).
	s := &Spec{
		VibeCase: CaseC3,
		Intent:   "no-deps",
		Tasks: []Task{
			{ID: "t1", Description: "first"},
			{ID: "t2", Description: "second"},
			{ID: "t3", Description: "third"},
		},
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("no-depends_on graph: expected nil, got %v", err)
	}
}

func TestExample_Spec_AcceptsAllCanonicalCases(t *testing.T) {
	// Each canonical C1..C7 must validate when intent+tasks are valid.
	for _, c := range []string{CaseC1, CaseC2, CaseC3, CaseC4, CaseC5, CaseC6, CaseC7} {
		s := validSpec()
		s.VibeCase = c
		if err := s.Validate(); err != nil {
			t.Fatalf("canonical case %s: expected nil, got %v", c, err)
		}
	}
}

func TestExample_IsValidVibeCase_AcceptsCanonical(t *testing.T) {
	for _, c := range []string{CaseC1, CaseC2, CaseC3, CaseC4, CaseC5, CaseC6, CaseC7} {
		if !IsValidVibeCase(c) {
			t.Fatalf("canonical %s should be valid", c)
		}
	}
}

func TestExample_IsValidVibeCase_RejectsUnknown(t *testing.T) {
	for _, c := range []string{"", "C0", "C8", "c1", "X", "code"} {
		if IsValidVibeCase(c) {
			t.Fatalf("non-canonical %q should be rejected", c)
		}
	}
}
