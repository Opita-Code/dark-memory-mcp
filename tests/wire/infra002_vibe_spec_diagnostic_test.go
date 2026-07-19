// INFRA-002 wire-conformance test (companion to f35/f36 in tests/wire/):
// dark_memory_vibe_spec MUST surface WHICH form was attempted (Form A,
// Form B step 1, Form B step 2) AND the underlying parser diagnostic
// when `tasks` is malformed. Pre-fix, every malformed payload returned
// the generic "invalid argument at field=tasks" with no clue why.
//
// This test pins the contract at the JSON-RPC wire level (H-3:
// wire-conformance is mandatory for every fix).
//
// Verified shapes:
//  1. tasks as Form A array with trailing garbage byte  → Form A named
//  2. tasks as Form B string of invalid JSON             → "Form B step 2"
//  3. tasks as a JSON object (neither form applies)      → "unknown form" + first byte
package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWire_INFRA002_ParseTasksFieldSurfacesFormAndCause verifies
// that every rejection path of parseTasksField names the form it
// tried AND, when json.Unmarshal is the failure source, includes the
// underlying diagnostic so the operator can act without grepping
// server logs.
//
// Reference: PRODUCTION_CHECKLIST.md R-3/R-4 (the "field=tasks"
// diagnostic gap that prompted this fix).
func TestWire_INFRA002_ParseTasksFieldSurfacesFormAndCause(t *testing.T) {
	s := startWireSession(t)
	if _, err := s.toolsCall("dark_memory_session_start", map[string]any{
		"operator":  "wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v", err)
	}

	// We share the helper for unwrapping structured errors with
	// the F35 test; see envelope.go.
	type trErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field"`
	}
	checkField := func(t *testing.T, caseName string, resp []byte, wantSubstrings []string) {
		t.Helper()
		// Outer envelope: must have isError=true.
		var outer toolResponseEnvelope
		if err := json.Unmarshal(resp, &outer); err != nil {
			t.Fatalf("%s: malformed outer response: %v raw=%s", caseName, err, respStr(resp))
		}
		if !outer.Result.IsError {
			t.Fatalf("%s: expected isError=true; content=%+v", caseName, outer.Result.Content)
		}
		// Inner envelope: must have the structured error.
		inner, err := unwrapToolResponse(t, resp)
		if err != nil {
			t.Fatalf("%s: unwrap inner: %v", caseName, err)
		}
		var e struct {
			Error trErr `json:"error"`
		}
		if err := json.Unmarshal(inner, &e); err != nil {
			t.Fatalf("%s: unmarshal inner: %v inner=%s", caseName, err, inner)
		}
		if e.Error.Code != "ErrInvalidArgument" {
			t.Fatalf("%s: code=%q, want ErrInvalidArgument", caseName, e.Error.Code)
		}
		if e.Error.Field != "tasks" {
			t.Fatalf("%s: field=%q, want tasks (F35 regression)", caseName, e.Error.Field)
		}
		msg := strings.ToLower(e.Error.Message)
		for _, want := range wantSubstrings {
			if !strings.Contains(msg, want) {
				t.Fatalf("%s: message %q must contain %q", caseName, e.Error.Message, want)
			}
		}
		t.Logf("%s OK: %s", caseName, e.Error.Message)
	}

	// Case 1: Form A with trailing garbage byte (middle dot, U+00B7).
	// The exact shape observed in the operator report on 2026-07-19.
	resp, err := s.toolsCall("dark_memory_vibe_spec", map[string]any{
		"vibe_case": "C1",
		"spec":      `{"intent":"INFRA-002 wire"}`,
		"tasks":     `[{"id":"T1","description":"x"}]·`, // trailing U+00B7
	})
	if err != nil {
		t.Fatalf("Case 1 transport error: %v resp=%s", err, respStr(resp))
	}
	checkField(t, "Form A trailing garbage", resp, []string{"form a", "invalid"})

	// Case 2: Form B whose inner JSON is not an array.
	resp, err = s.toolsCall("dark_memory_vibe_spec", map[string]any{
		"vibe_case": "C1",
		"spec":      `{"intent":"INFRA-002 wire"}`,
		"tasks":     `"[not-an-array]"`,
	})
	if err != nil {
		t.Fatalf("Case 2 transport error: %v resp=%s", err, respStr(resp))
	}
	checkField(t, "Form B inner non-JSON", resp, []string{"form b step 2"})

	// Case 3: tasks as an object — neither form applies.
	resp, err = s.toolsCall("dark_memory_vibe_spec", map[string]any{
		"vibe_case": "C1",
		"spec":      `{"intent":"INFRA-002 wire"}`,
		"tasks":     map[string]any{"id": "T1", "description": "x"},
	})
	if err != nil {
		t.Fatalf("Case 3 transport error: %v resp=%s", err, respStr(resp))
	}
	checkField(t, "Object-shaped (unknown form)", resp, []string{"unknown form", "{"})
}
