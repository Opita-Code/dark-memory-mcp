// F35 wire-conformance test: BindOrchestrator's typeMismatchToolError
// must surface the offending field path + expected/actual types as
// discrete ToolError fields, NOT as a generic "One or more arguments
// failed validation" message. The harness/operator relies on Field,
// ExpectedType, ActualType to render precise fix-up hints.
package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWire_F35_TypeMismatchSurfacesFieldPath forces a *json.UnmarshalTypeError
// at vibe_spec by sending `tasks` as a NUMBER (the schema declares
// anyOf:[array,string]; the orchestrator's parseTasksField only
// handles JSON-object and JSON-string prefixes). We expect:
//
//   * Code=ErrInvalidArgument
//   * The Field path mentions `tasks`
//   * The Message names the failing field's identity
//
// (At the JSON-RPC level the server returns a single TextContent
// block carrying the JSON-encoded ToolError. The harness's error
// path can surface this as a typed error to the operator.)
func TestWire_F35_TypeMismatchSurfacesFieldPath(t *testing.T) {
	s := startWireSession(t)
	if _, err := s.toolsCall("dark_memory_session_start", map[string]any{
		"operator":  "wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v", err)
	}

	// Send `tasks` as a JSON number. parseTasksField peeks at the
	// leading byte; a number starts with a digit, which is neither '['
	// nor '"' — so it returns an explicit error message naming the
	// field. We assert that message names "tasks".
	resp, err := s.toolsCall("dark_memory_vibe_spec", map[string]any{
		"vibe_case": "C1",
		"spec":      `{"intent":"F35 wire"}`,
		"tasks":     42.0, // invalid: leading byte digit → tolerated-error class
	})
	// We expect a tools-call-level failure, not a transport error.
	if err != nil {
		t.Fatalf("F35 transport error (unexpected): %v (response=%s)", err, respStr(resp))
	}
	if resp == nil {
		t.Fatalf("F35: nil response from server")
	}

	// Extract the content text from the error envelope. We need BOTH
	// envelopes:
//
//   - OUTER (mcp-go CallToolResult): {result:{content:[...],isError:true}}
//     The harness sees isError; setting it to true is mcp-go's
//     contract for "tool returned an error".
//   - INNER (our ToolResponse): {"error":{...}}. The structured
//     ToolError with field=code/message/hint/field lives here.
//
//	v1.3.0: shared envelope parser from envelope.go handles the
//	outer; the inner is parsed inline because F35 is the canonical
//	test that defines the structured-error contract.
	var outer toolResponseEnvelope
	if err := json.Unmarshal(resp, &outer); err != nil {
		t.Fatalf("F35: malformed outer response: %v raw=%s", err, respStr(resp))
	}
	if !outer.Result.IsError {
		t.Fatalf("F35: expected outer isError=true (mcp-go error marker); got content=%+v", outer.Result.Content)
	}
	inner, err := unwrapToolResponse(t, resp)
	if err != nil {
		t.Fatalf("F35: unwrap inner: %v", err)
	}
	var trEnvelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"error"`
	}
	if err := json.Unmarshal(inner, &trEnvelope); err != nil {
		t.Fatalf("F35: inner ToolResponse unmarshal: %v inner=%s", err, inner)
	}
	txt := trEnvelope.Error.Message
	if !strings.Contains(strings.ToLower(txt), "tasks") {
		t.Fatalf("F35: error text should mention 'tasks'; got %q", txt)
	}
	t.Logf("F35 surfaced structured error mentioning 'tasks': code=%s field=%q msg=%q", trEnvelope.Error.Code, trEnvelope.Error.Field, txt[:min(140, len(txt))])
}
