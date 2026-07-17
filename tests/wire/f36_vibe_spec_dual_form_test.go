// F36 wire-conformance test: dark_memory_vibe_spec must accept BOTH
// shapes for `tasks`:
//
//   A. JSON array of task objects (preference)
//   B. JSON-encoded string of an array (legacy dark_research_spec_create compat)
//
// The harness (opencode + Vercel AI SDK + the LLM) decides which
// shape goes on the wire. We can't force either, so we MUST accept
// both. This test pins that contract by feeding each shape through
// the real JSON-RPC wire and asserting a non-error response.
package wire

import (
	"encoding/json"
	"testing"
)

// TasksArrayFormSpec is a representative spec used by both forms.
const tasksTaskVibeSpec = `{"intent":"wire F36 dual-form","priority":"normal"}`

func TestWire_F36_VibeSpecAcceptsTasksAsArray(t *testing.T) {
	s := startWireSession(t)

	// Establish a session (required for project-scoped writes).
	if _, err := s.toolsCall("dark_memory_session_start", map[string]any{
		"operator":  "wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v", err)
	}

	// Form A: tasks as a JSON array of objects.
	resp, err := s.toolsCall("dark_memory_vibe_spec", map[string]any{
		"vibe_case": "C1",
		"spec":      tasksTaskVibeSpec,
		"tasks": []map[string]any{
			{"id": "T1", "description": "array-form-first", "depends_on": []string{}},
		},
	})
	if err != nil {
		t.Fatalf("F36 array form rejected at the wire: %v (response=%s)", err, respStr(resp))
	}
	assertNoToolError(t, "tasks=array", resp)
}

func TestWire_F36_VibeSpecAcceptsTasksAsStringifiedArray(t *testing.T) {
	s := startWireSession(t)
	if _, err := s.toolsCall("dark_memory_session_start", map[string]any{
		"operator":  "wire-test",
		"project_id": "default",
	}); err != nil {
		t.Fatalf("session_start: %v", err)
	}

	// Form B: tasks as a JSON-encoded string of an array. This is
	// the shape that dark_research_spec_create (the gemela in
	// dark-research-mcp) persists when storing `tasks`. LLMs that
	// have absorbed dark-research's history have been observed to
	// emit this shape against vibe_spec too.
	stringified := `[{"id":"T1","description":"string-form-first","depends_on":[]}]`
	resp, err := s.toolsCall("dark_memory_vibe_spec", map[string]any{
		"vibe_case": "C1",
		"spec":      tasksTaskVibeSpec,
		"tasks":     stringified,
	})
	if err != nil {
		t.Fatalf("F36 string-form rejected at the wire: %v (response=%s)", err, respStr(resp))
	}
	assertNoToolError(t, "tasks=stringified", resp)
}

// assertNoToolError verifies the response is not an MCP error envelope.
func assertNoToolError(t *testing.T, form string, resp []byte) {
	t.Helper()
	var env struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("F36/%s: malformed response: %v raw=%s", form, err, respStr(resp))
	}
	if env.Error != nil {
		t.Fatalf("F36/%s: top-level error envelope: %s", form, env.Error)
	}
	if env.Result.IsError {
		t.Fatalf("F36/%s: tool reported isError=true (content=%+v)", form, env.Result.Content)
	}
}

func respStr(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "..."
	}
	return string(b)
}
