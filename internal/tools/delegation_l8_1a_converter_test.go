package tools

// Phase 21 L8.1a — boundary test for the v4alpha -> v3 conversion.
//
// THIS TEST EXISTS BECAUSE THE PREVIOUS ONE DIDN'T.
//
// The L8.1a commit wired the canonical vibeflow classifier into
// RunDelegateIntentCore and shipped a set of wiring tests
// (delegation_l8_1a_wiring_test.go). Every one of them called
// RunDelegateIntentCore DIRECTLY and asserted on its
// DelegateIntentOutput. They all passed. The binary contained the
// "task_class" string. The deploy row 2566 was written claiming
// "L8.1a live in the process."
//
// None of that was true at the harness boundary.
//
// The production MCP tool `delegate_intent` is registered in this
// package. It calls RunDelegateIntentCore, then runs the result
// through convertV4AlphaToV3Output before returning. That converter
// did not copy TaskClass. So the live response — the only thing
// the operator's harness actually sees — arrived without the
// advisory, while every test, every binary check, and every commit
// message reported success.
//
// The fix is mechanical. The test that SHOULD have caught it is
// the one below: build a v4alpha output with a TaskClass, run it
// through the converter, assert the v3 output carries it. If a
// future change re-drops the field at the translation boundary,
// this test fails before the binary ships.

import (
	"encoding/json"
	"strings"
	"testing"

	v4mcpt "github.com/dark-agents/dark-memory-mcp/internal/v4alpha/transport/mcp"
)

// ptr is a tiny helper for building *bool literals in test data.
func ptr[T any](v T) *T { return &v }

// The single load-bearing test. If convertV4AlphaToV3Output drops
// TaskClass again, this fails.
func TestL8_1a_TaskClassSurvivesConversion(t *testing.T) {
	v4 := &v4mcpt.DelegateIntentOutput{
		Decision:  "delegate",
		Reasoning: "DECIDE: delegate — task is 350 chars.",
		Verdict:   "aligned",
		TaskClass: &v4mcpt.TaskClassAdvisory{
			Class:            "long",
			Confidence:       0.87,
			Grade:            "modeled",
			TokenCount:       62,
			MultiStep:        true,
			HasCode:          false,
			RouterThreshold:  200,
			AgreesWithRouter: ptr(true),
			Caveat:           "advisory only — did NOT influence decision",
		},
	}

	v3 := convertV4AlphaToV3Output(v4, "opita-ai")

	if v3.TaskClass == nil {
		t.Fatal("WIRING BROKEN (regression of 2026-10-08): the converter " +
			"dropped TaskClass. The inner pipeline computed it; the " +
			"v3 wire did not carry it. This is exactly the gap that " +
			"shipped yesterday and was caught by the end-to-end " +
			"delegate_intent call.")
	}

	// Field-by-field equality. A future change to either shape
	// surfaces here as a test failure, not a silent schema drift.
	got := v3.TaskClass
	if got.Class != "long" {
		t.Errorf("class: got %q, want long", got.Class)
	}
	if got.Confidence != 0.87 {
		t.Errorf("confidence: got %v, want 0.87", got.Confidence)
	}
	if got.Grade != "modeled" {
		t.Errorf("grade: got %q, want modeled", got.Grade)
	}
	if got.TokenCount != 62 {
		t.Errorf("token_count: got %d, want 62", got.TokenCount)
	}
	if got.MultiStep != true {
		t.Errorf("multi_step: got %v, want true", got.MultiStep)
	}
	if got.RouterThreshold != 200 {
		t.Errorf("router_threshold: got %d, want 200", got.RouterThreshold)
	}
	if got.AgreesWithRouter == nil || *got.AgreesWithRouter != true {
		t.Errorf("agrees_with_router: got %v, want pointer to true",
			got.AgreesWithRouter)
	}
	if got.Caveat == "" {
		t.Error("caveat must not be empty")
	}
}

// nil TaskClass must round-trip as nil (NOT as a zero-valued struct).
// The v3 wire uses omitempty, so a zero-value would still marshal to
// {} which is the wrong shape.
func TestL8_1a_NilTaskClassStaysNil(t *testing.T) {
	v4 := &v4mcpt.DelegateIntentOutput{Decision: "inline", Reasoning: "x"}
	v3 := convertV4AlphaToV3Output(v4, "opita-ai")
	if v3.TaskClass != nil {
		t.Fatalf("nil in must stay nil out, got %+v", v3.TaskClass)
	}
}

// End-to-end JSON check: the v3 output, marshaled, must contain
// "task_class" with the right shape. A test that only checked
// struct fields could miss a missing JSON tag.
func TestL8_1a_TaskClassReachesJSON(t *testing.T) {
	v4 := &v4mcpt.DelegateIntentOutput{
		Decision:  "delegate",
		Reasoning: "r",
		Verdict:   "aligned",
		TaskClass: &v4mcpt.TaskClassAdvisory{
			Class:            "short",
			Confidence:       0.4,
			Grade:            "modeled",
			TokenCount:       5,
			RouterThreshold:  200,
			AgreesWithRouter: ptr(false),
			Caveat:           "advisory only",
		},
	}
	v3 := convertV4AlphaToV3Output(v4, "opita-ai")
	raw, err := json.Marshal(v3)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		"task_class", "class", "confidence", "grade",
		"token_count", "router_threshold", "agrees_with_router", "caveat",
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("key %q absent from v3 JSON payload: %s", key, raw)
		}
	}
}
