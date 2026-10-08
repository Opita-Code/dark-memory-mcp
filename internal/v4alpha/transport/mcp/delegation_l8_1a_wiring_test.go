package mcp

// Phase 21 L8.1a — instrumentation wiring tests.
//
// The single most important test in this file is
// TestL8_1a_DecisionIsUnchanged. An instrumentation commit that
// accidentally changes a routing decision is worse than no
// instrumentation at all: it would silently change who pays for what.

import (
	"context"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/v4alpha/delegation"
)

func advise(t *testing.T, task, vibeCase string) (*DelegateIntentOutput, *TaskClassAdvisory, string) {
	t.Helper()
	out, err := RunDelegateIntentCore(
		context.Background(),
		DelegateIntentInput{
			TaskDescription: task,
			VibeCase:        vibeCase,
			Operator:        "test-op",
		},
		nil, nil, nil, nil, // no LLM, no cache, no memories, no emitter
	)
	if err != nil {
		t.Fatalf("RunDelegateIntentCore: %v", err)
	}
	return out, out.TaskClass, out.Decision
}

// THE LOAD-BEARING TEST. The advisory must not move Decision.
//
// It pins the invariant against the router's own rules: whatever
// DecideDelegation says for these inputs, the instrumented pipeline
// must say the same thing.
func TestL8_1a_DecisionIsUnchanged(t *testing.T) {
	cases := []struct {
		name     string
		task     string
		vibeCase string
	}{
		{"short task -> inline", "fix the typo in the header", "C1"},
		{"long task -> delegate",
			strings.Repeat("implement the caching layer ", 30), "C1"},
		{"coordination marker -> delegate",
			"refactor these three modules in parallel and then verify", "C1"},
		{"C7 multi -> delegate",
			strings.Repeat("do the thing ", 40), "C7"},
		{"C8 vibe-flow", "run the full vibe loop for the operator report", "C8"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, got := advise(t, tc.task, tc.vibeCase)
			want, _ := delegation.DecideDelegation(tc.vibeCase, tc.task)
			if got != want {
				t.Fatalf("DECISION CHANGED: instrumented pipeline said %q, router says %q",
					got, want)
			}
		})
	}
}

// The advisory is always present and always self-describing.
func TestL8_1a_AdvisoryIsPopulatedAndHonest(t *testing.T) {
	out, tc, _ := advise(t, "fix the typo in the header", "C1")
	if out.TaskClass == nil {
		t.Fatal("WIRING BROKEN: task_class absent from the production output")
	}
	if tc == nil {
		t.Fatal("advisory nil")
	}
	if tc.Class == "" {
		t.Error("class must never be empty")
	}
	if tc.Confidence < 0 || tc.Confidence > 1 {
		t.Errorf("confidence %v out of range 0..1", tc.Confidence)
	}
	// Downward-only epistemic labeling, same rule as L11.2: a
	// heuristic is modeled, never measured, never trusted as fact.
	if tc.Grade != "modeled" {
		t.Errorf("grade %q — a heuristic classifier is modeled, nothing more", tc.Grade)
	}
	if tc.Caveat == "" {
		t.Error("caveat must never be empty")
	}
	if !strings.Contains(tc.Caveat, "did NOT influence decision") {
		t.Errorf("caveat must state it did not influence the decision, got %q", tc.Caveat)
	}
	if !strings.Contains(tc.Caveat, "not yet wired into DECIDE") {
		t.Errorf("caveat must state DECIDE is not wired to it yet, got %q", tc.Caveat)
	}
	if tc.RouterThreshold != 200 {
		t.Errorf("router threshold %d must match router.go's literal", tc.RouterThreshold)
	}
}

// AgreesWithRouter must be nil, not false, when the router decided on
// something other than length. Claiming disagreement there would be a
// fabricated comparison between two things that were never compared.
func TestL8_1a_AgreesIsNilWhenNotLengthDriven(t *testing.T) {
	// Coordination marker: the router delegated on the marker, not on
	// length. A length-only router would have said inline. That is a
	// real behavioural difference, but reporting it as
	// "agrees_with_router=false" would misrepresent a comparison the
	// router never performed.
	_, tc, decision := advise(t, "refactor these three modules in parallel", "C1")
	if decision != "delegate" {
		t.Fatalf("expected delegate, got %q", decision)
	}
	if tc.AgreesWithRouter == nil {
		return // correct: not comparable
	}
	t.Fatalf("marker-driven decision must report agrees_with_router=nil, got %v",
		*tc.AgreesWithRouter)
}

// When DECIDE WAS length-driven, the comparison is meaningful and must
// be reported.
func TestL8_1a_AgreesIsComputedWhenLengthDriven(t *testing.T) {
	// Short, no markers: router went inline on length alone.
	_, shortTC, shortDec := advise(t, "fix the typo in the header", "C1")
	if shortDec != "inline" {
		t.Fatalf("expected inline, got %q", shortDec)
	}
	if shortTC.AgreesWithRouter == nil {
		t.Fatal("length-driven decision must produce a comparison")
	}
	if *shortTC.AgreesWithRouter != true {
		t.Error("short task + short threshold must agree")
	}

	// Long, no markers: router delegated on length alone.
	_, longTC, longDec := advise(t, strings.Repeat("implement the caching layer ", 30), "C1")
	if longDec != "delegate" {
		t.Fatalf("expected delegate, got %q", longDec)
	}
	if longTC.AgreesWithRouter == nil {
		t.Fatal("length-driven decision must produce a comparison")
	}
	if *longTC.AgreesWithRouter != true {
		t.Error("long task + length threshold must agree")
	}
}

// The advisory reaches the JSON a harness parses — a struct field that
// is not tagged or not set can pass a Go-level test and be absent from
// the tool output.
func TestL8_1a_AdvisoryReachesJSON(t *testing.T) {
	out, _, _ := advise(t, "fix the typo in the header", "C1")
	raw := mustJSON(out)
	for _, key := range []string{
		"task_class", "class", "confidence", "grade",
		"router_threshold", "caveat", "token_count",
	} {
		if !strings.Contains(raw, key) {
			t.Errorf("WIRING BROKEN: %q absent from the MCP JSON payload", key)
		}
	}
}

// Deterministic: same input, same advisory, byte for byte. The
// classifier must not introduce nondeterminism into a routing path.
func TestL8_1a_AdvisoryIsDeterministic(t *testing.T) {
	task := "run the full vibe loop and write the closeout report"
	_, a, _ := advise(t, task, "C8")
	_, b, _ := advise(t, task, "C8")
	if a.Class != b.Class || a.Confidence != b.Confidence || a.TokenCount != b.TokenCount {
		t.Fatalf("advisory is nondeterministic: %+v vs %+v", a, b)
	}
}
