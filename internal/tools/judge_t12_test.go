// Package tools — judge_t12_test.go: schema tests for the
// single-shot judge tool after v2.20.0 T12 (spec 1276).
//
// Verifies that:
//   - artifact_ref is exposed in the schema.
//   - content is OPTIONAL (removed from required) when artifact_ref
//     is set.
//   - artifact_ref.kind enum is the canonical 5 kinds (file,
//     git_sha, url, spec_id, artifact_id).
//
// The test uses the live RegisterJudge → registry → Get("judge").
// InputSchema path. No store or orchestrator needed for the schema
// assertions (we pass nil for both).
package tools

import (
	"encoding/json"
	"testing"
)

// judgeSchemaT12 fetches the "judge" tool's InputSchema as a
// map[string]any so individual tests can assert on its fields.
// Panics on registration failure (a test setup error).
func judgeSchemaT12(t *testing.T) map[string]any {
	t.Helper()
	reg := NewRegistry()
	// nil orchestrator + nil store: RegisterJudge only calls
	// reg.Add, so the schema is populated without invoking any
	// orchestrator method.
	RegisterJudge(reg, nil, nil)
	tool := reg.Get("judge")
	if tool == nil {
		t.Fatal("judge tool not registered")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	return schema
}

// TestJudgeSchema_AcceptsArtifactRef — v2.20.0 T12 (spec 1276) adds
// artifact_ref to the single-shot judge schema. The schema must
// expose the field so callers can use the artifact-anchored path.
func TestJudgeSchema_AcceptsArtifactRef(t *testing.T) {
	schema := judgeSchemaT12(t)
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema must have properties object")
	}
	artifactRef, ok := props["artifact_ref"].(map[string]any)
	if !ok {
		t.Fatal("properties.artifact_ref must be an object")
	}
	if artifactRef["type"] != "object" {
		t.Errorf("artifact_ref.type: got %v, want object", artifactRef["type"])
	}
	// The artifact_ref.properties must include the kind field with
	// the canonical 5-kind enum.
	refProps, ok := artifactRef["properties"].(map[string]any)
	if !ok {
		t.Fatal("artifact_ref.properties must be an object")
	}
	if _, ok := refProps["kind"]; !ok {
		t.Fatal("artifact_ref.properties.kind must be present")
	}
	if _, ok := refProps["path"]; !ok {
		t.Fatal("artifact_ref.properties.path must be present")
	}
	if _, ok := refProps["git_sha"]; !ok {
		t.Fatal("artifact_ref.properties.git_sha must be present")
	}
	if _, ok := refProps["url"]; !ok {
		t.Fatal("artifact_ref.properties.url must be present")
	}
	if _, ok := refProps["spec_id"]; !ok {
		t.Fatal("artifact_ref.properties.spec_id must be present")
	}
	if _, ok := refProps["artifact_id"]; !ok {
		t.Fatal("artifact_ref.properties.artifact_id must be present")
	}
}

// TestJudgeSchema_ContentOptional — v2.20.0 T12 (spec 1276) makes
// content optional. Callers that supply artifact_ref for drift_judge
// no longer need to also supply content. The legacy path (non-
// drift_judge eval_types) still requires content per the field
// description, but the MCP schema can no longer enforce that
// distinction (the schema doesn't know the eval_type in advance).
// The orchestrator validates the eval_type-based requirement at
// runtime.
func TestJudgeSchema_ContentOptional(t *testing.T) {
	schema := judgeSchemaT12(t)
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema.required must be an array; got %T", schema["required"])
	}
	// Sanity check: eval_type is still required.
	hasEvalType := false
	hasContent := false
	for _, v := range required {
		if s, ok := v.(string); ok {
			if s == "eval_type" {
				hasEvalType = true
			}
			if s == "content" {
				hasContent = true
			}
		}
	}
	if !hasEvalType {
		t.Errorf("eval_type must be in required")
	}
	if hasContent {
		t.Errorf("content must NOT be in required (T12 makes it optional; artifact_ref is the new path for drift_judge)")
	}
}

// TestJudgeSchema_ArtifactRefKindEnum — artifact_ref.kind enum
// must be the canonical 5 kinds (file, git_sha, url, spec_id,
// artifact_id). The orchestrator's ArtifactRef.Validate() rejects
// unknown kinds, so the schema must match.
func TestJudgeSchema_ArtifactRefKindEnum(t *testing.T) {
	schema := judgeSchemaT12(t)
	props := schema["properties"].(map[string]any)
	artifactRef := props["artifact_ref"].(map[string]any)
	refProps := artifactRef["properties"].(map[string]any)
	kind := refProps["kind"].(map[string]any)
	enumRaw, ok := kind["enum"].([]any)
	if !ok {
		t.Fatalf("artifact_ref.kind.enum must be an array; got %T", kind["enum"])
	}
	got := make([]string, 0, len(enumRaw))
	for _, v := range enumRaw {
		s, ok := v.(string)
		if !ok {
			t.Errorf("enum entry %v is not a string", v)
			continue
		}
		got = append(got, s)
	}
	want := map[string]bool{
		"file": true, "git_sha": true, "url": true, "spec_id": true, "artifact_id": true,
	}
	if len(got) != len(want) {
		t.Errorf("artifact_ref.kind enum: got %v, want %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("kind enum contains unexpected %q (got %v)", g, got)
		}
		delete(want, g)
	}
	for missing := range want {
		t.Errorf("kind enum missing %q", missing)
	}
}

// TestJudgeSchema_ToolDescription_MentionsArtifactRef — the tool
// description must mention artifact_ref so LLM tool-pickers know
// about the new artifact-anchored path. Without this, the LLM
// would always default to content (the legacy path).
func TestJudgeSchema_ToolDescription_MentionsArtifactRef(t *testing.T) {
	reg := NewRegistry()
	RegisterJudge(reg, nil, nil)
	tool := reg.Get("judge")
	if tool == nil {
		t.Fatal("judge tool not registered")
	}
	if !containsSubstring(tool.Description, "artifact_ref") {
		t.Errorf("judge tool description must mention artifact_ref; got %q", tool.Description)
	}
}

// TestJudgeSchema_PreservesLegacyKeys — T12 added artifact_ref but
// must NOT remove any existing schema field. Verify the legacy keys
// (target_type, target_id, content, model, agent_id, no_enrich,
// vibe_case, persona_id, spec_intent) are all still present.
func TestJudgeSchema_PreservesLegacyKeys(t *testing.T) {
	schema := judgeSchemaT12(t)
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema must have properties object")
	}
	legacyKeys := []string{
		"eval_type", "target_type", "target_id", "content",
		"model", "agent_id", "no_enrich", "vibe_case",
		"persona_id", "spec_intent", "artifact_ref",
	}
	for _, k := range legacyKeys {
		if _, ok := props[k]; !ok {
			t.Errorf("legacy key %q missing from schema (T12 must preserve all existing fields)", k)
		}
	}
}

// TestJudgeSchema_ConsensusUntouched — T12 added artifact_ref to the
// single-shot judge. The consensus tool schema (T09) must be
// unchanged. Register both, then assert consensus still has its
// artifact_ref (T09's work) and that the single-shot registry entry
// does not collide with consensus.
func TestJudgeSchema_ConsensusUntouched(t *testing.T) {
	reg := NewRegistry()
	RegisterJudge(reg, nil, nil)
	consensus := reg.Get("consensus")
	if consensus == nil {
		t.Fatal("consensus tool not registered")
	}
	var schema map[string]any
	if err := json.Unmarshal(consensus.InputSchema, &schema); err != nil {
		t.Fatalf("consensus InputSchema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("consensus schema must have properties object")
	}
	if _, ok := props["artifact_ref"]; !ok {
		t.Errorf("consensus schema must still expose artifact_ref (T09's work)")
	}
}

// containsSubstring is a small helper to avoid pulling in strings for
// a one-shot substring check (the contents-helper in health_test.go
// is internal to that test file).
func containsSubstring(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
