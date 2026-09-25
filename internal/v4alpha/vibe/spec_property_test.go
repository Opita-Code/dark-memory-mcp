package vibe

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

// taskIDGen generates a non-empty task id drawn from a tight alphabet.
// We reuse the same generator across tests so cyclic/dangling mutations
// are reproducible.
func taskIDGen() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z][a-z0-9_]{2,15}`)
}

// validLinearChain builds a Spec with n tasks in a linear dependency
// chain t1 -> t2 -> ... -> tn. Returns the spec plus the underlying
// task IDs so property tests can mutate them.
func validLinearChain(n int) (*Spec, []string) {
	ids := make([]string, n)
	tasks := make([]Task, n)
	for i := 0; i < n; i++ {
		ids[i] = "t_" + intToA(i)
		t := Task{ID: ids[i], Description: "task " + intToA(i)}
		if i > 0 {
			t.DependsOn = []string{ids[i-1]}
		}
		tasks[i] = t
	}
	return &Spec{
		VibeCase: CaseC1,
		Intent:   "linear",
		Tasks:    tasks,
	}, ids
}

// intToA is a tiny base-26 encoder for generating stable task IDs.
func intToA(i int) string {
	if i < 26 {
		return string(rune('a' + i))
	}
	return intToA(i/26) + string(rune('a'+i%26))
}

// TestProperty_Spec_AnyLinearChainAccepted — universal claim:
// for any n in [1..20], a fresh linear-chain spec validates. This
// protects against regressions in the cycle detector (a buggy
// topological sort might reject some valid DAGs).
func TestProperty_Spec_AnyLinearChainAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 20).Draw(t, "n")
		s, _ := validLinearChain(n)
		if err := s.Validate(); err != nil {
			t.Fatalf("linear chain n=%d: expected nil, got %v", n, err)
		}
	})
}

// TestProperty_Spec_AnyDiamondGraphAccepted — universal claim:
// for any branching factor and any depth, a balanced DAG validates.
func TestProperty_Spec_AnyDiamondGraphAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		branching := rapid.IntRange(2, 5).Draw(t, "branching")
		s := &Spec{
			VibeCase: CaseC1,
			Intent:   "diamond",
			Tasks: []Task{
				{ID: "root", Description: "r"},
			},
		}
		for i := 0; i < branching; i++ {
			id := "leaf_" + intToA(i)
			s.Tasks = append(s.Tasks, Task{ID: id, Description: id, DependsOn: []string{"root"}})
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("diamond b=%d: expected nil, got %v", branching, err)
		}
	})
}

// TestProperty_Spec_CycleAlwaysDetected — universal claim:
// for any valid linear-chain spec, adding a back-edge from any
// earlier task to any later task closes a cycle and is always caught
// by ErrTaskCycle.
//
// Linear chain topology: t_0 -> t_1 -> ... -> t_{n-1}. Every t_i has
// a transitive path to t_j for j > i. So adding an edge from t_i to
// t_j (i < j) closes a cycle. The mutation we apply:
//   s.Tasks[i].DependsOn = append(s.Tasks[i].DependsOn, ids[j])
//
// (We do NOT mutate the validator — anti-pattern A5.)
func TestProperty_Spec_CycleAlwaysDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(3, 10).Draw(t, "n")
		s, ids := validLinearChain(n)
		i := rapid.IntRange(0, n-2).Draw(t, "i")
		j := rapid.IntRange(i+1, n-1).Draw(t, "j")
		// Add back-edge: t_i now depends on t_j (which t_i already
		// reaches transitively through the chain).
		s.Tasks[i].DependsOn = append(s.Tasks[i].DependsOn, ids[j])
		err := s.Validate()
		if !errors.Is(err, ErrTaskCycle) {
			t.Fatalf("back-edge (%d->%d) in n=%d: expected ErrTaskCycle, got %v",
				i, j, n, err)
		}
	})
}

// TestProperty_Spec_DanglingDepAlwaysDetected — universal claim:
// for any valid spec + any task, appending an unknown ID to its
// DependsOn is always caught by ErrDanglingDep.
func TestProperty_Spec_DanglingDepAlwaysDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "n")
		s, _ := validLinearChain(n)
		idx := rapid.IntRange(0, n-1).Draw(t, "idx")
		danglingID := rapid.StringMatching(`missing_[a-z0-9_]{4,16}`).Draw(t, "dangling")
		// Ensure the dangling ID does not collide with an existing one.
		for _, existing := range s.Tasks {
			if existing.ID == danglingID {
				t.Skip("dangling collides with existing id; rare in 20 iter")
			}
		}
		s.Tasks[idx].DependsOn = append(s.Tasks[idx].DependsOn, danglingID)
		err := s.Validate()
		if !errors.Is(err, ErrDanglingDep) {
			t.Fatalf("dangling dep %q on task %d: expected ErrDanglingDep, got %v",
				danglingID, idx, err)
		}
	})
}

// TestProperty_Spec_DuplicateIDAlwaysDetected — universal claim:
// for any valid spec + any task, renaming a second task to match
// the first's ID is always caught by ErrDuplicateTaskID.
func TestProperty_Spec_DuplicateIDAlwaysDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 8).Draw(t, "n")
		s, _ := validLinearChain(n)
		// Rename s.Tasks[n-1] to s.Tasks[0].ID.
		s.Tasks[n-1].ID = s.Tasks[0].ID
		err := s.Validate()
		if !errors.Is(err, ErrDuplicateTaskID) {
			t.Fatalf("rename to duplicate: expected ErrDuplicateTaskID, got %v", err)
		}
	})
}

// TestProperty_Spec_AllCanonicalCasesAccepted — universal claim:
// every canonical vibe_case (C1..C7) accepts any valid spec.
func TestProperty_Spec_AllCanonicalCasesAccepted(t *testing.T) {
	cases := []string{CaseC1, CaseC2, CaseC3, CaseC4, CaseC5, CaseC6, CaseC7}
	rapid.Check(t, func(t *rapid.T) {
		c := rapid.SampledFrom(cases).Draw(t, "vibe_case")
		s, _ := validLinearChain(rapid.IntRange(1, 5).Draw(t, "n"))
		s.VibeCase = c
		if err := s.Validate(); err != nil {
			t.Fatalf("canonical case %s: expected nil, got %v", c, err)
		}
	})
}

// TestProperty_Spec_UnknownVibeCaseAlwaysRejected — universal claim:
// any non-canonical vibe_case (drawn from a tight rejection alphabet)
// is always caught by ErrUnknownVibeCase.
func TestProperty_Spec_UnknownVibeCaseAlwaysRejected(t *testing.T) {
	rejections := []string{"", "C0", "C8", "C9", "c1", "code", "X", "?"}
	rapid.Check(t, func(t *rapid.T) {
		c := rapid.SampledFrom(rejections).Draw(t, "bad_case")
		s, _ := validLinearChain(3)
		s.VibeCase = c
		err := s.Validate()
		if !errors.Is(err, ErrUnknownVibeCase) {
			t.Fatalf("vibe_case %q: expected ErrUnknownVibeCase, got %v", c, err)
		}
	})
}
