// Tests for parseTasksField — the F36 dual-form tasks parser used by
// VibeSpec. The pre-INFRA-002 implementation swallowed the underlying
// json.Unmarshal error and returned only a generic ErrInvalidArgument
// pointing at field=tasks. These tests pin down the post-fix
// behaviour: every failure path must surface BOTH (a) which form was
// attempted (Form A / Form B / unknown) AND (b) the underlying error
// message from json.Unmarshal when one was emitted.
package orchestration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dark-agents/dark-memory-mcp/internal/store"
)

// TestParseTasksField_HappyFormA pins the canonical Form A shape:
// a bare JSON array.
func TestParseTasksField_HappyFormA(t *testing.T) {
	raw := json.RawMessage(`[{"id":"T1","description":"hello"}]`)
	tasks, err := parseTasksField(raw)
	if err != nil {
		t.Fatalf("Form A happy path rejected: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T1" {
		t.Fatalf("Form A happy path produced wrong tasks: %#v", tasks)
	}
}

// TestParseTasksField_HappyFormB pins the canonical Form B shape:
// a JSON string whose inner value is a JSON array. This is the
// legacy dark_research_spec_create compatibility shape.
func TestParseTasksField_HappyFormB(t *testing.T) {
	raw := json.RawMessage(`"[{\"id\":\"T1\",\"description\":\"hello\"}]"`)
	tasks, err := parseTasksField(raw)
	if err != nil {
		t.Fatalf("Form B happy path rejected: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T1" {
		t.Fatalf("Form B happy path produced wrong tasks: %#v", tasks)
	}
}

// TestParseTasksField_TrailingJunk_FormA is the regression test for
// INFRA-002. Pre-fix, a payload like `[{...}]·` (a trailing middle-dot
// character past the JSON close-bracket) returned a generic
// "invalid argument at field=tasks" with no clue about the trailing
// garbage byte. Post-fix, the error must name Form A AND include the
// underlying json.Unmarshal diagnostic.
//
// This shape is the exact payload pattern the operator reported in the
// initial report (2026-07-19). The literal middle-dot character was
// observed in the harness transport layer's parameter encoding.
func TestParseTasksField_TrailingJunk_FormA(t *testing.T) {
	// Form A with trailing garbage byte (middle dot, U+00B7).
	raw := json.RawMessage("[{\"id\":\"T1\",\"description\":\"x\"}]\u00b7")
	_, err := parseTasksField(raw)
	if err == nil {
		t.Fatalf("Form A with trailing junk accepted; expected ErrInvalidArgument")
	}

	// F35 wire-propagation: errors.As must still find FieldError
	// pointing at tasks (so ToolError.Field gets populated).
	var fe *store.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("F35 regression: errors.As(&FieldError) failed; got %T: %v", err, err)
	}
	if fe.Field != "tasks" {
		t.Fatalf("F35 regression: FieldError.Field = %q, want %q", fe.Field, "tasks")
	}

	// errors.Is must still match the ErrInvalidArgument sentinel
	// so the ToToolError dispatch lands in the right branch.
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("F35 regression: errors.Is(ErrInvalidArgument) failed")
	}

	// INFRA-002 fix: error message must BOTH name the form AND
	// surface the underlying json.Unmarshal diagnostic.
	msg := err.Error()
	if !strings.Contains(msg, "Form A") {
		t.Errorf("INFRA-002 fix: error %q must mention Form A so the operator knows which form was attempted", msg)
	}
	// json.Unmarshal error for trailing garbage typically reads
	// "invalid character '\u00b7' after top-level value" or similar.
	// We assert the cause is reachable via Error() rather than
	// pinning the exact phrasing (json package can vary across
	// versions).
	if !strings.Contains(msg, "invalid") && !strings.Contains(msg, "parse") {
		t.Errorf("INFRA-002 fix: error %q must surface the underlying parser diagnostic, not just the field name", msg)
	}
}

// TestParseTasksField_NotJSON_FormB pins the Form B path where the
// outer value parses as a JSON string but the inner value is not
// valid JSON (e.g. LLM emitted "[not-a-json-array]" wrapped in
// quotes).
func TestParseTasksField_NotJSON_FormB(t *testing.T) {
	raw := json.RawMessage(`"[not-an-array]"`)
	_, err := parseTasksField(raw)
	if err == nil {
		t.Fatalf("Form B with non-JSON inner accepted; expected ErrInvalidArgument")
	}
	var fe *store.FieldError
	if !errors.As(err, &fe) || fe.Field != "tasks" {
		t.Fatalf("F35 regression: %v", err)
	}
	if !strings.Contains(err.Error(), "Form B step 2") {
		t.Errorf("INFRA-002 fix: Form B inner-parse error must name the second step (inner array); got %q", err.Error())
	}
}

// TestParseTasksField_UnknownFirstByte covers the case where the
// harness emits a non-array/non-string/non-object shape (e.g. a
// bare number or boolean). The implementation must name the offending
// byte so the operator can see "first byte='1'" or similar and pivot.
//
// v2.15.2 INFRA-005: an object starting with '{' is now Form C, not
// "unknown form". This test exercises the numeric case which remains
// in the unknown-form branch.
func TestParseTasksField_UnknownFirstByte(t *testing.T) {
	// Bare number — neither Form A, B, nor C applies.
	raw := json.RawMessage(`42`)
	_, err := parseTasksField(raw)
	if err == nil {
		t.Fatalf("Number-shaped tasks accepted; expected ErrInvalidArgument")
	}
	var fe *store.FieldError
	if !errors.As(err, &fe) || fe.Field != "tasks" {
		t.Fatalf("F35 regression: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown form") {
		t.Errorf("INFRA-002 fix: unknown-shape error must say so explicitly; got %q", msg)
	}
	// The parser emits the first non-whitespace byte via %q, which
	// wraps it in double quotes (e.g. `"4"`).
	if !strings.Contains(msg, `"4"`) && !strings.Contains(msg, "'4'") {
		t.Errorf("INFRA-002 fix: unknown-shape error must name the first byte so the operator can identify the form; got %q", msg)
	}
}

// TestParseTasksField_HappyFormC covers the opencode 1.18.16 +
// Vercel AI SDK + Anthropic protocol shape from anthropics/claude-code
// issue #59227: the harness sends tasks as a JSON object wrapping an
// array under the "tasks" key. v2.15.2 INFRA-005 unwraps this.
func TestParseTasksField_HappyFormC(t *testing.T) {
	raw := json.RawMessage(`{"tasks":[{"id":"T1","description":"hello"}]}`)
	tasks, err := parseTasksField(raw)
	if err != nil {
		t.Fatalf("Form C happy path rejected: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T1" {
		t.Fatalf("Form C happy path produced wrong tasks: %#v", tasks)
	}
}

// TestParseTasksField_FormC_MissingTasksKey covers the case where the
// harness sends an object under Form C but without a "tasks" key.
// The parser must reject with a Form C-specific error, not "unknown
// form".
func TestParseTasksField_FormC_MissingTasksKey(t *testing.T) {
	raw := json.RawMessage(`{"id":"T1","description":"hello"}`)
	_, err := parseTasksField(raw)
	if err == nil {
		t.Fatalf("Form C without tasks key accepted; expected ErrInvalidArgument")
	}
	var fe *store.FieldError
	if !errors.As(err, &fe) || fe.Field != "tasks" {
		t.Fatalf("F35 regression: %v", err)
	}
	if !strings.Contains(err.Error(), "Form C") {
		t.Errorf("Form C missing-key error must mention Form C; got %q", err.Error())
	}
}

// TestParseTasksField_Empty keeps the empty-input contract intact
// after the wrapper change. Note: the empty path returns
// orchestrator-internal fieldError (NOT *store.FieldError); the only
// F35 contract that matters here is errors.Is(err, ErrInvalidArgument)
// — that's enough to land the error in the correct ToToolError
// branch for the operator.
func TestParseTasksField_Empty(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"nil", nil},
		{"whitespace-only", json.RawMessage("   ")},
		{"null", json.RawMessage(`null`)},
		{"empty-string-form-B", json.RawMessage(`""`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTasksField(tc.raw)
			if err == nil {
				t.Fatalf("%s accepted; expected ErrInvalidArgument", tc.name)
			}
			if !errors.Is(err, store.ErrInvalidArgument) {
				t.Fatalf("%s: errors.Is(ErrInvalidArgument) failed: %v", tc.name, err)
			}
		})
	}
}
